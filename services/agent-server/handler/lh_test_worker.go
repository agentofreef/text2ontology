package handler

import (
	"context"
	"database/sql"
	"encoding/json"
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
		// Outer panic guard — if the worker poll loop ever panics, the entire
		// background queue would silently die without restarting. Catch and
		// log so the process keeps serving HTTP and the next deploy can fix.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("LH-TEST-WORKER: top-level panic recovered: %v", r)
			}
		}()
		recoverInterruptedRuns(db)
		log.Println("LH-TEST-WORKER: started")
		lhTestWorkerLoop(ctx, db)
	}()
	return done
}

// recoverInterruptedRuns resets runs/cases that were in-flight when the server last stopped.
func recoverInterruptedRuns(db *sql.DB) {
	// Reset running cases back to pending. 'cancelled' cases are intentionally
	// preserved as final-state — they stay cancelled across restarts.
	res, err := db.Exec(`UPDATE ont_test_run_case SET status = 'pending', updated_at = now() WHERE status = 'running'`)
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
	res, err = db.Exec(`UPDATE ont_test_run SET status = 'queued', cancel_requested = false, updated_at = now() WHERE status = 'running'`)
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

// lhTestWorkerLoop polls for queued runs every 2 seconds until ctx is
// cancelled, at which point it stops dequeuing and returns.
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

		rows, err := db.Query(`SELECT r.id, r.suite_id, r.llm_config_id, COALESCE(r.concurrency, 1)
			FROM ont_test_run r
			WHERE r.status = 'queued'
			ORDER BY r.created_at`)
		if err != nil {
			log.Printf("LH-TEST-WORKER: poll error: %v", err)
			continue
		}

		type queuedRun struct {
			ID, SuiteID, LLMConfigID string
			Concurrency              int
		}
		var queued []queuedRun
		for rows.Next() {
			var q queuedRun
			rows.Scan(&q.ID, &q.SuiteID, &q.LLMConfigID, &q.Concurrency)
			queued = append(queued, q)
		}
		rows.Close()

		for _, q := range queued {
			// Skip if already being processed
			if _, loaded := activeRuns.LoadOrStore(q.ID, true); loaded {
				continue
			}
			go processQueuedRun(db, q.ID, q.SuiteID, q.LLMConfigID, q.Concurrency)
		}
	}
}

// processQueuedRun executes all pending cases in a run using the worker pool pattern.
func processQueuedRun(db *sql.DB, runID, suiteID, llmConfigID string, concurrency int) {
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
		db.Exec(`UPDATE ont_test_run SET status = 'error', updated_at = now() WHERE id = $1`, runID)
		return
	}

	// Mark run as running
	db.Exec(`UPDATE ont_test_run SET status = 'running', started_at = COALESCE(started_at, now()), updated_at = now() WHERE id = $1`, runID)

	// Get project ID
	var projectID string
	err := db.QueryRow(`SELECT s.project_id FROM ont_test_suite s JOIN ont_test_run r ON r.suite_id = s.id WHERE r.id = $1`, runID).Scan(&projectID)
	if err != nil {
		log.Printf("LH-TEST-WORKER: run %s — failed to get project: %v", runID, err)
		db.Exec(`UPDATE ont_test_run SET status = 'error', updated_at = now() WHERE id = $1`, runID)
		return
	}

	// Load pending run-cases
	rows, err := db.Query(`SELECT rc.id, rc.user_question, rc.sort_order, COALESCE(rc.code,'')
		FROM ont_test_run_case rc
		WHERE rc.run_id = $1 AND rc.status = 'pending'
		ORDER BY rc.sort_order`, runID)
	if err != nil {
		log.Printf("LH-TEST-WORKER: run %s — failed to load cases: %v", runID, err)
		db.Exec(`UPDATE ont_test_run SET status = 'error', updated_at = now() WHERE id = $1`, runID)
		return
	}
	type pendingRC struct {
		ID, Question, Code string
		SortOrder          int
	}
	var pending []pendingRC
	for rows.Next() {
		var pc pendingRC
		rows.Scan(&pc.ID, &pc.Question, &pc.SortOrder, &pc.Code)
		pending = append(pending, pc)
	}
	rows.Close()

	if len(pending) == 0 {
		log.Printf("LH-TEST-WORKER: run %s — no pending cases, marking completed", runID)
		db.Exec(`UPDATE ont_test_run SET status = 'completed', finished_at = now(), updated_at = now() WHERE id = $1`, runID)
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
					db.Exec(`UPDATE ont_test_run_case SET status='error', execution_error=$1, updated_at=now() WHERE id=$2`,
						errMsg, c.ID)
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
				db.Exec(`UPDATE ont_test_run_case SET status='cancelled', updated_at=now() WHERE id=$1 AND status='pending'`, c.ID)
				resultCh <- lhCaseResult{CaseID: c.ID, Code: c.Code, Index: idx, Question: c.Question, Status: "cancelled"}
				return
			}

			runSem <- struct{}{}       // per-run concurrency gate
			globalLLMSem <- struct{}{} // global concurrency gate
			defer func() { <-globalLLMSem; <-runSem }()

			// Re-check after acquiring the slot (cancel may have arrived while
			// we waited for a slot to free up).
			if atomic.LoadInt32(&cancelled) != 0 {
				db.Exec(`UPDATE ont_test_run_case SET status='cancelled', updated_at=now() WHERE id=$1 AND status='pending'`, c.ID)
				resultCh <- lhCaseResult{CaseID: c.ID, Code: c.Code, Index: idx, Question: c.Question, Status: "cancelled"}
				return
			}

			// Mark case as running
			db.Exec(`UPDATE ont_test_run_case SET status = 'running', updated_at = now() WHERE id = $1`, c.ID)

			result := runLakehouseTestCase(db, projectID, suiteID, c.ID, c.Question, c.Code, idx,
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
			db.Exec(`UPDATE ont_test_run_case SET
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
				result.CaseID)
		}

		// Update run stats incrementally so polling clients see progress
		updateLHRunStats(db, runID)

		// Cooperative cancel check — read cancel_requested from DB on each
		// completed case. Cheap (single indexed lookup) and naturally
		// throttles to once-per-case-duration which is plenty responsive.
		if atomic.LoadInt32(&cancelled) == 0 {
			var cancelReq bool
			if err := db.QueryRow(`SELECT cancel_requested FROM ont_test_run WHERE id = $1`, runID).Scan(&cancelReq); err == nil && cancelReq {
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
		db.Exec(`UPDATE ont_test_run_case SET status='cancelled', updated_at=now() WHERE run_id=$1 AND status='pending'`, runID)
		db.Exec(`UPDATE ont_test_run SET status='cancelled', finished_at=now(), updated_at=now() WHERE id=$1`, runID)
		updateLHRunStats(db, runID)
		log.Printf("LH-TEST-WORKER: run %s — cancelled", runID)
		return
	}

	db.Exec(`UPDATE ont_test_run SET status = 'completed', finished_at = now(), updated_at = now() WHERE id = $1`, runID)
	updateLHRunStats(db, runID)

	log.Printf("LH-TEST-WORKER: run %s — completed", runID)
}
