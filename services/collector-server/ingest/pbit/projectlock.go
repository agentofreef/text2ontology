package pbit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// WithProjectLock runs fn while holding a PostgreSQL session-level advisory lock
// keyed on the project. It serializes concurrent imports into the SAME project
// so that:
//
//   - the cross-source name de-collision probes (ResolveCollisionFreeNames) and
//     the same-content dedup check see a stable view (no other import is
//     creating tables/objects/data_source rows for this project mid-flight), and
//   - two jobs never race on CREATE TABLE / COPY into the shared proj_<hex>
//     schema.
//
// The lock is advisory (cooperative): every writer into a project's lakehouse
// must funnel through here. It is taken on a dedicated connection and released
// on fn return; closing the connection also releases it, so a crash or a missed
// unlock cannot leak the lock past the connection's lifetime. Different projects
// hash to different keys and never block each other.
//
// Note: the key is hashtext(projectID)::int4. A hash collision between two
// different projects can only cause an occasional spurious serialization, never
// incorrect data — both still operate on their own proj_<hex> schema.
func WithProjectLock(ctx context.Context, db *sql.DB, projectID string, fn func(context.Context) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("project lock: acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, projectID); err != nil {
		return fmt.Errorf("project lock: acquire: %w", err)
	}
	defer func() {
		// Release on a fresh context so a cancelled parent ctx still unlocks.
		// (conn.Close above is the backstop: a session-level advisory lock is
		// dropped when its owning connection is returned/closed.)
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rctx, `SELECT pg_advisory_unlock(hashtext($1))`, projectID)
	}()

	return fn(ctx)
}
