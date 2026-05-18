package lakehouse

// TDD test suite for the composite-Intent / plan executor.
// Spec: .omc/specs/plan-mode-composite-intent.md §4 (T1-T4 unit / T6 e2e).
// All tests in this file are pure unit (no DB) — T6 e2e lives elsewhere.
//
// Discipline: each test is written BEFORE the production code that satisfies
// it. The initial run must fail because ParsePlan / PlanExecutor / etc. don't
// exist yet (compile error). After minimal implementation each test passes
// for the right reason — not because a panic was silently swallowed.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────────────

// validHeroPlanJSON is the supply_chain_impact plan from spec §3.2 verbatim.
// Used as the happy path for parsing tests and as the structural template
// for cycle / id-clash variants below.
const validHeroPlanJSON = `{
  "params": [
    {"name": "ingredient", "type": "string", "description": "原料名"}
  ],
  "steps": [
    {
      "id": "ingredient",
      "od": "Ingredient",
      "select": ["id"],
      "filters": [{"prop": "name", "op": "like", "value": "%$param.ingredient%"}]
    },
    {
      "id": "skus",
      "od": "SKU",
      "select": ["id"],
      "filters": [{"prop": "ingredient_id", "op": "in", "value": "$ingredient.id"}]
    },
    {
      "id": "recipes",
      "od": "RecipeLine",
      "select": ["spec_id"],
      "filters": [{"prop": "sku_code", "op": "in", "value": "$skus.id"}]
    },
    {
      "id": "impact",
      "od": "OrderLine",
      "metric": "SUM(OrderLine.line_total), COUNT(DISTINCT Order.store_id)",
      "groupBy": ["STORE.city"],
      "filters": [{"prop": "spec_id", "op": "in", "value": "$recipes.spec_id"}]
    }
  ],
  "output": "impact"
}`

// ─────────────────────────────────────────────────────────────────────────────
// T1 · plan 解析与校验
// ─────────────────────────────────────────────────────────────────────────────

func TestT1_ParsePlan_ValidHeroPlan(t *testing.T) {
	p, err := ParsePlan([]byte(validHeroPlanJSON))
	if err != nil {
		t.Fatalf("ParsePlan(valid hero plan) returned error: %v", err)
	}
	if p == nil {
		t.Fatal("ParsePlan(valid hero plan) returned nil plan, no error")
	}
	if got, want := len(p.Steps), 4; got != want {
		t.Errorf("Steps count: got %d, want %d", got, want)
	}
	if p.Output != "impact" {
		t.Errorf("Output: got %q, want %q", p.Output, "impact")
	}
	if len(p.Params) != 1 || p.Params[0].Name != "ingredient" {
		t.Errorf("Params: got %+v, want one param named 'ingredient'", p.Params)
	}
}

