// services/recall-server/cmd/server/main.go
//
// Phase 1 R2: real recall-server. Exposes the Od recall + vector-fallback
// pipeline over HTTP/JSON so the monolith and agent-server can call it
// instead of importing recall.BuildLakehouseContext[Cached] in-process.
//
// Endpoints:
//
//	GET  /healthz                          liveness
//	GET  /metrics                          Prometheus scrape
//	GET  /internal/recall/debug            wraps recall.HandleLakehouseDebug
//	POST /internal/recall/build-context    cached or uncached recall build
//
// Two-lane /internal/embed priority routing is Phase-1-exit work (R5) and
// not wired here yet. The /internal/recall/tokenize surface is deferred
// until monolith tokenization moves behind the boundary too.
//
// Auth: /internal/* requires X-Internal-Token (= INTERNAL_TOKEN env) +
// X-On-Behalf-Of header (pkg/authmw).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/recall-server/recall"
	"github.com/lakehouse2ontology/srvkit"
)

// HTTP-level metrics. The recall package itself emits the inner
// recall.build_context + recall.vector_search spans/histograms via
// pkg/observability — these wrappers cover decode + auth + encode.
var (
	httpBuildContextDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "recall_http_build_context_duration_ms",
		Help:    "/internal/recall/build-context HTTP handler wall-clock duration in milliseconds.",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000},
	}, []string{"outcome"})

	httpBuildContextRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "recall_http_build_context_requests_total",
		Help: "Total /internal/recall/build-context HTTP requests by outcome.",
	}, []string{"outcome"})
)

func main() {
	port := flag.String("port", "8093", "listen port")
	flag.Parse()

	obsCtx, obsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	obsShutdown, err := observability.Init(obsCtx, "recall-server")
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "recall-server",
		})
	})
	mux.Handle("/metrics", observability.MetricsHandler())
	mux.HandleFunc("/internal/recall/build-context", buildContextHandler(db))
	// Debug surface — wraps the existing handler factory, which itself
	// returns an http.HandlerFunc that reads projectId + question query
	// params. Mounted under /internal so it shares auth middleware.
	mux.Handle("/internal/recall/debug", recall.HandleLakehouseDebug(db))

	// Phase 4C.2: public /api/ontology/lakehouse-token-recall-debug for
	// browser-direct access (was monolith-gatewayed via recall_debug_proxy).
	mux.Handle("/api/ontology/lakehouse-token-recall-debug", recall.HandleLakehouseDebug(db))

	auth := authmw.New(db, authmw.NewStdoutAuditWriter())
	// Middleware order matters:
	//   CORSMiddleware (OUTERMOST)          — OPTIONS preflight + ACAO.
	//   TraceContextMiddleware              — extract W3C traceparent so
	//     every downstream span nests under the caller's span.
	//   ServerSpanMiddleware                — start server span per req.
	//   auth.Wrap                           — token + audit.
	//   mux                                 — business handlers.
	//   srvkit.RecoverMiddleware                — recover handler panics
	//     (→ 500) just inside CORS so panics are caught while CORS headers
	//     are still applied.
	handler := httputil.CORSMiddleware(os.Getenv("CORS_ALLOW_ORIGINS"))(
		srvkit.RecoverMiddleware(
			observability.TraceContextMiddleware(
				observability.ServerSpanMiddleware([]string{"/healthz", "/metrics"})(
					auth.Wrap(mux)))))

	addr := ":" + *port
	log.Printf("▼// recall-server listening on %s (DB OK, tracer=recall-server)", addr)

	// Graceful shutdown: srvkit.Run drains HTTP on SIGINT/SIGTERM, then the
	// deferred db.Close() + obsShutdown() run (traces flush last).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srvkit.Run(ctx, addr, handler); err != nil {
		log.Fatal(err)
	}
}

// buildContextRequest is the wire envelope for both cached + uncached
// recall calls. JSON keys mirror BuildLakehouseContextCached's parameter
// list exactly so the monolith (or agent-server) can serialize with
// zero translation.
type buildContextRequest struct {
	ProjectID string                `json:"projectId"`
	Tokens    []string              `json:"tokens"`
	Question  string                `json:"question"`
	Cached    *recall.CachedContext `json:"cached,omitempty"`
}

func buildContextHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		outcome := "ok"
		defer func() {
			ms := float64(time.Since(start).Milliseconds())
			httpBuildContextDuration.WithLabelValues(outcome).Observe(ms)
			httpBuildContextRequests.WithLabelValues(outcome).Inc()
		}()

		if r.Method != http.MethodPost {
			outcome = "method_not_allowed"
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}

		var req buildContextRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			outcome = "bad_request"
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
		if req.ProjectID == "" {
			outcome = "bad_request"
			writeErr(w, http.StatusBadRequest, "projectId required")
			return
		}

		var result recall.RecallResult
		if req.Cached != nil {
			result = recall.BuildLakehouseContextCached(
				r.Context(), db, req.ProjectID, req.Tokens, req.Question, req.Cached)
		} else {
			result = recall.BuildLakehouseContext(
				r.Context(), db, req.ProjectID, req.Tokens, req.Question)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			outcome = "write_error"
			log.Printf("build-context: write response failed: %v", err)
		}
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
