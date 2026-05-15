// Package lakehouse provides the LakehouseStore: schema-staging management
// and read-only catalog access over Postgres.
package lakehouse

import (
	"context"
	"database/sql"
	"errors"
)

// CatalogReader exposes read-only metadata about ingested tables.
//
// Used by lakehouse.ResolveQuery to map Od/Property names to physical
// columns and join keys.
type CatalogReader interface {
	// ListTables returns the user-facing table list for a project.
	// Used by the agent to enumerate ingested tables before resolving Od names.
	ListTables(ctx context.Context, projectID string) ([]TableMeta, error)
	// GetColumns returns column metadata for a single table.
	// Position is 1-indexed and follows the order columns were declared.
	GetColumns(ctx context.Context, projectID, table string) ([]ColumnMeta, error)
	// GetRelationships returns the cross-table FK-style edges declared for
	// the project. Direction is From → To with cardinality in Kind.
	GetRelationships(ctx context.Context, projectID string) ([]RelationshipMeta, error)
}

// StagingWriter is the row-write contract for staged ingestion.
//
// Mirrors the forward declaration in ingest/ports.go (canonical home is here).
// Each ingest run creates one StagingWriter, writes rows, and either
// CommitSwaps the staging schema into live or Rollbacks. Close releases the
// underlying DB resources regardless of commit/rollback outcome.
type StagingWriter interface {
	// CreateStaging provisions the staging schema and tables in one shot.
	// Idempotent: re-creating with the same schema name is safe; the staging
	// schema is namespaced per ingest run.
	CreateStaging(ctx context.Context, schema string, ddl []TableDDL) error
	// WriteRows appends rows to a staging table. Columns must match the
	// declared DDL; rows are positional in the same order. Returns the first
	// row's error if any (writers may buffer for batching).
	WriteRows(table string, columns []string, rows [][]any) error
	// CommitSwap atomically promotes the staging schema into the live tables
	// (typically by ALTER SCHEMA RENAME or equivalent). Idempotent only
	// before the first call — re-calling after success is an error.
	CommitSwap(ctx context.Context) error
	// Rollback discards all staged data and drops the staging schema.
	// Safe to call multiple times.
	Rollback(ctx context.Context) error
	// Close releases the underlying DB resources. Always call (defer is fine)
	// regardless of whether CommitSwap or Rollback was invoked.
	Close() error
}

// TableMeta describes one ingested table for catalog read-out.
type TableMeta struct {
	Name        string // physical table name (Postgres relname)
	DisplayName string // user-facing label as shown in the UI / Od config
	RowCount    int64  // approximate row count (sourced from pg_class.reltuples)
}

// ColumnMeta describes one column on an ingested table.
type ColumnMeta struct {
	Name     string // physical column name
	DataType string // Postgres data type (text, integer, numeric, timestamp, ...)
	Nullable bool   // whether the column accepts NULL
	Position int    // 1-indexed ordinal position
}

// RelationshipMeta describes one declared cross-table relationship.
type RelationshipMeta struct {
	From string // "TableA.col" — the originating side
	To   string // "TableB.col" — the referenced side
	Kind string // cardinality: "one-to-many", "many-to-one", "one-to-one"
}

// TableDDL is the staging-side declaration of a table to be created.
// Used by StagingWriter.CreateStaging.
type TableDDL struct {
	Name    string       // physical table name (no schema prefix)
	Columns []ColumnMeta // ordered column list (Position is ignored on input)
	PK      []string     // primary key column names (may be empty)
}

// Store is the concrete root that satisfies CatalogReader and (in a future
// stage) the ingest.StagingTarget contract via OpenStaging. Returned by
// NewStore.
//
// Stage 3B keeps Store minimal: NewStore wraps the *sql.DB and Reader()
// returns the receiver as a CatalogReader. The CatalogReader methods are
// stubs (return nil, nil) — Stage 4+ will port the real queries currently
// inlined in handler_object.go.
type Store struct {
	db *sql.DB
}

// NewStore constructs a Store backed by the given *sql.DB.
// The DB handle is not closed by Store — caller retains ownership.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Reader returns the read-only CatalogReader view of this Store.
// The returned value is the receiver itself, narrowed to the read-only
// interface so callers cannot accidentally invoke staging methods.
func (s *Store) Reader() CatalogReader { return s }

// errStoreNotImplemented is returned by Store catalog methods that have
// not yet been wired to real queries. Returning a typed error (rather
// than a silent (nil, nil)) ensures premature consumers fail loudly
// instead of silently observing empty catalog data.
var errStoreNotImplemented = errors.New("lakehouse.Store: catalog method not yet implemented (handlers still inline their queries; promote in a follow-up)")

// ListTables is a stub. Stage 4+ will populate this by porting the
// catalog query currently inlined in handler_object.go. Returns
// errStoreNotImplemented until then so callers fail fast.
func (s *Store) ListTables(ctx context.Context, projectID string) ([]TableMeta, error) {
	return nil, errStoreNotImplemented
}

// GetColumns is a stub. See ListTables.
func (s *Store) GetColumns(ctx context.Context, projectID, table string) ([]ColumnMeta, error) {
	return nil, errStoreNotImplemented
}

// GetRelationships is a stub. See ListTables.
func (s *Store) GetRelationships(ctx context.Context, projectID string) ([]RelationshipMeta, error) {
	return nil, errStoreNotImplemented
}
