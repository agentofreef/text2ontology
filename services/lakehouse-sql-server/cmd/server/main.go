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
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/dsnguard"
	"github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/lakehouse-sql-server/lakehouse"
	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
	"github.com/lakehouse2ontology/sqlrewrite"
	"github.com/lakehouse2ontology/srvkit"
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
	// Fail-closed: refuse to start on a malformed or legacy (text2dax) DSN.
	if err := dsnguard.AssertSafeDSN(dsn); err != nil {
		log.Fatalf("%v", err)
	}
	// Fail-closed on weak secrets when REQUIRE_STRONG_SECRETS is set (no-op otherwise).
	if err := dsnguard.AssertStrongSecrets("lakehouse-sql-server"); err != nil {
		log.Fatal(err)
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
	srvkit.TunePool(db)
	observability.SetDB(db)

	engine := &lakehouse.Engine{DB: db}

	mux := http.NewServeMux()
	// Shared healthz: liveness + token-gated ?check=db DB ping (single source
	// of truth across all services; replaces the inline JSON handler).
	httputil.InstallHealthzDB(mux, dsn, db, "lakehouse-sql-server")
	mux.Handle("/metrics", observability.MetricsHandler())
	mux.HandleFunc("/internal/smartquery/execute", executeHandler(engine))
	mux.HandleFunc("/internal/smartquery/execute-plan", executePlanHandler(engine))
	mux.HandleFunc("/internal/smartquery/execute-sql", executeSQLHandler(db))
	mux.HandleFunc("/internal/smartquery/validate-intent", validateIntentHandler())

	auth := authmw.New(db, authmw.NewDBAuditWriter(db))
	// Middleware order matters:
	//   TraceContextMiddleware (OUTERMOST) — extract W3C traceparent so
	//     every downstream span nests under the caller's span.
	//   auth.Wrap                           — token + audit.
	//   mux                                 — business handlers.
	// srvkit.RecoverMiddleware is OUTERMOST here (this service has no CORS
	// layer) so handler panics are recovered (→ 500) before they unwind the
	// goroutine. Otherwise the existing order is preserved.
	handler := srvkit.RecoverMiddleware(
		observability.TraceContextMiddleware(
			observability.ServerSpanMiddleware([]string{"/healthz", "/metrics"})(
				auth.Wrap(mux))))

	addr := ":" + *port
	log.Printf("▼// lakehouse-sql-server listening on %s (DB OK, tracer=lakehouse-sql-server)", addr)

	// Graceful shutdown: srvkit.Run drains HTTP on SIGINT/SIGTERM, then the
	// deferred db.Close() + obsShutdown() run (traces flush last).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srvkit.Run(ctx, addr, handler); err != nil {
		log.Fatal(err)
	}
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

// executePlanRequest is the HTTP body for composite-Intent execution.
// Plan is the lakehouse_metric_intent.plan JSONB verbatim — the handler
// re-validates it with ParsePlan rather than trusting the caller's pre-parse,
// so a malformed plan surfaces here instead of silently mis-executing.
type executePlanRequest struct {
	Plan      json.RawMessage   `json:"plan"`
	Params    map[string]string `json:"params"`
	ProjectID string            `json:"projectId"`
}

func executePlanHandler(engine *lakehouse.Engine) http.HandlerFunc {
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

		var req executePlanRequest
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
		if len(req.Plan) == 0 {
			outcome = "bad_request"
			writeErr(w, http.StatusBadRequest, "plan required")
			return
		}
		plan, err := lakehouse.ParsePlan(req.Plan)
		if err != nil {
			outcome = "bad_plan"
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}

		exec := &lakehouse.PlanExecutor{
			Runner: func(ctx context.Context, _ string, spec smartquery.QuerySpec) lakehouse.LakehouseResult {
				return engine.Execute(ctx, spec)
			},
		}
		result, err := exec.Execute(r.Context(), plan, req.Params, req.ProjectID)
		if err != nil {
			outcome = "exec_error"
			if result.ErrorMessage == "" {
				result.ErrorMessage = err.Error()
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			outcome = "write_error"
			log.Printf("execute-plan: write response failed: %v", err)
		}
	}
}

