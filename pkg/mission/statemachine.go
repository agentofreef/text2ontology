package mission

import "fmt"

// validTransitions is the task status state machine. Terminal statuses
// (passing, blocked) have no outgoing edges. See spec §3.5.
//
//	pending        -> active
//	active         -> passing | pending_retry | blocked
//	pending_retry  -> active
var validTransitions = map[TaskStatus]map[TaskStatus]bool{
	TaskPending:      {TaskActive: true},
	TaskActive:       {TaskPassing: true, TaskPendingRetry: true, TaskBlocked: true},
	TaskPendingRetry: {TaskActive: true},
	TaskPassing:      {},
	TaskBlocked:      {},
}

// CanTransition reports whether a task may move from `from` to `to`.
func CanTransition(from, to TaskStatus) bool {
	return validTransitions[from][to]
}

// IsTerminal reports whether a task status admits no further change.
func IsTerminal(s TaskStatus) bool {
	return s == TaskPassing || s == TaskBlocked
}

// ErrInvalidTransition is returned when a status change is illegal.
// The mutator surfaces this to the LLM so it can retry — the LLM may
// request transitions but never force one.
type ErrInvalidTransition struct {
	From, To TaskStatus
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid task transition %q -> %q", e.From, e.To)
}

// Transition validates and applies a status change to a task. The
// state machine is enforced here, never by the LLM (spec §3.3 / §10.2).
func (t *Task) Transition(to TaskStatus) error {
	if !CanTransition(t.Status, to) {
		return ErrInvalidTransition{From: t.Status, To: to}
	}
	t.Status = to
	return nil
}

// DeriveStatus computes the mission's top-level status from its tasks.
// This is the single place a mission outcome is decided — it is never
// set directly. A root-level capability gap (blockedRoot) makes the
// whole mission unanswerable before any task runs.
func DeriveStatus(tasks []Task, blockedRoot *BlockedReason) MissionStatus {
	if blockedRoot != nil {
		return MissionUnanswerable
	}
	if len(tasks) == 0 {
		return MissionActive
	}
	allTerminal, anyBlocked := true, false
	for _, t := range tasks {
		if !IsTerminal(t.Status) {
			allTerminal = false
		}
		if t.Status == TaskBlocked {
			anyBlocked = true
		}
	}
	switch {
	case !allTerminal:
		return MissionActive
	case anyBlocked:
		return MissionPartial
	default:
		return MissionComplete
	}
}
