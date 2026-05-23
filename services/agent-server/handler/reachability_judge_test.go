package handler

import (
	"context"
	"testing"

	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

// boolPtr returns a *bool — llmRequirementHint.Covered is *bool so we can
// distinguish "LLM didn't decide" from "explicit false".
func boolPtr(b bool) *bool { return &b }

// TestBuildVerdictFromLLMHints_HoleTwoFix is the regression test for the
// "real intent name + wrong shape" hole (see reachability_judge.go comment
// for the full bug). The LLM cites a real Intent and says covered=true, but
// none of the cited Intent's parameters declare shape_capability matching
// the required shape. With the hole-2 guard, the verdict MUST be flipped
// to infeasible — the param-shape declaration is the only invariant the
// gate is built on top of.
func TestBuildVerdictFromLLMHints_HoleTwoFix(t *testing.T) {
	// Intent "Order.Quantity" — has a Date param whose shape capability is
	// "single_month_prefix" (e.g. starts with YYYY-MM). It cannot serve a
	// year-range query in one call.
	intents := []recall.MetricIntent{{
		Name: "Order.Quantity",
		Parameters: []recall.MetricIntentParameter{{
			Name:            "month",
			Property:        "Order_Date",
			Type:            "string",
			Op:              "starts_with",
			ShapeCapability: "single_month_prefix",
		}},
	}}

	// LLM claims "year_range" filter is covered by Order.Quantity — name
	// real, but no param declares year_range capability.
	hints := []llmRequirementHint{{
		Kind:      "filter",
		Name:      "Order_Date",
		Shape:     "year_range",
		Covered:   boolPtr(true),
		CoveredBy: []string{"Order.Quantity"},
	}}

	vocab := []shapeCap{
		{Name: "single_month_prefix", Description: "YYYY-MM prefix"},
		{Name: "year_range", Description: "spans an entire year"},
	}

	verdict := buildVerdictFromLLMHints(context.Background(), nil, "", hints, intents, vocab, nil)
	if verdict.Feasible {
		t.Fatalf("hole-2 guard failed: gate let through a wrong-shape coverage claim (verdict=%+v)", verdict)
	}
	if len(verdict.Requirements) != 1 || verdict.Requirements[0].Covered {
		t.Fatalf("expected uncovered requirement, got %+v", verdict.Requirements)
	}
}

// TestBuildVerdictFromLLMHints_ShapeMatchAccepts confirms the inverse: when
// at least one cited Intent does declare the required shape, the gate
// accepts the coverage claim and stays feasible. (This is the happy path
// the hole-2 guard MUST NOT regress.)
func TestBuildVerdictFromLLMHints_ShapeMatchAccepts(t *testing.T) {
	intents := []recall.MetricIntent{{
		Name: "Order.Quantity.YearRange",
		Parameters: []recall.MetricIntentParameter{{
			Name:            "start_year",
			Property:        "Order_Date",
			Type:            "int",
			Op:              "between",
			ShapeCapability: "year_range",
		}},
	}}
	hints := []llmRequirementHint{{
		Kind:      "filter",
		Name:      "Order_Date",
		Shape:     "year_range",
		Covered:   boolPtr(true),
		CoveredBy: []string{"Order.Quantity.YearRange"},
	}}
	vocab := []shapeCap{{Name: "year_range", Description: "spans an entire year"}}

	verdict := buildVerdictFromLLMHints(context.Background(), nil, "", hints, intents, vocab, nil)
	if !verdict.Feasible {
		t.Fatalf("happy path regressed: %+v", verdict)
	}
	if len(verdict.Requirements) != 1 || !verdict.Requirements[0].Covered {
		t.Fatalf("expected covered requirement, got %+v", verdict.Requirements)
	}
}

// TestBuildVerdictFromLLMHints_EmptyVocabKeepsLegacy is the safety-net test:
// when the vocab table is empty (or the LLM emitted a shape not registered
// in vocab), the deterministic guard MUST be skipped — exactly the legacy
// behaviour. Without this, populating the vocab incrementally would silently
// flip previously-feasible questions to infeasible.
func TestBuildVerdictFromLLMHints_EmptyVocabKeepsLegacy(t *testing.T) {
	intents := []recall.MetricIntent{{
		Name: "Order.Quantity",
		Parameters: []recall.MetricIntentParameter{{
			Name:     "month",
			Property: "Order_Date",
			Type:     "string",
			Op:       "starts_with",
			// no ShapeCapability declared
		}},
	}}
	hints := []llmRequirementHint{{
		Kind:      "filter",
		Name:      "Order_Date",
		Shape:     "year_range", // would fail hole-2 check if vocab knew it
		Covered:   boolPtr(true),
		CoveredBy: []string{"Order.Quantity"},
	}}

	// vocab nil → legacy path, LLM verdict is trusted.
	verdict := buildVerdictFromLLMHints(context.Background(), nil, "", hints, intents, nil, nil)
	if !verdict.Feasible {
		t.Fatalf("legacy path regressed with empty vocab: %+v", verdict)
	}
}

// TestBuildVerdictFromLLMHints_UnknownShapeKeepsLegacy mirrors the previous
// test but with a populated vocab that simply doesn't contain the shape
// the LLM emitted. The guard MUST still skip — only registered shapes are
// subject to deterministic checking.
func TestBuildVerdictFromLLMHints_UnknownShapeKeepsLegacy(t *testing.T) {
	intents := []recall.MetricIntent{{
		Name: "Order.Quantity",
		Parameters: []recall.MetricIntentParameter{{
			Name:            "month",
			Property:        "Order_Date",
			Op:              "starts_with",
			ShapeCapability: "single_month_prefix",
		}},
	}}
	hints := []llmRequirementHint{{
		Kind:      "filter",
		Name:      "Order_Date",
		Shape:     "freeform_shape_not_in_vocab",
		Covered:   boolPtr(true),
		CoveredBy: []string{"Order.Quantity"},
	}}
	vocab := []shapeCap{{Name: "single_month_prefix", Description: "YYYY-MM prefix"}}

	verdict := buildVerdictFromLLMHints(context.Background(), nil, "", hints, intents, vocab, nil)
	if !verdict.Feasible {
		t.Fatalf("guard fired on a shape not in vocab: %+v", verdict)
	}
}

// TestAnyCitedIntentServesShape is a focused unit test on the helper,
// including the subsumption tolerance.
func TestAnyCitedIntentServesShape(t *testing.T) {
	intents := []recall.MetricIntent{
		{
			Name: "A",
			Parameters: []recall.MetricIntentParameter{
				{ShapeCapability: "single_period_prefix"},
			},
		},
		{
			Name: "B",
			Parameters: []recall.MetricIntentParameter{
				{ShapeCapability: "multi_period_range"},
			},
		},
	}
	// multi_period_range subsumes single_period_prefix (a range param can
	// also answer a single-period question). Identity-only otherwise.
	satisfies := map[string][]string{
		"multi_period_range": {"single_period_prefix"},
	}

	// Exact match.
	if !anyCitedIntentServesShape([]string{"B"}, "multi_period_range", intents, satisfies) {
		t.Fatal("expected B to serve multi_period_range exactly")
	}
	// Subsumption: B declares multi_period_range, which satisfies single_period_prefix.
	if !anyCitedIntentServesShape([]string{"B"}, "single_period_prefix", intents, satisfies) {
		t.Fatal("expected B (multi_period_range) to subsume single_period_prefix")
	}
	// Reverse must NOT hold: A declares single_period_prefix, cannot serve multi_period_range.
	if anyCitedIntentServesShape([]string{"A"}, "multi_period_range", intents, satisfies) {
		t.Fatal("single_period_prefix must NOT subsume multi_period_range (would re-open hole-2)")
	}
	if anyCitedIntentServesShape([]string{"A"}, "", intents, satisfies) {
		t.Fatal("empty required shape must return false")
	}
	if anyCitedIntentServesShape([]string{"C"}, "multi_period_range", intents, satisfies) {
		t.Fatal("cited intent C does not exist — must be false")
	}
}

// TestBuildVerdictFromLLMHints_SubsumptionAccepts verifies the gate accepts
// a coverage claim when the cited Intent declares a BROADER shape that
// subsumes the requirement's narrower shape — the false-refusal this
// robustness pass fixes.
func TestBuildVerdictFromLLMHints_SubsumptionAccepts(t *testing.T) {
	intents := []recall.MetricIntent{{
		Name: "Order.Range",
		Parameters: []recall.MetricIntentParameter{{
			Name:            "between",
			Property:        "Order_Date",
			Op:              "between",
			ShapeCapability: "multi_period_range",
		}},
	}}
	hints := []llmRequirementHint{{
		Kind:      "filter",
		Name:      "Order_Date",
		Shape:     "single_period_prefix", // narrower than what the param declares
		Covered:   boolPtr(true),
		CoveredBy: []string{"Order.Range"},
	}}
	vocab := []shapeCap{
		{Name: "single_period_prefix"},
		{Name: "multi_period_range", Satisfies: []string{"single_period_prefix"}},
	}

	verdict := buildVerdictFromLLMHints(context.Background(), nil, "", hints, intents, vocab, nil)
	if !verdict.Feasible {
		t.Fatalf("subsumption acceptance failed: a range param should cover a single-period requirement (%+v)", verdict)
	}
	if len(verdict.Requirements) != 1 || !verdict.Requirements[0].Covered {
		t.Fatalf("expected covered requirement, got %+v", verdict.Requirements)
	}
}

// TestBuildVerdictFromLLMHints_UndeclaredFilterDoesNotGate verifies the
// metric-only gate: when an Intent IS recalled but a filter/dimension has no
// declared parameter covering it, the verdict stays feasible (the ReAct loop
// resolves dimensions/filters via OD property recall + SmartQuery). Gating on
// the absence of a declared parameter was the false-negative this pass fixes —
// e.g. "X11产品early order年度趋势": the metric (early order) is recalled, but
// the X11 filter and 年度 dimension aren't intent parameters, yet the question
// is answerable downstream.
func TestBuildVerdictFromLLMHints_UndeclaredFilterDoesNotGate(t *testing.T) {
	intents := []recall.MetricIntent{{Name: "ORDER.ORDER_QUANTITY"}} // no parameters declared
	hints := []llmRequirementHint{
		{Kind: "metric", Name: "early order"},
		{Kind: "filter", Name: "X11", Shape: "等值", Covered: boolPtr(false), UncoveredReason: "参数表中无任何Intent"},
		{Kind: "dimension", Name: "年度", Shape: "分组", Covered: boolPtr(false)},
	}
	verdict := buildVerdictFromLLMHints(context.Background(), nil, "", hints, intents, nil, nil)
	if !verdict.Feasible {
		t.Fatalf("metric-only gate regressed: an undeclared filter/dimension must not gate when an Intent was recalled (%+v)", verdict)
	}
	// The uncovered filter/dimension are still surfaced for transparency.
	var sawUncovered bool
	for _, r := range verdict.Requirements {
		if (r.Kind == "filter" || r.Kind == "dimension") && !r.Covered {
			sawUncovered = true
		}
	}
	if !sawUncovered {
		t.Fatalf("expected the undeclared filter/dimension to be shown as uncovered, got %+v", verdict.Requirements)
	}
}

// TestBuildVerdictFromLLMHints_NoIntentsInfeasible verifies the metric-only
// gate fires when recall surfaced NO Intent at all — there is nothing to
// measure, so the turn is correctly refused.
func TestBuildVerdictFromLLMHints_NoIntentsInfeasible(t *testing.T) {
	hints := []llmRequirementHint{
		{Kind: "metric", Name: "early order"},
		{Kind: "filter", Name: "X11", Covered: boolPtr(false)},
	}
	verdict := buildVerdictFromLLMHints(context.Background(), nil, "", hints, nil, nil, nil) // no intents recalled
	if verdict.Feasible {
		t.Fatalf("expected infeasible when no Intent was recalled, got %+v", verdict)
	}
}

// Stage B value-domain gate — pure + fail-open behavior.

func TestLooksNumericOrDate(t *testing.T) {
	for _, s := range []string{"2024", "2024/05", "2024-2025", "2024.05.01", "12:30", " 2023 "} {
		if !looksNumericOrDate(s) {
			t.Errorf("expected %q to look numeric/date", s)
		}
	}
	for _, s := range []string{"TBD", "X11", "Beverages", "Not ready", "Q1FY25"} {
		if looksNumericOrDate(s) {
			t.Errorf("expected %q to NOT look numeric/date", s)
		}
	}
}

func TestResolveFilterValue_NilDBFailsOpen(t *testing.T) {
	// Without a DB the value gate must be inert (exists=true, no ambiguity) so
	// it never refuses.
	exists, props := resolveFilterValue(context.Background(), nil, "", "TBD")
	if !exists || len(props) != 0 {
		t.Fatalf("nil DB must fail open (exists=true, no props), got exists=%v props=%v", exists, props)
	}
	// And buildVerdictFromLLMHints must stay feasible with a value-bearing
	// filter when there's no DB to validate against.
	hints := []llmRequirementHint{
		{Kind: "metric", Name: "x"},
		{Kind: "filter", Name: "开发测试状态", Value: "TBD", Covered: boolPtr(false)},
	}
	v := buildVerdictFromLLMHints(context.Background(), nil, "", hints, []recall.MetricIntent{{Name: "m"}}, nil, nil)
	if !v.Feasible {
		t.Fatalf("nil-DB value gate must not refuse, got %+v", v)
	}
}

// Recall-grounding — the judge must trust the tokenizer + recall pipeline and
// never refuse on a token recall already resolved.

func TestSummarizeRecallResolution(t *testing.T) {
	rr := recall.RecallResult{
		MetricIntents: []recall.MetricIntent{
			{Name: "Order.Quantity", MatchedTokens: []string{"early order"}},
		},
		TokenDetails: map[string][]recall.KeywordHit{
			"YGPro7 15ASH11": {{Keyword: "Legion7 15ASH11", MappedTable: "PRODUCT", MappedField: "MTM", IsColumnRef: false}},
			"按月":             {{Keyword: "month", MappedTable: "EARLY_ORDER", MappedField: "ORDER_DATE", IsColumnRef: true}},
		},
	}
	roles, resolved := summarizeRecallResolution(rr)
	for _, want := range []string{"earlyorder", "ygpro715ash11", "按月"} {
		if !resolved[want] {
			t.Errorf("expected resolved token %q, got set %v", want, resolved)
		}
	}
	var sawMetric, sawValue, sawCol bool
	for _, r := range roles {
		switch r.Role {
		case "指标":
			sawMetric = true
		case "取值":
			sawValue = true
		case "列":
			sawCol = true
		}
	}
	if !sawMetric || !sawValue || !sawCol {
		t.Fatalf("expected metric+value+column roles, got %+v", roles)
	}
}

func TestRecallResolves_AbsorbsFragmentSplit(t *testing.T) {
	// recall matched the whole product token; the LLM re-split it into fragments.
	resolved := map[string]bool{"ygpro715ash11": true, "earlyorder": true}
	for _, frag := range []string{"YGPro7", "15ASH11", "YGPro7 15ASH11", "early"} {
		if !recallResolves(llmRequirementHint{Kind: "filter", Value: frag}, resolved) {
			t.Errorf("fragment %q of a recalled token must be treated as resolved", frag)
		}
	}
	if recallResolves(llmRequirementHint{Kind: "filter", Value: "TBD"}, resolved) {
		t.Error("TBD is not in recall — must not be treated as resolved")
	}
}

// requiredDimsFromRecall — the completeness contract's extraction step.

// TestRequiredDimsFromRecall_ColumnGroundedDimension verifies that a decompose
// "dimension" hint whose token recall matched as a COLUMN (IsColumnRef=true)
// yields the real "OD.Prop" group-by ref, while a filter-VALUE hint and an
// ungrounded dimension hint do NOT.
func TestRequiredDimsFromRecall_ColumnGroundedDimension(t *testing.T) {
	rr := recall.RecallResult{
		OdBlocks: []recall.OdBlock{{
			Name: "SALE",
			MatchedProps: []recall.PropertyMatch{
				{
					Name:        "GEO",
					DisplayName: "地区",
					Keywords: []recall.KeywordHit{
						{MatchedToken: "GEO", IsColumnRef: true},
					},
				},
				{
					// A value-only property (recall matched a data VALUE, not a
					// column) — must NOT become a group-by candidate.
					Name:        "Status",
					DisplayName: "状态",
					Keywords: []recall.KeywordHit{
						{MatchedToken: "Beverages", IsColumnRef: false},
					},
				},
			},
		}},
	}
	hints := []llmRequirementHint{
		{Kind: "dimension", Name: "GEO"},        // column-grounded → resolved
		{Kind: "filter", Name: "Beverages"},     // a filter value → not a dim
		{Kind: "dimension", Name: "渠道"},          // ungrounded dim → not resolved
		{Kind: "metric", Name: "销售额"},           // metric → never a dim
	}
	got := requiredDimsFromRecall(hints, rr)
	if len(got) != 1 || got[0] != "SALE.GEO" {
		t.Fatalf("expected [SALE.GEO], got %v", got)
	}
}

// TestRequiredDimsFromRecall_MatchesDisplayName confirms the dimension hint can
// resolve via the property DisplayName (not just Name), and dedupes.
func TestRequiredDimsFromRecall_MatchesDisplayName(t *testing.T) {
	rr := recall.RecallResult{
		OdBlocks: []recall.OdBlock{{
			Name: "ORDER",
			MatchedProps: []recall.PropertyMatch{{
				Name:        "ORDER_DATE",
				DisplayName: "年度",
				Keywords:    []recall.KeywordHit{{MatchedToken: "按年", IsColumnRef: true}},
			}},
		}},
	}
	hints := []llmRequirementHint{
		{Kind: "dimension", Name: "年度"}, // matches DisplayName
		{Kind: "dimension", Name: "按年"}, // matches the column-ref token → same ref
	}
	got := requiredDimsFromRecall(hints, rr)
	if len(got) != 1 || got[0] != "ORDER.ORDER_DATE" {
		t.Fatalf("expected deduped [ORDER.ORDER_DATE], got %v", got)
	}
}

// TestRequiredDimsFromRecall_NoColumnRefsEmpty verifies that when recall has no
// column references at all, no required dims are produced.
func TestRequiredDimsFromRecall_NoColumnRefsEmpty(t *testing.T) {
	rr := recall.RecallResult{
		OdBlocks: []recall.OdBlock{{
			Name: "SALE",
			MatchedProps: []recall.PropertyMatch{{
				Name:     "GEO",
				Keywords: []recall.KeywordHit{{MatchedToken: "GEO", IsColumnRef: false}},
			}},
		}},
	}
	hints := []llmRequirementHint{{Kind: "dimension", Name: "GEO"}}
	if got := requiredDimsFromRecall(hints, rr); len(got) != 0 {
		t.Fatalf("expected no dims (no column refs), got %v", got)
	}
}

// missingRequiredDims — the deterministic completeness backstop.

// TestMissingRequiredDims_CoveringVsMissing verifies the dim_columns coverage
// check: a required dim present in dim_columns (by bare prop, containment) is
// covered; an absent one is reported missing.
func TestMissingRequiredDims_CoveringVsMissing(t *testing.T) {
	resultM := M{"row_summary": M{"dim_columns": []string{"GEO", "Order_Date"}}}
	requiredDims := []string{"SALE.GEO", "SALE.Channel"}
	got := missingRequiredDims(resultM, requiredDims)
	if len(got) != 1 || got[0] != "SALE.Channel" {
		t.Fatalf("expected [SALE.Channel] missing, got %v", got)
	}
}

// TestMissingRequiredDims_AllCovered verifies no warning when every required
// dim is present (including the []interface{} dim_columns shape).
func TestMissingRequiredDims_AllCovered(t *testing.T) {
	resultM := M{"row_summary": M{"dim_columns": []interface{}{"GEO", "年度"}}}
	requiredDims := []string{"SALE.GEO", "ORDER.年度"}
	if got := missingRequiredDims(resultM, requiredDims); len(got) != 0 {
		t.Fatalf("expected nothing missing, got %v", got)
	}
}

// TestMissingRequiredDims_NoRowSummary verifies that when dim_columns is absent,
// all required dims are reported missing (the backstop's safe direction).
func TestMissingRequiredDims_NoRowSummary(t *testing.T) {
	got := missingRequiredDims(M{}, []string{"SALE.GEO"})
	if len(got) != 1 || got[0] != "SALE.GEO" {
		t.Fatalf("expected [SALE.GEO] missing with no row_summary, got %v", got)
	}
}

func TestBuildVerdictFromLLMHints_RecallResolvedDoesNotGate(t *testing.T) {
	// Regression: "YGPro7 15ASH11 有多少 early order" — the decompose LLM re-split
	// the product into two filters and pulled "early" out of the metric, then the
	// value-domain gate refused. With recall grounding, every fragment maps back
	// to a recall-resolved token → feasible, and the filters show as covered.
	intents := []recall.MetricIntent{{Name: "Order.Quantity"}}
	hints := []llmRequirementHint{
		{Kind: "metric", Name: "early order"},
		{Kind: "filter", Name: "product", Value: "YGPro7", Covered: boolPtr(false), UncoveredReason: "本体里找不到"},
		{Kind: "filter", Name: "spec", Value: "15ASH11", Covered: boolPtr(false)},
	}
	resolved := map[string]bool{"ygpro715ash11": true, "earlyorder": true}
	v := buildVerdictFromLLMHints(context.Background(), nil, "", hints, intents, nil, resolved)
	if !v.Feasible {
		t.Fatalf("recall-resolved fragments must not gate, got %+v", v)
	}
	for _, r := range v.Requirements {
		if r.Kind == "filter" && !r.Covered {
			t.Errorf("filter %q should be covered via recall grounding, got %+v", r.Dimension, r)
		}
	}
}
