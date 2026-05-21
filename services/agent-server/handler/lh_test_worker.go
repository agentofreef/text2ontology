package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lakehouse2ontology/llmclient"
)

// globalLLMSem limits the total number of concurrent LLM calls across all runs.
var globalLLMSem = make(chan struct{}, 10)

// activeRuns tracks which run IDs are currently being processed to avoid double-pickup.
var activeRuns sync.Map

// StartLHTestWorker launches the background worker that processes queued test
// runs with a never-cancelled context. Retained for callers/tests that don't
// participate in graceful shutdown.
func StartLHTestWorker(db *sql.DB) {
	StartLHTestWorkerCtx(context.Background(), db)
}

// StartLHTestWorkerCtx launches the background worker tied to ctx. When ctx is
// cancelled (graceful shutdown) the poll loop stops claiming new runs and the
// goroutine exits, so the worker never issues fresh queries against a
// soon-to-be-closed DB pool. In-flight run processing already completes its
// own cases; this only governs the dequeue loop.
//
// The returned channel is closed once the poll loop has fully exited, so the
// caller can block on it during shutdown (before db.Close()) to guarantee the
// dequeue loop is no longer touching the DB.
//
// Called once from main.go at startup with the shutdown ctx.
func StartLHTestWorkerCtx(ctx context.Context, db *sql.DB) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		recoverInterruptedRuns(ctx, db)
		log.Println("LH-TEST-WORKER: started")
		// Supervisor loop: if the poll loop panics, recover and restart it after
		// a short backoff instead of letting the background queue die silently
		// (which would strand every queued test run until the next deploy).
		// Stops when ctx is cancelled (graceful shutdown).
		const restartBackoff = 2 * time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			panicked := runWorkerLoopGuarded(ctx, db)
			if !panicked {
				// Clean return means ctx was cancelled — stop supervising.
				return
			}
			log.Printf("LH-TEST-WORKER: poll loop panicked, restarting in %s", restartBackoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(restartBackoff):
			}
		}
	}()
	return done
}

// runWorkerLoopGuarded runs one instance of the poll loop under a panic guard.
// Returns true if the loop panicked (caller should restart), false if it
// returned normally (ctx cancelled — caller should stop).
func runWorkerLoopGuarded(ctx context.Context, db *sql.DB) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("LH-TEST-WORKER: top-level panic recovered: %v", r)
			panicked = true
		}
	}()
	lhTestWorkerLoop(ctx, db)
	return false
}

// recoverInterruptedRuns resets runs/cases that were in-flight when the server
// last stopped. NOTE on multi-replica safety: this resets ALL 'running' rows,
// not just this replica's, so it must only run at startup before the poll loop
// claims work. With per-run SKIP LOCKED claiming, a row another live replica is
// actively processing is FOR UPDATE-locked by that replica's claim transaction
// only momentarily, so a blanket reset here could in theory re-queue a peer's
// run; in practice startup recovery races are tolerated because the claim is
// idempotent (a re-queued run is simply re-claimed exactly once).
func recoverInterruptedRuns(ctx context.Context, db *sql.DB) {
	// Reset running cases back to pending. 'cancelled' cases are intentionally
	// preserved as final-state — they stay cancelled across restarts.
	res, err := db.ExecContext(ctx, `UPDATE ont_test_run_case SET status = 'pending', updated_at = now() WHERE status = 'running'`)
	if err != nil {
		log.Printf("LH-TEST-WORKER: recover cases error: %v", err)
	}
	casesReset := int64(0)
	if res != nil {
		casesReset, _ = res.RowsAffected()
	}

	// Reset running runs back to queued AND clear stale cancel_requested so
	// the worker doesn't immediately re-cancel a recovered run that the user
	// requested cancellation for in a previous process. If the user still
	// wants it cancelled they can hit cancel again.
	res, err = db.ExecContext(ctx, `UPDATE ont_test_run SET status = 'queued', cancel_requested = false, updated_at = now() WHERE status = 'running'`)
	if err != nil {
		log.Printf("LH-TEST-WORKER: recover runs error: %v", err)
	}
	runsReset := int64(0)
	if res != nil {
		runsReset, _ = res.RowsAffected()
	}

	if runsReset > 0 || casesReset > 0 {
		log.Printf("LH-TEST-WORKER: recovered %d runs, %d cases", runsReset, casesReset)
	}
}

// queuedRun carries the columns needed to dispatch a claimed run.
type queuedRun struct {
	ID, SuiteID, LLMConfigID string
	Concurrency              int
}

