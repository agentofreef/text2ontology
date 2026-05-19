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

// blockRoot marks the mission as unanswerable due to a root-level capability
// gap and persists it. Called by the declare_capability_gap tool handler on an
// accepted gap (M2). Best-effort: save failures are logged, never propagated.
func (sm *shadowMission) blockRoot(ctx context.Context, reason mission.BlockedReason) {
	if sm == nil || sm.m == nil {
		return
	}
	sm.m.BlockedRoot = &reason
	sm.m.Status = mission.MissionUnanswerable
	sm.m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := sm.store.Save(ctx, sm.m); err != nil {
		log.Printf("MISSION-ACT: shadow mission blockRoot failed (mission %s): %v", sm.m.MissionID, err)
	}
}

// writeCapabilityGapLog persists a row to capability_gap_log. Best-effort:
// a DB failure is logged, never propagated. Called after an accepted gap.
func writeCapabilityGapLog(ctx context.Context, db *sql.DB, missionID, projectID string, reason mission.BlockedReason) {
	if db == nil {
		return
	}
	evidenceJSON, err := json.Marshal(reason.CandidatesChecked)
	if err != nil {
		evidenceJSON = []byte("[]")
	}
	var firstIntent any
	if len(reason.CandidatesChecked) > 0 {
		firstIntent = reason.CandidatesChecked[0].IntentName
	}
	_, dbErr := db.ExecContext(ctx, `
		INSERT INTO capability_gap_log
			(id, mission_id, project_id, intent_name, missing_dimension,
			 gap_kind, suggested_fix, evidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`,
		uuid.NewString(), missionID, projectID, firstIntent,
		reason.MissingDimension, string(reason.Kind), reason.SuggestedFix,
		string(evidenceJSON))
	if dbErr != nil {
		log.Printf("MISSION-ACT: capability_gap_log write failed (mission %s): %v", missionID, dbErr)
	}
}

// ── M3: analysis-plan shadow capture ─────────────────────────────────────────

// seedTasksFromFeatures is the start_analysis_plan hook (M3). It converts the
// analysis.FeatureLedger snapshot into mission.Task entries and appends them
// to the shadow mission, then saves. No-op when shadow path is inert.
//
// features is the []analysis.FeatureRuntime snapshot from the ledger (via
// st.ledger.Snapshot()). Each feature becomes one pending task; DependsOn is
// left empty because AnalysisFeature v1 has no dependency field — the ledger
// itself drives run order.
func (sm *shadowMission) seedTasksFromFeatures(ctx context.Context, features []featureRuntimeView) {
	if sm == nil || sm.m == nil || len(features) == 0 {
		return
	}
	tasks := make([]mission.Task, 0, len(features))
	for _, f := range features {
		taskType := "smartquery" // default; ToolHints[0].Tool is advisory only
		if len(f.ToolHints) > 0 && f.ToolHints[0].Tool != "" {
			taskType = f.ToolHints[0].Tool
		}
		tasks = append(tasks, mission.Task{
			ID:           f.ID,
			Type:         taskType,
			Behavior:     f.Behavior,
			Verification: f.Verification,
			Status:       mission.TaskPending,
			RetryBudget:  2,
		})
	}
	sm.m.Tasks = tasks
	sm.m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := sm.store.Save(ctx, sm.m); err != nil {
		log.Printf("MISSION-ACT: seedTasksFromFeatures save failed (mission %s): %v", sm.m.MissionID, err)
	}
}

