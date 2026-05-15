// Package authmw provides bearer-token HTTP middleware and audit logging
// for the lakehouse2ontology service split. See §3.5.4 of the consensus plan.
package authmw

import (
	"context"
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
