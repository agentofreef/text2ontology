// Package analysis carries the runtime state for plan-mode analysis: the
// FeatureLedger (WIP=1 state machine over the features of an AnalysisPattern)
// and the synthesis primitive that renders the final answer.
//
// Design discipline (.omc/specs/plan-from-ontology-knowledge.md):
//   - WIP=1: at most one feature is in the `active` state at any time (§3.3).
//   - Retry budget=2 per feature, then `blocked` is a legal terminal state (§9.6).
//   - Caveats are appended verbatim by the synthesis layer, not via the LLM (§9.5).
package analysis

import (
	"errors"
	"fmt"
	"sync"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// FeatureState enumerates the four legal states from L08 (the harness lecture
// on feature lists as harness primitives). Transitions are gated by the
// FeatureLedger methods — agents cannot mutate state directly.
type FeatureState string

const (
	StateNotStarted FeatureState = "not_started"
	StateActive     FeatureState = "active"
	StatePassing    FeatureState = "passing"
	StateBlocked    FeatureState = "blocked"
)

// RetryBudget is the number of *additional* attempts allowed after the first
// verify failure. Total attempts per feature = 1 + RetryBudget = 3 (spec §9.6).
const RetryBudget = 2

// FeatureRuntime is one row of the ledger: the static feature data from the
// pattern card plus the runtime fields managed by the state machine.
type FeatureRuntime struct {
	// Static (copied from recall.AnalysisFeature)
	ID           string
	Behavior     string
	Verification string
	ToolHints    []recall.AnalysisToolHint

	// Runtime
	State        FeatureState
	RetryCount   int       // attempts so far that ended in retry (0..RetryBudget)
	Evidence     *Evidence // populated when State is passing or blocked
}

// Evidence captures what the agent did on the most recent terminal step
// (passing or blocked). Used by the synthesizer to render the final answer.
type Evidence struct {
	Tool       string `json:"tool,omitempty"`
	Args       string `json:"args,omitempty"`   // serialised tool args (compact JSON)
	Summary    string `json:"summary,omitempty"` // human-readable result digest (single line)
	Reasoning  string `json:"reasoning,omitempty"`
	RowCount   int    `json:"rowCount,omitempty"`
	Value      string `json:"value,omitempty"` // scalar result if available
	Error      string `json:"error,omitempty"` // populated on blocked
}

// FeatureLedger is the per-turn analysis state. It is *not* persisted across
// turns in v1 (spec §2 explicitly defers cross-session continuity to v1.5).
//
// Concurrency: a single ledger is owned by a single agent turn. The mutex
// guards against accidental concurrent mutation if a tool handler ever spawns
// goroutines — current code does not, but the invariant is cheap to enforce.
type FeatureLedger struct {
	mu      sync.Mutex
	pattern *recall.AnalysisPattern
	rows    []*FeatureRuntime
	byID    map[string]*FeatureRuntime
}

// NewFeatureLedger builds a fresh ledger from a parsed pattern card. All
// features start as not_started; no feature is active until Activate is called.
func NewFeatureLedger(p *recall.AnalysisPattern) *FeatureLedger {
	l := &FeatureLedger{
		pattern: p,
		rows:    make([]*FeatureRuntime, 0, len(p.Features)),
		byID:    make(map[string]*FeatureRuntime, len(p.Features)),
	}
	for _, f := range p.Features {
		row := &FeatureRuntime{
			ID:           f.ID,
			Behavior:     f.Behavior,
			Verification: f.Verification,
			ToolHints:    append([]recall.AnalysisToolHint(nil), f.ToolHints...),
			State:        StateNotStarted,
		}
		l.rows = append(l.rows, row)
		l.byID[f.ID] = row
	}
	return l
}

// Pattern returns the underlying card (read-only — caller must not mutate).
func (l *FeatureLedger) Pattern() *recall.AnalysisPattern { return l.pattern }

// PickNext returns the first not_started feature in declaration order, or
// (nil, false) if every feature is either passing/blocked or already active.
//
// PickNext does *not* activate the feature — call Activate to claim it. This
// split lets the LLM see the next behavior+verification before committing.
func (l *FeatureLedger) PickNext() (*FeatureRuntime, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.rows {
		if r.State == StateNotStarted {
			// Return a shallow copy so the caller cannot mutate ledger state.
			cp := *r
			return &cp, true
		}
	}
	return nil, false
}

// Activate transitions a feature from not_started → active.
//
// Errors:
//   - ErrUnknownFeature if id is not in the ledger
//   - ErrIllegalTransition if the feature is not in not_started
//   - ErrWIPViolation if another feature is already active (WIP=1 invariant)
func (l *FeatureLedger) Activate(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.byID[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownFeature, id)
	}
	if row.State != StateNotStarted {
		return fmt.Errorf("%w: feature %q is %s, not not_started",
			ErrIllegalTransition, id, row.State)
	}
	for _, other := range l.rows {
		if other.State == StateActive {
			return fmt.Errorf("%w: feature %q is already active",
				ErrWIPViolation, other.ID)
		}
	}
	row.State = StateActive
	return nil
}

