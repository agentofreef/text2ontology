package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

// handleLHTestRunCaseAIJudge — POST /runs/{runId}/cases/{rcId}/ai-judge
//
// Compares the run-case's `final_answer` (what the AI showed the user) against
// the question template's `expected_answer` (the correct answer example), using
// the project's "agent" LLM. The judge sees the project ontology context (Od /
// Ok / Ol — same recall slice the runtime agent saw) so it can detect
// semantically-correct paraphrases, not just literal string equality.
//
// Outcomes:
//   - "correct"    → mark = correct, note = "[AI判定·正确] <reason>"
//   - "incorrect"  → mark = incorrect, note = "[AI判定·错误] <reason>"
//   - "unknown"    → no mark mutation, note = "[AI判定] 无法判断：<reason>"
//     (triggered when expected_answer or final_answer is empty, or the LLM
//     refuses to commit to a verdict).
//
// Response: { verdict, reason, mark, note }
func handleLHTestRunCaseAIJudge(db *sql.DB, rcID, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	CorsHeaders(w)

	if !IsValidUUID(rcID) || !IsValidUUID(runID) || !IsValidUUID(suiteID) {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "invalid id"})
		return
	}

	// Load run-case + master case + project context.
	var (
		caseID, question, finalAnswer, expectedAnswer string
		projectID                                     string
	)
	err := db.QueryRow(`SELECT rc.case_id, rc.user_question, COALESCE(rc.final_answer,''),
		COALESCE(c.expected_answer,''), s.project_id
		FROM ont_test_run_case rc
		JOIN ont_test_run r ON r.id = rc.run_id
		JOIN ont_test_suite s ON s.id = r.suite_id
		LEFT JOIN ont_test_case c ON c.id = rc.case_id
		WHERE rc.id = $1 AND rc.run_id = $2 AND r.suite_id = $3`,
		rcID, runID, suiteID).
		Scan(&caseID, &question, &finalAnswer, &expectedAnswer, &projectID)
	if err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "run-case not found"})
		return
	}

	// Fast-fail: missing final answer or missing expected answer → unknown.
	expectedAnswer = strings.TrimSpace(expectedAnswer)
	finalAnswer = strings.TrimSpace(finalAnswer)
	if expectedAnswer == "" {
		writeAIJudgeResult(db, rcID, runID, "unknown",
			"未配置正确回答示例（expected_answer），无法判断", w)
		return
	}
	if finalAnswer == "" {
		writeAIJudgeResult(db, rcID, runID, "unknown",
			"AI 没有最终回答（final_answer 为空），无法判断", w)
		return
	}

	// Build ontology context (same recall slice the runtime agent saw — Od + Ok),
	// plus the full Ol learned-facts index. The judge uses these to recognise
	// semantically-equivalent answers and to spot domain-specific errors.
	fewShots := loadAnnotationFewShots(db, projectID, question)
	tokens := tokenizeWithAnnotationFewShots(db, projectID, question, fewShots)
	recallResult := recall.BuildLakehouseContext(r.Context(), db, projectID, tokens, question)
	contextMD := recallResult.ContextMD
	olIndex := BuildOlIndex(db, projectID, "")
	if olIndex != "" && !strings.HasPrefix(olIndex, "暂无学习事实") {
		if contextMD != "" {
			contextMD += "\n\n"
		}
		contextMD += olIndex
	}

	// Resolve LLM config — same "agent" role used by the runtime test runner.
	baseURL, apiKey, modelName, _, _, vendor := llmclient.GetConfigForRole(db, "agent")
	if baseURL == "" {
		w.WriteHeader(500)
		JsonResp(w, M{"error": "无可用的 agent LLM 配置"})
		return
	}

	systemPrompt := `你是一名严格但讲道理的 AI 答案评审员（judge）。

你需要判断"AI 给用户展示的回答"是否回答了"用户问题"，并以"标准答案示例"为参考。

【判断口径】
- 数字 / 排名 / 维度组合：必须与标准答案在数值层面一致（允许两位小数四舍五入差异）。
- 单位、量纲、口径（如 "占比" vs "数量"、"全球" vs "本行内")：必须一致。
- 文本类回答：语义一致即视为正确，措辞不要求完全相同。
- 如果 AI 回答里同时包含正确与错误信息，按"主要结论是否正确"来打分；并在 reason 中指出错误片段。
- 如果 AI 回答看起来在表达"无法回答 / 无数据 / 出错"，但标准答案有具体内容 → incorrect。

【输出格式 — 必须严格遵守】
只输出一个 JSON 对象，且不要包裹 markdown 代码块：

{"verdict":"correct"|"incorrect"|"unknown","reason":"中文一句话；如果 incorrect，必须明确指出哪里错了"}

- verdict=correct：AI 回答与标准答案在结论上一致。
- verdict=incorrect：AI 回答与标准答案不一致 / 错算 / 维度错 / 口径错。
- verdict=unknown：信息不足以判断（例如标准答案本身有歧义、问题超出本体范围）。务必少用，能判就判。

不要解释 verdict 之外的内容，不要输出 JSON 之外的任何字符。`

	var ctxBlock string
	if contextMD != "" {
		ctxBlock = "\n\n## 数据本体上下文（Od / Ok / Ol，仅供参考，不参与判分）\n\n" + contextMD
	}

	userPrompt := fmt.Sprintf(`## 用户问题
%s

## 标准答案示例（expected_answer，由人工提供）
%s

## AI 给用户展示的回答（final_answer，需要被评审的对象）
%s%s

请直接输出 JSON。`,
		question, expectedAnswer, finalAnswer, ctxBlock)

	llmMessages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	content, _, err := llmclient.DoChatWithUsage(baseURL, apiKey,
		map[string]interface{}{
			"model":       modelName,
			"messages":    llmMessages,
			"max_tokens":  600,
			"temperature": 0.1,
			"_vendor":     vendor,
		})
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": "LLM 调用失败: " + err.Error()})
		return
	}

	verdict, reason := parseAIJudgeJSON(llmclient.StripThinkTags(content))
	if verdict == "" {
		writeAIJudgeResult(db, rcID, runID, "unknown",
			"AI 评审输出无法解析为 JSON：「"+truncateForNote(content, 160)+"」", w)
		return
	}

	writeAIJudgeResult(db, rcID, runID, verdict, reason, w)
}

