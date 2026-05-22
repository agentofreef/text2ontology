package handler

import "testing"

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

func TestSplitFilterValues(t *testing.T) {
	if got := splitFilterValues("a, b ,c", "in"); len(got) != 3 || got[1] != "b" {
		t.Errorf("in-split = %v, want [a b c]", got)
	}
	if got := splitFilterValues("a, b", "="); len(got) != 1 || got[0] != "a, b" {
		t.Errorf("eq must not split, got %v", got)
	}
}
