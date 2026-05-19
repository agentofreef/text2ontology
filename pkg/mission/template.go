package mission

import "fmt"

// Mission templates (spec §M3). A template is a pre-authored, named
// task-list shape. Both kinds of "plan" the project already has —
// analysis_pattern OK cards and composite-intent plan JSONB — are
// expressed as templates; instantiating one seeds a multi-task mission.
// A trivial mission (NewTrivialMission) is just the degenerate case:
// one task, no template needed.

// MissionTemplate is the authored shape a mission can be built from.
type MissionTemplate struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Tasks     []TemplateTask    `json:"tasks"`
	Synthesis TemplateSynthesis `json:"synthesis"`
}

// TemplateTask is one task slot in a template. It carries no runtime
// state (status / evidence / result) — Instantiate adds that.
type TemplateTask struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"` // smartquery | compose_query | lookup | sub_mission | ...
	Behavior     string   `json:"behavior"`
	Verification string   `json:"verification,omitempty"`
	Covers       []string `json:"covers,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	RetryBudget  int      `json:"retry_budget,omitempty"`
}

// TemplateSynthesis is the authored synthesis recipe: a render template
// plus caveats that must appear verbatim in the final answer.
type TemplateSynthesis struct {
	Template string   `json:"template,omitempty"`
	Caveats  []string `json:"caveats,omitempty"`
}

// TemplateConfig carries the per-instantiation identity fields.
type TemplateConfig struct {
	MissionID string
	ThreadID  string
	ProjectID string
	Question  string
}

// Instantiate produces a fresh multi-task mission from the template.
// Every task starts pending; an unset retry budget defaults to 2 (spec
// §9.6). Mission status is derived from the resulting task list.
func (mt MissionTemplate) Instantiate(c TemplateConfig) *Mission {
	tasks := make([]Task, 0, len(mt.Tasks))
	for _, tt := range mt.Tasks {
		rb := tt.RetryBudget
		if rb == 0 {
			rb = 2
		}
		tasks = append(tasks, Task{
			ID:           tt.ID,
			Type:         tt.Type,
			Behavior:     tt.Behavior,
			Verification: tt.Verification,
			Covers:       tt.Covers,
			DependsOn:    tt.DependsOn,
			Status:       TaskPending,
			RetryBudget:  rb,
		})
	}
	m := &Mission{
		MissionID: c.MissionID,
		ThreadID:  c.ThreadID,
		ProjectID: c.ProjectID,
		Question:  c.Question,
		Tasks:     tasks,
		Synthesis: Synthesis{Template: mt.Synthesis.Template, Caveats: mt.Synthesis.Caveats},
	}
	recomputeStatus(m)
	return m
}

// Validate checks the template is well-formed: task ids are present and
// unique, every depends_on resolves to a real task, and the dependency
// graph is acyclic. A malformed template must fail loudly at load time
// (spec §9.2 fail-fast) rather than produce a mission that deadlocks.
func (mt MissionTemplate) Validate() error {
	ids := make(map[string]bool, len(mt.Tasks))
	for _, t := range mt.Tasks {
		if t.ID == "" {
			return fmt.Errorf("template %q: a task has an empty id", mt.ID)
		}
		if ids[t.ID] {
			return fmt.Errorf("template %q: duplicate task id %q", mt.ID, t.ID)
		}
		ids[t.ID] = true
	}
	for _, t := range mt.Tasks {
		for _, dep := range t.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("template %q: task %q depends on unknown task %q", mt.ID, t.ID, dep)
			}
		}
	}
	if cyc := firstCycle(mt.Tasks); cyc != "" {
		return fmt.Errorf("template %q: dependency cycle through task %q", mt.ID, cyc)
	}
	return nil
}

// firstCycle returns the id of a task on a dependency cycle, or "" if
// the graph is acyclic. Standard DFS three-colour walk.
func firstCycle(tasks []TemplateTask) string {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(tasks))
	deps := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		deps[t.ID] = t.DependsOn
	}
	var visit func(id string) string
	visit = func(id string) string {
		color[id] = grey
		for _, dep := range deps[id] {
			switch color[dep] {
			case grey:
				return dep
			case white:
				if c := visit(dep); c != "" {
					return c
				}
			}
		}
		color[id] = black
		return ""
	}
	for _, t := range tasks {
		if color[t.ID] == white {
			if c := visit(t.ID); c != "" {
				return c
			}
		}
	}
	return ""
}
