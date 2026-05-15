// Package ingest provides the IngestionPort that accepts upstream data
// sources (PBIT / Excel / CSV) and routes them through the appropriate
// parser → header-binder → LakehouseStore.StagingWriter pipeline.
package ingest

import (
	"context"
	"io"
)

// SourceKind identifies the upstream artifact type.
type SourceKind string

const (
	// SourcePBIT is a Power BI Template (.pbit) archive.
	SourcePBIT SourceKind = "pbit"
	// SourceExcel is an .xlsx workbook (one or more sheets).
	SourceExcel SourceKind = "excel"
	// SourceCSV is a single .csv file.
	SourceCSV SourceKind = "csv"
)

// Source is one upstream artifact to ingest.
type Source struct {
	Kind        SourceKind    // SourcePBIT / SourceExcel / SourceCSV
	Filename    string        // original filename (audit + header binding)
	Reader      io.ReadSeeker // seekable so PBIT JSON can be re-read
	ProjectID   string        // owning project
	BindingHint string        // optional sheet/header hint for Excel
}

// IngestResult is what the caller (HTTP handler) reports back to the user.
type IngestResult struct {
	StagingSchema string           // e.g. "staging_2026_04_19_abc123"
	TablesWritten []string         // physical table names in the staging schema
	RowCounts     map[string]int64 // table name → row count actually written
	Warnings      []string         // header-binding mismatches, type coercions, etc.
}

// IngestionPort accepts a typed Source and routes it to the staging target.
//
// The only concrete implementation is in pbitlakehouse/ (PBIT).
// The HTTP layer composes implementations via a registry.
type IngestionPort interface {
	// Ingest parses the Source and writes its tables/rows into a fresh
	// staging schema obtained from target.OpenStaging. On success returns
	// the staging schema name + table list; on error the StagingWriter is
	// rolled back automatically.
	Ingest(ctx context.Context, src Source, target StagingTarget) (IngestResult, error)
}

// StagingTarget is the LakehouseStore-side handle exposed to the ingest layer.
// Defined here (not in lakehouse package) to keep ingest's import set minimal —
// ingest does not import lakehouse; lakehouse provides a value satisfying this.
type StagingTarget interface {
	// OpenStaging provisions a fresh staging schema with the given name and
	// returns a writer scoped to it. The caller (an IngestionPort) must
	// eventually call CommitSwap or Rollback on the returned writer.
	OpenStaging(ctx context.Context, schemaName string) (StagingWriter, error)
}

// StagingWriter is the row-write contract.
//
// Forward-declared here to break the import cycle between ingest and lakehouse.
// The canonical implementation lives in lakehouse and conforms to this signature.
type StagingWriter interface {
	// WriteRows appends rows to a staging table. Columns are in declared order.
	WriteRows(table string, columns []string, rows [][]any) error
	// CommitSwap atomically promotes the staging schema into live tables.
	CommitSwap(ctx context.Context) error
	// Rollback discards all staged data and drops the staging schema.
	Rollback(ctx context.Context) error
	// Close releases the underlying DB resources. Safe after CommitSwap or Rollback.
	Close() error
}
