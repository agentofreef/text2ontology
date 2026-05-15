// reflect_query_result — pre-synthesize self-critique step.
//
// Why this tool exists:
//   The legacy pipeline auto-invoked synthesize the moment smartquery
//   returned, with a system prompt that effectively said "summarize this
//   data". When recall surfaced the wrong intent (e.g. user asks "每个员工"
//   but only Sales.Total made it into the candidate set due to bare-keyword
//   gaps in lakehouse_keyword), the LLM happily summarised the single-row
//   total as the final answer — never noticing the shape mismatch with the
//   user's "per-employee" intent.
//
//   reflect_query_result inserts a structured self-critique LLM call between
//   smartquery and synthesize. It compares the user's question shape (per-X
//   / aggregate / ranking) against the result shape (row count, columns)
//   and emits a structured verdict. On verdict=mismatch the auto-invoke
//   drops synthesize and instead injects a follow-up that names the missing
//   dimensions + tells the main LLM to call re_recall / lookup before
//   smartquery-ing again. The retry budget bounds the loop.
//
// The tool is also registered as LLM-callable so callers can request a
// reflection explicitly (rare path; auto-invoke covers the common case).

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/lakehouse2ontology/llmclient"

	. "github.com/lakehouse2ontology/httputil"
)

// reflectToolDescription is shown to the LLM. We do NOT need it called by
// the LLM in the happy path (auto-invoke handles that), but the description
// explains the tool so an LLM that wants to second-guess its own answer
// can still reach for it.
const reflectToolDescription = `评估上一次 smartquery 的结果是否匹配用户问题的 shape。
返回 JSON：{verdict: "match"|"mismatch", reasoning, missing_dimensions[], suggested_action}。
- match: 答案 shape 与问题一致（例：用户问总销售，结果是 1 行汇总 → match）
- mismatch: 不一致（例：用户问"每个员工"但结果只有 1 行总数 → mismatch）
通常服务端会在 smartquery 后自动调一次；只有当你想再次确认时才主动调。`

// ReflectVerdict is the structured output of one reflect run. Callers cache
// this on the conversation so the SSE / agent_step record includes the
// reasoning chain.
type ReflectVerdict struct {
	Verdict           string   `json:"verdict"`            // "match" | "mismatch" | "uncertain"
	Reasoning         string   `json:"reasoning"`
	MissingDimensions []string `json:"missing_dimensions"` // hints to feed re_recall
	SuggestedAction   string   `json:"suggested_action"`   // "answer" | "re_recall" | "lookup_then_smartquery"
}

// runReflectTool is the dispatchTool handler for reflect_query_result. Args:
//
//	{
//	  userQuestion:   string,                  // verbatim user prompt
//	  smartqueryArgs: map[string]interface{},  // {intent, params} the LLM sent
//	  smartqueryResp: map[string]interface{},  // full smartquery tool result
//	}
//
// Output: M with verdict / reasoning / missing_dimensions / suggested_action,
// plus raw_llm_response for debugging.
func runReflectTool(db *sql.DB, args map[string]interface{}) M {
	userQuestion, _ := args["userQuestion"].(string)
	smartqueryArgs, _ := args["smartqueryArgs"].(map[string]interface{})
	resp, _ := args["smartqueryResp"].(M)
	if resp == nil {
		// fallback to map[string]interface{} cast
		if m, ok := args["smartqueryResp"].(map[string]interface{}); ok {
			resp = M(m)
		}
	}

	if strings.TrimSpace(userQuestion) == "" {
		return M{
			"verdict":            "uncertain",
			"reasoning":          "userQuestion 为空，无法评估",
			"missing_dimensions": []string{},
			"suggested_action":   "answer",
		}
	}
	if resp == nil {
		return M{
			"verdict":            "uncertain",
			"reasoning":          "smartqueryResp 为空，无 result 可比",
			"missing_dimensions": []string{},
			"suggested_action":   "answer",
		}
	}

	v := evaluateShape(db, userQuestion, smartqueryArgs, resp)
	return M{
		"verdict":            v.Verdict,
		"reasoning":          v.Reasoning,
		"missing_dimensions": v.MissingDimensions,
		"suggested_action":   v.SuggestedAction,
	}
}