// parseAIJudgeJSON extracts {verdict, reason} from the judge LLM's reply.
// Tolerates code-block wrappers and whitespace.
func parseAIJudgeJSON(s string) (verdict, reason string) {
	s = strings.TrimSpace(s)
	cleaned := llmclient.ExtractJSON(s)
	if cleaned == "" {
		return "", ""
	}
	var parsed struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return "", ""
	}
	v := strings.ToLower(strings.TrimSpace(parsed.Verdict))
	switch v {
	case "correct", "incorrect", "unknown":
		return v, strings.TrimSpace(parsed.Reason)
	default:
		return "", ""
	}
}

// writeAIJudgeResult persists mark + note based on verdict and returns the
// JSON response. The note is prefixed with "[AI判定·..]" so reviewers can
// distinguish AI-generated annotations from manual ones at a glance.
func writeAIJudgeResult(db *sql.DB, rcID, runID, verdict, reason string, w http.ResponseWriter) {
	if reason == "" {
		reason = "(LLM 未给出原因)"
	}
	var (
		mark string
		note string
	)
	switch verdict {
	case "correct":
		mark = "correct"
		note = "[AI判定·正确] " + reason
	case "incorrect":
		mark = "incorrect"
		note = "[AI判定·错误] " + reason
	default:
		mark = ""
		note = "[AI判定] 无法判断：" + reason
	}

	// Persist note unconditionally. For a decisive verdict (correct/incorrect)
	// write the new mark; for "unknown" explicitly clear the mark so the case
	// falls back to ⏳ pending instead of keeping a stale prior verdict that
	// would still count as 正确/错误 in the run stats.
	db.Exec(`UPDATE ont_test_run_case SET note = $1, updated_at = now() WHERE id = $2`, note, rcID)
	if verdict == "unknown" {
		db.Exec(`UPDATE ont_test_run_case SET mark = NULL, updated_at = now() WHERE id = $1`, rcID)
	} else if mark != "" {
		db.Exec(`UPDATE ont_test_run_case SET mark = $1, updated_at = now() WHERE id = $2`, mark, rcID)
	}
	updateLHRunStats(db, runID)

	JsonResp(w, M{
		"verdict":   verdict,
		"reason":    reason,
		"mark":      mark,
		"note":      note,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// truncateForNote keeps log/error fragments short when echoing them into the
// note field — the UI textarea is small and we don't want to dump a 4KB blob.
func truncateForNote(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
