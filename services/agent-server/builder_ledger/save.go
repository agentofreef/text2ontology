package builder_ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrVersionConflict is returned by Save when the DB's ledger version no
// longer matches oldVersion — another request has written the ledger in
// between Load and Save. Callers should reload, re-merge, and Save again.
var ErrVersionConflict = errors.New("builder_ledger version conflict: concurrent write detected")

// Save persists the ledger under thread_state->'builder_ledger' using an
// optimistic-concurrency check against oldVersion.
//
// Mechanics (mirror of ledger.Save):
//   - Bumps l.Version := oldVersion + 1 before writing.
//   - Uses jsonb_set so sibling keys in thread_state are not clobbered.
//   - WHERE clause: only updates if stored version == oldVersion (or the
//     key was absent when oldVersion == 0).
//   - Zero rows affected → ErrVersionConflict.
func Save(db *sql.DB, threadID string, l *BuilderLedger, oldVersion int) error {
	if l == nil {
		return fmt.Errorf("builder_ledger.Save: nil ledger")
	}
	l.Version = oldVersion + 1
	l.SchemaVersion = SchemaVersion
	l.UpdatedAt = time.Now()

	data, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("builder_ledger.Save marshal: %w", err)
	}

	result, execErr := db.Exec(`
		UPDATE ont_agent_thread
		   SET thread_state = jsonb_set(COALESCE(thread_state,'{}'::jsonb), '{builder_ledger}', $1::jsonb),
		       updated_at = now()
		 WHERE id = $2
		   AND COALESCE((thread_state->'builder_ledger'->>'version')::int, 0) = $3`,
		string(data), threadID, oldVersion)
	if execErr != nil {
		return fmt.Errorf("builder_ledger.Save exec: %w", execErr)
	}
	n, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("builder_ledger.Save rows: %w", rowsErr)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// SaveWithRetry wraps Save with a reload-remerge-retry loop for the common
// optimistic-concurrency conflict case. reapply receives the freshly-loaded
// ledger from DB and must return the pointer to save (or nil to abandon).
// maxRetries=2 is sufficient for single-user single-thread traffic.
func SaveWithRetry(ctx context.Context, db *sql.DB, threadID string, l *BuilderLedger, oldVersion int,
	reapply func(fresh *BuilderLedger) *BuilderLedger, maxRetries int,
) error {
	if err := Save(db, threadID, l, oldVersion); !errors.Is(err, ErrVersionConflict) {
		return err
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		fresh, loadErr := Load(ctx, db, threadID)
		if loadErr != nil {
			return fmt.Errorf("builder_ledger.SaveWithRetry reload: %w", loadErr)
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