// evaluateShape is the core logic. Tries the LLM-based reflection first;
// on any failure falls back to a deterministic rule check so the pipeline
// never blocks on a flaky LLM provider.
func evaluateShape(db *sql.DB, userQuestion string, smartqueryArgs map[string]interface{}, resp M) ReflectVerdict {
	// Pull a compact summary of the result so we don't blow the LLM's
	// context window with raw rows.
	rowCount := 0
	switch v := resp["total_rows"].(type) {
	case int:
		rowCount = v
	case float64:
		rowCount = int(v)
	}
	matchedIntent, _ := resp["matched_intent"].(string)
	groupBy := summariseGroupBy(resp)

	// Cheap rule check first — answer-by-keyword is enough for the common
	// "per-X without groupBy" disaster. We still ALSO call the LLM for
	// nuanced cases unless the rule is confident.
	if v, confident := ruleBasedVerdict(userQuestion, rowCount, groupBy, resp); confident {
		return v
	}

	llmVerdict, err := llmReflect(db, userQuestion, matchedIntent, groupBy, rowCount, resp)
	if err != nil {
		log.Printf("reflect: LLM call failed (%v); falling back to rule verdict", err)
		v, confident := ruleBasedVerdict(userQuestion, rowCount, groupBy, resp)
		if !confident {
			// Rule couldn't decide AND LLM is down — emit a non-empty
			// uncertain verdict so the pipeline can still answer instead
			// of dead-ending on an empty struct. We hint missing dims
			// from the breakdown extraction in case anything matches.
			hints := extractDimensionHints(userQuestion)
			return ReflectVerdict{
				Verdict:           "uncertain",
				Reasoning:         "LLM 不可用且 rule check 不能确定 shape；按现有结果作答",
				MissingDimensions: hints,
				SuggestedAction:   "answer",
			}
		}
		return v
	}
	return llmVerdict
}

// breakdownPhrases lists the linguistic patterns that signal "user wants per-X
// breakdown, not an aggregate". Order matters: phrases with explicit dimensions
// (e.g. "按员工") come first so we can also fish out the dimension hint.
//
// Includes Chinese 哪 X 询问句（"哪些客户"/"哪个员工"）— a major breakdown signal
// missed by the original list.
//
// Longer forms (e.g. "哪几位"/"每一位") come BEFORE their prefixes so the dim-
// trigger scan consumes the full quantifier before checking the dim word.
// Without this, "哪几位业务员" would stop at "哪几" and fail to find a dim.
var breakdownPhrases = []string{
	"哪几位", "哪几家", "哪几个",
	"哪些", "哪几", "哪位", "哪家", "哪个",
	"每一位", "每一家", "每一个",
	"每个", "每位", "每条", "每张", "每年", "每月", "每日",
	"各个", "各位", "各家", "各类", "各品类", "各国家", "各地区",
	"分组", "按组", "排名", "排序",
	"每一", "每", "各",
	"per ", "by ", "group by", "ranked", "ranking", "which ",
}

// breakdownDimTriggers is the subset of breakdown triggers that we expect to be
// followed *immediately* by a dimension word (no separator). e.g. "哪些客户" →
// trigger "哪些" + dim "客户". "按 员工" / "by employee" go through too because
// HasPrefix tolerates the leading space we trim.
//
// Same ordering rule as breakdownPhrases — longer quantifiers first.
var breakdownDimTriggers = []string{
	"哪几位", "哪几家", "哪几个",
	"哪些", "哪几", "哪位", "哪家", "哪个",
	"每一位", "每一家", "每一个",
	"每个", "每位", "每年", "每月", "每日",
	"各个", "各位", "各家", "各类", "各品类", "各国家", "各地区",
	"按",
	"每一", "每", "各",
	"per ", "by ", "which ",
}

