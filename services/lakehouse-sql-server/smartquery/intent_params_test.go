package smartquery

// Symmetric to services/agent-server/smartquery/intent_params_test.go for the
// enum_ref bounded value-ref contract (spec
// .omc/specs/bounded-value-ref-contract.md §4). Lock-step duplication: the
// two BindIntentParams implementations must agree on every code path, so
// the test sets must agree too. Existing pre-spec tests live with the
// pipeline validators in intent_validate_test.go / pass_validate_test.go
// — only the new enum_ref-specific cases are added here.

import (
	"errors"
	"strings"
	"testing"
)

// T1 · enum_ref schema validation — type without a Property declaration
// must fail with PARAM_SCHEMA_INVALID. Catches "wrote type:enum_ref but
// forgot which property's keyword table to look up" at Intent author time.
func TestBindIntentParams_EnumRefMissingProperty(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "city", Type: "enum_ref" /* no Property */, Optional: true},
	}
	err := BindIntentParams(spec, map[string]interface{}{"city": "上海"}, schema)
	if err == nil {
		t.Fatal("expected schema-invalid rejection for enum_ref without property")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "PARAM_SCHEMA_INVALID" {
		t.Errorf("expected PARAM_SCHEMA_INVALID, got %v", err)
	}
}

// T2 · enum_ref happy path — value in AllowedValues matches, produces a
// FilterItem with the canonical (allowed-list) value.
func TestBindIntentParams_EnumRefHappy(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{
			Name: "city", Type: "enum_ref", Property: "city", Optional: true,
			AllowedValues: []string{"上海", "北京"},
		},
	}
	err := BindIntentParams(spec, map[string]interface{}{"city": "上海"}, schema)
	if err != nil {
		t.Fatalf("expected enum_ref happy bind, got %v", err)
	}
	if len(spec.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d (%+v)", len(spec.Filters), spec.Filters)
	}
	f := spec.Filters[0]
	if f.Prop != "city" || f.Op != "=" || f.Value != "上海" {
		t.Errorf("expected city=上海 filter, got %+v", f)
	}
}

// T3 · enum_ref unhappy path — value not in AllowedValues must fail loudly
// with PARAM_VALUE_UNKNOWN, listing every candidate so the agent loop can
// retry without guessing.
func TestBindIntentParams_EnumRefUnknown(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{
			Name: "city", Type: "enum_ref", Property: "city", Optional: true,
			AllowedValues: []string{"上海", "北京"},
		},
	}
	err := BindIntentParams(spec, map[string]interface{}{"city": "Mars"}, schema)
	if err == nil {
		t.Fatal("expected PARAM_VALUE_UNKNOWN for value not in allowed list")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "PARAM_VALUE_UNKNOWN" {
		t.Fatalf("expected PARAM_VALUE_UNKNOWN, got %v", err)
	}
	if !strings.Contains(re.Message, "Mars") {
		t.Errorf("message must include offending value, got %q", re.Message)
	}
	if !strings.Contains(re.Message, "上海") || !strings.Contains(re.Message, "北京") {
		t.Errorf("message must list allowed values, got %q", re.Message)
	}
	allowed, _ := re.Detail["allowed"].([]string)
	if len(allowed) != 2 || allowed[0] != "上海" || allowed[1] != "北京" {
		t.Errorf("detail.allowed must equal [上海 北京], got %v", re.Detail["allowed"])
	}
	if got, _ := re.Detail["got"].(string); got != "Mars" {
		t.Errorf("detail.got must equal %q, got %v", "Mars", re.Detail["got"])
	}
}

// T4 · enum_ref tolerance — case-insensitive (ASCII), trim whitespace.
// Canonical Value is the AllowedValues entry, NOT the raw LLM input.
func TestBindIntentParams_EnumRefCaseAndTrim(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{
			Name: "city", Type: "enum_ref", Property: "city", Optional: true,
			AllowedValues: []string{"上海", "Shanghai"},
		},
	}
	if err := BindIntentParams(spec, map[string]interface{}{"city": "上海 "}, schema); err != nil {
		t.Fatalf("expected trim match, got %v", err)
	}
	if got := spec.Filters[0].Value; got != "上海" {
		t.Errorf("expected canonical 上海, got %q", got)
	}
	spec.Filters = nil
	if err := BindIntentParams(spec, map[string]interface{}{"city": "shanghai"}, schema); err != nil {
		t.Fatalf("expected case-insensitive match, got %v", err)
	}
	if got := spec.Filters[0].Value; got != "Shanghai" {
		t.Errorf("expected canonical Shanghai, got %q", got)
	}
}

// Backward compat — nil AllowedValues falls back to type:string behavior.
// Required by ValidateIntentDryRun, which has no DB context to populate
// candidates.
func TestBindIntentParams_EnumRefNilAllowedFallback(t *testing.T) {
	spec := &QuerySpec{}
	schema := []IntentParameter{
		{Name: "city", Type: "enum_ref", Property: "city", Optional: true /* AllowedValues unset */},
	}
	if err := BindIntentParams(spec, map[string]interface{}{"city": "anything"}, schema); err != nil {
		t.Fatalf("nil AllowedValues must skip strict check, got %v", err)
	}
	if len(spec.Filters) != 1 || spec.Filters[0].Value != "anything" {
		t.Errorf("expected pass-through filter when AllowedValues is nil, got %+v", spec.Filters)
	}
}
