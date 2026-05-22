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
//
// renameMap (optional) maps the imported base table name (the part after the
// last dot in each "schema.table") to a collision-free name within the project:
// after IMPORT FOREIGN SCHEMA lands the foreign tables under their original
// names, each entry triggers an ALTER FOREIGN TABLE ... RENAME so a second data
// source in the same project does not collide with an existing table. Pass nil
// to import under the original names.
func SetupForeignSchema(ctx context.Context, db *sql.DB, dsID, finalSchema string, cfg Config, tables []string, renameMap map[string]string) error {
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

	// Import into a private per-source staging schema first, then move each
	// foreign table into finalSchema under its collision-free name. Importing
	// straight INTO finalSchema would fail when another source in the project
	// already owns a table of the same original name (IMPORT FOREIGN SCHEMA can't
	// rename on import); the staging hop lets us land + rename safely.
	importSchema := finalSchema + "_fdwimp_" + strings.ReplaceAll(dsID, "-", "_")
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`DROP SCHEMA IF EXISTS %s CASCADE`, pq.QuoteIdentifier(importSchema),
	)); err != nil {
		return fmt.Errorf("drop stale import schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`CREATE SCHEMA %s`, pq.QuoteIdentifier(importSchema),
	)); err != nil {
		return fmt.Errorf("create import schema: %w", err)
	}
	// Always clean the staging schema up — on success it's empty after the moves,
	// on failure it holds partial imports we don't want to leak.
	defer func() {
		_, _ = db.ExecContext(ctx, fmt.Sprintf(
			`DROP SCHEMA IF EXISTS %s CASCADE`, pq.QuoteIdentifier(importSchema)))
	}()

	// IMPORT FOREIGN SCHEMA operates per remote schema. Group the selected
	// "schema.table" names by their remote schema; bare names default to
	// "public". All groups import INTO the staging schema, then move INTO the
	// single flat finalSchema, matching the file/sqlite flat-table-namespace
	// convention.
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
			pq.QuoteIdentifier(importSchema),
		)); err != nil {
			return fmt.Errorf("import foreign schema %q: %w", remoteSchema, err)
		}
	}

	// Move each imported foreign table into finalSchema under its collision-free
	// name (renameMap[orig] → final). A different source resolves to a suffixed
	// name; an idempotent re-confirm of THIS source resolves back to its own
	// name, so we DROP any same-(final-)named table first to replace cleanly.
	imported, err := listForeignTables(ctx, db, importSchema)
	if err != nil {
		return fmt.Errorf("list imported foreign tables: %w", err)
	}
	for _, orig := range imported {
		final := orig
		if v, ok := renameMap[orig]; ok && v != "" {
			final = v
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			`DROP FOREIGN TABLE IF EXISTS %s.%s CASCADE`,
			pq.QuoteIdentifier(finalSchema), pq.QuoteIdentifier(final),
		)); err != nil {
			return fmt.Errorf("drop conflicting foreign table %s.%s: %w", finalSchema, final, err)
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			`ALTER FOREIGN TABLE %s.%s SET SCHEMA %s`,
			pq.QuoteIdentifier(importSchema), pq.QuoteIdentifier(orig),
			pq.QuoteIdentifier(finalSchema),
		)); err != nil {
			return fmt.Errorf("move foreign table %s.%s → %s: %w", importSchema, orig, finalSchema, err)
		}
		if final != orig {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				`ALTER FOREIGN TABLE %s.%s RENAME TO %s`,
				pq.QuoteIdentifier(finalSchema), pq.QuoteIdentifier(orig),
				pq.QuoteIdentifier(final),
			)); err != nil {
				return fmt.Errorf("rename foreign table %s.%s → %s: %w", finalSchema, orig, final, err)
			}
		}
	}
	return nil
}

// listForeignTables returns the foreign-table names in a schema.
func listForeignTables(ctx context.Context, db *sql.DB, schema string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT foreign_table_name FROM information_schema.foreign_tables
		WHERE foreign_table_schema = $1 ORDER BY foreign_table_name`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
