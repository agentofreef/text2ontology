package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/lakehouse2ontology/observability"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Load reads the ledger stored at ont_agent_thread.thread_state->'ledger'
// for the given thread. It always returns a non-nil Ledger — an empty
// thread or a thread with no ledger key yet yields New() so callers
// never need to nil-check.
//
// Behaviour:
//
//   - Thread row missing → returns New() and a nil error (caller decides
//     whether that's a bug). The handler always creates the thread row
//     before reaching this path, so in practice only tests hit this.
//   - thread_state->'ledger' missing / "null" / invalid JSON → returns
//     New() with a log line (invalid JSON only — missing is normal).
//   - SchemaVersion mismatch → returns New() so caller can re-run lazy
//     rebuild. Rolling back one schema version is not worth migration
//     code given rebuild is cheap.
//
// The returned Ledger's Version field records the version read from
// DB; pass it back to Save() as oldVersion for optimistic-concurrency
// check.
func Load(ctx context.Context, db *sql.DB, threadID string) (*Ledger, error) {
	_, span := observability.Tracer().Start(ctx, "ledger.load",
		trace.WithAttributes(attribute.String("thread_id", threadID)))
	defer span.End()

	var raw sql.NullString
	err := db.QueryRow(`
		SELECT thread_state->'ledger'
		  FROM ont_agent_thread
		 WHERE id = $1`, threadID).Scan(&raw)
	if err == sql.ErrNoRows {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger.Load query: %w", err)
	}
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		return New(), nil
	}

	var l Ledger
	if err := json.Unmarshal([]byte(raw.String), &l); err != nil {
		log.Printf("ledger.Load: invalid JSON in thread %s, starting fresh: %v", threadID, err)
		return New(), nil
	}
	l.EnsureMaps()

	// SchemaVersion mismatch → let caller rebuild from scratch. Keep
	// Version 0 so the first Save just writes a fresh ledger at
	// version 1 (no conditional update dance needed).
	if l.SchemaVersion != SchemaVersion {
		log.Printf("ledger.Load: schema %d != %d in thread %s, resetting", l.SchemaVersion, SchemaVersion, threadID)
		fresh := New()
		// Preserve turn count so turn numbers stay stable across a
		// rebuild; users reading chat history shouldn't see turns
		// renumbered.
		fresh.TurnCount = l.TurnCount
		return fresh, nil
	}

	return &l, nil
}
