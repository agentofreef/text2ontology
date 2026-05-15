// Package synthesizer composes the final natural-language answer for the
// Lakehouse Agent using a focused LLM call + mechanical post-checks.
//
// Goal: bypass the outer agent's prose-writing pass when the structured
// inputs (smartquery result + Intent metadata) are sufficient to produce a
// pass-rated answer. Mechanical gates (no LLM self-eval) decide pass/fail.
//
// Pipeline:
//
//	Input → composeWithLLM(focused prompt, locked terminology) → draft
//	      → runMechanicalChecks(draft, input) → []Gap
//	      → if no gaps: Result{Passed: true, Answer: draft}
//	        else:       Result{Passed: false, Gaps: gaps}
package synthesizer

// Input is the data contract for one synthesizer invocation. Every field is
// pulled from existing smartquery handler state — no new computation upstream.
type Input struct {
	Question     string                   // user question, verbatim
	UserTerms    []string                 // indicator words extracted from Question (占比/比率/份额/...)
	Metric       string                   // smartquery canonical_metric
	GroupBy      []string                 // smartquery groupBy
	Filters      []FilterRef              // smartquery filters
	IntentName   string                   // matched Metric Intent (empty if none)
	IntentSuffix string                   // pivot_percent_suffix (e.g. "占比", "全球占比", "转化率")
	PercentAxis  string                   // "row" | "column" | ""
	PercentScope string                   // "filtered" | "global" | ""
	ResponseTpl  string                   // pivot response_template (must be followed verbatim if non-empty)
	Rows         []map[string]interface{} // execution rows (post-pivot if pivot fired; never contain summary aggregates — those ride on resp.summary_toon)
	PivotColumns []string                 // ordered pivot output column names (with suffix already applied)
	TotalLabel   string                   // pivot total column label (e.g. "Total" or "总订单数量")
	// RowSummary mirrors smartquery resp's "row_summary" — total/data/summary
	// row counts plus distinct dim count, used for accurate "共 X 个产品" claims.
	// Nil when resp didn't carry one (e.g. error path).
	RowSummary map[string]interface{}
}

// FilterRef is a slim mirror of smartquery.FilterItem to keep this package free
// of cross-cutting imports. Only Prop/Op/Value are needed for echoing checks.
type FilterRef struct {
	Prop  string
	Op    string
	Value string
}

// Gap is one mechanical-gate failure. Type drives downstream routing in the
// agent loop; Detail is human-readable; Recommendation is what the outer LLM
// (or operator) should do next.
type Gap struct {
	Type           string `json:"type"`           // "term_drift" | "number_fabrication" | "filter_missing" | "template_skipped" | "scope_unstated" | "blacklist_term"
	Detail         string `json:"detail"`         // human-readable description of what failed
	Recommendation string `json:"recommendation"` // "rerun_smartquery" | "clarify_user" | "rewrite_prose" | ""
}

// Result is what the synthesizer returns. Exactly one of (Answer, Gaps) is
// meaningful: Answer when Passed=true, Gaps otherwise.
type Result struct {
	Passed bool   `json:"passed"`
	Answer string `json:"answer,omitempty"`
	Gaps   []Gap  `json:"gaps,omitempty"`
	// Diagnostic — set even on success for observability of the gate run.
	ChecksRun int `json:"checksRun"`
}
