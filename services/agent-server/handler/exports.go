// Package handler exports the HTTP factory functions wired onto agent-server's
// mux by cmd/server/main.go. Thin aliases around the lowercase in-package
// symbols so the legacy monolith copies keep their names.
package handler

import (
	"database/sql"
	"net/http"
)

// ── Agent turn (Phase 2 A2) ────────────────────────────────────────────────

// HandleAgentStream returns the main SSE endpoint for a single agent turn.
// Wire to POST /internal/agent/stream.
func HandleAgentStream(db *sql.DB) http.HandlerFunc {
	return handleAgentStreamLakehouse(db)
}

// HandleAgentThreads returns the thread-list CRUD endpoint. Wire to
// /internal/agent/threads — GET / POST.
func HandleAgentThreads(db *sql.DB) http.HandlerFunc {
	return handleLakehouseAgentThreads(db)
}

// HandleAgentThreadByID returns the single-thread read / rename / archive
// endpoint. Wire to /internal/agent/threads/ (with trailing slash + id).
func HandleAgentThreadByID(db *sql.DB) http.HandlerFunc {
	return handleLakehouseAgentThreadByID(db)
}

// ── Phase 3 X5 exports (lh-testing + ledger + agent-annotations) ───────────

// HandleLHTestSuites returns the lh-test-suites list + create factory.
func HandleLHTestSuites(db *sql.DB) http.HandlerFunc { return handleLHTestSuites(db) }

// HandleLHTestSuiteByID returns the suite get/update/delete + test-run kick-off factory.
func HandleLHTestSuiteByID(db *sql.DB) http.HandlerFunc { return handleLHTestSuiteByID(db) }

// HandleLHTestRunCancelExported is the cooperative cancel endpoint (POST).
func HandleLHTestRunCancelExported(db *sql.DB) http.HandlerFunc { return handleLHTestRunCancel(db) }

// HandleAgentAnnotations returns the /agent-annotations list + create factory.
func HandleAgentAnnotations(db *sql.DB) http.HandlerFunc { return handleAgentAnnotations(db) }

// HandleAgentAnnotationByID returns the agent-annotation detail factory.
func HandleAgentAnnotationByID(db *sql.DB) http.HandlerFunc { return handleAgentAnnotationByID(db) }

// HandleAnnotationsRecompute returns the vector-recompute batch factory.
func HandleAnnotationsRecompute(db *sql.DB) http.HandlerFunc {
	return handleAnnotationsRecompute(db)
}

// HandleLakehouseTokenRecallWithTokenize runs tokenize + recall for
// operator inspection (the /lakehouse-token-recall-tokenize debug route).
func HandleLakehouseTokenRecallWithTokenize(db *sql.DB) http.HandlerFunc {
	return handleLakehouseTokenRecallWithTokenize(db)
}

// HandleLedgerDebug + HandleLakehouseLedgerGet are already exported
// directly by handler_ledger_debug.go / handler_ledger_view.go, and
// StartLHTestWorker lives in lh_test_worker.go. Those symbols don't
// need wrappers — callers import them directly through this package.
