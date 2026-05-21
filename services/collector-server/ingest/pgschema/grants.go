// Package pgschema centralises runtime CREATE SCHEMA together with the
// per-schema grants that the post-cutover reader roles need.
//
// Why this exists: collector-server creates schemas at ingest time (staging
// and final lakehouse schemas). After the role cutover, the reader services
// (lakehouse-sql-server, agent-server, recall-server) can only see those
// schemas if USAGE + default SELECT privileges are emitted per-schema at
// creation. Centralising that in one helper guarantees every CREATE SCHEMA
// site stays consistent.
//
// Resilience: pre-cutover the reader/owner roles may not exist yet. Rather
// than emit GRANT/ALTER statements that would error (and, inside a
// transaction, poison it), we first query pg_roles and only grant to roles
// that actually exist. The CREATE SCHEMA itself is fatal-on-error; the grants
// are strictly best-effort.
package pgschema

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/lib/pq"
)

// readerRoles are the roles that must be able to read collector-created
// schemas after the role cutover.
var readerRoles = []string{
	"lakehouse_sql_server_user",
	"agent_server_user",
	"recall_server_user",
}

// ownerRole owns the tables collector creates; ALTER DEFAULT PRIVILEGES is
// scoped to objects this role creates within the schema.
const ownerRole = "collector_server_user"

// Execer is satisfied by both *sql.DB and *sql.Tx, so the same helper works
// for autocommit DDL (staging schemas) and inside an existing transaction
// (final-schema merges). Using the context-aware methods keeps callers that
// already thread a context honest.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// CreateSchemaWithGrants creates the schema (idempotent, fatal-on-error) and
// then best-effort grants read access to the reader roles. The grant phase
// never fails the call: roles that do not exist yet are simply skipped, and
// any other grant error is logged and swallowed.
//
// The schema identifier is always quoted via pq.QuoteIdentifier — callers must
// pass the raw (already-sanitised) schema name, never a pre-interpolated SQL
// fragment.
func CreateSchemaWithGrants(ctx context.Context, ex Execer, schema string) error {
	quoted := pq.QuoteIdentifier(schema)

	// 1. CREATE SCHEMA — fatal on error.
	if _, err := ex.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, quoted)); err != nil {
		return fmt.Errorf("pgschema: create schema %q: %w", schema, err)
	}

	// 2. Grants — best-effort. Only emit statements for roles that exist so we
	//    never issue a failing statement (which, in a tx, would poison it).
	grantSchemaReadAccess(ctx, ex, schema, quoted)
	return nil
}

// roleLookup resolves which of the candidate roles exist. It is a package
// variable so tests can substitute a deterministic implementation without a
// live pg_roles table; production always uses existingRoles.
var roleLookup = existingRoles

// grantSchemaReadAccess emits USAGE + default-SELECT grants to whichever reader
// roles currently exist. Anything that goes wrong here is logged and ignored:
// pre-cutover the roles may be absent, and ingest must not hard-fail on that.
func grantSchemaReadAccess(ctx context.Context, ex Execer, schema, quoted string) {
	existing, err := roleLookup(ctx, ex, append(append([]string{}, readerRoles...), ownerRole))
	if err != nil {
		// Could not determine role state (e.g. restricted catalog access);
		// skip grants entirely rather than risk poisoning a transaction.
		log.Printf("[pgschema] skipping grants for schema %q: role lookup failed: %v", schema, err)
		return
	}

	// Filter the reader list down to roles that exist.
	var readers []string
	for _, r := range readerRoles {
		if existing[r] {
			readers = append(readers, r)
		}
	}
	if len(readers) == 0 {
		// Pre-cutover: no reader roles yet. Nothing to grant; this is normal.
		return
	}

	quotedReaders := make([]string, len(readers))
	for i, r := range readers {
		quotedReaders[i] = pq.QuoteIdentifier(r)
	}
	readerList := strings.Join(quotedReaders, ", ")

	// GRANT USAGE ON SCHEMA <schema> TO <readers> — idempotent.
	usage := fmt.Sprintf(`GRANT USAGE ON SCHEMA %s TO %s`, quoted, readerList)
	if _, err := ex.ExecContext(ctx, usage); err != nil {
		log.Printf("[pgschema] best-effort GRANT USAGE on %q failed (ignored): %v", schema, err)
	}

	// ALTER DEFAULT PRIVILEGES only makes sense if the owner role exists.
	if existing[ownerRole] {
		alter := fmt.Sprintf(
			`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT ON TABLES TO %s`,
			pq.QuoteIdentifier(ownerRole), quoted, readerList,
		)
		if _, err := ex.ExecContext(ctx, alter); err != nil {
			log.Printf("[pgschema] best-effort ALTER DEFAULT PRIVILEGES on %q failed (ignored): %v", schema, err)
		}
	}
}

// existingRoles returns the subset of candidate roles present in pg_roles.
// A read-only catalog query, safe to run inside a transaction without
// poisoning it.
func existingRoles(ctx context.Context, ex Execer, candidates []string) (map[string]bool, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT rolname FROM pg_roles WHERE rolname = ANY($1)`,
		pq.Array(candidates),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	present := make(map[string]bool, len(candidates))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return present, nil
}
