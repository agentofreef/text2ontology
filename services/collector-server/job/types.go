// Package job is the durable background-task queue for collector-server.
//
// Lifecycle: queued → running → succeeded | failed | cancelled.
// Workers claim jobs via FOR UPDATE SKIP LOCKED, write heartbeat_at every 5s,
// and a sweeper marks stale (>2min) running jobs as failed so a restarted
// container can pick up where the dead worker left off.
//
// Handlers are registered per-kind via Runner.RegisterHandler. Each handler
// receives a Reporter to push progress updates (debounced ≤1 DB write/sec).
package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Kind identifies the ingest pipeline a job runs.
type Kind string

const (
	KindFileUpload     Kind = "file_upload"
	KindPostgresSync   Kind = "postgres_sync"
	KindPbitIngest     Kind = "pbit_ingest"
	KindPbixExtract    Kind = "pbix_extract"
	KindWizardConfirm  Kind = "wizard_confirm"
)

// Status maps to ingest_job.status CHECK constraint.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Job is a row from ingest_job loaded for a worker. Progress fields are
// snapshots at claim time; the live values are written via Reporter.
type Job struct {
	ID             string
	DataSourceID   *string // NULL for jobs not tied to a data_source
	ProjectID      string
	Kind           Kind
	Status         Status
	Phase          *string
	Percent        int
	RowsDone       int64
	RowsTotal      int64
	BytesDone      int64
	Message        *string
	WorkerID       *string
	HeartbeatAt    *time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
	Error          *string
	RetryCount     int
	Payload        json.RawMessage
	CancelRequested bool
	CreatedAt      time.Time
	CreatedBy      *string
}

// Handler runs one job kind. Implementers should:
//   - Honour ctx.Done() for cancellation
//   - Periodically check rep.Cancelled() (set when cancel_requested=true)
//   - Call rep.Update(...) at meaningful checkpoints (≥1Hz is fine; the
//     reporter debounces to ≤1 DB write/sec)
//   - Return nil on success, error on failure (becomes ingest_job.error)
type Handler func(ctx context.Context, db *sql.DB, j *Job, rep *Reporter) error

// EnqueueArgs is the input to Enqueue — a thin convenience wrapper for the
// HTTP handlers that want to drop work into the queue and return immediately.
type EnqueueArgs struct {
	DataSourceID *string
	ProjectID    string
	Kind         Kind
	Payload      any // marshalled to JSONB
	CreatedBy    *string
}

// Enqueue inserts a queued job and returns its ID. Workers will pick it up on
// the next claim tick (50ms-1s).
func Enqueue(ctx context.Context, db *sql.DB, a EnqueueArgs) (string, error) {
	payload, err := json.Marshal(a.Payload)
	if err != nil {
		return "", err
	}
	if len(payload) == 0 || string(payload) == "null" {
		payload = []byte("{}")
	}
	var id string
	err = db.QueryRowContext(ctx, `
		INSERT INTO ingest_job (data_source_id, project_id, kind, payload, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5)
		RETURNING id
	`, a.DataSourceID, a.ProjectID, string(a.Kind), payload, a.CreatedBy).Scan(&id)
	return id, err
}