// executeSQLRequest is the HTTP body for SQL-mode metric execution.
//
//	sql      human-authored, OD-name SQL declaring parameters INLINE via
//	         {sys.req.NAME} (required) / {sys.opt.NAME} (optional). There is no
//	         separate param table — params are derived by parsing the SQL.
//	params   the LLM/editor-supplied parameter values, keyed by NAME. Values are
//	         strings; the driver infers the column type. A NAME present with a
//	         non-empty value is "provided"; absent/empty optionals are dropped and
//	         absent required ones surface as MISSING_REQUIRED.
//
// paramTypes is retained for body-compat but no longer used: every value binds
// via $N driver args regardless of type (pq infers), so int-coercion is moot.
type executeSQLRequest struct {
	ProjectID string `json:"projectId"`
	SQL       string `json:"sql"`
	// Params values are scalar (JSON string/number) OR list (JSON array). A list
	// binds to a single $N as `= ANY($N)` (the executor wraps it in pq.Array).
	Params     map[string]interface{} `json:"params"`
	ParamTypes map[string]string      `json:"paramTypes"` // deprecated, ignored
	RowLimit   int                    `json:"rowLimit"`
}

// executeSQLHandler implements POST /internal/smartquery/execute-sql — the
// SQL-mode metric runtime. Pipeline (security-critical ordering):
//
//	1. RenderSysParams(sql, params) — position-awarely substitute every inline
//	   {sys.req/opt.NAME}: provided VALUES → positional $N + driver args; provided
//	   dimensions → quoted "NAME"; absent optionals → dropped (predicate/comma
//	   cleaned); absent requireds → recorded in missingRequired. Runs FIRST so all
//	   brace payloads are gone before any keyword/identifier scan.
//	2. missingRequired non-empty → return {ok:false,error:"MISSING_REQUIRED:..."}
//	   so callers (agent/editor) can surface the missing param to the user.
//	3. RejectDDL(rendered)         — block DDL/DML/dollar-quote/cross-schema/
//	   multi-statement on the post-render text.
//	4. load OD→canonical_query map  — ont_object_type WHERE project_id +
//	   canonical_query<>'' + mark.
//	5. ExtractReferencedNames       — every FROM/JOIN ref must be a known OD.
//	6. BuildCTEPrefix + prepend      — wrap canonical_query CTEs.
//	7. MaybeInjectLimit              — cap rows.
//	8. ExecuteSQLParams(db, projectId, finalSQL, args...) — bind args from step 1.
//
// Values ALWAYS bind through the $N driver args produced in step 1 — never
// concatenated. Identifiers (dimensions) are quoted-ident escaped in step 1.
func executeSQLHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req executeSQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
		req.SQL = strings.TrimSpace(req.SQL)
		if req.ProjectID == "" || req.SQL == "" {
			writeErr(w, http.StatusBadRequest, "projectId and sql required")
			return
		}
		rowLimit := req.RowLimit
		if rowLimit <= 0 || rowLimit >= 50000 {
			rowLimit = 10000
		}

		// 1. Inline {sys.req/opt.NAME} render FIRST (before any keyword/identifier
		// scan): values → $N args, dimensions → quoted idents, absent optionals
		// dropped, absent requireds collected.
		rewritten, args, missingRequired := sqlrewrite.RenderSysParams(req.SQL, req.Params)

		// 2. Missing required params → clear error so callers can surface it.
		if len(missingRequired) > 0 {
			resp := map[string]interface{}{
				"ok":    false,
				"error": "MISSING_REQUIRED:" + strings.Join(missingRequired, ","),
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// 3. Reject DDL on the post-render text.
		if err := sqlrewrite.RejectDDL(rewritten); err != nil {
			writeSQLErr(w, err.Error())
			return
		}

		// 4. Load OD → canonical_query for the project (mirrors backend-api's
		// loadProjectOdCanonical). lakehouse-sql-server has DB access here.
		odCanonical, odNames, err := loadProjectOdCanonical(db, req.ProjectID)
		if err != nil {
			writeSQLErr(w, fmt.Sprintf("加载 Ontology 对象失败: %v", err))
			return
		}
		if len(odCanonical) == 0 {
			writeSQLErr(w, "当前项目尚未配置任何 Ontology 对象（或对象未完成 canonical_query 固化）")
			return
		}

		// 5. Validate every FROM/JOIN reference is a known OD (case-insensitive).
		refs := sqlrewrite.ExtractReferencedNames(rewritten)
		lowerToName := make(map[string]string, len(odCanonical))
		for name := range odCanonical {
			lowerToName[strings.ToLower(name)] = name
		}
		var unknown, used []string
		seen := map[string]bool{}
		for _, ref := range refs {
			lr := strings.ToLower(ref)
			name, ok := lowerToName[lr]
			if !ok {
				unknown = append(unknown, ref)
				continue
			}
			if !seen[lr] {
				seen[lr] = true
				used = append(used, name)
			}
		}
		if len(unknown) > 0 {
			writeSQLErr(w, fmt.Sprintf("未知的 Ontology 对象: %s。可用对象: %s",
				strings.Join(unknown, ", "), strings.Join(odNames, ", ")))
			return
		}
		if len(used) == 0 {
			writeSQLErr(w, "查询中未引用任何 Ontology 对象（FROM/JOIN 至少需要一个 Od 名）")
			return
		}

		// 6+7. CTE prefix + LIMIT injection.
		finalSQL := sqlrewrite.BuildCTEPrefix(odCanonical, used) + "\n" + rewritten
		finalSQL = sqlrewrite.MaybeInjectLimit(finalSQL, rowLimit)

		// 8. Execute with the $N args produced by RenderSysParams (step 1). Values
		// bind via driver args; identifiers were already quoted-ident escaped.
		ok, resultJSON, errMsg, _ := lakehouse.ExecuteSQLParams(db, req.ProjectID, finalSQL, args...)

		// Parse resultJSON ([]map) into rows + ordered-ish columns. Column order
		// is derived from the first row's keys (Go map iteration is unordered, so
		// callers needing strict order should rely on the SQL SELECT list); the
		// agent path consumes rows directly and does not depend on columns order.
		var rows []map[string]interface{}
		if resultJSON != "" {
			_ = json.Unmarshal([]byte(resultJSON), &rows)
		}
		columns := []string{}
		if len(rows) > 0 {
			for k := range rows[0] {
				columns = append(columns, k)
			}
			sort.Strings(columns)
		}
		resp := map[string]interface{}{
			"ok":       ok,
			"sql":      finalSQL,
			"columns":  columns,
			"rows":     rows,
			"rowCount": len(rows),
		}
		if !ok {
			resp["error"] = errMsg
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("execute-sql: write response failed: %v", err)
		}
	}
}

