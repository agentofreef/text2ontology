// Package observability wires the monolith's OpenTelemetry tracing and
// Prometheus metrics endpoints. Chunk E of the lakehouse2ontology arch-split
// Phase 0 lands this on the current monolith so we can collect a 7-14 day
// baseline (p99 latency, conflict rates, error rates) before any service
// extraction begins. See .omc/plans/arch-split-plan-final.md §OQ-8 and §4.2.
//
// Additive-only: this package is wired in by main.go after flag parsing but
// never mutates application state. Spans are opened around the 7 critical
// paths (agent.turn, recall.build_context, recall.vector_search, ledger.load,
// ledger.save_with_retry, smartquery.generate_sql, smartquery.execute_sql).
// Metrics match the list in plan §4.2.
//
// Environment variables (read at Init):
//
//	OTEL_EXPORTER_OTLP_ENDPOINT  default "otel-collector:4317"
//	OTEL_SERVICE_NAME            default "lakehouse2ontology-monolith"
//	OTEL_SDK_DISABLED            default "false"; "true" returns a no-op
package observability

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// tracerName is the named tracer the monolith uses for every critical-path
// span. Keeping it stable across restarts keeps Jaeger queries simple.
const tracerName = "lakehouse2ontology-monolith"

var (
	// globalTracer holds the active tracer. Wrapped in atomic.Pointer for
	// safe concurrent reads during Init and after fallback assignment.
	globalTracer atomic.Pointer[trace.Tracer]

	dbRef   *sql.DB
	dbRefMu sync.RWMutex

	// Package-level Prometheus metrics (plan §4.2). Declared at package
	// init via promauto so they're always registered even if OTel is
	// disabled — Prometheus scraping works independent of tracing.

	// LedgerSaveDuration — histogram of ledger.SaveWithRetry latency (ms).
	LedgerSaveDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ledger_save_duration_ms",
		Help:    "Ledger SaveWithRetry wall-clock duration in milliseconds.",
		Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	})

	// LedgerSaveConflictRate — counter of optimistic-concurrency retries.
	// Alerting uses PromQL rate() over this counter divided by save attempts.
	LedgerSaveConflictRate = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ledger_save_conflict_total",
		Help: "Total number of ledger SaveWithRetry conflict retries (per attempt, not per call).",
	})

	// RecallBuildDuration — histogram of recall.BuildLakehouseContext(Cached).
	RecallBuildDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "recall_build_context_duration_ms",
		Help:    "recall.BuildLakehouseContext(Cached) wall-clock duration in milliseconds.",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000},
	})

	// RecallEmbedDuration — histogram of recall.vector_search wall clock,
	// labelled by coarse batch-size bucket.
	RecallEmbedDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "recall_embed_duration_ms",
		Help:    "recall.LakehouseVectorTopN embed+search wall-clock duration in milliseconds.",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
	}, []string{"batch_size_bucket"})

	// SmartqueryExecDuration — histogram of smartquery SQL execution, labelled
	// by coarse complexity bucket (simple=no joins, complex=multi-Od).
	SmartqueryExecDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "smartquery_execute_duration_ms",
		Help:    "smartquery SQL execute wall-clock duration in milliseconds.",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000},
	}, []string{"complexity_bucket"})

	// SSEStreamDuration — histogram of full SSE stream wall-clock (agent turn).
	SSEStreamDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sse_stream_duration_ms",
		Help:    "Lakehouse-agent SSE stream wall-clock duration in milliseconds (per turn).",
		Buckets: []float64{100, 500, 1000, 2500, 5000, 10000, 25000, 60000, 120000, 300000},
	})

	// SSEStreamErrors — counter of SSE write / stream errors, labelled by type.
	SSEStreamErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sse_stream_errors_total",
		Help: "SSE stream errors encountered during lakehouse-agent turns.",
	}, []string{"error_type"})

	// CrossSvcHTTPDuration — reserved for Phase 1+ when internal HTTP hops
	// appear. Monolith-local spans do not emit observations; the metric
	// exists pre-split so dashboards / alerts wire up cleanly at cutover.
	CrossSvcHTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "cross_service_http_duration_ms",
		Help:    "Internal HTTP hop wall-clock in milliseconds (zero observations on monolith; reserved for Phase 1+).",
		Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
	}, []string{"from_service", "to_service", "endpoint"})

	// postgresPoolGauge collects sql.DB.Stats() on each Prometheus scrape and
	// emits the four pool gauges (postgres_pool_{in_use,idle,open,max_open}).
	// It is a custom prometheus.Collector rather than a GaugeFunc so all four
	// stats are read in a single Stats() call (consistent snapshot) and the
	// 120-connection ceiling alert in ops/sli-slo.md has a real series to fire
	// on. Registered into the default registry below and referenced by
	// MetricsHandler so the gauge is emitted on /metrics.
	postgresPoolGauge = newPoolStatsCollector()

	// MCPToolCallDuration — reserved for Phase 1+ (MCP tools server). Zero
	// observations pre-split; declared so the metric name is stable across
	// the cutover.
	MCPToolCallDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mcp_tool_call_duration_ms",
		Help:    "MCP tool call wall-clock in milliseconds (zero observations on monolith; reserved for Phase 1+).",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	}, []string{"tool_name"})

	// MCPToolCallErrorRate — reserved for Phase 1+.
	MCPToolCallErrorRate = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_tool_call_errors_total",
		Help: "MCP tool call errors (zero observations on monolith; reserved for Phase 1+).",
	}, []string{"tool_name", "error_type"})
)

