package handler

// mission_shadow.go — M1 of the MissionAct architecture (.omc/specs/mission-act.md §9).
//
// "Shadow mission": when the USE_MISSION_ACT feature flag is on, every
// lakehouse agent turn creates, populates and persists one ont_mission row
// that runs *alongside* the existing flow. It does NOT drive control flow —
// it is a best-effort observation record. M2 makes the mission actually
// steer the loop.
//
// Hard invariants (spec §9 M1 exit criteria):
//   - Flag OFF  ⇒ byte-identical behavior. None of this code executes.
//   - Purely additive: no prompt / control-flow / frontend changes.
//   - Save/Load failures NEVER fail the turn — log and continue.
//
// All integration glue lives here; handler_agent_lakehouse.go only has the
// four minimal hook calls (turn-start, per-step-result, turn-end, and the
// ont_agent_step.mission_id wiring).

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/lakehouse2ontology/mission"
)

// missionActEnabled is read once at package init from the USE_MISSION_ACT
// env var. Default false ⇒ zero behavior change. Any value other than the
// truthy set below leaves the shadow path entirely dormant.
var missionActEnabled = func() bool {
	switch os.Getenv("USE_MISSION_ACT") {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}()

// shadowMission is the per-turn handle threaded through the lakehouse agent
// loop. A nil *shadowMission is fully inert — every method is a no-op — so
// the call sites need no flag checks of their own.
type shadowMission struct {
	store *mission.Store
	m     *mission.Mission
}

// newShadowMission is the turn-start hook. When the flag is off (or no DB),
// it returns nil so all later hook calls are no-ops. When on, it builds a
// fresh mission.Mission and persists it best-effort.
func newShadowMission(ctx context.Context, db *sql.DB, threadID, projectID, question string) *shadowMission {
	if !missionActEnabled || db == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m := &mission.Mission{
		MissionID:   uuid.NewString(),
		ThreadID:    threadID,
		ProjectID:   projectID,
		Question:    question,
		StepResults: map[string]mission.StepResult{},
		Status:      mission.MissionActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	sm := &shadowMission{store: mission.NewStore(db), m: m}
	if err := sm.store.Save(ctx, m); err != nil {
		// Best-effort: a DB hiccup must never fail a user query.
		log.Printf("MISSION-ACT: shadow mission create failed (thread %s): %v", threadID, err)
	}
	return sm
}

// nullableMissionID yields a value suitable for the ont_agent_step.mission_id
// UUID column: the mission's id string when the shadow path is live, or an
// untyped nil so the column is written as SQL NULL (flag off ⇒ old behavior).
func nullableMissionID(sm *shadowMission) any {
	if sm == nil || sm.m == nil || sm.m.MissionID == "" {
		return nil
	}
	return sm.m.MissionID
}

// recordStep is the per-step-result hook. It mirrors a tagged smartquery /
// compose_query result into the mission's StepResults map under the same
// step id (e.g. "t1") that tagDataStep assigned. The rows are parsed from
// the result's execution_result JSON string — the same table the step
// already carries. Inert when the shadow path is off.
func (sm *shadowMission) recordStep(result map[string]any) {
	if sm == nil || sm.m == nil || result == nil {
		return
	}
	stepID, _ := result["step_id"].(string)
	if stepID == "" {
		return
	}
	rows := parseExecutionRows(result)
	sm.m.StepResults[stepID] = mission.StepResult{Rows: rows}
}

// finish is the turn-end hook. It records the final assistant answer into
// synthesis.output, marks the mission complete and persists it again.
// Best-effort: a save failure is logged, never propagated.
func (sm *shadowMission) finish(ctx context.Context, finalAnswer string) {
	if sm == nil || sm.m == nil {
		return
	}
	sm.m.Synthesis.Output = finalAnswer
	sm.m.Status = mission.MissionComplete
	sm.m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := sm.store.Save(ctx, sm.m); err != nil {
		log.Printf("MISSION-ACT: shadow mission finalize failed (mission %s): %v", sm.m.MissionID, err)
	}
}

// parseExecutionRows extracts the row table from a smartquery / compose_query
// result M. execution_result is a JSON-encoded []map[string]any (see
// tool_compose_query.go / lakehouse_tools.go). Returns nil on any parse miss
// — the shadow record is best-effort.
func parseExecutionRows(result map[string]any) []map[string]any {
	raw, _ := result["execution_result"].(string)
	if raw == "" || raw == "null" {
		return nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	return rows
}
