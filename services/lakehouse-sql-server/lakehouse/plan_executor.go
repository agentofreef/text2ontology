// Package lakehouse — composite-Intent / plan executor.
//
// Spec: .omc/specs/plan-mode-composite-intent.md.
//
// A Plan is a step DAG authored on lakehouse_metric_intent.plan. Each step is
// a single QuerySpec (one OD, optional metric / groupBy / filters / select);
// later steps reference upstream step results through `$stepId.col` IN-list
// expansion. The executor walks the DAG in topological order, substitutes
// references, calls a pluggable StepRunner per step, and returns the result
// of the step named by plan.output.
//
// Determinism is a hard requirement: no LLM is invoked at execute time. The
// LLM only chose the Intent + parameter values upstream (in agent-server).
package lakehouse

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// Plan is the parsed lakehouse_metric_intent.plan JSON. Field names and JSON
// tags match spec §3.2 verbatim so the on-disk JSONB round-trips identically.
type Plan struct {
	Params []smartquery.IntentParameter `json:"params"`
	Steps  []PlanStep                   `json:"steps"`
	Output string                       `json:"output"`
}

// PlanStep is one node of the step DAG. `Metric` empty = "selector" step
// (only produces a row set for downstream IN expansion); set = leaf
// aggregation step that contributes rows to the final result.
type PlanStep struct {
	ID      string           `json:"id"`
	Od      string           `json:"od"`
	Select  []string         `json:"select,omitempty"`
	Metric  string           `json:"metric,omitempty"`
	GroupBy []string         `json:"groupBy,omitempty"`
	Filters []PlanStepFilter `json:"filters,omitempty"`
}

// PlanStepFilter mirrors smartquery.FilterItem with no FuzzyMatch — composite
// Intents are author-controlled and don't need the keyword-correction hook.
type PlanStepFilter struct {
	Prop  string `json:"prop"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// refPattern matches `$<head>.<tail>` where head is either "param" (literal)
// or a step ID. `tail` is a single identifier (param name or column name).
// Embedded references inside strings (e.g. `%$param.ingredient%`) are also
// matched — the resulting substitution preserves the surrounding text.
var refPattern = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)`)

// dottedPropPattern matches "Od.prop" style cross-OD references that may
// appear in a step's metric / groupBy / filter prop. We use it to auto-extend
// spec.Objects so multi-OD steps trigger the engine's join-path resolver.
// The leading byte must NOT be `$` so we don't match plan-level references
// (which are handled by refPattern + SubstituteFilter).
var dottedPropPattern = regexp.MustCompile(`(?:\A|[^A-Za-z0-9_$])([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)`)

// ─────────────────────────────────────────────────────────────────────────────
// Parsing + validation
// ─────────────────────────────────────────────────────────────────────────────

// ParsePlan decodes a plan JSON and runs full structural validation:
//   - step IDs unique
//   - every `$param.X` declared in params
//   - every `$stepId.col` references a known step
//   - `output` names an existing step
//   - the step dependency graph is acyclic
//
// On any structural error, returns a descriptive error that names the
// offending id / param / step so the failure is addressable, not generic.
func ParsePlan(raw []byte) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("plan json decode: %w", err)
	}
	if len(p.Steps) == 0 {
		return nil, fmt.Errorf("plan has no steps")
	}
	if p.Output == "" {
		return nil, fmt.Errorf("plan.output not set")
	}

	// 1. Step IDs unique.
	seen := map[string]bool{}
	for _, s := range p.Steps {
		if s.ID == "" {
			return nil, fmt.Errorf("plan step missing id (od=%q)", s.Od)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("plan has duplicate step id %q", s.ID)
		}
		seen[s.ID] = true
	}

	// 2. output → existing step.
	if !seen[p.Output] {
		return nil, fmt.Errorf("plan.output %q references unknown step", p.Output)
	}

	// 3. References: $param.X declared, $stepId references a known step.
	paramSet := map[string]bool{}
	for _, pr := range p.Params {
		paramSet[pr.Name] = true
	}
	for _, s := range p.Steps {
		for _, f := range s.Filters {
			for _, m := range refPattern.FindAllStringSubmatch(f.Value, -1) {
				head, tail := m[1], m[2]
				if head == "param" {
					if !paramSet[tail] {
						return nil, fmt.Errorf("step %q filter %q references undeclared param %q", s.ID, f.Prop, tail)
					}
					continue
				}
				if !seen[head] {
					return nil, fmt.Errorf("step %q filter %q references unknown step %q", s.ID, f.Prop, head)
				}
				if head == s.ID {
					return nil, fmt.Errorf("step %q filter %q self-references (cycle of 1)", s.ID, f.Prop)
				}
			}
		}
	}

	// 4. Cycle detection via topological sort.
	if _, err := p.TopoOrder(); err != nil {
		return nil, err
	}

	return &p, nil
}

