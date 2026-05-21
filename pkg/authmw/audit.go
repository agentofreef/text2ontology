// Package authmw provides bearer-token HTTP middleware and audit logging
// for the lakehouse2ontology service split. See §3.5.4 of the consensus plan.
package authmw

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// AuditEvent captures one auditable internal-service call.
// Fields match §3.5.4: ts, request_id, caller_service, callee_service,
// on_behalf_of_user, project_id, endpoint.
type AuditEvent struct {
	Timestamp     time.Time `json:"ts"`
	RequestID     string    `json:"request_id,omitempty"`
	CallerService string    `json:"caller_service"`
	OnBehalfOf    string    `json:"on_behalf_of"`
	ProjectID     string    `json:"project_id"`
	Path          string    `json:"path"`
	Method        string    `json:"method"`
	StatusCode    int       `json:"status_code"`
}

// AuditWriter is the interface satisfied by any audit log sink.
type AuditWriter interface {
	WriteAudit(ctx context.Context, e AuditEvent) error
}

// StdoutAuditWriter writes JSON-lines to stderr (separate from app log stdout).
// Phase 0 implementation; Phase 2 may swap to a DB or file sink.
type StdoutAuditWriter struct {
	mu sync.Mutex
}

// NewStdoutAuditWriter returns a ready StdoutAuditWriter.
func NewStdoutAuditWriter() *StdoutAuditWriter { return &StdoutAuditWriter{} }

// WriteAudit encodes e as a single JSON line to stderr.
// The mutex ensures concurrent goroutines do not interleave partial JSON lines.
func (s *StdoutAuditWriter) WriteAudit(_ context.Context, e AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.NewEncoder(os.Stderr).Encode(e)
}

// DBAuditWriter persists audit events to the ont_audit_log table so the audit
// trail survives container restarts (the stderr-only writer loses everything on
// `docker logs` rotation / `down`). Retention is the operator's responsibility
// (see the table comment in docs/schema/schema.sql).
//
// Durability vs. availability: the audit write must never block or fail a real
// request. On any DB error (pool down, table missing pre-migration) it falls
// back to the stderr writer so the record is still emitted somewhere and the
// caller's request proceeds.
type DBAuditWriter struct {
	db       *sql.DB
	fallback *StdoutAuditWriter
}

// NewDBAuditWriter returns an audit writer backed by ont_audit_log with a
// stderr fallback. Pass the same *sql.DB the service already uses.
func NewDBAuditWriter(db *sql.DB) *DBAuditWriter {
	return &DBAuditWriter{db: db, fallback: NewStdoutAuditWriter()}
}

// WriteAudit inserts one row into ont_audit_log. All identifier columns are
// stored as TEXT so an odd value can never abort the insert (and force the
// fallback) on a type cast. On any failure it logs to stderr instead.
func (d *DBAuditWriter) WriteAudit(ctx context.Context, e AuditEvent) error {
	if d == nil || d.db == nil {
		return d.fallback.WriteAudit(ctx, e)
	}
	ts := e.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO ont_audit_log
		   (ts, request_id, caller_service, on_behalf_of, project_id, path, method, status_code)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		ts, e.RequestID, e.CallerService, e.OnBehalfOf, e.ProjectID, e.Path, e.Method, e.StatusCode)
	if err != nil {
		return d.fallback.WriteAudit(ctx, e)
	}
	return nil
}
