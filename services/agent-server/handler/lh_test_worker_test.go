package handler

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/lib/pq"
)

// TestClaimRunSQL_UsesSkipLocked is the W5-1 regression guard: the run-claim
// statement MUST use FOR UPDATE SKIP LOCKED so that multiple worker replicas
// (or goroutines) can poll the same queue without ever claiming the same run
// twice. A plain SELECT WHERE status='queued' would let two replicas pick the
// same row and double-run a test. This unit test runs without a database.
func TestClaimRunSQL_UsesSkipLocked(t *testing.T) {
	if !strings.Contains(claimRunSQL, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("claimRunSQL must use FOR UPDATE SKIP LOCKED for multi-replica safety; got:\n%s", claimRunSQL)
	}
	// The claim must also mutate status to 'running' atomically (a SELECT-only
	// claim would not actually take ownership of the row).
	if !strings.Contains(claimRunSQL, "UPDATE ont_test_run") {
		t.Fatalf("claimRunSQL must atomically UPDATE the run to 'running'; got:\n%s", claimRunSQL)
	}
	if !strings.Contains(claimRunSQL, "status     = 'running'") && !strings.Contains(claimRunSQL, "status = 'running'") {
		t.Fatalf("claimRunSQL must set status='running'; got:\n%s", claimRunSQL)
	}
}

// workerTestDB opens a Postgres connection from DATABASE_URL. Skips the test
// when DATABASE_URL is unset or the DB is unreachable — matches the pattern in
// intent_enum_ref_test.go / handler_agent_builder_test.go.
func workerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping LH-test-worker claim integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("DB unreachable, skipping: %v", err)
	}
	return db
}

// TestClaimNextRun_NoDoubleClaim is the W5-1 integration test: two concurrent
// claim loops race over a queue of N runs against ONE database, and every run
// must be claimed by exactly one loop. This proves the SKIP LOCKED claim is
// safe across replicas (each goroutine simulates a separate worker process).
//
// It seeds its own project-scoped suite + runs so it never touches existing
// data, and tears everything down via the ON DELETE CASCADE from suite → run.
func TestClaimNextRun_NoDoubleClaim(t *testing.T) {
	db := workerTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// Need an existing project to satisfy ont_test_suite.project_id FK.
	var projectID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM project ORDER BY created_at LIMIT 1`).Scan(&projectID); err != nil {
		t.Skipf("no project row available for test: %v", err)
	}

	// Create an isolated suite; cascade-deletes its runs on cleanup.
	var suiteID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO ont_test_suite (project_id, name) VALUES ($1, $2) RETURNING id`,
		projectID, "w5-claim-test-suite").Scan(&suiteID); err != nil {
		t.Fatalf("insert suite: %v", err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM ont_test_suite WHERE id = $1`, suiteID); err != nil {
			t.Logf("cleanup suite %s: %v", suiteID, err)
		}
	}()

	const nRuns = 25
	runIDs := make(map[string]bool, nRuns)
	for i := 0; i < nRuns; i++ {
		var id string
		if err := db.QueryRowContext(ctx,
			`INSERT INTO ont_test_run (suite_id, title, status, concurrency)
			 VALUES ($1, $2, 'queued', 1) RETURNING id`,
			suiteID, "w5-claim-run").Scan(&id); err != nil {
			t.Fatalf("insert run %d: %v", i, err)
		}
		runIDs[id] = true
	}

	// Two concurrent claim loops, each draining until the queue is empty.
	// Record every claimed id; a double-claim shows up as a duplicate.
	var mu sync.Mutex
	claimCounts := make(map[string]int, nRuns)
	claimLoop := func(wg *sync.WaitGroup) {
		defer wg.Done()
		for {
			q, err := claimNextRun(ctx, db)
			if err != nil {
				t.Errorf("claimNextRun error: %v", err)
				return
			}
			if q == nil {
				return // queue drained
			}
			mu.Lock()
			claimCounts[q.ID]++
			mu.Unlock()
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go claimLoop(&wg)
	go claimLoop(&wg)
	wg.Wait()

	// Every seeded run must be claimed exactly once; no extras, no duplicates.
	if len(claimCounts) != nRuns {
		t.Fatalf("expected %d distinct runs claimed, got %d", nRuns, len(claimCounts))
	}
	for id := range runIDs {
		switch claimCounts[id] {
		case 1:
			// good
		case 0:
			t.Errorf("run %s was never claimed", id)
		default:
			t.Errorf("run %s was claimed %d times (double-run!)", id, claimCounts[id])
		}
	}

	// All claimed runs should now be 'running' (claim flips status atomically).
	var stillQueued int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ont_test_run WHERE suite_id = $1 AND status = 'queued'`,
		suiteID).Scan(&stillQueued); err != nil {
		t.Fatalf("count queued: %v", err)
	}
	if stillQueued != 0 {
		t.Errorf("expected 0 runs still 'queued' after draining, got %d", stillQueued)
	}
}
