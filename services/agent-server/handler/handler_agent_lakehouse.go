package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/agent-server/smartquery"
	"github.com/lakehouse2ontology/services/agent-server/lakehouse"
	"github.com/lakehouse2ontology/services/agent-server/builder_ledger"
	"github.com/lakehouse2ontology/services/agent-server/ledger"
	"github.com/lakehouse2ontology/services/agent-server/recall"
	"github.com/lakehouse2ontology/services/agent-server/synthesizer"

	. "github.com/lakehouse2ontology/httputil"

	"go.opentelemetry.io/otel/attribute"
)

// synthFollowUpMaxFails caps Synthesizer retries before falling back to the
// legacy prose path. Conservative=3: lets the outer LLM see gap feedback up
// to 3 times, then surrenders so the user still gets an answer.
const synthFollowUpMaxFails = 3

// lookupToolDescription is the LLM-facing description for the `lookup` tool.
// Kept as a backtick-quoted const so we don't fight escape rules for the
// embedded examples and Chinese punctuation.
const lookupToolDescription = `查询本体定义 + 业务关键词。
• ontology_name — 按 Od/Ok 名搜本体（如 ["Customer","Track"]）
• keyword — 按业务术语搜 lakehouse_keyword 表（精确→模糊→项目级→向量 4 级降级，如 ["摇滚","早单"]）
通常 context 已含 Od 信息时不必调用；遇到生疏术语 / 找不到属性 / 想确认值映射时再 lookup。`

// smartqueryToolDescription is the LLM-facing description for the
// `smartquery` tool in **strict mode**. The contract is intentionally tiny:
// LLM picks an Intent by name and fills its declared params. The server
// owns metric / groupBy / orderBy / limit / canonical filters / SQL gen.
// LLM never builds spec — that path is closed.
const smartqueryToolDescription = `执行数据查询，返回表格结果（严格模式）。

调用形式：{"intent":"Intent名","params":{...}}

intent — 必填。从 context 顶部「🎯 查询意图（Metric Intent）」小节列出的 Intent name 中选一个；
       未匹配到任何 Intent 的查询会被 INTENT_NOT_FOUND 显式拒绝（不允许 ad-hoc 查询）。

params — 按 Intent 声明的 parameters schema 填，用户问题没提到的省略走 Intent 默认值。
       常见参数（具体以 🎯 小节内 parameters 列表为准）：
         n        Top N 数值，用户说 "Top 5" 时填 5
         genre    流派/类别名，用户提到具体值时填
         country  国家/地区名，用户提到具体值时填

调用示例：
  "Top 5 摇滚乐手"   → {"intent":"Sales.ByArtist","params":{"n":5,"genre":"Rock"}}
  "卖得最好的国家"  → {"intent":"Sales.ByCountry","params":{}}
  "总营收是多少"    → {"intent":"Sales.Total","params":{}}

严格规则：
  1. 不能填 metric/groupBy/filters/orderBy/limit — Intent 已含这些；额外字段会被 TOOL_ARGS_INVALID 拒绝
  2. params 里的 key 必须在 Intent 的 parameters schema 内；未声明的 key 会被 PARAM_UNKNOWN 拒绝
  3. params 值类型不匹配（如 n 填 "abc" 不是数字）会被 PARAM_TYPE_ERROR 拒绝`

// smartqueryExecutor is the cross-service surface of lakehouse.RemoteClient.
// Post-Phase-1 D4b: the monolith no longer hosts a local smartquery engine —
// LAKEHOUSE_SQL_URL must point at a reachable lakehouse-sql-server. smartqueryExec
// log.Fatal's at first use if either LAKEHOUSE_SQL_URL or INTERNAL_TOKEN is
// missing so misconfig surfaces immediately rather than as silent empty results.
type smartqueryExecutor interface {
	Execute(ctx context.Context, spec smartquery.QuerySpec) lakehouse.LakehouseResult
	ExecutePlan(ctx context.Context, planJSON []byte, params map[string]string, projectID string) lakehouse.LakehouseResult
}

var (
	smartqueryExecOnce   sync.Once
	smartqueryExecCached smartqueryExecutor
)

// smartqueryExec returns the remote client used by lakehouseToolSmartQuery
// and the global-total pass. Cached on first call. The *sql.DB param is
// retained for source-compatibility with in-process callsites but is no
// longer used — smartquery runs exclusively in lakehouse-sql-server now.
func smartqueryExec(_ *sql.DB) smartqueryExecutor {
	smartqueryExecOnce.Do(func() {
		url := os.Getenv("LAKEHOUSE_SQL_URL")
		if url == "" {
			log.Fatal("LAKEHOUSE_SQL_URL is required — monolith no longer ships an in-process smartquery engine after Phase 1 D4b. Set LAKEHOUSE_SQL_URL=http://127.0.0.1:18094 (or your deployment's lakehouse-sql-server URL).")
		}
		token := os.Getenv("INTERNAL_TOKEN")
		if token == "" {
			log.Fatal("INTERNAL_TOKEN is required for /internal/* auth on lakehouse-sql-server.")
		}
		log.Printf("   SmartqueryExec: remote → %s", url)
		smartqueryExecCached = &lakehouse.RemoteClient{
			BaseURL:    url,
			Token:      token,
			OnBehalfOf: "monolith-internal",
			HTTP:       &http.Client{Timeout: 60 * time.Second},
		}
	})
	return smartqueryExecCached
}

// autoInvokeSynthesize runs the synthesize tool as a separate boundary right
// after smartquery returns. It:
//
//  1. Builds synth args from smartquery result + the LLM's smartquery args
//  2. Calls the dispatchTool closure with name="synthesize"
//  3. Emits a function_call SSE event so UI shows it as its own step
//  4. Persists a separate ont_agent_step record (debuggable)
//  5. Returns the user-message body to inject into llmMessages (or "" to skip)
//
// synthFailCount is mutated through the pointer when the gate fails; caller
// keeps the counter scoped to the request handler.
func autoInvokeSynthesize(
	dispatchTool func(string, map[string]interface{}) M,
	sendSSEFull func(string, M),
	saveRoundStep func([]M, string, string, M, int, int, int, int64),
	sentMsgsSnapshot []M,
	userQuestion string,
	smartqueryResult M,
	smartqueryArgs map[string]interface{},
	synthFailCount *int,
	startTime time.Time,
) string {
	if smartqueryResult == nil {
		return ""
	}
	// Skip synth on ANY failure mode. Each check below catches a distinct
	// failure class — missing all of them would let synth hallucinate "未查询
	// 到数据" on an SQL error or auth failure.
	//
	//   error           → tool-arg validation failure (top-level error key)
	//   execution_status≠ok → smartquery executor reported failure
	//   execution_error ≠"" → SQL/network/parse error from PG
	//   total_rows  == 0    → empty result; LLM should self-correct (loosen
	//                         filters, retry) before any prose is composed
	if errVal, hasErr := smartqueryResult["error"]; hasErr && errVal != nil {
		return ""
	}
	if status, _ := smartqueryResult["execution_status"].(string); status != "" && status != "success" {
		return ""
	}
	if execErr, _ := smartqueryResult["execution_error"].(string); execErr != "" {
		return ""
	}
	switch v := smartqueryResult["total_rows"].(type) {
	case int:
		if v == 0 {
			return ""
		}
	case float64: // JSON round-trip path
		if v == 0 {
			return ""
		}
	}
	// Suspicious all-zero result: skip synth so the agent loop sees the
	// smartquery tool result's suspicious_zero_hint and self-corrects,
	// instead of receiving a locked "echo this answer" instruction for a
	// number that is almost certainly an unmatched JOIN.
	if h, _ := smartqueryResult["suspicious_zero_hint"].(string); h != "" {
		return ""
	}

	synthArgs := map[string]interface{}{
		"userQuestion":   userQuestion,
		"smartqueryResp": smartqueryResult,
		"smartqueryArgs": smartqueryArgs,
	}
	synthResult := dispatchTool("synthesize", synthArgs)

	// Surface as its own SSE function_call event + agent_step record.
	fcRecord := M{"name": "synthesize", "arguments": synthArgs, "result": synthResult}
	sendSSEFull("function_call", fcRecord)
	saveRoundStep(sentMsgsSnapshot, "", "", fcRecord, 0, 0, 0, time.Since(startTime).Milliseconds())

	msg, didFail := formatSynthMessage(synthResult, *synthFailCount)
	if didFail {
		*synthFailCount++
	}
	return msg
}

// runSynthesizeTool is the dispatchTool handler for the auto-invoked
// "synthesize" tool. Args carry the smartquery context the loop captured:
//
//	{
//	  userQuestion: string,        // verbatim user prompt
//	  smartqueryResp: M,           // full smartquery tool result
//	  smartqueryArgs: M,           // raw LLM args to smartquery (for fallback)
//	}
//
// Output mirrors synthesizer.Result fields under M keys (passed/answer/gaps/
// checksRun) so the caller can inspect verdict and the UI / agent_step can
// render the synthesize step as a discrete tool boundary.
func runSynthesizeTool(db *sql.DB, args map[string]interface{}) M {
	userQuestion, _ := args["userQuestion"].(string)
	resp, _ := args["smartqueryResp"].(M)
	if resp == nil {
		return M{"passed": false, "gaps": []synthesizer.Gap{{
			Type: "compose_failed", Detail: "synthesize tool: smartqueryResp missing",
			Recommendation: "rewrite_prose",
		}}, "checksRun": 0}
	}

	// Extract smartquery resp fields. Spec metadata exposed via "_spec_*"
	// keys by lakehouseToolSmartQuery (loop-friendly accessors).
	resultJSON, _ := resp["execution_result"].(string)
	pivotedInfo, _ := resp["pivoted"].(M)
	matchedIntentName, _ := resp["matched_intent"].(string)
	metric, _ := resp["_spec_metric"].(string)
	var groupBy []string
	if gb, ok := resp["_spec_groupBy"].([]string); ok {
		groupBy = gb
	}
	var filters []synthesizer.FilterRef
	if fs, ok := resp["_spec_filters"].([]M); ok {
		for _, f := range fs {
			fr := synthesizer.FilterRef{}
			if v, ok := f["prop"].(string); ok {
				fr.Prop = v
			}
			if v, ok := f["op"].(string); ok {
				fr.Op = v
			}
			if v, ok := f["value"].(string); ok {
				fr.Value = v
			}
			filters = append(filters, fr)
		}
	}

	// Pivot field accessors (pivotedInfo may be nil when no Intent fired).
	getStr := func(m M, k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	getStrSlice := func(m M, k string) []string {
		if v, ok := m[k].([]string); ok {
			return v
		}
		return nil
	}

	// Parse rows. On parse failure rows is nil — Synthesizer's rows-aware
	// gates degrade gracefully.
	var rows []map[string]interface{}
	_ = json.Unmarshal([]byte(resultJSON), &rows)

	// (summary aggregates no longer appear inline in rows — they ride on
	// resp["summary_toon"]. Nothing to strip here.)

	// Pull row_summary from resp (computed by lakehouseToolSmartQuery before
	// preview truncation). Synth's compose prompt surfaces it so the LLM
	// reports "共 X 个产品" using distinct_dim_count instead of len(rows).
	rowSummary, _ := resp["row_summary"].(M)

	in := synthesizer.Input{
		Question:     userQuestion,
		UserTerms:    synthesizer.ExtractIndicators(userQuestion),
		Metric:       metric,
		GroupBy:      groupBy,
		Filters:      filters,
		IntentName:   matchedIntentName,
		IntentSuffix: getStr(pivotedInfo, "percentSuffix"),
		PercentAxis:  getStr(pivotedInfo, "percentAxis"),
		PercentScope: getStr(pivotedInfo, "percentScope"),
		ResponseTpl:  getStr(pivotedInfo, "responseTemplate"),
		Rows:         rows,
		PivotColumns: getStrSlice(pivotedInfo, "orderedLabels"),
		TotalLabel:   getStr(pivotedInfo, "totalLabel"),
		RowSummary:   map[string]interface{}(rowSummary),
	}
	res := synthesizer.Run(db, in)
	out := M{
		"passed":    res.Passed,
		"checksRun": res.ChecksRun,
	}
	if res.Passed {
		out["answer"] = res.Answer
	} else {
		out["gaps"] = res.Gaps
	}
	return out
}

// formatSynthMessage formats the user-message content that follows a successful
// auto-invoked synthesize tool dispatch. The synth result M comes from the
// dispatchTool("synthesize", ...) call. Returns ("", false) to signal "no
// extra message needed" (smartquery errored, no answer, or fallback).
//
//   - passed=true  → "echo synth_answer verbatim" instruction (conservative
//     mode signoff — outer LLM still runs but with locked instruction)
//   - passed=false && failCount<max → gap feedback + retry instruction
//   - passed=false && failCount>=max → "" (fall back to outer LLM's own prose
//     path; smartquery tool_result already in conversation)
func formatSynthMessage(synthResult M, synthFailCount int) (content string, didFail bool) {
	if synthResult == nil {
		return "", false
	}
	passed, _ := synthResult["passed"].(bool)
	if passed {
		ans, _ := synthResult["answer"].(string)
		if ans != "" {
			return "Synthesizer 已生成通过 mechanical gates 的回复。**请直接 echo 输出以下内容**（不要修改、不要重新解释、不要补充）：\n\n" + ans, false
		}
		return "", false
	}
	if synthFailCount >= synthFollowUpMaxFails {
		return "", false
	}
	gaps, _ := synthResult["gaps"].([]synthesizer.Gap)
	if len(gaps) == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("⚠ Synthesizer 自检未通过（第 %d/%d 次重试）。请根据以下 gaps 修正回复 prose（**不必**再调用工具）：\n",
		synthFailCount+1, synthFollowUpMaxFails))
	for _, g := range gaps {
		b.WriteString(fmt.Sprintf("- [%s] %s — 建议: %s\n", g.Type, g.Detail, g.Recommendation))
	}
	return b.String(), true
}

