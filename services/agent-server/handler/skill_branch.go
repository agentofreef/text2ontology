package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

// ─── Ambiguity Clarification Sub-Agent ────────────────────────────────────────
// v2 model: clarification happens in T_parent (natural LLM ↔ user Q&A).
// clarify_and_branch is called AFTER the user has already answered, to
// create a clean Sub-Agent execution environment scoped to the chosen Od.
// T_child's seed contains the scoped recall markdown (non-chosen candidates
// stripped, ambiguity warning removed); T_child does not re-ask the user.
// Context isolation: T_child's llmMessages[] never includes T_parent's steps.

// v2ToolClarifyAndBranch creates a Sub-Agent child thread with a scoped
// clean recall context, based on the user's already-clarified choice.
//
// Required args:
//   - ambiguousKeyword : the keyword that caused the ambiguity
//   - chosenOd         : the Od the user chose (English name)
//
// Optional args:
//   - clarificationSummary : one-line user intent summary (used in seed prompt)
func v2ToolClarifyAndBranch(db *sql.DB, projectID, parentThreadID string, args map[string]interface{}) M {
	if !IsValidUUID(parentThreadID) {
		return M{"error": "parent thread id 无效"}
	}
	ambiguousKeyword, _ := args["ambiguousKeyword"].(string)
	chosenOd, _ := args["chosenOd"].(string)
	clarificationSummary, _ := args["clarificationSummary"].(string)

	if strings.TrimSpace(ambiguousKeyword) == "" {
		return M{"error": "ambiguousKeyword 不能为空"}
	}
	if strings.TrimSpace(chosenOd) == "" {
		return M{"error": "chosenOd 不能为空：必须传入用户澄清后选中的 Od 英文名"}
	}

	// 1. Load the original user question from the parent thread (strip any
	//    enriched context block prepended by the main agent pipeline).
	var raw string
	db.QueryRow(`SELECT COALESCE(content,'') FROM ont_agent_step
		WHERE thread_id = $1 AND role = 'user'
		ORDER BY step_index LIMIT 1`, parentThreadID).Scan(&raw)
	originalQuestion := raw
	if idx := strings.LastIndex(raw, "\n\n---\n\n"); idx >= 0 {
		originalQuestion = raw[idx+len("\n\n---\n\n"):]
	}
	originalQuestion = strings.TrimSpace(originalQuestion)
	if originalQuestion == "" {
		return M{"error": "无法从父线程提取原始问题"}
	}

	// 2. Re-run the recall pipeline (tokenize + recall) for the original
	//    question. We reuse the main agent's tokenizer so the result matches
	//    what the parent saw.
	fewShots := loadAnnotationFewShots(db, projectID, originalQuestion)
	tokens := tokenizeWithAnnotationFewShots(db, projectID, originalQuestion, fewShots)
	// v2ToolClarifyAndBranch has no ctx parameter and its only wiring in the
	// main agent is commented out; Background keeps the signature surgical.
	r := recall.BuildLakehouseContext(context.Background(), db, projectID, tokens, originalQuestion)

	// 3. Validate chosenOd against the ambiguity candidates: the user's
	//    choice must correspond to one of the candidate Ods for the given
	//    ambiguous keyword. This guards against typos / hallucinations.
	var matchedAmbiguity *recall.Ambiguity
	for i := range r.Ambiguities {
		if !strings.EqualFold(r.Ambiguities[i].Keyword, ambiguousKeyword) {
			continue
		}
		matchedAmbiguity = &r.Ambiguities[i]
		break
	}
	if matchedAmbiguity != nil {
		valid := false
		for _, c := range matchedAmbiguity.Candidates {
			if strings.EqualFold(c.OdName, chosenOd) {
				valid = true
				break
			}
		}
		if !valid {
			names := make([]string, 0, len(matchedAmbiguity.Candidates))
			for _, c := range matchedAmbiguity.Candidates {
				names = append(names, c.OdName)
			}
			return M{"error": fmt.Sprintf(
				"chosenOd %q 不在歧义关键词 %q 的候选集里（候选: %s）。请重新向用户确认。",
				chosenOd, ambiguousKeyword, strings.Join(names, ", "))}
		}
	}

	// 4. Filter OdBlocks: drop only the *non-chosen* candidates for this
	//    ambiguity; keep unrelated Ods untouched.
	r.OdBlocks = filterOdBlocksByChosen(r.OdBlocks, r.Ambiguities, chosenOd)
	r.Ambiguities = nil
	cleanContextMD := recall.FormatContext(r, tokens, originalQuestion)

	// 5. Build the Sub-Agent seed system prompt.
	seedPrompt := buildSubAgentSeedPrompt(originalQuestion, chosenOd, clarificationSummary, cleanContextMD)

	// 6. Create the child thread with branch metadata in thread_state JSONB.
	//    We do NOT inject an initial clarification assistant message —
	//    the Sub-Agent starts fresh and executes directly.
	//
	//    Thread Memory Ledger: snapshot-copy the parent's ledger into the
	//    child so the Sub-Agent starts with all Ods/Intents/tokens already
	//    established in the parent conversation. After fork the ledgers
	//    are independent (parent's later activity doesn't sync back).
	childTitle := "Sub-Agent: " + truncateRunes(originalQuestion, 40)
	childState := M{
		"parent_thread_id":      parentThreadID,
		"branch_reason":         "ambiguity:" + ambiguousKeyword,
		"status":                "active",
		"seed_system_prompt":    seedPrompt,
		"ambiguous_keyword":     ambiguousKeyword,
		"chosen_od":             chosenOd,
		"clarification_summary": clarificationSummary,
	}
	var parentLedgerRaw sql.NullString
	db.QueryRow(`SELECT thread_state->'ledger' FROM ont_agent_thread WHERE id = $1`, parentThreadID).Scan(&parentLedgerRaw)
	if parentLedgerRaw.Valid && parentLedgerRaw.String != "" && parentLedgerRaw.String != "null" {
		var parentLedger json.RawMessage
		if err := json.Unmarshal([]byte(parentLedgerRaw.String), &parentLedger); err == nil {
			childState["ledger"] = parentLedger
		}
	}
	stateJSON, _ := json.Marshal(childState)

	var childID string
	err := db.QueryRow(`INSERT INTO ont_agent_thread
		(project_id, title, agent_type, thread_state)
		VALUES ($1, $2, 'knowledge', $3::jsonb) RETURNING id::text`,
		projectID, childTitle, string(stateJSON)).Scan(&childID)
	if err != nil {
		return M{"error": "创建 Sub-Agent 子线程失败: " + err.Error()}
	}

	// 7. Mark parent as suspended.
	db.Exec(`UPDATE ont_agent_thread
		SET thread_state = jsonb_set(COALESCE(thread_state,'{}'::jsonb), '{status}', '"suspended"'),
		    updated_at = now()
		WHERE id = $1`, parentThreadID)

	return M{
		"success":              true,
		"childThreadId":        childID,
		"switchTo":             childID, // frontend reads this and auto-navigates
		"ambiguousKeyword":     ambiguousKeyword,
		"chosenOd":             chosenOd,
		"clarificationSummary": clarificationSummary,
		"summary_text": fmt.Sprintf(
			"已切换到 Sub-Agent：%s 场景。子线程将用干净的 scoped 上下文直接执行查询，完成后自动切回主线程。",
			chosenOd),
	}
}

