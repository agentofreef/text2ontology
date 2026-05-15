package job

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// Runner owns the worker pool + dispatcher. Lifecycle:
//
//	r := job.NewRunner(db, 4)
//	r.RegisterHandler(KindFileUpload, fileingest.HandleFileUploadJob)
//	go r.Start(ctx)         // launches N workers + the in-process sweeper
type Runner struct {
	db          *sql.DB
	dispatcher  *dispatcher
	workerCount int
	workerID    string

	startOnce sync.Once
	stopCh    chan struct{}
}

// NewRunner returns an unstarted runner. Default size is 4 workers; override
// via env COLLECTOR_JOB_WORKERS.
func NewRunner(db *sql.DB, defaultWorkers int) *Runner {
	n := defaultWorkers
	if v := os.Getenv("COLLECTOR_JOB_WORKERS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	return &Runner{
		db:          db,
		dispatcher:  newDispatcher(),
		workerCount: n,
		workerID:    randID(),
		stopCh:      make(chan struct{}),
	}
}

// RegisterHandler is called once at startup per kind, before Start.
func (r *Runner) RegisterHandler(k Kind, h Handler) {
	r.dispatcher.register(k, h)
}

// Start launches workerCount goroutines. Returns immediately. Idempotent.
func (r *Runner) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		log.Printf("job.Runner: starting %d worker(s) workerID=%s", r.workerCount, r.workerID)
		for i := 0; i < r.workerCount; i++ {
			go r.workerLoop(ctx, i)
		}
	})
}

// Stop signals all workers to exit on the next claim tick.
func (r *Runner) Stop() {
	close(r.stopCh)
}

// workerLoop runs until ctx is cancelled or Stop() is called. Polls every
// 500ms when idle; immediately fetches the next job after finishing one.
func (r *Runner) workerLoop(ctx context.Context, idx int) {
	idleBackoff := 500 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		default:
		}

		j, err := r.claimNext(ctx)
		if err != nil {
			log.Printf("job.worker[%d]: claim error: %v", idx, err)
			time.Sleep(idleBackoff)
			continue
		}
		if j == nil {
			time.Sleep(idleBackoff)
			continue
		}

		r.runJob(ctx, j)
	}
}

// claimNext atomically pulls the oldest queued job and marks it running.
// Returns (nil, nil) when no work available — caller should sleep.
func (r *Runner) claimNext(ctx context.Context) (*Job, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE ingest_job SET
			status       = 'running',
			worker_id    = $1,
			started_at   = COALESCE(started_at, now()),
			heartbeat_at = now()
		WHERE id = (
			SELECT id FROM ingest_job
			WHERE status = 'queued'
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, data_source_id, project_id, kind, status, payload, retry_count, created_at, created_by
	`, r.workerID)
	j := &Job{}
	err := row.Scan(
		&j.ID, &j.DataSourceID, &j.ProjectID, &j.Kind, &j.Status,
		&j.Payload, &j.RetryCount, &j.CreatedAt, &j.CreatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// runJob executes one claimed job: starts a heartbeat ticker + flusher,
// invokes the registered handler, writes terminal status.
func (r *Runner) runJob(parentCtx context.Context, j *Job) {
	jobCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	rep := newReporter(r.db, j.ID)
	go rep.runFlusher(jobCtx)
	go r.heartbeatLoop(jobCtx, j.ID, rep)

	defer func() {
		// Final flush so the last progress snapshot lands before status changes.
		flushCtx, flushCancel := context.WithTimeout(parentCtx, 3*time.Second)
		rep.Flush(flushCtx)
		flushCancel()
	}()

	defer func() {
		// Panic safety: a handler that panics still gets a terminal status.
		if rec := recover(); rec != nil {
			log.Printf("job %s (%s): panic: %v", j.ID, j.Kind, rec)
			r.markTerminal(parentCtx, j.ID, StatusFailed, fmt.Sprintf("panic: %v", rec))
		}
	}()

	h, err := r.dispatcher.lookup(j.Kind)
	if err != nil {
		log.Printf("job %s: %v", j.ID, err)
		r.markTerminal(parentCtx, j.ID, StatusFailed, err.Error())
		return
	}

	log.Printf("job %s: kind=%s starting", j.ID, j.Kind)
	err = h(jobCtx, r.db, j, rep)

	if rep.Cancelled() {
		r.markTerminal(parentCtx, j.ID, StatusCancelled, "cancelled by user")
		return
	}
	if err != nil {
		log.Printf("job %s: kind=%s failed: %v", j.ID, j.Kind, err)
		r.markTerminal(parentCtx, j.ID, StatusFailed, err.Error())
		return
	}
	r.markTerminal(parentCtx, j.ID, StatusSucceeded, "")
}

// heartbeatLoop pings heartbeat_at every 5s and reads cancel_requested.
// Stops when ctx is cancelled.
func (r *Runner) heartbeatLoop(ctx context.Context, jobID string, rep *Reporter) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	// Immediate first beat (claim already wrote heartbeat_at, but we still
	// want to populate cancel_requested observation for the handler).
	r.beat(ctx, jobID, rep)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.beat(ctx, jobID, rep)
		}
	}
}

func (r *Runner) beat(ctx context.Context, jobID string, rep *Reporter) {
	var cancelReq bool
	err := r.db.QueryRowContext(ctx, `
		UPDATE ingest_job SET heartbeat_at = now()
		WHERE id = $1 AND status = 'running'
		RETURNING cancel_requested
	`, jobID).Scan(&cancelReq)
	if err != nil {
		// Not necessarily a problem — could be that status already moved off
		// running (e.g. sweeper marked failed). Don't spam logs.
		return
	}
	if cancelReq {
		rep.SetCancelled()
	}
}

func (r *Runner) markTerminal(ctx context.Context, jobID string, status Status, errMsg string) {
	var msgArg interface{}
	if errMsg != "" {
		msgArg = errMsg
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE ingest_job SET
			status       = $2,
			completed_at = now(),
			error        = $3
		WHERE id = $1
	`, jobID, string(status), msgArg)
	if err != nil {
		log.Printf("job %s: failed to write terminal status %s: %v", jobID, status, err)
	}
}

func randID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%s", host, hex.EncodeToString(b))
}
