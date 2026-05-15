package synthesizer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/lakehouse2ontology/llmclient"
)

// indicatorTerms is the universe of "share/percent/conversion" words this
// package treats as semantically loaded. ExtractIndicators() returns the
// subset present in a question; the *complement* (others in this list) is
// what mechanical gates blacklist from the draft to prevent term drift.
//
// Add new business terms here when product surfaces them. Sub-string match
// (case-insensitive) — "占比最大的" matches "占比".
var indicatorTerms = []string{
	"占比", "比例", "比率", "份额", "转化率", "百分比",
	"占总体", "占全球", "全球占比", "对全球贡献",
	"占所有", "占全部", "占整体",
	"share", "percentage", "distribution", "ratio", "proportion",
	"各占", "各自占", "占",
}

// shareOfAllPrefixes are the natural-language hints that a user is asking for
// "this slice's share of all X-values" rather than "share within this slice".
// Used by checkShareHasDenominator's second detection path.
var shareOfAllPrefixes = []string{
	"所有", "全部", "全球", "整体", "各", "全",
}

// ExtractIndicators scans question for any indicator term and returns the
// matched substrings (case-preserved from question, deduplicated, ordered by
// first-appearance). Used by mechanical gates as the authoritative "user's
// vocabulary" set.
//
// The longest-match-first traversal prevents "占比" from being consumed by a
// subsequent "占" entry; we sort the indicator list by descending length.
func ExtractIndicators(question string) []string {
	if question == "" {
		return nil
	}
	terms := append([]string(nil), indicatorTerms...)
	// Sort by length descending so multi-char terms claim characters before
	// single-char "占" can.
	for i := 1; i < len(terms); i++ {
		for j := i; j > 0 && len([]rune(terms[j])) > len([]rune(terms[j-1])); j-- {
			terms[j], terms[j-1] = terms[j-1], terms[j]
		}
	}

	// Mask byte positions of already-matched terms so shorter sub-strings
	// (e.g. "占" inside an already-matched "占比") don't double-match. Iterate
	// terms longest-first; for each match, replace those bytes in workingQ
	// with a sentinel rune so subsequent strings.Index won't find them.
	const sentinel = '\x00'
	workingQ := []rune(strings.ToLower(question))
	originalQ := []rune(question)
	seen := map[string]bool{}
	var found []string
	for _, t := range terms {
		tLower := []rune(strings.ToLower(t))
		idx := runeIndex(workingQ, tLower)
		if idx < 0 {
			continue
		}
		// Reproduce the case as it appears in the original question.
		matched := string(originalQ[idx : idx+len(tLower)])
		key := strings.ToLower(matched)
		if !seen[key] {
			seen[key] = true
			found = append(found, matched)
		}
		// Mask all occurrences of this term to handle repeats.
		for {
			i := runeIndex(workingQ, tLower)
			if i < 0 {
				break
			}
			for j := 0; j < len(tLower); j++ {
				workingQ[i+j] = sentinel
			}
		}
	}
	return found
}

