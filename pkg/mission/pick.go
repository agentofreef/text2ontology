package mission

// PickActive is the WIP=1 scheduler (spec §3.2 main loop, §10.7). It
// returns the next task to activate: the first task that is pending or
// pending_retry and whose dependencies have all passed. It returns nil
// when a task is already active (WIP=1 — never pick a second), or when
// no task is runnable (the loop then terminates).
//
// Task order is significant: the mission's Tasks slice is the intended
// run order, and depends_on layers extra ordering on top.
func PickActive(m *Mission) *Task {
	if m == nil {
		return nil
	}
	for i := range m.Tasks {
		if m.Tasks[i].Status == TaskActive {
			return nil // WIP=1: one task already in flight
		}
	}
	for i := range m.Tasks {
		t := &m.Tasks[i]
		if t.Status != TaskPending && t.Status != TaskPendingRetry {
			continue
		}
		if dependenciesMet(m, t) {
			return t
		}
	}
	return nil
}

// dependenciesMet reports whether every task in t.DependsOn has reached
// the passing terminal state. A blocked dependency never satisfies a
// dependent — that dependent stays unrunnable, which is correct: you
// cannot build on a step that failed.
func dependenciesMet(m *Mission, t *Task) bool {
	for _, depID := range t.DependsOn {
		dep, err := findTask(m, depID)
		if err != nil || dep.Status != TaskPassing {
			return false
		}
	}
	return true
}

// Runnable reports whether the mission has any task that could still be
// picked now or later — i.e. a pending/pending_retry task whose
// dependencies are not permanently blocked. Used to tell "the loop is
// waiting" apart from "the loop is genuinely done".
func Runnable(m *Mission) bool {
	if m == nil {
		return false
	}
	for i := range m.Tasks {
		t := &m.Tasks[i]
		if t.Status == TaskActive {
			return true
		}
		if t.Status != TaskPending && t.Status != TaskPendingRetry {
			continue
		}
		if !dependencyPermanentlyBlocked(m, t) {
			return true
		}
	}
	return false
}

// dependencyPermanentlyBlocked reports whether any dependency of t is
// blocked — in which case t can never run.
func dependencyPermanentlyBlocked(m *Mission, t *Task) bool {
	for _, depID := range t.DependsOn {
		dep, err := findTask(m, depID)
		if err != nil || dep.Status == TaskBlocked {
			return true
		}
	}
	return false
}
