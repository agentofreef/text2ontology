package smartquery

import (
	"errors"
	"testing"
)

func TestBindIntentParams_EmptySchema(t *testing.T) {
	spec := &QuerySpec{}
	if err := BindIntentParams(spec, nil, nil); err != nil {
		t.Errorf("nil schema + nil params must noop, got %v", err)
	}
	if spec.Limit != 0 || len(spec.Filters) != 0 {
		t.Errorf("expected zero spec, got %+v", spec)
	}
}

func TestBindIntentParams_IntLimit(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "n", Type: "int", Default: float64(10), Description: "Top N"},
	}
	if err := BindIntentParams(spec, map[string]interface{}{"n": float64(5)}, schema); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if spec.Limit != 5 {
		t.Errorf("expected Limit=5, got %d", spec.Limit)
	}
}

func TestBindIntentParams_IntDefault(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "n", Type: "int", Default: float64(10)},
	}
	if err := BindIntentParams(spec, nil, schema); err != nil {
		t.Fatalf("expected default to apply, got %v", err)
	}
	if spec.Limit != 10 {
		t.Errorf("expected default Limit=10, got %d", spec.Limit)
	}
}

func TestBindIntentParams_IntStringCoerce(t *testing.T) {
	// LLM sometimes sends "5" instead of 5; accept it.
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "n", Type: "int"}}
	if err := BindIntentParams(spec, map[string]interface{}{"n": "5"}, schema); err != nil {
		t.Fatalf("expected string→int coerce, got %v", err)
	}
	if spec.Limit != 5 {
		t.Errorf("expected Limit=5, got %d", spec.Limit)
	}
}

func TestBindIntentParams_IntZeroRejected(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "n", Type: "int"}}
	err := BindIntentParams(spec, map[string]interface{}{"n": float64(0)}, schema)
	if err == nil {
		t.Fatal("expected zero limit rejected (LIMIT 0 = empty result)")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "PARAM_TYPE_ERROR" {
		t.Errorf("expected PARAM_TYPE_ERROR, got %v", err)
	}
}

func TestBindIntentParams_IntNegativeRejected(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "n", Type: "int"}}
	err := BindIntentParams(spec, map[string]interface{}{"n": float64(-5)}, schema)
	if err == nil {
		t.Fatal("expected negative limit rejected")
	}
}

func TestBindIntentParams_IntNonInteger(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "n", Type: "int"}}
	err := BindIntentParams(spec, map[string]interface{}{"n": float64(5.5)}, schema)
	if err == nil {
		t.Fatal("expected non-integer rejected")
	}
}

func TestBindIntentParams_IntInvalidString(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "n", Type: "int"}}
	err := BindIntentParams(spec, map[string]interface{}{"n": "abc"}, schema)
	if err == nil {
		t.Fatal("expected non-numeric string rejected")
	}
}

func TestBindIntentParams_PropertyFilter(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "genre", Type: "property_filter", Property: "GenreName", Optional: true},
	}
	err := BindIntentParams(spec, map[string]interface{}{"genre": "Rock"}, schema)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if len(spec.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(spec.Filters))
	}
	f := spec.Filters[0]
	if f.Prop != "GenreName" || f.Op != "=" || f.Value != "Rock" {
		t.Errorf("expected GenreName=Rock filter, got %+v", f)
	}
}

func TestBindIntentParams_PropertyFilterCustomOp(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "min_qty", Type: "property_filter", Property: "Quantity", Op: ">=", Optional: true},
	}
	err := BindIntentParams(spec, map[string]interface{}{"min_qty": "100"}, schema)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if spec.Filters[0].Op != ">=" {
		t.Errorf("expected custom op >=, got %q", spec.Filters[0].Op)
	}
}

func TestBindIntentParams_PropertyFilterFuzzy(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "name", Type: "property_filter", Property: "ArtistName", FuzzyMatch: true, Optional: true},
	}
	err := BindIntentParams(spec, map[string]interface{}{"name": "Beatle"}, schema)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if !spec.Filters[0].FuzzyMatch {
		t.Errorf("expected FuzzyMatch=true, got %+v", spec.Filters[0])
	}
}

func TestBindIntentParams_PropertyFilterMissingProperty(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "genre", Type: "property_filter" /* no Property */, Optional: true},
	}
	err := BindIntentParams(spec, map[string]interface{}{"genre": "Rock"}, schema)
	if err == nil {
		t.Fatal("expected schema-invalid rejection")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "PARAM_SCHEMA_INVALID" {
		t.Errorf("expected PARAM_SCHEMA_INVALID, got %v", err)
	}
}

func TestBindIntentParams_OptionalMissing(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "genre", Type: "property_filter", Property: "GenreName", Optional: true},
	}
	if err := BindIntentParams(spec, nil, schema); err != nil {
		t.Errorf("optional missing must be ok, got %v", err)
	}
	if len(spec.Filters) != 0 {
		t.Errorf("expected no filter when optional param missing, got %v", spec.Filters)
	}
}

func TestBindIntentParams_RequiredMissing(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "genre", Type: "property_filter", Property: "GenreName" /* required */},
	}
	err := BindIntentParams(spec, nil, schema)
	if err == nil {
		t.Fatal("expected required-missing rejection")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "PARAM_REQUIRED" {
		t.Errorf("expected PARAM_REQUIRED, got %v", err)
	}
}

func TestBindIntentParams_UnknownParam(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "n", Type: "int", Optional: true}}
	err := BindIntentParams(spec, map[string]interface{}{"foo": "bar"}, schema)
	if err == nil {
		t.Fatal("expected unknown param rejected")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "PARAM_UNKNOWN" {
		t.Errorf("expected PARAM_UNKNOWN, got %v", err)
	}
}

func TestBindIntentParams_UnknownType(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "x", Type: "weird_unknown_type"}}
	err := BindIntentParams(spec, map[string]interface{}{"x": "v"}, schema)
	if err == nil {
		t.Fatal("expected unknown type rejected")
	}
}

func TestBindIntentParams_MultipleParams(t *testing.T) {
	// Sales.ByArtist real-world: n + genre + country.
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "n", Type: "int", Default: float64(10)},
		{Name: "genre", Type: "property_filter", Property: "GenreName", Optional: true},
		{Name: "country", Type: "property_filter", Property: "BillingCountry", Optional: true},
	}
	err := BindIntentParams(spec, map[string]interface{}{
		"n":     float64(5),
		"genre": "Rock",
	}, schema)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if spec.Limit != 5 {
		t.Errorf("expected Limit=5, got %d", spec.Limit)
	}
	if len(spec.Filters) != 1 || spec.Filters[0].Prop != "GenreName" {
		t.Errorf("expected 1 filter on GenreName, got %v", spec.Filters)
	}
}

func TestBindIntentParams_Idempotent(t *testing.T) {
	// Calling twice with the same args produces the same Limit (not stacked
	// filters, since filters dedup happens downstream — but we verify the
	// first-call shape so a second call doesn't change Limit semantics).
	spec := &QuerySpec{}
	schema := []IntentParameter{{Name: "n", Type: "int"}}
	args := map[string]interface{}{"n": float64(5)}
	if err := BindIntentParams(spec, args, schema); err != nil {
		t.Fatal(err)
	}
	if err := BindIntentParams(spec, args, schema); err != nil {
		t.Fatal(err)
	}
	if spec.Limit != 5 {
		t.Errorf("expected Limit=5 after second bind, got %d", spec.Limit)
	}
}
