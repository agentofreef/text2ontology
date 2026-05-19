package mission

import "strings"

// QuestionExempt returns an Exempt that approves any literal appearing
// verbatim as a substring of the question. First-turn dispatch args
// (city / period_label / etc.) that the LLM bound from the question
// pass through — those are quotes of the user, not copies of a step
// result. Once the same literal later originates from a step the gate
// still catches a redundant copy on subsequent turns.
//
// An empty question returns nil — no exemption needed when there is
// nothing to exempt against.
func QuestionExempt(q string) Exempt {
	if q == "" {
		return nil
	}
	return func(s string) bool {
		return s != "" && strings.Contains(q, s)
	}
}

// TrivialConfig holds the inputs to NewTrivialMission.
type TrivialConfig struct {
	MissionID string
	ThreadID  string
	ProjectID string
	Question  string
	TaskID    string
	// Args is the smartquery argument map (typically {"intent":"...",
	// "params":{...}}). The caller decides the exact shape; pkg/mission
	// is shape-agnostic.
	Args map[string]any
}

// NewTrivialMission builds a one-task mission that wraps a single
// smartquery call — the fast path for the common case (spec §4.3:
// trivial-mission zero-cost). Mission status is recomputed from the
// initial task list; cursor stays empty until the first start_task.
func NewTrivialMission(c TrivialConfig) *Mission {
	m := &Mission{
		MissionID: c.MissionID,
		ThreadID:  c.ThreadID,
		ProjectID: c.ProjectID,
		Question:  c.Question,
		Tasks:     TrivialTasks(c.TaskID, c.Args),
	}
	recomputeStatus(m)
	return m
}

// TrivialTasks returns the one-task list a trivial mission carries:
// a single pending smartquery dispatch with the default retry budget.
func TrivialTasks(taskID string, args map[string]any) []Task {
	return []Task{
		{
			ID:          taskID,
			Type:        "smartquery",
			Status:      TaskPending,
			Dispatch:    Dispatch{Tool: "smartquery", Args: args},
			RetryBudget: 2,
		},
	}
}
