package lakehouse

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// recordingExecer is a fake execSetLocaler that records every Exec'd statement
// so we can assert applyReadOnlyGuards issues the right SET LOCAL commands
// without a live Postgres. It can optionally fail on the Nth Exec call.
type recordingExecer struct {
	stmts   []string
	failOn  int // 1-based index of the call that should error; 0 = never fail
	callIdx int
}

func (r *recordingExecer) Exec(query string, args ...any) (sql.Result, error) {
	r.callIdx++
	r.stmts = append(r.stmts, query)
	if r.failOn != 0 && r.callIdx == r.failOn {
		return nil, errors.New("boom")
	}
	return driverResult{}, nil
}

type driverResult struct{}

func (driverResult) LastInsertId() (int64, error) { return 0, nil }
func (driverResult) RowsAffected() (int64, error) { return 0, nil }

func TestApplyReadOnlyGuards_IssuesSetLocalStatements(t *testing.T) {
	rec := &recordingExecer{}
	if err := applyReadOnlyGuards(rec, 30000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.stmts) != 2 {
		t.Fatalf("expected exactly 2 SET LOCAL statements, got %d: %v", len(rec.stmts), rec.stmts)
	}
	// First statement must enforce read-only — this is what rejects writes.
	if !strings.Contains(rec.stmts[0], "SET LOCAL transaction_read_only = on") {
		t.Errorf("first statement must set transaction_read_only, got %q", rec.stmts[0])
	}
	// Second must set the statement timeout with the provided value.
	if !strings.Contains(rec.stmts[1], "SET LOCAL statement_timeout = 30000") {
		t.Errorf("second statement must set statement_timeout=30000, got %q", rec.stmts[1])
	}
	// read-only must be applied BEFORE the timeout (and before any user SQL).
	if !strings.Contains(rec.stmts[0], "transaction_read_only") {
		t.Error("read-only guard must be issued first")
	}
}

func TestApplyReadOnlyGuards_PropagatesReadOnlyError(t *testing.T) {
	rec := &recordingExecer{failOn: 1}
	err := applyReadOnlyGuards(rec, 30000)
	if err == nil {
		t.Fatal("expected error when SET read-only fails")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error should mention read-only, got %v", err)
	}
}

func TestApplyReadOnlyGuards_PropagatesTimeoutError(t *testing.T) {
	rec := &recordingExecer{failOn: 2}
	err := applyReadOnlyGuards(rec, 30000)
	if err == nil {
		t.Fatal("expected error when SET statement_timeout fails")
	}
	if !strings.Contains(err.Error(), "statement_timeout") {
		t.Errorf("error should mention statement_timeout, got %v", err)
	}
}
