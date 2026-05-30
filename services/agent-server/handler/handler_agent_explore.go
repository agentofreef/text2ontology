package handler

// handler_agent_explore.go — explore-mode chat handler. Drives an LLM
// conversation whose tool surface is `lookup` + `smartquery` (reused from the
// lakehouse tools) plus a third tool `commit_card` that signals convergence on
// a reusable metric definition. When the LLM calls `commit_card`, the handler:
//
//  1. parse-validates the proposed BARE-SQL via sqlrewrite.ParseBareMetricSQL,
//  2. pre-creates a `mark=false` row in lakehouse_metric (so the frontend has a
//     stable id to PUT to on user 采纳),
//  3. inserts trigger keywords in the same tx,
//  4. mints a fresh draftId,
//  5. emits a single SSE event `{"type":"commit_card", ...}` carrying the
//     CommitCardPayload.
//
// AC-10 invariant: every commit_card payload's querySql passes ParseBareMetricSQL.
// AC-11 hardening: when the LLM client is the test Fixture, the Args["name"]
// passed in by the script flows straight into payload.Name — combined with
// Fixture.ToolCallsServed > 0 this defeats stub-bypass gaming.
//
// Phase 4a stub gate: EXPLORE_PHASE_4A_STUB=true switches to a deterministic
// codepath that synthesises a commit_card from the most recent smartquery
// result without consulting the LLM. Default off; AC-11 (d) discriminates 4a
// (no FIXTURE_LLM_DETERMINISTIC_ prefix) from 4b (prefix present).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/lakehouse2ontology/contracts"
	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/ontology"
	"github.com/lakehouse2ontology/services/agent-server/smartquery"

	. "github.com/lakehouse2ontology/httputil"
)

// exploreMaxRounds caps the explore LLM loop so a model that never converges
// can't burn unlimited tokens / time. Eight rounds matches the lakehouse loop's
// practical ceiling for tool-heavy turns.
const exploreMaxRounds = 8

// commitCardToolDescription is the LLM-facing description for the explore-mode
// `commit_card` tool. The LLM emits a STRUCTURED spec — it does NOT write SQL.
// The server compiles the SQL deterministically via the SmartQuery engine,
// which is the project invariant ("LLM selects from finite sets; a compiler
// emits SQL"). Column names are resolved against the OD's properties server-
// side (Chinese display_name OR English name both accepted).
const commitCardToolDescription = `提交本轮收敛得到的口径（metric）定义。你只描述「结构」，**不要写 SQL** —— 服务端用确定性引擎编译 SQL。

仅当：已通过 lookup 探明 OD/字段，且用户已确认这是他要的口径时调用。否则继续对话或调 lookup。

先判断 intent：
- "aggregate"（度量/数字：多少/总数/求和/平均/占比/排名）：填 measure，dimensions 是分组维度（可空）。
- "enumerate"（列举：有哪些/列出/哪些种类）：填 dimensions（要列出的列），不填 measure。

参数：
- name (snake_case 机器名)
- displayName (人类可读名)
- primaryOd (OD 名，来自 lookup)
- intent ("aggregate" | "enumerate")
- measure {agg, column}：agg ∈ SUM/COUNT/AVG/MIN/MAX/COUNT_DISTINCT；column 是列名（COUNT 计数全部时可省略 → COUNT(*)）。仅 aggregate 需要。
- dimensions (列名数组：aggregate 的分组维度 / enumerate 要列出的列)
- filters (可选过滤数组，每项 {prop, op, value}；op ∈ =,!=,>,<,>=,<=,like,in)
- triggerKeywords (至少 2 个用户口头说法)
- responseTemplate (可选；UI 渲染模板)
- description (简介；若问题超出简单度量能力，在此说明保留意见)

列名可用中文显示名或英文名 —— 服务端会自动解析；找不到的列会报错并告诉你可选列。`

// inspectToolDescription is the LLM-facing description for the `inspect` tool —
// the explore agent's window into ACTUAL data. Before guessing a filter value
// or picking which columns identify a row, the agent should look at the real
// distinct values / sample rows so it grounds the structured spec in reality.
const inspectToolDescription = `探查某个 OD 的真实数据,用来「看清楚」再起草口径。两种用法:
- 只给 primaryOd:返回该 OD 的列清单 + 几行样本,让你看清有哪些列、长什么样。
- 给 primaryOd + column:返回该列的实时去重取值(最多 50 个),用来确定 filter 该填什么值、哪列才是标识列。

何时必用:当你要加 filter 但不确定值长什么样(例如「拿铁」在 spec_id 里到底是 SPEC-LATTE-* 还是别的),
或不确定哪列是"名称/标识"列时 —— 先 inspect,不要猜。列名可用中文或英文。`