// dimSynonyms maps a Chinese dimension noun to the substrings we expect to see
// in a SQL column name when that dimension is used as a groupBy. Lookup is
// strings.Contains-based and case-insensitive — see dimMatchesAnyColumn.
//
// Adding a new dimension = add a row here; no regex involved.
var dimSynonyms = map[string][]string{
	"员工":   {"employee", "staff", "emp"},
	"职员":   {"employee", "staff", "emp"},
	"业务员": {"employee", "staff", "emp"},
	"客户":   {"customer", "company", "client", "cust"},
	"用户":   {"user", "customer"},
	"公司":   {"company", "customer"},
	"产品":   {"product", "prod", "item", "sku"},
	"商品":   {"product", "prod", "item", "sku"},
	"品类":   {"category", "cat"},
	"类别":   {"category", "cat"},
	"分类":   {"category", "cat"},
	"国家":   {"country"},
	"地区":   {"region", "area"},
	"区域":   {"region", "area"},
	"省份":   {"province", "state"},
	"城市":   {"city"},
	"月份":   {"month", "mon"},
	"月":     {"month", "mon"},
	"年":     {"year", "yr"},
	"季度":   {"quarter", "qtr"},
	"周":     {"week", "wk"},
	"订单":   {"order"},
}

// dimWordsForExtract is the ordered list scanned when we're looking for a dim
// word right after a breakdown trigger. Longer words come first so "月份" wins
// over "月" when both could match.
var dimWordsForExtract = []string{
	"月份", "季度",
	"员工", "职员", "业务员",
	"客户", "用户", "公司",
	"产品", "商品",
	"品类", "类别", "分类",
	"国家", "地区", "区域", "省份", "城市",
	"订单",
	"月", "年", "周",
}

// rankingPhrases — when these appear AND result has 1 row, definitely mismatch.
var rankingPhrases = []string{
	"排名", "排序", "排行", "top ", "前几", "最高", "最低", "最多", "最少",
}

