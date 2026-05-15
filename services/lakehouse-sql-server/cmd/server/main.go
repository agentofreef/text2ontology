// services/lakehouse-sql-server/cmd/server/main.go
//
// Phase 1 D2: real implementation of lakehouse-sql-server. Exposes the
// smartquery execute path over HTTP/JSON at /internal/smartquery/execute
// so the monolith (and later agent-server) can call it as a data-plane
// service instead of importing the lakehouse.Engine in-process.
//
// Endpoints:
//
//	GET  /healthz                 liveness
//	GET  /metrics                 Prometheus scrape
//	POST /internal/smartquery/execute   {spec: QuerySpec} -> LakehouseResult
//
// Auth: /internal/* requires X-Internal-Token (= INTERNAL_TOKEN env) +
// X-On-Behalf-Of header. See pkg/authmw.
//
// Observability is deferred to D2.5 (factor backend/observability → pkg/
// first); this binary has only Prometheus counters for now.
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
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/lakehouse-sql-server/lakehouse"
	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// HTTP-level metrics for the /internal/smartquery/execute endpoint. The
// underlying lakehouse.Engine emits its own smartquery_execute_duration_ms
// histogram via the (currently monolith-owned) observability package —
// these wrappers measure the HTTP handler around it (decode + auth +
// response write) to keep HTTP latency separable from SQL latency.
var (
	httpExecuteDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "lakehouse_sql_http_execute_duration_ms",
		Help:    "/internal/smartquery/execute HTTP handler wall-clock duration in milliseconds (covers decode + engine.Execute + encode).",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000},
	}, []string{"outcome"})

	httpExecuteRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "lakehouse_sql_http_execute_requests_total",
		Help: "Total /internal/smartquery/execute HTTP requests by outcome.",
	}, []string{"outcome"})
)

func main() {
	port := flag.String("port", "8094", "listen port")
	flag.Parse()

	// Observability: wire OTLP tracer first so DB/HTTP init is covered. The
	// shared pkg/observability is the same package the monolith uses;
	// service name is what shows up in Jaeger. Shutdown flushes on exit.
	obsCtx, obsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	obsShutdown, err := observability.Init(obsCtx, "lakehouse-sql-server")
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
	// Cheap warm ping — fail fast if the DSN or credentials are bad rather than
	// waiting for the first real request.
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := db.PingContext(pingCtx); err != nil {
		cancel()
		log.Fatalf("db.Ping: %v", err)
	}
	cancel()
	observability.SetDB(db)

	engine := &lakehouse.Engine{DB: db}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "lakehouse-sql-server",
		})
	})
	mux.Handle("/metrics", observability.MetricsHandler())
	mux.HandleFunc("/internal/smartquery/execute", executeHandler(engine))
	mux.HandleFunc("/internal/smartquery/validate-intent", validateIntentHandler())

	auth := authmw.New(db, authmw.NewStdoutAuditWriter())
	// Middleware order matters:
	//   TraceContextMiddleware (OUTERMOST) — extract W3C traceparent so
	//     every downstream span nests under the caller's span.
	//   auth.Wrap                           — token + audit.
	//   mux                                 — business handlers.
	handler := observability.TraceContextMiddleware(
		observability.ServerSpanMiddleware([]string{"/healthz", "/metrics"})(
			auth.Wrap(mux)))

	addr := ":" + *port
	log.Printf("▼// lakehouse-sql-server listening on %s (DB OK, tracer=lakehouse-sql-server)", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}

// executeRequest is the HTTP request body: a full QuerySpec envelope.
// JSON shape matches pkg/contracts/querySpec.go exactly (freeze clause)
// so monolith callers can serialize their in-process smartquery.QuerySpec
// and this handler decodes it byte-identically into the service-local
// smartquery.QuerySpec.
type executeRequest struct {
	Spec smartquery.QuerySpec `json:"spec"`
}

func executeHandler(engine *lakehouse.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		outcome := "ok"
		defer func() {
			ms := float64(time.Since(start).Milliseconds())
			httpExecuteDuration.WithLabelValues(outcome).Observe(ms)
			httpExecuteRequests.WithLabelValues(outcome).Inc()
		}()

		if r.Method != http.MethodPost {
			outcome = "method_not_allowed"
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}

		var req executeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			outcome = "bad_request"
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
		if req.Spec.ProjectID == "" {
			outcome = "bad_request"
			writeErr(w, http.StatusBadRequest, "spec.projectId required")
			return
		}

		result := engine.Execute(r.Context(), req.Spec)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			outcome = "write_error"
			log.Printf("execute: write response failed: %v", err)
		}
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// validateIntentHandler is the dry-run gate backend-api hits on POST/PUT
// /api/ontology/metric-intents. Body is smartquery.IntentValidationInput;
// response is smartquery.IntentValidationResult.
//
// Pure structural validation — no DB access, no project context, no SQL
// execution. Catches: bare canonical_metric+auto_group_by combinations
// that would produce MISSING_METRIC_OR_GROUPBY at runtime, parameter
// schema flaws (property_filter without property, unknown type),
// canonical_filters with bad ops, default_limit producing negative
// limit. Does NOT catch property name typos — those resolve at first
// runtime query.
//
// Defense line 2 of the strict-mode contract (lines 1+3 are LLM tool
// surface gating in agent-server and PassValidateSpec at runtime).
func validateIntentHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var in smartquery.IntentValidationInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
		result := smartquery.ValidateIntentDryRun(in)
		w.Header().Set("Content-Type", "application/json")
		// Always 200 — the validation outcome is in result.Ok / .Errors.
		// Returning non-200 for logical validation failures would conflate
		// transport errors with business validation results.
		if err := json.NewEncoder(w).Encode(result); err != nil {
			log.Printf("validate-intent: write response failed: %v", err)
		}
	}
}