// writeSQLErr writes a 200 with {ok:false,error:...} for SQL-mode validation /
// execution failures — the outcome is in the body, not the HTTP status, so the
// agent / editor can distinguish "query rejected" from "transport broke".
func writeSQLErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       false,
		"error":    msg,
		"columns":  []string{},
		"rows":     []map[string]interface{}{},
		"rowCount": 0,
	})
}

// loadProjectOdCanonical returns originalName → canonical_query for every
// marked OD in the project that has a non-empty canonical_query, plus a sorted
// display list of those names. Mirrors backend-api's loadProjectOdCanonical
// (the passthrough loader) so SQL-mode metrics resolve OD names identically.
func loadProjectOdCanonical(db *sql.DB, projectID string) (map[string]string, []string, error) {
	rows, err := db.Query(`SELECT o.name, COALESCE(o.canonical_query,'')
		FROM ont_object_type o
		WHERE o.project_id = $1 AND COALESCE(o.mark,true) = true
		ORDER BY o.name`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	var names []string
	for rows.Next() {
		var n, cq string
		if err := rows.Scan(&n, &cq); err != nil {
			continue
		}
		if cq == "" {
			continue // exclude ODs without a fixed canonical_query
		}
		m[n] = cq
		names = append(names, n)
	}
	sort.Strings(names)
	return m, names, nil
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