// recordVerifyFeature is the verify_feature hook (M3). It mirrors the ledger
// state transition into the shadow mission using mission.Mutate. The mutator
// enforces WIP=1 and state-machine legality; any rejection is logged and
// ignored — the turn is never affected. No-op when shadow path is inert.
//
// outcome is one of "passing" / "retry" / "blocked" (the "outcome" key in the
// runVerifyFeature result M). tool/summary/reasoning are evidence strings.
func (sm *shadowMission) recordVerifyFeature(ctx context.Context, featureID, outcome, tool, summary, reasoning string) {
	ev := mission.Evidence{Tool: tool, ResultSummary: summary, Reasoning: reasoning}
	if sm == nil || sm.m == nil || featureID == "" {
		return
	}
	m := sm.m

	// The mutator requires the task to be active before completing/blocking it.
	// Drive pending→active if needed (the ledger already did this; we mirror).
	taskActive := false
	for _, t := range m.Tasks {
		if t.ID == featureID && t.Status == mission.TaskActive {
			taskActive = true
			break
		}
	}
	if !taskActive {
		if err := mission.Mutate(m, mission.Mutation{Kind: mission.MutateStartTask, TaskID: featureID}); err != nil {
			log.Printf("MISSION-ACT: recordVerifyFeature start(%s) rejected: %v", featureID, err)
			return // can't proceed without active state
		}
	}

	var err error
	switch outcome {
	case "passing":
		err = mission.Mutate(m, mission.Mutation{
			Kind:     mission.MutateCompleteTask,
			TaskID:   featureID,
			Evidence: &ev,
		})
	case "retry":
		err = mission.Mutate(m, mission.Mutation{
			Kind:   mission.MutateRetryTask,
			TaskID: featureID,
		})
	default: // "blocked" and any unexpected value
		err = mission.Mutate(m, mission.Mutation{
			Kind:   mission.MutateBlockTask,
			TaskID: featureID,
			Blocked: &mission.BlockedReason{
				Kind:             mission.GapShapeUnsupported,
				MissingDimension: featureID,
				CandidatesChecked: []mission.CandidateCheck{{
					IntentName:      featureID,
					WhyInsufficient: ev.Reasoning,
				}},
			},
		})
	}
	if err != nil {
		log.Printf("MISSION-ACT: recordVerifyFeature mutate(%s, %s) rejected: %v", featureID, outcome, err)
		return
	}
	sm.m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if saveErr := sm.store.Save(ctx, sm.m); saveErr != nil {
		log.Printf("MISSION-ACT: recordVerifyFeature save failed (mission %s): %v", sm.m.MissionID, saveErr)
	}
}

// recordCompleteAnalysis is the complete_analysis hook (M3). Sets
// synthesis.output from the machine-rendered final answer; mission status
// derives automatically from task states (complete if all passing, partial if
// any blocked). No-op when shadow path is inert.
func (sm *shadowMission) recordCompleteAnalysis(ctx context.Context, finalAnswer string) {
	if sm == nil || sm.m == nil {
		return
	}
	sm.m.Synthesis.Output = finalAnswer
	// Recompute status from task states (already handled by Mutate calls
	// above; call DeriveStatus directly to ensure it's up-to-date even if
	// some Mutate calls were rejected).
	sm.m.Status = mission.DeriveStatus(sm.m.Tasks, sm.m.BlockedRoot)
	sm.m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := sm.store.Save(ctx, sm.m); err != nil {
		log.Printf("MISSION-ACT: recordCompleteAnalysis save failed (mission %s): %v", sm.m.MissionID, err)
	}
}

// recordReachability stores the 任务可达器 verdict on the shadow mission and
// saves best-effort. Called once per turn, before the ReAct loop. No-op when
// the shadow path is inert.
func (sm *shadowMission) recordReachability(ctx context.Context, verdict mission.ReachabilityVerdict) {
	if sm == nil || sm.m == nil {
		return
	}
	sm.m.Reachability = &verdict
	if !verdict.Feasible {
		sm.m.Status = mission.MissionUnanswerable
	}
	sm.m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := sm.store.Save(ctx, sm.m); err != nil {
		log.Printf("MISSION-ACT: recordReachability save failed (mission %s): %v", sm.m.MissionID, err)
	}
}

// featureRuntimeView is the minimal projection of analysis.FeatureRuntime
// that seedTasksFromFeatures needs. Using this thin view avoids importing the
// analysis package into mission_shadow.go (which would create a package
// dependency that isn't needed anywhere else in the shadow path).
type featureRuntimeView struct {
	ID           string
	Behavior     string
	Verification string
	ToolHints    []featureToolHintView
}

type featureToolHintView struct {
	Tool string
}

// finish is the turn-end hook. It records the final assistant answer into
// synthesis.output, marks the mission complete and persists it again.
// Best-effort: a save failure is logged, never propagated.
func (sm *shadowMission) finish(ctx context.Context, finalAnswer string) {
	if sm == nil || sm.m == nil {
		return
	}
	sm.m.Synthesis.Output = finalAnswer
	// Preserve an unanswerable verdict — a reachability gate or a
	// capability gap already set it; "the turn ended" must not relabel
	// it as complete.
	if sm.m.Status != mission.MissionUnanswerable {
		sm.m.Status = mission.MissionComplete
	}
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