// setTracer atomically stores a new tracer value.
func setTracer(t trace.Tracer) {
	globalTracer.Store(&t)
}

// Tracer returns the monolith's global named tracer. Always non-nil: before
// Init() is called, or when OTEL_SDK_DISABLED=true, this returns a no-op
// tracer so callers can open spans unconditionally without nil-check logic.
func Tracer() trace.Tracer {
	if t := globalTracer.Load(); t != nil {
		return *t
	}
	return noop.NewTracerProvider().Tracer(tracerName)
}

// MetricsHandler exposes the Prometheus scrape endpoint. Register at
// GET /metrics in the monolith mux.
//
// It registers the DB connection-pool collector (postgresPoolGauge) into the
// default registry on first call so the postgres_pool_* gauges appear on
// /metrics. Registration is idempotent: a duplicate (already-registered)
// error from a second call is ignored so multiple mounts of /metrics are safe.
func MetricsHandler() http.Handler {
	if err := prometheus.Register(postgresPoolGauge); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			log.Printf("   Observability: postgres pool collector registration failed: %v", err)
		}
	}
	return promhttp.Handler()
}

// SetDB stores the *sql.DB pointer read by the postgres pool collector.
// Main.go calls this once after opening the pool. Safe to call before or
// after Init.
func SetDB(db *sql.DB) {
	dbRefMu.Lock()
	defer dbRefMu.Unlock()
	dbRef = db
}

// poolStatsCollector is a prometheus.Collector that reports sql.DB.Stats()
// for the registered DB on every scrape. It snapshots all four pool gauges
// from a single Stats() call so the values are mutually consistent. When no
// DB has been registered yet (SetDB not called) it emits no series rather
// than misleading zeros.
type poolStatsCollector struct {
	inUse   *prometheus.Desc
	idle    *prometheus.Desc
	open    *prometheus.Desc
	maxOpen *prometheus.Desc
}

