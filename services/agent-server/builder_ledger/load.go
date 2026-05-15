package builder_ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
)

// Load reads the builder ledger stored at
// ont_agent_thread.thread_state->'builder_ledger' for the given thread.
// It always returns a non-nil BuilderLedger — missing/invalid JSON or
// schema mismatch all yield New() so callers never need to nil-check.
//
// The returned ledger's Version field records the version read from DB;
// pass it back to Save() as oldVersion for optimistic-concurrency.
func Load(ctx context.Context, db *sql.DB, threadID string) (*BuilderLedger, error) {
	var raw sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT thread_state->'builder_ledger'
		  FROM ont_agent_thread
		 WHERE id = $1`, threadID).Scan(&raw)
	if err == sql.ErrNoRows {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("builder_ledger.Load query: %w", err)
	}
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		return New(), nil
	}

	var l BuilderLedger
	if err := json.Unmarshal([]byte(raw.String), &l); err != nil {
		log.Printf("builder_ledger.Load: invalid JSON in thread %s, starting fresh: %v", threadID, err)
		return New(), nil
	}
	l.EnsureMaps()

	// SchemaVersion mismatch → reset. No migration; the ledger is
	// ephemeral per-session context so a fresh start is acceptable.
	if l.SchemaVersion != SchemaVersion {
		log.Printf("builder_ledger.Load: schema %d != %d in thread %s, resetting",
			l.SchemaVersion, SchemaVersion, threadID)
		fresh := New()
		// Preserve turn count so turn numbers stay stable across resets.
		fresh.TurnCount = l.TurnCount
		return fresh, nil
	}

	return &l, nil
}
