// Package mission implements the MissionAct architecture: the unified
// per-turn agent state. Single-query / compose / plan / capability_gap
// are not separate modes — they are different shapes of one Mission.
//
// See .omc/specs/mission-act.md for the full design.
package mission

// TaskStatus is the lifecycle state of one task — the "status" leg of
// the L08 behavior/verification/status triad. Legal transitions are
// defined in statemachine.go; passing and blocked are terminal.
type TaskStatus string

const (
	TaskPending      TaskStatus = "pending"
	TaskActive       TaskStatus = "active"
	TaskPassing      TaskStatus = "passing"
	TaskBlocked      TaskStatus = "blocked"
	TaskPendingRetry TaskStatus = "pending_retry"
)

// MissionStatus is the top-level mission outcome. It is always derived
// from the tasks (DeriveStatus), never set directly.
type MissionStatus string

const (
	MissionActive       MissionStatus = "active"
	MissionComplete     MissionStatus = "complete"
	MissionPartial      MissionStatus = "partial"
	MissionUnanswerable MissionStatus = "unanswerable"
)

// GapKind classifies a capability gap — what kind of fix it points at.
type GapKind string

const (
	GapNoParam          GapKind = "no_param"          // no Intent exposes the dimension
	GapShapeUnsupported GapKind = "shape_unsupported" // param exists, shape too narrow
	GapNoData           GapKind = "no_data"           // underlying column absent
)

// Mission is the single source of truth for one turn of agent work.
// Everything the agent knows this turn lives here; transcripts, UI and
// eval are all views of this object.
type Mission struct {
	MissionID       string                `json:"mission_id"`
	ThreadID        string                `json:"thread_id"`
	ParentMissionID string                `json:"parent_mission_id,omitempty"`
	ProjectID       string                `json:"project_id"`
	Question        string                `json:"question"`
	Recall          Recall                `json:"recall"`
	Decomposition   []DecompItem          `json:"decomposition"`
	Tasks           []Task                `json:"tasks"`
	Cursor          string                `json:"cursor,omitempty"`
	StepResults     map[string]StepResult `json:"step_results"`
	Synthesis       Synthesis             `json:"synthesis"`
	Status          MissionStatus         `json:"status"`
	// Reachability is the 任务可达器 verdict — the mandatory first
	// judgment: can the question be answered from authorized data, and
	// why / why not. Set before any querying; gates the rest.
	Reachability    *ReachabilityVerdict  `json:"reachability,omitempty"`
	BlockedRoot     *BlockedReason        `json:"blocked_root,omitempty"`
	CreatedAt       string                `json:"created_at,omitempty"`
	UpdatedAt       string                `json:"updated_at,omitempty"`
}

// Recall is the candidate registry produced by recall-server: the
// Intents / OK cards / keywords a task may draw on.
type Recall struct {
	Intents  []any    `json:"intents,omitempty"`
	OKCards  []any    `json:"ok_cards,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

// DecompItem is one structured (dimension, shape) the question requires.
// Tasks reference these by ID via Task.Covers — the coverage check
// matches them against declared Intent parameters.
type DecompItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // metric | dimension | filter
	Name        string `json:"name"`
	Shape       string `json:"shape"` // scalar | group-by | range | ...
	WhyRequired string `json:"why_required,omitempty"`
}

// Task is one unit of work — the L08 triad: behavior, verification,
// status.
type Task struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"` // smartquery | compose_query | lookup | reflect | synthesize | sub_mission
	Behavior      string         `json:"behavior"`
	Covers        []string       `json:"covers,omitempty"`     // DecompItem IDs
	DependsOn     []string       `json:"depends_on,omitempty"` // Task IDs
	Dispatch      Dispatch       `json:"dispatch"`
	Verification  string         `json:"verification,omitempty"`
	Status        TaskStatus     `json:"status"`
	RetryBudget   int            `json:"retry_budget"`
	ResultRef     string         `json:"result_ref,omitempty"` // step id, e.g. "t1"
	Evidence      *Evidence      `json:"evidence,omitempty"`
	BlockedReason *BlockedReason `json:"blocked_reason,omitempty"`
}

// Dispatch is the tool call a task runs. Args values sourced from prior
// step results must be references, not literals (pointer invariant,
// see pointer.go and spec §0.5).
type Dispatch struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

// Evidence records how a task reached its terminal state.
type Evidence struct {
	Tool          string `json:"tool,omitempty"`
	ResultSummary string `json:"result_summary,omitempty"`
	Reasoning     string `json:"reasoning,omitempty"`
}

// BlockedReason is the proof attached to a blocked task — what is
// missing, which Intents were checked, and where the fix lives.
type BlockedReason struct {
	Kind              GapKind          `json:"kind"`
	MissingDimension  string           `json:"missing_dimension,omitempty"`
	CandidatesChecked []CandidateCheck `json:"candidates_checked,omitempty"`
	SuggestedFix      string           `json:"suggested_fix,omitempty"`
}

// CandidateCheck is one Intent examined while proving a capability gap.
type CandidateCheck struct {
	IntentName      string `json:"intent_name"`
	ParamsSummary   string `json:"params_summary,omitempty"`
	WhyInsufficient string `json:"why_insufficient,omitempty"`
}

// StepResult is one tool result table, registered under a step id
// (t1, t2, ...). It is the resolution source for pointer references.
type StepResult struct {
	Rows     []map[string]any `json:"rows"`
	FromTask string           `json:"from_task,omitempty"`
	Schema   []string         `json:"schema,omitempty"`
}

// Synthesis holds the final answer and the caveats appended verbatim
// from blocked tasks.
type Synthesis struct {
	Template         string   `json:"template,omitempty"`
	Caveats          []string `json:"caveats,omitempty"`
	Output           string   `json:"output,omitempty"`
	ClosestReachable string   `json:"closest_reachable,omitempty"`
}
