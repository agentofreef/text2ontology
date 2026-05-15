// Package sqlite provides a source connector for SQLite database files
// (.sqlite / .db / .sqlite3). The user uploads a file; the connector reads
// catalog (sqlite_master + PRAGMA), then bulk-copies selected tables into
// a Postgres staging schema in the collector's own DB. Data flow mirrors
// ingest/postgres but the source side is a local file opened via the
// pure-Go modernc.org/sqlite driver (no cgo, no Dockerfile changes).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers as "sqlite"
)

// Open opens a SQLite database file and validates it via PRAGMA schema_version.
// Caller is responsible for calling db.Close().
func Open(ctx context.Context, dbPath string) (*sql.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("sqlite file not accessible: %w", err)
	}
	// Open as read-only via URI form so concurrent connectors don't fight
	// over write locks. modernc.org/sqlite supports the standard URI syntax.
	uri := "file:" + dbPath + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, err
	}
	// Spot check that this is a real SQLite file: PRAGMA schema_version is
	// the cheapest read that still requires a valid header.
	var v int
	if err := db.QueryRowContext(pingCtx, `PRAGMA schema_version`).Scan(&v); err != nil {
		db.Close()
		return nil, fmt.Errorf("not a valid SQLite database: %w", err)
	}
	return db, nil
}

// quoteIdent quotes a SQLite identifier with double quotes, doubling any
// embedded quotes per the SQLite spec. Used for table names returned from
// sqlite_master that may contain spaces or other punctuation.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
