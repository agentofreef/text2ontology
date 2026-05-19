package mission

import (
	"errors"
	"testing"
)

func newMission(tasks ...Task) *Mission {
	return &Mission{MissionID: "m1", Tasks: tasks, Status: MissionActive}
}

// Happy path: pending -> start -> complete; mission ends in complete.
func TestMutateHappyPath(t *testing.T) {
	m := newMission(Task{ID: "t-1", Status: TaskPending})
	if err := Mutate(m, Mutation{Kind: MutateStartTask, TaskID: "t-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if m.Tasks[0].Status != TaskActive {
		t.Fatalf("after start status=%s", m.Tasks[0].Status)
	}
	if m.Cursor != "t-1" {
		t.Fatalf("cursor not set, got %q", m.Cursor)
	}
	if m.Status != MissionActive {
		t.Fatalf("mission status got %s", m.Status)
	}
	rows := []map[string]any{{"city": "上海"}}
	err := Mutate(m, Mutation{
		Kind: MutateCompleteTask, TaskID: "t-1",
		StepID: "t1", StepResult: &StepResult{Rows: rows},
		Evidence: &Evidence{Tool: "smartquery", ResultSummary: "1 row"},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if m.Tasks[0].Status != TaskPassing {
		t.Fatalf("after complete status=%s", m.Tasks[0].Status)
	}
	if m.Tasks[0].ResultRef != "t1" {
		t.Fatalf("ResultRef not set, got %q", m.Tasks[0].ResultRef)
	}
	if sr, ok := m.StepResults["t1"]; !ok || sr.FromTask != "t-1" {
		t.Fatalf("step result not registered: %+v", m.StepResults)
	}
	if m.Cursor != "" {
		t.Fatalf("cursor should clear, got %q", m.Cursor)
	}
	if m.Status != MissionComplete {
		t.Fatalf("mission should be complete, got %s", m.Status)
	}
}

// WIP=1: starting a second task while one is active is rejected, and
// the second task stays pending.
func TestMutateWIP1Enforced(t *testing.T) {
	m := newMission(
		Task{ID: "a", Status: TaskPending},
		Task{ID: "b", Status: TaskPending},
	)
	if err := Mutate(m, Mutation{Kind: MutateStartTask, TaskID: "a"}); err != nil {
		t.Fatal(err)
	}
	err := Mutate(m, Mutation{Kind: MutateStartTask, TaskID: "b"})
	if err == nil {
		t.Fatal("starting a second task should be rejected (WIP=1)")
	}
	if m.Tasks[1].Status != TaskPending {
		t.Fatalf("rejected start mutated status: %s", m.Tasks[1].Status)
	}
}

// An illegal transition (pending -> passing without going through
// active) is rejected by the state machine.
func TestMutateIllegalTransitionRejected(t *testing.T) {
	m := newMission(Task{ID: "t-1", Status: TaskPending})
	err := Mutate(m, Mutation{Kind: MutateCompleteTask, TaskID: "t-1"})
	if err == nil {
		t.Fatal("complete on a pending task should be rejected")
	}
	var bad ErrInvalidTransition
	if !errors.As(err, &bad) {
		t.Fatalf("expected ErrInvalidTransition, got %T: %v", err, err)
	}
	if m.Tasks[0].Status != TaskPending {
		t.Fatalf("rejected mutation mutated status: %s", m.Tasks[0].Status)
	}
}

// Blocking without a reason is rejected — the proof is the point.
func TestMutateBlockRequiresReason(t *testing.T) {
	m := newMission(Task{ID: "t-1", Status: TaskActive})
	if err := Mutate(m, Mutation{Kind: MutateBlockTask, TaskID: "t-1"}); err == nil {
		t.Fatal("block_task without blocked_reason should be rejected")
	}
	if m.Tasks[0].Status != TaskActive {
		t.Fatalf("status changed despite rejection: %s", m.Tasks[0].Status)
	}
	br := &BlockedReason{Kind: GapNoParam, MissingDimension: "year"}
	if err := Mutate(m, Mutation{Kind: MutateBlockTask, TaskID: "t-1", Blocked: br}); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}
	if m.Tasks[0].Status != TaskBlocked || m.Tasks[0].BlockedReason == nil {
		t.Fatalf("block did not take effect: %+v", m.Tasks[0])
	}
	if m.Status != MissionPartial {
		t.Fatalf("mission status after single-task block: %s", m.Status)
	}
}

// Retry consumes budget; once exhausted, retry is rejected (caller is
// expected to block instead).
func TestMutateRetryBudget(t *testing.T) {
	m := newMission(Task{ID: "t-1", Status: TaskActive, RetryBudget: 1})
	if err := Mutate(m, Mutation{Kind: MutateRetryTask, TaskID: "t-1"}); err != nil {
		t.Fatalf("first retry: %v", err)
	}
	if m.Tasks[0].Status != TaskPendingRetry {
		t.Fatalf("status after retry: %s", m.Tasks[0].Status)
	}
	if m.Tasks[0].RetryBudget != 0 {
		t.Fatalf("budget not decremented: %d", m.Tasks[0].RetryBudget)
	}
	// Re-activate so the task is in `active` again before exhausting the budget.
	if err := Mutate(m, Mutation{Kind: MutateStartTask, TaskID: "t-1"}); err != nil {
		t.Fatalf("re-activate: %v", err)
	}
	err := Mutate(m, Mutation{Kind: MutateRetryTask, TaskID: "t-1"})
	if err == nil {
		t.Fatal("retry with 0 budget should be rejected")
	}
}

func TestMutateUnknownKind(t *testing.T) {
	m := newMission(Task{ID: "t-1", Status: TaskActive})
	if err := Mutate(m, Mutation{Kind: "no_such_kind", TaskID: "t-1"}); err == nil {
		t.Fatal("unknown mutation kind should be rejected")
	}
}

func TestMutateSynthesizeMarksComplete(t *testing.T) {
	m := newMission(Task{ID: "t-1", Status: TaskPassing})
	recomputeStatus(m) // simulate prior recompute
	if err := Mutate(m, Mutation{Kind: MutateSynthesize, Output: "final"}); err != nil {
		t.Fatal(err)
	}
	if m.Synthesis.Output != "final" {
		t.Fatalf("output not set: %q", m.Synthesis.Output)
	}
	if m.Status != MissionComplete {
		t.Fatalf("status: %s", m.Status)
	}
}

// GateDispatch is the pointer-invariant gate every dispatch passes.
func TestGateDispatch(t *testing.T) {
	steps := map[string]StepResult{
		"t1": {Rows: []map[string]any{{"city": "上海"}, {"city": "北京"}}},
	}

	// A literal copied out of a step result is rejected with a Violation.
	bad := Dispatch{Tool: "smartquery", Args: map[string]any{"city": "上海"}}
	err := GateDispatch(bad, steps, nil)
	if err == nil {
		t.Fatal("dispatch with copied literal should be rejected")
	}
	var v Violation
	if !errors.As(err, &v) || v.ShouldBe != "t1.city[0]" {
		t.Fatalf("expected pointer violation pointing at t1.city[0], got: %v", err)
	}

	// A reference value passes.
	ok := Dispatch{Tool: "smartquery", Args: map[string]any{"city": "t1.city[0]"}}
	if err := GateDispatch(ok, steps, nil); err != nil {
		t.Fatalf("reference value should pass: %v", err)
	}

	// A question-origin literal is exempt.
	if err := GateDispatch(bad, steps, map[string]bool{"上海": true}); err != nil {
		t.Fatalf("exempt literal should pass: %v", err)
	}

	// Empty args are a no-op.
	if err := GateDispatch(Dispatch{Tool: "x"}, steps, nil); err != nil {
		t.Fatalf("empty args: %v", err)
	}
}