// ruleBasedVerdict does a deterministic shape check based on linguistic
// patterns + result row count. Returns (verdict, confident=true) when the
// rule is unambiguous. Otherwise returns a placeholder + false so the caller
// asks the LLM.
//
// Confident MISMATCH:
//   - user question contains any breakdown phrase AND result rowCount ≤ 1
//   - user question contains ranking phrase AND result rowCount ≤ 1
//
// Confident MATCH:
//   - user question contains aggregate-only phrasing AND result rowCount ≤ 1
func ruleBasedVerdict(userQuestion string, rowCount int, groupBy []string, resp M) (ReflectVerdict, bool) {
	q := strings.ToLower(userQuestion)

	hasBreakdown := false
	for _, p := range breakdownPhrases {
		if strings.Contains(q, strings.ToLower(p)) {
			hasBreakdown = true
			break
		}
	}
	hasRanking := false
	for _, p := range rankingPhrases {
		if strings.Contains(q, strings.ToLower(p)) {
			hasRanking = true
			break
		}
	}

	if (hasBreakdown || hasRanking) && rowCount <= 1 {
		// User asked for per-X / ranking but smartquery returned a single
		// row — clearly the wrong intent fired (almost certainly the
		// fallback Sales.Total pattern).
		reason := fmt.Sprintf("用户问题含 'per/each/排名' 类词汇 (%s) 但结果仅 %d 行", joinDetected(q, breakdownPhrases, rankingPhrases), rowCount)
		// Try to extract dimension hints from the breakdown phrase neighbourhood.
		hints := extractDimensionHints(userQuestion)
		return ReflectVerdict{
			Verdict:           "mismatch",
			Reasoning:         reason,
			MissingDimensions: hints,
			SuggestedAction:   "re_recall",
		}, true
	}

	// Dimension mismatch: user explicitly asked for breakdown by dim X
	// (e.g. "哪些客户" / "每个员工") but the result has no column / groupBy
	// corresponding to X. Confident mismatch — points to compose_query so
	// the LLM can build {odName, groupBy:[X]} with cross-OD joins.
	//
	// This catches the case where the strict-mode intent fired with the
	// wrong dimension (e.g. asked "哪些客户" got Sales.ByEmployee → 9 rows
	// of EmployeeID). Without this branch the legacy rule would say "match"
	// just because rowCount > 1.
	if requestedDim := extractBreakdownDim(userQuestion); requestedDim != "" {
		columns := unionColumns(groupBy, extractResultColumns(resp))
		if len(columns) > 0 && !dimMatchesAnyColumn(requestedDim, columns) {
			hints := extractDimensionHints(userQuestion)
			merged := false
			for _, h := range hints {
				if h == requestedDim {
					merged = true
					break
				}
			}
			if !merged {
				hints = append([]string{requestedDim}, hints...)
			}
			return ReflectVerdict{
				Verdict: "mismatch",
				Reasoning: fmt.Sprintf(
					"用户要求按 %q 维度展开（如 %q），但结果列 %v 不含对应字段",
					requestedDim, requestedDim, columns),
				MissingDimensions: hints,
				SuggestedAction:   "compose_query",
			}, true
		}
	}

	// Filter-value mention without applied filter. Rule: user named a
	// specific qualifier (e.g. "Beverages 类别下", "美国客户", "2024 年") but
	// the bound spec applied 0 filters. This catches the cross-dimension
	// case where strict mode picked an intent matching the breakdown but
	// missed the user's WHERE clause hint.
	if filterValMention := detectFilterValueMention(userQuestion); filterValMention != "" {
		if specFilters, _ := resolveSpecFilters(resp); len(specFilters) == 0 {
			hints := extractDimensionHints(userQuestion)
			if filterValMention != "" {
				hints = append(hints, filterValMention)
			}
			return ReflectVerdict{
				Verdict:           "mismatch",
				Reasoning:         fmt.Sprintf("用户提到了特定过滤值 %q（如「X 类别下」/「X 客户」/「X 国家」），但本次查询没有应用任何 filter", filterValMention),
				MissingDimensions: hints,
				SuggestedAction:   "compose_query",
			}, true
		}
	}

	// Aggregate query that legitimately returns 1 row → confident match.
	// We only short-circuit on rowCount == 1 because that's the unambiguous
	// "aggregate-shape" signal. For multi-row results without breakdown
	// phrases we defer to the LLM — the rule can't tell whether the rows
	// genuinely answer the question or whether we got the wrong dim.
	if !hasBreakdown && !hasRanking && rowCount == 1 {
		return ReflectVerdict{
			Verdict:           "match",
			Reasoning:         "无分组/排名意图且结果为单行聚合 → 视为匹配",
			MissingDimensions: []string{},
			SuggestedAction:   "answer",
		}, true
	}

	return ReflectVerdict{}, false
}

// extractBreakdownDim scans the question for "<breakdown_trigger><dim>" pairs
// and returns the first matching dim word. Pure string operations — no regex.
//
// Examples:
//   - "哪些客户"   → "客户"
//   - "每个员工"   → "员工"
//   - "按 月份"    → "月份"
//   - "by employee" → "" (English dim words not in dimWordsForExtract; the
//                         caller's groupBy check handles English columns directly)
//
// Returns "" when no breakdown trigger is followed by a known Chinese dim.
func extractBreakdownDim(q string) string {
	qLower := strings.ToLower(q)
	for _, trigger := range breakdownDimTriggers {
		tLow := strings.ToLower(trigger)
		offset := 0
		for offset < len(qLower) {
			rel := strings.Index(qLower[offset:], tLow)
			if rel < 0 {
				break
			}
			absEnd := offset + rel + len(tLow)
			tail := q[absEnd:]
			tail = strings.TrimLeft(tail, " 　\t")
			for _, dim := range dimWordsForExtract {
				if strings.HasPrefix(tail, dim) {
					return dim
				}
			}
			offset = absEnd
		}
	}
	return ""
}