// handleAgentStreamLakehouse implements the lakehouse "book-flipping" agent.
// System prompt contains only the Topic L0 index; LLM navigates with 4 tools:
// lookup, read, request_query, smartquery.
func handleAgentStreamLakehouse(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}

		// agent.turn — the outermost span for one lakehouse-agent turn.
		// Wraps the entire SSE streaming lifecycle; child spans (recall,
		// smartquery, ledger, recall) surface as children. P1b closes the
		// monolith-side ctx propagation — the tool helpers receive ctx as
		// their first param, so in-process and cross-service spans both
		// nest under agent.turn.
		ctx, turnSpan := observability.Tracer().Start(r.Context(), "agent.turn")
		defer turnSpan.End()
		turnStart := time.Now()
		// SSEStreamDuration observation covers the whole streaming window.
		defer func() {
			observability.SSEStreamDuration.Observe(float64(time.Since(turnStart).Milliseconds()))
		}()

		CorsHeaders(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			observability.SSEStreamErrors.WithLabelValues("no_flusher").Inc()
			http.Error(w, "streaming not supported", 500)
			return
		}

		sendSSE := func(eventType string, data interface{}) {
			jsonData, _ := json.Marshal(M{"type": eventType, "content": data})
			if _, err := fmt.Fprintf(w, "data: %s\n\n", jsonData); err != nil {
				observability.SSEStreamErrors.WithLabelValues("write_error").Inc()
			}
			flusher.Flush()
		}
		sendSSEFull := func(eventType string, payload M) {
			payload["type"] = eventType
			jsonData, _ := json.Marshal(payload)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", jsonData); err != nil {
				observability.SSEStreamErrors.WithLabelValues("write_error").Inc()
			}
			flusher.Flush()
		}

		body := ReadBody(r)
		projectID := StrVal(body, "projectId")
		threadID := StrVal(body, "threadId")
		mode := StrVal(body, "mode") // "" / "lakehouse" / "builder"
		// Record turn attributes now that we've parsed the body.
		turnSpan.SetAttributes(
			attribute.String("project_id", projectID),
			attribute.String("thread_id", threadID),
		)
		if !IsValidUUID(projectID) {
			sendSSE("error", "projectId required")
			return
		}

		rawMsgs, _ := body["messages"].([]interface{})
		type chatMsg struct{ Role, Content string }
		var bodyMessages []chatMsg
		for _, rm := range rawMsgs {
			if m, ok := rm.(map[string]interface{}); ok {
				bodyMessages = append(bodyMessages, chatMsg{fmt.Sprintf("%v", m["role"]), fmt.Sprintf("%v", m["content"])})
			}
		}
		if len(bodyMessages) == 0 {
			sendSSE("error", "messages required")
			return
		}
		userQuestion := bodyMessages[len(bodyMessages)-1].Content

		// Create or get thread. agent_type is authoritative from DB for existing
		// threads; body.mode only seeds new threads. Defaults to 'lakehouse'.
		agentType := "lakehouse"
		if !IsValidUUID(threadID) {
			if mode == "builder" {
				agentType = "builder"
			}
			title := userQuestion
			if len([]rune(title)) > 50 {
				title = string([]rune(title)[:50]) + "..."
			}
			if err := db.QueryRow(`INSERT INTO ont_agent_thread (project_id, title, agent_type) VALUES ($1, $2, $3) RETURNING id`,
				projectID, title, agentType).Scan(&threadID); err != nil {
				log.Printf("LAKEHOUSE-AGENT: failed to create thread: %v", err)
				sendSSE("error", "创建对话失败: "+err.Error())
				return
			}
		} else {
			// Existing thread: read agent_type from DB; body.mode is ignored.
			db.QueryRow(`SELECT COALESCE(agent_type,'lakehouse') FROM ont_agent_thread WHERE id = $1`, threadID).Scan(&agentType)
			db.Exec(`UPDATE ont_agent_thread SET updated_at = now() WHERE id = $1`, threadID)
		}
		sendSSEFull("thread", M{"threadId": threadID, "agentType": agentType})

		// MissionAct M1 — shadow mission (turn start). Behind USE_MISSION_ACT;
		// nil + no-op when the flag is off. Best-effort: never fails the turn.
		// See mission_shadow.go.
		shadowM := newShadowMission(ctx, db, threadID, projectID, userQuestion)

		// ── Branch-thread detection ──
		// If this thread has parent_thread_id in thread_state, it's a clarification
		// child thread and must use the distilled seed prompt (no main system prompt,
		// no T_parent history). This enforces context isolation at the messages[] level.
		//
		// Note: the legacy `fully_loaded_ods` key in thread_state is intentionally
		// no longer read — it was never populated (no writer existed; the previous
		// reader's text[] cast also errored silently). The ledger replaces it.
		var branchParentID, branchSeedPrompt string
		db.QueryRow(`SELECT COALESCE(thread_state->>'parent_thread_id',''),
		                    COALESCE(thread_state->>'seed_system_prompt','')
		             FROM ont_agent_thread WHERE id = $1`, threadID).Scan(&branchParentID, &branchSeedPrompt)
		isBranchThread := IsValidUUID(branchParentID) && branchSeedPrompt != ""

		// Load conversation history from DB.
		//
		// Cross-turn tool results are NOT injected as user-message stubs any
		// more — that was the 500-char-truncation path that caused the LLM
		// to re-lookup Ods it had already loaded. The Thread Memory Ledger
		// (see ontology/ledger) now carries that structured context across
		// turns, so the only thing replayed here is the raw user/assistant
		// conversation content.
		var messages []chatMsg
		histRows, _ := db.Query(`SELECT role, COALESCE(content,'')
			FROM ont_agent_step WHERE thread_id = $1 ORDER BY step_index`, threadID)
		if histRows != nil {
			for histRows.Next() {
				var role, content string
				histRows.Scan(&role, &content)
				messages = append(messages, chatMsg{Role: role, Content: content})
			}
			histRows.Close()
		}

		messages = append(messages, chatMsg{Role: "user", Content: userQuestion})

		// Compute step index (DB save happens after annotation so we store enriched content)
		var stepIdx int
		db.QueryRow(`SELECT COALESCE(MAX(step_index),0) FROM ont_agent_step WHERE thread_id = $1`, threadID).Scan(&stepIdx)
		stepIdx++

		sendSSE("thinking", "正在分析问题实体...")

		// ── Pre-processing: annotation fewshot → LLM tokenize → ledger-aware recall ──
		//
		// The Thread Memory Ledger is loaded, lazily rebuilt from history if
		// empty, and passed into recall as a CachedContext. Tokens already
		// strongly resolved earlier in the thread skip DB work entirely; only
		// hot (new / weakly-seen) tokens go through the full 3-tier recall.
		// After recall returns, new hits are merged into the ledger, and the
		// ledger is persisted at turn-end (not here — see the end of the
		// streaming loop, around the `done` SSE event).
		//
		// recallContextMD is ephemeral: injected into llmMessages for the
		// current round only; history user steps store the raw userQuestion.
		var recallContextMD string
		var threadLedger *ledger.Ledger
		var ledgerOldVersion int
		// recallResult is hoisted to handler scope so plan-mode tools
		// (start_analysis_plan) can look up analysis_pattern OK cards in
		// recallResult.OkEntries. Populated inside the lakehouse ledger block.
		var recallResult recall.RecallResult

		// Builder ledger — parallel to threadLedger but for builder agent mode.
		var threadBuilderLedger *builder_ledger.BuilderLedger
		var builderLedgerOldVersion int
		var builderContextMD string

		if !isBranchThread && agentType == "lakehouse" {
			// Load ledger; lazy-rebuild if this is a legacy thread with prior
			// steps but no ledger yet.
			l, err := ledger.Load(ctx, db, threadID)
			if err != nil {
				log.Printf("LAKEHOUSE-AGENT: ledger.Load error (continuing with empty ledger): %v", err)
				l = ledger.New()
			}
			ledgerOldVersion = l.Version
			if l.IsEmpty() && l.RebuiltFromStep == 0 {
				// Check whether this thread has prior user steps that would
				// seed a meaningful ledger. The handler just INSERTed the
				// current step at stepIdx, so count < stepIdx means legacy
				// content exists.
				var priorUserSteps int
				db.QueryRow(`SELECT COUNT(*) FROM ont_agent_step WHERE thread_id = $1 AND role = 'user'`, threadID).Scan(&priorUserSteps)
				if priorUserSteps > 1 { // >1 because the current turn's user row is already in (see below)
					tokenize := func(q string) []string {
						fs := loadAnnotationFewShots(db, projectID, q)
						return tokenizeWithAnnotationFewShots(db, projectID, q, fs)
					}
					doRecall := func(tokens []string, question string) recall.RecallResult {
						return recall.BuildLakehouseContext(ctx, db, projectID, tokens, question)
					}
					if replayed, rerr := ledger.RebuildFromSteps(db, threadID, 20, tokenize, doRecall, l); rerr != nil {
						log.Printf("LAKEHOUSE-AGENT: ledger rebuild error: %v", rerr)
					} else {
						log.Printf("LAKEHOUSE-AGENT: ledger rebuilt from %d steps for thread %s", replayed, threadID)
					}
				}
			}
			// Bump turn count BEFORE recall so new entries are stamped with
			// the correct turn number.
			l.TurnCount++

			fewShots := loadAnnotationFewShots(db, projectID, userQuestion)
			tokens := tokenizeWithAnnotationFewShots(db, projectID, userQuestion, fewShots)
			go saveAnnotation(db, projectID, threadID, userQuestion, tokens, nil)

			cached := ledger.BuildCachedContext(l)
			recallResult = recall.BuildLakehouseContextCached(ctx, db, projectID, tokens, userQuestion, cached)
			l.MergeRecallResult(recallResult, l.TurnCount)
			threadLedger = l

			// Render with ledger-aware formatter: "🧠 线程记忆" header + body +
			// optional "📚 线程其它记忆" orphan footer.
			recallContextMD = ledger.FormatContextWithLedger(recallResult, tokens, userQuestion, l, l.TurnCount)
		}

		// Builder ledger — loaded for builder threads (parallel to the query ledger block above).
		if !isBranchThread && agentType == "builder" {
			bl, err := builder_ledger.Load(ctx, db, threadID)
			if err != nil {
				log.Printf("BUILDER-AGENT: builder_ledger.Load error (continuing with empty ledger): %v", err)
				bl = builder_ledger.New()
			}
			builderLedgerOldVersion = bl.Version
			bl.TurnCount++
			threadBuilderLedger = bl
			builderContextMD = bl.FormatPrefix()

			// Defer save so it fires on EVERY exit path (including LLM-error early
			// return). The reapply closure captures threadBuilderLedger by ref so
			// any merges that happened during the turn (even partial) get persisted.
			defer func() {
				blReapply := func(fresh *builder_ledger.BuilderLedger) *builder_ledger.BuilderLedger {
					return threadBuilderLedger
				}
				if err := builder_ledger.SaveWithRetry(ctx, db, threadID, threadBuilderLedger, builderLedgerOldVersion, blReapply, 2); err != nil {
					log.Printf("BUILDER-AGENT: builder_ledger save (deferred) failed for thread %s: %v", threadID, err)
				}
			}()
		}

		// Save raw userQuestion to DB — recall context is ephemeral and must not be stored.
		// mission_id is the shadow mission's id (NULL when USE_MISSION_ACT is off).
		db.Exec(`INSERT INTO ont_agent_step (thread_id, step_index, role, content, mission_id)
			VALUES ($1, $2, 'user', $3, $4)`,
			threadID, stepIdx, userQuestion, nullableMissionID(shadowM))

		sendSSE("thinking", "正在加载知识目录...")

		// Load LLM config — single "agent" role binding (was: 3-tier fallback
		// across ok_workbench/ont_route/sql_generate, simplified 2026-04-20).
		baseURL, apiKey, modelName, _, isToolCall, vendor := llmclient.GetConfigForRole(db, "agent")

		// Get project name for system prompt
		var projectName string
		db.QueryRow(`SELECT COALESCE(name,'') FROM project WHERE id = $1`, projectID).Scan(&projectName)
		if projectName == "" {
			projectName = "当前项目"
		}

		xmlToolSection := ""
		if !isToolCall {
			xmlToolSection = `
## 工具调用格式

<function_call>
{"name":"工具名","arguments":{...}}
</function_call>

### smartquery — 执行数据查询（严格模式）
{"intent":"Intent 名","params":{...}}

intent  从 context 顶部「🎯 查询意图（Metric Intent）」里挑一个 name；未匹配则该查询不在覆盖范围
params  按 Intent 的 parameters schema 填，常见 key：
        n        Top N（用户说"Top 5"填 5）
        genre    流派/类别（用户提到具体值时填）
        country  国家/地区（用户提到具体值时填）
        其它请看 🎯 小节里 Intent 自带的 parameters 列表

例：
"Top 5 摇滚乐手"  → {"intent":"Sales.ByArtist","params":{"n":5,"genre":"Rock"}}
"卖得最好的国家" → {"intent":"Sales.ByCountry","params":{}}

不要填 metric/groupBy/filters/orderBy/limit —— 由 Intent 决定，多填会被拒绝。

### lookup — 查本体定义 / 业务关键词（context 不足或确认值映射时）
{"ontology_name":["Od/Ok 名"], "keyword":["业务术语"]}
`
		}

		odCatalog := buildODCatalogBlock(ctx, db, projectID)
		systemPrompt := `你是 ` + projectName + ` 的数据湖仓分析助手。

` + odCatalog + `

## 工作方式

用户的问题已经过预处理，相关的数据对象（Od）和知识（Ok）已在【已识别的数据上下文】中提供。

**数据类问题**（查数量、排名、趋势等）：
- 直接根据上下文中的 Od 和属性调用 smartquery 执行查询，不需要先 lookup
- 如上下文中信息不足，可调用 lookup 补充
- **不确定查询口径/逻辑时**：查看下方【已习得的业务经验】中是否有相关经验关键词，如有则调用 lookup(keyword=["经验关键词"]) 获取完整经验后再执行查询

**知识类问题**（概念解释）：
- 直接根据上下文中的知识参考回答

**上下文不足 / 不确定口径**：
- 先检查【已习得的业务经验】中的经验关键词，调用 lookup 查询相关经验
- 如果没有匹配的经验，再告知用户未能识别其问题中的具体指标或维度

**用户请求"记住 / 学习 / 总结经验"等**：
- 查询模式 (Agent) 不负责知识沉淀，只负责回答数据问题
- 礼貌告知用户：知识录入请到本体管理 / 建模流程

## 已完整加载的数据对象（Od）

以下对象已在对话历史中通过 lookup 完整加载过，包含所有属性和业务规则。**请优先使用这些信息，避免重复 lookup**：

FULLY_LOADED_ODS_PLACEHOLDER

## 查询工具策略

smartquery / compose_query 的调用契约见各自工具自带说明，这里只讲策略：
- 优先 smartquery（命中 context 顶部「🎯 查询意图」小节的 Intent）。Intent 没完美覆盖所有维度时，**仍先选最接近的调一次**——reflect 会评估、不匹配会指路补救；不要因为没完美 Intent 就回"无法处理"。
- reflect 判 mismatch、re_recall 也没更好 Intent 时，用 compose_query 自由组合。
- 只有 🎯 候选集**完全为空**时才告知用户"当前查询超出已配置范围"。

## 歧义处理

如果【已识别的数据上下文】中出现 "### ⚠ 需要澄清" 小节：
1. 列出候选 Od 及其描述，请用户明确业务场景
2. 用户回答后，直接根据用户选择调用 smartquery（不切子线程）

## smartquery 之后的自省（必读）

每次 smartquery 后服务端自动调一次 reflect_query_result，结果以 follow-up message 追加进对话。**必须读它**：
- **verdict=match**：服务端接着自动调 synthesize 给结构化模板，直接据此写中文答复，**不要再调工具**。
- **verdict=mismatch**：按 follow-up 的 missing_dimensions + 推荐动作补救——优先 re_recall(hints) 找更合适 Intent，仍不行用 compose_query，再不行就给当前最佳答案 + 一句话说明缺失维度让用户拍板。**最多 2 轮自我修正**，超过就收尾给答案。
- **verdict=uncertain**：直接答用户，但回复里含蓄提示结果可能不全。
**反例**（绝不要做）：reflect 说 mismatch，你照样答用户总数 → 答非所问，不可接受。

## 错误恢复

工具返回 error code 时，按 error 文案修正后重试（参数类错误改 params；INTENT_NOT_FOUND 核对 🎯 小节里的 name 拼写）。
**SPEC_VALIDATION_FAILED / 服务端 SQL 报错**：Intent 配置或数据有问题，告知用户并请求指导，**不要**反复重试。

**编号回复规则**：当你给出了编号选项（1. XXX 2. YYY），用户回复纯数字（如 "2"）就是选择该编号，不要再次确认。

## 结果解读规则（重要）

**row_summary**：smartquery 响应里的 row_summary.note 已扣除 0 数据空行；回复"共 X 条/X 个"等数量时**直接读它**，不要自己数表格。

**指标语义**：忠实于 Intent.canonical_metric 的函数名：
- sum(X) = 求和（单位 = X 的业务单位）；avg/min/max = 平均/最小/最大；**绝不**报成"合计"
- 维度名直接读 SQL 列名（结果表头），不擅自翻译

**summary_toon**：pivot Intent 命中 + 占比开启时响应携带的 TOON 块（含「筛选合计 / 全局合计」），写汇总数字 / 占比锚点时**直接引用**，不要去表格行里找。

**占比回复双口径**：用户问"占比/比例/份额/贡献/分布"时，回复**同时给出两个口径**：
1. 分组内占比（横向，本行内部各类别之和=100%）
2. 占总量的占比（纵向，分母=所有分组该指标合计）
即使用户只问一个方向也补上另一个，并用一句话点明差异。

## 数据模板：用引用代替数字（必读，回复质量红线）

每个 smartquery / compose_query 成功结果在【输出】里都带一个 ` + "`id=tN`" + `（如 t1、t2）。
当你在最终答复里需要报告**来自查询结果的数值**时，**绝对不要手写数字** —— 改写成引用，
前端会把引用渲染成真值：

- 标量（求和 / 平均 / 计数 / 极值）：「sum(t1.列名)」「avg(t1.列名)」「count(t1.列名)」「min(t1.列名)」「max(t1.列名)」
- 整张表：「t1」 —— 把结果表 t1 原样内联
- 单元格：「t1.列名[行号]」 —— 结果表 t1 第「行号」行（0 起算）那一列的值

列名一律用结果【输出】表头里出现的真实列名（如 amount、city）。引用整体用全角方括号「」包住。

例：
- ✗ 错：受冲击毛营收是 8,380,820 元。
- ✓ 对：受冲击毛营收是「sum(t1.amount)」元。
- ✗ 错：（手抄一张分城市的表）
- ✓ 对：各城市分布见下表：「t1」

**引用形式**：
  1. 「tN」 —— 整张结果表
  2. 单元格：「tN.列名[行号]」 —— 第「行号」行（0 起算）、该列的那个值
  3. 单个聚合值：「agg(tN.列名)」 或 「agg(tN.列名 WHERE 筛选列=tN.筛选列[行号])」
       agg ∈ sum/avg/count/min/max；WHERE 只支持**单个等值条件**，
       没有 AND/OR、没有范围、没有其它运算符。
       **铁律：WHERE 的筛选值必须是单元格引用 tN.列名[行号]，绝不能手写字面量。**
       原因：你手打的 '值' 一旦和真实数据对不上（列名不符、值实为 '值市'、大小写差异），
       筛选命中 0 行 → 整个引用解析失败 → 原始 token「…」直接暴露给用户。
       单元格引用把筛选值从真实结果表里取出——永远对得上、永远不会编错。
       ✗ 错：「sum(t1.amount WHERE city='上海')」  —— '上海' 是你手打的字面量
       ✓ 对：「sum(t1.amount WHERE city=t1.city[0])」 —— 筛选值取自 t1 第 0 行的 city
       （先看 t1 结果表，确认"上海"在第几行，行号就填几）
  4. 派生值（占比 / 比值 / 差值 / 倍数 / 任何要算的数）：
       把**整个算式**包进一对「」，算式里可以有聚合值、数字、加减乘除、括号。
       前端会求值出最终数字。
       例 占比：「sum(t1.amt WHERE city=t1.city[0]) / sum(t1.amt) * 100」
            （末尾的 % 号写在「」**外面**当普通文字：…「…* 100」%）
       例 差值：「max(t1.amt) - min(t1.amt)」
       **绝不**自己把占比 / 比值 / 差值算出来再写一个数字——那就是编造。
       把算式整体交给「」，让前端算。

**列名必须逐字照抄**：引用里的列名、WHERE 的筛选列名，必须和该结果【输出】
TOON 表头里的列名**一字不差**（含大小写、下划线）。例如表头是 ` + "`Total_amount`" + ` 就写
` + "`Total_amount`" + `，不要简写成 ` + "`amount`" + `。写错列名引用会解析失败、原始 token 暴露给用户。
每个 tN 的列名各自独立——引用 tN 前先看那个 tN 的表头。

**逐项 / 逐行问法**（"各城市分别是多少"、"每个 X 卖了多少"）有两种正确写法，任选其一：
- 直接「tN」给整表 —— 表里每行已带它自己的值；
- 或逐行用 WHERE，筛选值一律用单元格引用、行号顺次递增：
  「t1.city[0]」营收「sum(t1.amount WHERE city=t1.city[0])」元；
  「t1.city[1]」营收「sum(t1.amount WHERE city=t1.city[1])」元……
  城市名也用引用「t1.city[行号]」，不要手打——出现的值和筛选值都从真实数据取。
**单行问题**（"上海的营收是多少"）：先在 t1 结果表里找"上海"在第几行（设为第 k 行），
再写 「sum(t1.amount WHERE city=t1.city[k])」。
绝不要对每行写不带 WHERE 的「sum(tN.列)」——那是整列求和，每行会得到同一个总数（错）。

**铁律 1 —— 必须引用，绝不编造**：
答复里任何**来自数据的数字**都必须写成引用，不能是你自己写出来的字面量。
如果你想报一个数、但手里没有能支撑它的查询结果（没有对应的 tN）——
**先去查**（调 smartquery / compose_query 把那个结果查出来，创建引用），再用引用报。
**绝不**凭记忆、凭估算、凭"大概"、凭推算写数字。宁可多查一次，绝不编一个数。

**铁律 2 —— 引用只在本轮有效**：
tN 是**本轮**的编号，每一轮都从 t1 重新开始。
**只能引用你在本轮亲自查出来的 tN**。绝不要引用上一轮对话里出现过的 tN ——
那个编号在本轮指向的是另一张表（或根本不存在），会渲染成错数或暴露原始 token。
如果用户的追问需要之前查过的数据，**在本轮重新查一遍**、生成本轮的 tN 再引用
（重查同时保证数据是最新的）。

为什么：大语言模型对长数字的转录天生不可靠，直接写数字会抄错。引用让"真值"只来自查询结果本身，
你只负责"指哪个数"，不负责"报数"。非数值的结论 / 解读文字照常正常写，**只有数字和表格用引用**。
` + capabilityGapPromptSection() + `
` + xmlToolSection + `
## 日期参考

- 今天: ` + time.Now().Format("2006-01-02") + `
- 去年同期: ` + time.Now().AddDate(-1, 0, 0).Format("2006-01-02") + `
- 最近6个月: ` + time.Now().AddDate(0, -6, 0).Format("2006-01-02") + ` ~ ` + time.Now().Format("2006-01-02")

		// ── Override system prompt for branch (clarification) threads ──
		// Branch threads get a distilled seed prompt with only the original question
		// and candidate list. No project-wide context, no main system rules.
		if isBranchThread {
			systemPrompt = branchSeedPrompt
		}

		// ── Override system prompt for builder threads ──
		// Builder threads use a different prompt + tool set (see handler_agent_builder.go).
		// Placed AFTER the isBranchThread check so a branch-of-builder still gets the
		// branch seed prompt; not a current code path but consistent with intent.
		if agentType == "builder" {
			systemPrompt = builderSystemPrompt(projectName)
		}

		// ── Inject Ol (learned facts) index into system prompt ──
		// Confirmed learned facts provide cross-cutting business knowledge that should
		// guide smartquery decisions. Skip placeholder when no facts exist. Only
		// lakehouse (query) mode needs this — builder does not run smartquery.
		if agentType == "lakehouse" {
			olIndex := BuildOlIndex(db, projectID, "")
			if !strings.HasPrefix(olIndex, "暂无学习事实") {
				systemPrompt += "\n\n" + olIndex
			}
		}

		var llmMessages []M
		llmMessages = append(llmMessages, M{"role": "system", "content": systemPrompt})
		// Build message list: history turns use raw content; only the last user message
		// gets the ephemeral recall context prepended (in-memory only, never saved to DB).
		for i, m := range messages {
			content := m.Content
			if i == len(messages)-1 && m.Role == "user" {
				if agentType == "builder" && builderContextMD != "" {
					content = builderContextMD + "\n\n---\n\n" + m.Content
				} else if recallContextMD != "" {
					content = recallContextMD + "\n\n---\n\n" + m.Content
				}
			}
			llmMessages = append(llmMessages, M{"role": m.Role, "content": content})
		}

		// Define tools for native tool_call path
		var v2Tools []llmclient.ToolDef
		if agentType == "builder" {
			v2Tools = builderV2Tools()
		} else {
			// Lakehouse Agent (查询模式) 工具表 — 仅 lookup + smartquery。
			// 知识沉淀（anchor / causality / learned-fact）不属于查询职责，
			// 不在 LLM 工具面里暴露；后续如需要应放进独立的 builder/scribe 流程。
			v2Tools = []llmclient.ToolDef{
				{Name: "lookup", Description: lookupToolDescription, Parameters: M{
					"type": "object",
					"properties": M{
						"ontology_name": M{"type": "array", "items": M{"type": "string"}, "description": "Od / Ok 名（英文或中文）"},
						"keyword":       M{"type": "array", "items": M{"type": "string"}, "description": "业务关键词列表，每个词独立搜"},
					},
				}},
				{Name: "smartquery", Description: smartqueryToolDescription, Parameters: M{
					"type":     "object",
					"required": []string{"intent"},
					"properties": M{
						"intent": M{
							"type":        "string",
							"description": "Intent name (从 context 顶部 🎯 小节里挑)，必填。例：Sales.ByArtist / Sales.ByCountry / Sales.Total",
						},
						"params": M{
							"type":                 "object",
							"description":          "按 Intent 的 parameters schema 填的用户级参数。例 {n:5, genre:\"Rock\"}。Intent 没声明的 key 会被拒绝。",
							"additionalProperties": true,
						},
					},
				}},
				// reflect_query_result is normally auto-invoked after smartquery,
				// but registered here so the LLM can call it explicitly when it
				// wants to second-guess its own answer (rare path).
				{Name: "reflect_query_result", Description: reflectToolDescription, Parameters: M{
					"type":     "object",
					"required": []string{"userQuestion", "smartqueryResp"},
					"properties": M{
						"userQuestion":   M{"type": "string", "description": "用户原问题"},
						"smartqueryArgs": M{"type": "object", "description": "上一次 smartquery 的 args (intent + params)", "additionalProperties": true},
						"smartqueryResp": M{"type": "object", "description": "上一次 smartquery 的完整 result", "additionalProperties": true},
					},
				}},
				// re_recall lets the LLM widen the recall candidate set with
				// hints discovered via reflect or lookup. Triggered when the
				// initial recall missed a dimension that the user clearly
				// expressed.
				{Name: "re_recall", Description: reRecallToolDescription, Parameters: M{
					"type":     "object",
					"required": []string{"hints"},
					"properties": M{
						"hints":        M{"type": "array", "items": M{"type": "string"}, "description": "强制纳入候选集的 token 列表，例 [\"EmployeeID\",\"员工\"]"},
						"userQuestion": M{"type": "string", "description": "可选：覆盖原问题（默认沿用本轮的 userQuestion）"},
					},
				}},
				// compose_query is the catalog-bound free-composition escape
				// valve. Used after reflect=mismatch when re_recall + lookup
				// confirm there's no pre-built intent for the user's query
				// shape. LLM emits {odName, metric, filters, groupBy, ...},
				// every token validated against the project ontology, then
				// the same engine strict-mode uses runs the SQL.
				{Name: "compose_query", Description: composeQueryToolDescription, Parameters: M{
					"type":     "object",
					"required": []string{"odName", "metric"},
					"properties": M{
						"odName": M{"type": "string", "description": "主 OD 名（单个，必填）。例 \"SALE\""},
						"metric": M{"type": "string", "description": "聚合表达式 func(arg)。func ∈ sum/avg/min/max/count/distinct_count；arg 必须是主 OD 的 property 名。⚠ count 不接受 count(*)，请用 count(id) 或其它具体列——引擎会拒绝 *（避免 JOIN 双重计数）"},
						"filters": M{"type": "array", "items": M{
							"type": "object",
							"required": []string{"property", "op"},
							"properties": M{
								"property": M{"type": "string", "description": "OD 上某 property 的名"},
								"op":       M{"type": "string", "description": "=, !=, >, <, >=, <=, in, not_in, like, between"},
								"value":    M{"type": "string", "description": "过滤值（in/between 用逗号分隔）"},
							},
						}},
						"groupBy": M{"type": "array", "items": M{"type": "string"}, "description": "分组列名数组，每个必须是 OD 的 property"},
						"orderBy": M{"type": "array", "items": M{
							"type": "object",
							"required": []string{"label"},
							"properties": M{
								"label": M{"type": "string", "description": "结果列名"},
								"dir":   M{"type": "string", "enum": []string{"ASC", "DESC"}},
							},
						}},
						"limit": M{"type": "integer", "minimum": 1},
					},
				}},
				// ── Plan-mode tools (analysis_pattern OK cards) ──
				// See .omc/specs/plan-from-ontology-knowledge.md §3.5.
				// When recall context shows a "📊 分析 Skill" block, the
				// question may warrant a multi-dimension analysis. These
				// three tools drive a WIP=1 feature loop.
				{Name: "start_analysis_plan", Description: startAnalysisPlanToolDescription, Parameters: M{
					"type":     "object",
					"required": []string{"patternId", "reason"},
					"properties": M{
						"patternId": M{"type": "string", "description": "📊 分析 Skill 块里给出的 patternId（OK 卡片 id）"},
						"reason":    M{"type": "string", "description": "为什么这个问题值得展开多维分析（一句话）"},
					},
				}},
				{Name: "verify_feature", Description: verifyFeatureToolDescription, Parameters: M{
					"type":     "object",
					"required": []string{"featureId", "verdict"},
					"properties": M{
						"featureId": M{"type": "string", "description": "当前 active 特征的 id"},
						"verdict": M{
							"type":        "string",
							"enum":        []string{"pass", "fail", "blocked"},
							"description": "pass=验证条件满足；fail=不满足、需换工具/参数重试；blocked=该维度确实拿不到（如引擎 bug）",
						},
						"tool":      M{"type": "string", "description": "你为这个特征用了哪个工具"},
						"summary":   M{"type": "string", "description": "结果的单行摘要（人类可读）"},
						"reasoning": M{"type": "string", "description": "为什么给这个 verdict"},
						"value":     M{"type": "string", "description": "标量结果（如有），如 \"8,380,820\""},
						"rowCount":  M{"type": "integer", "description": "结果行数（如有）"},
						"error":     M{"type": "string", "description": "verdict=blocked/fail 时的原因"},
					},
				}},
				{Name: "complete_analysis", Description: completeAnalysisToolDescription, Parameters: M{
					"type":       "object",
					"properties": M{},
				}},
			}

			// MissionAct M2 — append declare_capability_gap when flag is on.
			// Zero impact when off: the tool is never added, never seen by LLM.
			if missionActEnabled {
				v2Tools = append(v2Tools, llmclient.ToolDef{
					Name:        "declare_capability_gap",
					Description: declareCapabilityGapToolDescription,
					Parameters:  M(declareCapabilityGapToolDef()),
				})
			}
		}

		// Sub-Agent (branch thread) — temporarily disabled.
		// if isBranchThread { ... }

		// ── Filter degradation guard state ──
		// Tracks the previous smartquery's filter prop set + empty-result flag.
		// Replaces the naive "count decreased" check with prop-set semantics:
		// a filter prop that moves from filters → groupBy is NOT a degradation
		// (LLM is correctly broadening to enumerate that dimension), only a
		// filter prop that **completely vanishes** from both filters and
		// groupBy is suspicious (semantic loss).
		//
		// nil prev list = no prior smartquery in this thread.
		var lastSmartqueryFilterProps []string
		lastSmartqueryWasEmpty := false

		// Plan-mode state (.omc/specs/plan-from-ontology-knowledge.md §3.5).
		// nil until start_analysis_plan succeeds; lives one agent turn only.
		var planState *analysisPlanState

		// Plan-mode tool-thrash guard: within a single active feature, the LLM
		// can keep calling data tools (smartquery/compose_query/lookup) without
		// ever calling verify_feature — this resurfaces spec §1.3's "13 步无界
		// 试错" anti-pattern from the old non-plan path. retry budget=2 only
		// bounds verify_feature *verdicts*, not raw tool calls before verify.
		// We count tool calls since the active feature started, and at
		// planToolNudgeThreshold inject ONE nudge telling the LLM to verify
		// (pass/fail/blocked — blocked is honest if the data can't be obtained).
		const planToolNudgeThreshold = 4
		var planToolCallsThisFeature int
		var planNudgedThisFeature bool

		// checkPlanToolBudget is called once per dispatch in plan-mode. It
		// resets the counter on start_analysis_plan / verify_feature (the LLM
		// either started a feature or reported a verdict), is a no-op on
		// complete_analysis (terminal), and otherwise increments + maybe nudges.
		checkPlanToolBudget := func(toolName string) {
			if planState == nil {
				return
			}
			switch toolName {
			case "start_analysis_plan", "verify_feature":
				planToolCallsThisFeature = 0
				planNudgedThisFeature = false
			case "complete_analysis":
				// terminal — no-op
			default:
				planToolCallsThisFeature++
				if planToolCallsThisFeature >= planToolNudgeThreshold && !planNudgedThisFeature {
					llmMessages = append(llmMessages, M{
						"role": "user",
						"content": fmt.Sprintf(
							"你为当前 active 特征已经调用了 %d 次工具但还没调 verify_feature 上报结论。"+
								"请立刻调 verify_feature 给出 pass/fail/blocked verdict —— "+
								"如果验证条件确实拿不到（如引擎/数据限制）就标 blocked，"+
								"诚实终态比无界试错好（spec §3.3 / §7.7）。",
							planToolCallsThisFeature),
					})
					planNudgedThisFeature = true
				}
			}
		}

		// Data-template step ids (.omc/specs — 数据模板). Each successful
		// smartquery / compose_query result is tagged with a stable id
		// (t1, t2, …) for this turn. The id rides inside the result M, so it
		// reaches BOTH the LLM (via toolResultToMarkdown) and the frontend
		// (via the function_call SSE event). The LLM then reports key numbers
		// as references — 「sum(t1.amount)」 / 「t1」 — instead of transcribing
		// digits; the frontend resolves the references against the stored
		// result tables at render time. Numbers never touch the LLM output
		// or the DB; only templates do.
		dataStepSeq := 0
		tagDataStep := func(toolName string, result M) {
			if result == nil {
				return
			}
			if toolName != "smartquery" && toolName != "compose_query" {
				return
			}
			if st, _ := result["execution_status"].(string); st != "success" {
				return
			}
			if _, already := result["step_id"]; already {
				return
			}
			dataStepSeq++
			result["step_id"] = fmt.Sprintf("t%d", dataStepSeq)
		}

		// Dispatch a tool by name.
		//
		// Defense-in-depth: although the v2Tools array (line ~740) already
		// gates which tools the LLM can SEE per agent_type, we also reject
		// cross-mode tool calls here. Builder threads can only invoke the
		// 6 builder tools; lakehouse threads can only invoke the 6 lakehouse
		// tools. Any cross-mode call returns a clear error rather than
		// silently executing.
		// P5 consolidated 21 tools → 14:
		//   builder: list(type) + inspect(target,mode) replace 4+3 split tools;
		//            propose/update/delete × {od,intent,link} stay (9 tools).
		//   lakehouse: remember(type) replaces link_to_od + create_causality
		//              + propose_learned_fact.
		builderToolNames := map[string]bool{
			// Exploration (2 — was 7)
			"list":    true,
			"inspect": true,
			// OD CRUD (3)
			"propose_od": true,
			"update_od":  true,
			"delete_od":  true,
			// Intent CRUD (3)
			"propose_intent": true,
			"update_intent":  true,
			"delete_intent":  true,
			// Link CRUD (3)
			"propose_link": true,
			"update_link":  true,
			"delete_link":  true,
		}
		lakehouseToolNames := map[string]bool{
			"lookup":     true,
			"smartquery": true,
			// synthesize is server-side post-processing (autoInvokeSynthesize),
			// not LLM-visible — listed here so the mode-gate allows the
			// internal dispatch.
			"synthesize": true,
			// reflect_query_result is auto-invoked after smartquery (server
			// inserts the call); also LLM-visible so callers can request a
			// second look explicitly.
			"reflect_query_result": true,
			// re_recall is LLM-callable (used after reflect verdict=mismatch
			// to widen the recall candidate set with explicit hint tokens).
			"re_recall": true,
			// compose_query is the Tier 1 escape valve for queries no
			// pre-built intent covers. LLM-callable; validates every token
			// against the project's catalog before SQL generation.
			"compose_query": true,
			// Plan-mode tools — drive the analysis_pattern feature loop.
			"start_analysis_plan": true,
			"verify_feature":      true,
			"complete_analysis":   true,
			// MissionAct M2 — capability gap declaration. Only registered
			// when USE_MISSION_ACT is on; the v2Tools block below mirrors
			// this guard so the LLM never sees the tool when the flag is off.
			"declare_capability_gap": missionActEnabled,
		}

		dispatchTool := func(name string, args map[string]interface{}) M {
			if agentType == "builder" {
				if !builderToolNames[name] {
					return M{
						"error":            fmt.Sprintf("工具 %q 在构造模式 (builder) 不可用", name),
						"tool_unavailable": true,
						"availableTools":   []string{"list", "inspect", "propose_od", "update_od", "delete_od", "propose_intent", "update_intent", "delete_intent", "propose_link", "update_link", "delete_link"},
					}
				}
			} else {
				if !lakehouseToolNames[name] {
					return M{
						"error":            fmt.Sprintf("工具 %q 在查询模式 (lakehouse) 不可用", name),
						"tool_unavailable": true,
						"availableTools":   []string{"lookup", "smartquery"},
					}
				}
			}
			switch name {
			// ── Builder mode tools (US-002 / US-003) ──
			// Mode gate above guarantees these are only reached when
			// agent_type='builder'.
			// ── Unified DB / catalog exploration (P5) ──
			// list(type) — type ∈ {tables, ods, intents, links}
			// inspect(mode, …) — mode ∈ {schema, fk_candidates, sql, value_search}
			case "list":
				listType, _ := args["type"].(string)
				switch listType {
				case "tables", "lakehouse_tables":
					res := builderToolListLakehouseTables(db, projectID)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeListLakehouseTables(res, threadBuilderLedger.TurnCount)
					}
					return res
				case "ods", "od":
					res := builderToolListOds(db, projectID, args)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeListOds(res, threadBuilderLedger.TurnCount)
					}
					return res
				case "intents", "intent":
					res := builderToolListIntents(db, projectID, args)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeListIntents(res, threadBuilderLedger.TurnCount)
					}
					return res
				case "links", "link":
					res := builderToolListLinks(db, projectID, args)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeListLinks(res, threadBuilderLedger.TurnCount)
					}
					return res
				default:
					return M{"error": fmt.Sprintf("list: 未知 type %q（应为 tables / ods / intents / links）", listType)}
				}
			case "inspect":
				mode, _ := args["mode"].(string)
				switch mode {
				case "schema":
					// inspect.schema → analyze_table; arg name was tableName
					if v, ok := args["table"].(string); ok && v != "" {
						args["tableName"] = v
					}
					res := builderToolAnalyzeTable(ctx, db, projectID, args)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeAnalyzeTable(args, res, threadBuilderLedger.TurnCount)
					}
					return res
				case "fk_candidates", "relationships":
					res := builderToolAnalyzeRelationships(ctx, db, projectID, args)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeAnalyzeRelationships(args, res, threadBuilderLedger.TurnCount)
					}
					return res
				case "sql", "execute":
					res := builderToolQueryData(ctx, db, projectID, args)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeQueryData(args, res, threadBuilderLedger.TurnCount)
					}
					return res
				case "value_search":
					// value_search routes to query_data with searchKeyword/inTable
					// args already in canonical shape; alias keyword→searchKeyword.
					if v, ok := args["keyword"].(string); ok && v != "" {
						args["searchKeyword"] = v
					}
					res := builderToolQueryData(ctx, db, projectID, args)
					if threadBuilderLedger != nil {
						threadBuilderLedger.MergeQueryData(args, res, threadBuilderLedger.TurnCount)
					}
					return res
				default:
					return M{"error": fmt.Sprintf("inspect: 未知 mode %q（应为 schema / fk_candidates / sql / value_search）", mode)}
				}
			// ── OD CRUD ──
			case "propose_od":
				// Server-side minimum-turn guard (plan MAJOR-5 fix). The
				// system prompt also tells the LLM to interview ≥3 turns
				// first, but we double-check here so that prompt drift
				// can't bypass the rule.
				var userMsgCount int
				db.QueryRow(`SELECT COUNT(*) FROM ont_agent_step WHERE thread_id = $1 AND role = 'user'`, threadID).Scan(&userMsgCount)
				if userMsgCount < 3 {
					return M{
						"interview_bypassed": true,
						"error":              fmt.Sprintf("需先访谈至少 3 轮，当前仅 %d 轮。请先了解业务背景再提议 OD。", userMsgCount),
						"userMessageCount":   userMsgCount,
					}
				}
				res := builderToolProposeOd(ctx, db, projectID, threadID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergePropose("propose_od", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			case "update_od":
				res := builderToolUpdateOd(ctx, db, projectID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergeUpdate("update_od", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			case "delete_od":
				res := builderToolDeleteOd(ctx, db, projectID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergeDelete("delete_od", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			// ── Intent CRUD ──
			case "propose_intent":
				res := builderToolProposeIntent(db, projectID, threadID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergePropose("propose_intent", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			case "update_intent":
				res := builderToolUpdateIntent(ctx, db, projectID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergeUpdate("update_intent", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			case "delete_intent":
				res := builderToolDeleteIntent(ctx, db, projectID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergeDelete("delete_intent", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			// ── Link CRUD ──
			case "propose_link":
				res := builderToolProposeLink(db, projectID, threadID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergePropose("propose_link", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			case "update_link":
				res := builderToolUpdateLink(ctx, db, projectID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergeUpdate("update_link", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			case "delete_link":
				res := builderToolDeleteLink(ctx, db, projectID, args)
				if threadBuilderLedger != nil {
					threadBuilderLedger.MergeDelete("delete_link", args, res, threadBuilderLedger.TurnCount)
				}
				return res
			case "lookup":
				// Ledger-aware variant — cached Ods/tokens return short
				// pointers instead of re-rendering, and freshly-loaded
				// entries are merged into threadLedger in-place so the
				// next turn sees them as cold.
				currentTurn := 1
				if threadLedger != nil {
					currentTurn = threadLedger.TurnCount
				}
				return lakehouseToolLookupCached(ctx, db, projectID, args, threadLedger, currentTurn)
			case "smartquery":
				// Strict mode (P7.2): LLM only fills {intent, params}; the
				// server is the sole owner of spec.Filters / GroupBy / OrderBy
				// / Limit / Metric. The legacy "filter degradation guard" that
				// inspected raw args["filters"] is moot — params don't degrade
				// the same way (Intent's canonical filters are server-controlled
				// and cannot be dropped by LLM). Surfacing per-bind errors via
				// PARAM_REQUIRED / PARAM_UNKNOWN / PARAM_TYPE_ERROR / SPEC_VALIDATION_FAILED
				// gives the LLM clear self-correction signals.
				_ = lastSmartqueryFilterProps
				_ = lastSmartqueryWasEmpty
				return lakehouseToolSmartQuery(ctx, db, projectID, userQuestion, args)
			case "synthesize":
				return runSynthesizeTool(db, args)
			case "reflect_query_result":
				return runReflectTool(db, args)
			case "re_recall":
				return runReRecallTool(ctx, db, projectID, userQuestion, args)
			case "compose_query":
				return runComposeQueryTool(ctx, db, projectID, userQuestion, args)
			case "start_analysis_plan":
				st, res := runStartAnalysisPlan(recallResult.OkEntries, args)
				if st != nil {
					planState = st
					// MissionAct M3 — seed shadow mission tasks from feature list.
					if shadowM != nil && st.ledger != nil {
						snap := st.ledger.Snapshot()
						views := make([]featureRuntimeView, len(snap))
						for i, r := range snap {
							hints := make([]featureToolHintView, len(r.ToolHints))
							for j, h := range r.ToolHints {
								hints[j] = featureToolHintView{Tool: h.Tool}
							}
							views[i] = featureRuntimeView{
								ID: r.ID, Behavior: r.Behavior,
								Verification: r.Verification, ToolHints: hints,
							}
						}
						shadowM.seedTasksFromFeatures(ctx, views)
					}
				}
				return res
			case "verify_feature":
				res := runVerifyFeature(planState, args)
				// MissionAct M3 — mirror verify_feature outcome into shadow mission.
				if shadowM != nil {
					featureID, _ := args["featureId"].(string)
					outcome, _ := res["outcome"].(string)
					shadowM.recordVerifyFeature(ctx, featureID, outcome,
						strArg(args, "tool"), strArg(args, "summary"), strArg(args, "reasoning"))
				}
				return res
			case "complete_analysis":
				fa, res := runCompleteAnalysis(planState)
				// MissionAct M3 — record final synthesis into shadow mission.
				if shadowM != nil && fa != "" {
					shadowM.recordCompleteAnalysis(ctx, fa)
				}
				return res
			case "declare_capability_gap":
				// MissionAct M2 — capability gap declaration.
				// Returns M{"finalAnswer": "..."} on an accepted gap (terminal,
				// same shape as complete_analysis) so the three break-paths below
				// handle it identically. Returns M{"error": "..."} on gate
				// rejection (non-terminal — the agent loop continues).
				r := runDeclareCapabilityGap(ctx, db, shadowM, recallResult.MetricIntents, args)
				if r.terminal {
					return M{"finalAnswer": r.finalAnswer}
				}
				return M(r.toolResult)
			// remember 工具已撤销 — 查询模式只暴露 lookup + smartquery。
			// 知识沉淀（anchor / causality / fact）暂无 LLM 入口，需要时通过
			// builder mode 或独立 API 操作。
			// case "clarify_and_branch": // temporarily disabled
			// 	return v2ToolClarifyAndBranch(db, projectID, threadID, args)
			// case "return_to_parent": // temporarily disabled
			// 	return v2ToolReturnToParent(db, projectID, threadID, args)
			default:
				return M{"error": "未知工具: " + name}
			}
		}

		// MissionAct 任务可达器 — reachability gate (M4).
		// Runs BEFORE the ReAct loop. Fail-open: any LLM/parse error falls
		// through to the normal loop unchanged. Only active when
		// USE_MISSION_ACT is on AND agentType is "lakehouse" (builder has no
		// recall Intents to gate against).
		if missionActEnabled && agentType == "lakehouse" {
			if infeasibleAnswer := runReachabilityJudge(ctx, db, shadowM, userQuestion, recallResult.MetricIntents); infeasibleAnswer != "" {
				// Infeasible — stream the machine-templated answer and stop.
				sendSSE("token", infeasibleAnswer)
				// Persist as a zero-round assistant step so the turn is not blank.
				stepIdx++
				fcJSON, _ := json.Marshal(M{})
				sentJSON, _ := json.Marshal(llmMessages)
				db.Exec(`INSERT INTO ont_agent_step
					(thread_id, step_index, role, content, thinking, function_call,
					 system_prompt, llm_messages, duration_ms, prompt_tokens, completion_tokens, total_tokens, mission_id)
					VALUES ($1, $2, 'assistant', $3, '', $4::jsonb, $5, $6::jsonb, 0, 0, 0, 0, $7)`,
					threadID, stepIdx, infeasibleAnswer, string(fcJSON),
					systemPrompt, string(sentJSON), nullableMissionID(shadowM))
				lastAssistantContent := infeasibleAnswer
				shadowM.finish(context.Background(), lastAssistantContent)
				sendSSEFull("done", M{"promptTokens": 0, "completionTokens": 0, "totalTokens": 0, "modelName": modelName})
				return
			}
		}

		var promptTokens, completionTokens, totalTokens int
		const maxRounds = 20
		// Counts Synthesizer self-check failures across rounds. Once it hits
		// synthFollowUpMaxFails, the helper falls back to legacy prose path.
		synthFailCount := 0

		// lastAssistantContent tracks the most recent non-empty assistant
		// text persisted this turn. The final saveRoundStep call of a turn
		// carries the final answer, so this ends up holding it.
		var lastAssistantContent string

		// MissionAct M1 — finalise the shadow mission on EVERY turn-exit
		// path. The handler has several early returns (LLM error, synthesize
		// short-circuit, plan-mode terminal); a deferred closure persists
		// the accumulated step_results + final answer no matter how the turn
		// ends. context.Background() so a cancelled request context cannot
		// drop the final write. No-op when USE_MISSION_ACT is off.
		defer func() {
			// MissionAct M3-lite — reconcile the sub-question task list against
			// the final answer (one LLM call, fail-open) before finalising. No-op
			// when there are no sub-question tasks.
			reconcileMissionTasks(context.Background(), db, shadowM, lastAssistantContent)
			shadowM.finish(context.Background(), lastAssistantContent)
		}()

		// saveRoundStep persists one LLM call round to ont_agent_step.
		// sentMsgs is the exact llmMessages snapshot sent to the LLM this round.
		saveRoundStep := func(sentMsgs []M, roundContent, roundThinking string, roundFC M, roundPT, roundCT, roundTT int, roundDur int64) {
			stepIdx++
			if roundContent != "" {
				lastAssistantContent = roundContent
			}
			fcJSON, _ := json.Marshal(roundFC)
			sentJSON, _ := json.Marshal(sentMsgs)
			// mission_id ties this step to the shadow mission (NULL when
			// USE_MISSION_ACT is off — see mission_shadow.go).
			db.Exec(`INSERT INTO ont_agent_step
				(thread_id, step_index, role, content, thinking, function_call,
				 system_prompt, llm_messages, duration_ms, prompt_tokens, completion_tokens, total_tokens, mission_id)
				VALUES ($1, $2, 'assistant', $3, $4, $5::jsonb, $6, $7::jsonb, $8, $9, $10, $11, $12)`,
				threadID, stepIdx, roundContent, roundThinking, string(fcJSON),
				systemPrompt, string(sentJSON), roundDur,
				roundPT, roundCT, roundTT, nullableMissionID(shadowM))
		}

		for round := 0; round < maxRounds; round++ {
			sendSSE("thinking", fmt.Sprintf("调用 LLM...（轮次 %d/%d）", round+1, maxRounds))

			roundStart := time.Now()
			// Snapshot exactly what we're sending this round — before any mutation
			sentMsgsSnapshot := make([]M, len(llmMessages))
			copy(sentMsgsSnapshot, llmMessages)

			var roundPT, roundCT, roundTT int
			var roundContent, roundThinking string
			var roundFC M

			if isToolCall {
				// ── Native tool_call path ──
				content, toolCalls, usage, err := llmclient.DoChatWithTools(
					baseURL, apiKey,
					M{"model": modelName, "messages": llmMessages, "max_tokens": 4096, "temperature": 0.1},
					v2Tools, "", vendor,
				)
				if err != nil {
					sendSSE("error", "LLM 失败: "+err.Error())
					return
				}
				if usage != nil {
					roundPT += usage.PromptTokens
					roundCT += usage.CompletionTokens
					roundTT += usage.TotalTokens
				}

				if len(toolCalls) == 0 {
					// No native tool call — re-stream. But check streamed content for XML tool calls too.
					streamedFinal, sUsage, sErr := llmclient.DoChatStreamCallback(
						baseURL, apiKey,
						M{"model": modelName, "messages": llmMessages, "max_tokens": 4096, "temperature": 0.1, "_vendor": vendor},
						func(token string) { sendSSE("token", token) },
						func(thinking string) { roundThinking += thinking; sendSSE("thinking", thinking) },
					)
					if sErr != nil {
						roundContent = llmclient.StripThinkTags(content)
						sendSSE("token", roundContent)
					} else {
						roundContent = streamedFinal
						if sUsage != nil {
							roundPT += sUsage.PromptTokens
							roundCT += sUsage.CompletionTokens
							roundTT += sUsage.TotalTokens
						}
					}

					// Check streamed content for XML tool calls (vendor XML / <function_call>).
					// Some models output tool calls as XML text instead of native tool_calls.
					fcName, fcArgs, _, hasFc := llmclient.ExtractFunctionCallXML(roundContent)
					if hasFc {
						sendSSEFull("clear_tokens", M{})
						sendSSE("thinking", fmt.Sprintf("调用工具: %s", fcName))
						toolResult := dispatchTool(fcName, fcArgs)
						tagDataStep(fcName, toolResult)
						shadowM.recordStep(toolResult) // MissionAct M1 — shadow step result
						roundFC = M{"name": fcName, "arguments": fcArgs, "result": toolResult}
						sendSSEFull("function_call", roundFC)
						// Plan-mode terminal (streamed-XML path): see native path.
						// declare_capability_gap (M2) also returns finalAnswer on acceptance.
						if fcName == "complete_analysis" || fcName == "declare_capability_gap" {
							if fa, ok := toolResult["finalAnswer"].(string); ok && fa != "" {
								sendSSE("token", fa)
								saveRoundStep(sentMsgsSnapshot, fa, roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
								promptTokens += roundPT
								completionTokens += roundCT
								totalTokens += roundTT
								break
							}
						}
						saveRoundStep(sentMsgsSnapshot, "", roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
						promptTokens += roundPT
						completionTokens += roundCT
						totalTokens += roundTT
						llmMessages = append(llmMessages, M{"role": "assistant", "content": roundContent})
						var followUp string
						switch fcName {
						case "smartquery", "compose_query":
							// reflect runs as auto-invoke right below; its
							// returned message either tells the LLM to answer
							// (verdict=match → synthesize message) or to
							// re_recall / lookup / compose_query (verdict=mismatch).
							followUp = ""
						case "re_recall":
							followUp = "\n\n请基于新的候选 intent 列表重新调用 smartquery。"
						case "lookup":
							followUp = "\n\n请根据以上结果，立即调用 smartquery 执行数据查询，不要再次调用 lookup。"
						default:
							followUp = "\n\n请继续完成任务。"
						}
						llmMessages = append(llmMessages, M{"role": "user", "content": toolResultToMarkdown(fcName, fcArgs, toolResult) + followUp})
						checkPlanToolBudget(fcName)
						// Auto-invoke reflect as a separate tool boundary after
						// a successful smartquery. reflect returns a follow-up
						// that either chains synthesize (verdict=match) or
						// directs the LLM to re_recall / lookup
						// (verdict=mismatch). The synthesize legacy bridge
						// lives inside autoInvokeReflect for the match path.
						// In plan-mode the per-feature check is verify_feature,
						// not the legacy reflect→synthesize bridge — suppress
						// autoInvokeReflect so the feature loop is not hijacked.
						if planState == nil && (fcName == "smartquery" || fcName == "compose_query") {
							if reflectMsg := autoInvokeReflect(ctx, db, dispatchTool, sendSSEFull, saveRoundStep, sentMsgsSnapshot, userQuestion, toolResult, fcArgs, &synthFailCount, time.Now()); reflectMsg != "" {
								llmMessages = append(llmMessages, M{"role": "user", "content": reflectMsg})
							}
						}
						continue
					}

					// Truly no tool call — final answer.
					saveRoundStep(sentMsgsSnapshot, roundContent, roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
					promptTokens += roundPT
					completionTokens += roundCT
					totalTokens += roundTT
					break
				}

				tc := toolCalls[0]
				sendSSE("thinking", fmt.Sprintf("调用工具: %s", tc.Name))
				toolResult := dispatchTool(tc.Name, tc.Arguments)
				tagDataStep(tc.Name, toolResult)
				shadowM.recordStep(toolResult) // MissionAct M1 — shadow step result
				roundFC = M{"name": tc.Name, "arguments": tc.Arguments, "result": toolResult}
				sendSSEFull("function_call", roundFC)

				// Plan-mode terminal: complete_analysis renders the final
				// answer (machine-stitched synthesis — template + verbatim
				// caveats). Emit it directly and end the turn; the LLM does
				// not get to rephrase it (spec §9.5).
				// declare_capability_gap (M2) uses the same finalAnswer shape.
				if tc.Name == "complete_analysis" || tc.Name == "declare_capability_gap" {
					if fa, ok := toolResult["finalAnswer"].(string); ok && fa != "" {
						roundContent = fa
						sendSSE("token", fa)
						saveRoundStep(sentMsgsSnapshot, roundContent, roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
						promptTokens += roundPT
						completionTokens += roundCT
						totalTokens += roundTT
						break
					}
				}

				saveRoundStep(sentMsgsSnapshot, roundContent, roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
				promptTokens += roundPT
				completionTokens += roundCT
				totalTokens += roundTT

				llmMessages = append(llmMessages, llmclient.BuildAssistantToolCallMessage([]llmclient.ToolCallResult{{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}}))
				llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, toolResultToMarkdown(tc.Name, tc.Arguments, toolResult)))
				checkPlanToolBudget(tc.Name)
				// Auto-invoke reflect after smartquery (replaces legacy
				// synthesize auto-invoke). reflect's return message either
				// chains synthesize (verdict=match) or instructs the LLM to
				// re_recall / lookup with the missing dimensions found in the
				// shape mismatch.
				// Plan-mode suppresses autoInvokeReflect — see the streamed-XML
				// path above for the rationale (verify_feature owns per-feature
				// checking in plan-mode).
				if planState == nil && (tc.Name == "smartquery" || tc.Name == "compose_query") {
					if reflectMsg := autoInvokeReflect(ctx, db, dispatchTool, sendSSEFull, saveRoundStep, sentMsgsSnapshot, userQuestion, toolResult, tc.Arguments, &synthFailCount, time.Now()); reflectMsg != "" {
						llmMessages = append(llmMessages, M{"role": "user", "content": reflectMsg})
					}
				}

			} else {
				// ── XML fallback path (streaming) ──
				streamedContent, sUsage, sErr := llmclient.DoChatStreamCallback(
					baseURL, apiKey,
					M{"model": modelName, "messages": llmMessages, "max_tokens": 4096, "temperature": 0.1, "_vendor": vendor},
					func(token string) { sendSSE("token", token) },
					func(thinking string) { roundThinking += thinking; sendSSE("thinking", thinking) },
				)
				if sErr != nil {
					sendSSE("error", "LLM 失败: "+sErr.Error())
					return
				}
				if sUsage != nil {
					roundPT += sUsage.PromptTokens
					roundCT += sUsage.CompletionTokens
					roundTT += sUsage.TotalTokens
				}

				fcName, fcArgs, _, hasFc := llmclient.ExtractFunctionCallXML(streamedContent)
				if !hasFc {
					roundContent = streamedContent
					saveRoundStep(sentMsgsSnapshot, roundContent, roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
					promptTokens += roundPT
					completionTokens += roundCT
					totalTokens += roundTT
					break
				}

				sendSSEFull("clear_tokens", M{})
				sendSSE("thinking", fmt.Sprintf("调用工具: %s", fcName))
				toolResult := dispatchTool(fcName, fcArgs)
				tagDataStep(fcName, toolResult)
				shadowM.recordStep(toolResult) // MissionAct M1 — shadow step result
				roundFC = M{"name": fcName, "arguments": fcArgs, "result": toolResult}
				sendSSEFull("function_call", roundFC)

				// Plan-mode terminal (XML path): see native path above.
				// declare_capability_gap (M2) uses the same finalAnswer shape.
				if fcName == "complete_analysis" || fcName == "declare_capability_gap" {
					if fa, ok := toolResult["finalAnswer"].(string); ok && fa != "" {
						roundContent = fa
						sendSSE("token", fa)
						saveRoundStep(sentMsgsSnapshot, roundContent, roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
						promptTokens += roundPT
						completionTokens += roundCT
						totalTokens += roundTT
						break
					}
				}

				saveRoundStep(sentMsgsSnapshot, roundContent, roundThinking, roundFC, roundPT, roundCT, roundTT, time.Since(roundStart).Milliseconds())
				promptTokens += roundPT
				completionTokens += roundCT
				totalTokens += roundTT

				llmMessages = append(llmMessages, M{"role": "assistant", "content": streamedContent})
				var followUp string
				switch fcName {
				case "smartquery", "compose_query":
					// reflect runs as auto-invoke immediately after; its
					// return message drives next step.
					followUp = ""
				case "re_recall":
					followUp = "\n\n请基于新的候选 intent 列表重新调用 smartquery。"
				case "lookup":
					followUp = "\n\n请根据以上结果，立即调用 smartquery 执行数据查询，不要再次调用 lookup。"
				default:
					followUp = "\n\n请继续完成任务。"
				}
				llmMessages = append(llmMessages, M{"role": "user", "content": toolResultToMarkdown(fcName, fcArgs, toolResult) + followUp})
				checkPlanToolBudget(fcName)
				// Auto-invoke reflect after smartquery (XML fallback path).
				// Suppressed in plan-mode — verify_feature owns per-feature checks.
				if planState == nil && fcName == "smartquery" {
					if reflectMsg := autoInvokeReflect(ctx, db, dispatchTool, sendSSEFull, saveRoundStep, sentMsgsSnapshot, userQuestion, toolResult, fcArgs, &synthFailCount, time.Now()); reflectMsg != "" {
						llmMessages = append(llmMessages, M{"role": "user", "content": reflectMsg})
					}
				}
			}
		}

		// Persist the thread ledger at turn end. The handler has been
		// mutating threadLedger in place across recall merge + each
		// lookup tool call; this is the single place it lands in DB.
		// Use SaveWithRetry so an optimistic-concurrency conflict (e.g.
		// a concurrent request on the same thread) is handled by
		// reload + re-merge rather than silently dropping this turn's
		// work.
		if threadLedger != nil {
			reapply := func(fresh *ledger.Ledger) *ledger.Ledger {
				// On conflict, re-merge this turn's ods / intents /
				// tokens into the freshly-loaded copy. Since merge
				// is idempotent + commutative, repeating the same
				// recall-result merge is safe.
				for id, od := range threadLedger.Ods {
					if od.LoadedInTurn == threadLedger.TurnCount {
						fresh.MergeLookupOd(od.OdBlock, od.LoadedInTurn)
						// preserve load method from this turn (mergeOd picks stronger)
						_ = id
					}
				}
				fresh.TurnCount++
				return fresh
			}
			if err := ledger.SaveWithRetry(ctx, db, threadID, threadLedger, ledgerOldVersion, reapply, 2); err != nil {
				log.Printf("LAKEHOUSE-AGENT: ledger save failed for thread %s: %v", threadID, err)
			}
		}

		// NOTE: builder ledger save is now done via `defer` near the load block,
		// so it fires even on LLM-error early-return paths.

		// MissionAct M1 shadow-mission finalisation runs via the deferred
		// closure registered before the round loop — see above.

		sendSSEFull("done", M{
			"promptTokens": promptTokens, "completionTokens": completionTokens,
			"totalTokens": totalTokens, "modelName": modelName,
		})
	}
}

// lakehouseToolLookupCached is the ledger-aware variant of the lookup
// tool. For each requested Od / keyword, it checks the thread ledger
// first:
//
//   - Od already loaded in ledger → returns a 1-line pointer block,
//     "Od:<name> 已在线程记忆中（见 🧠 头部）" — the full detail is
//     already visible in the turn's recall render, so re-printing it
//     here would just waste tokens.
//   - Token already StrongHit → similar pointer.
//   - Otherwise → full load + merges into the ledger in-place so the
//     NEXT tool call (or next turn) sees it as cold.
//
// Parameters mirror lakehouseToolLookup plus the live ledger pointer.
// If l is nil, falls through to the uncached implementation so the
// branch-thread path (which doesn't use the ledger yet) keeps working.
func lakehouseToolLookupCached(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}, l *ledger.Ledger, currentTurn int) M {
	if l == nil {
		return lakehouseToolLookup(ctx, db, projectID, args)
	}

	// Parse the same arg shape as the legacy path.
	var odNames_ []string
	var kwTokens []string
	if ns, ok := args["ontology_name"].([]interface{}); ok {
		for _, n := range ns {
			if s := strings.TrimSpace(fmt.Sprintf("%v", n)); s != "" {
				odNames_ = append(odNames_, s)
			}
		}
	}
	if ks, ok := args["keyword"].([]interface{}); ok {
		for _, k := range ks {
			if s := strings.TrimSpace(fmt.Sprintf("%v", k)); s != "" {
				kwTokens = append(kwTokens, s)
			}
		}
	} else if k, ok := args["keyword"].(string); ok && strings.TrimSpace(k) != "" {
		kwTokens = append(kwTokens, strings.TrimSpace(k))
	}
	if len(odNames_) == 0 && len(kwTokens) == 0 {
		for _, key := range []string{"name", "entityName", "topicName"} {
			if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
				odNames_ = []string{strings.TrimSpace(v)}
				break
			}
		}
	}
	if len(odNames_) == 0 && len(kwTokens) == 0 {
		return M{"error": "ontology_name or keyword is required"}
	}

	// Build name→cached lookup once.
	cachedByName := map[string]*ledger.LedgerOd{}
	for _, od := range l.Ods {
		cachedByName[strings.ToLower(od.Name)] = od
	}

	// Partition Ods into cached (pointer) vs. to-load.
	var cachedOdPointers []*ledger.LedgerOd
	var odsToLoad []string
	for _, name := range odNames_ {
		if od, ok := cachedByName[strings.ToLower(name)]; ok && len(od.AllPropNames) > 0 {
			cachedOdPointers = append(cachedOdPointers, od)
		} else {
			odsToLoad = append(odsToLoad, name)
		}
	}

	// Partition tokens into cached (StrongHit) vs. hot.
	var cachedTokens []string
	var hotTokens []string
	for _, tok := range kwTokens {
		if t, ok := l.Tokens[tok]; ok && t.StrongHit {
			cachedTokens = append(cachedTokens, tok)
		} else {
			hotTokens = append(hotTokens, tok)
		}
	}

	// Load fresh Ods only for the uncached.
	var freshOdBlocks []recall.OdBlock
	for _, name := range odsToLoad {
		blk := loadFullLakehouseOd(db, projectID, name)
		if blk != nil {
			freshOdBlocks = append(freshOdBlocks, *blk)
			l.MergeLookupOd(*blk, currentTurn)
		}
	}

	// Fresh keyword recall only for the hot tokens, using the CURRENT
	// ledger as a cache (so tokens hot relative to kwTokens but cached
	// relative to the thread don't re-query DB).
	var r recall.RecallResult
	if len(hotTokens) > 0 {
		cached := ledger.BuildCachedContext(l)
		// P1b: thread the turn's ctx so the remote recall span nests under
		// agent.turn and W3C traceparent flows into recall-server.
		r = recall.BuildLakehouseContextCached(ctx, db, projectID, hotTokens, "lookup", cached)
		l.MergeRecallResult(r, currentTurn)
	} else {
		r = recall.RecallResult{TokenDetails: map[string][]recall.KeywordHit{}}
	}

	// Add fresh Ods into r.OdBlocks so FormatContext renders them fully.
	mergedOdSet := map[string]bool{}
	for _, blk := range r.OdBlocks {
		mergedOdSet[strings.ToLower(blk.Name)] = true
	}
	for _, blk := range freshOdBlocks {
		if !mergedOdSet[strings.ToLower(blk.Name)] {
			r.OdBlocks = append(r.OdBlocks, blk)
			mergedOdSet[strings.ToLower(blk.Name)] = true
		}
	}

	// Render the fresh block via the existing lookup formatter.
	allTokens := append(append([]string{}, odNames_...), kwTokens...)
	r.DirectOds = nil
	r.HasMatches = len(r.OdBlocks) > 0 || len(r.OkEntries) > 0 || len(r.OlEntries) > 0
	freshMD := recall.FormatContext(r, allTokens, "lookup")

	// Prepend pointer block for cached items.
	var sb strings.Builder
	if len(cachedOdPointers) > 0 || len(cachedTokens) > 0 {
		sb.WriteString("### 🧠 已在线程记忆中（直接引用，勿再次 lookup）\n\n")
		if len(cachedOdPointers) > 0 {
			sb.WriteString(fmt.Sprintf("cached_ods[%d|]{od|kind|matchedProps|loadedInTurn}:\n", len(cachedOdPointers)))
			for _, od := range cachedOdPointers {
				sb.WriteString(fmt.Sprintf("  %s|%s|%d|T%d\n",
					od.Name, od.Kind, len(od.MatchedProps), od.LoadedInTurn))
			}
			sb.WriteString("\n")
		}
		if len(cachedTokens) > 0 {
			sb.WriteString(fmt.Sprintf("cached_tokens[%d|]{token|firstSeen|lastSeen}:\n", len(cachedTokens)))
			for _, tok := range cachedTokens {
				t := l.Tokens[tok]
				sb.WriteString(fmt.Sprintf("  %s|T%d|T%d\n", tok, t.FirstSeen, t.LastSeen))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString(freshMD)

	// involved trace (frontend graph highlighting) — mirror legacy.
	involved := buildLookupInvolvedTrace(r, allTokens)

	// Summary: 1 line for SSE/history display.
	summary := fmt.Sprintf("lookup: od=%d(cached=%d) tokens=%d(cached=%d)",
		len(odNames_), len(cachedOdPointers), len(kwTokens), len(cachedTokens))

	result := M{
		"content":  sb.String(),
		"count":    len(allTokens),
		"involved": involved,
		"summary":  summary,
	}
	if len(r.Ambiguities) > 0 {
		result["ambiguities"] = r.Ambiguities
	}
	return result
}

// buildLookupInvolvedTrace derives the "involved" M used by the
// frontend graph highlighter. Extracted so lakehouseToolLookupCached
// and lakehouseToolLookup can share the logic.
func buildLookupInvolvedTrace(r recall.RecallResult, allTokens []string) M {
	odNameSet := map[string]bool{}
	propKeySet := map[string]bool{}
	okTitleSet := map[string]bool{}
	var odNames []string
	var propertyKeys []M
	var okTitles []string
	var chain []M
	addOd := func(name string) {
		if name == "" || odNameSet[name] {
			return
		}
		odNameSet[name] = true
		odNames = append(odNames, name)
	}
	addProp := func(odName, propName string) {
		if odName == "" || propName == "" {
			return
		}
		key := odName + "." + propName
		if propKeySet[key] {
			return
		}
		propKeySet[key] = true
		propertyKeys = append(propertyKeys, M{"odName": odName, "propName": propName})
	}
	addOk := func(title string) {
		if title == "" || okTitleSet[title] {
			return
		}
		okTitleSet[title] = true
		okTitles = append(okTitles, title)
	}
	for _, blk := range r.OdBlocks {
		addOd(blk.Name)
		for _, p := range blk.MatchedProps {
			propName := p.DisplayName
			if propName == "" {
				propName = p.Name
			}
			addProp(blk.Name, propName)
			if p.OkTitle != "" {
				addOk(p.OkTitle)
			}
			for _, kw := range p.Keywords {
				step := M{"token": kw.MatchedToken, "keyword": kw.Keyword, "tier": kw.Tier, "odName": blk.Name, "propName": propName}
				if p.OkTitle != "" {
					step["okTitle"] = p.OkTitle
				}
				chain = append(chain, step)
			}
		}
	}
	for _, ok := range r.OkEntries {
		addOk(ok.Title)
		chain = append(chain, M{"okTitle": ok.Title, "token": strings.Join(ok.Tokens, ",")})
	}
	if propertyKeys == nil {
		propertyKeys = []M{}
	}
	if odNames == nil {
		odNames = []string{}
	}
	if okTitles == nil {
		okTitles = []string{}
	}
	if chain == nil {
		chain = []M{}
	}
	return M{
		"kind":         "lookup",
		"tokens":       allTokens,
		"odNames":      odNames,
		"propertyKeys": propertyKeys,
		"okTitles":     okTitles,
		"chain":        chain,
	}
}

// lakehouseToolLookup is the lakehouse-specific lookup tool (legacy,
// ledger-unaware). Retained for the branch-thread path where the
// ledger isn't yet wired in. Main thread uses lakehouseToolLookupCached.
//
// Two paths:
//   - ontology_name: direct full Od load from DB (all properties + Ok + links)
//   - keyword: recall via BuildLakehouseContext (lakehouse_keyword table)
func lakehouseToolLookup(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	// Parse ontology_name and keyword arrays separately.
	var odNames_ []string
	var kwTokens []string
	if ns, ok := args["ontology_name"].([]interface{}); ok {
		for _, n := range ns {
			if s := strings.TrimSpace(fmt.Sprintf("%v", n)); s != "" {
				odNames_ = append(odNames_, s)
			}
		}
	}
	if ks, ok := args["keyword"].([]interface{}); ok {
		for _, k := range ks {
			if s := strings.TrimSpace(fmt.Sprintf("%v", k)); s != "" {
				kwTokens = append(kwTokens, s)
			}
		}
	} else if k, ok := args["keyword"].(string); ok && strings.TrimSpace(k) != "" {
		kwTokens = append(kwTokens, strings.TrimSpace(k))
	}
	// Backward compat single name fields.
	if len(odNames_) == 0 && len(kwTokens) == 0 {
		for _, key := range []string{"name", "entityName", "topicName"} {
			if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
				odNames_ = []string{strings.TrimSpace(v)}
				break
			}
		}
	}
	if len(odNames_) == 0 && len(kwTokens) == 0 {
		return M{"error": "ontology_name or keyword is required"}
	}

	allTokens := append(append([]string{}, odNames_...), kwTokens...)

	// ── Path 1: Direct full Od load for ontology_name ──
	// Returns complete Od definition: all properties with types, descriptions, Ok entries.
	var directOdBlocks []recall.OdBlock
	for _, odName := range odNames_ {
		blk := loadFullLakehouseOd(db, projectID, odName)
		if blk != nil {
			directOdBlocks = append(directOdBlocks, *blk)
		}
	}

	// ── Path 2: Keyword recall via lakehouse_keyword ──
	var r recall.RecallResult
	if len(kwTokens) > 0 {
		// P1b: thread the turn's ctx (see companion edits in
		// lakehouseToolSmartQuery + lakehouseToolLookupCached).
		r = recall.BuildLakehouseContext(ctx, db, projectID, kwTokens, "lookup")
	}

	// ── Merge: direct Od blocks first, then recall results ──
	// Avoid duplicates: if an Od is already in directOdBlocks, skip it from recall.
	directOdSet := map[string]bool{}
	for _, blk := range directOdBlocks {
		directOdSet[strings.ToLower(blk.Name)] = true
	}
	var mergedOdBlocks []recall.OdBlock
	mergedOdBlocks = append(mergedOdBlocks, directOdBlocks...)
	for _, blk := range r.OdBlocks {
		if !directOdSet[strings.ToLower(blk.Name)] {
			mergedOdBlocks = append(mergedOdBlocks, blk)
		}
	}
	// Also include DirectOds from recall that aren't already present.
	for _, blk := range r.DirectOds {
		if !directOdSet[strings.ToLower(blk.Name)] {
			mergedOdBlocks = append(mergedOdBlocks, blk)
			directOdSet[strings.ToLower(blk.Name)] = true
		}
	}

	r.OdBlocks = mergedOdBlocks
	r.DirectOds = nil // already merged into OdBlocks
	r.HasMatches = len(mergedOdBlocks) > 0 || len(r.OkEntries) > 0 || len(r.OlEntries) > 0
	r.ContextMD = recall.FormatContext(r, allTokens, "lookup")

	// ── Enrich Ol entries with full details for lookup mode ──
	if len(r.OlEntries) > 0 {
		var olDetail strings.Builder
		olDetail.WriteString("\n### 经验详情（Ol）\n\n")
		for _, ol := range r.OlEntries {
			var content, factType, tagsRaw string
			db.QueryRow(`SELECT COALESCE(content,''), COALESCE(fact_type,'business_rule'), COALESCE(tags,'{}')::text
				FROM ont_learned_fact WHERE id = $1`, ol.ID).Scan(&content, &factType, &tagsRaw)

			olDetail.WriteString(fmt.Sprintf("#### `Ol:%s` [%s]\n", ol.Title, factType))
			olDetail.WriteString(fmt.Sprintf("**摘要**: %s\n", ol.Summary))
			if content != "" && content != ol.Summary {
				olDetail.WriteString(fmt.Sprintf("**详情**: %s\n", content))
			}

			// Fetch linked entities
			linkRows, _ := db.Query(`SELECT l.target_type, l.target_id::text, l.role FROM ont_fact_link l WHERE l.fact_id = $1`, ol.ID)
			if linkRows != nil {
				var linkParts []string
				for linkRows.Next() {
					var tt, tid, role string
					linkRows.Scan(&tt, &tid, &role)
					name := resolveFactLinkTargetName(db, tt, tid)
					prefix := tt
					switch tt {
					case "object":
						prefix = "Od"
					case "knowledge":
						prefix = "Ok"
					case "fact":
						prefix = "Ol"
					}
					if name != "" {
						linkParts = append(linkParts, fmt.Sprintf("%s:%s (%s)", prefix, name, role))
					}
				}
				linkRows.Close()
				if len(linkParts) > 0 {
					olDetail.WriteString(fmt.Sprintf("**关联**: %s\n", strings.Join(linkParts, "、")))
				}
			}
			olDetail.WriteString("\n")
		}
		r.ContextMD += olDetail.String()
	}

	// ── Build "involved" trace for frontend graph highlighting ──
	odNameSet := map[string]bool{}
	propKeySet := map[string]bool{}
	okTitleSet := map[string]bool{}
	var odNames []string
	var propertyKeys []M
	var okTitles []string
	var chain []M

	addOd := func(name string) {
		if name == "" || odNameSet[name] {
			return
		}
		odNameSet[name] = true
		odNames = append(odNames, name)
	}
	addProp := func(odName, propName string) {
		if odName == "" || propName == "" {
			return
		}
		key := odName + "." + propName
		if propKeySet[key] {
			return
		}
		propKeySet[key] = true
		propertyKeys = append(propertyKeys, M{"odName": odName, "propName": propName})
	}
	addOk := func(title string) {
		if title == "" || okTitleSet[title] {
			return
		}
		okTitleSet[title] = true
		okTitles = append(okTitles, title)
	}

	for _, blk := range r.OdBlocks {
		addOd(blk.Name)
		for _, p := range blk.MatchedProps {
			propName := p.DisplayName
			if propName == "" {
				propName = p.Name
			}
			addProp(blk.Name, propName)
			if p.OkTitle != "" {
				addOk(p.OkTitle)
			}
			for _, kw := range p.Keywords {
				step := M{
					"token":    kw.MatchedToken,
					"keyword":  kw.Keyword,
					"tier":     kw.Tier,
					"odName":   blk.Name,
					"propName": propName,
				}
				if p.OkTitle != "" {
					step["okTitle"] = p.OkTitle
				}
				chain = append(chain, step)
			}
		}
	}
	for _, ok := range r.OkEntries {
		addOk(ok.Title)
		chain = append(chain, M{"okTitle": ok.Title, "token": strings.Join(ok.Tokens, ",")})
	}

	if propertyKeys == nil {
		propertyKeys = []M{}
	}
	if odNames == nil {
		odNames = []string{}
	}
	if okTitles == nil {
		okTitles = []string{}
	}
	if chain == nil {
		chain = []M{}
	}

	result := M{
		"content": r.ContextMD,
		"count":   len(allTokens),
		"involved": M{
			"kind":         "lookup",
			"tokens":       allTokens,
			"odNames":      odNames,
			"propertyKeys": propertyKeys,
			"okTitles":     okTitles,
			"chain":        chain,
		},
	}
	if len(r.Ambiguities) > 0 {
		result["ambiguities"] = r.Ambiguities
	}
	return result
}

// loadFullLakehouseOd loads a complete Od definition by name: all properties with
// data types, descriptions, Ok entries, and join_key links. Returns an OdBlock with
// ALL properties as MatchedProps so FormatContext renders them with full detail.
func loadFullLakehouseOd(db *sql.DB, projectID string, odName string) *recall.OdBlock {
	var odID, name, kind, description string
	// Try exact match first, then fuzzy.
	// Skip unmarked Ods — the lookup tool must mirror the recall pipeline so
	// an Od that was disabled on /lakehouse/ontology/lakehouse-objects cannot sneak
	// back in via an explicit LLM lookup by name.
	db.QueryRow(`SELECT id::text, name, COALESCE(kind,''), COALESCE(description,'')
		FROM ont_object_type WHERE project_id = $1 AND name = $2
		  AND COALESCE(mark, true) = true`,
		projectID, odName).Scan(&odID, &name, &kind, &description)
	if odID == "" {
		db.QueryRow(`SELECT id::text, name, COALESCE(kind,''), COALESCE(description,'')
			FROM ont_object_type WHERE project_id = $1
			  AND COALESCE(mark, true) = true
			  AND (name ILIKE '%'||$2||'%' OR $2 ILIKE '%'||name||'%')
			ORDER BY CASE WHEN LOWER(name) = LOWER($2) THEN 0 ELSE 1 END, LENGTH(name)
			LIMIT 1`, projectID, odName).Scan(&odID, &name, &kind, &description)
	}
	if odID == "" {
		return nil
	}

	blk := &recall.OdBlock{
		OdID:        odID,
		Name:        name,
		Kind:        kind,
		Description: description,
	}

	// Load ALL properties as MatchedProps (full detail for LLM).
	pRows, err := db.Query(`
		SELECT p.id::text, p.name, COALESCE(p.display_name, p.name),
		       COALESCE(p.source_column,''), COALESCE(p.data_type,''),
		       COALESCE(p.description,''), COALESCE(p.is_machine_code, false)
		FROM ont_property p WHERE p.object_type_id = $1 ORDER BY p.name`, odID)
	if err != nil {
		return blk
	}
	defer pRows.Close()

	var allNames []string
	allDescs := map[string]string{}
	for pRows.Next() {
		var pid, pName, displayName, sourceCol, dataType, desc string
		var isMC bool
		pRows.Scan(&pid, &pName, &displayName, &sourceCol, &dataType, &desc, &isMC)

		propDesc := desc
		if isMC {
			propDesc += " [MC: 高基数列，值不可枚举]"
		}

		pm := recall.PropertyMatch{
			PropertyID:   pid,
			Name:         pName,
			DisplayName:  displayName,
			SourceColumn: sourceCol,
			DataType:     dataType,
			Description:  propDesc,
			ObjectTypeID: odID,
		}

		// Load Ok entry for this property.
		db.QueryRow(`SELECT k.id::text, k.title, COALESCE(k.summary,'')
			FROM ont_knowledge k WHERE k.anchor_type = 'property' AND k.anchor_id = $1`,
			pid).Scan(&pm.OkID, &pm.OkTitle, &pm.OkSummary)
		if pm.OkID != "" {
			defRows, err := db.Query(`SELECT COALESCE(content,'') FROM ont_knowledge_definition
				WHERE knowledge_id = $1 AND def_type = 'positive' ORDER BY sort_order`, pm.OkID)
			if err == nil {
				for defRows.Next() {
					var c string
					defRows.Scan(&c)
					if c != "" {
						pm.OkDefs = append(pm.OkDefs, c)
					}
				}
				defRows.Close()
			}
		}

		blk.MatchedProps = append(blk.MatchedProps, pm)
		allNames = append(allNames, displayName)
		if desc != "" {
			allDescs[displayName] = desc
		}
	}
	blk.AllPropNames = allNames
	blk.AllPropDescs = allDescs

	// Load join_key links via ont_causality.
	linkRows, err := db.Query(`
		SELECT DISTINCT
			CASE WHEN fo.id = $1 THEN toto.name ELSE fo.name END AS target_od,
			COALESCE(c.direction, 'N:N') AS cardinality
		FROM ont_causality c
		JOIN ont_knowledge fk ON c.from_knowledge_id = fk.id AND fk.anchor_type = 'property'
		JOIN ont_property fp ON fk.anchor_id = fp.id::text
		JOIN ont_object_type fo ON fp.object_type_id = fo.id
		JOIN ont_knowledge tk ON c.to_knowledge_id = tk.id AND tk.anchor_type = 'property'
		JOIN ont_property tp ON tk.anchor_id = tp.id::text
		JOIN ont_object_type toto ON tp.object_type_id = toto.id
		WHERE c.relation_type = 'join_key' AND c.project_id = $2
		  AND (fo.id = $1 OR toto.id = $1)`, odID, projectID)
	if err == nil {
		defer linkRows.Close()
		for linkRows.Next() {
			var targetName, cardinality string
			linkRows.Scan(&targetName, &cardinality)
			blk.Links = append(blk.Links, recall.OdLink{TargetOdName: targetName, Cardinality: cardinality})
		}
	}

	return blk
}

// lakehouseToolSmartQuery executes a SQL query against the lakehouse via
// **strict-mode** dispatch (P7.2):
//
//   - LLM only fills {intent: "Intent.Name", params: {...}}
//   - Server resolves the Intent by name → loads canonical_metric, auto_group_by,
//     default_order_by, default_limit, canonical_filters, parameters schema
//   - BindIntentParams translates LLM-supplied params (typed) onto spec.Limit /
//     spec.Filters per the schema
//   - LLM has no path to fill metric/groupBy/filters/orderBy/limit directly —
//     attempts return TOOL_ARGS_INVALID
//
// This is the third defense line after PassValidateSpec (lakehouse pipeline)
// and Intent dry-run save validation (backend-api). It guarantees the LLM
// can only produce one of three outcomes: correct SQL, INTENT_NOT_FOUND, or
// PARAM_*_ERROR / SPEC_VALIDATION_FAILED — never silently wrong SQL.
func lakehouseToolSmartQuery(ctx context.Context, db *sql.DB, projectID, userQuestion string, args map[string]interface{}) M {
	// ── Strict-mode arg gate ──
	// Reject any spec-level field LLM might have leftover from old prompts.
	// Each rejected field carries a hint so LLM learns the strict-mode
	// contract via tool feedback, not via prompt re-engineering.
	bannedSpecFields := []string{
		"objects", "metric", "groupBy", "filters", "orderBy", "limit",
		"displayMode", "addShareColumn", "shareLabel", "metricFilter",
		"derivedMetric", "denseGroups", "percentAxis",
	}
	for _, k := range bannedSpecFields {
		if _, present := args[k]; present {
			return M{
				"error": fmt.Sprintf(
					"TOOL_ARGS_INVALID: 严格模式下 smartquery 只接受 {intent, params}；额外字段 %q 不允许（spec 字段全部由 Intent + params 生成）。如需新查询模式，请联系运营添加新 Intent。",
					k),
				"code": "TOOL_ARGS_INVALID",
			}
		}
	}

	intentName, _ := args["intent"].(string)
	intentName = strings.TrimSpace(intentName)
	if intentName == "" {
		return M{
			"error": "INTENT_REQUIRED: smartquery 必填 intent 字段。请从 context 顶部「🎯 查询意图（Metric Intent）」小节挑一个 Intent name 传入。无 Intent 命中的查询不在严格模式覆盖范围内。",
			"code":  "INTENT_REQUIRED",
		}
	}

	hint, objectNames, intentParams, planJSON, notFound := lookupIntentByName(db, projectID, intentName)
	if notFound {
		return M{
			"error": fmt.Sprintf(
				"INTENT_NOT_FOUND: 未找到名为 %q 的 Metric Intent (project_id=%s)。可用 Intent 名见 context 顶部 🎯 小节；如该查询场景未配置 Intent，请告知用户当前不支持。",
				intentName, projectID),
			"code": "INTENT_NOT_FOUND",
		}
	}

	// Composite Intent (spec .omc/specs/plan-mode-composite-intent.md): when
	// the Intent carries a plan, dispatch to the deterministic plan executor
	// instead of the single-query path. LLM tool surface is unchanged —
	// it still called smartquery({intent, params}) — only the server-side
	// dispatch differs. Most response fields that the single-query path
	// builds (bound_spec, pivot, keyword corrections, joinPath) are
	// inapplicable to a plan, so we return a minimal compatible shape.
	if isPlanIntent(planJSON) {
		rawParams, _ := args["params"].(map[string]interface{})
		stringParams := make(map[string]string, len(rawParams))
		for k, v := range rawParams {
			if v == nil {
				continue
			}
			stringParams[k] = fmt.Sprint(v)
		}
		_ = intentParams // plan params live inside plan JSON; Intent.parameters is irrelevant here
		res := smartqueryExec(db).ExecutePlan(ctx, planJSON, stringParams, projectID)
		execStatus := "error"
		if res.ExecutionOK {
			execStatus = "success"
		}
		go func() {
			q := userQuestion
			if q == "" {
				q = fmt.Sprintf("[LH PLAN] %s", hint.Name)
			}
			db.Exec(`INSERT INTO ont_query_log (project_id, user_question,
				generated_sql, objects, metric, group_by,
				execution_status, execution_result, execution_error, source_type, used_llm, mark)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'lakehouse',false,false)
				ON CONFLICT (project_id, user_question) DO UPDATE SET
					generated_sql=EXCLUDED.generated_sql, execution_status=EXCLUDED.execution_status,
					execution_result=EXCLUDED.execution_result, execution_error=EXCLUDED.execution_error`,
				projectID, q,
				res.SQL, strings.Join(objectNames, ","), "(plan)", "",
				execStatus, res.ResultJSON, res.ErrorMessage)
		}()
		resp := M{
			"execution_status":  execStatus,
			"execution_result":  res.ResultJSON,
			"execution_error":   res.ErrorMessage,
			"matched_intent":    hint.Name,
			"matched_intent_id": hint.IntentID,
			"displayMode":       "table",
			"plan_mode":         true,
			"involved": M{
				"kind":         "smartquery",
				"odNames":      objectNames,
				"propertyKeys": []M{},
			},
		}
		// Surface the step trace so the frontend can render the plan DAG
		// (per-step OD / status / row count / duration / SQL). Single source
		// of truth: lakehouse-sql-server filled LakehouseResult.PlanTrace
		// inside the executor — agent-server only forwards it.
		if res.PlanTrace != nil {
			resp["plan_trace"] = res.PlanTrace
		}
		return resp
	}

	// Build base spec from Intent. Objects come from Intent's lead Od;
	// IntentHint carries canonical_metric / auto_group_by / default_order /
	// default_limit / canonical_filters that the lakehouse pipeline applies
	// via PassApplyIntentHint. Spec at this point is "skeleton + intent" —
	// BindIntentParams next fills user-level params (Top N, filter values).
	spec := smartquery.QuerySpec{
		ProjectID:   projectID,
		Objects:     objectNames,
		IntentHint:  hint,
		DisplayMode: "table",
	}

	// Type-validated parameter binding (defense line 1).
	rawParams, _ := args["params"].(map[string]interface{})
	// Bounded value-ref contract (spec
	// .omc/specs/bounded-value-ref-contract.md): for every enum_ref
	// parameter, pre-resolve the candidate set from lakehouse_keyword so
	// the binder can fail loudly (PARAM_VALUE_UNKNOWN) when the LLM
	// invents a value. Pure pass-through for non-enum_ref params, and
	// graceful degrade (leaves AllowedValues nil → string-typed behavior)
	// for properties whose candidate set exceeds the cap or fails to
	// query — see applyEnumRefCandidates / resolveEnumRefCandidates.
	intentParams = applyEnumRefCandidates(db, projectID, intentParams)
	if err := smartquery.BindIntentParams(&spec, rawParams, intentParams); err != nil {
		var re *smartquery.ResolveError
		code := "PARAM_BIND_ERROR"
		if errors.As(err, &re) && re.Code != "" {
			code = re.Code
		}
		return M{
			"error": fmt.Sprintf("%s: %s", code, err.Error()),
			"code":  code,
		}
	}

	matchedIntentID := hint.IntentID
	matchedIntentName := hint.Name

	// ── LIMIT-strip for Intent pivot (pre-execution) ──
	// When an Intent with pivot_on will run AND user requested LIMIT N AND
	// pivot_on is in spec.GroupBy, the SQL's LIMIT would truncate raw
	// (dim, pivot_value) pairs BEFORE pivot aggregation. Result: top-N
	// products lose smaller pivot-value buckets (e.g. 未转换的Real Order rows when
	// Real Order rows dominate top of ORDER BY). The post-pivot wide rows
	// then show 0 for the missing bucket — silently wrong.
	//
	// Fix: snapshot spec.Limit, set to 0 so SQL returns all rows, then
	// reapply the limit AFTER applyIntentPivot has built wide rows.
	pivotLimit := 0
	if !spec.AddShareColumn && spec.Limit > 0 {
		if pivotOn := intentPivotOnForSpec(db, projectID, userQuestion, spec.Objects); pivotOn != "" {
			for _, g := range spec.GroupBy {
				if strings.EqualFold(g, pivotOn) {
					pivotLimit = spec.Limit
					spec.Limit = 0
					log.Printf("intent DEBUG: stripping spec.Limit=%d for pivot_on=%q (will reapply after pivot)", pivotLimit, pivotOn)
					break
				}
			}
		}
	}

	// Ensure every filter-referenced prop also appears as a groupBy column.
	// Rationale: dense SQL only emits columns from GroupByCols into SELECT; a
	// filter-only prop (e.g. BRAND='Yoga 2-in-1') silently disappears from the
	// output table. Promoting eq/IN filter props to groupBy makes the result
	// self-describing ("this row is YOGA 2-in-1 × NA × Real Order = 9891")
	// without changing row semantics: an "=" filter produces a constant column,
	// an "IN" filter produces rows per matched value. Non-equality filters
	// (>, <, BETWEEN, LIKE) are NOT promoted — they typically have unbounded
	// cardinality and would explode the row count.
	promoteFilterPropsToGroupBy(&spec)

	// Server-side safety net: any prop referenced by filters / groupBy / orderBy
	// must have its owning Od in spec.Objects. LLM sometimes copies the Intent's
	// objects array verbatim and forgets to append cross-Od refs (e.g. BRAND
	// lives on PRODUCT, not ORDER) — that produces "column ORDER.BRAND does not
	// exist" errors. This guard auto-adds missing Ods and surfaces a warning so
	// the LLM can learn from the correction in subsequent turns.
	objectsWarnings := ensureObjectsCoverReferencedProps(db, projectID, &spec)

	// matchedIntentID / matchedIntentName were already populated above by
	// lookupIntentHint — no second DB round-trip needed.
	_ = matchedIntentID

	// P1b: thread the turn's ctx so the RemoteClient's cross_service_http
	// span nests under agent.turn and W3C traceparent carries the turn's
	// span context into the service side. Behavior change: SSE client
	// disconnects now cancel the in-flight SQL request instead of letting
	// it run to completion — desired (no wasted DB compute on abandoned
	// queries).
	result := smartqueryExec(db).Execute(ctx, spec)

	if result.ErrorMessage != "" && result.SQL == "" {
		return M{"error": result.ErrorMessage, "debug": result.DebugInfo}
	}

	execStatus := "error"
	if result.ExecutionOK {
		execStatus = "success"
	}

	// Build keyword correction trace.
	tokenMappings := make([]M, 0, len(result.DebugInfo.KeywordCorrections))
	for _, kc := range result.DebugInfo.KeywordCorrections {
		tokenMappings = append(tokenMappings, M{
			"prop": kc.Prop, "userValue": kc.UserValue, "dbValue": kc.DBValue, "status": kc.Status,
		})
	}

	// Build graph highlight trace.
	odNameSet := map[string]bool{}
	propKeySet := map[string]bool{}
	var odNames []string
	var propertyKeys []M
	for _, p := range result.DebugInfo.ResolvedProps {
		if p.ObjectName != "" && !odNameSet[p.ObjectName] {
			odNameSet[p.ObjectName] = true
			odNames = append(odNames, p.ObjectName)
		}
		key := p.ObjectName + "." + p.Name
		if p.Name != "" && p.ObjectName != "" && !propKeySet[key] {
			propKeySet[key] = true
			propertyKeys = append(propertyKeys, M{"odName": p.ObjectName, "propName": p.Name})
		}
	}
	for _, name := range spec.Objects {
		if name != "" && !odNameSet[name] {
			odNameSet[name] = true
			odNames = append(odNames, name)
		}
	}
	if propertyKeys == nil {
		propertyKeys = []M{}
	}
	if odNames == nil {
		odNames = []string{}
	}

	// ── Intent-aware pivot (post-processing) ──
	// If any Metric Intent on one of spec.Objects declares pivot_on, and that
	// column appears in the result rows, we pivot long-format → wide-format
	// server-side. This guarantees the deterministic "未转换的Real Order / Real Order
	// / Total" (or similar) column layout the UI + downstream LLM expect.
	resultJSON := result.ResultJSON
	pivotedInfo := M{}
	percentAxis, _ := args["percentAxis"].(string)
	// Skip Intent pivot when universal share is on — share column already
	// added in SQL by engine; pivot post-processing would reshape rows long→wide
	// (using Intent's pivot_on) and silently drop the share column.
	if result.ExecutionOK && resultJSON != "" && len(spec.Objects) > 0 && !spec.AddShareColumn {
		if pivoted, info := applyIntentPivot(ctx, db, projectID, userQuestion, spec, resultJSON, percentAxis, pivotLimit); pivoted != "" {
			resultJSON = pivoted
			pivotedInfo = info
		}
	}

	// Compute row summary BEFORE the preview-truncation block below collapses
	// resultJSON to 10 rows — the LLM needs to see total / data / summary /
	// distinct counts from the FULL result, otherwise previewWarning makes
	// it think there are only 10 rows. resultJSON at this point is post-pivot
	// (if Intent pivot ran) but pre-truncation, which is exactly what we want.
	rowSummary := computeRowSummary(resultJSON, pivotedInfo, spec)

	// Row-count guard.
	isPreview := false
	totalRows := 0
	if result.ExecutionOK && resultJSON != "" {
		var rows []interface{}
		if json.Unmarshal([]byte(resultJSON), &rows) == nil {
			totalRows = len(rows)
			if totalRows > 200 {
				isPreview = true
				preview := rows[:10]
				if b, err := json.Marshal(preview); err == nil {
					resultJSON = string(b)
				}
			}
		}
	}
	previewWarning := ""
	if isPreview {
		previewWarning = fmt.Sprintf("⚠ 查询返回 %d 行数据，数量过多。以下仅展示前 10 行作为数据预览。请增加 groupBy 聚合维度或收紧 filters 条件来缩减结果集。", totalRows)
		if result.ErrorMessage == "" {
			result.ErrorMessage = previewWarning
		}
	}

	// Persist to ont_query_log for flywheel (best-effort, async).
	go func() {
		q := userQuestion
		if q == "" {
			q = fmt.Sprintf("[LH] %s BY %s FROM %s", spec.Metric, strings.Join(spec.GroupBy, ","), strings.Join(spec.Objects, ","))
		}
		db.Exec(`INSERT INTO ont_query_log (project_id, user_question,
			generated_sql, objects, metric, group_by,
			execution_status, execution_result, execution_error, source_type, used_llm, mark)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'lakehouse',false,false)
			ON CONFLICT (project_id, user_question) DO UPDATE SET
				generated_sql=EXCLUDED.generated_sql, execution_status=EXCLUDED.execution_status,
				execution_result=EXCLUDED.execution_result, execution_error=EXCLUDED.execution_error`,
			projectID, q,
			result.SQL, strings.Join(spec.Objects, ","), spec.Metric, strings.Join(spec.GroupBy, ","),
			execStatus, resultJSON, result.ErrorMessage)
	}()

	resp := M{
		"ontology_sql":     result.OntologySQL,
		"generated_sql":    result.SQL,
		"execution_status": execStatus,
		"execution_result": resultJSON,
		"execution_error":  result.ErrorMessage,
		"tokenMappings":    tokenMappings,
		"displayMode":      spec.DisplayMode,
		"summary":          "",
		"preview_warning":  previewWarning,
		"total_rows":       totalRows,
		"involved": M{
			"kind":         "smartquery",
			"odNames":      odNames,
			"propertyKeys": propertyKeys,
		},
	}
	if len(objectsWarnings) > 0 {
		// Surface guard corrections to the LLM so it can self-correct on the
		// next turn (and so operators can see the LLM's spec drift in logs).
		resp["warnings"] = objectsWarnings
	}
	if rowSummary != nil {
		resp["row_summary"] = rowSummary
	}
	if len(pivotedInfo) > 0 {
		resp["pivoted"] = pivotedInfo
		// Prefer the Intent name applyIntentPivot resolved from the rendered
		// rows (keyword-aware on actual pivot values) over the one
		// lookupIntentHint picked from priority + question text alone.
		if iName, ok := pivotedInfo["intentName"].(string); ok && iName != "" {
			matchedIntentName = iName
		}
		// Expose summary_toon as a top-level field. The data table never
		// contains aggregate rows; summary aggregates ride here as a
		// compact TOON block the LLM consumes alongside the table.
		if st, ok := pivotedInfo["summaryToon"].(string); ok && st != "" {
			resp["summary_toon"] = st
		}
	}
	if matchedIntentName != "" {
		resp["matched_intent"] = matchedIntentName
	}
	if matchedIntentID != "" {
		resp["matched_intent_id"] = matchedIntentID
	}
	// P7.5 bound_spec — full server-side spec post-IntentHint + params binding,
	// surfaced for debugging. The LLM sees this in tool result so it can
	// understand exactly what filters/groupBy/limit/orderBy ran (vs. what
	// Intent + params produced). Frontend debug panel reads from this too.
	specOrderBy := make([]M, 0, len(spec.OrderBy))
	for _, o := range spec.OrderBy {
		specOrderBy = append(specOrderBy, M{"prop": o.Prop, "dir": o.Dir})
	}
	specFiltersFull := make([]M, 0, len(spec.Filters))
	for _, f := range spec.Filters {
		row := M{"prop": f.Prop, "op": f.Op, "value": f.Value}
		if f.FuzzyMatch {
			row["fuzzyMatch"] = true
		}
		specFiltersFull = append(specFiltersFull, row)
	}
	resp["bound_spec"] = M{
		"objects":  spec.Objects,
		"metric":   spec.Metric,
		"groupBy":  spec.GroupBy,
		"filters":  specFiltersFull,
		"orderBy":  specOrderBy,
		"limit":    spec.Limit,
		"intentId": matchedIntentID,
		"intent":   matchedIntentName,
	}
	// ── Empty-result hint ──
	// When the query returned 0 rows AND it has equality filters on
	// non-numeric values without fuzzyMatch, flag candidates the LLM should
	// retry with fuzzyMatch=true. This is the structured form of "your
	// PRODUCT_NAME='Yoga' returned nothing — try ILIKE '%Yoga%' instead".
	//
	// Numeric filter values (years, IDs) are excluded — those are usually
	// either correct or genuinely unmatched.
	if result.ExecutionOK && totalRows == 0 && len(spec.Filters) > 0 {
		var suggestable []string
		for _, f := range spec.Filters {
			if f.FuzzyMatch {
				continue
			}
			op := strings.ToLower(strings.TrimSpace(f.Op))
			if op != "" && op != "=" && op != "==" && op != "in" {
				continue
			}
			v := strings.TrimSpace(f.Value)
			if v == "" {
				continue
			}
			// Skip purely-numeric values (years, IDs).
			if _, err := strconv.ParseFloat(v, 64); err == nil {
				continue
			}
			suggestable = append(suggestable, fmt.Sprintf("%s=%q", f.Prop, v))
		}
		if len(suggestable) > 0 {
			resp["empty_result_hint"] = fmt.Sprintf(
				"查询返回 0 行。建议把以下 filter 的 fuzzyMatch 设为 true（生成 ILIKE 模糊匹配，绝对**不要**删 filter）：%s。例如：{prop:..., op:=, value:..., fuzzyMatch:true}。",
				strings.Join(suggestable, "、"))
		}
	}
	// ── Suspicious all-zero hint (Tier 2 tripwire) ──
	// A multi-OD query that returns rows but whose every metric (non-groupBy)
	// column is zero/null across every row is almost always a JOIN that
	// matched nothing — typically a filter VALUE that does not exist (wrong
	// date format, misspelled enum), not a wrong Intent. SQL succeeded and a
	// row came back, so neither the empty-result hint (total_rows==0) nor
	// reflect's shape-check fires. Flag it explicitly and steer the fix
	// toward the filter values rather than re-recall.
	if result.ExecutionOK && totalRows > 0 && len(spec.Objects) > 1 &&
		spec.Metric != "" && suspiciousAllZero(resultJSON, spec.GroupBy) {
		resp["suspicious_zero_hint"] = "查询成功返回了维度行，但所有指标列在每一行都是 0/NULL。" +
			"这通常是跨 OD 的 JOIN 一行没匹配上——极可能某个 filter 的【值】不存在" +
			"（日期格式写错、枚举名拼错），而不是 Intent 选错。请先核对 filter 的 value" +
			"（尤其日期/期间是否为 YYYY-MM），不要急着 re_recall 换 Intent。"
	}
	// Expose spec/intent metadata to the agent loop so it can build synth args.
	resp["_spec_metric"] = spec.Metric
	resp["_spec_groupBy"] = spec.GroupBy
	specFiltersOut := make([]M, 0, len(spec.Filters))
	for _, f := range spec.Filters {
		specFiltersOut = append(specFiltersOut, M{"prop": f.Prop, "op": f.Op, "value": f.Value})
	}
	resp["_spec_filters"] = specFiltersOut
	return resp
}

// isZeroish reports whether a result-cell value counts as zero/empty. Numeric
// columns from the PG driver arrive as JSON strings ("0", "0.00"), so string
// parsing is required alongside the native numeric cases.
func isZeroish(v interface{}) bool {
	switch x := v.(type) {
	case nil:
		return true
	case float64:
		return x == 0
	case int:
		return x == 0
	case bool:
		return !x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return true
		}
		f, err := strconv.ParseFloat(s, 64)
		return err == nil && f == 0
	default:
		return false
	}
}

// suspiciousAllZero reports whether every metric (non-groupBy) column is
// zero/null across every result row. groupBy entries may be "OD.prop" form —
// the last dotted segment is matched case-insensitively against column names.
// Returns false on parse failure or empty result (the empty-result hint owns
// the 0-row case).
func suspiciousAllZero(resultJSON string, groupBy []string) bool {
	var rows []map[string]interface{}
	if json.Unmarshal([]byte(resultJSON), &rows) != nil || len(rows) == 0 {
		return false
	}
	dim := make(map[string]bool, len(groupBy))
	for _, g := range groupBy {
		seg := g
		if i := strings.LastIndex(seg, "."); i >= 0 {
			seg = seg[i+1:]
		}
		dim[strings.ToLower(strings.TrimSpace(seg))] = true
	}
	metricCells := 0
	for _, row := range rows {
		for k, v := range row {
			if dim[strings.ToLower(k)] {
				continue
			}
			metricCells++
			if !isZeroish(v) {
				return false
			}
		}
	}
	return metricCells > 0
}

// lookupIntentHint queries lakehouse_metric_intent for the highest-priority
// Intent applicable to spec.Objects whose keyword/alias appears verbatim
// in userQuestion. It does NOT mutate spec — instead it returns an
// IntentHint that the lakehouse pipeline applies on its side via
// PassApplyIntentHint. Same priority + keyword gate as the previous
// in-place enforcement; just decoupled from spec mutation so the SQL
// service is the single owner of "spec → spec" transforms.
//
// Returns nil when no Intent qualifies.
func lookupIntentHint(db *sql.DB, projectID, userQuestion string, objects []string) *smartquery.IntentHint {
	if len(objects) == 0 {
		return nil
	}
	// Keyword-match gate: lakehouse_keyword.keyword OR aliases must appear
	// in userQuestion (length ≥ 2 to avoid single-char triggers like "的").
	const keywordMatchExpr = `(EXISTS (
			SELECT 1 FROM lakehouse_keyword lk
			WHERE lk.metric_intent_id = mi.id
			  AND lk.project_id = mi.project_id
			  AND COALESCE(lk.is_stopword, false) = false
			  AND (
			    (LENGTH(lk.keyword) >= 2 AND LOWER($Q) LIKE '%'||LOWER(lk.keyword)||'%')
			    OR EXISTS (
			      SELECT 1 FROM unnest(COALESCE(lk.aliases,'{}'::text[])) a
			      WHERE LENGTH(a) >= 2 AND LOWER($Q) LIKE '%'||LOWER(a)||'%'
			    )
			  )
		))`
	var intentID, intentName, canonicalMetric string
	var autoGB pq.StringArray
	var replaceGB bool
	var defaultOrderLabel, defaultOrderDir sql.NullString
	var defaultLimit sql.NullInt64
	var canonicalFiltersJSON, parametersJSON []byte
	query := `
		SELECT mi.id::text, mi.name, COALESCE(mi.canonical_metric,''),
		       COALESCE(mi.auto_group_by, '{}'::text[]),
		       COALESCE(mi.replace_group_by, false),
		       mi.default_order_by_label, mi.default_order_by_dir, mi.default_limit,
		       COALESCE(mi.canonical_filters, '[]'::jsonb),
		       COALESCE(mi.parameters, '[]'::jsonb)
		FROM lakehouse_metric_intent mi
		JOIN ont_object_type o ON mi.object_id = o.id
		WHERE mi.project_id = $1
		  AND COALESCE(mi.mark, true) = true
		  AND COALESCE(o.mark, true) = true
		  AND LOWER(o.name) = ANY(SELECT LOWER(unnest($2::text[])))
		ORDER BY ` + strings.ReplaceAll(keywordMatchExpr, "$Q", "$3") + ` DESC,
		         mi.priority DESC
		LIMIT 1`
	args := []interface{}{projectID, pq.Array(objects), userQuestion}
	if err := db.QueryRow(query, args...).Scan(
		&intentID, &intentName, &canonicalMetric, &autoGB, &replaceGB,
		&defaultOrderLabel, &defaultOrderDir, &defaultLimit,
		&canonicalFiltersJSON, &parametersJSON,
	); err != nil {
		return nil
	}
	// Hard gate: no keyword match → no hint. Mirrors applyIntentPivot's
	// selection rule so priority-only auto-fire can't inject irrelevant
	// dimensions for unrelated questions on the same Od.
	if userQuestion != "" {
		var hasKwMatch bool
		gateQ := `SELECT ` + strings.ReplaceAll(keywordMatchExpr, "$Q", "$2") + `
			FROM lakehouse_metric_intent mi WHERE mi.id = $1`
		if err := db.QueryRow(gateQ, intentID, userQuestion).Scan(&hasKwMatch); err == nil && !hasKwMatch {
			log.Printf("intent DEBUG: hint suppressed — no keyword match for Intent %s in question %q", intentID, userQuestion)
			return nil
		}
	}
	hint := &smartquery.IntentHint{
		IntentID:        intentID,
		Name:            intentName,
		CanonicalMetric: canonicalMetric,
		AutoGroupBy:     []string(autoGB),
		ReplaceGroupBy:  replaceGB,
	}
	if defaultOrderLabel.Valid {
		hint.DefaultOrderByLabel = defaultOrderLabel.String
	}
	if defaultOrderDir.Valid {
		hint.DefaultOrderByDir = defaultOrderDir.String
	}
	if defaultLimit.Valid && defaultLimit.Int64 > 0 {
		hint.DefaultLimit = int(defaultLimit.Int64)
	}
	// canonical_filters JSONB → []FilterItem
	if len(canonicalFiltersJSON) > 0 {
		var raw []map[string]interface{}
		if err := json.Unmarshal(canonicalFiltersJSON, &raw); err == nil {
			for _, fm := range raw {
				prop, _ := fm["prop"].(string)
				op, _ := fm["op"].(string)
				val, _ := fm["value"].(string)
				if op == "" {
					op = "="
				}
				if prop != "" {
					hint.CanonicalFilters = append(hint.CanonicalFilters, smartquery.FilterItem{Prop: prop, Op: op, Value: val})
				}
			}
		}
	}
	return hint
}

// isPlanIntent reports whether the lakehouse_metric_intent.plan JSONB value
// read from the DB represents a composite Intent. NULL → empty bytes;
// 'null'::jsonb → string "null"; '{}'::jsonb → empty object. Anything else
// non-empty is treated as a plan to be parsed downstream.
func isPlanIntent(planJSON []byte) bool {
	s := strings.TrimSpace(string(planJSON))
	if s == "" || s == "null" || s == "{}" {
		return false
	}
	return true
}

// lookupIntentByName loads the full Metric Intent record by name (strict-mode
// dispatch path). Unlike lookupIntentHint — which finds the highest-priority
// keyword-gated intent for a question — this resolves an explicit intent name
// the LLM provided in `smartquery({intent: "..."})`. Returns:
//
//   - hint:        IntentHint to attach to spec.IntentHint (consumed by
//                  lakehouse pipeline's PassApplyIntentHint)
//   - objectNames: lead Od name array used for spec.Objects
//   - params:      typed parameter schema for BindIntentParams
//   - notFound:    true when intent doesn't exist (caller emits INTENT_NOT_FOUND);
//                  false on success
//
// IntentParameter type comes from agent-server/smartquery package — single
// source of truth for both the schema definition and BindIntentParams.
func lookupIntentByName(db *sql.DB, projectID, intentName string) (
	hint *smartquery.IntentHint,
	objectNames []string,
	params []smartquery.IntentParameter,
	planJSON []byte,
	notFound bool,
) {
	intentName = strings.TrimSpace(intentName)
	if intentName == "" {
		return nil, nil, nil, nil, true
	}
	var (
		intentID, intentNameOut, canonicalMetric, objectName string
		autoGB                                               pq.StringArray
		replaceGB                                            bool
		defaultOrderLabel, defaultOrderDir                   sql.NullString
		defaultLimit                                         sql.NullInt64
		canonicalFiltersJSON, parametersJSON, planBytes      []byte
	)
	err := db.QueryRow(`
		SELECT mi.id::text, mi.name,
		       COALESCE(mi.canonical_metric, ''),
		       COALESCE(mi.auto_group_by, '{}'::text[]),
		       COALESCE(mi.replace_group_by, false),
		       mi.default_order_by_label, mi.default_order_by_dir, mi.default_limit,
		       COALESCE(mi.canonical_filters, '[]'::jsonb),
		       COALESCE(mi.parameters, '[]'::jsonb),
		       COALESCE(o.name, ''),
		       mi.plan
		FROM lakehouse_metric_intent mi
		JOIN ont_object_type o ON mi.object_id = o.id
		WHERE mi.project_id = $1
		  AND COALESCE(mi.mark, true) = true
		  AND COALESCE(o.mark, true) = true
		  AND LOWER(mi.name) = LOWER($2)
		LIMIT 1`,
		projectID, intentName).Scan(
		&intentID, &intentNameOut, &canonicalMetric, &autoGB, &replaceGB,
		&defaultOrderLabel, &defaultOrderDir, &defaultLimit,
		&canonicalFiltersJSON, &parametersJSON, &objectName, &planBytes,
	)
	if err != nil {
		log.Printf("intent DEBUG: lookupIntentByName(%q) miss: %v", intentName, err)
		return nil, nil, nil, nil, true
	}
	planJSON = planBytes
	hint = &smartquery.IntentHint{
		IntentID:        intentID,
		Name:            intentNameOut,
		CanonicalMetric: canonicalMetric,
		AutoGroupBy:     []string(autoGB),
		ReplaceGroupBy:  replaceGB,
	}
	if defaultOrderLabel.Valid {
		hint.DefaultOrderByLabel = defaultOrderLabel.String
	}
	if defaultOrderDir.Valid {
		hint.DefaultOrderByDir = defaultOrderDir.String
	}
	if defaultLimit.Valid && defaultLimit.Int64 > 0 {
		hint.DefaultLimit = int(defaultLimit.Int64)
	}
	if len(canonicalFiltersJSON) > 0 {
		var raw []map[string]interface{}
		if err := json.Unmarshal(canonicalFiltersJSON, &raw); err == nil {
			for _, fm := range raw {
				prop, _ := fm["prop"].(string)
				op, _ := fm["op"].(string)
				val, _ := fm["value"].(string)
				if op == "" {
					op = "="
				}
				if prop != "" {
					hint.CanonicalFilters = append(hint.CanonicalFilters,
						smartquery.FilterItem{Prop: prop, Op: op, Value: val})
				}
			}
		}
	}
	if len(parametersJSON) > 0 {
		_ = json.Unmarshal(parametersJSON, &params)
	}
	if objectName != "" {
		objectNames = []string{objectName}
	}
	// canonical_filters / auto_group_by may reference cross-OD properties in
	// "OD.Prop" form (e.g. "PRODUCT.CategoryID"). Those dim ODs MUST land in
	// spec.Objects or ResolveJoinPath has no edges to walk — the SQL builder
	// then strips the prefix onto the lead OD and the query dies with
	// `column <leadOD>.<prop> does not exist`. compose_query already tracks
	// referenced dim ODs into spec.Objects; the strict Intent path must too.
	seenOD := map[string]bool{strings.ToLower(objectName): true}
	addDimOD := func(prop string) {
		dot := strings.Index(prop, ".")
		if dot <= 0 {
			return
		}
		od := strings.TrimSpace(prop[:dot])
		if od == "" || seenOD[strings.ToLower(od)] {
			return
		}
		seenOD[strings.ToLower(od)] = true
		objectNames = append(objectNames, od)
	}
	for _, f := range hint.CanonicalFilters {
		addDimOD(f.Prop)
	}
	for _, gb := range hint.AutoGroupBy {
		addDimOD(gb)
	}
	return hint, objectNames, params, planJSON, false
}

// computeRowSummary turns the result rows into a structured summary the
// outer LLM can read at a glance: total rows, distinct count of dim-column
// combinations (= the meaningful "how many products / GEOs" answer when
// pivot has collapsed an axis), zero-data row count, and a human-readable
// note.
//
// Aggregate "summary rows" (筛选合计 / 全局合计) no longer appear in the
// table — they ride on resp["summary_toon"] — so this function does not
// scan for or count them.
//
// dimCols come from pivotedInfo["dimCols"] when Intent pivot ran (those are
// the groupBy cols MINUS pivot_on, which is what wide rows actually carry),
// otherwise fall back to spec.GroupBy.
func computeRowSummary(resultJSON string, pivotedInfo M, spec smartquery.QuerySpec) M {
	if resultJSON == "" {
		return nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(resultJSON), &rows); err != nil || len(rows) == 0 {
		return nil
	}
	var dimCols []string
	if dc, ok := pivotedInfo["dimCols"].([]string); ok && len(dc) > 0 {
		dimCols = dc
	} else {
		dimCols = append(dimCols, spec.GroupBy...)
	}
	if len(dimCols) == 0 {
		return nil
	}
	// Case-insensitive row lookup (mirror applyIntentPivot.getRowVal).
	lookup := func(row map[string]interface{}, key string) interface{} {
		if v, ok := row[key]; ok {
			return v
		}
		kl := strings.ToLower(key)
		for k, v := range row {
			if strings.ToLower(k) == kl {
				return v
			}
		}
		return nil
	}
	totalLabel, _ := pivotedInfo["totalLabel"].(string)
	var zeroDataRows int
	distinctKeys := map[string]bool{}
	for _, row := range rows {
		// Skip dim-cartesian-product empty rows (totalLabel value = 0). They
		// inflate "共 X 项" claims with rows that have NO real data — should
		// not be counted as "products with data".
		if totalLabel != "" {
			if t, ok := numericFromAny(lookup(row, totalLabel)); ok && t == 0 {
				zeroDataRows++
				continue
			}
		}
		var keyParts []string
		for _, d := range dimCols {
			keyParts = append(keyParts, fmt.Sprintf("%v", lookup(row, d)))
		}
		distinctKeys[strings.Join(keyParts, "|")] = true
	}
	var note string
	if zeroDataRows > 0 {
		note = fmt.Sprintf("结果共 %d 行（表格不含合计行；合计在 summary_toon）。其中 %d 行 %s=0（dim 笛卡尔积空行，无实际数据，已从 distinct_dim_items 排除）。按维度 (%s) **真实有数据的项目数 = %d**。",
			len(rows), zeroDataRows, totalLabel, strings.Join(dimCols, ", "), len(distinctKeys))
	} else {
		note = fmt.Sprintf("结果共 %d 行（表格不含合计行；合计在 summary_toon）。按维度 (%s) 真实有数据的项目数 = %d。",
			len(rows), strings.Join(dimCols, ", "), len(distinctKeys))
	}
	return M{
		"total_rows":         len(rows),
		"data_rows":          len(rows),
		"summary_rows":       0,
		"zero_data_rows":     zeroDataRows,
		"dim_columns":        dimCols,
		"distinct_dim_items": len(distinctKeys),
		"note":               note,
	}
}

// numericFromAny coerces any numeric-or-numeric-string value to float64.
// Returns ok=false when the input is neither numeric nor a parseable number,
// so the caller can distinguish "absent / unparseable" from "zero".
func numericFromAny(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		if n == "" {
			return 0, false
		}
		var f float64
		if _, err := fmt.Sscanf(n, "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// intentPivotOnForSpec mirrors applyIntentPivot's Intent selection (kw_match
// DESC, priority DESC, must have non-empty pivot_on) and returns just the
// pivot_on column name. Used pre-execution to detect "Intent pivot will run
// on this spec" so we can strip spec.Limit before SQL generation — otherwise
// LIMIT N truncates raw (dim, pivot_value) pairs and the post-pivot wide
// rows end up with zero-filled cells for product rows whose smaller
// pivot-value buckets fell outside the top N.
//
// Returns "" when no Intent with pivot_on applies, or when none have a
// keyword match in userQuestion (mirroring applyIntentPivot's hard gate).
func intentPivotOnForSpec(db *sql.DB, projectID, userQuestion string, objects []string) string {
	if len(objects) == 0 {
		return ""
	}
	const keywordMatchExpr = `(EXISTS (
			SELECT 1 FROM lakehouse_keyword lk
			WHERE lk.metric_intent_id = mi.id
			  AND lk.project_id = mi.project_id
			  AND COALESCE(lk.is_stopword, false) = false
			  AND (
			    (LENGTH(lk.keyword) >= 2 AND LOWER($Q) LIKE '%'||LOWER(lk.keyword)||'%')
			    OR EXISTS (
			      SELECT 1 FROM unnest(COALESCE(lk.aliases,'{}'::text[])) a
			      WHERE LENGTH(a) >= 2 AND LOWER($Q) LIKE '%'||LOWER(a)||'%'
			    )
			  )
		))`
	var pivotOn string
	query := `SELECT COALESCE(mi.pivot_on,'')
		FROM lakehouse_metric_intent mi
		JOIN ont_object_type o ON mi.object_id = o.id
		WHERE mi.project_id = $1
		  AND COALESCE(mi.mark, true) = true
		  AND COALESCE(o.mark, true) = true
		  AND mi.pivot_on IS NOT NULL AND mi.pivot_on <> ''
		  AND LOWER(o.name) = ANY(SELECT LOWER(unnest($2::text[])))
		ORDER BY ` + strings.ReplaceAll(keywordMatchExpr, "$Q", "$3") + ` DESC,
		         mi.priority DESC
		LIMIT 1`
	args := []interface{}{projectID, pq.Array(objects), userQuestion}
	if err := db.QueryRow(query, args...).Scan(&pivotOn); err != nil {
		return ""
	}
	// Hard gate: require at least one matched keyword (mirrors applyIntentPivot).
	if userQuestion != "" && pivotOn != "" {
		var hasKw bool
		gateQ := `SELECT ` + strings.ReplaceAll(keywordMatchExpr, "$Q", "$2") + `
			FROM lakehouse_metric_intent mi WHERE mi.pivot_on = $1`
		_ = db.QueryRow(gateQ, pivotOn, userQuestion).Scan(&hasKw)
		if !hasKw {
			return ""
		}
	}
	return pivotOn
}

// promoteFilterPropsToGroupBy appends every equality / IN filter's prop to
// spec.GroupBy when it's not already present. This makes the SQL's SELECT
// self-describing — a row filtered to BRAND='YOGA 2-in-1' will show the
// BRAND column in the result, instead of the filter value being invisible.
//
// Operators considered low-cardinality (safe to promote):
//   - ""                (default, treated as "=")
//   - "=", "==", "eq"
//   - "in", "IN"
//
// Non-equality operators (>, <, >=, <=, !=, LIKE, BETWEEN, ...) are NOT
// promoted — they typically span many distinct values and would explode the
// output row count. The LLM can still reference such columns in groupBy
// explicitly if it wants them in SELECT.
func promoteFilterPropsToGroupBy(spec *smartquery.QuerySpec) {
	if len(spec.Filters) == 0 {
		return
	}
	wasGB := append([]string(nil), spec.GroupBy...)
	var promoted []string
	for _, f := range spec.Filters {
		op := strings.ToLower(strings.TrimSpace(f.Op))
		switch op {
		case "", "=", "==", "eq", "in":
			// eligible
		default:
			continue
		}
		// AppendGroupBy is the only mutation path. It dedups via
		// CanonicalPropKey so `"PRODUCT.CATALOG_NAME"` filter and a pre-
		// existing `"CATALOG_NAME"` groupBy no longer produce a duplicate —
		// this is the anti-seesaw contract in action.
		if spec.AppendGroupBy(f.Prop) {
			promoted = append(promoted, f.Prop)
		}
	}
	if len(promoted) > 0 {
		log.Printf("filter→groupBy DEBUG: promoting filter props %v into spec.GroupBy (was %v, now %v)", promoted, wasGB, spec.GroupBy)
	}
}

// ensureObjectsCoverReferencedProps guarantees that every prop referenced by
// filters / groupBy / orderBy has its owning Od in spec.Objects. The LLM's
// discretion over the `objects` field is unreliable: it sometimes copies the
// Intent's single-Od literal and omits cross-Od refs, which produces SQL like
// `WHERE ORDER.BRAND = ...` when BRAND actually lives on PRODUCT.
//
// For each referenced prop we query ont_property × ont_object_type. If ANY
// owner is already in spec.Objects, it's covered; otherwise we append the
// first owner (alphabetical by query order) and record a warning so the tool
// response can echo it back to the LLM.
//
// Ambiguous prop names (owned by multiple Ods, none covered) pick the first
// match — real ambiguity here should be rare in a well-designed ontology;
// caller can disambiguate via groupBy after seeing the warning.
func ensureObjectsCoverReferencedProps(db *sql.DB, projectID string, spec *smartquery.QuerySpec) []string {
	// Collect unique prop names mentioned in filters / groupBy / orderBy.
	propSeen := map[string]bool{}
	var propOrdered []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		k := strings.ToLower(name)
		if !propSeen[k] {
			propSeen[k] = true
			propOrdered = append(propOrdered, name)
		}
	}
	for _, f := range spec.Filters {
		add(f.Prop)
	}
	for _, g := range spec.GroupBy {
		add(g)
	}
	for _, o := range spec.OrderBy {
		add(o.Prop)
	}
	if len(propOrdered) == 0 {
		return nil
	}

	// Existing objects as a lowercased set.
	objSet := map[string]bool{}
	for _, o := range spec.Objects {
		objSet[strings.ToLower(o)] = true
	}

	// Resolve prop → Od owners in one query.
	//
	// Schema note: The `mark` columns on these tables are curation flags with
	//   project-specific semantics (ont_property.mark defaults to false
	//   for ALL rows in a freshly-ingested project — filtering by it
	//   here would exclude every property and silently return "no
	//   owner" for every prop, which is the exact bug we're fixing).
	//   This function is a name-resolution helper that should find the
	//   owner regardless of curation state; the caller handles whether
	//   the resulting Od is usable.
	query := `
		SELECT p.name, o.name
		FROM ont_property p
		JOIN ont_object_type o ON p.object_type_id = o.id
		WHERE o.project_id = $1
		  AND LOWER(p.name) = ANY(SELECT LOWER(unnest($2::text[])))`
	args := []interface{}{projectID, pq.Array(propOrdered)}
	rows, err := db.Query(query, args...)
	if err != nil {
		log.Printf("objects DEBUG: prop→Od lookup failed: %v", err)
		return nil
	}
	defer rows.Close()

	propToOds := map[string][]string{}
	for rows.Next() {
		var propName, odName string
		if err := rows.Scan(&propName, &odName); err != nil {
			continue
		}
		propToOds[strings.ToLower(propName)] = append(propToOds[strings.ToLower(propName)], odName)
	}

	var warnings []string
	for _, prop := range propOrdered {
		owners := propToOds[strings.ToLower(prop)]
		if len(owners) == 0 {
			continue // unknown prop — let resolver report it
		}
		// Already covered?
		covered := false
		for _, od := range owners {
			if objSet[strings.ToLower(od)] {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		chosen := owners[0]
		objSet[strings.ToLower(chosen)] = true
		spec.Objects = append(spec.Objects, chosen)
		msg := fmt.Sprintf("auto-added Od %q to objects because prop %q belongs to it", chosen, prop)
		warnings = append(warnings, msg)
		log.Printf("objects DEBUG: %s", msg)
	}
	return warnings
}

// applyIntentPivot rewrites resultJSON from long format to wide format using
// the pivot config on the best-matching Metric Intent for spec.Objects.
// Returns the rewritten JSON (or "" if no Intent applies / no pivot column in
// groupBy) plus a diagnostic M.
//
// Long-format input (example):
//
//	[{"Geo":"AP","Order_Type":"未转换的Real Order","Total_Order_Quantity":"9891"},
//	 {"Geo":"AP","Order_Type":"Real Order", "Total_Order_Quantity":"31140"}, ...]
//
// Wide-format output (pivot_on=Order_Type, pivot_values=[未转换的Real Order,Real Order],
// total_label="Total"):
//
//	[{"Geo":"AP","未转换的Real Order":9891,"Real Order":31140,"Total":41031}, ...]
//
// Intent selection is two-tier:
//
//  1. Intents with a lakehouse_keyword (or alias) that appears verbatim inside
//     userQuestion come first. This lets "每个 Geo 订单占比" select
//     Order.Quantity.Distribution (keyword '占比') while "每个 Geo 订单数量"
//     selects Order.Quantity (keyword '订单'). Two siblings on the same Od
//     can coexist with different pivot axes / grand-total toggles.
//  2. Ties (both matched, or neither matched) fall back to priority DESC
//     — identical to the legacy behaviour, so callers that never pass
//     userQuestion still get the pre-refactor result.
//
// Minimum keyword length 2 avoids noise like "的" / single letters spuriously
// matching every question.
func applyIntentPivot(ctx context.Context, db *sql.DB, projectID, userQuestion string, spec smartquery.QuerySpec, resultJSON string, percentAxisOverride string, pivotLimit int) (string, M) {
	log.Printf("pivot DEBUG: applyIntentPivot ENTER — userQuestion=%q (len=%d) objects=%v",
		userQuestion, len(userQuestion), spec.Objects)
	if len(spec.Objects) == 0 || resultJSON == "" {
		log.Printf("pivot DEBUG: empty objects or json (objects=%d jsonLen=%d)", len(spec.Objects), len(resultJSON))
		return "", nil
	}

	// Find an Intent on these Objects with pivot_on set. First match wins;
	// priority DESC so the most specific intent for this Od takes precedence.
	var pivotOn, totalLabel string
	var pivotValues pq.StringArray
	// Extra knobs: optional per-value display labels, append percentage cols,
	// append a grand-total summary row at the bottom.
	var pivotLabels pq.StringArray
	var withPercent, appendGrandTotal bool
	var dbPercentAxis string

	var percentScope, canonicalFiltersJSON, percentSuffix, intentName, responseTemplate string
	const selectCols = `
		SELECT COALESCE(mi.pivot_on,''),
		       COALESCE(mi.pivot_values, '{}'::text[]),
		       COALESCE(mi.pivot_total_label,'Total'),
		       COALESCE(mi.pivot_column_labels, '{}'::text[]),
		       COALESCE(mi.pivot_with_percent, false),
		       COALESCE(mi.pivot_append_grand_total, false),
		       COALESCE(mi.pivot_percent_axis,'row'),
		       COALESCE(mi.pivot_percent_scope,'filtered'),
		       COALESCE(mi.canonical_filters,'[]'::jsonb)::text,
		       COALESCE(mi.pivot_percent_suffix,'占比'),
		       COALESCE(mi.name,''),
		       COALESCE(mi.response_template,'')`
	var err error
	// keywordMatchExpr ranks Intents whose lakehouse_keyword (or any alias) appears
	// verbatim in userQuestion above those that don't. Questions without any
	// keyword hit (or called without userQuestion) degrade to pure priority order.
	//
	// Minimum keyword/alias length 2 avoids single-char noise like "的" matching
	// everything. Lowercased on both sides so "Order"/"订单" behave the same.
	const keywordMatchExpr = `(EXISTS (
			SELECT 1 FROM lakehouse_keyword lk
			WHERE lk.metric_intent_id = mi.id
			  AND lk.project_id = mi.project_id
			  AND (
			    (LENGTH(lk.keyword) >= 2 AND LOWER($Q) LIKE '%'||LOWER(lk.keyword)||'%')
			    OR EXISTS (
			      SELECT 1 FROM unnest(COALESCE(lk.aliases,'{}'::text[])) a
			      WHERE LENGTH(a) >= 2 AND LOWER($Q) LIKE '%'||LOWER(a)||'%'
			    )
			  )
		))`
	// DEBUG: trace userQuestion into Intent selection.
	log.Printf("pivot DEBUG: applyIntentPivot Intent select input — projectID=%s objects=%v userQuestion=%q (len=%d)",
		projectID, spec.Objects, userQuestion, len(userQuestion))

	{
		q := selectCols + `
			FROM lakehouse_metric_intent mi
			JOIN ont_object_type o ON mi.object_id = o.id
			WHERE mi.project_id = $1
			  AND COALESCE(mi.mark, true) = true
			  AND COALESCE(o.mark, true) = true
			  AND mi.pivot_on IS NOT NULL AND mi.pivot_on <> ''
			  AND LOWER(o.name) = ANY(SELECT LOWER(unnest($2::text[])))
			ORDER BY ` + strings.ReplaceAll(keywordMatchExpr, "$Q", "$3") + ` DESC,
			         mi.priority DESC
			LIMIT 1`
		err = db.QueryRow(q, projectID, pq.Array(spec.Objects), userQuestion).Scan(
			&pivotOn, &pivotValues, &totalLabel,
			&pivotLabels, &withPercent, &appendGrandTotal, &dbPercentAxis,
			&percentScope, &canonicalFiltersJSON, &percentSuffix, &intentName, &responseTemplate)
	}
	// DEBUG: show which Intent won (and why it might not be what we expect).
	log.Printf("pivot DEBUG: Intent selected — name=%q pivotOn=%q scope=%q axis=%q canonicalFilters=%s err=%v",
		intentName, pivotOn, percentScope, dbPercentAxis, canonicalFiltersJSON, err)

	// Resolve final percent axis: smartquery arg > DB config > default "row"
	finalPercentAxis := "row"
	if percentAxisOverride == "row" || percentAxisOverride == "column" {
		finalPercentAxis = percentAxisOverride
	} else if dbPercentAxis == "column" {
		finalPercentAxis = "column"
	}
	if err != nil || pivotOn == "" {
		log.Printf("pivot DEBUG: lookup failed err=%v pivotOn=%q objects=%v", err, pivotOn, spec.Objects)
		return "", nil
	}

	// The pivot column must appear in the user's groupBy — otherwise there's
	// nothing to pivot against.
	inGB := false
	for _, g := range spec.GroupBy {
		if strings.EqualFold(g, pivotOn) {
			inGB = true
			break
		}
	}
	if !inGB {
		log.Printf("pivot DEBUG: pivot_on %q not in groupBy %v", pivotOn, spec.GroupBy)
		return "", nil
	}

	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(resultJSON), &rows); err != nil || len(rows) == 0 {
		log.Printf("pivot DEBUG: unmarshal failed err=%v rows=%d", err, len(rows))
		return "", nil
	}
	log.Printf("pivot DEBUG: applying pivot_on=%q pivotValues=%v rows=%d", pivotOn, []string(pivotValues), len(rows))

	// Identify dimension columns = groupBy minus pivotOn, preserving order.
	var dimCols []string
	for _, g := range spec.GroupBy {
		if !strings.EqualFold(g, pivotOn) {
			dimCols = append(dimCols, g)
		}
	}
	// Identify the metric column — typically "Total_<prop>" but we cope by
	// scanning the first row for the first non-dim, non-pivot column.
	var metricCol string
	for k := range rows[0] {
		if strings.EqualFold(k, pivotOn) {
			continue
		}
		isDim := false
		for _, d := range dimCols {
			if strings.EqualFold(k, d) {
				isDim = true
				break
			}
		}
		if !isDim {
			metricCol = k
			break
		}
	}
	if metricCol == "" {
		return "", nil
	}

	// Group rows by the dim tuple → map from pivot_on value to metric value.
	type bucket struct {
		dims   map[string]interface{}
		values map[string]float64
	}
	buckets := map[string]*bucket{}
	var bucketOrder []string
	observedPivotVals := map[string]bool{}

	// Case-insensitive key lookup for row maps. PostgreSQL via quoteColRef
	// lowercases column names in JSON output, but our pivotOn/dimCols/metricCol
	// may have mixed case from the ontology or Metric Intent config.
	getRowVal := func(row map[string]interface{}, key string) interface{} {
		// Try exact match first
		if v, ok := row[key]; ok {
			return v
		}
		// Fall back to case-insensitive match
		keyLower := strings.ToLower(key)
		for k, v := range row {
			if strings.ToLower(k) == keyLower {
				return v
			}
		}
		return nil
	}

	dimKey := func(row map[string]interface{}) string {
		var parts []string
		for _, d := range dimCols {
			parts = append(parts, fmt.Sprintf("%v", getRowVal(row, d)))
		}
		return strings.Join(parts, "||")
	}

	for _, row := range rows {
		k := dimKey(row)
		b, ok := buckets[k]
		if !ok {
			dimVals := map[string]interface{}{}
			for _, d := range dimCols {
				dimVals[d] = getRowVal(row, d)
			}
			b = &bucket{dims: dimVals, values: map[string]float64{}}
			buckets[k] = b
			bucketOrder = append(bucketOrder, k)
		}
		pVal := fmt.Sprintf("%v", getRowVal(row, pivotOn))
		observedPivotVals[pVal] = true
		b.values[pVal] = toFloat(getRowVal(row, metricCol))
	}

	// Determine column order: use pivot_values if provided, else sorted observed.
	var cols []string
	if len([]string(pivotValues)) > 0 {
		cols = []string(pivotValues)
	} else {
		for v := range observedPivotVals {
			cols = append(cols, v)
		}
		sort.Strings(cols)
	}

	// Label map: output column name per pivot value. Defaults to the value itself.
	labelFor := func(val string) string {
		labels := []string(pivotLabels)
		for i, pv := range cols {
			if i < len(labels) && labels[i] != "" && strings.EqualFold(val, pv) {
				return labels[i]
			}
		}
		return val
	}

	// Build wide rows with DETERMINISTIC column order.
	//
	// We can't use map[string]interface{} for rows because Go's encoding/json
	// sorts map keys alphabetically (by Unicode codepoint), so column order
	// becomes unpredictable — particularly bad for mixed Chinese/English keys
	// where 已(0x5DF2) < 总(0x603B) < 未(0x672A) gives a confusing default.
	//
	// Instead we use orderedRow ([]orderedField) with custom MarshalJSON that
	// preserves insertion order. The fixed business-friendly layout is:
	//   [dim cols (groupBy order, minus pivotOn)] →
	//   [Total label column] →
	//   [pivot value cols (labelFor() order)] →
	//   [percent cols (same order, only when withPercent=true)]
	//
	// Total appears immediately after dimension columns because it represents
	// the headline metric; the per-value breakdown and percentages elaborate
	// on it. Frontend ResultViewer renders columns via Object.keys(rows[0]),
	// which preserves JSON insertion order in JavaScript.
	wide := make([]orderedRow, 0, len(bucketOrder)+1)
	rowTotals := make([]float64, 0, len(bucketOrder)) // per-row totals for row-axis %
	gtByLabel := map[string]float64{}                 // per-column totals for column-axis %
	rowValByLabel := make([]map[string]float64, 0, len(bucketOrder))
	var gtTotal float64

	// Pre-compute the full ordered list of pivot value labels (whitelist + extras).
	// "Extras" are pivot values observed in data that weren't in pivot_values; we
	// keep them so nothing is silently dropped, but they appear after the
	// whitelist preserving deterministic order.
	orderedLabels := make([]string, 0, len(cols))
	orderedSrcVals := make([]string, 0, len(cols))
	seenLabel := map[string]bool{}
	for _, c := range cols {
		lbl := labelFor(c)
		if !seenLabel[lbl] {
			orderedLabels = append(orderedLabels, lbl)
			orderedSrcVals = append(orderedSrcVals, c)
			seenLabel[lbl] = true
		}
	}
	if len([]string(pivotValues)) > 0 {
		// Stable extras order: sort by raw pivot value string for determinism.
		var extras []string
		for v := range observedPivotVals {
			if !containsFold(cols, v) {
				extras = append(extras, v)
			}
		}
		sort.Strings(extras)
		for _, v := range extras {
			lbl := labelFor(v)
			if !seenLabel[lbl] {
				orderedLabels = append(orderedLabels, lbl)
				orderedSrcVals = append(orderedSrcVals, v)
				seenLabel[lbl] = true
			}
		}
	}

	// ── Pass 1: build rows in target column order, computing totals ──
	for _, k := range bucketOrder {
		b := buckets[k]
		// Per-label value lookup for percentage pass.
		vals := make(map[string]float64, len(orderedLabels))
		var rowTotal float64
		for i, lbl := range orderedLabels {
			v := b.values[orderedSrcVals[i]]
			vals[lbl] = v
			rowTotal += v
			gtByLabel[lbl] += v
		}
		gtTotal += rowTotal
		rowTotals = append(rowTotals, rowTotal)
		rowValByLabel = append(rowValByLabel, vals)

		// Now emit fields in the canonical column order.
		out := make(orderedRow, 0, len(dimCols)+1+len(orderedLabels)*2)
		for _, d := range dimCols {
			out = append(out, orderedField{Key: d, Value: b.dims[d]})
		}
		out = append(out, orderedField{Key: totalLabel, Value: rowTotal})
		for _, lbl := range orderedLabels {
			out = append(out, orderedField{Key: lbl, Value: vals[lbl]})
		}
		// Percent columns appended in Pass 2 (we need cross-row totals first
		// for column-axis %), but reserve placeholder slots in the right order.
		wide = append(wide, out)
	}

	// ── Drop zero-total rows (dim-cartesian-product artifacts) ──
	// Dense SQL does CROSS JOIN(dims) LEFT JOIN(fact) so every dim combo
	// shows up even when there's no fact data — those land here as
	// rowTotal=0. Per business rule (totalOrder = realOrder + 未转换 = 0
	// implies both buckets are 0), the row carries no information and
	// must not appear in the table. gtTotal/gtByLabel are accumulator
	// sums from the loop above; zero rows contributed 0, so dropping them
	// here doesn't change the summary aggregates computed downstream.
	if len(wide) > 0 {
		var keepWide []orderedRow
		var keepTotals []float64
		var keepValByLabel []map[string]float64
		dropped := 0
		for i, t := range rowTotals {
			if t == 0 {
				dropped++
				continue
			}
			keepWide = append(keepWide, wide[i])
			keepTotals = append(keepTotals, t)
			keepValByLabel = append(keepValByLabel, rowValByLabel[i])
		}
		if dropped > 0 {
			log.Printf("pivot DEBUG: dropped %d zero-total rows from %d wide rows", dropped, len(wide))
			wide = keepWide
			rowTotals = keepTotals
			rowValByLabel = keepValByLabel
		}
	}

	// ── Global percent scope: compute unfiltered column totals ──
	// When percentScope=="global" + column axis, the denominator for each
	// percentage column should be the column total from the FULL (unfiltered)
	// data, not the filtered result set. We achieve this by re-executing the
	// same query with only canonical_filters (no user-added filters).
	pctDenomByLabel := gtByLabel // default: filtered totals
	if percentScope == "global" && finalPercentAxis == "column" && withPercent {
		// Global denominator query:
		//   - Group only by pivotOn (no user dimensions) so each label maps to
		//     a single row and we can't miss buckets to a LIMIT truncation.
		//   - Drop Limit entirely (spec.Limit may be small and would truncate
		//     the universe, producing denominator < filtered → >100% shares).
		globalSpec := smartquery.QuerySpec{
			ProjectID: spec.ProjectID,
			Objects:   spec.Objects, Metric: spec.Metric,
			GroupBy: []string{pivotOn}, Limit: 0,
		}
		// Parse canonical_filters from Intent JSON → only these filters apply
		var cfs []struct {
			Prop  string `json:"prop"`
			Op    string `json:"op"`
			Value string `json:"value"`
		}
		if json.Unmarshal([]byte(canonicalFiltersJSON), &cfs) == nil {
			for _, cf := range cfs {
				op := cf.Op
				if op == "" {
					op = "="
				}
				globalSpec.Filters = append(globalSpec.Filters, smartquery.FilterItem{
					Prop: cf.Prop, Op: op, Value: cf.Value,
				})
			}
		}
		// P1b: thread the turn's ctx (applyIntentPivot now has it as first
		// param). The global-total smartquery pass shares the same
		// cross-service trace as the main smartquery call.
		globalResult := smartqueryExec(db).Execute(ctx, globalSpec)
		if globalResult.ExecutionOK && globalResult.ResultJSON != "" {
			var globalRows []map[string]interface{}
			if json.Unmarshal([]byte(globalResult.ResultJSON), &globalRows) == nil && len(globalRows) > 0 {
				globalGt := map[string]float64{}
				for _, row := range globalRows {
					// Case-insensitive key lookup (SQL engine may uppercase columns)
					var pv string
					var mv float64
					for k, v := range row {
						if strings.EqualFold(k, pivotOn) {
							pv, _ = v.(string)
						}
						if strings.EqualFold(k, metricCol) {
							mv = toFloat(v)
						}
					}
					lbl := labelFor(pv)
					globalGt[lbl] += mv
				}
				if len(globalGt) > 0 {
					pctDenomByLabel = globalGt
					log.Printf("pivot DEBUG: global percent scope — unfiltered totals: %v", globalGt)
				}
			}
		}
	}

	// ── Pass 2: append percentage columns in the canonical order ──
	if withPercent {
		for i := range wide {
			for _, lbl := range orderedLabels {
				val := rowValByLabel[i][lbl]
				var pct float64
				if finalPercentAxis == "column" {
					if colTotal := pctDenomByLabel[lbl]; colTotal > 0 {
						pct = (val / colTotal) * 100
					}
				} else {
					if rowTotals[i] > 0 {
						pct = (val / rowTotals[i]) * 100
					}
				}
				wide[i] = append(wide[i], orderedField{Key: lbl + " " + percentSuffix, Value: roundPct(pct)})
			}
		}
	}

	// ── Pass 3: append total-share column (row total / grand total) ──
	if withPercent {
		// Compute grand-total denominator: for global scope, sum the unfiltered
		// per-label totals; for filtered scope, use the filtered gtTotal.
		var denomTotal float64
		if percentScope == "global" {
			for _, v := range pctDenomByLabel {
				denomTotal += v
			}
		}
		if denomTotal <= 0 {
			denomTotal = gtTotal
		}
		totalShareLabel := "总订单占比分布"
		for i := range wide {
			var pct float64
			if denomTotal > 0 {
				pct = (rowTotals[i] / denomTotal) * 100
			}
			wide[i] = append(wide[i], orderedField{Key: totalShareLabel, Value: roundPct(pct)})
		}
	}

	// ── Default sort: descending by total column ──
	if len(wide) > 1 {
		idx := make([]int, len(wide))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			return rowTotals[idx[a]] > rowTotals[idx[b]]
		})
		sortedWide := make([]orderedRow, len(wide))
		// rowTotals is index-aligned with wide; reorder it in lock-step so that
		// summary-row aggregates computed below from gtByLabel/gtTotal stay
		// correct (those are sums, not per-row, so unaffected by reorder, but
		// any future per-row code reading rowTotals after sort needs the
		// alignment). Slicing below uses the new order to drop tail rows.
		sortedTotals := make([]float64, len(wide))
		for i, j := range idx {
			sortedWide[i] = wide[j]
			sortedTotals[i] = rowTotals[j]
		}
		wide = sortedWide
		rowTotals = sortedTotals
	}

	// ── LIMIT post-pivot (Intent-pivot path) ──
	// pivotLimit is the user's original spec.Limit, snapshotted in
	// lakehouseToolSmartQuery before SQL generation and stripped from the spec
	// (so the SQL returned ALL raw rows, not just top N). Now that pivot has
	// reshaped long→wide, slicing here gives top-N PRODUCTS with their full
	// pivot-value breakdown intact.
	if pivotLimit > 0 && len(wide) > pivotLimit {
		log.Printf("pivot DEBUG: applying post-pivot LIMIT %d (was %d wide rows)", pivotLimit, len(wide))
		wide = wide[:pivotLimit]
		rowTotals = rowTotals[:pivotLimit]
	}

	// ── Summary (auxiliary, NOT in table rows) ──
	// Per project convention: summary aggregates must NOT appear inline in
	// the data table. Putting them as rows conflates "real entity rows"
	// (PRC, EMEA, products...) with "totals across the table", forcing
	// every downstream consumer (LLM, frontend, gates) to filter them
	// back out. Instead, emit a separate TOON-formatted block.
	//
	// Row 1 (always): 筛选合计 — sums of the filtered result set.
	// Row 2 (global scope only): 全局合计 — unfiltered totals (the basis
	// of column-axis denominator).
	var summaryToon string
	if withPercent && len(wide) > 0 {
		var globalDenomTotal float64
		for _, v := range pctDenomByLabel {
			globalDenomTotal += v
		}
		if globalDenomTotal <= 0 {
			globalDenomTotal = gtTotal
		}

		// Column header order matches the wide-row visual layout so an LLM
		// reading both blocks can align column-by-column.
		hdr := make([]string, 0, 2+len(orderedLabels)*2+1)
		hdr = append(hdr, "scope", totalLabel)
		hdr = append(hdr, orderedLabels...)
		for _, lbl := range orderedLabels {
			hdr = append(hdr, lbl+" "+percentSuffix)
		}
		hdr = append(hdr, "总订单占比分布")

		fmtCell := func(v interface{}) string {
			switch n := v.(type) {
			case float64:
				if n == float64(int64(n)) {
					return fmt.Sprintf("%d", int64(n))
				}
				return fmt.Sprintf("%g", n)
			case string:
				return toonVal(n)
			default:
				return fmt.Sprintf("%v", n)
			}
		}

		buildRow := func(scope string, totalVal float64, pivotVals map[string]float64, pctFn func(lbl string) float64, totalSharePct float64) []string {
			cells := make([]string, 0, len(hdr))
			cells = append(cells, toonVal(scope), fmtCell(totalVal))
			for _, lbl := range orderedLabels {
				cells = append(cells, fmtCell(pivotVals[lbl]))
			}
			for _, lbl := range orderedLabels {
				cells = append(cells, fmtCell(roundPct(pctFn(lbl))))
			}
			cells = append(cells, fmtCell(roundPct(totalSharePct)))
			return cells
		}

		// Row 1: 筛选合计
		row1Pct := func(lbl string) float64 {
			if finalPercentAxis == "column" {
				if denom := pctDenomByLabel[lbl]; denom > 0 {
					return (gtByLabel[lbl] / denom) * 100
				}
				return 0
			}
			if gtTotal > 0 {
				return (gtByLabel[lbl] / gtTotal) * 100
			}
			return 0
		}
		var filteredSharePct float64
		if globalDenomTotal > 0 {
			filteredSharePct = (gtTotal / globalDenomTotal) * 100
		}
		row1 := buildRow("筛选合计", gtTotal, gtByLabel, row1Pct, filteredSharePct)

		rows := [][]string{row1}

		// Row 2: 全局合计 (only for percent_scope='global')
		if percentScope == "global" {
			row2Pct := func(lbl string) float64 {
				if pctDenomByLabel[lbl] > 0 {
					return 100.0
				}
				return 0
			}
			row2 := buildRow("全局合计", globalDenomTotal, pctDenomByLabel, row2Pct, 100.0)
			rows = append(rows, row2)
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("summary[%d|]{%s}:\n", len(rows), strings.Join(hdr, "|")))
		for _, r := range rows {
			b.WriteString("  ")
			b.WriteString(strings.Join(r, "|"))
			b.WriteString("\n")
		}
		summaryToon = b.String()
	}

	outJSON, err := json.Marshal(wide)
	if err != nil {
		return "", nil
	}

	// Diagnostic summary. percentSuffix + responseTemplate added for Synthesizer.
	return string(outJSON), M{
		"pivotOn":          pivotOn,
		"pivotValues":      cols,
		"pivotLabels":      []string(pivotLabels),
		"totalLabel":       totalLabel,
		"withPercent":      withPercent,
		"percentAxis":      finalPercentAxis,
		"percentScope":     percentScope,
		"percentSuffix":    percentSuffix,
		"intentName":       intentName,
		"responseTemplate": responseTemplate,
		"appendGrandTotal": appendGrandTotal,
		"rowsBefore":       len(rows),
		"rowsAfter":        len(wide),
		"dimCols":          dimCols,
		"metricCol":        metricCol,
		"orderedLabels":    orderedLabels,
		"summaryToon":      summaryToon,
	}
}

// orderedField is a single key/value entry in an orderedRow. Used to emit
// pivot output JSON with columns in a deterministic, business-friendly order
// rather than Go's default alphabetical map-key ordering.
type orderedField struct {
	Key   string
	Value interface{}
}

// orderedRow is a JSON object whose key order is preserved on serialization.
// Required for pivot output where mixed Chinese/English column names cause
// confusing alphabetical defaults under encoding/json (which sorts map keys).
type orderedRow []orderedField

// MarshalJSON emits the row as a JSON object, writing fields in slice order.
func (r orderedRow) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range r {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(f.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// roundPct rounds a percentage to 2 decimal places.
func roundPct(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// toonVal escapes a value for use in a TOON pipe-delimited tabular row.
// Mirrors recall.toonVal — kept local to avoid an inter-package export.
func toonVal(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "|\"\n\r:") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// toFloat normalises a JSON-decoded value to float64 for summing. Numbers
// come through as float64; Postgres numeric as string; everything else as 0.
func toFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func containsFold(ss []string, v string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// handleLakehouseAgentThreads lists/creates lakehouse-agent threads.
//
// API contract (US-004 / plan Step 1.6):
//   - GET  ?projectId=X                    → all threads (lakehouse + builder)
//   - GET  ?projectId=X&agent_type=builder → builder threads only
//   - GET  ?projectId=X&agent_type=lakehouse → lakehouse threads only
//   - POST {title, agentType?}             → create with agentType (defaults
//     to 'lakehouse' when omitted or invalid)
//
// Each row in the GET response carries an `agentType` field so the frontend
// history page can render mode badges + filter UI without a follow-up call.
func handleLakehouseAgentThreads(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			title := StrVal(body, "title")
			if title == "" {
				title = "新对话"
			}
			agentType := StrVal(body, "agentType")
			if agentType != "lakehouse" && agentType != "builder" {
				agentType = "lakehouse"
			}
			var id string
			err := db.QueryRow(`INSERT INTO ont_agent_thread (project_id, title, agent_type) VALUES ($1, $2, $3) RETURNING id`,
				pid, title, agentType).Scan(&id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id, "agentType": agentType})
			return
		}

		// GET — agent_type is optional. Empty/invalid value omits the filter
		// so callers without a preference see both modes (history page).
		agentType := r.URL.Query().Get("agent_type")
		if agentType != "lakehouse" && agentType != "builder" {
			agentType = ""
		}
		search := r.URL.Query().Get("search")

		q := `SELECT id, project_id, title, COALESCE(agent_type,'lakehouse'), created_at, updated_at
			FROM ont_agent_thread WHERE project_id = $1`
		args := []interface{}{pid}
		nextArg := 2
		if agentType != "" {
			q += fmt.Sprintf(` AND agent_type = $%d`, nextArg)
			args = append(args, agentType)
			nextArg++
		}
		if search != "" {
			q += fmt.Sprintf(` AND title ILIKE $%d`, nextArg)
			args = append(args, "%"+search+"%")
			nextArg++
		}
		q += ` ORDER BY updated_at DESC LIMIT 50`

		rows, err := db.Query(q, args...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, pid2, title, at string
			var ca, ua time.Time
			rows.Scan(&id, &pid2, &title, &at, &ca, &ua)
			list = append(list, M{
				"id": id, "projectId": pid2, "title": title,
				"agentType": at,
				"createdAt": ca.Format(time.RFC3339), "updatedAt": ua.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

// handleLakehouseAgentThreadByID gets/deletes a lakehouse agent thread
func handleLakehouseAgentThreadByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/ontology/lakehouse-agent-threads")

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_agent_thread WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		// GET: thread + steps
		var pid, title, agentType string
		var ca, ua time.Time
		var parentThreadID, branchStatus, branchReason, ambiguousKeyword string
		err := db.QueryRow(`SELECT project_id, title, COALESCE(agent_type,'lakehouse'), created_at, updated_at,
			COALESCE(thread_state->>'parent_thread_id',''),
			COALESCE(thread_state->>'status','active'),
			COALESCE(thread_state->>'branch_reason',''),
			COALESCE(thread_state->>'ambiguous_keyword','')
			FROM ont_agent_thread WHERE id = $1`, id).Scan(&pid, &title, &agentType, &ca, &ua,
			&parentThreadID, &branchStatus, &branchReason, &ambiguousKeyword)
		if err != nil {
			w.WriteHeader(404)
			JsonResp(w, M{"error": "not found"})
			return
		}

		// Load steps — include system_prompt and llm_messages so the history page
		// can show the exact JSON sent to the LLM for each round.
		stepRows, _ := db.Query(`SELECT id, step_index, role, COALESCE(content,''),
			COALESCE(thinking,''), function_call,
			COALESCE(system_prompt,''), COALESCE(llm_messages::text,''),
			duration_ms, prompt_tokens, completion_tokens, total_tokens, created_at
			FROM ont_agent_step WHERE thread_id = $1 ORDER BY step_index`, id)
		var steps []M
		if stepRows != nil {
			for stepRows.Next() {
				var sid string
				var si, dur, pt, ct, tt int
				var role, content, thinking, sysPrompt, llmMsgsRaw string
				var fcRaw sql.NullString
				var sca time.Time
				stepRows.Scan(&sid, &si, &role, &content, &thinking, &fcRaw,
					&sysPrompt, &llmMsgsRaw, &dur, &pt, &ct, &tt, &sca)
				step := M{
					"id": sid, "stepIndex": si, "role": role, "content": content,
					"thinking": thinking, "durationMs": dur,
					"promptTokens": pt, "completionTokens": ct, "totalTokens": tt,
					"createdAt": sca.Format(time.RFC3339),
				}
				if sysPrompt != "" {
					step["systemPrompt"] = sysPrompt
				}
				if llmMsgsRaw != "" && llmMsgsRaw != "null" {
					var llmMsgs interface{}
					if json.Unmarshal([]byte(llmMsgsRaw), &llmMsgs) == nil {
						step["llmMessages"] = llmMsgs
					}
				}
				if fcRaw.Valid && fcRaw.String != "" && fcRaw.String != "null" {
					var fc interface{}
					json.Unmarshal([]byte(fcRaw.String), &fc)
					step["functionCall"] = fc
				}
				steps = append(steps, step)
			}
			stepRows.Close()
		}
		if steps == nil {
			steps = []M{}
		}

		resp := M{
			"id": id, "projectId": pid, "title": title,
			"agentType": agentType,
			"createdAt": ca.Format(time.RFC3339), "updatedAt": ua.Format(time.RFC3339),
			"steps":  steps,
			"status": branchStatus,
		}
		if parentThreadID != "" {
			resp["parentThreadId"] = parentThreadID
			resp["branchReason"] = branchReason
			resp["ambiguousKeyword"] = ambiguousKeyword
		}
		JsonResp(w, resp)
	}
}