// runeIndex finds the first index of needle inside haystack as rune slices.
// Returns -1 if not found.
func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
outer:
	for i := 0; i <= len(haystack)-len(needle); i++ {
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// Run is the package entry point. It composes a draft via LLM, runs the full
// mechanical gate set, and returns either Answer (Passed) or Gaps (Failed).
//
// Failure on any single gate causes Passed=false. The complete failing-gates
// list is returned so the caller can either retry, escalate, or fall through.
func Run(db *sql.DB, in Input) Result {
	draft := composeWithLLM(db, in)
	if draft == "" {
		return Result{
			Passed: false,
			Gaps: []Gap{{
				Type: "compose_failed", Detail: "synthesizer LLM call returned empty",
				Recommendation: "rewrite_prose",
			}},
			ChecksRun: 0,
		}
	}

	gaps := runMechanicalChecks(draft, in)
	res := Result{Passed: len(gaps) == 0, Gaps: gaps, ChecksRun: numChecks}
	if res.Passed {
		res.Answer = strings.TrimSpace(draft)
	}
	return res
}

// numChecks is the count of mechanical checks runMechanicalChecks performs.
// Kept as a const for ChecksRun observability — when adding/removing checks,
// update this and the slice in runMechanicalChecks together.
const numChecks = 10

func runMechanicalChecks(draft string, in Input) []Gap {
	var gaps []Gap
	// Run all checks; collect every failure (don't short-circuit) so the
	// caller sees the full picture in one round.
	for _, check := range []func(string, Input) *Gap{
		checkUserTermsPreserved,
		checkBlacklistedTermsAbsent,
		checkFiltersEchoed,
		checkResponseTplFollowed,
		checkPivotSuffixUsed,
		checkScopeStated,
		checkShareHasDenominator,
		checkDenomLabelPrecise,
		checkZeroTotalRowsNotClaimed,
		checkRowCountUsesDistinct,
	} {
		if g := check(draft, in); g != nil {
			gaps = append(gaps, *g)
		}
	}
	return gaps
}

// ── Mechanical checks ────────────────────────────────────────────────────────

// checkUserTermsPreserved: every term the user actually used must appear in
// the draft. Failure here is the most common LLM failure mode (saying "比率"
// when user said "占比") — top priority gate.
func checkUserTermsPreserved(draft string, in Input) *Gap {
	draftLower := strings.ToLower(draft)
	for _, term := range in.UserTerms {
		if !strings.Contains(draftLower, strings.ToLower(term)) {
			return &Gap{
				Type:           "term_drift",
				Detail:         fmt.Sprintf("user said %q but draft does not contain that exact term", term),
				Recommendation: "rewrite_prose",
			}
		}
	}
	return nil
}

// checkBlacklistedTermsAbsent: synonyms NOT in user's vocabulary must NOT
// appear in the draft. This catches "user asked 占比, draft adds '比率' as a
// synonym" — different denominators in domain language.
//
// Skipped if user used no indicator terms (no anchor for blacklisting).
func checkBlacklistedTermsAbsent(draft string, in Input) *Gap {
	if len(in.UserTerms) == 0 {
		return nil
	}
	used := map[string]bool{}
	for _, t := range in.UserTerms {
		used[strings.ToLower(t)] = true
	}
	// Single-char "占" is excluded from blacklist — it's a substring of every
	// 占比/占总体/etc and would false-positive on legitimate uses.
	const excludeShort = 1
	draftLower := strings.ToLower(draft)
	for _, term := range indicatorTerms {
		if len([]rune(term)) <= excludeShort {
			continue
		}
		if used[strings.ToLower(term)] {
			continue
		}
		if strings.Contains(draftLower, strings.ToLower(term)) {
			return &Gap{
				Type:           "blacklist_term",
				Detail:         fmt.Sprintf("draft uses %q but user said %v — terminology drift", term, in.UserTerms),
				Recommendation: "rewrite_prose",
			}
		}
	}
	return nil
}

// checkFiltersEchoed: each filter value must appear somewhere in the draft.
// Catches "user filtered to PRC but draft talks about all GEOs" failure mode.
//
// Numeric filter values < 4 chars are skipped (false-positive on short numbers
// matching unrelated numbers in the draft).
func checkFiltersEchoed(draft string, in Input) *Gap {
	for _, f := range in.Filters {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		if len([]rune(v)) < 2 {
			continue // single-char values too noisy
		}
		// Numeric values: only check if ≥ 4 chars (year-like or specific id).
		if isAllDigits(v) && len(v) < 4 {
			continue
		}
		if !strings.Contains(strings.ToLower(draft), strings.ToLower(v)) {
			return &Gap{
				Type:           "filter_missing",
				Detail:         fmt.Sprintf("filter %s%s%q applied to query but value not echoed in draft", f.Prop, f.Op, v),
				Recommendation: "rewrite_prose",
			}
		}
	}
	return nil
}

// checkResponseTplFollowed: when an Intent supplies a response_template, the
// draft must contain its non-placeholder literal segments. Conservative —
// extracts substrings outside {placeholder} braces and checks each one is
// present.
//
// Skipped if no template configured.
func checkResponseTplFollowed(draft string, in Input) *Gap {
	tpl := strings.TrimSpace(in.ResponseTpl)
	if tpl == "" {
		return nil
	}
	literals := extractTemplateLiterals(tpl)
	for _, lit := range literals {
		// Only check literals that are at least 2 chars and not all whitespace.
		lit = strings.TrimSpace(lit)
		if len([]rune(lit)) < 2 {
			continue
		}
		if !strings.Contains(draft, lit) {
			return &Gap{
				Type:           "template_skipped",
				Detail:         fmt.Sprintf("response_template literal %q missing from draft", lit),
				Recommendation: "rewrite_prose",
			}
		}
	}
	return nil
}

// checkPivotSuffixUsed: when pivot output columns exist (e.g. "Real Order
// 占比"), the draft must reference at least one of them by name. This nails
// the "draft made up its own column label" failure.
//
// Only enforced when both PivotColumns and IntentSuffix are non-empty (which
// implies the user is in a percent/share context).
func checkPivotSuffixUsed(draft string, in Input) *Gap {
	if len(in.PivotColumns) == 0 || in.IntentSuffix == "" {
		return nil
	}
	// At least one pivot column must be referenced.
	for _, col := range in.PivotColumns {
		if strings.Contains(draft, col) {
			return nil
		}
	}
	// Also accept the suffix word itself (e.g. "占比") as a fallback —
	// sometimes prose talks about 占比 generically without naming each column.
	if strings.Contains(draft, in.IntentSuffix) {
		return nil
	}
	return &Gap{
		Type:           "template_skipped",
		Detail:         fmt.Sprintf("pivot output columns %v not referenced and suffix %q absent", in.PivotColumns, in.IntentSuffix),
		Recommendation: "rewrite_prose",
	}
}

// checkScopeStated: when the response involves a percent metric, the draft
// must explicitly mention the denominator scope (filtered/global) so the
// user knows what 100% means. The check is lenient — accepts any of a small
// set of scope-disclosing phrases.
//
// Skipped if no percent involved (no PercentAxis or no Intent).
func checkScopeStated(draft string, in Input) *Gap {
	if in.PercentAxis == "" || in.IntentName == "" {
		return nil
	}
	// Any of these phrases counts as "scope disclosed".
	scopeMarkers := []string{
		"分母", "占",
		"全球", "全部", "整体", "总和",
		"切片内", "本切片", "范围内", "限定", "筛选",
		"filtered", "global",
	}
	for _, m := range scopeMarkers {
		if strings.Contains(draft, m) {
			return nil
		}
	}
	return &Gap{
		Type: "scope_unstated",
		Detail: fmt.Sprintf("percent axis=%s scope=%s but draft does not state the denominator scope",
			in.PercentAxis, in.PercentScope),
		Recommendation: "rewrite_prose",
	}
}

// checkShareHasDenominator catches the "share question with degenerate
// denominator" failure mode in two patterns:
//
//   - Pattern A (filter ∩ groupBy): user asked share by dim X, LLM put X in
//     both filter and groupBy → only one value of X in rows → X/X = 100%.
//     Example: filter GEO=PRC + groupBy [GEO, Order_Type] → 2 rows, both PRC.
//
//   - Pattern B (filter without dim in groupBy): user asked "X 在所有 Y 中占比"
//     where Y is the share-target dim, but LLM filtered Y=X *and* did NOT put
//     Y in groupBy → result has no Y dimension at all, denominator unrecoverable.
//     Example: question "AP 占所有 GEO 百分比", filter GEO=AP, groupBy=[ORDER_TYPE].
//
// In both cases the fix is the same: drop the filter on Y, set
// addShareColumn=true with groupBy=[Y, ...], read the target row's share.
//
// Heuristic gates (conservative — both patterns require all of):
//   - UserTerms non-empty (share-class indicator in question)
//   - rows.length ≤ 2 (degenerate / aggregated-to-single-slice signal)
//   - At least one filter with eq/in op
//   - Question explicitly mentions the filter's dim name OR an "all-Y" prefix
//     (所有/全部/全球/整体/各/全) immediately preceding the dim name
func checkShareHasDenominator(draft string, in Input) *Gap {
	if len(in.UserTerms) == 0 {
		return nil // no share question
	}
	if len(in.Rows) > 2 {
		return nil // enough rows for a meaningful denominator
	}
	if len(in.Filters) == 0 {
		return nil // no filter to suspect
	}
	suspectFilters := []FilterRef{}
	groupBySet := map[string]bool{}
	for _, g := range in.GroupBy {
		groupBySet[strings.ToLower(g)] = true
	}
	qLower := strings.ToLower(in.Question)
	for _, f := range in.Filters {
		op := strings.ToLower(strings.TrimSpace(f.Op))
		if op != "" && op != "=" && op != "==" && op != "in" {
			continue
		}
		propLower := strings.ToLower(f.Prop)
		if !strings.Contains(qLower, propLower) {
			continue // dim name not in question — unrelated filter (e.g. date)
		}
		// Pattern A: filter prop is also in groupBy → classic share-by-partition.
		if groupBySet[propLower] {
			suspectFilters = append(suspectFilters, f)
			continue
		}
		// Pattern B: question asks "<all-Y-prefix><Y>" but Y isn't in groupBy.
		// Without Y in groupBy, the SQL collapses to one row over the filtered
		// slice — no way to recover the across-Y denominator from the result.
		for _, prefix := range shareOfAllPrefixes {
			if strings.Contains(qLower, prefix+propLower) ||
				strings.Contains(qLower, prefix+" "+propLower) {
				suspectFilters = append(suspectFilters, f)
				break
			}
		}
	}
	if len(suspectFilters) == 0 {
		return nil
	}
	// Build a recommendation that names the offending filter explicitly. For
	// Pattern B (filter dim not in groupBy), the rerun must also ADD the dim
	// to groupBy — otherwise share would still collapse.
	var dropParts []string
	var addGroupBy []string
	for _, f := range suspectFilters {
		dropParts = append(dropParts, fmt.Sprintf("%s%s%q", f.Prop, f.Op, f.Value))
		if !groupBySet[strings.ToLower(f.Prop)] {
			addGroupBy = append(addGroupBy, f.Prop)
		}
	}
	suggestedGroupBy := append([]string(nil), in.GroupBy...)
	suggestedGroupBy = append(suggestedGroupBy, addGroupBy...)
	return &Gap{
		Type: "data_insufficient",
		Detail: fmt.Sprintf(
			"用户问题含占比/份额（%v），但 rows 只有 %d 行，分母无意义（filter %s 把数据过早收窄到 %s 一个切片，结果不含跨 %v 的对比基线）。",
			in.UserTerms, len(in.Rows), strings.Join(dropParts, "、"), suspectFilters[0].Value, addGroupBy),
		Recommendation: fmt.Sprintf(
			"rerun_smartquery：删除 filter %s，set addShareColumn=true，groupBy=%v。结果会含全部 %v 维度值，目标行 (%s) 的 share 即为答案。",
			strings.Join(dropParts, "、"), suggestedGroupBy, addGroupBy,
			suspectFilters[0].Value),
	}
}

// checkDenomLabelPrecise catches the "全球" mislabeling failure mode: when the
// query is scoped by filters (e.g. GEN=X10), the denominator is NOT actually
// "全球" — it's "X10 across all <share-target dim>". Calling it "全球" implies
// "no filter applied" and misleads the user (e.g. they wonder if X11/X12 are
// included).
//
// Skipped when:
//   - filters is empty (truly global query — "全球" is accurate)
//   - user themselves used "全球" in vocabulary (e.g. UserTerms includes
//     "全球占比" / "占全球") — preserving user terminology takes precedence
//
// When triggered, recommends rephrasing with the explicit filter scope (e.g.
// "X10 全部地区合计" / "X10 各 GEO 合计").
func checkDenomLabelPrecise(draft string, in Input) *Gap {
	if len(in.Filters) == 0 {
		return nil
	}
	for _, t := range in.UserTerms {
		if strings.Contains(t, "全球") {
			return nil
		}
	}
	if !strings.Contains(draft, "全球") {
		return nil
	}
	return &Gap{
		Type: "denom_mislabeled",
		Detail: fmt.Sprintf(
			"draft 用了「全球」一词，但查询有 scope filter %v —— 「全球」暗示无 filter，会让用户误解为包含其它未筛选数据。",
			in.Filters),
		Recommendation: "rewrite_prose：改用 \"<filter值> 全部 <share-target>\" 或 \"<filter值> 各 <维度>\"（如 \"X10 全部地区合计\" / \"X10 各 GEO 合计\"），不要用「全球」。",
	}
}

// checkZeroTotalRowsNotClaimed catches the "phantom zero-data row" failure mode:
// dense-mode SQL emits dim-cartesian-product rows where some (dim × pivot_value)
// combinations have no actual data → TotalLabel column = 0. Those rows are
// schema artifacts, not real records, and must not be listed, ranked, or counted
// in summary.
//
// Heuristic: for each row whose TotalLabel value is numerically 0, scan its
// other string fields. If any sufficiently-specific dim value (≥3 chars,
// alphabetic content) appears verbatim in the draft AND does NOT appear in
// the user's question, flag — the synth almost certainly listed a no-data row.
//
// Skipped when:
//   - TotalLabel is empty (synth didn't get a pivot output → no concept of "total row")
//   - rows is empty
//   - the dim value also appears in user Question (user explicitly asked about it)
func checkZeroTotalRowsNotClaimed(draft string, in Input) *Gap {
	if in.TotalLabel == "" || len(in.Rows) == 0 {
		return nil
	}
	// Columns to skip when scanning for "dim values" — pivot value cols, the
	// total col itself, and percent-suffixed cols are all metric-ish, not dim.
	skipCol := map[string]bool{in.TotalLabel: true}
	for _, c := range in.PivotColumns {
		skipCol[c] = true
		if in.IntentSuffix != "" {
			skipCol[c+" "+in.IntentSuffix] = true
		}
	}
	for _, row := range in.Rows {
		total, ok := numericValue(row[in.TotalLabel])
		if !ok || total != 0 {
			continue
		}
		for col, val := range row {
			if skipCol[col] {
				continue
			}
			// Heuristic: skip percent-flavored columns even when not in PivotColumns.
			if strings.Contains(col, "占比") || strings.Contains(col, "%") || strings.Contains(col, "率") {
				continue
			}
			s, isStr := val.(string)
			if !isStr || len([]rune(s)) < 3 {
				continue
			}
			// User explicitly asked about this dim value → not a phantom claim.
			if strings.Contains(in.Question, s) {
				continue
			}
			if strings.Contains(draft, s) {
				return &Gap{
					Type: "phantom_zero_row",
					Detail: fmt.Sprintf(
						"draft 提到 %q，但该行 %s=0（dim 笛卡尔积补出来的空行，无实际数据）。此行不应进入 summary。",
						s, in.TotalLabel),
					Recommendation: "rewrite_prose: 跳过 " + in.TotalLabel + "=0 的行，不要列入 Top-N、不要计入合计、不要写「X 有 0 条数据」。",
				}
			}
		}
	}
	return nil
}

// checkRowCountUsesDistinct catches the "inflated count" failure mode: when
// row_summary signals total_rows > distinct_dim_items (i.e. 0-total
// dim-cartesian-product rows are present), the draft must report the
// "共 X 款/项/个" claim using distinct_dim_items, not total_rows.
//
// Example: pivot output has 62 rows = 1 summary + 1 zero-total + 60 real
// products. row_summary.total_rows=62, distinct_dim_items=60. If draft says
// "共 62 款" without saying "60", flag — synth almost certainly used len(rows)
// or total_rows instead of the corrected count.
//
// Skipped when:
//   - RowSummary is nil (older resp path or compose error)
//   - total_rows == distinct_dim_items (no inflation risk)
//   - draft also contains the distinct count (LLM showed both)
//   - user's question already contains the inflated number verbatim (e.g. user
//     literally said "62 款", so echoing back is acceptable)
func checkRowCountUsesDistinct(draft string, in Input) *Gap {
	if in.RowSummary == nil {
		return nil
	}
	totalRows, ok1 := numericValue(in.RowSummary["total_rows"])
	distinct, ok2 := numericValue(in.RowSummary["distinct_dim_items"])
	if !ok1 || !ok2 || totalRows == distinct {
		return nil
	}
	totalStr := fmt.Sprintf("%d", int(totalRows))
	distinctStr := fmt.Sprintf("%d", int(distinct))
	if strings.Contains(in.Question, totalStr) {
		return nil // user explicitly used this number
	}
	if !strings.Contains(draft, totalStr) {
		return nil // draft didn't quote the inflated number
	}
	if strings.Contains(draft, distinctStr) {
		return nil // draft showed the corrected count somewhere too
	}
	return &Gap{
		Type: "total_rows_inflated",
		Detail: fmt.Sprintf("draft 提到 %s（=row_summary.total_rows，含 %d 行 0-total 空行），但有意义的项目数 = distinct_dim_items = %s。",
			totalStr, int(numericValueOr(in.RowSummary["zero_data_rows"], 0)), distinctStr),
		Recommendation: "rewrite_prose: 把 \"" + totalStr + " 款/项/个\" 改成 \"" + distinctStr + "\"（来自 row_summary.distinct_dim_items）。",
	}
}

// numericValueOr returns numericValue(v) if parseable, else fallback.
func numericValueOr(v interface{}, fallback float64) float64 {
	if n, ok := numericValue(v); ok {
		return n
	}
	return fallback
}

// numericValue coerces an interface{} to a float64 when the underlying type
// is numeric (or a numeric-looking string). Returns ok=false otherwise so the
// caller can distinguish "not present / not a number" from "zero".
func numericValue(v interface{}) (float64, bool) {
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
		// Lightweight parse — most pivot outputs already serialize numbers as
		// JSON numbers, but defensive against string-typed totals.
		if n == "" {
			return 0, false
		}
		var f float64
		if _, err := fmt.Sscanf(n, "%f", &f); err == nil {
			return f, true
		}
		return 0, false
	}
	return 0, false
}

