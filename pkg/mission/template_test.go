package mission

import "testing"

func sampleTemplate() MissionTemplate {
	return MissionTemplate{
		ID:   "supply_disruption",
		Name: "供应中断影响测算",
		Tasks: []TemplateTask{
			{ID: "gross", Type: "smartquery", Behavior: "受冲击毛营收"},
			{ID: "subs", Type: "smartquery", Behavior: "可替代 SKU"},
			{ID: "synth", Type: "synthesize", Behavior: "综合", DependsOn: []string{"gross", "subs"}},
		},
		Synthesis: TemplateSynthesis{
			Template: "受冲击毛营收 {{.gross}}",
			Caveats:  []string{"毛营收不等于净损失"},
		},
	}
}

func TestInstantiate(t *testing.T) {
	m := sampleTemplate().Instantiate(TemplateConfig{
		MissionID: "m-1", ThreadID: "th-1", ProjectID: "p-1", Question: "燕麦奶断供影响",
	})
	if len(m.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(m.Tasks))
	}
	for _, tk := range m.Tasks {
		if tk.Status != TaskPending {
			t.Errorf("task %s should start pending, got %s", tk.ID, tk.Status)
		}
		if tk.RetryBudget != 2 {
			t.Errorf("task %s retry budget should default to 2, got %d", tk.ID, tk.RetryBudget)
		}
	}
	if m.Tasks[2].ID != "synth" || len(m.Tasks[2].DependsOn) != 2 {
		t.Errorf("synth task dependencies lost: %+v", m.Tasks[2])
	}
	if m.Synthesis.Template == "" || len(m.Synthesis.Caveats) != 1 {
		t.Errorf("synthesis recipe not carried: %+v", m.Synthesis)
	}
	if m.Status != MissionActive {
		t.Errorf("a fresh multi-task mission should be active, got %s", m.Status)
	}
	// The instantiated mission must be schedulable: PickActive yields a
	// task with no dependencies first.
	first := PickActive(m)
	if first == nil || (first.ID != "gross" && first.ID != "subs") {
		t.Errorf("first pick should be a dependency-free task, got %v", first)
	}
}

func TestInstantiateRetryBudgetOverride(t *testing.T) {
	tmpl := MissionTemplate{ID: "t", Tasks: []TemplateTask{
		{ID: "a", Type: "smartquery", RetryBudget: 5},
	}}
	m := tmpl.Instantiate(TemplateConfig{MissionID: "m"})
	if m.Tasks[0].RetryBudget != 5 {
		t.Errorf("explicit retry budget should be kept, got %d", m.Tasks[0].RetryBudget)
	}
}

func TestValidateOK(t *testing.T) {
	if err := sampleTemplate().Validate(); err != nil {
		t.Errorf("a well-formed template should validate: %v", err)
	}
}

func TestValidateDuplicateID(t *testing.T) {
	tmpl := MissionTemplate{ID: "t", Tasks: []TemplateTask{
		{ID: "a", Type: "smartquery"},
		{ID: "a", Type: "lookup"},
	}}
	if err := tmpl.Validate(); err == nil {
		t.Error("duplicate task id should fail validation")
	}
}

func TestValidateUnknownDependency(t *testing.T) {
	tmpl := MissionTemplate{ID: "t", Tasks: []TemplateTask{
		{ID: "a", Type: "smartquery", DependsOn: []string{"ghost"}},
	}}
	if err := tmpl.Validate(); err == nil {
		t.Error("a depends_on pointing at a non-existent task should fail")
	}
}

func TestValidateCycle(t *testing.T) {
	tmpl := MissionTemplate{ID: "t", Tasks: []TemplateTask{
		{ID: "a", Type: "smartquery", DependsOn: []string{"b"}},
		{ID: "b", Type: "smartquery", DependsOn: []string{"c"}},
		{ID: "c", Type: "smartquery", DependsOn: []string{"a"}},
	}}
	if err := tmpl.Validate(); err == nil {
		t.Error("a dependency cycle should fail validation")
	}
}

func TestValidateEmptyID(t *testing.T) {
	tmpl := MissionTemplate{ID: "t", Tasks: []TemplateTask{{Type: "smartquery"}}}
	if err := tmpl.Validate(); err == nil {
		t.Error("a task with an empty id should fail validation")
	}
}
