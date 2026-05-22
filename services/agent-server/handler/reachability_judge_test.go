package handler

import (
	"testing"

	"github.com/lakehouse2ontology/services/agent-server/recall"
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

	verdict := buildVerdictFromLLMHints(hints, intents, vocab)
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

	verdict := buildVerdictFromLLMHints(hints, intents, vocab)
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
	verdict := buildVerdictFromLLMHints(hints, intents, nil)
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

	verdict := buildVerdictFromLLMHints(hints, intents, vocab)
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

	verdict := buildVerdictFromLLMHints(hints, intents, vocab)
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
	verdict := buildVerdictFromLLMHints(hints, intents, nil)
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
	verdict := buildVerdictFromLLMHints(hints, nil, nil) // no intents recalled
	if verdict.Feasible {
		t.Fatalf("expected infeasible when no Intent was recalled, got %+v", verdict)
	}
}
