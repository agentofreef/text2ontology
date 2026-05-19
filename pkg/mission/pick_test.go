package mission

import "testing"

func TestPickActiveOrder(t *testing.T) {
	m := &Mission{Tasks: []Task{
		{ID: "a", Status: TaskPassing},
		{ID: "b", Status: TaskPending},
		{ID: "c", Status: TaskPending},
	}}
	got := PickActive(m)
	if got == nil || got.ID != "b" {
		t.Fatalf("expected first pending task b, got %v", got)
	}
}

func TestPickActiveWIP1(t *testing.T) {
	m := &Mission{Tasks: []Task{
		{ID: "a", Status: TaskActive},
		{ID: "b", Status: TaskPending},
	}}
	if got := PickActive(m); got != nil {
		t.Fatalf("WIP=1: nothing should be picked while a task is active, got %v", got)
	}
}

func TestPickActivePendingRetry(t *testing.T) {
	m := &Mission{Tasks: []Task{
		{ID: "a", Status: TaskPassing},
		{ID: "b", Status: TaskPendingRetry},
	}}
	got := PickActive(m)
	if got == nil || got.ID != "b" {
		t.Fatalf("pending_retry should be pickable, got %v", got)
	}
}

func TestPickActiveDependencies(t *testing.T) {
	// c depends on b; b is still pending, so c is not yet runnable —
	// b must be picked first.
	m := &Mission{Tasks: []Task{
		{ID: "b", Status: TaskPending},
		{ID: "c", Status: TaskPending, DependsOn: []string{"b"}},
	}}
	if got := PickActive(m); got == nil || got.ID != "b" {
		t.Fatalf("b should be picked before its dependent c, got %v", got)
	}

	// Once b passes, c becomes runnable.
	m.Tasks[0].Status = TaskPassing
	if got := PickActive(m); got == nil || got.ID != "c" {
		t.Fatalf("c should be picked once b passes, got %v", got)
	}
}

func TestPickActiveBlockedDependency(t *testing.T) {
	// c depends on b; b is blocked — c can never run.
	m := &Mission{Tasks: []Task{
		{ID: "b", Status: TaskBlocked},
		{ID: "c", Status: TaskPending, DependsOn: []string{"b"}},
	}}
	if got := PickActive(m); got != nil {
		t.Fatalf("c depends on a blocked task — nothing runnable, got %v", got)
	}
	if Runnable(m) {
		t.Fatal("mission with only a blocked-dependency task is not runnable")
	}
}

func TestRunnable(t *testing.T) {
	// An active task means the loop is still in flight.
	active := &Mission{Tasks: []Task{{ID: "a", Status: TaskActive}}}
	if !Runnable(active) {
		t.Error("a mission with an active task is runnable")
	}
	// All terminal — not runnable.
	done := &Mission{Tasks: []Task{
		{ID: "a", Status: TaskPassing},
		{ID: "b", Status: TaskBlocked},
	}}
	if Runnable(done) {
		t.Error("a mission with only terminal tasks is not runnable")
	}
	// A plain pending task is runnable.
	pending := &Mission{Tasks: []Task{{ID: "a", Status: TaskPending}}}
	if !Runnable(pending) {
		t.Error("a mission with a pending task is runnable")
	}
}

func TestPickActiveNilAndEmpty(t *testing.T) {
	if PickActive(nil) != nil {
		t.Error("nil mission picks nothing")
	}
	if PickActive(&Mission{}) != nil {
		t.Error("empty mission picks nothing")
	}
}
