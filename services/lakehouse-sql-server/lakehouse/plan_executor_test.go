package lakehouse

// TDD tests for the composite-Intent / Plan Executor (spec
// .omc/specs/plan-mode-composite-intent.md §4).
//
//   T1 — Plan parse + structural validation
//   T2 — Reference substitution (`$param.X`, `$stepId.col`)
//   T3 — Topological execution / cycle detection
//   T4 — Empty intermediate-result short-circuit
//
// T5 (combine block) is v1.1 — explicitly skipped here.
// T6 (end-to-end hero question) goes in a separate integration test against
// the live demo project, not this unit suite.

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// ── T1 · Plan parse + validation ─────────────────────────────────────────────

func TestT1_ParsePlan_Valid(t *testing.T) {
	raw := `{
      "params": [{"name":"ingredient","type":"string","description":"原料名"}],
      "steps": [
        {"id":"ing","od":"Ingredient","select":["id"],
         "filters":[{"prop":"name","op":"like","value":"%$param.ingredient%"}]},
        {"id":"skus","od":"SKU","select":["id"],
         "filters":[{"prop":"ingredient_id","op":"in","value":"$ing.id"}]},
        {"id":"impact","od":"OrderLine",
         "metric":"SUM(line_total)",
         "filters":[{"prop":"sku_code","op":"in","value":"$skus.id"}]}
      ],
      "output":"impact"
    }`
	p, err := ParsePlan([]byte(raw))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(p.Steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(p.Steps))
	}
	if p.Output != "impact" {
		t.Fatalf("want output=impact, got %q", p.Output)
	}
	if len(p.Params) != 1 || p.Params[0].Name != "ingredient" {
		t.Fatalf("unexpected params: %+v", p.Params)
	}
}

func TestT1_ParsePlan_DuplicateStepID(t *testing.T) {
	raw := `{"params":[],"steps":[
      {"id":"a","od":"A","select":["id"]},
      {"id":"a","od":"B","select":["id"]}
    ],"output":"a"}`
	_, err := ParsePlan([]byte(raw))
	if err == nil {
		t.Fatal("expected duplicate-step-id error, got nil")
	}
	m := strings.ToLower(err.Error())
	if !strings.Contains(m, "duplicate") && !strings.Contains(err.Error(), "重复") {
		t.Fatalf("error should mention duplicate id: %v", err)
	}
	if !strings.Contains(err.Error(), "a") {
		t.Fatalf("error should name the offending id: %v", err)
	}
}

func TestT1_ParsePlan_UnknownStepRef(t *testing.T) {
	raw := `{"params":[],"steps":[
      {"id":"a","od":"A","select":["id"],
       "filters":[{"prop":"x","op":"in","value":"$nope.id"}]}
    ],"output":"a"}`
	_, err := ParsePlan([]byte(raw))
	if err == nil {
		t.Fatal("expected unknown-step-ref error, got nil")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error should name 'nope': %v", err)
	}
}

func TestT1_ParsePlan_UnknownParamRef(t *testing.T) {
	raw := `{"params":[{"name":"x","type":"string"}],"steps":[
      {"id":"a","od":"A","select":["id"],
       "filters":[{"prop":"n","op":"like","value":"%$param.y%"}]}
    ],"output":"a"}`
	_, err := ParsePlan([]byte(raw))
	if err == nil {
		t.Fatal("expected unknown-param-ref error, got nil")
	}
	if !strings.Contains(err.Error(), "y") {
		t.Fatalf("error should name unknown param 'y': %v", err)
	}
}

func TestT1_ParsePlan_Cycle(t *testing.T) {
	// a → b → a
	raw := `{"params":[],"steps":[
      {"id":"a","od":"A","select":["id"],
       "filters":[{"prop":"x","op":"in","value":"$b.id"}]},
      {"id":"b","od":"B","select":["id"],
       "filters":[{"prop":"y","op":"in","value":"$a.id"}]}
    ],"output":"a"}`
	_, err := ParsePlan([]byte(raw))
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	m := strings.ToLower(err.Error())
	if !strings.Contains(m, "cycle") && !strings.Contains(err.Error(), "循环") {
		t.Fatalf("error should mention cycle/循环: %v", err)
	}
}

func TestT1_ParsePlan_OutputMustReferenceStep(t *testing.T) {
	raw := `{"params":[],"steps":[
      {"id":"a","od":"A","select":["id"]}
    ],"output":"nope"}`
	_, err := ParsePlan([]byte(raw))
	if err == nil {
		t.Fatal("expected unknown-output error, got nil")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error should name 'nope': %v", err)
	}
}