// runExploreTurn is the entry point invoked from handler_agent_lakehouse.go's
// agentPolicy short-circuit. It drives the explore LLM loop end-to-end:
// loads history, builds the system prompt + tool defs, runs ≤exploreMaxRounds
// LLM rounds dispatching lookup/smartquery/commit_card, and persists each
// step into ont_agent_step.
//
// validatorRejection — when non-nil, the prior turn's commit_card was rejected
// downstream (via PUT ?dryRun=true); the rejection is prepended to the system
// prompt for G9 self-correction.
//
// llmClient — production code passes llmclient.NewLiveClient(); tests pass a
// Fixture. The seam exists for AC-11 hardening (see fixture.go).
func runExploreTurn(
	ctx context.Context,
	db *sql.DB,
	threadID, projectID, userQuestion string,
	validatorRejection *contracts.ValidatorRejection,
	sendSSEFull func(string, M),
	llmClient llmclient.Client,
) {
	// ── Load history ──
	var messages []M
	histRows, _ := db.QueryContext(ctx, `SELECT role, COALESCE(content,'')
		FROM ont_agent_step WHERE thread_id = $1 ORDER BY step_index`, threadID)
	if histRows != nil {
		for histRows.Next() {
			var role, content string
			_ = histRows.Scan(&role, &content)
			messages = append(messages, M{"role": role, "content": content})
		}
		histRows.Close()
	}

	// ── Compute next step index ──
	var stepIdx int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(step_index),0) FROM ont_agent_step WHERE thread_id = $1`, threadID).Scan(&stepIdx)
	stepIdx++

	// Persist the current user turn immediately so a mid-turn crash still
	// leaves history coherent (mirrors the lakehouse path).
	if _, err := db.ExecContext(ctx, `INSERT INTO ont_agent_step
		(thread_id, step_index, role, content)
		VALUES ($1, $2, 'user', $3)`,
		threadID, stepIdx, userQuestion); err != nil {
		log.Printf("EXPLORE-AGENT: failed to persist user step: %v", err)
	}
	stepIdx++

	// ── System prompt ──
	systemPrompt := exploreSystemPrompt()
	if validatorRejection != nil {
		systemPrompt = prependValidatorRejection(systemPrompt, validatorRejection)
	}

	// ── Build LLM messages ──
	llmMessages := []M{{"role": "system", "content": systemPrompt}}
	for _, m := range messages {
		llmMessages = append(llmMessages, m)
	}
	llmMessages = append(llmMessages, M{"role": "user", "content": userQuestion})

	// ── Tool defs ──
	tools := []llmclient.ToolDef{
		{Name: "lookup", Description: lookupToolDescription, Parameters: M{
			"type": "object",
			"properties": M{
				"ontology_name": M{"type": "array", "items": M{"type": "string"}},
				"keyword":       M{"type": "array", "items": M{"type": "string"}},
			},
		}},
		{Name: "smartquery", Description: smartqueryToolDescription, Parameters: M{
			"type":     "object",
			"required": []string{"odName"},
			"properties": M{
				"odName":  M{"type": "string"},
				"metric":  M{"type": "string"},
				"params":  M{"type": "object", "additionalProperties": true},
				"filters": M{"type": "array", "items": M{"type": "object"}},
				"groupBy": M{"type": "array", "items": M{"type": "string"}},
				"orderBy": M{"type": "array", "items": M{"type": "object"}},
				"limit":   M{"type": "integer"},
			},
		}},
		{Name: "commit_card", Description: commitCardToolDescription, Parameters: M{
			"type": "object",
			// The LLM emits a STRUCTURED spec; the server compiles SQL. measure
			// is required only for intent=aggregate, dimensions only for
			// enumerate — enforced in commitCardPayloadFromArgs (JSON-schema
			// can't express the conditional cleanly).
			"required": []string{
				"name", "displayName", "primaryOd", "intent", "triggerKeywords",
			},
			"properties": M{
				"name":        M{"type": "string"},
				"displayName": M{"type": "string"},
				"primaryOd":   M{"type": "string", "description": "OD 名，来自 lookup"},
				"intent":      M{"type": "string", "enum": []string{"aggregate", "enumerate"}, "description": "aggregate=度量(多少/总数/求和/占比); enumerate=列举(有哪些/列出/哪些种类)"},
				"measure": M{
					"type":        "object",
					"description": "intent=aggregate 必填；服务端据此生成聚合表达式",
					"properties": M{
						"agg":    M{"type": "string", "enum": []string{"SUM", "COUNT", "AVG", "MIN", "MAX", "COUNT_DISTINCT"}},
						"column": M{"type": "string", "description": "列名(中文显示名或英文名都可)；COUNT 计数全部时可省略"},
					},
				},
				"dimensions": M{"type": "array", "items": M{"type": "string"}, "description": "aggregate 的分组维度 / enumerate 要列出的列(列名,中文或英文)"},
				"filters": M{
					"type":        "array",
					"description": "可选过滤；每项 {prop, op, value}",
					"items": M{"type": "object", "properties": M{
						"prop":  M{"type": "string"},
						"op":    M{"type": "string", "enum": []string{"=", "!=", ">", "<", ">=", "<=", "like", "in"}},
						"value": M{"type": "string"},
					}},
				},
				"triggerKeywords":  M{"type": "array", "items": M{"type": "string"}},
				"responseTemplate": M{"type": "string"},
				"description":      M{"type": "string"},
			},
		}},
		{Name: "inspect", Description: inspectToolDescription, Parameters: M{
			"type":     "object",
			"required": []string{"primaryOd"},
			"properties": M{
				"primaryOd": M{"type": "string", "description": "要探查的 OD 名(来自 lookup)"},
				"column":    M{"type": "string", "description": "可选。给定则返回该列的实时去重取值;不给则返回列清单 + 几行样本"},
			},
		}},
	}

	// ── LLM config ──
	baseURL, apiKey, modelName, _, _, _, vendor := llmclient.GetConfigForRoleWithProxy(db, "agent")
	if baseURL == "" || modelName == "" {
		log.Printf("EXPLORE-AGENT: no active agent LLM config; aborting turn")
		sendSSEFull("error", M{"content": "no LLM config"})
		return
	}

	// ── Most-recent successful lookup result holds the OD UUID we need ──
	// emitCommitCard re-uses this to bind the new lakehouse_metric.object_id
	// without issuing a fresh DB lookup.
	var lastLookupResult M

	// ── Phase 4a stub gate ──
	// When EXPLORE_PHASE_4A_STUB=true, skip the LLM loop entirely on
	// deterministic triggers ("出卡" / "commit"). The stub emits a payload
	// whose Name starts with "FIXTURE_DET_STUB" so AC-11 (d) can tell the
	// stub path apart from the LLM-fixture path (which uses
	// "FIXTURE_LLM_DETERMINISTIC_").
	if isStubEnabled() && stubShouldEmit(userQuestion) {
		emitStubCommitCard(ctx, db, threadID, projectID, lastLookupResult, sendSSEFull, &stepIdx, userQuestion, llmMessages)
		return
	}

	// ── LLM loop ──
	emittedCardThisTurn := false
	nudgedThisTurn := false
	// Consecutive failed smartquery / tool calls — if we hit this without
	// emitting a card, the LLM is stuck in a broken-tool retry spiral. Nudge
	// it to skip ahead to commit_card based on the lookup results it already
	// has.
	consecutiveToolErrors := 0
	// Tracks whether the closing analytical answer has been streamed. The
	// main loop's text-close path sets this; the guaranteed final-answer
	// phase at the bottom checks it so we never answer twice.
	finalAnswerStreamed := false
	for round := 0; round < exploreMaxRounds; round++ {
		chatBody := M{
			"model":       modelName,
			"messages":    llmMessages,
			"max_tokens":  4096,
			"temperature": 0,
		}
		content, toolCalls, _, err := llmClient.DoChatWithTools(ctx, baseURL, apiKey, chatBody, tools, "", vendor)
		if err != nil {
			log.Printf("EXPLORE-AGENT: LLM call failed: %v", err)
			sendSSEFull("error", M{"content": "LLM 调用失败: " + err.Error()})
			return
		}

		// No tool call → LLM is bailing into text. If we never emitted a
		// commit_card this turn, that's a CONTRACT VIOLATION (per the system
		// prompt). Nudge once with an explicit user-style reminder and retry
		// the round. If it bails a SECOND time, surrender to text.
		if len(toolCalls) == 0 {
			if !emittedCardThisTurn && !nudgedThisTurn {
				nudgedThisTurn = true
				log.Printf("EXPLORE-AGENT: nudging LLM after text-only response (no commit_card yet)")
				// Show user something so the stream doesn't feel hung mid-turn.
				partial := strings.TrimSpace(llmclient.StripThinkTags(content))
				if partial != "" {
					emitStreamingToken(partial+"\n\n（…让我再试一次,把它固化成一条 metric…）\n", sendSSEFull)
				}
				llmMessages = append(llmMessages, M{
					"role": "user",
					"content": "你刚才没有调用 commit_card,违反了 system prompt 的核心契约。" +
						"无论已存在的 metric 是否相关、无论工具调用是否成功,本轮都必须以 commit_card 收尾。" +
						"请基于已经掌握的 OD / schema / 数据形状立刻 emit 一个 commit_card。" +
						"信息不完美没关系 — 在 description 字段里把保留意见 / 局限标记清楚,然后 emit。",
				})
				continue
			}
			// Already nudged or already shipped a card — accept text close.
			finalText := strings.TrimSpace(llmclient.StripThinkTags(content))
			if finalText == "" {
				finalText = "（无回答）"
			}
			// Chunked emit so the chat feels like it's actually streaming.
			// True per-token streaming would require swapping DoChatWithTools
			// for DoChatStreamCallback, which doesn't yet support our tool
			// surface. This is the UX bridge until that lands.
			emitStreamingToken(finalText, sendSSEFull)
			_, _ = db.ExecContext(ctx, `INSERT INTO ont_agent_step
				(thread_id, step_index, role, content)
				VALUES ($1, $2, 'assistant', $3)`,
				threadID, stepIdx, finalText)
			stepIdx++
			sendSSEFull("done", M{"modelName": modelName})
			return
		}

		// If the LLM emitted reasoning text ALONGSIDE the tool call (some
		// models do this naturally; the system prompt also asks for it),
		// stream it to the chat BEFORE the tool call so the user sees the
		// "why am I doing this" before the "what I did" chip. This is the
		// difference between a black-box agent and one the user can follow.
		if reasoning := strings.TrimSpace(llmclient.StripThinkTags(content)); reasoning != "" {
			emitStreamingToken(reasoning+"\n\n", sendSSEFull)
			_, _ = db.ExecContext(ctx, `INSERT INTO ont_agent_step
				(thread_id, step_index, role, content)
				VALUES ($1, $2, 'assistant', $3)`,
				threadID, stepIdx, reasoning)
			stepIdx++
		}

		// One tool call per round (we iterate; multi-call batches handled by
		// the next loop iteration).
		tc := toolCalls[0]
		llmMessages = append(llmMessages, llmclient.BuildAssistantToolCallMessage([]llmclient.ToolCallResult{tc}))
		sendSSEFull("function_call", M{"name": tc.Name, "arguments": tc.Arguments})

		switch tc.Name {
		case "lookup":
			res := lakehouseToolLookup(ctx, db, projectID, tc.Arguments)
			if res != nil {
				lastLookupResult = res
				consecutiveToolErrors = 0
			}
			resJSON, _ := json.Marshal(res)
			llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, string(resJSON)))
			sendSSEFull("tool_result", M{"name": "lookup", "result": res})
		case "inspect":
			od := strOrEmpty(tc.Arguments["primaryOd"])
			col := strOrEmpty(tc.Arguments["column"])
			res, ierr := inspectOD(ctx, db, projectID, od, col)
			if ierr != nil {
				res = M{"error": ierr.Error()}
			}
			resJSON, _ := json.Marshal(res)
			llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, string(resJSON)))
			sendSSEFull("tool_result", M{"name": "inspect", "result": res})
		case "smartquery":
			res := lakehouseToolSmartQuery(ctx, db, projectID, userQuestion, tc.Arguments, nil)
			resJSON, _ := json.Marshal(res)
			llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, string(resJSON)))
			sendSSEFull("tool_result", M{"name": "smartquery", "result": res})
			// Tool-error detection: smartquery returns {"code":"...", "error":"..."}
			// shape on failure. We don't want the LLM to spin on broken tools.
			if res != nil {
				if _, hasErr := res["error"]; hasErr {
					consecutiveToolErrors++
				} else if _, hasCode := res["code"]; hasCode {
					consecutiveToolErrors++
				} else {
					consecutiveToolErrors = 0
				}
			}
			if consecutiveToolErrors >= 2 && !emittedCardThisTurn && !nudgedThisTurn {
				nudgedThisTurn = true
				log.Printf("EXPLORE-AGENT: nudging LLM after %d consecutive tool errors", consecutiveToolErrors)
				llmMessages = append(llmMessages, M{
					"role": "user",
					"content": "smartquery 已经失败 2 次 — 这个项目可能没有名字匹配的预注册口径。" +
						"不要再调 smartquery。立刻基于 lookup 已经返回的 OD schema 推断,emit 一个 commit_card —— " +
						"primaryOd 选最相关的 OD,intent 选 aggregate,measure 填 {agg, column}(如 {agg:SUM, column:实付金额}),dimensions 填分组维度。" +
						"在 description 里注明:「基于 schema 推断,smartquery 验证受限」。",
				})
			}
		case "commit_card":
			if emittedCardThisTurn {
				// Bound: ≤1 commit_card per user turn (plan A-5). Feed back
				// a tool-result telling the LLM to stop.
				resJSON, _ := json.Marshal(M{"error": "已在本轮提交一次 commit_card；若需再提交请在下轮再说"})
				llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, string(resJSON)))
				continue
			}
			payload, exec, emitErr := emitCommitCard(ctx, db, projectID, lastLookupResult, tc.Arguments, sendSSEFull)
			if emitErr != nil {
				// Structural-validation / column-resolution / engine-execution
				// failure — feed the addressable error back so the LLM emits a
				// corrected structured spec next round (no SQL text to repair).
				resJSON, _ := json.Marshal(M{"error": emitErr.Error()})
				llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, string(resJSON)))
				continue
			}
			emittedCardThisTurn = true
			// Persist a synthetic assistant step recording the emit.
			payloadJSON, _ := json.Marshal(payload)
			_, _ = db.ExecContext(ctx, `INSERT INTO ont_agent_step
				(thread_id, step_index, role, content, function_call)
				VALUES ($1, $2, 'assistant', $3, $4::jsonb)`,
				threadID, stepIdx, "[commit_card emitted]", string(payloadJSON))
			stepIdx++

			// Emit metric_result from the engine's already-executed rows +
			// feed them back so the LLM writes the closing analysis.
			emitMetricResultFromExec(ctx, db, payload, exec, tc.ID, userQuestion, sendSSEFull, &llmMessages)
		default:
			resJSON, _ := json.Marshal(M{"error": "unknown tool: " + tc.Name})
			llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, string(resJSON)))
		}
	}

	// ── Loop exhausted without commit_card → forced final round (with retry) ──
	// Give the LLM up to N shots with ONLY the commit_card tool exposed so it
	// can't spin on lookup/smartquery. Each emit/SQL failure feeds the error
	// back so the next attempt can self-correct. This honors the prompt's
	// "every turn must end with commit_card" contract even when the natural
	// loop bailed out — AND it survives the common BARE_SQL_REJECTED /
	// missing-FROM / GROUP-BY failures that previously killed a single-shot
	// forced round dead.
	if !emittedCardThisTurn {
		availableODs := listProjectODs(ctx, db, projectID)
		odHint := ""
		if len(availableODs) > 0 {
			odHint = "\n本项目可用的 OD 名(primaryOd MUST 从这个列表里选): " + strings.Join(availableODs, ", ") + "."
		}
		llmMessages = append(llmMessages, M{
			"role": "user",
			"content": "时间到。基于至今为止 lookup 拿到的 OD/schema 信息,立刻 emit 一个 commit_card。" +
				"信息不完美没关系 — 在 description 里把保留意见(例如「smartquery 无法验证」「OD 关联待确认」)写清楚就行。" +
				"现在只剩 commit_card 一个工具可用,不要再尝试 lookup 或 smartquery。" +
				"\n\n记住:你只描述结构(intent + measure{agg,column} 或 dimensions + 可选 filters),不要写 SQL。" +
				"列名用 lookup 返回的属性名(中文或英文都行),只基于单个 OD。" + odHint,
		})
		const maxForcedAttempts = 3
		for attempt := 1; attempt <= maxForcedAttempts && !emittedCardThisTurn; attempt++ {
			log.Printf("EXPLORE-AGENT: forced final commit_card round, attempt %d/%d", attempt, maxForcedAttempts)
			forcedTools := []llmclient.ToolDef{tools[2]} // commit_card only — same index as tools[] above
			chatBody := M{
				"model":       modelName,
				"messages":    llmMessages,
				"max_tokens":  4096,
				"temperature": 0,
			}
			_, toolCalls, _, err := llmClient.DoChatWithTools(ctx, baseURL, apiKey, chatBody, forcedTools, "", vendor)
			if err != nil {
				log.Printf("EXPLORE-AGENT: forced round attempt %d LLM call failed: %v", attempt, err)
				break // network/LLM error won't fix itself by retrying immediately
			}
			if len(toolCalls) == 0 || toolCalls[0].Name != "commit_card" {
				log.Printf("EXPLORE-AGENT: forced round attempt %d produced no commit_card", attempt)
				llmMessages = append(llmMessages, M{
					"role":    "user",
					"content": "你没有调用 commit_card。现在 MUST 立刻调用 commit_card 工具,不要输出纯文本。",
				})
				continue
			}
			tc := toolCalls[0]
			llmMessages = append(llmMessages, llmclient.BuildAssistantToolCallMessage([]llmclient.ToolCallResult{tc}))
			sendSSEFull("function_call", M{"name": tc.Name, "arguments": tc.Arguments})
			payload, exec, emitErr := emitCommitCard(ctx, db, projectID, lastLookupResult, tc.Arguments, sendSSEFull)
			if emitErr != nil {
				// Feed the addressable error back so the next attempt fixes the
				// structured spec (wrong column / bad measure / exec error).
				log.Printf("EXPLORE-AGENT: forced round attempt %d emit failed: %v", attempt, emitErr)
				resJSON, _ := json.Marshal(M{"error": emitErr.Error()})
				llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, string(resJSON)))
				llmMessages = append(llmMessages, M{
					"role": "user",
					"content": "上面的 commit_card 被拒绝: " + emitErr.Error() +
						"\n请按报错修正后立刻重新 emit(检查列名是否在该 OD 上、measure 是否正确)。",
				})
				continue
			}
			emittedCardThisTurn = true
			payloadJSON, _ := json.Marshal(payload)
			_, _ = db.ExecContext(ctx, `INSERT INTO ont_agent_step
				(thread_id, step_index, role, content, function_call)
				VALUES ($1, $2, 'assistant', $3, $4::jsonb)`,
				threadID, stepIdx, "[commit_card emitted via forced final round]", string(payloadJSON))
			stepIdx++
			emitMetricResultFromExec(ctx, db, payload, exec, tc.ID, userQuestion, sendSSEFull, &llmMessages)
		}
	}

	// ── GUARANTEED final-answer phase (always fires) ──
	// The user must NEVER be left with a dead spinner. Whatever happened above
	// — card succeeded, all cards failed, LLM went silent — we make exactly one
	// tools-disabled call to produce a closing message and stream it.
	//
	//   • card succeeded → the answer-nudge from executeAndEmitMetricResult is
	//     already in llmMessages, so this produces the 4-section analysis.
	//   • no card succeeded → we inject an honest "explain what you found and
	//     why you couldn't finalize a metric" nudge so the user gets a real
	//     explanation (citing the OD/schema seen + the SQL errors) instead of
	//     silence.
	if !finalAnswerStreamed {
		if !emittedCardThisTurn {
			log.Printf("EXPLORE-AGENT: no card succeeded — synthesizing honest fallback answer")
			llmMessages = append(llmMessages, M{
				"role": "user",
				"content": "本轮没能成功固化一条可执行的口径(SQL 反复失败或信息不足)。" +
					"现在请直接用文字诚实地回答用户最初的问题: 「" + userQuestion + "」\n\n" +
					"要求:\n" +
					"1. 基于你通过 lookup 看到的 OD / 字段,尽量给出**力所能及的判断或方向**,而不是空手而归。\n" +
					"2. 明确说明**为什么没能给出精确口径**(例如:相关字段不在同一个 OD、缺少关联、SQL 形态受限)。\n" +
					"3. 给用户**下一步可操作的建议**(换个问法 / 需要补哪个 OD 关联 / 可以先看哪个更粗的指标)。\n" +
					"4. 用 Markdown,语气务实。不要道歉刷屏,也不要假装查到了不存在的数据。\n" +
					"5. 不要再调工具,直接文字结束。",
			})
		} else {
			log.Printf("EXPLORE-AGENT: synthesizing guaranteed final answer (loop didn't circle back)")
		}
		synthBody := M{
			"model":       modelName,
			"messages":    llmMessages,
			"max_tokens":  4096,
			"temperature": 0,
		}
		// No tools → the model can only reply with text.
		synthContent, _, _, synthErr := llmClient.DoChatWithTools(ctx, baseURL, apiKey, synthBody, nil, "", vendor)
		finalText := ""
		if synthErr != nil {
			log.Printf("EXPLORE-AGENT: final-answer synthesis failed: %v", synthErr)
		} else {
			finalText = strings.TrimSpace(llmclient.StripThinkTags(synthContent))
		}
		// Last-ditch fallback: if even the synthesis call failed or returned
		// empty, emit a hard-coded honest message so the spinner never dies
		// silent. This is the floor of the reliability guarantee.
		if finalText == "" {
			if emittedCardThisTurn {
				finalText = "我已经把口径草稿放到了右侧,但在生成总结时遇到了问题。你可以在右栏点「测试运行」查看实际数据,或直接采纳这条口径。"
			} else {
				finalText = "抱歉,我这一轮没能把你的问题固化成一条可执行的口径 —— 多次尝试生成的 SQL 都没能在数据库上跑通。" +
					"建议:换一个更聚焦单一对象的问法,或确认相关字段是否都在同一个对象(OD)上。你也可以告诉我具体想看哪个指标,我再试一次。"
			}
		}
		emitStreamingToken(finalText, sendSSEFull)
		_, _ = db.ExecContext(ctx, `INSERT INTO ont_agent_step
			(thread_id, step_index, role, content)
			VALUES ($1, $2, 'assistant', $3)`,
			threadID, stepIdx, finalText)
		stepIdx++
		finalAnswerStreamed = true
	}

	sendSSEFull("done", M{"modelName": modelName})
}

// emitCommitCard validates + persists + emits one commit_card SSE event.
// Returns (payload, nil) on success or (zero, err) on parse-reject. Never
// calls sendSSEFull on the error path (the caller decides whether to retry
// with self-correction or surface the failure).
//
// Pre-creates a `mark=false` row so the frontend has a stable id to PUT to.
// triggers are inserted in the SAME tx; rollback on any error leaves no
// orphan row. lastLookup carries the OD UUID resolved during this turn's
// lookup tool call; if the LLM emits without ever calling lookup first, we
// fall back to a per-project name->id resolve.
// draftExecResult carries the engine's execution output for the drafted metric
// so the caller can emit a metric_result + feed rows to the LLM without
// re-running anything.
type draftExecResult struct {
	columns         []string
	rows            []M
	sql             string
	level           string // "simple" (engine) | "sql" (server-built DISTINCT)
	canonicalMetric string // derived measure expr (empty for single-OD enumerate)
}

func emitCommitCard(
	ctx context.Context,
	db *sql.DB,
	projectID string,
	lastLookup M,
	rawArgs map[string]interface{},
	sendSSEFull func(string, M),
) (contracts.CommitCardPayload, *draftExecResult, error) {
	payload, err := commitCardPayloadFromArgs(rawArgs)
	if err != nil {
		return contracts.CommitCardPayload{}, nil, err
	}
	if len(payload.TriggerKeywords) < 2 {
		return contracts.CommitCardPayload{}, nil, fmt.Errorf("NEED_TWO_TRIGGERS: 至少需要 2 个 triggerKeywords，当前只有 %d", len(payload.TriggerKeywords))
	}

	// ── Structural column resolution (replaces the old SQL-text aliaser) ──
	// The LLM may use Chinese display_name OR English name; resolve to the
	// canonical English `name`. Dimensions + filters MAY reference another OD
	// as `OD.column` (cross-OD) — the SmartQuery engine auto-joins from
	// ont_causality at compile time. The measure stays on the primary OD.
	resolver, rerr := loadODColumns(ctx, db, projectID, payload.PrimaryOD)
	if rerr != nil {
		return contracts.CommitCardPayload{}, nil, fmt.Errorf("LOAD_OD_COLUMNS: %v", rerr)
	}
	if payload.Measure != nil && payload.Measure.Column != "" {
		col, cerr := resolver.resolve(payload.Measure.Column) // measure: primary-OD only
		if cerr != nil {
			return contracts.CommitCardPayload{}, nil, fmt.Errorf("measure.column: %v", cerr)
		}
		payload.Measure.Column = col
	}
	for i, d := range payload.Dimensions {
		col, cerr := resolveColumnRef(ctx, db, projectID, resolver, d)
		if cerr != nil {
			return contracts.CommitCardPayload{}, nil, fmt.Errorf("dimensions[%d]: %v", i, cerr)
		}
		payload.Dimensions[i] = col
	}
	for i := range payload.Filters {
		col, cerr := resolveColumnRef(ctx, db, projectID, resolver, payload.Filters[i].Prop)
		if cerr != nil {
			return contracts.CommitCardPayload{}, nil, fmt.Errorf("filters[%d].prop: %v", i, cerr)
		}
		payload.Filters[i].Prop = col
	}

	// auto_group_by mirrors the resolved dimensions; canonical_metric is
	// derived inside executeStructuredDraft (which also picks the level).
	payload.AutoGroupBy = append([]string{}, payload.Dimensions...)

	// ── Compile + execute via the deterministic engine (validates too) ──
	exec, eerr := executeStructuredDraft(ctx, db, projectID, payload)
	if eerr != nil {
		// Execution/validation failure → surface to the LLM for self-correction
		// (the caller feeds emitErr back as a tool result and retries).
		return contracts.CommitCardPayload{}, nil, eerr
	}
	payload.QuerySql = exec.sql
	payload.CanonicalMetric = exec.canonicalMetric

	// Resolve object_id UUID. Prefer the most recent lookup tool result; if
	// the LLM never called lookup, fall back to a direct lookup by name.
	objectID := resolveObjectIDFromLookup(lastLookup, payload.PrimaryOD)
	if objectID == "" {
		objectID = resolveObjectIDByName(ctx, db, projectID, payload.PrimaryOD)
	}
	if objectID == "" {
		return contracts.CommitCardPayload{}, nil, fmt.Errorf("OD_NOT_FOUND: primaryOd=%q 未匹配到 ont_object_type", payload.PrimaryOD)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return contracts.CommitCardPayload{}, nil, fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	parametersJSON, _ := json.Marshal(payload.Parameters)
	if len(payload.Parameters) == 0 {
		parametersJSON = []byte("[]")
	}
	autoGB := payload.AutoGroupBy
	if autoGB == nil {
		autoGB = []string{}
	}

	// Persisted shape is decided by executeStructuredDraft:
	//   single-OD enumerate → level='sql' (server-built SELECT DISTINCT).
	//   aggregate / cross-OD enumerate → level='simple' (engine re-derives from
	//   canonical_metric + auto_group_by, resolving cross-OD JOINs at query time).
	metricLevel := exec.level

	var newID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO lakehouse_metric
			(project_id, object_id, name, display_name, description, level,
			 canonical_metric, canonical_filters, auto_group_by,
			 parameters, response_template, mark, query_sql)
		VALUES ($1, $2, $3, $4, $5, $6,
		        $7, '[]'::jsonb, $8,
		        $9::jsonb, $10, false, NULLIF($11,''))
		RETURNING id`,
		projectID, objectID, payload.Name, payload.DisplayName, payload.Description, metricLevel,
		payload.CanonicalMetric, pgStringArray(autoGB),
		string(parametersJSON), payload.ResponseTemplate, payload.QuerySql,
	).Scan(&newID); err != nil {
		return contracts.CommitCardPayload{}, nil, fmt.Errorf("insert lakehouse_metric: %w", err)
	}
	payload.ID = newID

	if err := ontology.UpdateMetricTriggers(ctx, tx, projectID, newID, objectID, payload.TriggerKeywords); err != nil {
		return contracts.CommitCardPayload{}, nil, fmt.Errorf("insert triggers: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return contracts.CommitCardPayload{}, nil, fmt.Errorf("commit: %w", err)
	}
	committed = true

	payload.DraftID = uuid.New().String()
	sendSSEFull("commit_card", commitCardPayloadToMap(payload))
	return payload, exec, nil
}