// ── Helpers ──────────────────────────────────────────────────────────────────

var allDigitsRE = regexp.MustCompile(`^[0-9]+$`)

func isAllDigits(s string) bool { return allDigitsRE.MatchString(s) }

// extractTemplateLiterals splits a "{placeholder}"-style template into the
// literal text segments between braces. Empty/whitespace-only segments are
// dropped by the caller. Used by checkResponseTplFollowed.
func extractTemplateLiterals(tpl string) []string {
	var out []string
	var buf strings.Builder
	depth := 0
	for _, r := range tpl {
		switch r {
		case '{':
			if depth == 0 && buf.Len() > 0 {
				out = append(out, buf.String())
				buf.Reset()
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				buf.WriteRune(r)
			}
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// ── LLM compose ──────────────────────────────────────────────────────────────

// composeWithLLM calls the LLM via the "synthesizer" role binding (falls
// back to active chat config if unbound) with a focused prompt — only the
// structured input, no recall noise, no tool definitions.
//
// Returns "" on error so Run can surface a compose_failed gap rather than
// crashing the agent loop.
func composeWithLLM(db *sql.DB, in Input) string {
	baseURL, apiKey, modelName, _, _, _ := llmclient.GetConfigForRole(db, "synthesizer")
	if baseURL == "" || modelName == "" {
		log.Printf("synthesizer: no LLM config (role=synthesizer + chat fallback both empty), skipping")
		return ""
	}

	rowsJSON, _ := json.Marshal(in.Rows)
	if len(rowsJSON) > 8000 {
		// Truncate row payload to keep context tight; sample first N rows.
		// 8KB roughly = 2K tokens at this density.
		var truncated []map[string]interface{}
		if err := json.Unmarshal(rowsJSON, &truncated); err == nil && len(truncated) > 0 {
			cap := 30
			if len(truncated) > cap {
				truncated = truncated[:cap]
			}
			rowsJSON, _ = json.Marshal(truncated)
		}
	}
	filtersJSON, _ := json.Marshal(in.Filters)

	systemPrompt := `你是一个专门生成数据查询回复的助手。任务很窄：根据下面给定的结构化输入，写一段简洁、准确的中文回复。

# 硬约束（违反任何一条都算失败）

1. 用户用了哪些指标术语就保留哪些（占比/比率/份额/转化率/...），禁止替换为同义词
2. percent 列名（如 "Real Order 占比"）原样使用，不要改写
3. 如有 response_template 必须套用，不要自创句式
4. 数字必须取自 rows——可以是某行的原值，**也可以**是 rows 中 metric 列的 SUM/AVG（明确说"求和"或在算式里点出来），但不能用 rows 之外的数字
5. 必须显式说明分母 scope，**用词必须精确**：
   - 公式：分母 = "<share-target 维> 的合计" within "<scope filters>"
   - 例：filter=[GEN=X10], groupBy=[GEO], addShareColumn=true → 分母 = "X10 全部地区合计" / "X10 各 GEO 合计" / "X10 总订单量"
   - **禁止**用「全球」「全量」等暗示无 filter 的词，**除非** filters 真的为空（无任何 scope 限定）
   - 用户原话怎么称呼分母（如"总订单量"、"总和"）就尽量沿用，不要自行加「全球」修饰
6. 用户提到的具体 filter 值（如 PRC、X11、Real Order）必须在回复中出现
7. **指标语义必须忠实于 metric 函数名**——count ≠ sum，绝对不能混用：
   - **count(X)** = X 的**计数 / 数量**。读 metric 字段拿到 "count(PRODUCT_OFFERING_SHORT_NAME)" → 结果单位 = "**款/项/个 PRODUCT_OFFERING_SHORT_NAME**"，不是 "单订单"、不是 "件数量"。SUM(count 列) = 该分组下的不同 X 总数
   - **sum(X)** = X 的**求和 / 总量**。结果单位 = X 本身的业务单位（如 sum(Order_Quantity) → "单"/"pcs"）
   - **avg(X) / min(X) / max(X)** = 平均/最小/最大值。**绝不**报成"合计"
   - 反例（来自真实 bug）：metric=count(PRODUCT_OFFERING_SHORT_NAME), filter=Series=Legion 5, 结果 3 行 Count=1。**错**："Legion 5 共 3 单订单"。**对**："Legion 5 系列共有 3 款机型（PRODUCT_OFFERING_SHORT_NAME）"
   - 维度名要忠实，**不自创翻译**：GEN 就是 GEN（不要叫成 region / 地区），SERIES_COI 就是系列（不是 brand）。groupBy 列名 / filter prop 名是什么就报什么

# 占比/比例的合成规则（核心）

只要用户问题包含"占比/比例/份额/贡献/percentage"等词，按下面公式合成 headline：

**第一步**：确定关注子集 = rows 中匹配「用户问题里提到的具体 GEO/产品名/Order_Type 等值」的行
- 例：用户问 "NA、EMEA、AP 的 Real Order 占比" → 子集 = (GEO ∈ {N.A., EMEA, AP}) AND (Order_Type = Real Order) 的行

**第二步**：算 headline = SUM(子集.metric) / SUM(全部 rows.metric) × 100%
- metric 列 = Total_<prop> 或 Count（rows 中的数值列，去掉 % 派生列）
- 这是**复合占比**——一个数字，不是逐行占比的列表

**第三步**：headline 之外可补 1-2 行子集明细，但**不要展示子集之外的行**（用户没问就不报）

例：
- 用户问"NA EMEA AP Real Order 占比"，rows 含 10 个 (GEO, Order_Type) 组合
- ✓ 正确："NA + EMEA + AP 三个地区的 Real Order 共 3,453 单，占 X10 全部 1,059,469 单的 0.33%"
- ❌ 错误：逐行报"NA 0.00%、EMEA 0.20%、AP 0.12%"（这是分量不是合成；且没合成成一个数字）
- ❌ 错误：把 PRC/LAS 行也列出来（用户没问）

# 通用规则（任何 pivot Intent 都适用）

- **0 行规则**：rows 中 TotalLabel 列（如 "总订单数量" / "总销售额" / 总<对应业务总计>）= 0 的行 = **dim 笛卡尔积补出来的空行**，没有实际数据：
  - 不列该行的维度名（产品名/GEO/...）
  - 不计入合计、不参与排名 / Top-N
  - **不要算进"共 X 款/项/个"** —— 直接读 row_summary.note（系统已扣掉 0 行，给的就是真实有数据的项目数）
  - 不要写"X 有 0 条 ..."这类陈述（用户问"哪些有"不是"哪些没"）
  - 唯一例外：用户原话明确问"哪些没有 / 哪些为 0" → 才列 0 行
- **存在性问题（"哪些 <X> 有 / 没有 <Y>"）**：是问存在性，不是数量：
  - 问"有 <Y>"（无指定子类型）→ 只列 TotalLabel 列 > 0 的行
  - 问"有 <某具体 pivot 列对应的子类>"（如某 Order_Type / Sales_Channel 值）→ 缩到该 pivot 列 > 0 的行
  - 报"共 X 款 / 项 / 个有 ..."必须按对应过滤后的真实数量（直接读 row_summary.note）
  - **pivot 列名直接 = 业务概念名**（如 "Real Order"、"未转换的Real Order"、"Online"、"Offline"），用户问"有 <概念名>" 就映射到对应 pivot 列 > 0；不要自创术语解释概念之间的关系

# 输出要求

- 首句 = headline 数字 + 一句话口径说明
- 后续可补 2-3 条 子集明细 / 转化率分解，但**不要展示子集之外的行**
- 不要解释"我是怎么算的"、不要列 SQL、不要谈 Intent
- 纯中文，不要 Markdown 标题`

	rowSummaryJSON := []byte("null")
	if in.RowSummary != nil {
		if b, err := json.Marshal(in.RowSummary); err == nil {
			rowSummaryJSON = b
		}
	}

	userPrompt := fmt.Sprintf(`# 用户问题
%s

# 用户实际使用的指标术语（必须保留）
%v

# 查询参数
- metric: %s
- groupBy: %v
- filters: %s

# Intent 元数据
- name: %s
- 列名后缀: %s
- percent_axis: %s
- percent_scope: %s
- response_template: %s
- pivot 输出列名: %v
- 总计列名: %s

# 行摘要（表格不含合计行——这里的 rows 全是真实数据；合计单独走 summary_toon）
%s

# 数据 rows (JSON)
%s

请按硬约束生成回复。报"共 X 个/项"时**必须**直接读 row_summary.note（不是 len(rows)，也不是 total_rows）。`,
		in.Question,
		in.UserTerms,
		in.Metric,
		in.GroupBy,
		string(filtersJSON),
		in.IntentName,
		in.IntentSuffix,
		in.PercentAxis,
		in.PercentScope,
		in.ResponseTpl,
		in.PivotColumns,
		in.TotalLabel,
		string(rowSummaryJSON),
		string(rowsJSON),
	)

	body := map[string]interface{}{
		"model": modelName,
		"messages": []map[string]interface{}{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens":  800,
		"temperature": 0.1,
	}

	out, err := llmclient.DoChat(baseURL, apiKey, body)
	if err != nil {
		log.Printf("synthesizer: LLM call failed: %v", err)
		return ""
	}
	return llmclient.StripThinkTags(out)
}