func TestT1_ParsePlan_DuplicateStepID(t *testing.T) {
	bad := `{
	  "params": [{"name": "x", "type": "string"}],
	  "steps": [
	    {"id": "a", "od": "Foo", "select": ["id"], "filters": []},
	    {"id": "a", "od": "Bar", "select": ["id"], "filters": []}
	  ],
	  "output": "a"
	}`
	_, err := ParsePlan([]byte(bad))
	if err == nil {
		t.Fatal("expected error for duplicate step id, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "重复") {
		t.Errorf("error should mention 'duplicate' / '重复': %v", err)
	}
	if !strings.Contains(err.Error(), "a") {
		t.Errorf("error should name the duplicated id 'a': %v", err)
	}
}

func TestT1_ParsePlan_UnknownStepReference(t *testing.T) {
	bad := `{
	  "params": [],
	  "steps": [
	    {"id": "a", "od": "Foo", "select": ["id"], "filters": [
	      {"prop": "x", "op": "in", "value": "$ghost.id"}
	    ]}
	  ],
	  "output": "a"
	}`
	_, err := ParsePlan([]byte(bad))
	if err == nil {
		t.Fatal("expected error for $ghost reference to nonexistent step, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the unknown step 'ghost': %v", err)
	}
}

func TestT1_ParsePlan_UndeclaredParam(t *testing.T) {
	bad := `{
	  "params": [{"name": "declared", "type": "string"}],
	  "steps": [
	    {"id": "a", "od": "Foo", "select": ["id"], "filters": [
	      {"prop": "x", "op": "=", "value": "$param.undeclared"}
	    ]}
	  ],
	  "output": "a"
	}`
	_, err := ParsePlan([]byte(bad))
	if err == nil {
		t.Fatal("expected error for $param.undeclared, got nil")
	}
	if !strings.Contains(err.Error(), "undeclared") {
		t.Errorf("error should name the undeclared param: %v", err)
	}
}

func TestT1_ParsePlan_CycleDetected(t *testing.T) {
	// a -> b (a filters on $b.x), b -> a (b filters on $a.x). Cycle.
	bad := `{
	  "params": [],
	  "steps": [
	    {"id": "a", "od": "Foo", "select": ["x"], "filters": [
	      {"prop": "y", "op": "in", "value": "$b.x"}
	    ]},
	    {"id": "b", "od": "Bar", "select": ["x"], "filters": [
	      {"prop": "y", "op": "in", "value": "$a.x"}
	    ]}
	  ],
	  "output": "a"
	}`
	_, err := ParsePlan([]byte(bad))
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") && !strings.Contains(err.Error(), "循环") {
		t.Errorf("error should mention 'cycle' / '循环': %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T2 · 引用替换
// ─────────────────────────────────────────────────────────────────────────────

func TestT2_SubstituteStepRef_DedupAndJoin(t *testing.T) {
	stepResults := map[string][]map[string]interface{}{
		"skus": {{"id": "A"}, {"id": "B"}, {"id": "A"}},
	}
	f := PlanStepFilter{Prop: "sku_code", Op: "in", Value: "$skus.id"}
	got, empty, err := SubstituteFilter(f, nil, stepResults)
	if err != nil {
		t.Fatalf("SubstituteFilter: %v", err)
	}
	if empty {
		t.Fatal("non-empty step result reported as empty")
	}
	// Expect deduped, comma-joined values. Order isn't part of the contract,
	// so accept either permutation.
	if got.Value != "A,B" && got.Value != "B,A" {
		t.Errorf("substituted value = %q, want %q or %q", got.Value, "A,B", "B,A")
	}
}

func TestT2_SubstituteParam_EmbeddedInString(t *testing.T) {
	params := map[string]string{"ingredient": "燕麦奶"}
	f := PlanStepFilter{Prop: "name", Op: "like", Value: "%$param.ingredient%"}
	got, _, err := SubstituteFilter(f, params, nil)
	if err != nil {
		t.Fatalf("SubstituteFilter: %v", err)
	}
	if got.Value != "%燕麦奶%" {
		t.Errorf("substituted value = %q, want %q", got.Value, "%燕麦奶%")
	}
}

func TestT2_SubstituteStepRef_RejectedOnNonInOp(t *testing.T) {
	stepResults := map[string][]map[string]interface{}{
		"skus": {{"id": "A"}},
	}
	f := PlanStepFilter{Prop: "sku_code", Op: "=", Value: "$skus.id"}
	_, _, err := SubstituteFilter(f, nil, stepResults)
	if err == nil {
		t.Fatal("expected error for $stepId ref on op '=', got nil")
	}
	if !strings.Contains(err.Error(), "in") && !strings.Contains(err.Error(), "IN") {
		t.Errorf("error should mention op must be 'in'/'not in': %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T3 · 拓扑执行顺序 / 环检测
// ─────────────────────────────────────────────────────────────────────────────

func TestT3_TopologicalOrder_ResortsScrambledSteps(t *testing.T) {
	// Steps listed in REVERSE dependency order. ParsePlan must accept them
	// and the executor must walk them as ingredient → skus → recipes → impact.
	scrambled := `{
	  "params": [{"name": "ingredient", "type": "string"}],
	  "steps": [
	    {"id": "impact",     "od": "OrderLine", "metric": "SUM(OrderLine.line_total)", "filters": [
	      {"prop": "spec_id", "op": "in", "value": "$recipes.spec_id"}]},
	    {"id": "recipes",    "od": "RecipeLine", "select": ["spec_id"], "filters": [
	      {"prop": "sku_code", "op": "in", "value": "$skus.id"}]},
	    {"id": "skus",       "od": "SKU", "select": ["id"], "filters": [
	      {"prop": "ingredient_id", "op": "in", "value": "$ingredient.id"}]},
	    {"id": "ingredient", "od": "Ingredient", "select": ["id"], "filters": [
	      {"prop": "name", "op": "=", "value": "$param.ingredient"}]}
	  ],
	  "output": "impact"
	}`
	p, err := ParsePlan([]byte(scrambled))
	if err != nil {
		t.Fatalf("ParsePlan(scrambled): %v", err)
	}
	order, err := p.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	want := []string{"ingredient", "skus", "recipes", "impact"}
	if len(order) != len(want) {
		t.Fatalf("TopoOrder length: got %d, want %d (%v)", len(order), len(want), order)
	}
	for i, id := range want {
		if order[i] != id {
			t.Errorf("TopoOrder[%d]: got %q, want %q (full: %v)", i, order[i], id, order)
		}
	}
}

// T3 cycle case is covered by TestT1_ParsePlan_CycleDetected — listing it
// twice would only duplicate the same assertion.

// ─────────────────────────────────────────────────────────────────────────────
// T3b · TopoLevels — Kahn 分层、并行执行时长
// (v1.5 升级：plan-mode-composite-intent.md §2 撤销「v1 串行」红线。)
// ─────────────────────────────────────────────────────────────────────────────

func TestT3b_TopoLevels_DiamondAndIndependentLeaves(t *testing.T) {
	// Plan shape:
	//
	//      a ──┐
	//          ├──→ b ──┐
	//      c ──┘        ├──→ d
	//      e ──────────────┘
	//
	// Expected layers: [a c e], [b], [d]. (e is independent of a/b/c so it
	// sits at layer 0 with a and c.)
	plan, err := ParsePlan([]byte(`{
	  "params": [],
	  "steps": [
	    {"id": "a", "od": "A", "select": ["id"], "filters": []},
	    {"id": "c", "od": "C", "select": ["id"], "filters": []},
	    {"id": "e", "od": "E", "select": ["id"], "filters": []},
	    {"id": "b", "od": "B", "select": ["id"], "filters": [
	      {"prop": "a_id", "op": "in", "value": "$a.id"},
	      {"prop": "c_id", "op": "in", "value": "$c.id"}]},
	    {"id": "d", "od": "D", "metric": "SUM(D.x)", "filters": [
	      {"prop": "b_id", "op": "in", "value": "$b.id"},
	      {"prop": "e_id", "op": "in", "value": "$e.id"}]}
	  ],
	  "output": "d"
	}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	levels, err := plan.TopoLevels()
	if err != nil {
		t.Fatalf("TopoLevels: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("expected 3 layers, got %d (%v)", len(levels), levels)
	}
	if got, want := strings.Join(levels[0], ","), "a,c,e"; got != want {
		t.Errorf("layer 0 = %q, want %q", got, want)
	}
	if got, want := strings.Join(levels[1], ","), "b"; got != want {
		t.Errorf("layer 1 = %q, want %q", got, want)
	}
	if got, want := strings.Join(levels[2], ","), "d"; got != want {
		t.Errorf("layer 2 = %q, want %q", got, want)
	}
}

func TestT3b_ParallelExecution_LayerWallClock(t *testing.T) {
	// 3 mutually-independent leaves, each sleeping 80ms inside the runner.
	// Serial would take ~240ms; parallel must finish in well under 200ms.
	plan, err := ParsePlan([]byte(`{
	  "params": [],
	  "steps": [
	    {"id": "a", "od": "A", "select": ["id"], "filters": []},
	    {"id": "b", "od": "B", "select": ["id"], "filters": []},
	    {"id": "c", "od": "C", "select": ["id"], "filters": []},
	    {"id": "out", "od": "Z", "metric": "SUM(Z.v)", "filters": [
	      {"prop": "a_id", "op": "in", "value": "$a.id"},
	      {"prop": "b_id", "op": "in", "value": "$b.id"},
	      {"prop": "c_id", "op": "in", "value": "$c.id"}]}
	  ],
	  "output": "out"
	}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}

	// Peak concurrency observed during layer 0 must be exactly 3 — that's
	// what proves the layer actually fans out. Layer 1 is single-step, so
	// it never lifts peak above what layer 0 already saw.
	var inFlight, peak int32
	runner := func(ctx context.Context, stepID string, spec smartquery.QuerySpec) LakehouseResult {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		// Selector steps need a row each so the downstream IN-list is non-empty.
		// The output step closes the plan with a numeric row.
		switch stepID {
		case "a":
			return LakehouseResult{ExecutionOK: true, ResultJSON: `[{"id":"A1"}]`}
		case "b":
			return LakehouseResult{ExecutionOK: true, ResultJSON: `[{"id":"B1"}]`}
		case "c":
			return LakehouseResult{ExecutionOK: true, ResultJSON: `[{"id":"C1"}]`}
		case "out":
			return LakehouseResult{ExecutionOK: true, ResultJSON: `[{"sum":42}]`}
		}
		t.Fatalf("unexpected step: %s", stepID)
		return LakehouseResult{}
	}

	start := time.Now()
	exec := &PlanExecutor{Runner: runner}
	res, err := exec.Execute(context.Background(), plan, nil, "proj")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.ExecutionOK {
		t.Fatalf("ExecutionOK=false, errMsg=%q", res.ErrorMessage)
	}
	if peak < 3 {
		t.Errorf("peak in-flight = %d, want 3 (proves layer-0 fan-out)", peak)
	}
	// Serial would be ≥240ms. Headroom 200ms covers CI slack and Go scheduler jitter.
	if elapsed > 200*time.Millisecond {
		t.Errorf("elapsed = %v, want < 200ms (parallel layer 0). Serial baseline ~240ms.", elapsed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T4 · 空中间结果短路
// ─────────────────────────────────────────────────────────────────────────────

func TestT4_EmptyIntermediateShortCircuit(t *testing.T) {
	// Two-step plan: a (filter on $param) → b (filter on $a.id). If a returns
	// 0 rows, b must NOT be executed with `IN ()` (illegal SQL); the plan
	// returns ExecutionOK=true with an empty ResultJSON.
	plan, err := ParsePlan([]byte(`{
	  "params": [{"name": "name", "type": "string"}],
	  "steps": [
	    {"id": "a", "od": "Foo", "select": ["id"], "filters": [
	      {"prop": "name", "op": "=", "value": "$param.name"}]},
	    {"id": "b", "od": "Bar", "metric": "SUM(Bar.amount)", "filters": [
	      {"prop": "foo_id", "op": "in", "value": "$a.id"}]}
	  ],
	  "output": "b"
	}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}

	bCalled := false
	runner := func(ctx context.Context, stepID string, spec smartquery.QuerySpec) LakehouseResult {
		switch stepID {
		case "a":
			// a runs, returns zero rows (no Foo named "ghost"). ExecutionOK still true.
			return LakehouseResult{ExecutionOK: true, ResultJSON: "[]"}
		case "b":
			bCalled = true
			// If the executor calls us at all for b, that's a bug — short-circuit
			// must skip the SQL round-trip when a returns 0 rows.
			return LakehouseResult{ExecutionOK: true, ResultJSON: `[{"amount":42}]`}
		}
		t.Fatalf("unexpected step: %s", stepID)
		return LakehouseResult{}
	}

	exec := &PlanExecutor{Runner: runner}
	res, err := exec.Execute(context.Background(), plan, map[string]string{"name": "ghost"}, "proj-x")
	if err != nil {
		t.Fatalf("Execute returned error: %v (short-circuit should be silent success)", err)
	}
	if !res.ExecutionOK {
		t.Errorf("ExecutionOK=false on empty short-circuit; want true. errMsg=%q", res.ErrorMessage)
	}
	if res.ResultJSON != "[]" {
		t.Errorf("ResultJSON on empty short-circuit = %q, want %q", res.ResultJSON, "[]")
	}
	if bCalled {
		t.Error("downstream step 'b' was executed despite upstream 'a' being empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Trace · executor must emit a PlanTrace covering every step.
// (B1: drives the frontend PlanGraph visualization.)
// ─────────────────────────────────────────────────────────────────────────────

func TestPlanTrace_PopulatedOnSuccess(t *testing.T) {
	plan, err := ParsePlan([]byte(`{
	  "params": [{"name": "name", "type": "string"}],
	  "steps": [
	    {"id": "a", "od": "A", "select": ["id"], "filters": [
	      {"prop": "name", "op": "=", "value": "$param.name"}]},
	    {"id": "b", "od": "B", "metric": "SUM(B.x)", "filters": [
	      {"prop": "a_id", "op": "in", "value": "$a.id"}]}
	  ],
	  "output": "b"
	}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	runner := func(ctx context.Context, stepID string, spec smartquery.QuerySpec) LakehouseResult {
		switch stepID {
		case "a":
			return LakehouseResult{ExecutionOK: true, ResultJSON: `[{"id":"A1"},{"id":"A2"}]`, SQL: "SELECT id FROM A WHERE name='foo'"}
		case "b":
			return LakehouseResult{ExecutionOK: true, ResultJSON: `[{"x":10}]`, SQL: "SELECT SUM(x) FROM B WHERE a_id IN ('A1','A2')"}
		}
		t.Fatalf("unexpected step: %s", stepID)
		return LakehouseResult{}
	}
	exec := &PlanExecutor{Runner: runner}
	res, err := exec.Execute(context.Background(), plan, map[string]string{"name": "foo"}, "proj")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PlanTrace == nil {
		t.Fatal("PlanTrace nil on success — expected populated trace")
	}
	if len(res.PlanTrace.Steps) != 2 {
		t.Fatalf("trace step count = %d, want 2", len(res.PlanTrace.Steps))
	}
	if res.PlanTrace.Output != "b" {
		t.Errorf("trace.Output = %q, want %q", res.PlanTrace.Output, "b")
	}
	stA := res.PlanTrace.Steps[0]
	if stA.ID != "a" || stA.Layer != 0 || stA.Status != "success" || stA.RowCount != 2 {
		t.Errorf("step a trace = %+v; want id=a layer=0 status=success rowCount=2", stA)
	}
	if stA.IsOutput {
		t.Errorf("step a marked IsOutput=true; only output step should be true")
	}
	stB := res.PlanTrace.Steps[1]
	if stB.ID != "b" || stB.Layer != 1 || stB.Status != "success" || !stB.IsOutput {
		t.Errorf("step b trace = %+v; want id=b layer=1 status=success isOutput=true", stB)
	}
	if len(stB.DependsOn) != 1 || stB.DependsOn[0] != "a" {
		t.Errorf("step b dependsOn = %v, want [a]", stB.DependsOn)
	}
	if stB.SQL == "" {
		t.Error("step b SQL empty in trace; want runner SQL captured")
	}
}

func TestPlanTrace_EmptyShortCircuitMarked(t *testing.T) {
	plan, err := ParsePlan([]byte(`{
	  "params": [{"name": "name", "type": "string"}],
	  "steps": [
	    {"id": "a", "od": "A", "select": ["id"], "filters": [
	      {"prop": "name", "op": "=", "value": "$param.name"}]},
	    {"id": "b", "od": "B", "metric": "SUM(B.x)", "filters": [
	      {"prop": "a_id", "op": "in", "value": "$a.id"}]}
	  ],
	  "output": "b"
	}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	runner := func(ctx context.Context, stepID string, spec smartquery.QuerySpec) LakehouseResult {
		if stepID == "a" {
			return LakehouseResult{ExecutionOK: true, ResultJSON: "[]"}
		}
		t.Errorf("step b should not have been called (upstream empty)")
		return LakehouseResult{ExecutionOK: true, ResultJSON: "[]"}
	}
	exec := &PlanExecutor{Runner: runner}
	res, _ := exec.Execute(context.Background(), plan, map[string]string{"name": "ghost"}, "proj")
	if res.PlanTrace == nil {
		t.Fatal("trace nil even on empty short-circuit; want non-nil so frontend can show which step went empty")
	}
	if res.PlanTrace.Steps[0].Status != "success" || res.PlanTrace.Steps[0].RowCount != 0 {
		t.Errorf("step a trace = %+v; want status=success rowCount=0 (the gate-keeper step itself ran fine)", res.PlanTrace.Steps[0])
	}
	if res.PlanTrace.Steps[1].Status != "empty" {
		t.Errorf("step b trace status = %q, want %q", res.PlanTrace.Steps[1].Status, "empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Sanity: an obviously-bad executor wiring should surface as an error, not
// hang or panic. Guards against accidentally introducing nil-runner crashes
// during refactors.
// ─────────────────────────────────────────────────────────────────────────────

func TestPlanExecutor_NilRunnerRejected(t *testing.T) {
	plan, err := ParsePlan([]byte(validHeroPlanJSON))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	exec := &PlanExecutor{Runner: nil}
	_, err = exec.Execute(context.Background(), plan, map[string]string{"ingredient": "燕麦奶"}, "proj-x")
	if err == nil {
		t.Fatal("expected error from nil Runner, got nil")
	}
	var rerr *smartquery.ResolveError
	if errors.As(err, &rerr) {
		// Acceptable shape: any structured error.
	}
}
