package mission

import "testing"

func TestCanTransition(t *testing.T) {
	legal := []struct{ from, to TaskStatus }{
		{TaskPending, TaskActive},
		{TaskActive, TaskPassing},
		{TaskActive, TaskPendingRetry},
		{TaskActive, TaskBlocked},
		{TaskPendingRetry, TaskActive},
	}
	for _, c := range legal {
		if !CanTransition(c.from, c.to) {
			t.Errorf("expected %s -> %s to be legal", c.from, c.to)
		}
	}
	illegal := []struct{ from, to TaskStatus }{
		{TaskPending, TaskPassing}, // skips active
		{TaskPending, TaskBlocked},
		{TaskActive, TaskPending},
		{TaskPassing, TaskActive},  // terminal
		{TaskBlocked, TaskActive},  // terminal
		{TaskPassing, TaskBlocked}, // terminal
	}
	for _, c := range illegal {
		if CanTransition(c.from, c.to) {
			t.Errorf("expected %s -> %s to be illegal", c.from, c.to)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	for _, s := range []TaskStatus{TaskPassing, TaskBlocked} {
		if !IsTerminal(s) {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []TaskStatus{TaskPending, TaskActive, TaskPendingRetry} {
		if IsTerminal(s) {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

// T6: an illegal transition is rejected and leaves status untouched.
func TestTaskTransition(t *testing.T) {
	task := &Task{ID: "task-1", Status: TaskPending}
	if err := task.Transition(TaskActive); err != nil {
		t.Fatalf("pending->active should succeed: %v", err)
	}
	if task.Status != TaskActive {
		t.Fatalf("status not updated, got %s", task.Status)
	}

	bad := &Task{ID: "task-2", Status: TaskPending}
	err := bad.Transition(TaskPassing)
	if err == nil {
		t.Fatal("pending->passing should be rejected")
	}
	if _, ok := err.(ErrInvalidTransition); !ok {
		t.Fatalf("expected ErrInvalidTransition, got %T", err)
	}
	if bad.Status != TaskPending {
		t.Fatalf("a rejected transition must not mutate status, got %s", bad.Status)
	}
}

func TestDeriveStatus(t *testing.T) {
	cases := []struct {
		name        string
		tasks       []Task
		blockedRoot *BlockedReason
		want        MissionStatus
	}{
		{"root gap -> unanswerable", nil, &BlockedReason{Kind: GapNoParam}, MissionUnanswerable},
		{"no tasks -> active", nil, nil, MissionActive},
		{"one active -> active", []Task{{Status: TaskActive}}, nil, MissionActive},
		{"pending present -> active", []Task{{Status: TaskPassing}, {Status: TaskPending}}, nil, MissionActive},
		{"all passing -> complete", []Task{{Status: TaskPassing}, {Status: TaskPassing}}, nil, MissionComplete},
		{"passing+blocked -> partial", []Task{{Status: TaskPassing}, {Status: TaskBlocked}}, nil, MissionPartial},
		{"all blocked -> partial", []Task{{Status: TaskBlocked}}, nil, MissionPartial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveStatus(c.tasks, c.blockedRoot); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}
