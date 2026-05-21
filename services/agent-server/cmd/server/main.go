// services/agent-server/cmd/server/main.go
//
// Phase 2 A2: real agent-server. Hosts the SSE-streaming agent turn
// endpoint plus thread CRUD. Monolith backend-api will reverse-proxy
// /api/ontology/lakehouse-agent-stream → here in A3; direct frontend
// redirection is deferred (frontend URL stability preserved).
//
// Endpoints:
//   GET  /healthz                           liveness (no auth)
//   GET  /metrics                           Prometheus scrape
//   POST /internal/agent/stream             SSE agent turn
//   GET  /internal/agent/threads            list threads
//   *    /internal/agent/threads/{id...}    single-thread + sub-routes
//
// Auth: /internal/* requires X-Internal-Token + X-On-Behalf-Of (pkg/authmw).
// Tracing: W3C traceparent extraction via observability.TraceContextMiddleware;
// outgoing calls to recall-server + lakehouse-sql-server use the clients
// under services/agent-server/{recall,lakehouse}, which inject the same
// header via observability.InjectTraceContext — so agent.turn + its
// descendants collapse into a single Jaeger trace.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/agent-server/handler"
	"github.com/lakehouse2ontology/srvkit"
)

func main() {
	port := flag.String("port", "8092", "listen port")
	flag.Parse()

	obsCtx, obsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	obsShutdown, err := observability.Init(obsCtx, "agent-server")
	obsCancel()
	if err != nil {
		log.Fatalf("observability.Init: %v", err)
	}
	defer func() {
		sdCtx, sdCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sdCancel()
		_ = obsShutdown(sdCtx)
	}()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := db.PingContext(pingCtx); err != nil {
		cancel()
		log.Fatalf("db.Ping: %v", err)
	}
	cancel()
	srvkit.TunePool(db)
	observability.SetDB(db)

	// The agent turn path calls two downstream services — surface missing
	// env fast rather than at first turn. Mirrors Phase-1-D4b / R4c's
	// hard-fail pattern so misconfig is impossible to ignore.
	if os.Getenv("LAKEHOUSE_SQL_URL") == "" {
		log.Fatal("LAKEHOUSE_SQL_URL is required (e.g. http://lakehouse-sql-server:8094)")
	}
	if os.Getenv("RECALL_SERVER_URL") == "" {
		log.Fatal("RECALL_SERVER_URL is required (e.g. http://recall-server:8093)")
	}
	if os.Getenv("INTERNAL_TOKEN") == "" {
		log.Fatal("INTERNAL_TOKEN is required for service-to-service auth")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "agent-server",
		})
	})
	mux.Handle("/metrics", observability.MetricsHandler())
	mux.HandleFunc("/internal/agent/stream", handler.HandleAgentStream(db))
	mux.HandleFunc("/internal/agent/threads", handler.HandleAgentThreads(db))
	mux.HandleFunc("/internal/agent/threads/", handler.HandleAgentThreadByID(db))

	// Phase 3 X5: lh-testing + ledger debug/view + agent-annotations +
	// lakehouse-token-recall-tokenize all migrated from monolith.
	mux.HandleFunc("/internal/agent/lh-test-suites", handler.HandleLHTestSuites(db))
	mux.HandleFunc("/internal/agent/lh-test-suites/", handler.HandleLHTestSuiteByID(db))
	mux.HandleFunc("/internal/agent/lh-test-runs/", handler.HandleLHTestRunCancelExported(db))
	mux.HandleFunc("/internal/agent/_debug/ledger-rebuild", handler.HandleLedgerDebug(db))
	mux.HandleFunc("/internal/agent/lakehouse-ledger", handler.HandleLakehouseLedgerGet(db))
	mux.HandleFunc("/internal/agent/agent-annotations", handler.HandleAgentAnnotations(db))
	mux.HandleFunc("/internal/agent/agent-annotations/", handler.HandleAgentAnnotationByID(db))
	mux.HandleFunc("/internal/agent/agent-annotations-recompute", handler.HandleAnnotationsRecompute(db))
	mux.HandleFunc("/internal/agent/lakehouse-token-recall-tokenize", handler.HandleLakehouseTokenRecallWithTokenize(db))

	// Phase 4C.2: public /api/* mirrors for browser-direct access.
	// Same handlers, different auth gate (user-bearer via authmw.Wrap
	// vs INTERNAL_TOKEN on /internal/*). Path mapping aligns with the
	// monolith's legacy URLs so the frontend swap is base-URL only.
	mux.HandleFunc("/api/ontology/lakehouse-agent-stream", handler.HandleAgentStream(db))
	mux.HandleFunc("/api/ontology/lakehouse-agent-threads", handler.HandleAgentThreads(db))
	mux.HandleFunc("/api/ontology/lakehouse-agent-threads/", handler.HandleAgentThreadByID(db))
	// MissionAct (.omc/specs/mission-act.md) — read the mission ledger
	// for a thread. Empty list is legitimate (USE_MISSION_ACT off, or
	// pre-MissionAct thread).
	mux.HandleFunc("/api/ontology/lakehouse-missions", handler.HandleMissionsByThread(db))
	mux.HandleFunc("/internal/agent/missions", handler.HandleMissionsByThread(db))
	mux.HandleFunc("/api/ontology/lh-test-suites", handler.HandleLHTestSuites(db))
	mux.HandleFunc("/api/ontology/lh-test-suites/", handler.HandleLHTestSuiteByID(db))
	mux.HandleFunc("/api/ontology/lh-test-runs/", handler.HandleLHTestRunCancelExported(db))
	mux.HandleFunc("/api/ontology/_debug/ledger-rebuild", adminOnly(db, handler.HandleLedgerDebug(db)))
	mux.HandleFunc("/api/ontology/lakehouse-ledger", handler.HandleLakehouseLedgerGet(db))
	mux.HandleFunc("/api/ontology/agent-annotations", handler.HandleAgentAnnotations(db))
	mux.HandleFunc("/api/ontology/agent-annotations/", handler.HandleAgentAnnotationByID(db))
	mux.HandleFunc("/api/ontology/agent-annotations-recompute", handler.HandleAnnotationsRecompute(db))
	mux.HandleFunc("/api/ontology/lakehouse-token-recall-tokenize", handler.HandleLakehouseTokenRecallWithTokenize(db))

	// Signal-driven shutdown context. The LH-test background worker and
	// srvkit.Run both observe this ctx so a SIGINT/SIGTERM stops the dequeue
	// loop and drains HTTP before the deferred db.Close()/obsShutdown() run.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background worker: dequeues ont_test_run rows with status='queued'
	// and executes each suite's cases with lakehouseToolLookup/SmartQuery
	// in-process. Moved from monolith main.go's ontology.StartLHTestWorker.
	// Tied to ctx so it stops claiming new runs on shutdown; lhWorkerDone
	// closes once its poll loop has fully exited.
	lhWorkerDone := handler.StartLHTestWorkerCtx(ctx, db)

	auth := authmw.New(db, authmw.NewStdoutAuditWriter())
	// Middleware order (outermost first): CORS → traceparent → server-span
	// → auth → handler. Same pattern lakehouse-sql-server + recall-server
	// follow. Phase 4C.2 added CORSMiddleware so the browser can fetch
	// /api/ontology/lakehouse-agent-stream (SSE) directly, bypassing the
	// monolith gateway.
	// srvkit.RecoverMiddleware sits just inside CORS so a handler panic is
	// caught (→ 500) while CORS response headers are still applied. Otherwise
	// the existing order is preserved: CORS → recover → traceparent →
	// server-span → auth → handler.
	httpHandler := httputil.CORSMiddleware(os.Getenv("CORS_ALLOW_ORIGINS"))(
		srvkit.RecoverMiddleware(
			observability.TraceContextMiddleware(
				observability.ServerSpanMiddleware([]string{"/healthz", "/metrics"})(
					auth.Wrap(mux)))))

	addr := ":" + *port
	log.Printf("▼// agent-server listening on %s (DB OK, tracer=agent-server)", addr)
	log.Printf("   → lakehouse-sql: %s", os.Getenv("LAKEHOUSE_SQL_URL"))
	log.Printf("   → recall:        %s", os.Getenv("RECALL_SERVER_URL"))

	// WriteTimeout=0 because /internal/agent/stream + /api/ontology/
	// lakehouse-agent-stream are text/event-stream (SSE); a fixed write
	// window would sever long-lived streams. Shutdown order: srvkit.Run
	// drains HTTP → OnShutdown waits for the LH worker poll loop to exit →
	// deferred db.Close() → deferred obsShutdown() (traces flush last).
	err = srvkit.Run(ctx, addr, httpHandler,
		srvkit.WithWriteTimeout(0),
		srvkit.WithOnShutdown(func(context.Context) {
			select {
			case <-lhWorkerDone:
			case <-time.After(5 * time.Second):
				log.Println("agent-server: LH-test worker drain timed out")
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
}
