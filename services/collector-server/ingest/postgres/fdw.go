package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/services/collector-server/ingest/pgschema"
)

// SetupForeignSchema wires an external PostgreSQL into the ontology database
// as postgres_fdw foreign tables — the zero-copy alternative to SyncTables.
//
// After this runs, finalSchema (proj_<hex>) holds one foreign table per
// selected source table. The lakehouse engine queries them exactly like
// physical tables, but every SELECT is pushed down to the remote at query
// time: no rows are copied, the data is never stale, and an arbitrarily large
// source DB costs nothing to "import".
//
// Idempotent: the per-data-source foreign server is dropped CASCADE and
// recreated, which also clears its old user mapping + foreign tables, so a
// re-confirm cleanly picks up credential or table-selection changes.
//
// db is the ontology's own Postgres (collector DB). cfg describes the remote.
// tables are "schema.table" names (the form Discover emits).
func SetupForeignSchema(ctx context.Context, db *sql.DB, dsID, finalSchema string, cfg Config, tables []string) error {
	if len(tables) == 0 {
		return fmt.Errorf("SetupForeignSchema: no tables selected")
	}
	serverName := "fdw_" + strings.ReplaceAll(dsID, "-", "_")
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}

	if _, err := db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS postgres_fdw`); err != nil {
		return fmt.Errorf("create postgres_fdw extension: %w", err)
	}

	// Drop + recreate the server CASCADE — this also removes its prior user
	// mapping and any foreign tables it owns in finalSchema, so credential or
	// table-selection changes on a re-confirm take effect without leftovers.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`DROP SERVER IF EXISTS %s CASCADE`, pq.QuoteIdentifier(serverName),
	)); err != nil {
		return fmt.Errorf("drop stale foreign server: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`CREATE SERVER %s FOREIGN DATA WRAPPER postgres_fdw
		 OPTIONS (host %s, port %s, dbname %s, sslmode %s)`,
		pq.QuoteIdentifier(serverName),
		pq.QuoteLiteral(cfg.Host),
		pq.QuoteLiteral(strconv.Itoa(cfg.Port)),
		pq.QuoteLiteral(cfg.Database),
		pq.QuoteLiteral(sslmode),
	)); err != nil {
		return fmt.Errorf("create foreign server: %w", err)
	}

	// User mapping for the role the ontology DB itself connects as. The remote
	// credentials live only here (in pg_user_mapping), never copied elsewhere.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`CREATE USER MAPPING FOR CURRENT_USER SERVER %s OPTIONS (user %s, password %s)`,
		pq.QuoteIdentifier(serverName),
		pq.QuoteLiteral(cfg.User),
		pq.QuoteLiteral(cfg.Password),
	)); err != nil {
		return fmt.Errorf("create user mapping: %w", err)
	}

	if err := pgschema.CreateSchemaWithGrants(ctx, db, finalSchema); err != nil {
		return fmt.Errorf("create final schema %q: %w", finalSchema, err)
	}

	// IMPORT FOREIGN SCHEMA operates per remote schema. Group the selected
	// "schema.table" names by their remote schema; bare names default to
	// "public". All groups import INTO the single flat finalSchema, matching
	// the file/sqlite path's flat-table-namespace-per-project convention.
	bySchema := map[string][]string{}
	for _, t := range tables {
		remoteSchema, name := "public", t
		if dot := strings.Index(t, "."); dot > 0 {
			remoteSchema, name = t[:dot], t[dot+1:]
		}
		bySchema[remoteSchema] = append(bySchema[remoteSchema], name)
	}
	for remoteSchema, names := range bySchema {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = pq.QuoteIdentifier(n)
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			`IMPORT FOREIGN SCHEMA %s LIMIT TO (%s) FROM SERVER %s INTO %s`,
			pq.QuoteIdentifier(remoteSchema),
			strings.Join(quoted, ", "),
			pq.QuoteIdentifier(serverName),
			pq.QuoteIdentifier(finalSchema),
		)); err != nil {
			return fmt.Errorf("import foreign schema %q: %w", remoteSchema, err)
		}
	}
	return nil
}