// extractResultColumns pulls the column names from the first row of
// execution_result so the dim-match check has visibility even when
// bound_spec.groupBy is null (legacy intents emit groupBy via metric SQL
// directly without populating spec.GroupBy).
func extractResultColumns(resp M) []string {
	raw, _ := resp["execution_result"].(string)
	if raw == "" {
		return nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	cols := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		cols = append(cols, k)
	}
	return cols
}

// unionColumns merges groupBy entries and result column names, deduping by
// case-insensitive identity. Order isn't significant — callers do Contains
// checks against this slice.
func unionColumns(groupBy, resultCols []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(groupBy)+len(resultCols))
	add := func(s string) {
		key := strings.ToLower(strings.TrimSpace(s))
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	for _, c := range groupBy {
		add(c)
	}
	for _, c := range resultCols {
		add(c)
	}
	return out
}

// dimMatchesAnyColumn returns true when at least one column name contains a
// synonym of the requested dim (case-insensitive). Pure strings.Contains —
// no regex. The Chinese dim itself is also considered a synonym so columns
// like "客户名" / "员工编号" still match.
func dimMatchesAnyColumn(dim string, columns []string) bool {
	syns := append([]string{dim}, dimSynonyms[dim]...)
	for _, col := range columns {
		colLow := strings.ToLower(col)
		for _, s := range syns {
			if strings.Contains(colLow, strings.ToLower(s)) {
				return true
			}
		}
	}
	return false
}

