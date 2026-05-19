package mission

import "testing"

// The store relies on the mission JSON round-tripping losslessly: Save
// marshals the full Mission into ont_mission.state, Load decodes it
// back. The SQL plumbing is exercised in integration; this test guards
// the serialisation contract those round trips depend on.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := &Mission{
		MissionID:       "m-1",
		ThreadID:        "th-1",
		ParentMissionID: "m-0",
		ProjectID:       "proj-1",
		Question:        "各城市 2025 营收",
		Decomposition: []DecompItem{
			{ID: "d1", Kind: "metric", Name: "营收", Shape: "scalar"},
			{ID: "d2", Kind: "dimension", Name: "city", Shape: "group-by"},
		},
		Tasks: []Task{
			{
				ID: "task-1", Type: "smartquery", Status: TaskPassing,
				Behavior: "查 2025 各城市营收",
				Covers:   []string{"d1", "d2"},
				Dispatch: Dispatch{
					Tool: "smartquery",
					Args: map[string]any{"intent": "store_revenue", "params": map[string]any{"period_label": "2025"}},
				},
				ResultRef: "t1",
				Evidence:  &Evidence{Tool: "smartquery", ResultSummary: "5 rows"},
			},
		},
		StepResults: map[string]StepResult{
			"t1": {
				Rows:     []map[string]any{{"city": "上海", "Total_amount": 1164026.28}},
				FromTask: "task-1",
				Schema:   []string{"city", "Total_amount"},
			},
		},
		Synthesis: Synthesis{Output: "上海 1,164,026.28 元", Caveats: []string{"仅 2025"}},
		Status:    MissionComplete,
	}

	data, err := EncodeMission(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeMission(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.MissionID != original.MissionID ||
		got.ParentMissionID != original.ParentMissionID ||
		got.Status != original.Status ||
		got.Synthesis.Output != original.Synthesis.Output {
		t.Fatalf("scalar mismatch after round trip: %+v", got)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Status != TaskPassing || got.Tasks[0].ResultRef != "t1" {
		t.Fatalf("tasks mismatch: %+v", got.Tasks)
	}
	if got.Tasks[0].Evidence == nil || got.Tasks[0].Evidence.Tool != "smartquery" {
		t.Fatalf("evidence lost: %+v", got.Tasks[0].Evidence)
	}
	if got.Tasks[0].Dispatch.Tool != "smartquery" {
		t.Fatalf("dispatch lost: %+v", got.Tasks[0].Dispatch)
	}
	if got.Tasks[0].Dispatch.Args["intent"] != "store_revenue" {
		t.Fatalf("dispatch.args lost: %+v", got.Tasks[0].Dispatch.Args)
	}
	if sr, ok := got.StepResults["t1"]; !ok || sr.FromTask != "task-1" || len(sr.Rows) != 1 {
		t.Fatalf("step results mismatch: %+v", got.StepResults)
	}
	if got.StepResults["t1"].Rows[0]["city"] != "上海" {
		t.Fatalf("row data mismatch: %+v", got.StepResults["t1"].Rows[0])
	}
}

// An empty mission round-trips to its zero value (defensive: nothing
// blows up on a freshly inserted row before the agent has done work).
func TestEncodeDecodeEmpty(t *testing.T) {
	data, err := EncodeMission(&Mission{MissionID: "m-empty", Status: MissionActive})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMission(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.MissionID != "m-empty" || got.Status != MissionActive {
		t.Fatalf("empty mission round trip: %+v", got)
	}
	if len(got.Tasks) != 0 {
		t.Fatalf("tasks should be empty, got %v", got.Tasks)
	}
}