// claimRunSQL atomically claims the oldest queued run and flips it to
// 'running' in a single statement. FOR UPDATE SKIP LOCKED is the multi-replica
// correctness mechanism: concurrent worker loops (across replicas or
// goroutines) never claim the same run because each claim transaction locks
// the candidate row and peers skip locked rows instead of blocking.
const claimRunSQL = `
	UPDATE ont_test_run SET
		status     = 'running',
		started_at = COALESCE(started_at, now()),
		updated_at = now()
	WHERE id = (
		SELECT id FROM ont_test_run
		WHERE status = 'queued'
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	)
	RETURNING id, suite_id, COALESCE(llm_config_id::text, ''), COALESCE(concurrency, 1)`

// claimNextRun runs claimRunSQL and returns the claimed run, or (nil, nil) when
// no work is available. This is the correctness boundary for multi-replica
// safety — the in-process activeRuns guard is only a cheap local optimisation,
// not the source of truth.
func claimNextRun(ctx context.Context, db *sql.DB) (*queuedRun, error) {
	q := &queuedRun{}
	err := db.QueryRowContext(ctx, claimRunSQL).
		Scan(&q.ID, &q.SuiteID, &q.LLMConfigID, &q.Concurrency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return q, nil
}

// lhTestWorkerLoop polls for queued runs every 2 seconds until ctx is
// cancelled, at which point it stops dequeuing and returns. Each tick it claims
// runs one-at-a-time via FOR UPDATE SKIP LOCKED until the queue is drained,
// dispatching each claimed run to a background goroutine.
func lhTestWorkerLoop(ctx context.Context, db *sql.DB) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("LH-TEST-WORKER: shutdown requested, stopping poll loop")
			return
		case <-ticker.C:
		}

		// Drain all currently-queued runs this tick; each claim is atomic.
		for {
			if ctx.Err() != nil {
				return
			}
			q, err := claimNextRun(ctx, db)
			if err != nil {
				log.Printf("LH-TEST-WORKER: claim error: %v", err)
				break
			}
			if q == nil {
				break // queue empty
			}
			// Local in-flight guard: belt-and-braces against a single replica
			// double-dispatching the same id within one process. Correctness
			// across replicas comes from SKIP LOCKED above, not this map.
			if _, loaded := activeRuns.LoadOrStore(q.ID, true); loaded {
				continue
			}
			go processQueuedRun(ctx, db, q.ID, q.SuiteID, q.LLMConfigID, q.Concurrency)
		}
	}
}