// v2ToolReturnToParent closes the child thread, appends the distilled answer
// to the parent as a new assistant step, and unsuspends the parent.
func v2ToolReturnToParent(db *sql.DB, projectID, childThreadID string, args map[string]interface{}) M {
	if !IsValidUUID(childThreadID) {
		return M{"error": "child thread id 无效"}
	}
	summary, _ := args["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return M{"error": "summary 不能为空：它将作为主线程用户看到的最终答案"}
	}
	resolution, _ := args["resolution"].(map[string]interface{})

	// 1. Read parent_thread_id from child's thread_state.
	var parentID string
	db.QueryRow(`SELECT COALESCE(thread_state->>'parent_thread_id','')
		FROM ont_agent_thread WHERE id = $1`, childThreadID).Scan(&parentID)
	if !IsValidUUID(parentID) {
		return M{"error": "当前线程不是 Sub-Agent 子线程，无法返回父线程"}
	}

	// 2. Compute next step_index in T_parent.
	var nextIdx int
	db.QueryRow(`SELECT COALESCE(MAX(step_index),0)+1 FROM ont_agent_step
		WHERE thread_id = $1`, parentID).Scan(&nextIdx)

	// 3. Insert distilled answer as a new assistant step in T_parent.
	fcPayload := M{
		"name":      "branch_return",
		"arguments": resolution,
		"result": M{
			"summary":    summary,
			"fromChild":  childThreadID,
			"resolution": resolution,
		},
	}
	fcJSON, _ := json.Marshal(fcPayload)
	_, err := db.Exec(`INSERT INTO ont_agent_step
		(thread_id, step_index, role, content, function_call)
		VALUES ($1, $2, 'assistant', $3, $4::jsonb)`,
		parentID, nextIdx, summary, string(fcJSON))
	if err != nil {
		return M{"error": "写入主线程蒸馏答案失败: " + err.Error()}
	}

	// 4. Unsuspend parent, mark child completed.
	db.Exec(`UPDATE ont_agent_thread
		SET thread_state = jsonb_set(COALESCE(thread_state,'{}'::jsonb), '{status}', '"active"'),
		    updated_at = now()
		WHERE id = $1`, parentID)
	db.Exec(`UPDATE ont_agent_thread
		SET thread_state = jsonb_set(COALESCE(thread_state,'{}'::jsonb), '{status}', '"completed"'),
		    updated_at = now()
		WHERE id = $1`, childThreadID)

	return M{
		"success":        true,
		"parentThreadId": parentID,
		"switchTo":       parentID,
		"summary_text":   "Sub-Agent 已完成，最终答案已回填到主线程。界面将自动切回主线程。",
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// filterOdBlocksByChosen removes only the *non-chosen* candidates for any
// ambiguity. Ods that never appeared in any ambiguity's candidate list are
// preserved (they are unrelated to the ambiguity and still relevant).
func filterOdBlocksByChosen(blocks []recall.OdBlock, ambiguities []recall.Ambiguity, chosenOd string) []recall.OdBlock {
	candidateNames := map[string]bool{}
	for _, a := range ambiguities {
		for _, c := range a.Candidates {
			candidateNames[strings.ToLower(c.OdName)] = true
		}
	}
	chosenLower := strings.ToLower(chosenOd)
	kept := make([]recall.OdBlock, 0, len(blocks))
	for _, blk := range blocks {
		nameLower := strings.ToLower(blk.Name)
		// Keep if: (a) not a candidate at all, OR (b) is the chosen candidate
		if !candidateNames[nameLower] || nameLower == chosenLower {
			kept = append(kept, blk)
		}
	}
	return kept
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// buildSubAgentSeedPrompt assembles the v2 Sub-Agent seed prompt.
// The seed contains: original question, user's clarified choice, scoped clean
// recall markdown, and instructions to execute directly without re-asking.
func buildSubAgentSeedPrompt(originalQuestion, chosenOd, clarificationSummary, cleanContextMD string) string {
	var b strings.Builder
	b.WriteString("你是数据分析 Sub-Agent，专注执行一次已经澄清完毕的查询。本线程上下文完全隔离——你看不到任何主线程的历史，也无需再向用户确认任何口径。\n\n")

	b.WriteString("## 原始问题\n")
	b.WriteString(strings.TrimSpace(originalQuestion) + "\n\n")

	b.WriteString("## 已澄清的业务场景\n")
	b.WriteString(fmt.Sprintf("用户在主线程已经明确选择了 **%s** 对象。\n", chosenOd))
	if s := strings.TrimSpace(clarificationSummary); s != "" {
		b.WriteString("澄清说明：" + s + "\n")
	}
	b.WriteString("\n")

	b.WriteString("## 已识别的数据上下文（已 scope 到选中场景）\n\n")
	b.WriteString(strings.TrimSpace(cleanContextMD) + "\n\n")

	b.WriteString("## 你的任务（严格顺序）\n")
	b.WriteString("1. 根据上面的干净上下文，直接调用 smartquery 执行查询\n")
	b.WriteString(fmt.Sprintf("2. 如果需要补充上下文，可以调 lookup，但 ontology_name 必须限定在 %q\n", chosenOd))
	b.WriteString("3. 得到查询结果后，必须调用 return_to_parent 工具，summary 字段填写对主线程用户的完整最终答案（包含查询结论、必要数字、简要解释）\n")
	b.WriteString("4. return_to_parent 调用后本轮结束，前端会自动切回主线程\n\n")

	b.WriteString("**禁止**：\n")
	b.WriteString("- 重新向用户询问任何澄清问题（澄清已经在主线程完成）\n")
	b.WriteString("- 调用 clarify_and_branch（本线程已经是澄清后的执行层，工具也已被禁用）\n")
	b.WriteString("- return_to_parent 的 summary 里说 \"请参考上面\" 之类引用本线程的话（summary 会独立显示给主线程用户）\n")
	return b.String()
}