// ── T2 · Reference substitution ──────────────────────────────────────────────

func TestT2_ResolveFilterValue_StepResultIn_Dedup(t *testing.T) {
	results := map[string][]map[string]any{
		"skus": {{"id": "A"}, {"id": "B"}, {"id": "A"}},
	}
	got, empty, err := ResolveFilterValue("$skus.id", "in", nil, results)
	if err != nil {
		t.Fatalf("ResolveFilterValue: %v", err)
	}
	if empty {
		t.Fatal("expected non-empty result set")
	}
	parts := strings.Split(got, ",")
	sort.Strings(parts)
	want := []string{"A", "B"}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("got dedup=%v want %v (raw=%q)", parts, want, got)
	}
}

func TestT2_ResolveFilterValue_ParamEmbed(t *testing.T) {
	got, empty, err := ResolveFilterValue("%$param.ingredient%", "like",
		map[string]any{"ingredient": "燕麦奶"}, nil)
	if err != nil {
		t.Fatalf("ResolveFilterValue: %v", err)
	}
	if empty {
		t.Fatal("param embed should never produce empty")
	}
	if got != "%燕麦奶%" {
		t.Fatalf("got %q want %q", got, "%燕麦奶%")
	}
}

func TestT2_ResolveFilterValue_StepRef_NonInOpRejected(t *testing.T) {
	results := map[string][]map[string]any{"s": {{"id": "A"}}}
	for _, op := range []string{"=", "<>", "like", ">"} {
		_, _, err := ResolveFilterValue("$s.id", op, nil, results)
		if err == nil {
			t.Errorf("op %q: expected error for $stepId ref on non-IN op", op)
		}
	}
}

func TestT2_ResolveFilterValue_StepRef_NotInAllowed(t *testing.T) {
	results := map[string][]map[string]any{"s": {{"id": "A"}, {"id": "B"}}}
	_, _, err := ResolveFilterValue("$s.id", "not in", nil, results)
	if err != nil {
		t.Fatalf("'not in' should accept $stepId ref: %v", err)
	}
}

func TestT2_ResolveFilterValue_StepResult_EmptySignal(t *testing.T) {
	results := map[string][]map[string]any{"s": {}}
	got, empty, err := ResolveFilterValue("$s.id", "in", nil, results)
	if err != nil {
		t.Fatalf("empty step ref should NOT error: %v", err)
	}
	if !empty {
		t.Fatalf("expected empty=true, got empty=false (value=%q)", got)
	}
}

// ── T3 · Topological execution / cycle detection ────────────────────────────

