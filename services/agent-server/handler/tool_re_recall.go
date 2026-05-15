// re_recall — let the LLM re-run the recall layer with explicit hint tokens.
//
// The recall layer is normally invoked once per turn, before the LLM gets
// any tools. If the initial recall misses (because tokeniser dropped a key
// term, or because lakehouse_keyword lacks a bare-word entry, or because
// LLM tokenize timed out), the LLM has no way to "go back and try again".
// reflect_query_result detects shape mismatches; re_recall is the lever
// that turns that detection into a recovery action.
//
// Usage from the LLM (after reflect verdict=mismatch):
//
//   re_recall({"hints": ["EmployeeID", "员工"]})
//
// The hints are merged into the original question's token set and fed
// straight into recall.BuildLakehouseContext. The result is the same
// shape as the initial recall — markdown context block + candidate
// intents — so the LLM can re-do its smartquery selection with the
// expanded set.
//
// Safety: re_recall does not write any state. It is a read-only widening
// of the candidate set. The strict-mode smartquery still validates that
// any chosen intent actually exists.

package handler

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

const reRecallToolDescription = `重新跑一次 recall，把额外提示词作为 token 喂回。
当 reflect_query_result 返回 verdict=mismatch + missing_dimensions=[…]，
或你 lookup 时发现某个 OD/property 应该被 recall 但实际没出现，调用本工具补救。

参数 hints：你想强制纳入候选集的关键词（如 ["EmployeeID","员工"] 或 ["CategoryName","类别"]）。
返回更新后的 RecallResult markdown，含新候选 intent 列表。
随后用新看到的 intent 重新调用 smartquery。`

// runReRecallTool is the dispatchTool handler for re_recall. Args:
//
//	{
//	  hints:        []string,  // extra tokens to force into the candidate set
//	  userQuestion: string,    // original user question (for FormatContext header)
//	}
//
// Output: M with contextMd (markdown), candidateIntents (extracted slice),
// hintsUsed, totalTokens — enough for the LLM to re-decide its smartquery.
func runReRecallTool(ctx context.Context, db *sql.DB, projectID, userQuestion string, args map[string]interface{}) M {
	hints := stringSliceFromAny(args["hints"])
	cleaned := make([]string, 0, len(hints))
	seen := map[string]bool{}
	for _, h := range hints {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		key := strings.ToLower(h)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, h)
	}
	if len(cleaned) == 0 {
		return M{"error": "hints is required and must contain at least one non-blank token"}
	}

	// Optional: caller can pass userQuestion override; default to the
	// outer-scope userQuestion captured from the SSE turn.
	if v, ok := args["userQuestion"].(string); ok && strings.TrimSpace(v) != "" {
		userQuestion = v
	}

	// We deliberately use BuildLakehouseContext (not the cached variant) —
	// the whole point is to bypass the per-token cache state that caused
	// the original miss.
	result := recall.BuildLakehouseContext(ctx, db, projectID, cleaned, userQuestion)

	candidates := extractCandidateIntents(result)

	return M{
		"contextMd":         result.ContextMD,
		"candidateIntents":  candidates,
		"hintsUsed":         cleaned,
		"totalTokens":       len(cleaned),
		"hintsResolution":   "merged into recall token set",
	}
}

// extractCandidateIntents pulls the intent names out of RecallResult so the
// LLM can see them as a flat list (rather than parsing markdown). Shape
// matches recall.MetricIntent in types.go.
func extractCandidateIntents(r recall.RecallResult) []M {
	out := make([]M, 0, len(r.MetricIntents))
	for _, mi := range r.MetricIntents {
		out = append(out, M{
			"name":            mi.Name,
			"objectName":      mi.ObjectName,
			"canonicalMetric": mi.CanonicalMetric,
			"autoGroupBy":     mi.AutoGroupBy,
			"tier":            mi.Tier,
			"matchedTokens":   mi.MatchedTokens,
		})
	}
	return out
}
