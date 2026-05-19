package mission

import "fmt"

// MutationKind enumerates the state changes the LLM may request. The
// LLM proposes a Mutation; the mutator validates and applies it — the
// LLM never edits mission state directly (spec §3.3, §10.2).
type MutationKind string

const (
	MutateStartTask    MutationKind = "start_task"
	MutateCompleteTask MutationKind = "complete_task"
	MutateBlockTask    MutationKind = "block_task"
	MutateRetryTask    MutationKind = "retry_task"
	MutateSynthesize   MutationKind = "synthesize"
)

// Mutation is one requested change to a mission.
type Mutation struct {
	Kind   MutationKind
	TaskID string // task-scoped mutations

	// complete_task: the result table to register under StepID, plus
	// how the task is evidenced.
	StepID     string
	StepResult *StepResult
	Evidence   *Evidence

	// block_task: the proof of why the task cannot complete.
	Blocked *BlockedReason

	// synthesize: the final answer text.
	Output string
}

// Mutate validates a requested change against the task state machine
// and the WIP=1 invariant, applies it, and recomputes mission status.
// On any rejection the mission is left untouched.
func Mutate(m *Mission, mut Mutation) error {
	switch mut.Kind {
	case MutateStartTask:
		return applyStart(m, mut.TaskID)
	case MutateCompleteTask:
		return applyComplete(m, mut)
	case MutateBlockTask:
		return applyBlock(m, mut)
	case MutateRetryTask:
		return applyRetry(m, mut.TaskID)
	case MutateSynthesize:
		m.Synthesis.Output = mut.Output
		recomputeStatus(m)
		return nil
	default:
		return fmt.Errorf("unknown mutation kind %q", mut.Kind)
	}
}

func findTask(m *Mission, id string) (*Task, error) {
	for i := range m.Tasks {
		if m.Tasks[i].ID == id {
			return &m.Tasks[i], nil
		}
	}
	return nil, fmt.Errorf("task %q not found", id)
}

func recomputeStatus(m *Mission) {
	m.Status = DeriveStatus(m.Tasks, m.BlockedRoot)
}

// applyStart moves a task to active, enforcing WIP=1 — at most one task
// may be active at a time (spec §10.7).
func applyStart(m *Mission, taskID string) error {
	for i := range m.Tasks {
		if m.Tasks[i].Status == TaskActive && m.Tasks[i].ID != taskID {
			return fmt.Errorf("WIP=1 violated: task %q is already active", m.Tasks[i].ID)
		}
	}
	t, err := findTask(m, taskID)
	if err != nil {
		return err
	}
	if err := t.Transition(TaskActive); err != nil {
		return err
	}
	m.Cursor = taskID
	recomputeStatus(m)
	return nil
}

func applyComplete(m *Mission, mut Mutation) error {
	t, err := findTask(m, mut.TaskID)
	if err != nil {
		return err
	}
	if err := t.Transition(TaskPassing); err != nil {
		return err
	}
	if mut.StepResult != nil && mut.StepID != "" {
		if m.StepResults == nil {
			m.StepResults = map[string]StepResult{}
		}
		sr := *mut.StepResult
		sr.FromTask = t.ID
		m.StepResults[mut.StepID] = sr
		t.ResultRef = mut.StepID
	}
	if mut.Evidence != nil {
		t.Evidence = mut.Evidence
	}
	if m.Cursor == t.ID {
		m.Cursor = ""
	}
	recomputeStatus(m)
	return nil
}

func applyBlock(m *Mission, mut Mutation) error {
	if mut.Blocked == nil {
		return fmt.Errorf("block_task requires a blocked_reason")
	}
	t, err := findTask(m, mut.TaskID)
	if err != nil {
		return err
	}
	if err := t.Transition(TaskBlocked); err != nil {
		return err
	}
	t.BlockedReason = mut.Blocked
	if m.Cursor == t.ID {
		m.Cursor = ""
	}
	recomputeStatus(m)
	return nil
}

func applyRetry(m *Mission, taskID string) error {
	t, err := findTask(m, taskID)
	if err != nil {
		return err
	}
	if t.RetryBudget <= 0 {
		return fmt.Errorf("task %q has no retry budget left", taskID)
	}
	if err := t.Transition(TaskPendingRetry); err != nil {
		return err
	}
	t.RetryBudget--
	if m.Cursor == t.ID {
		m.Cursor = ""
	}
	recomputeStatus(m)
	return nil
}

// GateDispatch enforces the pointer invariant on a dispatch's args: any
// literal copied out of a prior step result is rejected and must be
// rewritten as a reference. exempt approves question-origin literals
// (spec §0.5). Every dispatch passes this gate before its task runs.
func GateDispatch(d Dispatch, steps map[string]StepResult, exempt Exempt) error {
	if len(d.Args) == 0 {
		return nil
	}
	if v, bad := ScanForLiteral(d.Args, steps, exempt); bad {
		return v
	}
	return nil
}
