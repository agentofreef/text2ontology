package job

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// SweepStaleJobs marks 'running' jobs whose heartbeat_at is older than 2
// minutes as failed (error='worker_lost'). Called once at boot + periodically
// by RunSweeper. Idempotent.
//
// Mirrors the wizard.SweepStaleOnBoot pattern (services/collector-server/
// ingest/wizard/resume.go) — fire-and-forget goroutine, log-only errors.
func SweepStaleJobs(db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := db.ExecContext(ctx, `
		UPDATE ingest_job SET
			status       = 'failed',
			completed_at = now(),
			error        = 'worker_lost'
		WHERE status = 'running'
		  AND (heartbeat_at IS NULL OR heartbeat_at < now() - interval '2 minutes')
	`)
	if err != nil {
		log.Printf("job.SweepStaleJobs: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("job.SweepStaleJobs: marked %d stale running job(s) as failed", n)
	}
}

// SweepOldJobs deletes succeeded/failed/cancelled jobs older than 30 days.
// Called once at boot to keep the table small.
func SweepOldJobs(db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := db.ExecContext(ctx, `
		DELETE FROM ingest_job
		WHERE status IN ('succeeded','failed','cancelled')
		  AND completed_at < now() - interval '30 days'
	`)
	if err != nil {
		log.Printf("job.SweepOldJobs: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("job.SweepOldJobs: deleted %d old job(s)", n)
	}
}

// RunSweeper runs SweepStaleJobs at the given interval until ctx is cancelled.
// Caller is expected to launch this in a goroutine: `go r.RunSweeper(ctx, 30s)`.
func (r *Runner) RunSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			SweepStaleJobs(r.db)
		}
	}
}