// Pass transitions the *active* feature to passing with the given evidence.
// Returns an error if no feature is active or id does not match the active one.
func (l *FeatureLedger) Pass(id string, ev Evidence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, err := l.requireActive(id)
	if err != nil {
		return err
	}
	row.State = StatePassing
	row.Evidence = &ev
	return nil
}

// Retry transitions the *active* feature back to not_started, incrementing
// retry_count. Returns ErrRetryExhausted if the budget is already exhausted —
// the caller should call Block in that case.
func (l *FeatureLedger) Retry(id string, ev Evidence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, err := l.requireActive(id)
	if err != nil {
		return err
	}
	if row.RetryCount >= RetryBudget {
		return fmt.Errorf("%w: feature %q exhausted %d retries",
			ErrRetryExhausted, id, RetryBudget)
	}
	row.RetryCount++
	row.State = StateNotStarted
	row.Evidence = &ev // keep latest reasoning even on retry — useful for trace
	return nil
}

// Block transitions the *active* feature to blocked (terminal, legal state).
// Used either when retry budget is exhausted or the LLM declares the feature
// cannot be completed (e.g. an engine bug, missing data).
func (l *FeatureLedger) Block(id string, ev Evidence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, err := l.requireActive(id)
	if err != nil {
		return err
	}
	row.State = StateBlocked
	row.Evidence = &ev
	return nil
}

// AllSettled reports whether the ledger has no remaining work — every feature
// is in a terminal state (passing or blocked). The synthesizer waits for this
// before rendering the final answer.
func (l *FeatureLedger) AllSettled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.rows {
		if r.State == StateNotStarted || r.State == StateActive {
			return false
		}
	}
	return true
}

// Snapshot returns a defensive copy of all feature runtimes — used by the
// synthesizer + the trace renderer. Caller may freely read the result.
func (l *FeatureLedger) Snapshot() []FeatureRuntime {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]FeatureRuntime, len(l.rows))
	for i, r := range l.rows {
		out[i] = *r
		if r.Evidence != nil {
			ev := *r.Evidence
			out[i].Evidence = &ev
		}
	}
	return out
}

// CountByState returns (passing, blocked, notStarted, active) tallies. Used by
// the LLM-facing prompt to communicate progress without exposing internals.
func (l *FeatureLedger) CountByState() (passing, blocked, notStarted, active int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.rows {
		switch r.State {
		case StatePassing:
			passing++
		case StateBlocked:
			blocked++
		case StateNotStarted:
			notStarted++
		case StateActive:
			active++
		}
	}
	return
}

func (l *FeatureLedger) requireActive(id string) (*FeatureRuntime, error) {
	row, ok := l.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownFeature, id)
	}
	if row.State != StateActive {
		return nil, fmt.Errorf("%w: feature %q is %s, not active",
			ErrIllegalTransition, id, row.State)
	}
	return row, nil
}

// Sentinel errors for ledger transitions. Use errors.Is for matching.
var (
	ErrUnknownFeature    = errors.New("analysis: unknown feature id")
	ErrIllegalTransition = errors.New("analysis: illegal state transition")
	ErrWIPViolation      = errors.New("analysis: WIP=1 violated")
	ErrRetryExhausted    = errors.New("analysis: retry budget exhausted")
)
