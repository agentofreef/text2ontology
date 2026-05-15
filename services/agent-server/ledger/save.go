package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lakehouse2ontology/observability"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ErrVersionConflict is returned by Save when the DB's ledger version
// no longer matches oldVersion — another request has written the ledger
// in between Load and Save. Callers should reload, re-merge this turn's
// work, and Save again.
var ErrVersionConflict = errors.New("ledger version conflict: concurrent write detected")

// Save persists the ledger under thread_state->'ledger' using an
// optimistic-concurrency check against oldVersion. oldVersion is the
// Version field read by Load; a 0 here means "no prior ledger existed".
//
// Mechanics:
//   - Bumps l.Version := oldVersion + 1 before writing (callers shouldn't
//     mutate Version themselves).
//   - Uses jsonb_set merge on thread_state so we don't clobber sibling
//     keys (parent_thread_id, seed_system_prompt, status, ...).
//   - Conditional WHERE clause: only updates if the current stored
//     version matches oldVersion (or the ledger key was absent when
//     oldVersion==0).
//   - On zero rows affected → ErrVersionConflict. Caller decides retry.
//
// The updated_at column is touched so UI thread lists surface the
// activity.
func Save(db *sql.DB, threadID string, l *Ledger, oldVersion int) error {
	if l == nil {
		return fmt.Errorf("ledger.Save: nil ledger")
	}
	l.Version = oldVersion + 1
	l.SchemaVersion = SchemaVersion

	data, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("ledger.Save marshal: %w", err)
	}

	// Conditional UPDATE:
	//   - oldVersion == 0 → require ledger key to be NULL/missing
	//     (first write on this thread). Using COALESCE to 0 lets an
	//     explicit "version":0 ledger also match, but no valid ledger
	//     ever has version 0 so that's benign.
	//   - oldVersion > 0  → require current stored version to equal it.
	result, execErr := db.Exec(`
		UPDATE ont_agent_thread
		   SET thread_state = jsonb_set(COALESCE(thread_state,'{}'::jsonb), '{ledger}', $1::jsonb),
		       updated_at = now()
		 WHERE id = $2
		   AND COALESCE((thread_state->'ledger'->>'version')::int, 0) = $3`,
		string(data), threadID, oldVersion)
	if execErr != nil {
		return fmt.Errorf("ledger.Save exec: %w", execErr)
	}
	n, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("ledger.Save rows: %w", rowsErr)
	}
	if n == 0 {
		// Either the thread row is missing (rare — handler always
		// creates one) or version drifted. Either way the caller
		// needs to reload.
		return ErrVersionConflict
	}
	return nil
}

// SaveWithRetry wraps Save + reload for the common case: caller holds a
// merge-ready *Ledger and a closure that re-applies this turn's delta
// to a freshly-loaded ledger. On conflict, Load → remerge → Save is
// retried up to maxRetries times (2 is fine for single-user single-
// thread traffic — production races happen only via the browser's
// accidental double-submit).
//
// reapply receives the freshly-loaded ledger and must return the same
// pointer (or a derived one) ready to save. If reapply returns nil, the
// retry is abandoned without an error (caller chose to give up).
func SaveWithRetry(ctx context.Context, db *sql.DB, threadID string, l *Ledger, oldVersion int,
	reapply func(fresh *Ledger) *Ledger, maxRetries int,
) error {
	_, span := observability.Tracer().Start(ctx, "ledger.save_with_retry",
		trace.WithAttributes(attribute.String("thread_id", threadID)))
	defer span.End()
	start := time.Now()
	defer func() {
		observability.LedgerSaveDuration.Observe(float64(time.Since(start).Milliseconds()))
	}()

	if err := Save(db, threadID, l, oldVersion); !errors.Is(err, ErrVersionConflict) {
		return err
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Each retry is a distinct optimistic-concurrency conflict event.
		observability.LedgerSaveConflictRate.Inc()
		fresh, loadErr := Load(ctx, db, threadID)
		if loadErr != nil {
			return fmt.Errorf("ledger.SaveWithRetry reload: %w", loadErr)
		}
		newL := reapply(fresh)
		if newL == nil {
			return nil
		}
		if err := Save(db, threadID, newL, fresh.Version); !errors.Is(err, ErrVersionConflict) {
			return err
		}
	}
	return ErrVersionConflict
}