// processQueuedRun executes all pending cases in a run using the worker pool
// pattern. ctx is the worker context (cancelled on graceful shutdown); it
// bounds the run's DB/LLM calls so a shutdown doesn't strand work mid-flight.
func processQueuedRun(ctx context.Context, db *sql.DB, runID, suiteID, llmConfigID string, concurrency int) {
	defer activeRuns.Delete(runID)

	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 10 {
		concurrency = 10
	}

	// Resolve LLM config
	llBaseURL, llAPIKey, llModelName, _, llIsToolCall, _, llVendor := llmclient.GetConfigByID(db, llmConfigID)
	if llBaseURL == "" {
		log.Printf("LH-TEST-WORKER: run %s — LLM config unavailable (id: %s)", runID, llmConfigID)
		if _, err := db.ExecContext(ctx, `UPDATE ont_test_run SET status = 'error', updated_at = now() WHERE id = $1`, runID); err != nil {
			log.Printf("LH-TEST-WORKER: run %s — failed to mark error: %v", runID, err)
		}
		return
	}

	// NOTE: the run is already marked 'running' atomically by claimNextRun, so
	// there is no separate "mark as running" UPDATE here anymore.

	// Get project ID
	var projectID string
	err := db.QueryRowContext(ctx, `SELECT s.project_id FROM ont_test_suite s JOIN ont_test_run r ON r.suite_id = s.id WHERE r.id = $1`, runID).Scan(&projectID)
	if err != nil {
		log.Printf("LH-TEST-WORKER: run %s — failed to get project: %v", runID, err)
		if _, e := db.ExecContext(ctx, `UPDATE ont_test_run SET status = 'error', updated_at = now() WHERE id = $1`, runID); e != nil {
			log.Printf("LH-TEST-WORKER: run %s — failed to mark error: %v", runID, e)
		}
		return
	}

	// Load pending run-cases
	rows, err := db.QueryContext(ctx, `SELECT rc.id, rc.user_question, rc.sort_order, COALESCE(rc.code,'')
		FROM ont_test_run_case rc
		WHERE rc.run_id = $1 AND rc.status = 'pending'
		ORDER BY rc.sort_order`, runID)
	if err != nil {
		log.Printf("LH-TEST-WORKER: run %s — failed to load cases: %v", runID, err)
		if _, e := db.ExecContext(ctx, `UPDATE ont_test_run SET status = 'error', updated_at = now() WHERE id = $1`, runID); e != nil {
			log.Printf("LH-TEST-WORKER: run %s — failed to mark error: %v", runID, e)
		}
		return
	}
	type pendingRC struct {
		ID, Question, Code string
		SortOrder          int
	}
	var pending []pendingRC
	for rows.Next() {
		var pc pendingRC
		if err := rows.Scan(&pc.ID, &pc.Question, &pc.SortOrder, &pc.Code); err != nil {
			log.Printf("LH-TEST-WORKER: run %s — scan case error: %v", runID, err)
			continue
		}
		pending = append(pending, pc)
	}
	rows.Close()

	if len(pending) == 0 {
		log.Printf("LH-TEST-WORKER: run %s — no pending cases, marking completed", runID)
		if _, err := db.ExecContext(ctx, `UPDATE ont_test_run SET status = 'completed', finished_at = now(), updated_at = now() WHERE id = $1`, runID); err != nil {
			log.Printf("LH-TEST-WORKER: run %s — failed to mark completed: %v", runID, err)
		}
		updateLHRunStats(db, runID)
		return
	}

	log.Printf("LH-TEST-WORKER: run %s — executing %d cases (concurrency=%d)", runID, len(pending), concurrency)

	// Worker pool with dual semaphore
	resultCh := make(chan lhCaseResult, len(pending))
	var wg sync.WaitGroup
	runSem := make(chan struct{}, concurrency)

	// cancelled is read by every worker before claiming a slot. Set to 1 by
	// the result-collector loop the moment it observes cancel_requested=true
	// in the DB. Workers that haven't started yet skip straight to a synthetic
	// 'cancelled' result; in-flight workers complete normally (no way to
	// interrupt a running LLM call without losing the response).
	// Using int32 + sync/atomic (not atomic.Bool) for Go 1.18 compatibility.
	var cancelled int32

	for i, pc := range pending {
		wg.Add(1)
		go func(idx int, c pendingRC) {
			defer wg.Done()

			// Panic recovery for each worker — without this, a panic inside
			// runLakehouseTestCase (e.g. nil deref in tool dispatch) would leave
			// the case stuck in 'running' forever AND short the result channel
			// by one, causing the result-collection loop to block until the
			// run was manually killed. Mark the case 'error' and push a synthetic
			// error result so the collector sees the same N results it expects.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("LH-TEST-WORKER: case %s panic recovered: %v", c.ID, r)
					errMsg := fmt.Sprintf("worker panic: %v", r)
					if _, e := db.ExecContext(ctx, `UPDATE ont_test_run_case SET status='error', execution_error=$1, updated_at=now() WHERE id=$2`,
						errMsg, c.ID); e != nil {
						log.Printf("LH-TEST-WORKER: case %s — failed to mark panic error: %v", c.ID, e)
					}
					// Non-blocking send — channel is buffered to len(pending),
					// so this should always succeed; the default arm is a
					// belt-and-braces guard against a future capacity change.
					select {
					case resultCh <- lhCaseResult{
						CaseID:         c.ID,
						Code:           c.Code,
						Index:          idx,
						Question:       c.Question,
						Status:         "error",
						ExecutionError: errMsg,
					}:
					default:
					}
				}
			}()

			// Cancel check before claiming a semaphore slot — workers queued
			// behind active ones skip immediately, so a cancel actually frees
			// the run within roughly one-case-duration instead of N/concurrency.
			if atomic.LoadInt32(&cancelled) != 0 {
				if _, e := db.ExecContext(ctx, `UPDATE ont_test_run_case SET status='cancelled', updated_at=now() WHERE id=$1 AND status='pending'`, c.ID); e != nil {
					log.Printf("LH-TEST-WORKER: case %s — failed to mark cancelled: %v", c.ID, e)
				}
				resultCh <- lhCaseResult{CaseID: c.ID, Code: c.Code, Index: idx, Question: c.Question, Status: "cancelled"}
				return
			}

			runSem <- struct{}{}       // per-run concurrency gate
			globalLLMSem <- struct{}{} // global concurrency gate
			defer func() { <-globalLLMSem; <-runSem }()

			// Re-check after acquiring the slot (cancel may have arrived while
			// we waited for a slot to free up).
			if atomic.LoadInt32(&cancelled) != 0 {
				if _, e := db.ExecContext(ctx, `UPDATE ont_test_run_case SET status='cancelled', updated_at=now() WHERE id=$1 AND status='pending'`, c.ID); e != nil {
					log.Printf("LH-TEST-WORKER: case %s — failed to mark cancelled: %v", c.ID, e)
				}
				resultCh <- lhCaseResult{CaseID: c.ID, Code: c.Code, Index: idx, Question: c.Question, Status: "cancelled"}
				return
			}

			// Mark case as running
			if _, e := db.ExecContext(ctx, `UPDATE ont_test_run_case SET status = 'running', updated_at = now() WHERE id = $1`, c.ID); e != nil {
				log.Printf("LH-TEST-WORKER: case %s — failed to mark running: %v", c.ID, e)
			}

			result := runLakehouseTestCase(ctx, db, projectID, suiteID, c.ID, c.Question, c.Code, idx,
				llBaseURL, llAPIKey, llModelName, llIsToolCall, llVendor)

			resultCh <- result
		}(i, pc)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results and persist
	for result := range resultCh {
		// Persist case result. Cancelled cases skip the heavy UPDATE because
		// the worker already wrote 'cancelled' before sending the synthetic
		// result, and there are no LLM-side fields to record.
		if result.Status != "cancelled" {
			fcJSON, _ := json.Marshal(result.FunctionCalls)
			if _, e := db.ExecContext(ctx, `UPDATE ont_test_run_case SET
				status = $1, generated_sql = $2, execution_status = $3,
				execution_result = $4, execution_error = $5, final_answer = $6,
				function_calls = $7::jsonb, duration_ms = $8, model_name = $9,
				prompt_tokens = $10, completion_tokens = $11, total_tokens = $12,
				updated_at = now()
				WHERE id = $13`,
				result.Status, result.GeneratedSQL, result.ExecutionStatus,
				result.ExecutionResult, result.ExecutionError, result.FinalAnswer,
				string(fcJSON), result.DurationMs, result.ModelName,
				result.PromptTokens, result.CompletionTokens, result.TotalTokens,
				result.CaseID); e != nil {
				log.Printf("LH-TEST-WORKER: case %s — failed to persist result: %v", result.CaseID, e)
			}
		}

		// Update run stats incrementally so polling clients see progress
		updateLHRunStats(db, runID)

		// Cooperative cancel check — read cancel_requested from DB on each
		// completed case. Cheap (single indexed lookup) and naturally
		// throttles to once-per-case-duration which is plenty responsive.
		if atomic.LoadInt32(&cancelled) == 0 {
			var cancelReq bool
			if err := db.QueryRowContext(ctx, `SELECT cancel_requested FROM ont_test_run WHERE id = $1`, runID).Scan(&cancelReq); err == nil && cancelReq {
				atomic.StoreInt32(&cancelled, 1)
				log.Printf("LH-TEST-WORKER: run %s — cancel requested, draining workers", runID)
			}
		}
	}

	// Mark run final state. If cancellation was requested, anything still
	// 'pending' must be flipped to 'cancelled' too — recover-on-startup logic
	// looks for 'pending' (and resets 'running' → 'pending') and would re-pick
	// them up otherwise.
	if atomic.LoadInt32(&cancelled) != 0 {
		if _, e := db.ExecContext(ctx, `UPDATE ont_test_run_case SET status='cancelled', updated_at=now() WHERE run_id=$1 AND status='pending'`, runID); e != nil {
			log.Printf("LH-TEST-WORKER: run %s — failed to cancel pending cases: %v", runID, e)
		}
		if _, e := db.ExecContext(ctx, `UPDATE ont_test_run SET status='cancelled', finished_at=now(), updated_at=now() WHERE id=$1`, runID); e != nil {
			log.Printf("LH-TEST-WORKER: run %s — failed to mark cancelled: %v", runID, e)
		}
		updateLHRunStats(db, runID)
		log.Printf("LH-TEST-WORKER: run %s — cancelled", runID)
		return
	}

	if _, e := db.ExecContext(ctx, `UPDATE ont_test_run SET status = 'completed', finished_at = now(), updated_at = now() WHERE id = $1`, runID); e != nil {
		log.Printf("LH-TEST-WORKER: run %s — failed to mark completed: %v", runID, e)
	}
	updateLHRunStats(db, runID)

	log.Printf("LH-TEST-WORKER: run %s — completed", runID)
}