// filterValuePatterns describes the syntactic shapes that strongly signal
// "user is naming a specific filter value the SQL must apply". These are
// the dim-noun patterns: 类别下 / 国家下 / 区域内 / etc, with the actual
// value preceding the qualifier.
var filterValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(\S+?)\s*类别(下|中|内|里)`),
	regexp.MustCompile(`(\S+?)\s*品类(下|中|内|里)`),
	regexp.MustCompile(`(\S+?)\s*国家(下|中|内|里|的)`),
	regexp.MustCompile(`(\S+?)\s*地区(下|中|内|里|的)`),
	regexp.MustCompile(`(\S+?)\s*区域(下|中|内|里|的)`),
	regexp.MustCompile(`(\S+?)\s*门店(下|中|内|里|的)`),
}

// detectFilterValueMention returns the matched value (e.g. "Beverages") if
// the user's question contains a "<value> <dim-noun>" pattern. Empty when
// no obvious filter qualifier is present.
func detectFilterValueMention(question string) string {
	for _, re := range filterValuePatterns {
		if m := re.FindStringSubmatch(question); m != nil && len(m) > 1 {
			val := strings.TrimSpace(m[1])
			if val != "" && val != "每个" && val != "各个" && val != "所有" {
				return val
			}
		}
	}
	return ""
}

// resolveSpecFilters peels the filters slice out of the tool result so the
// rule can check whether any WHERE clause actually applied. Supports three
// shapes:
//
//  1. smartquery (intent-bound): result.bound_spec.filters
//  2. compose_query: result.filters (top-level, as []smartquery.FilterItem)
//  3. legacy: result._spec_filters
//
// We use reflective length-check on shape 2 because Go-struct slices coming
// out of the same process aren't []interface{} and a naive type assertion
// would treat them as "absent".
func resolveSpecFilters(resp M) ([]interface{}, bool) {
	// Shape 1: smartquery bound_spec.filters
	bound, _ := resp["bound_spec"].(M)
	if bound == nil {
		if m, ok := resp["bound_spec"].(map[string]interface{}); ok {
			bound = M(m)
		}
	}
	if bound != nil {
		if arr, ok := bound["filters"].([]interface{}); ok && len(arr) > 0 {
			return arr, true
		}
		if arr, ok := bound["filters"].([]map[string]interface{}); ok && len(arr) > 0 {
			out := make([]interface{}, len(arr))
			for i, x := range arr {
				out[i] = x
			}
			return out, true
		}
	}
	// Shape 2: compose_query top-level filters (could be any slice type).
	if v, exists := resp["filters"]; exists && v != nil {
		if rv := reflectValueOfSlice(v); rv > 0 {
			// Non-empty filters slice — at least one filter applied. We
			// don't need to materialise it for the rule (only length matters).
			return []interface{}{struct{}{}}, true
		}
	}
	// Shape 3: legacy _spec_filters
	if arr, ok := resp["_spec_filters"].([]interface{}); ok && len(arr) > 0 {
		return arr, true
	}
	return nil, false
}

// reflectValueOfSlice returns the length of v if v is a slice, else 0. Avoids
// pulling the reflect package as a top-level import — uses a small helper
// approach: try the common concrete types we know flow through here.
func reflectValueOfSlice(v interface{}) int {
	if v == nil {
		return 0
	}
	switch s := v.(type) {
	case []interface{}:
		return len(s)
	case []map[string]interface{}:
		return len(s)
	}
	// Fall through to reflect for other slice types (e.g. []FilterItem from
	// the compose_query path).
	return reflectSliceLenViaJSON(v)
}

// reflectSliceLenViaJSON is the slow but safe fallback: marshal the value
// to JSON, count top-level array elements. Used at most once per reflect
// call so the cost is irrelevant.
func reflectSliceLenViaJSON(v interface{}) int {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || b[0] != '[' {
		return 0
	}
	var arr []interface{}
	if err := json.Unmarshal(b, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// joinDetected returns a short human-readable summary of which trigger
// phrases were detected in the question. Used for the verdict reasoning.
func joinDetected(qLower string, groups ...[]string) string {
	hits := []string{}
	seen := map[string]bool{}
	for _, g := range groups {
		for _, p := range g {
			if strings.Contains(qLower, strings.ToLower(p)) && !seen[p] {
				seen[p] = true
				hits = append(hits, p)
				if len(hits) >= 3 {
					return strings.Join(hits, "/")
				}
			}
		}
	}
	return strings.Join(hits, "/")
}

// extractDimensionHints scans for dimension words adjacent to breakdown
// phrases and returns them as hint candidates for re_recall. Best-effort —
// the LLM-driven path is more nuanced; this is the rule-based fallback.
//
// Heuristic: look for "<breakdown phrase><dim word>" patterns and capture the
// dim word. e.g. "每个员工" → "员工", "按客户" → "客户", "分品类" → "品类".
func extractDimensionHints(q string) []string {
	// Common dimension words we expect users to use in Chinese.
	candidates := []string{
		"员工", "职员", "业务员",
		"客户", "用户",
		"产品", "商品", "品类", "类别", "分类",
		"国家", "地区", "区域", "省份", "城市",
		"月", "月份", "季度", "年", "周",
		"订单",
	}
	out := []string{}
	seen := map[string]bool{}
	for _, c := range candidates {
		if strings.Contains(q, c) && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// summariseGroupBy lifts the bound spec's groupBy + intent name out of the
// smartquery result so the reflect LLM sees what dimension(s) actually fired.
func summariseGroupBy(resp M) []string {
	bound, _ := resp["bound_spec"].(M)
	if bound == nil {
		if m, ok := resp["bound_spec"].(map[string]interface{}); ok {
			bound = M(m)
		}
	}
	if bound == nil {
		return nil
	}
	gb, ok := bound["groupBy"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(gb))
	for _, x := range gb {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// llmReflect calls the configured reflect-role LLM with a structured prompt
// and parses the JSON verdict. If no reflect role is bound, falls back to
// the synthesize role; if that's also unbound returns an error and the
// caller uses the rule verdict.
func llmReflect(db *sql.DB, userQuestion, matchedIntent string, groupBy []string, rowCount int, resp M) (ReflectVerdict, error) {
	// Role-name fallback chain: reflect → synthesizer → agent.
	// The DB binding uses "synthesizer" (not "synthesize" — a typo we
	// previously had here that silently turned every multi-row reflect
	// into uncertain). "agent" is the universal lakehouse-mode chat role
	// that's always bound, so as a final fallback it guarantees we get
	// nuanced reasoning instead of falling out to the rule-only path.
	baseURL, apiKey, modelName, _, _, vendor := llmclient.GetConfigForRole(db, "reflect")
	if baseURL == "" || modelName == "" {
		baseURL, apiKey, modelName, _, _, vendor = llmclient.GetConfigForRole(db, "synthesizer")
	}
	if baseURL == "" || modelName == "" {
		baseURL, apiKey, modelName, _, _, vendor = llmclient.GetConfigForRole(db, "agent")
	}
	if baseURL == "" || modelName == "" {
		return ReflectVerdict{}, fmt.Errorf("no reflect/synthesizer/agent role configured")
	}

	// Compact a few sample rows so the LLM can see structure without dumping
	// large result sets.
	sample := compactResultSample(resp, 3)

	system := `你是查询结果 shape 评估器。判断用户问题的"期望 shape"和实际 smartquery 结果的"实际 shape"是否一致。