// loadODColumns + odColumnResolver provide STRUCTURAL column resolution against
// the OD's ont_property rows. This replaces the old SQL-text regex aliaser:
// the LLM names a column (Chinese display_name OR English name) and we resolve
// it to the canonical English `name`, or reject it with the available columns.
type odColumnResolver struct {
	byName    map[string]string // lower(name) -> name
	byDisplay map[string]string // lower(display_name) -> name
	available []string
}

func loadODColumns(ctx context.Context, db *sql.DB, projectID, odName string) (*odColumnResolver, error) {
	r := &odColumnResolver{byName: map[string]string{}, byDisplay: map[string]string{}}
	if odName == "" {
		return r, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.name, COALESCE(p.display_name,'')
		  FROM ont_property p
		  JOIN ont_object_type o ON o.id = p.object_type_id
		 WHERE p.project_id = $1 AND LOWER(o.name) = LOWER($2)`,
		projectID, odName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, disp string
		if err := rows.Scan(&name, &disp); err != nil {
			continue
		}
		if name == "" {
			continue
		}
		r.byName[strings.ToLower(name)] = name
		r.available = append(r.available, name)
		if disp != "" && disp != name {
			r.byDisplay[strings.ToLower(disp)] = name
		}
	}
	return r, nil
}

// resolveBare resolves a single column name (no OD prefix) against this OD's
// properties, accepting English name or Chinese display_name.
func (r *odColumnResolver) resolveBare(token string) (string, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return "", fmt.Errorf("COLUMN_EMPTY")
	}
	if n, ok := r.byName[strings.ToLower(t)]; ok {
		return n, nil
	}
	if n, ok := r.byDisplay[strings.ToLower(t)]; ok {
		return n, nil
	}
	return "", fmt.Errorf("COLUMN_NOT_FOUND: 列 %q 不在 OD 上;可用列: %s", t, strings.Join(r.available, ", "))
}

// resolve rejects cross-OD prefixes (used for the measure column + inspect,
// which are single-OD). Dimensions/filters use resolveColumnRef instead.
func (r *odColumnResolver) resolve(token string) (string, error) {
	if strings.Contains(token, ".") {
		return "", fmt.Errorf("CROSS_OD_NOT_ALLOWED: 列 %q 含 OD 前缀；度量只能基于主 OD", token)
	}
	return r.resolveBare(token)
}

// inspectOD is the read-only data-introspection behind the `inspect` tool.
// All SQL is SERVER-BUILT from {od, column} with pq.QuoteIdentifier (the LLM
// supplies no SQL) and run against the OD view in the public schema.
//   - column given → live `SELECT DISTINCT <col>` (≤50) so the agent sees the
//     real value domain before choosing a filter / identifier column.
//   - column omitted → column list + a few sample rows so it sees the shape.
func inspectOD(ctx context.Context, db *sql.DB, projectID, od, column string) (M, error) {
	resolver, err := loadODColumns(ctx, db, projectID, od)
	if err != nil {
		return nil, fmt.Errorf("LOAD_OD_COLUMNS: %v", err)
	}
	if len(resolver.available) == 0 {
		return nil, fmt.Errorf("OD_NOT_FOUND: %q 不存在或没有属性", od)
	}
	odQ := pq.QuoteIdentifier(od)

	if strings.TrimSpace(column) != "" {
		col, cerr := resolver.resolve(column)
		if cerr != nil {
			return nil, cerr
		}
		colQ := pq.QuoteIdentifier(col)
		const cap = 50
		q := fmt.Sprintf(`SELECT DISTINCT %s::text AS v FROM %s WHERE %s IS NOT NULL ORDER BY 1 LIMIT %d`,
			colQ, odQ, colQ, cap+1)
		rows, qerr := db.QueryContext(ctx, q)
		if qerr != nil {
			return nil, fmt.Errorf("INSPECT_FAILED: %v", qerr)
		}
		defer rows.Close()
		var vals []string
		for rows.Next() {
			var v sql.NullString
			if err := rows.Scan(&v); err == nil && v.Valid {
				vals = append(vals, v.String)
			}
		}
		truncated := len(vals) > cap
		if truncated {
			vals = vals[:cap]
		}
		return M{"od": od, "column": col, "distinctValues": vals, "count": len(vals), "truncated": truncated}, nil
	}

	// No column: column list + sample rows.
	q := fmt.Sprintf(`SELECT * FROM %s LIMIT 5`, odQ)
	rows, qerr := db.QueryContext(ctx, q)
	if qerr != nil {
		return nil, fmt.Errorf("INSPECT_FAILED: %v", qerr)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var sample []M
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := M{}
		for i, c := range cols {
			row[c] = inspectCellValue(vals[i])
		}
		sample = append(sample, row)
	}
	return M{"od": od, "columns": resolver.available, "sampleRows": sample}, nil
}

// inspectCellValue normalises a scanned cell to a JSON-friendly form.
func inspectCellValue(v interface{}) interface{} {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339)
	}
	return v
}

// buildCanonicalMetric turns a structured MeasureSpec into the aggregate
// expression string the engine + persistence expect. Column is already
// resolved to the canonical English name and is quoted as an identifier.
func buildCanonicalMetric(m *contracts.MeasureSpec) string {
	if m == nil {
		return ""
	}
	if m.Agg == "COUNT" && m.Column == "" {
		return "COUNT(*)"
	}
	col := pq.QuoteIdentifier(m.Column)
	if m.Agg == "COUNT_DISTINCT" {
		return "COUNT(DISTINCT " + col + ")"
	}
	return m.Agg + "(" + col + ")"
}

// executeStructuredDraft compiles + runs the drafted metric deterministically.
//
//	aggregate → build a smartquery.QuerySpec and run the engine (Execute);
//	            the engine's OntologySQL is a clean bare single-OD SELECT.
//	enumerate → server-build `SELECT DISTINCT <dims> FROM "<OD>" [WHERE …]`
//	            (the LLM never writes it) and run it via ExecuteSQLMetric.
//
// Returns the rows/columns/sql, or an error the LLM can self-correct from.
func executeStructuredDraft(ctx context.Context, db *sql.DB, projectID string, p contracts.CommitCardPayload) (*draftExecResult, error) {
	exec := smartqueryExec(db)
	crossOD := hasCrossODRef(p.Dimensions) || hasCrossODFilter(p.Filters)

	// Single-OD enumerate keeps the exact server-built SELECT DISTINCT (no
	// spurious count column). Anything cross-OD — or any aggregate — goes
	// through the engine, which resolves the JOINs from ont_causality.
	if p.Intent == "enumerate" && !crossOD {
		sqlText := buildEnumerateSQL(p.PrimaryOD, p.Dimensions, p.Filters)
		res := exec.ExecuteSQLMetric(ctx, projectID, sqlText, map[string]interface{}{})
		if !res.ExecutionOK {
			return nil, fmt.Errorf("ENUMERATE_EXEC_FAILED: %s", res.ErrorMessage)
		}
		rows, cols := rowsColsFromResultJSON(res.ResultJSON, p.Dimensions)
		return &draftExecResult{columns: cols, rows: rows, sql: sqlText, level: "sql", canonicalMetric: ""}, nil
	}

	// Engine path. aggregate → the real measure. cross-OD enumerate → EMPTY
	// metric + GroupBy only: the engine's gate allows a groupBy-only spec
	// (PassGateMetricOrGroupBy), producing the distinct dimension combos across
	// the joined ODs — no spurious COUNT column. (COUNT(*) would fail because
	// the engine resolves the agg's column, and "*" isn't a property.)
	metric := ""
	if p.Intent == "aggregate" {
		metric = buildCanonicalMetric(p.Measure)
	}
	spec := smartquery.QuerySpec{
		ProjectID:   projectID,
		Objects:     []string{p.PrimaryOD},
		Metric:      metric,
		GroupBy:     append([]string{}, p.Dimensions...),
		DisplayMode: "table",
	}
	for _, f := range p.Filters {
		op := f.Op
		if op == "" {
			op = "="
		}
		spec.Filters = append(spec.Filters, smartquery.FilterItem{Prop: f.Prop, Op: op, Value: f.Value})
	}
	// ensureObjectsCoverReferencedProps adds the ODs referenced by cross-OD
	// groupBy/filter props so the engine can JOIN them.
	// promoteFilterPropsToGroupBy ("过滤即展示") turns a filtered prop into a
	// displayed dimension — desirable for aggregate, but for enumerate it makes
	// the scope filter (e.g. MENUITEM.name='拿铁') a column that collides with a
	// same-named dimension (INGREDIENT.name) and shows "拿铁" in every row.
	// So promote only for aggregate; enumerate keeps the filter as a pure WHERE.
	if p.Intent == "aggregate" {
		promoteFilterPropsToGroupBy(&spec)
	}
	ensureObjectsCoverReferencedProps(db, projectID, &spec)

	res := exec.Execute(ctx, spec)
	if !res.ExecutionOK {
		return nil, fmt.Errorf("EXEC_FAILED: %s", res.ErrorMessage)
	}
	sqlText := res.OntologySQL
	if strings.TrimSpace(sqlText) == "" {
		sqlText = res.SQL
	}
	// Engine output uses BARE column names (the OD prefix is stripped), so seed
	// the column order with the prefix-stripped dimensions — otherwise the
	// header list shows phantom `OD.col` columns next to the real bare ones.
	rows, cols := rowsColsFromResultJSON(res.ResultJSON, stripODPrefixes(p.Dimensions))
	return &draftExecResult{columns: cols, rows: rows, sql: sqlText, level: "simple", canonicalMetric: metric}, nil
}

// stripODPrefixes drops a leading `OD.` from each column ref so the names match
// the engine's bare-column result keys.
func stripODPrefixes(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		if idx := strings.LastIndex(c, "."); idx >= 0 {
			out[i] = c[idx+1:]
		} else {
			out[i] = c
		}
	}
	return out
}

// hasCrossODRef / hasCrossODFilter detect `OD.column` references that require
// the engine's JOIN resolution rather than the single-OD fast path.
func hasCrossODRef(cols []string) bool {
	for _, c := range cols {
		if strings.Contains(c, ".") {
			return true
		}
	}
	return false
}

func hasCrossODFilter(filters []contracts.CommitFilter) bool {
	for _, f := range filters {
		if strings.Contains(f.Prop, ".") {
			return true
		}
	}
	return false
}

// resolveColumnRef resolves a dimension/filter column that MAY be cross-OD
// (`OD.column`). Bare columns resolve against the primary OD's properties;
// `OD.column` resolves the column against THAT OD and re-emits `OD.<name>`.
// Both forms accept Chinese display_name or English name.
func resolveColumnRef(ctx context.Context, db *sql.DB, projectID string, primary *odColumnResolver, token string) (string, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return "", fmt.Errorf("COLUMN_EMPTY")
	}
	if i := strings.Index(t, "."); i >= 0 {
		odPart := strings.TrimSpace(t[:i])
		colPart := strings.TrimSpace(t[i+1:])
		r, err := loadODColumns(ctx, db, projectID, odPart)
		if err != nil {
			return "", fmt.Errorf("LOAD_OD_COLUMNS(%s): %v", odPart, err)
		}
		if len(r.available) == 0 {
			return "", fmt.Errorf("CROSS_OD_NOT_FOUND: OD %q 不存在或无属性", odPart)
		}
		col, cerr := r.resolveBare(colPart)
		if cerr != nil {
			return "", fmt.Errorf("%s.%v", odPart, cerr)
		}
		return odPart + "." + col, nil
	}
	return primary.resolveBare(t)
}

// buildEnumerateSQL deterministically assembles a single-OD SELECT DISTINCT.
// Identifiers via pq.QuoteIdentifier, literals via pq.QuoteLiteral, op via a
// strict whitelist — the LLM contributes only resolved column names + values.
func buildEnumerateSQL(od string, dims []string, filters []contracts.CommitFilter) string {
	cols := make([]string, 0, len(dims))
	for _, d := range dims {
		cols = append(cols, pq.QuoteIdentifier(d))
	}
	sb := strings.Builder{}
	sb.WriteString("SELECT DISTINCT ")
	sb.WriteString(strings.Join(cols, ", "))
	sb.WriteString(" FROM ")
	sb.WriteString(pq.QuoteIdentifier(od))
	whitelist := map[string]string{"=": "=", "!=": "!=", ">": ">", "<": "<", ">=": ">=", "<=": "<=", "like": "LIKE"}
	var where []string
	for _, f := range filters {
		op, ok := whitelist[strings.ToLower(strings.TrimSpace(f.Op))]
		if !ok {
			continue // skip unsupported ops (e.g. `in`) — keep enumerate simple
		}
		where = append(where, pq.QuoteIdentifier(f.Prop)+" "+op+" "+pq.QuoteLiteral(f.Value))
	}
	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(where, " AND "))
	}
	return sb.String()
}

// rowsColsFromResultJSON parses the engine's ResultJSON ([]object) into []M and
// an ordered column list (dimensions first, then any remaining keys). JSON
// object key order isn't guaranteed, so we anchor on the known dimensions.
func rowsColsFromResultJSON(resultJSON string, dims []string) ([]M, []string) {
	var raw []map[string]interface{}
	if resultJSON != "" {
		_ = json.Unmarshal([]byte(resultJSON), &raw)
	}
	rows := make([]M, 0, len(raw))
	seen := map[string]bool{}
	cols := []string{}
	for _, d := range dims {
		if !seen[d] {
			seen[d] = true
			cols = append(cols, d)
		}
	}
	for _, r := range raw {
		m := M{}
		for k, v := range r {
			m[k] = v
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
		rows = append(rows, m)
	}
	return rows, cols
}

// validMeasureAggs is the closed set of aggregate functions the structured
// measure accepts. Mirrors the engine's recognized aggregates.
var validMeasureAggs = map[string]bool{
	"SUM": true, "COUNT": true, "AVG": true, "MIN": true, "MAX": true, "COUNT_DISTINCT": true,
}

// commitCardPayloadFromArgs decodes the LLM tool-call args into a typed
// CommitCardPayload. The LLM emits a STRUCTURED spec (measure / dimensions /
// filters) — it does NOT write SQL. CanonicalMetric + QuerySql are derived
// later in emitCommitCard (left empty here).
func commitCardPayloadFromArgs(args map[string]interface{}) (contracts.CommitCardPayload, error) {
	var p contracts.CommitCardPayload
	p.Name = strOrEmpty(args["name"])
	p.DisplayName = strOrEmpty(args["displayName"])
	p.PrimaryOD = strOrEmpty(args["primaryOd"])
	p.Intent = strings.ToLower(strings.TrimSpace(strOrEmpty(args["intent"])))
	if p.Intent == "" {
		p.Intent = "aggregate" // default
	}
	p.ResponseTemplate = strOrEmpty(args["responseTemplate"])
	p.Description = strOrEmpty(args["description"])
	p.Dimensions = anyToStrings(args["dimensions"])
	p.TriggerKeywords = anyToStrings(args["triggerKeywords"])

	// measure {agg, column} — JSON round-trip.
	if raw, ok := args["measure"]; ok && raw != nil {
		blob, _ := json.Marshal(raw)
		var ms contracts.MeasureSpec
		if err := json.Unmarshal(blob, &ms); err == nil {
			ms.Agg = strings.ToUpper(strings.TrimSpace(ms.Agg))
			ms.Column = strings.TrimSpace(ms.Column)
			if ms.Agg != "" || ms.Column != "" {
				p.Measure = &ms
			}
		}
	}
	// filters [{prop,op,value}] — JSON round-trip.
	if raw, ok := args["filters"]; ok && raw != nil {
		blob, _ := json.Marshal(raw)
		var fs []contracts.CommitFilter
		if err := json.Unmarshal(blob, &fs); err == nil {
			p.Filters = fs
		}
	}

	// ── Shape gates ──
	if strings.TrimSpace(p.Name) == "" {
		return p, fmt.Errorf("NAME_REQUIRED: commit_card.name is empty")
	}
	if strings.TrimSpace(p.PrimaryOD) == "" {
		return p, fmt.Errorf("PRIMARY_OD_REQUIRED: commit_card.primaryOd is empty")
	}
	if p.Intent != "aggregate" && p.Intent != "enumerate" {
		return p, fmt.Errorf("INTENT_INVALID: intent 必须是 aggregate 或 enumerate,当前=%q", p.Intent)
	}
	if p.Intent == "aggregate" {
		if p.Measure == nil || p.Measure.Agg == "" {
			return p, fmt.Errorf("MEASURE_REQUIRED: intent=aggregate 时必须填 measure{agg,column}")
		}
		if !validMeasureAggs[p.Measure.Agg] {
			return p, fmt.Errorf("MEASURE_AGG_INVALID: agg=%q 不在 SUM/COUNT/AVG/MIN/MAX/COUNT_DISTINCT 内", p.Measure.Agg)
		}
		// COUNT may omit column (→ COUNT(*)); every other agg needs a column.
		if p.Measure.Column == "" && p.Measure.Agg != "COUNT" {
			return p, fmt.Errorf("MEASURE_COLUMN_REQUIRED: agg=%s 必须指定 column", p.Measure.Agg)
		}
	} else { // enumerate
		if len(p.Dimensions) == 0 {
			return p, fmt.Errorf("DIMENSIONS_REQUIRED: intent=enumerate 时必须填至少一个 dimensions 列")
		}
	}
	return p, nil
}

func commitCardPayloadToMap(p contracts.CommitCardPayload) M {
	// Re-marshal through JSON so the SSE event matches the wire shape
	// exactly (json tags, omitempty, etc.) without a hand-maintained map.
	blob, _ := json.Marshal(p)
	var m M
	_ = json.Unmarshal(blob, &m)
	return m
}

// resolveObjectIDFromLookup walks the lookup tool-result M and returns the
// uuid for primaryOD if present. The result shape mirrors lakehouseToolLookup
// (see handler_agent_lakehouse.go) — a top-level "ontology" key holding a
// list of {name, id, ...} entries.
func resolveObjectIDFromLookup(res M, primaryOD string) string {
	if res == nil || primaryOD == "" {
		return ""
	}
	// Common shape: res["ontology"] -> []interface{} of M{"name", "id", ...}.
	for _, k := range []string{"ontology", "ods", "objects"} {
		raw, ok := res[k]
		if !ok || raw == nil {
			continue
		}
		list, ok := raw.([]interface{})
		if !ok {
			continue
		}
		for _, item := range list {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			name := strOrEmpty(m["name"])
			id := strOrEmpty(m["id"])
			if id == "" {
				id = strOrEmpty(m["object_id"])
			}
			if name != "" && id != "" && strings.EqualFold(name, primaryOD) {
				return id
			}
		}
	}
	return ""
}

// resolveObjectIDByName falls back to a direct SELECT on ont_object_type
// scoped to the project. Used when the LLM emits commit_card without ever
// calling lookup first (legal but rare).
func resolveObjectIDByName(ctx context.Context, db *sql.DB, projectID, odName string) string {
	if strings.TrimSpace(odName) == "" {
		return ""
	}
	var id string
	_ = db.QueryRowContext(ctx, `
		SELECT id::text FROM ont_object_type
		WHERE project_id = $1 AND name = $2
		LIMIT 1`, projectID, odName).Scan(&id)
	return id
}

func exploreSystemPrompt() string {
	// The prompt text lives in services/agent-server/prompts/explore_system.md
	// for editability without recompile, but we inline a fallback so the
	// handler is self-contained for tests / containers without the file.
	const fallback = `你是数据分析师的探索期共创助手。本轮任务：通过 lookup 与用户共同探明数据形状，
当对话收敛到一个清晰、可复用的口径（metric）时，调用 commit_card 工具提交。

铁律：
- 你不写 SQL。commit_card 只描述结构：先判 intent(aggregate/enumerate)；
  aggregate 填 measure{agg,column}+可选 dimensions；enumerate 填 dimensions(要列出的列)。
- 列名用 lookup 返回的属性名(中文或英文都行)。需要别的对象的列(取名字/按关联对象过滤)时,用 OD.列名(如 INGREDIENT.name),引擎会自动 JOIN。服务端负责编译 SQL。
- 问「X 的配方/X 需要什么」这类必须加 filter 限定到 X(如 MENUITEM.name = '拿铁'),不要列出整个目录。先用 inspect 看真实取值再选列/填 filter。
- commit_card.triggerKeywords 至少 2 个用户口头说法。
- 服务端拒绝时(工具结果带 error),按报错修正结构后立刻重新 emit。`

	if path := os.Getenv("EXPLORE_SYSTEM_PROMPT_PATH"); path != "" {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
	}
	// Default location: ./services/agent-server/prompts/explore_system.md when
	// running from the repo root, OR /app/prompts/explore_system.md in the
	// container. Best-effort — fallback wins on miss.
	for _, candidate := range []string{
		"services/agent-server/prompts/explore_system.md",
		"/app/prompts/explore_system.md",
		"prompts/explore_system.md",
	} {
		if data, err := os.ReadFile(candidate); err == nil && len(data) > 0 {
			return string(data)
		}
	}
	return fallback
}

func prependValidatorRejection(systemPrompt string, vr *contracts.ValidatorRejection) string {
	if vr == nil {
		return systemPrompt
	}
	var b strings.Builder
	b.WriteString("前一轮口径校验失败: ")
	b.WriteString(vr.Code)
	b.WriteString("\n")
	if vr.Error != "" {
		b.WriteString(vr.Error)
		b.WriteString("\n")
	}
	for _, e := range vr.Errors {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	b.WriteString("\n请按上述反馈修正本轮 commit_card；不要重复同一错误形状。\n\n")
	b.WriteString(systemPrompt)
	return b.String()
}

// ── Phase 4a stub gate ────────────────────────────────────────────────

func isStubEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("EXPLORE_PHASE_4A_STUB")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func stubShouldEmit(userMsg string) bool {
	low := strings.ToLower(userMsg)
	for _, trigger := range []string{"出卡", "commit", "commit card", "save metric"} {
		if strings.Contains(low, trigger) {
			return true
		}
	}
	return false
}

// emitStubCommitCard is the Phase 4a deterministic codepath. Used only for
// plumbing tests; the name prefix "FIXTURE_DET_STUB" lets AC-11 (d)
// distinguish it from the LLM-driven (4b) path which carries the
// "FIXTURE_LLM_DETERMINISTIC_" prefix set by the fixture script.
func emitStubCommitCard(
	ctx context.Context,
	db *sql.DB,
	threadID, projectID string,
	lastLookup M,
	sendSSEFull func(string, M),
	stepIdx *int,
	userMsg string,
	llmMessages []M,
) {
	_ = userMsg
	_ = llmMessages
	stubArgs := map[string]interface{}{
		"name":             "FIXTURE_DET_STUB_metric",
		"displayName":      "FIXTURE_DET_STUB metric",
		"primaryOd":        "STUB_OD",
		"intent":           "aggregate",
		"measure":          map[string]interface{}{"agg": "COUNT"},
		"triggerKeywords":  []string{"stub-a", "stub-b"},
		"responseTemplate": "{{total}}",
		"description":      "Phase 4a deterministic stub (NEVER ships to prod)",
	}
	payload, _, err := emitCommitCard(ctx, db, projectID, lastLookup, stubArgs, sendSSEFull)
	if err != nil {
		log.Printf("EXPLORE-AGENT(stub): emit failed: %v", err)
		sendSSEFull("error", M{"content": "stub emit failed: " + err.Error()})
		return
	}
	payloadJSON, _ := json.Marshal(payload)
	_, _ = db.ExecContext(ctx, `INSERT INTO ont_agent_step
		(thread_id, step_index, role, content, function_call)
		VALUES ($1, $2, 'assistant', $3, $4::jsonb)`,
		threadID, *stepIdx, "[commit_card emitted (stub)]", string(payloadJSON))
	*stepIdx++
	sendSSEFull("done", M{"modelName": "stub"})
}

// ── tiny helpers ─────────────────────────────────────────────────────

func strOrEmpty(v interface{}) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func anyToStrings(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// listProjectODs returns the list of OD names (uppercase preserved) for the
// project that have mark=true. Used to anchor the forced-final-round prompt
// against LLM OD-name hallucinations.
func listProjectODs(ctx context.Context, db *sql.DB, projectID string) []string {
	rows, err := db.QueryContext(ctx, `
		SELECT name FROM ont_object_type
		 WHERE project_id = $1 AND COALESCE(mark, true) = true
		 ORDER BY name
	`, projectID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// executeAndEmitMetricResult runs the drafted SQL, emits the metric_result
// SSE event, and feeds the head rows back into the LLM context as a
// tool_result + answer-the-user nudge. Returns true on success.
//
// On SQL execution failure: DELETES the just-persisted lakehouse_metric row
// (and its triggers/keywords via cascade) so a broken draft never lingers
// in the registry, then feeds the error back to the LLM and returns false.
// The caller should clear emittedCardThisTurn so the LLM can retry with a
// corrected commit_card in the next round.
//
// userQuestion is interpolated into the closing prompt so the LLM produces a
// real analytical answer ("which store leads", "by how much", "what's the
// impact") instead of a hand-wave ("please see the draft on the right").
// emitMetricResultFromExec emits the metric_result SSE from the engine's
// already-executed rows and feeds them back so the LLM writes the closing
// 4-section analysis. Execution + validation happened in emitCommitCard (any
// failure surfaced there as emitErr), so this path is the success path only —
// no SQL is run here, and no rollback is needed.
func emitMetricResultFromExec(
	ctx context.Context,
	db *sql.DB,
	payload contracts.CommitCardPayload,
	exec *draftExecResult,
	toolCallID string,
	userQuestion string,
	sendSSEFull func(string, M),
	llmMessages *[]M,
) {
	_ = ctx
	_ = db
	rows := []M{}
	cols := []string{}
	if exec != nil {
		rows = exec.rows
		cols = exec.columns
	}
	sendSSEFull("metric_result", M{
		"draftId":     payload.DraftID,
		"columns":     cols,
		"rows":        rows,
		"totalRows":   len(rows),
		"executedSql": payload.QuerySql,
	})
	headRows := rows
	if len(headRows) > 12 {
		headRows = headRows[:12]
	}
	resJSON, _ := json.Marshal(M{
		"ok":        true,
		"draftId":   payload.DraftID,
		"id":        payload.ID,
		"columns":   cols,
		"rows":      headRows,
		"truncated": len(rows) > len(headRows),
	})
	*llmMessages = append(*llmMessages, llmclient.BuildToolResultMessage(toolCallID, string(resJSON)))
	*llmMessages = append(*llmMessages, M{
		"role": "user",
		"content": "草稿已入库,口径已由引擎编译并执行,真实数据如下:\n" + string(resJSON) +
			"\n\n现在请基于这些真实数据回答用户最初的问题: 「" + userQuestion + "」" +
			"\n\n回答必须以下 **4 个章节**完整覆盖,用 Markdown 二级标题(`##`)组织:" +
			"\n\n## 思路" +
			"\n- 我把『" + userQuestion + "』这个问题在数据上理解为: ...(把自然语言映射到结构化语义)" +
			"\n- 我为什么选 `" + payload.PrimaryOD + "` 这个 OD: ...(从问题里的实体名→对象类型)" +
			"\n- 我为什么用 `" + payload.CanonicalMetric + "` 这个度量 + GROUP BY = ...: ...(为什么这个 measure 能回答问题)" +
			"\n\n## 数据解读" +
			"\n- 上面 `columns` 里每个字段在业务上是什么含义(用人话解释,不要照抄技术名)" +
			"\n- 这些列**为什么足以回答用户的问题** — 把列名映射回用户语境,论证「这就是用户要的东西」" +
			"\n- 如果列名含义不直观(例如 `category`、`status` 等),必须解释它的取值代表什么" +
			"\n\n## 关键发现" +
			"\n- 引用至少 2 个**真实数字**(从上面 rows 里直接挑)" +
			"\n- 给出**排名 / 占比 / 趋势 / 异常**等用户可直接用的洞察" +
			"\n- 用 Markdown 表格或列表组织,数字加粗" +
			"\n\n## 不确定与提示" +
			"\n- 这个回答的边界 / 假设 / 数据限制(数据时效、列名歧义、覆盖度)" +
			"\n- 用户如果想深挖,**下一步**可以问什么" +
			"\n\n禁止:" +
			"\n- 不要说「请在右栏查看草稿」「采纳后就能复用」这类**推脱话** — 用户能看到右栏,他们要的是**分析结论**不是导航" +
			"\n- 不要再调工具,直接以 markdown 文字结束本轮",
	})
}

// emitStreamingToken splits a finished LLM text into small chunks and emits
// them as separate `token` SSE events so the frontend gets a streaming feel.
// We don't have token-level streaming with tool calls in this code path, so
// this is a UX approximation — splits at ~6-char boundaries (respecting
// rune boundaries), 18ms gap, capped at ~3s total wall-clock.
func emitStreamingToken(text string, sendSSEFull func(string, M)) {
	const chunkRunes = 6
	const gapMS = 18
	const maxTotalMS = 3000
	runes := []rune(text)
	totalChunks := (len(runes) + chunkRunes - 1) / chunkRunes
	gap := gapMS
	if totalChunks > 0 && totalChunks*gap > maxTotalMS {
		gap = maxTotalMS / totalChunks
		if gap < 1 {
			gap = 1
		}
	}
	for i := 0; i < len(runes); i += chunkRunes {
		end := i + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		sendSSEFull("token", M{"content": string(runes[i:end])})
		if end < len(runes) {
			time.Sleep(time.Duration(gap) * time.Millisecond)
		}
	}
}

func pgStringArray(ss []string) interface{} {
	if len(ss) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