func TestT3_TopologicalSort_ReordersOutOfOrder(t *testing.T) {
	// impact depends on skus depends on ing — fed in reverse.
	raw := `{"params":[],"steps":[
      {"id":"impact","od":"OrderLine","metric":"SUM(t)",
       "filters":[{"prop":"sku","op":"in","value":"$skus.id"}]},
      {"id":"skus","od":"SKU","select":["id"],
       "filters":[{"prop":"ing","op":"in","value":"$ing.id"}]},
      {"id":"ing","od":"Ingredient","select":["id"]}
    ],"output":"impact"}`
	p, err := ParsePlan([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	order, err := TopologicalSort(p)
	if err != nil {
		t.Fatalf("topo: %v", err)
	}
	got := make([]string, len(order))
	for i, idx := range order {
		got[i] = p.Steps[idx].ID
	}
	want := []string{"ing", "skus", "impact"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("topo order %v want %v", got, want)
	}
}

func TestT3_TopologicalSort_DetectsCycle(t *testing.T) {
	// Construct directly — ParsePlan would reject earlier, but TopologicalSort
	// must be a standalone safety net.
	p := &Plan{
		Steps: []PlanStep{
			{ID: "a", Od: "A", Filters: []smartquery.FilterItem{{Prop: "x", Op: "in", Value: "$b.id"}}},
			{ID: "b", Od: "B", Filters: []smartquery.FilterItem{{Prop: "y", Op: "in", Value: "$a.id"}}},
		},
		Output: "a",
	}
	_, err := TopologicalSort(p)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

// ── T4 · Empty intermediate-result short-circuit ─────────────────────────────

// mockRunner records every spec it's asked to execute and returns canned rows
// keyed by the spec's primary Od. ResultJSON is the only field the executor
// reads to chain step→step; the rest of LakehouseResult passes through.
type mockRunner struct {
	rowsByOd map[string][]map[string]any
	calls    []smartquery.QuerySpec
}

func (m *mockRunner) Execute(ctx context.Context, spec smartquery.QuerySpec) LakehouseResult {
	m.calls = append(m.calls, spec)
	od := ""
	if len(spec.Objects) > 0 {
		od = spec.Objects[0]
	}
	rows := m.rowsByOd[od]
	raw, _ := json.Marshal(rows)
	return LakehouseResult{
		ExecutionOK: true,
		ResultJSON:  string(raw),
		SQL:         "/* mock */",
	}
}

func TestT4_ExecutePlan_EmptyShortCircuit(t *testing.T) {
	raw := `{"params":[{"name":"ingredient","type":"string"}],"steps":[
      {"id":"ing","od":"Ingredient","select":["id"],
       "filters":[{"prop":"name","op":"like","value":"%$param.ingredient%"}]},
      {"id":"skus","od":"SKU","select":["id"],
       "filters":[{"prop":"ingredient_id","op":"in","value":"$ing.id"}]},
      {"id":"impact","od":"OrderLine","metric":"SUM(line_total)",
       "filters":[{"prop":"sku_code","op":"in","value":"$skus.id"}]}
    ],"output":"impact"}`
	p, err := ParsePlan([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	runner := &mockRunner{
		rowsByOd: map[string][]map[string]any{
			"Ingredient": {}, // empty — short-circuit upstream
			"SKU":        {{"id": "should-not-be-used"}},
			"OrderLine":  {{"Total_line_total": 9999}},
		},
	}
	res := ExecutePlan(context.Background(), runner, p,
		map[string]any{"ingredient": "不存在的原料"}, "proj-id")

	if !res.ExecutionOK {
		t.Fatalf("expected ExecutionOK=true on graceful empty, got false: %s",
			res.ErrorMessage)
	}
	// Only the upstream step should have run. The downstream skus/impact must
	// have been short-circuited — emphatically NOT issued as `IN ()` SQL.
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 runner call (the upstream ingredient step), got %d",
			len(runner.calls))
	}
	// And the final ResultJSON must be a clean empty-set, not the OrderLine
	// row the mock would have returned if downstream had wrongly fired.
	if strings.Contains(res.ResultJSON, "9999") {
		t.Fatalf("downstream incorrectly executed: ResultJSON=%s", res.ResultJSON)
	}
}

func TestT4_ExecutePlan_HappyPathProducesOutput(t *testing.T) {
	raw := `{"params":[{"name":"ingredient","type":"string"}],"steps":[
      {"id":"ing","od":"Ingredient","select":["id"],
       "filters":[{"prop":"name","op":"like","value":"%$param.ingredient%"}]},
      {"id":"impact","od":"OrderLine","metric":"SUM(line_total)",
       "filters":[{"prop":"sku_code","op":"in","value":"$ing.id"}]}
    ],"output":"impact"}`
	p, err := ParsePlan([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	runner := &mockRunner{
		rowsByOd: map[string][]map[string]any{
			"Ingredient": {{"id": "X"}, {"id": "Y"}},
			"OrderLine":  {{"Total_line_total": 1234.5}},
		},
	}
	res := ExecutePlan(context.Background(), runner, p,
		map[string]any{"ingredient": "燕麦奶"}, "proj-id")
	if !res.ExecutionOK {
		t.Fatalf("expected ExecutionOK=true, got: %s", res.ErrorMessage)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 runner calls, got %d", len(runner.calls))
	}
	// The impact spec must have been called with the resolved IN list.
	last := runner.calls[1]
	if last.Objects[0] != "OrderLine" {
		t.Fatalf("last call should be OrderLine, got %v", last.Objects)
	}
	if len(last.Filters) != 1 {
		t.Fatalf("expected 1 filter on impact spec, got %d", len(last.Filters))
	}
	resolved := strings.Split(last.Filters[0].Value, ",")
	sort.Strings(resolved)
	if !reflect.DeepEqual(resolved, []string{"X", "Y"}) {
		t.Fatalf("expected IN list X,Y; got %v", resolved)
	}
	// projectId must propagate to each step spec.
	for i, c := range runner.calls {
		if c.ProjectID != "proj-id" {
			t.Errorf("call %d: projectId=%q want proj-id", i, c.ProjectID)
		}
	}
}
