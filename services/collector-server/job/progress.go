package job

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"time"
)

// Reporter pushes progress updates for a running job. It debounces writes to
// at most one DB UPDATE per writeInterval (default 1s) so handlers can call
// Update() in tight loops without flooding the database.
//
// Reporter is goroutine-safe: handlers may share it across worker goroutines
// (e.g. one per sheet of a multi-sheet xlsx).
type Reporter struct {
	db       *sql.DB
	jobID    string
	interval time.Duration

	// Live snapshot — atomically updated by Update(); read by flusher.
	mu     sync.Mutex
	dirty  bool
	phase  string
	pct    int
	rows   int64
	total  int64
	bytes  int64
	msg    string

	// Liveness — whether a flush is in flight (also serves as final-flush gate).
	cancelled atomic.Bool
}

// newReporter is package-internal; the worker creates one per claimed job.
func newReporter(db *sql.DB, jobID string) *Reporter {
	return &Reporter{
		db:       db,
		jobID:    jobID,
		interval: time.Second,
	}
}

// Update records a new progress snapshot. Returns immediately; the actual
// DB write is debounced (≤1 write/sec by default).
//
// Empty/zero arguments are ignored (i.e. pass "" or 0 to leave a field
// unchanged from the previous Update). Negative percent is clamped.
func (r *Reporter) Update(phase string, pct int, rowsDone, rowsTotal, bytesDone int64, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if phase != "" {
		r.phase = phase
	}
	if pct >= 0 {
		if pct > 100 {
			pct = 100
		}
		r.pct = pct
	}
	if rowsDone > 0 {
		r.rows = rowsDone
	}
	if rowsTotal > 0 {
		r.total = rowsTotal
	}
	if bytesDone > 0 {
		r.bytes = bytesDone
	}
	if msg != "" {
		r.msg = msg
	}
	r.dirty = true
}

// SetCancelled records that the worker observed cancel_requested=true; the
// next Flush will write status='cancelled' instead of just progress.
func (r *Reporter) SetCancelled() { r.cancelled.Store(true) }

// Cancelled reports whether the most-recent heartbeat saw cancel_requested.
// Handlers should poll this in long inner loops to bail out early.
func (r *Reporter) Cancelled() bool { return r.cancelled.Load() }

// runFlusher is launched by the worker; it drains progress updates to DB
// every interval until ctx is cancelled. The final flush is performed by
// the worker via Flush() before terminal status is written.
func (r *Reporter) runFlusher(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Flush(ctx)
		}
	}
}

// Flush writes the current progress snapshot to ingest_job (best-effort —
// errors are logged silently). Safe to call from any goroutine.
func (r *Reporter) Flush(ctx context.Context) {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	phase, pct, rows, total, bytes, msg := r.phase, r.pct, r.rows, r.total, r.bytes, r.msg
	r.dirty = false
	r.mu.Unlock()

	_, _ = r.db.ExecContext(ctx, `
		UPDATE ingest_job SET
			phase      = COALESCE(NULLIF($2,''), phase),
			percent    = $3,
			rows_done  = $4,
			rows_total = $5,
			bytes_done = $6,
			message    = COALESCE(NULLIF($7,''), message)
		WHERE id = $1
	`, r.jobID, phase, pct, rows, total, bytes, msg)
}
