package analysis

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// helper: build a 2-feature pattern for tests.
func newTestPattern(t *testing.T) *recall.AnalysisPattern {
	t.Helper()
	raw := json.RawMessage(`{
		"trigger": {"keywords":["k"]},
		"features": [
			{"id":"f1","behavior":"b1","verification":"v1"},
			{"id":"f2","behavior":"b2","verification":"v2"}
		],
		"synthesis": {"template":"tpl"}
	}`)
	p, err := recall.ParseAnalysisPattern(raw)
	if err != nil {
		t.Fatalf("ParseAnalysisPattern: %v", err)
	}
	return p
}

// T2 — legal state transitions: not_started → active → passing.
func TestFeatureLedger_HappyPath(t *testing.T) {
	l := NewFeatureLedger(newTestPattern(t))

	next, ok := l.PickNext()
	if !ok || next.ID != "f1" {
		t.Fatalf("first pick should be f1, got %+v ok=%v", next, ok)
	}
	if err := l.Activate("f1"); err != nil {
		t.Fatalf("activate f1: %v", err)
	}
	if err := l.Pass("f1", Evidence{Tool: "smartquery", Summary: "ok"}); err != nil {
		t.Fatalf("pass f1: %v", err)
	}
	if l.AllSettled() {
		t.Fatal("ledger should not be settled with f2 still not_started")
	}

	next2, ok := l.PickNext()
	if !ok || next2.ID != "f2" {
		t.Fatalf("second pick should be f2, got %+v ok=%v", next2, ok)
	}
	if err := l.Activate("f2"); err != nil {
		t.Fatalf("activate f2: %v", err)
	}
	if err := l.Pass("f2", Evidence{Tool: "compose_query", Summary: "ok"}); err != nil {
		t.Fatalf("pass f2: %v", err)
	}
	if !l.AllSettled() {
		t.Fatal("ledger should be settled after both pass")
	}
	p, b, ns, a := l.CountByState()
	if p != 2 || b != 0 || ns != 0 || a != 0 {
		t.Errorf("counts = (passing=%d blocked=%d notStarted=%d active=%d), want (2,0,0,0)", p, b, ns, a)
	}
}

// T2b — illegal transitions return typed errors.
func TestFeatureLedger_IllegalTransitions(t *testing.T) {
	l := NewFeatureLedger(newTestPattern(t))

	// Pass before activate
	if err := l.Pass("f1", Evidence{}); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("pass before activate: want ErrIllegalTransition, got %v", err)
	}
	// Unknown feature
	if err := l.Activate("ghost"); !errors.Is(err, ErrUnknownFeature) {
		t.Errorf("activate ghost: want ErrUnknownFeature, got %v", err)
	}
	// Double activate same feature
	if err := l.Activate("f1"); err != nil {
		t.Fatalf("first activate: %v", err)
	}
	if err := l.Activate("f1"); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("re-activate f1: want ErrIllegalTransition, got %v", err)
	}
}

// T3 — WIP=1 invariant: cannot activate two features at once.
func TestFeatureLedger_WIP1(t *testing.T) {
	l := NewFeatureLedger(newTestPattern(t))
	if err := l.Activate("f1"); err != nil {
		t.Fatalf("activate f1: %v", err)
	}
	err := l.Activate("f2")
	if !errors.Is(err, ErrWIPViolation) {
		t.Fatalf("activate f2 while f1 active: want ErrWIPViolation, got %v", err)
	}
	// After passing f1, f2 should be activatable.
	if err := l.Pass("f1", Evidence{Summary: "ok"}); err != nil {
		t.Fatalf("pass f1: %v", err)
	}
	if err := l.Activate("f2"); err != nil {
		t.Errorf("activate f2 after f1 passed: %v", err)
	}
}

// T4 — retry budget of 2: 1st + 2nd retry are allowed, 3rd fails to retry
// (the caller must Block at that point).
func TestFeatureLedger_RetryBudget(t *testing.T) {
	l := NewFeatureLedger(newTestPattern(t))

	// Attempt 1: activate then retry
	if err := l.Activate("f1"); err != nil {
		t.Fatalf("activate#1: %v", err)
	}
	if err := l.Retry("f1", Evidence{Reasoning: "wrong tool"}); err != nil {
		t.Fatalf("retry#1: %v", err)
	}

	// Attempt 2: activate then retry again
	if err := l.Activate("f1"); err != nil {
		t.Fatalf("activate#2: %v", err)
	}
	if err := l.Retry("f1", Evidence{Reasoning: "bad params"}); err != nil {
		t.Fatalf("retry#2: %v", err)
	}

	// Attempt 3: activate, retry should now be exhausted
	if err := l.Activate("f1"); err != nil {
		t.Fatalf("activate#3: %v", err)
	}
	err := l.Retry("f1", Evidence{Reasoning: "still no"})
	if !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("retry#3: want ErrRetryExhausted, got %v", err)
	}
	// Caller should now Block.
	if err := l.Block("f1", Evidence{Error: "verify failed 3 times"}); err != nil {
		t.Fatalf("block after exhausted: %v", err)
	}

	snap := l.Snapshot()
	if snap[0].State != StateBlocked {
		t.Errorf("f1 state = %s, want blocked", snap[0].State)
	}
	if snap[0].RetryCount != RetryBudget {
		t.Errorf("f1 retry_count = %d, want %d", snap[0].RetryCount, RetryBudget)
	}
	if snap[0].Evidence == nil || snap[0].Evidence.Error == "" {
		t.Errorf("f1 evidence missing error: %+v", snap[0].Evidence)
	}

	// PickNext should now skip f1 (terminal) and return f2.
	next, ok := l.PickNext()
	if !ok || next.ID != "f2" {
		t.Errorf("PickNext after f1 blocked: want f2, got %+v ok=%v", next, ok)
	}
}

// T4b — Snapshot is defensive: mutating returned values does not affect the ledger.
func TestFeatureLedger_SnapshotIsCopy(t *testing.T) {
	l := NewFeatureLedger(newTestPattern(t))
	_ = l.Activate("f1")
	_ = l.Pass("f1", Evidence{Summary: "ok"})

	snap := l.Snapshot()
	snap[0].State = StateBlocked
	snap[0].Evidence.Summary = "TAMPERED"

	// Original ledger should be unchanged.
	snap2 := l.Snapshot()
	if snap2[0].State != StatePassing {
		t.Errorf("after tamper: state = %s, want passing", snap2[0].State)
	}
	if snap2[0].Evidence.Summary != "ok" {
		t.Errorf("after tamper: summary = %q, want %q", snap2[0].Evidence.Summary, "ok")
	}
}
