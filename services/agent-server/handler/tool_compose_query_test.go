package handler

import (
	"context"
	"testing"
)

// Stage A value-domain guard — pure helper behavior (no DB).

func TestValueInDomain_NormalisesSpaceUnderscoreCase(t *testing.T) {
	domain := []string{"Ready", "Not ready"}
	// Exact, case, and space/underscore variants all match.
	for _, v := range []string{"Not ready", "not ready", "NOT_READY", "notready", "Ready", "ready"} {
		if !valueInDomain(v, domain) {
			t.Errorf("expected %q to be in domain %v", v, domain)
		}
	}
	// The fabricated value that bit us must NOT match.
	for _, v := range []string{"TBD", "tbd", "pending", "未就绪"} {
		if valueInDomain(v, domain) {
			t.Errorf("expected %q to be ABSENT from domain %v", v, domain)
		}
	}
}

func TestBareFilterPropName_StripsQualifierAndGranularity(t *testing.T) {
	cases := map[string]string{
		"PRODUCT.COST_READINESS": "COST_READINESS",
		"COST_READINESS":         "COST_READINESS",
		"EARLY_ORDER.MONTH(月)":   "MONTH",
		"  GEO  ":                "GEO",
	}
	for in, want := range cases {
		if got := bareFilterPropName(in); got != want {
			t.Errorf("bareFilterPropName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBareDimKey_EquatesQualifiedAndBare locks the dedupe-key equivalence both
// required-dim injection sites rely on: an OD-qualified ref and its bare form
// (and case/granularity variants) collapse to ONE key, while distinct props do
// not. General — placeholder names only.
func TestBareDimKey_EquatesQualifiedAndBare(t *testing.T) {
	// Same column in different surface forms → same key.
	same := [][2]string{
		{"OdA.Prop1", "Prop1"},        // qualified vs bare
		{"Prop1", "OdA.Prop1"},        // bare vs qualified (reverse)
		{"OdA.Prop1", "ODB.PROP1"},    // case-insensitive + different OD qualifier
		{"OdA.Prop1(月)", "Prop1"},    // granularity suffix stripped
		{"  Prop1  ", "prop1"},        // trim + case
	}
	for _, p := range same {
		if bareDimKey(p[0]) != bareDimKey(p[1]) {
			t.Errorf("bareDimKey(%q)=%q must equal bareDimKey(%q)=%q",
				p[0], bareDimKey(p[0]), p[1], bareDimKey(p[1]))
		}
	}
	// Genuinely different columns → different keys.
	diff := [][2]string{
		{"OdA.Prop1", "OdA.Prop2"},
		{"Prop1", "Prop2"},
		{"OdA.Prop1", "Prop10"},
	}
	for _, p := range diff {
		if bareDimKey(p[0]) == bareDimKey(p[1]) {
			t.Errorf("bareDimKey(%q) and bareDimKey(%q) must differ, both = %q",
				p[0], p[1], bareDimKey(p[0]))
		}
	}
}

// reqDimInjectionGroupBy replicates the required-dim injection dedupe loop
// (tool_compose_query.go runComposeQueryTool / handler_agent_lakehouse.go Mode A)
// so the bare-vs-qualified skip rule can be asserted without DB plumbing. It
// appends each reqDim only when no existing groupBy entry shares its bare key.
func reqDimInjectionGroupBy(groupBy, requiredDims []string) []string {
	out := append([]string{}, groupBy...)
	for _, reqDim := range requiredDims {
		nd := bareDimKey(reqDim)
		already := false
		for _, g := range out {
			if bareDimKey(g) == nd {
				already = true
				break
			}
		}
		if !already {
			out = append(out, reqDim)
		}
	}
	return out
}

func TestRequiredDimInjection_DedupesBareVsQualified(t *testing.T) {
	// groupBy already has the bare prop → a qualified required dim of the SAME
	// column must NOT be injected (the bug: bare + qualified = 0 rows).
	if got := reqDimInjectionGroupBy([]string{"Prop1"}, []string{"OdA.Prop1"}); len(got) != 1 {
		t.Errorf("qualified reqDim must be skipped when bare present: got %v", got)
	}
	// And the reverse: groupBy has the qualified form → bare required dim skipped.
	if got := reqDimInjectionGroupBy([]string{"OdA.Prop1"}, []string{"Prop1"}); len(got) != 1 {
		t.Errorf("bare reqDim must be skipped when qualified present: got %v", got)
	}
	// A genuinely new dim IS injected; a same-column variant alongside it is not.
	got := reqDimInjectionGroupBy([]string{"Prop1"}, []string{"OdA.Prop1", "Prop2"})
	if len(got) != 2 || got[1] != "Prop2" {
		t.Errorf("new dim must be injected, duplicate skipped: got %v", got)
	}
}

func TestSplitFilterValues(t *testing.T) {
	if got := splitFilterValues("a, b ,c", "in"); len(got) != 3 || got[1] != "b" {
		t.Errorf("in-split = %v, want [a b c]", got)
	}
	if got := splitFilterValues("a, b", "="); len(got) != 1 || got[0] != "a, b" {
		t.Errorf("eq must not split, got %v", got)
	}
}

// Path A pure-metric gate — pure helpers (no DB).

func TestMetricColFromExpr(t *testing.T) {
	cases := map[string]string{
		"sum(ORDER_QUANTITY)":         "ORDER_QUANTITY",
		"count(ORDER.ORDER_ID)":       "ORDER_ID",   // OD-qualified arg → bare column
		"avg( UnitPrice )":            "UnitPrice",  // whitespace tolerated
		"distinct_count(CustomerID)":  "CustomerID",
		"not a metric":                "",           // unparseable → ""
		"":                            "",
	}
	for in, want := range cases {
		if got := metricColFromExpr(in); got != want {
			t.Errorf("metricColFromExpr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormMetricColName(t *testing.T) {
	if normMetricColName(" Order_Quantity ") != "order_quantity" {
		t.Errorf("normMetricColName should lower+trim (underscores preserved)")
	}
}

func TestMetricColumnAuthorized_NilDBFailsOpen(t *testing.T) {
	// Without a DB the pure-metric gate must be inert (authorized=true) so it
	// never blocks a query on infra failure.
	ok, authed := metricColumnAuthorized(context.Background(), nil, "", "", "ORDER_QUANTITY")
	if !ok || len(authed) != 0 {
		t.Fatalf("nil DB must fail open (authorized=true, no authed cols), got ok=%v authed=%v", ok, authed)
	}
}
