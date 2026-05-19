package mission

import (
	"strings"
	"testing"
)

func TestParamCovers(t *testing.T) {
	p := IntentParam{Name: "city", Type: "enum_ref", Property: "city"}
	if !p.Covers(DecompItem{Name: "city", Kind: "dimension"}) {
		t.Error("name match should cover")
	}
	if !p.Covers(DecompItem{Name: "CITY", Kind: "dimension"}) {
		t.Error("match should be case-insensitive")
	}
	// property-only match: param name differs, property is what counts.
	pp := IntentParam{Name: "city_filter", Type: "enum_ref", Property: "city"}
	if !pp.Covers(DecompItem{Name: "city", Kind: "filter"}) {
		t.Error("property match should cover")
	}
	if p.Covers(DecompItem{Name: "year", Kind: "filter"}) {
		t.Error("unrelated dimension must not be covered")
	}
	if p.Covers(DecompItem{Name: "", Kind: "filter"}) {
		t.Error("empty dimension name must not be covered")
	}
}

func TestCoveringIntents(t *testing.T) {
	intents := []IntentSpec{
		{Name: "store_revenue", Params: []IntentParam{
			{Name: "city", Property: "city"},
			{Name: "period_label", Property: "period"},
		}},
		{Name: "store_count", Params: []IntentParam{
			{Name: "city", Property: "city"},
		}},
	}
	got := CoveringIntents(DecompItem{Name: "city", Kind: "dimension"}, intents)
	if len(got) != 2 {
		t.Fatalf("city should be covered by 2 intents, got %v", got)
	}
	got = CoveringIntents(DecompItem{Name: "period", Kind: "filter"}, intents)
	if len(got) != 1 || got[0] != "store_revenue" {
		t.Fatalf("period should be covered only by store_revenue, got %v", got)
	}
	if got := CoveringIntents(DecompItem{Name: "year", Kind: "filter"}, intents); len(got) != 0 {
		t.Fatalf("year is covered by nobody, got %v", got)
	}
}

func TestUncoveredDimensions(t *testing.T) {
	intents := []IntentSpec{
		{Name: "store_revenue", Params: []IntentParam{{Name: "city", Property: "city"}}},
	}
	decomp := []DecompItem{
		{ID: "d1", Kind: "metric", Name: "营收"},          // metric — never checked
		{ID: "d2", Kind: "dimension", Name: "city"},      // covered
		{ID: "d3", Kind: "filter", Name: "year"},         // NOT covered
	}
	un := UncoveredDimensions(decomp, intents)
	if len(un) != 1 || un[0].ID != "d3" {
		t.Fatalf("only d3 (year) should be uncovered, got %+v", un)
	}
}

// VerifyNoParamGap is the gate that catches a false capability-gap claim.
func TestVerifyNoParamGap(t *testing.T) {
	candidates := []IntentSpec{
		{Name: "store_revenue", Params: []IntentParam{
			{Name: "city", Property: "city"},
			{Name: "period_label", Property: "period"},
		}},
	}
	// A genuine gap: no parameter covers "year".
	if err := VerifyNoParamGap("year", candidates); err != nil {
		t.Errorf("year is a real gap, claim should hold: %v", err)
	}
	// A false claim: "period" IS covered — the gate must reject it.
	err := VerifyNoParamGap("period", candidates)
	if err == nil {
		t.Fatal("claiming period is a gap should be rejected")
	}
	if !strings.Contains(err.Error(), "period_label") {
		t.Errorf("rejection should name the contradicting parameter, got: %v", err)
	}
	// An empty dimension is not a valid claim.
	if err := VerifyNoParamGap("", candidates); err == nil {
		t.Error("empty missing dimension should be rejected")
	}
}