func newPoolStatsCollector() *poolStatsCollector {
	return &poolStatsCollector{
		inUse: prometheus.NewDesc(
			"postgres_pool_in_use",
			"sql.DB.Stats().InUse — connections currently checked out of the pool.",
			nil, nil),
		idle: prometheus.NewDesc(
			"postgres_pool_idle",
			"sql.DB.Stats().Idle — idle connections in the pool.",
			nil, nil),
		open: prometheus.NewDesc(
			"postgres_pool_open_connections",
			"sql.DB.Stats().OpenConnections — total established connections (in use + idle).",
			nil, nil),
		maxOpen: prometheus.NewDesc(
			"postgres_pool_max_open_connections",
			"sql.DB.Stats().MaxOpenConnections — configured upper bound on open connections (0 = unlimited).",
			nil, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *poolStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.inUse
	ch <- c.idle
	ch <- c.open
	ch <- c.maxOpen
}

// Collect implements prometheus.Collector. Reads dbRef under the RWMutex and
// emits one gauge sample per pool stat. No-ops (emits nothing) if no DB is set.
func (c *poolStatsCollector) Collect(ch chan<- prometheus.Metric) {
	dbRefMu.RLock()
	db := dbRef
	dbRefMu.RUnlock()
	if db == nil {
		return
	}
	s := db.Stats()
	ch <- prometheus.MustNewConstMetric(c.inUse, prometheus.GaugeValue, float64(s.InUse))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(s.Idle))
	ch <- prometheus.MustNewConstMetric(c.open, prometheus.GaugeValue, float64(s.OpenConnections))
	ch <- prometheus.MustNewConstMetric(c.maxOpen, prometheus.GaugeValue, float64(s.MaxOpenConnections))
}

// Init constructs an OTel TracerProvider with an OTLP gRPC exporter pointing
// at OTEL_EXPORTER_OTLP_ENDPOINT (default "otel-collector:4317"). Returns a
// shutdown function that flushes on process exit. If OTEL_SDK_DISABLED=true,
// Init returns a no-op shutdown without opening a connection; the monolith
// still exports /metrics.
//
// The exporter is configured with insecure credentials since monolith-in-
// docker reaches the collector over the internal bridge network. Flip to
// WithTLSCredentials at Phase 1+ if the collector moves off-host.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	if serviceName == "" {
		serviceName = envOr("OTEL_SERVICE_NAME", tracerName)
	}

	disabled := envOr("OTEL_SDK_DISABLED", "false")
	if v, err := strconv.ParseBool(disabled); err == nil && v {
		log.Printf("   Observability: OTEL_SDK_DISABLED=%s — tracer remains no-op, /metrics still exported", disabled)
		setTracer(noop.NewTracerProvider().Tracer(tracerName))
		return func(context.Context) error { return nil }, nil
	}

	endpoint := envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317")

	// grpc.NewClient does not block on initial connection — the OTel batcher
	// handles lazy dial and reconnect. This keeps the monolith bootable when
	// the collector isn't up yet (common during Phase 0).
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Printf("   Observability: OTLP collector %s setup failed (%v) — tracer falls back to no-op, /metrics still exported", endpoint, err)
		setTracer(noop.NewTracerProvider().Tracer(tracerName))
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("observability: otlptracegrpc.New: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion("0.3.0"),
		),
		resource.WithHost(),
		resource.WithProcess(),
	)
	if err != nil {
		_ = exp.Shutdown(ctx)
		return nil, fmt.Errorf("observability: resource.New: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(resolveSampler()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	setTracer(tp.Tracer(tracerName))

	log.Printf("   Observability: OTLP exporter → %s (service=%s)", endpoint, serviceName)

	shutdown := func(sc context.Context) error {
		// Shutdown flushes pending spans then closes the exporter.
		// Don't propagate errors during process shutdown — just log.
		if err := tp.Shutdown(sc); err != nil {
			log.Printf("   Observability: tracer shutdown error: %v", err)
		}
		return nil
	}
	return shutdown, nil
}

// BatchSizeBucket maps a numeric batch size to the coarse string label used
// for RecallEmbedDuration. Keeps label cardinality low.
// resolveSampler returns the trace sampler, driven by OTEL_TRACES_SAMPLER_ARG
// (a 0.0–1.0 ratio). Default 1.0 preserves the previous always-sample behavior;
// set e.g. 0.2 in production to bound otel-collector memory and trace storage
// under load. ParentBased ensures a child of a sampled parent is always
// sampled, so one agent turn + its downstream hops remain a single trace.
func resolveSampler() sdktrace.Sampler {
	ratio := 1.0
	if v := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			ratio = f
		}
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

func BatchSizeBucket(n int) string {
	switch {
	case n <= 1:
		return "1"
	case n <= 4:
		return "2-4"
	case n <= 16:
		return "5-16"
	case n <= 64:
		return "17-64"
	case n <= 256:
		return "65-256"
	default:
		return "257+"
	}
}

// ComplexityBucket maps a smartquery spec's shape to a coarse bucket. Pass
// the number of objects and whether the spec has filters. "simple" means
// single-Od no-join; "complex" means multi-Od (join path required).
func ComplexityBucket(objectCount int, hasFilters bool) string {
	if objectCount <= 1 {
		if hasFilters {
			return "simple_filtered"
		}
		return "simple"
	}
	return "complex"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TraceContextMiddleware extracts the W3C traceparent + baggage headers
// from incoming requests and attaches the resulting span context to the
// request's context so downstream Tracer().Start() calls nest as children
// of the caller's span. Without this, cross-service spans start in an
// orphan root and Jaeger shows each service as a disconnected trace.
//
// Failure-safe by design: if the traceparent header is missing or
// malformed, OTel's Extract returns the unmodified ctx and we proceed —
// spans degrade to orphan-root rather than failing the request.
//
// Intended to be the OUTERMOST middleware in a service so that every
// downstream handler (auth, business logic) sees the propagated ctx.
// Typical wiring in a service main.go:
//
//	handler := observability.TraceContextMiddleware(auth.Wrap(mux))
func TraceContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(
			r.Context(),
			propagation.HeaderCarrier(r.Header),
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// InjectTraceContext writes the current span context from ctx into the
// request headers using the W3C traceparent format. Call before
// http.Client.Do on outbound internal requests to stitch the remote
// server's spans into the local trace.
//
// No-op if ctx has no active span — header is simply not set.
func InjectTraceContext(ctx context.Context, req *http.Request) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}

// ServerSpanMiddleware starts a server-side span around every incoming
// HTTP request. Span name is "<METHOD> <path>"; attributes capture
// http.method, http.route (the unparameterized path — servlet-style
// templates would be richer but Go's default mux doesn't expose them),
// and http.status_code (observed via a wrapping ResponseWriter).
//
// Paths whose prefix appears in skipPrefixes are passed through
// untraced. Use this to exclude liveness/metrics probes that would
// otherwise flood Jaeger with noise (/healthz, /metrics).
//
// Intended wiring (INSIDE TraceContextMiddleware so the parent context
// is already extracted):
//
//	handler := observability.TraceContextMiddleware(
//	    observability.ServerSpanMiddleware([]string{"/healthz","/metrics"})(
//	        authmw.Wrap(mux)))
func ServerSpanMiddleware(skipPrefixes []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, pfx := range skipPrefixes {
				if strings.HasPrefix(r.URL.Path, pfx) {
					next.ServeHTTP(w, r)
					return
				}
			}
			serveWithSpan(w, r, next)
		})
	}
}