// stepDependencies returns the set of step IDs whose results step s consumes
// via `$stepId.col` filter values. `$param.X` references are ignored — those
// are bound from caller-supplied params, not from another step.
func (s PlanStep) stepDependencies() []string {
	dep := map[string]bool{}
	for _, f := range s.Filters {
		for _, m := range refPattern.FindAllStringSubmatch(f.Value, -1) {
			if m[1] != "param" {
				dep[m[1]] = true
			}
		}
	}
	out := make([]string, 0, len(dep))
	for k := range dep {
		out = append(out, k)
	}
	return out
}

// TopoLevels returns the step IDs partitioned into dependency layers (Kahn).
// All steps in levels[0] have zero dependencies and can run in parallel.
// Every step in levels[i] depends only on steps in levels[<i]. Within each
// layer steps are sorted alphabetically for deterministic output ordering
// (the parallel runtime still races them — sort just makes test assertions
// stable).
//
// Returns an error on cycles or references to undefined steps.
func (p *Plan) TopoLevels() ([][]string, error) {
	byID := map[string]PlanStep{}
	for _, s := range p.Steps {
		byID[s.ID] = s
	}
	inDeg := map[string]int{}
	children := map[string][]string{}
	for _, s := range p.Steps {
		for _, dep := range s.stepDependencies() {
			if _, ok := byID[dep]; !ok {
				return nil, fmt.Errorf("step %q depends on unknown step %q", s.ID, dep)
			}
			inDeg[s.ID]++
			children[dep] = append(children[dep], s.ID)
		}
	}
	remaining := map[string]bool{}
	for _, s := range p.Steps {
		remaining[s.ID] = true
	}
	var levels [][]string
	for len(remaining) > 0 {
		var layer []string
		for id := range remaining {
			if inDeg[id] == 0 {
				layer = append(layer, id)
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("plan contains a cycle (remaining steps with no zero-in-degree node: %v)", keysOf(remaining))
		}
		sort.Strings(layer)
		for _, id := range layer {
			delete(remaining, id)
			for _, c := range children[id] {
				inDeg[c]--
			}
		}
		levels = append(levels, layer)
	}
	return levels, nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TopoOrder returns step IDs in dependency order (upstreams before downstreams).
// Returns an error if the dependency graph contains a cycle.
func (p *Plan) TopoOrder() ([]string, error) {
	byID := map[string]PlanStep{}
	deps := map[string][]string{}
	for _, s := range p.Steps {
		byID[s.ID] = s
		deps[s.ID] = s.stepDependencies()
	}

	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := map[string]int{}
	var order []string

	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case done:
			return nil
		case onStack:
			return fmt.Errorf("plan contains a cycle: %s -> %s", strings.Join(path, " -> "), id)
		}
		state[id] = onStack
		for _, d := range deps[id] {
			if _, ok := byID[d]; !ok {
				// ParsePlan already validates this, but TopoOrder may be called
				// on a partially-validated plan during tests — fail loudly here too.
				return fmt.Errorf("step %q depends on unknown step %q", id, d)
			}
			if err := visit(d, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = done
		order = append(order, id)
		return nil
	}

	for _, s := range p.Steps {
		if err := visit(s.ID, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Substitution
// ─────────────────────────────────────────────────────────────────────────────

// SubstituteFilter expands `$param.*` and `$stepId.col` references inside
// f.Value into concrete literals.
//
// Rules (spec §3.3):
//   - `$param.X` is replaced with the param's literal value. Multiple occurrences
//     and embedding (e.g. `"%$param.ingredient%"`) both work.
//   - `$stepId.col` becomes a comma-separated, deduplicated list of values from
//     that step's results column. Only legal on `in` / `not in` ops.
//   - If `$stepId.col` references a step with zero rows, the returned filter is
//     a no-op but the second return value (`empty`) is true so the caller can
//     short-circuit the SQL round-trip (see PlanExecutor.Execute).
func SubstituteFilter(
	f PlanStepFilter,
	params map[string]string,
	stepResults map[string][]map[string]interface{},
) (PlanStepFilter, bool, error) {
	out := f
	matches := refPattern.FindAllStringSubmatch(f.Value, -1)
	if len(matches) == 0 {
		return out, false, nil
	}

	// Split into param refs (substitute textually) and stepRefs (validate
	// op + expand only if standalone).
	for _, m := range matches {
		head, tail := m[1], m[2]
		token := m[0]
		if head == "param" {
			val, ok := params[tail]
			if !ok {
				return out, false, fmt.Errorf("substitute: unbound param %q", tail)
			}
			out.Value = strings.ReplaceAll(out.Value, token, val)
		}
	}

	// Re-scan with param refs already substituted. Anything left that matches
	// refPattern must be a $stepId.col reference.
	matches = refPattern.FindAllStringSubmatch(out.Value, -1)
	if len(matches) == 0 {
		return out, false, nil
	}

	op := strings.ToLower(strings.TrimSpace(out.Op))
	if op != "in" && op != "not in" {
		return out, false, fmt.Errorf("substitute: $stepId.col reference is only valid on op 'in' or 'not in', got %q (value=%q)", f.Op, f.Value)
	}

	// stepRef must be the WHOLE value — mixing literal text with a set
	// expansion has no defined semantics.
	if !isStandaloneRef(out.Value) {
		return out, false, fmt.Errorf("substitute: $stepId.col must be the entire filter value, got %q", out.Value)
	}

	m := matches[0]
	stepID, col := m[1], m[2]
	rows, ok := stepResults[stepID]
	if !ok {
		return out, false, fmt.Errorf("substitute: step %q has no results yet", stepID)
	}

	uniq := make(map[string]bool, len(rows))
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		raw, present := row[col]
		if !present {
			continue
		}
		s := stringify(raw)
		if s == "" || uniq[s] {
			continue
		}
		uniq[s] = true
		values = append(values, s)
	}
	if len(values) == 0 {
		// Empty IN-list. Mark for short-circuit instead of producing illegal SQL.
		return out, true, nil
	}
	out.Value = strings.Join(values, ",")
	return out, false, nil
}

func isStandaloneRef(v string) bool {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "$") {
		return false
	}
	loc := refPattern.FindStringIndex(v)
	if loc == nil {
		return false
	}
	return loc[0] == 0 && loc[1] == len(v)
}

func stringify(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Execution
// ─────────────────────────────────────────────────────────────────────────────

// StepRunner executes a single step's QuerySpec against the lakehouse and
// returns a LakehouseResult. Decoupled from Engine.Execute so the unit tests
// can run without a database, and so callers wanting to layer metrics /
// tracing / dry-run logic can wrap the runner cleanly.
type StepRunner func(ctx context.Context, stepID string, spec smartquery.QuerySpec) LakehouseResult

// PlanExecutor runs a parsed Plan to completion. Deterministic — no LLM.
// Runner must be non-nil; Execute returns an error otherwise.
type PlanExecutor struct {
	Runner StepRunner
}

// Execute runs the plan and returns the result of the step named by
// plan.Output. Behaviour summary:
//
//   - Steps are walked in dependency LAYERS (Kahn). Steps in the same layer
//     run concurrently — the per-step work is independent SQL, and the
//     `$stepId.col` reference graph guarantees no cross-step state writes
//     within a layer.
//   - Per step, filter values are bound via SubstituteFilter using results
//     written by previous layers (visible — a previous layer fully completed
//     before this one starts).
//   - If any filter substitution yields an empty IN-list (upstream step had
//     zero rows), this step is skipped and inherits an empty result. All
//     downstream steps that depend on it propagate the empty result too. The
//     final returned LakehouseResult is `{ExecutionOK: true, ResultJSON: "[]"}`
//     — a clean "0 影响 / 未找到", never an error or panic.
//   - The non-empty path assembles a smartquery.QuerySpec from the step
//     (Objects=[step.Od …], Metric, GroupBy, Filters) and hands it to Runner.
//   - A non-OK Runner result anywhere fails the whole plan immediately; the
//     first error wins, in-flight goroutines in the same layer still run to
//     completion (no goroutine leak) but their results are discarded.
func (e *PlanExecutor) Execute(
	ctx context.Context,
	plan *Plan,
	params map[string]string,
	projectID string,
) (LakehouseResult, error) {
	if e.Runner == nil {
		return LakehouseResult{}, fmt.Errorf("PlanExecutor.Execute: Runner is nil")
	}

	levels, err := plan.TopoLevels()
	if err != nil {
		return LakehouseResult{}, err
	}
	stepByID := map[string]PlanStep{}
	for _, s := range plan.Steps {
		stepByID[s.ID] = s
	}

	// stepResults: rows of each non-empty step. empty: ids of steps that
	// short-circuited (so dependents short-circuit too). traceByID: every
	// step records its StepTrace here so the frontend can render the DAG
	// after the plan finishes. Maps are shared across the layer's goroutines
	// (writes go to disjoint keys) — the mutex guards map header mutation.
	var mu sync.Mutex
	stepResults := map[string][]map[string]interface{}{}
	empty := map[string]bool{}
	traceByID := map[string]StepTrace{}

	// runStep executes a single step in the current layer and writes its
	// StepTrace into traceByID before returning.
	runStep := func(id string, layerIdx int) error {
		stepStart := time.Now()
		step := stepByID[id]
		deps := step.stepDependencies()
		baseTrace := StepTrace{
			ID:        id,
			Layer:     layerIdx,
			Od:        step.Od,
			DependsOn: deps,
			IsOutput:  id == plan.Output,
		}
		recordTrace := func(st StepTrace) {
			st.DurationMs = time.Since(stepStart).Milliseconds()
			mu.Lock()
			traceByID[id] = st
			mu.Unlock()
		}

		// Propagate empty short-circuit from any upstream dependency.
		mu.Lock()
		upstreamEmpty := false
		for _, dep := range deps {
			if empty[dep] {
				upstreamEmpty = true
				break
			}
		}
		// Snapshot upstream results for substitution while holding the lock
		// (the previous layer fully completed, but reading the map without
		// the lock can still race with concurrent re-hash from this layer).
		snapshot := make(map[string][]map[string]interface{}, len(stepResults))
		for k, v := range stepResults {
			snapshot[k] = v
		}
		mu.Unlock()

		if upstreamEmpty {
			mu.Lock()
			empty[id] = true
			stepResults[id] = nil
			mu.Unlock()
			t := baseTrace
			t.Status = "empty_upstream"
			recordTrace(t)
			return nil
		}

		boundFilters := make([]smartquery.FilterItem, 0, len(step.Filters))
		shortCircuit := false
		for _, f := range step.Filters {
			sub, isEmpty, err := SubstituteFilter(f, params, snapshot)
			if err != nil {
				t := baseTrace
				t.Status = "failed"
				t.Error = err.Error()
				recordTrace(t)
				return fmt.Errorf("step %q: %w", id, err)
			}
			if isEmpty {
				shortCircuit = true
				break
			}
			boundFilters = append(boundFilters, smartquery.FilterItem{
				Prop:  sub.Prop,
				Op:    sub.Op,
				Value: sub.Value,
			})
		}
		if shortCircuit {
			mu.Lock()
			empty[id] = true
			stepResults[id] = nil
			mu.Unlock()
			t := baseTrace
			t.Status = "empty"
			recordTrace(t)
			return nil
		}

		groupBy := step.GroupBy
		if step.Metric == "" && len(groupBy) == 0 {
			groupBy = step.Select
		}
		spec := smartquery.QuerySpec{
			ProjectID: projectID,
			Objects:   collectStepObjects(step, boundFilters),
			Metric:    step.Metric,
			GroupBy:   groupBy,
			Filters:   boundFilters,
		}
		res := e.Runner(ctx, id, spec)
		if !res.ExecutionOK {
			t := baseTrace
			t.Status = "failed"
			t.Error = res.ErrorMessage
			t.SQL = res.SQL
			t.OntologySQL = res.OntologySQL
			recordTrace(t)
			return &stepFailure{stepID: id, result: res}
		}
		rows, err := decodeResultRows(res.ResultJSON)
		if err != nil {
			t := baseTrace
			t.Status = "failed"
			t.Error = err.Error()
			t.SQL = res.SQL
			recordTrace(t)
			return fmt.Errorf("step %q: decode result rows: %w", id, err)
		}
		mu.Lock()
		stepResults[id] = rows
		mu.Unlock()

		t := baseTrace
		t.Status = "success"
		t.RowCount = len(rows)
		t.SQL = res.SQL
		t.OntologySQL = res.OntologySQL
		recordTrace(t)
		return nil
	}

	buildTrace := func() *PlanTrace {
		out := &PlanTrace{Output: plan.Output, Steps: make([]StepTrace, 0, len(plan.Steps))}
		for layerIdx, layer := range levels {
			for _, id := range layer {
				if st, ok := traceByID[id]; ok {
					out.Steps = append(out.Steps, st)
				} else {
					// Defensive: a step that was never reached (e.g. an early
					// failure short-circuited the plan) still appears in the
					// trace as "pending" so the frontend can show what didn't run.
					out.Steps = append(out.Steps, StepTrace{
						ID:        id,
						Layer:     layerIdx,
						Od:        stepByID[id].Od,
						Status:    "pending",
						DependsOn: stepByID[id].stepDependencies(),
						IsOutput:  id == plan.Output,
					})
				}
			}
		}
		return out
	}

	for layerIdx, layer := range levels {
		// Single-step layer: skip the goroutine overhead. Functionally
		// identical to the parallel path.
		if len(layer) == 1 {
			if err := runStep(layer[0], layerIdx); err != nil {
				return failureResultWithTrace(err, buildTrace())
			}
			continue
		}
		var (
			wg       sync.WaitGroup
			errOnce  sync.Once
			layerErr error
		)
		for _, id := range layer {
			id := id
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := runStep(id, layerIdx); err != nil {
					errOnce.Do(func() { layerErr = err })
				}
			}()
		}
		wg.Wait()
		if layerErr != nil {
			return failureResultWithTrace(layerErr, buildTrace())
		}
	}

	trace := buildTrace()

	// Final output.
	if empty[plan.Output] {
		return LakehouseResult{ExecutionOK: true, ResultJSON: "[]", PlanTrace: trace}, nil
	}
	rows := stepResults[plan.Output]
	out, err := json.Marshal(rows)
	if err != nil {
		return LakehouseResult{PlanTrace: trace}, fmt.Errorf("encode output: %w", err)
	}
	if string(out) == "null" {
		out = []byte("[]")
	}
	return LakehouseResult{ExecutionOK: true, ResultJSON: string(out), PlanTrace: trace}, nil
}

// stepFailure is a typed error returned when a step's Runner reports
// ExecutionOK=false. failureResult unwraps it so the caller still gets the
// upstream LakehouseResult (with errorMessage / SQL / debug info), matching
// the v1 contract.
type stepFailure struct {
	stepID string
	result LakehouseResult
}

func (e *stepFailure) Error() string {
	return fmt.Sprintf("step %q failed: %s", e.stepID, e.result.ErrorMessage)
}

func failureResult(err error) (LakehouseResult, error) {
	return failureResultWithTrace(err, nil)
}

func failureResultWithTrace(err error, trace *PlanTrace) (LakehouseResult, error) {
	var sf *stepFailure
	if errAs(err, &sf) {
		r := sf.result
		if trace != nil {
			r.PlanTrace = trace
		}
		return r, err
	}
	return LakehouseResult{PlanTrace: trace}, err
}

// errAs is a tiny local shim for errors.As to keep imports minimal.
func errAs(err error, target **stepFailure) bool {
	for err != nil {
		if sf, ok := err.(*stepFailure); ok {
			*target = sf
			return true
		}
		type wrapped interface{ Unwrap() error }
		if w, ok := err.(wrapped); ok {
			err = w.Unwrap()
			continue
		}
		return false
	}
	return false
}

// collectStepObjects builds the Objects list for a step's QuerySpec. step.Od
// is always first; any additional ODs implied by `Od.prop` references in the
// metric, groupBy, or filter prop strings are appended deduped. This mirrors
// the agent-server's ensureObjectsCoverReferencedProps guard so multi-OD
// plan steps trigger ResolveJoinPath instead of dying with "Property not
// found on Od".
func collectStepObjects(step PlanStep, boundFilters []smartquery.FilterItem) []string {
	primary := step.Od
	seen := map[string]bool{strings.ToLower(primary): true}
	out := []string{primary}
	addFromDotted := func(s string) {
		for _, m := range dottedPropPattern.FindAllStringSubmatch(s, -1) {
			od := m[1]
			k := strings.ToLower(od)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, od)
		}
	}
	addFromDotted(step.Metric)
	for _, g := range step.GroupBy {
		addFromDotted(g)
	}
	for _, f := range boundFilters {
		addFromDotted(f.Prop)
	}
	return out
}

func decodeResultRows(resultJSON string) ([]map[string]interface{}, error) {
	if resultJSON == "" || resultJSON == "null" {
		return nil, nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(resultJSON), &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