定义：
- 期望 shape = aggregate / breakdown / ranking
  · "总销售额是多少" → aggregate（期望 1 行）
  · "每个员工销售额" → breakdown（期望 ≥2 行 + 包含员工标识列）
  · "销售额排名前 5" → ranking（期望 ≥2 行 + 排序）
- 实际 shape：从 row count + groupBy + columns 推断

输出 JSON（不要写其它字符）：
{
  "verdict": "match" 或 "mismatch" 或 "uncertain",
  "reasoning": "一句话说清差距",
  "missing_dimensions": ["缺失的维度名，可选"],
  "suggested_action": "answer" | "re_recall" | "lookup_then_smartquery"
}

verdict 规则：
- match：实际 shape 满足问题期望 → suggested_action=answer
- mismatch：实际 shape 不满足（如问 breakdown 但结果 1 行）→ suggested_action=re_recall（推荐）或 lookup_then_smartquery
- uncertain：信息不足 → suggested_action=answer，不做猜测`

	user := fmt.Sprintf(`用户问题：%s

smartquery 命中的 intent：%s
groupBy：%v
result rows：%d
样本结果：%s

请评估并输出 JSON。`, userQuestion, matchedIntent, groupBy, rowCount, sample)

	chatBody := M{
		"model":       modelName,
		"messages":    []M{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"max_tokens":  512,
		"temperature": 0,
		"_vendor":     vendor,
	}

	deadline := time.Now().Add(30 * time.Second)
	_ = deadline // DoChat enforces its own 120s timeout; this comment marks intent

	content, err := llmclient.DoChat(baseURL, apiKey, chatBody)
	if err != nil {
		return ReflectVerdict{}, err
	}
	content = llmclient.StripThinkTags(content)
	content = llmclient.ExtractJSON(content)

	var v ReflectVerdict
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return ReflectVerdict{}, fmt.Errorf("parse reflect JSON: %w (raw: %s)", err, content)
	}
	if v.Verdict != "match" && v.Verdict != "mismatch" && v.Verdict != "uncertain" {
		return ReflectVerdict{}, fmt.Errorf("invalid verdict %q", v.Verdict)
	}
	if v.SuggestedAction == "" {
		v.SuggestedAction = "answer"
	}
	if v.MissingDimensions == nil {
		v.MissingDimensions = []string{}
	}
	return v, nil
}

// compactResultSample returns the first N rows of execution_result as a
// short JSON snippet. If parsing fails returns the raw string truncated.
func compactResultSample(resp M, n int) string {
	raw, _ := resp["execution_result"].(string)
	if raw == "" {
		return "(empty)"
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		if len(raw) > 400 {
			return raw[:400] + "…"
		}
		return raw
	}
	if len(rows) > n {
		rows = rows[:n]
	}
	out, _ := json.Marshal(rows)
	if len(out) > 600 {
		return string(out[:600]) + "…"
	}
	return string(out)
}

// autoInvokeReflect runs the reflect step right after smartquery (replacing
// the old autoInvokeSynthesize bridge). It:
//
//  1. Calls reflect to evaluate shape match
//  2. Emits an SSE function_call event (UI shows reflect as a discrete step)
//  3. Persists an ont_agent_step record (audit / debug)
//  4. If verdict=match → also runs synthesize (preserves the polished prose
//     UX of the legacy pipeline) and returns synth's followUp message
//  5. If verdict=mismatch → SKIPS synthesize and returns a follow-up that
//     tells the main LLM what's missing + which tools to call to recover
//
// Returns "" only when smartquery itself failed (existing skip rules from
// autoInvokeSynthesize are mirrored — never reflect on a failed query).
func autoInvokeReflect(
	ctx context.Context,
	db *sql.DB,
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
	// Mirror autoInvokeSynthesize's skip rules — never reflect on a failed
	// or empty smartquery.
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
	case float64:
		if v == 0 {
			return ""
		}
	}

	reflectArgs := map[string]interface{}{
		"userQuestion":   userQuestion,
		"smartqueryArgs": smartqueryArgs,
		"smartqueryResp": smartqueryResult,
	}
	reflectResult := dispatchTool("reflect_query_result", reflectArgs)

	// Emit reflect as its own SSE function_call + agent_step record.
	fc := M{"name": "reflect_query_result", "arguments": reflectArgs, "result": reflectResult}
	sendSSEFull("function_call", fc)
	saveRoundStep(sentMsgsSnapshot, "", "", fc, 0, 0, 0, time.Since(startTime).Milliseconds())

	verdict, _ := reflectResult["verdict"].(string)
	reasoning, _ := reflectResult["reasoning"].(string)
	suggestedAction, _ := reflectResult["suggested_action"].(string)
	missing := stringSliceFromAny(reflectResult["missing_dimensions"])

	switch verdict {
	case "mismatch":
		// Drop synthesize. Tell the main LLM what's wrong + how to fix it.
		var hintsHint string
		if len(missing) > 0 {
			quoted := make([]string, len(missing))
			for i, m := range missing {
				quoted[i] = fmt.Sprintf("%q", m)
			}
			hintsHint = "\n建议 re_recall hints=[" + strings.Join(quoted, ", ") + "]"
		}
		var actionHint string
		switch suggestedAction {
		case "re_recall":
			actionHint = "\n请调用 re_recall(hints=[…]) 重启 recall 把缺失维度作为 token 喂回。"
		case "lookup_then_smartquery":
			actionHint = "\n请调用 lookup 找回相关 OD/property，再以正确 intent 重新 smartquery。"
		default:
			actionHint = "\n请用 re_recall 或 lookup 探查后再 smartquery。"
		}
		return fmt.Sprintf("\n\n⚠️ reflect verdict: mismatch — %s%s%s\n请补救后再答用户。最多再尝试 2 轮。", reasoning, hintsHint, actionHint)

	case "match":
		// Happy path: chain into synthesize like before for the polished
		// prose answer. Reuse autoInvokeSynthesize's body verbatim — same
		// SSE/step semantics as legacy.
		synthMsg := autoInvokeSynthesize(dispatchTool, sendSSEFull, saveRoundStep, sentMsgsSnapshot, userQuestion, smartqueryResult, smartqueryArgs, synthFailCount, time.Now())
		return synthMsg

	default: // "uncertain"
		// Don't block — prefer answering with what we have. Reuse the synth
		// path so prose composition still happens.
		synthMsg := autoInvokeSynthesize(dispatchTool, sendSSEFull, saveRoundStep, sentMsgsSnapshot, userQuestion, smartqueryResult, smartqueryArgs, synthFailCount, time.Now())
		if synthMsg == "" {
			return "\n\nℹ️ reflect verdict: uncertain — 请直接回答用户。"
		}
		return synthMsg
	}
}

// stringSliceFromAny is provided by handler_lh_testing_export.go.