// ServerSpanMiddlewareForPrefixes is the include-list variant of
// ServerSpanMiddleware. Only requests whose path begins with one of
// the given prefixes get a span; everything else passes through
// untraced. Intended for mixed-surface services (like the monolith
// which serves both /api/* and the SPA) where the trace-worthy
// subset is small relative to total traffic.
func ServerSpanMiddlewareForPrefixes(prefixes []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			matched := false
			for _, pfx := range prefixes {
				if strings.HasPrefix(r.URL.Path, pfx) {
					matched = true
					break
				}
			}
			if !matched {
				next.ServeHTTP(w, r)
				return
			}
			serveWithSpan(w, r, next)
		})
	}
}

// serveWithSpan is the common span-wrapping body shared by both
// ServerSpanMiddleware variants.
func serveWithSpan(w http.ResponseWriter, r *http.Request, next http.Handler) {
	ctx, span := Tracer().Start(r.Context(), r.Method+" "+r.URL.Path)
	sr := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(sr, r.WithContext(ctx))
	span.SetAttributes(
		attribute.String("http.method", r.Method),
		attribute.String("http.route", r.URL.Path),
		attribute.Int("http.status_code", sr.status),
	)
	span.End()
}

// statusResponseWriter captures the HTTP status code written by the
// handler so the surrounding server span can record it. Forwards
// Flush so SSE handlers (flushed per-chunk) keep working when wrapped.
type statusResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
