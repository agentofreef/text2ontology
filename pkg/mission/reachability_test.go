package mission

import (
	"strings"
	"testing"
)

func judgeIntents() []IntentSpec {
	return []IntentSpec{
		{Name: "store_revenue", Params: []IntentParam{
			{Name: "city", Property: "city"},
			{Name: "period_label", Property: "period"},
		}},
	}
}

// All required dimensions covered → feasible.
func TestJudgeFeasible(t *testing.T) {
	decomp := []DecompItem{
		{ID: "d1", Kind: "metric", Name: "营收"},
		{ID: "d2", Kind: "dimension", Name: "city"},
		{ID: "d3", Kind: "filter", Name: "period"},
	}
	v := Judge(decomp, judgeIntents())
	if !v.Feasible {
		t.Fatalf("expected feasible, got %+v", v)
	}
	if !strings.HasPrefix(v.Reason, "可行") {
		t.Errorf("reason should start 可行: %q", v.Reason)
	}
	// city requirement should name its covering intent.
	for _, r := range v.Requirements {
		if r.Dimension == "city" && (len(r.CoveredBy) != 1 || r.CoveredBy[0] != "store_revenue") {
			t.Errorf("city should be covered by store_revenue: %+v", r)
		}
	}
}

// A single uncovered dimension makes the WHOLE question infeasible,
// even though other dimensions are covered.
func TestJudgeWholeQuestionInfeasible(t *testing.T) {
	decomp := []DecompItem{
		{ID: "d1", Kind: "metric", Name: "营收"},
		{ID: "d2", Kind: "dimension", Name: "city"},    // covered
		{ID: "d3", Kind: "filter", Name: "employee"},   // NOT covered
	}
	v := Judge(decomp, judgeIntents())
	if v.Feasible {
		t.Fatal("one uncovered dimension must make the whole question infeasible")
	}
	if !strings.HasPrefix(v.Reason, "不可行") {
		t.Errorf("reason should start 不可行: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "employee") {
		t.Errorf("reason should name the uncovered dimension: %q", v.Reason)
	}
	// The covered subset is still reported (for "I could answer these").
	sub := v.AnswerableSubset()
	if len(sub) != 1 || sub[0] != "city" {
		t.Errorf("answerable subset should be [city], got %v", sub)
	}
}

// A metric requirement is the query target, not a filter — it never
// gates feasibility.
func TestJudgeMetricDoesNotGate(t *testing.T) {
	decomp := []DecompItem{{ID: "d1", Kind: "metric", Name: "毛利率"}}
	v := Judge(decomp, judgeIntents())
	if !v.Feasible {
		t.Errorf("a metric-only question must not be gated by coverage: %+v", v)
	}
}

// No dimensions at all → vacuously feasible.
func TestJudgeNoDimensions(t *testing.T) {
	v := Judge(nil, judgeIntents())
	if !v.Feasible {
		t.Error("empty decomposition is vacuously feasible")
	}
}

// Every dimension uncovered → infeasible, reason lists them all.
func TestJudgeAllUncovered(t *testing.T) {
	decomp := []DecompItem{
		{ID: "d1", Kind: "filter", Name: "employee"},
		{ID: "d2", Kind: "filter", Name: "supplier"},
	}
	v := Judge(decomp, judgeIntents())
	if v.Feasible {
		t.Fatal("all-uncovered must be infeasible")
	}
	if !strings.Contains(v.Reason, "employee") || !strings.Contains(v.Reason, "supplier") {
		t.Errorf("reason should list every uncovered dimension: %q", v.Reason)
	}
	if len(v.AnswerableSubset()) != 0 {
		t.Errorf("nothing should be answerable, got %v", v.AnswerableSubset())
	}
}
