package handler

import (
	"testing"

	. "github.com/lakehouse2ontology/httputil"
)

// TestRuleBasedVerdict_DimMismatch — the bug this fix targets:
//
//   user: "员工 Andrew Fuller 卖给了哪些客户"
//   smartquery picks Sales.ByEmployee → 9 rows of {EmployeeID, Total_NetAmount}
//
// Pre-fix: rule returned confident=true match (no breakdown phrase matched
// "哪些"; rowCount > 1 → fell through to the "no breakdown → match" branch).
//
// Post-fix: extractBreakdownDim("...哪些客户") → "客户". Result columns
// {EmployeeID, Total_NetAmount} contain no Customer synonym → mismatch with
// suggested_action=compose_query.
func TestRuleBasedVerdict_DimMismatch_AndrewFuller(t *testing.T) {
	resp := M{
		"total_rows":       9,
		"matched_intent":   "Sales.ByEmployee",
		"execution_result": `[{"EmployeeID":"1","Total_NetAmount":"49659423"},{"EmployeeID":"2","Total_NetAmount":"48314100"}]`,
		"bound_spec": M{
			"groupBy": nil,
			"filters": []interface{}{},
		},
	}
	v, confident := ruleBasedVerdict("员工 Andrew Fuller 卖给了哪些客户", 9, nil, resp)
	if !confident {
		t.Fatalf("expected confident=true (dim-mismatch rule should fire), got confident=false; verdict=%+v", v)
	}
	if v.Verdict != "mismatch" {
		t.Fatalf("expected verdict=mismatch, got %q (reasoning=%s)", v.Verdict, v.Reasoning)
	}
	if v.SuggestedAction != "compose_query" {
		t.Fatalf("expected suggested_action=compose_query, got %q", v.SuggestedAction)
	}
	foundCustomerHint := false
	for _, h := range v.MissingDimensions {
		if h == "客户" {
			foundCustomerHint = true
			break
		}
	}
	if !foundCustomerHint {
		t.Fatalf("expected '客户' in missing_dimensions, got %v", v.MissingDimensions)
	}
}

// TestRuleBasedVerdict_DimMatch — sanity check: "每个员工销售额" with groupBy
// containing EmployeeID should NOT trigger the new mismatch rule. (Existing
// behaviour preserved.)
func TestRuleBasedVerdict_DimMatch_PerEmployee(t *testing.T) {
	resp := M{
		"total_rows":       9,
		"matched_intent":   "Sales.ByEmployee",
		"execution_result": `[{"EmployeeID":"1","Total_NetAmount":"49659423"}]`,
		"bound_spec": M{
			"groupBy": []interface{}{"EmployeeID"},
		},
	}
	v, confident := ruleBasedVerdict("每个员工的销售额", 9, []string{"EmployeeID"}, resp)
	// Dim matches → rule shouldn't classify as mismatch. It may either:
	//   (a) return confident=true match (if some other path catches it), or
	//   (b) return confident=false (defer to LLM)
	// Either is acceptable; what we forbid is mismatch.
	if confident && v.Verdict == "mismatch" {
		t.Fatalf("'每个员工'+groupBy=[EmployeeID] should NOT be flagged mismatch; got %+v", v)
	}
}

// TestRuleBasedVerdict_AggregateOneRow — "总销售额是多少" with rowCount=1 stays
// in the single-row aggregate match shortcut (legacy behaviour).
func TestRuleBasedVerdict_AggregateOneRow(t *testing.T) {
	resp := M{
		"total_rows":       1,
		"matched_intent":   "Sales.Total",
		"execution_result": `[{"Total_NetAmount":"447406632"}]`,
	}
	v, confident := ruleBasedVerdict("总销售额是多少", 1, nil, resp)
	if !confident {
		t.Fatalf("aggregate single-row should be confident match; got confident=false v=%+v", v)
	}
	if v.Verdict != "match" {
		t.Fatalf("expected match, got %q", v.Verdict)
	}
}

// TestRuleBasedVerdict_MultiRowNoBreakdown_NoConfidentMatch — pre-fix the rule
// returned confident=true match for any rowCount>=1 result without breakdown
// keywords. Post-fix it should defer to LLM (confident=false) so subtle
// dim mismatches (e.g. dimension words obscured by typos / English column
// names) get human-language reasoning instead of a rubber-stamp.
func TestRuleBasedVerdict_MultiRowNoBreakdown_DefersToLLM(t *testing.T) {
	resp := M{
		"total_rows":       5,
		"matched_intent":   "Sales.ByEmployee",
		"execution_result": `[{"EmployeeID":"1","Total_NetAmount":"100"}]`,
	}
	// Question has NO breakdown phrase, NO dim hint → rule has nothing to
	// classify on; should defer to LLM (confident=false).
	_, confident := ruleBasedVerdict("show me the data", 5, []string{"EmployeeID"}, resp)
	if confident {
		t.Fatalf("multi-row with no breakdown phrase should NOT be confident match; rule should defer to LLM")
	}
}

func TestExtractBreakdownDim(t *testing.T) {
	cases := []struct {
		q    string
		want string
	}{
		{"员工 Andrew Fuller 卖给了哪些客户", "客户"},
		{"每个员工销售额", "员工"},
		{"按月份汇总", "月份"},
		{"各类产品的销量", "产品"},
		{"哪几位业务员", "业务员"},
		{"总销售额是多少", ""}, // no breakdown trigger
		{"美国销售额", ""},   // dim not after a breakdown trigger
	}
	for _, c := range cases {
		if got := extractBreakdownDim(c.q); got != c.want {
			t.Errorf("extractBreakdownDim(%q) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestDimMatchesAnyColumn(t *testing.T) {
	cases := []struct {
		dim     string
		columns []string
		want    bool
	}{
		{"客户", []string{"CustomerID", "Total"}, true},
		{"客户", []string{"CompanyName", "Total"}, true},
		{"客户", []string{"EmployeeID", "Total"}, false},
		{"员工", []string{"EmployeeID"}, true},
		{"员工", []string{"StaffName"}, true},
		{"员工", []string{"CustomerID"}, false},
		{"产品", []string{"ProductName"}, true},
		{"产品", []string{"SKU"}, true},
		{"客户", []string{"客户名"}, true}, // Chinese column name fallback
	}
	for _, c := range cases {
		if got := dimMatchesAnyColumn(c.dim, c.columns); got != c.want {
			t.Errorf("dimMatchesAnyColumn(%q,%v) = %v, want %v", c.dim, c.columns, got, c.want)
		}
	}
}
