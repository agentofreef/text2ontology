package mission

import "testing"

func TestQuestionExempt(t *testing.T) {
	if QuestionExempt("") != nil {
		t.Fatal("empty question should yield nil exempt")
	}
	ex := QuestionExempt("各城市 2025 上海营收")
	for _, s := range []string{"上海", "2025", "营收", "各城市"} {
		if !ex(s) {
			t.Errorf("%q should be exempt (substring of question)", s)
		}
	}
	for _, s := range []string{"", "深圳", "2026"} {
		if ex(s) {
			t.Errorf("%q should NOT be exempt", s)
		}
	}
}

func TestTrivialTasks(t *testing.T) {
	args := map[string]any{"intent": "store_revenue", "params": map[string]any{"city": "上海"}}
	tasks := TrivialTasks("task-1", args)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	tk := tasks[0]
	if tk.ID != "task-1" || tk.Type != "smartquery" || tk.Status != TaskPending {
		t.Fatalf("unexpected task shape: %+v", tk)
	}
	if tk.Dispatch.Tool != "smartquery" {
		t.Fatalf("dispatch tool: %q", tk.Dispatch.Tool)
	}
	if tk.RetryBudget != 2 {
		t.Fatalf("retry budget: %d", tk.RetryBudget)
	}
}

func TestNewTrivialMission(t *testing.T) {
	m := NewTrivialMission(TrivialConfig{
		MissionID: "m-1", ThreadID: "th-1", ProjectID: "p-1",
		Question: "上海2025营收", TaskID: "task-1",
		Args: map[string]any{"intent": "store_revenue"},
	})
	if m == nil {
		t.Fatal("nil mission")
	}
	if m.MissionID != "m-1" || m.Question != "上海2025营收" {
		t.Fatalf("scalar fields wrong: %+v", m)
	}
	if len(m.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(m.Tasks))
	}
	// A trivial mission with one pending task is still "active" — it
	// has not been dispatched yet. The status only flips to complete
	// once the task is started and passes.
	if m.Status != MissionActive {
		t.Fatalf("trivial mission should start active, got %s", m.Status)
	}
}

// A trivial mission's first dispatch carries question-origin literals
// (city="上海" from the user's words). With QuestionExempt as the
// gate's exempt, that dispatch passes — there are no step results yet,
// and the user's own words are not "copies" of anything.
func TestTrivialDispatchPassesQuestionExempt(t *testing.T) {
	m := NewTrivialMission(TrivialConfig{
		MissionID: "m-1", Question: "上海2025年营收", TaskID: "task-1",
		Args: map[string]any{"params": map[string]any{"city": "上海"}},
	})
	ex := QuestionExempt(m.Question)
	if err := GateDispatch(m.Tasks[0].Dispatch, m.StepResults, ex); err != nil {
		t.Fatalf("first dispatch should pass the gate: %v", err)
	}

	// Once a step result also contains "上海", the question exempt still
	// approves it on this turn — first-turn dispatch is the user's
	// quote, not a step copy. The gate's job is to catch redundant
	// copies on later turns (a wider integration test territory).
	m.StepResults = map[string]StepResult{
		"t1": {Rows: []map[string]any{{"city": "上海"}}},
	}
	if err := GateDispatch(m.Tasks[0].Dispatch, m.StepResults, ex); err != nil {
		t.Fatalf("question-exempt literal must pass even when t1 contains it: %v", err)
	}

	// Without the exempt, the same dispatch is flagged — the gate is
	// real, the exempt is doing the work.
	if err := GateDispatch(m.Tasks[0].Dispatch, m.StepResults, nil); err == nil {
		t.Fatal("without exempt, copy of a step cell must be flagged")
	}
}
