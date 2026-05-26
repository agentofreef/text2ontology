// services/backend-api/cmd/server/main.go
//
// Phase 3 B1: backend-api moves from Phase-0 stub to a real service shell.
// The shape matches the other Phase-1/2 services (recall-server,
// lakehouse-sql-server, agent-server): observability first, DB+ping next,
// then /healthz + /metrics + an empty /internal/backend-api/* namespace
// wrapped in TraceContextMiddleware → authmw.Wrap → mux.
//
// What this commit does NOT do:
//   - Migrate any /api/ontology/* CRUD routes off the monolith. Those
//     routes continue to be served by backend/ at :18091. This shell is
//     just the landing pad for B2 (shadow-copy handlers) and B3 (wire
//     HTTP routes behind feature flags).
//   - Swap the docker-compose container image. The compose stanza still
//     builds from Dockerfile.service-stub with SERVICE=backend-api; the
//     real Dockerfile lives in this directory for when B4 flips it.
//
// A /internal/backend-api/ping endpoint is included so that smoke tests
// can verify authmw + observability wiring without waiting for B2.
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
	"github.com/lakehouse2ontology/dsnguard"
	"github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/backend-api/core"
	"github.com/lakehouse2ontology/services/backend-api/handler"
	"github.com/lakehouse2ontology/srvkit"
)

func main() {
	port := flag.String("port", "8090", "listen port")
	flag.Parse()

	obsCtx, obsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	obsShutdown, err := observability.Init(obsCtx, "backend-api")
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
	if err := dsnguard.AssertStrongSecrets("backend-api"); err != nil {
		log.Fatal(err)
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

	// Fail-closed startup checks for auth. AUTH_TOKEN_SECRET signs every
	// session token; if it rotates, all existing tokens silently invalidate.
	// Admin bootstrap runs once: when the seed admin still carries the
	// BOOTSTRAP_REQUIRED sentinel, ADMIN_PASSWORD must be set so we can
	// install a real password hash before serving any request.
	core.AssertAuthEnv()
	// Fail-closed on a weak admin password when REQUIRE_STRONG_SECRETS is set.
	if err := dsnguard.AssertStrongAdminPassword(); err != nil {
		log.Fatal(err)
	}
	if err := core.BootstrapAdminIfNeeded(db); err != nil {
		log.Fatalf("[auth] admin bootstrap failed: %v", err)
	}

	mux := http.NewServeMux()
	// Shared healthz: liveness + token-gated ?check=db DB ping (single source
	// of truth across all services; replaces the inline JSON handler).
	httputil.InstallHealthzDB(mux, dsn, db, "backend-api")
	mux.Handle("/metrics", observability.MetricsHandler())

	// /internal/backend-api/ping is an authenticated placeholder endpoint.
	// Its sole purpose is to let callers (monolith proxy, smoke scripts)
	// confirm authmw + observability are wired correctly before B2 lands
	// the real handlers. Response echoes X-On-Behalf-Of so the audit log
	// is observable in responses during debugging.
	mux.HandleFunc("/internal/backend-api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"service":    "backend-api",
			"onBehalfOf": r.Header.Get("X-On-Behalf-Of"),
		})
	})

	// Phase 3 B3: 6 leaf CRUD endpoints from B2's shadow-copied handlers.
	// Path shape: /internal/backend-api/<entity>[/<id>[/<subroute>]]. The
	// monolith's crud_proxy.go rewrites public /api/ontology/<entity>[/...]
	// to exactly these paths. Trailing-slash + non-slash routes both land
	// on the same handler pair (list/create vs detail) so net/http's
	// default ServeMux dispatches correctly. Entities migrated in B2:
	// versions, aliases, rules, methods, skills, links.
	mux.HandleFunc("/internal/backend-api/aliases", handler.HandleAliases(db))
	mux.HandleFunc("/internal/backend-api/aliases/", handler.HandleAliasByID(db))
	mux.HandleFunc("/internal/backend-api/rules", handler.HandleRules(db))
	mux.HandleFunc("/internal/backend-api/rules/", handler.HandleRuleByID(db))
	mux.HandleFunc("/internal/backend-api/methods", handler.HandleMethods(db))
	mux.HandleFunc("/internal/backend-api/methods/", handler.HandleMethodByID(db))
	mux.HandleFunc("/internal/backend-api/skills", handler.HandleSkills(db))
	mux.HandleFunc("/internal/backend-api/skills/", handler.HandleSkillByID(db))
	mux.HandleFunc("/internal/backend-api/links", handler.HandleLinks(db))
	mux.HandleFunc("/internal/backend-api/links/", handler.HandleLinkByID(db))

	// Phase 3 B5: four more entity families migrated from the monolith.
	// handler_metric.go, handler_object.go (+properties + property-nodes),
	// handler_knowledge.go (+ knowledge-generate + knowledge-sync-properties),
	// handler_ol.go (+ learned-facts-recompute + fact-definitions + fact-links).
	// Paths mirror the legacy /api/ontology/* names under /internal/backend-api.
	mux.HandleFunc("/internal/backend-api/objects", handler.HandleObjects(db))
	mux.HandleFunc("/internal/backend-api/objects/", handler.HandleObjectByID(db))
	mux.HandleFunc("/internal/backend-api/properties", handler.HandleProperties(db))
	mux.HandleFunc("/internal/backend-api/properties/", handler.HandlePropertyByID(db))
	mux.HandleFunc("/internal/backend-api/property-nodes", handler.HandlePropertyNodes(db))
	mux.HandleFunc("/internal/backend-api/metrics", handler.HandleMetrics(db))
	mux.HandleFunc("/internal/backend-api/metrics/", handler.HandleMetricByID(db))
	mux.HandleFunc("/internal/backend-api/knowledge", handler.HandleKnowledgeEntries(db))
	mux.HandleFunc("/internal/backend-api/knowledge/", handler.HandleKnowledgeEntryByID(db))
	mux.HandleFunc("/internal/backend-api/knowledge-generate", handler.HandleKnowledgeGenerate(db))
	mux.HandleFunc("/internal/backend-api/knowledge-sync-properties", handler.HandleKnowledgeSyncProperties(db))
	mux.HandleFunc("/internal/backend-api/learned-facts", handler.HandleLearnedFacts(db))
	mux.HandleFunc("/internal/backend-api/learned-facts/", handler.HandleLearnedFactByID(db))
	mux.HandleFunc("/internal/backend-api/learned-facts-recompute", handler.HandleLearnedFactsRecomputeVectors(db))
	mux.HandleFunc("/internal/backend-api/fact-definitions", handler.HandleFactDefinitions(db))
	mux.HandleFunc("/internal/backend-api/fact-definitions/", handler.HandleFactDefinitionByID(db))
	mux.HandleFunc("/internal/backend-api/fact-links", handler.HandleFactLinks(db))
	mux.HandleFunc("/internal/backend-api/fact-links/", handler.HandleFactLinkByID(db))

	// Phase 3 X1: causality + query-logs (+ CSV import/export/template +
	// example-questions which shares the handler_query_log file) +
	// token-annotations.
	mux.HandleFunc("/internal/backend-api/causality", handler.HandleCausalities(db))
	mux.HandleFunc("/internal/backend-api/causality/", handler.HandleCausalityByID(db))
	mux.HandleFunc("/internal/backend-api/query-logs", handler.HandleQueryLogs(db))
	mux.HandleFunc("/internal/backend-api/query-logs/", handler.HandleQueryLogByID(db))
	mux.HandleFunc("/internal/backend-api/query-logs-template", handler.HandleQueryLogTemplate(db))
	mux.HandleFunc("/internal/backend-api/query-logs-upload", handler.HandleQueryLogUpload(db))
	mux.HandleFunc("/internal/backend-api/query-logs-export", handler.HandleQueryLogExport(db))
	mux.HandleFunc("/internal/backend-api/example-questions", handler.HandleExampleQuestions(db))
	mux.HandleFunc("/internal/backend-api/token-annotations", handler.HandleTokenAnnotations(db))
	mux.HandleFunc("/internal/backend-api/token-annotations/", handler.HandleTokenAnnotationByID(db))

	// Phase 3 X2: keyword-triage (metric-intents routes removed).
	mux.HandleFunc("/internal/backend-api/keyword-triage/queue", handler.HandleTriageQueue(db))
	mux.HandleFunc("/internal/backend-api/keyword-triage/token", handler.HandleTriageToken(db))
	mux.HandleFunc("/internal/backend-api/keyword-triage/assign", handler.HandleTriageAssign(db))
	mux.HandleFunc("/internal/backend-api/keyword-triage/objects-tree", handler.HandleTriageObjectsTree(db))

	// Phase 3 X3: sql-passthrough (ont_*/lakehouse_* direct SQL) +
	// lakehouse-sql (project-scoped lakehouse_* direct SQL).
	mux.HandleFunc("/internal/backend-api/sql-passthrough", handler.HandleSQLPassthrough(db))
	mux.HandleFunc("/internal/backend-api/sql-passthrough/schema", handler.HandleSQLPassthroughSchema(db))
	mux.HandleFunc("/internal/backend-api/sql-passthrough/history", handler.HandleSQLPassthroughHistory(db))
	mux.HandleFunc("/internal/backend-api/sql-passthrough/snippets", handler.HandleSQLPassthroughSnippets(db))
	mux.HandleFunc("/internal/backend-api/sql-passthrough/snippets/", handler.HandleSQLPassthroughSnippetByID(db))
	mux.HandleFunc("/internal/backend-api/lakehouse-sql/execute", handler.HandleLakehouseSQLExecute(db))
	mux.HandleFunc("/internal/backend-api/lakehouse-sql/schema", handler.HandleLakehouseSQLSchema(db))
	mux.HandleFunc("/internal/backend-api/lakehouse-sql/history", handler.HandleLakehouseSQLHistory(db))
	mux.HandleFunc("/internal/backend-api/lakehouse-sql/snippets", handler.HandleLakehouseSQLSnippets(db))
	mux.HandleFunc("/internal/backend-api/lakehouse-sql/snippets/", handler.HandleLakehouseSQLSnippetByID(db))
	// Phase 3 X4: ontology export/import (full bundle download/upload).
	mux.HandleFunc("/internal/backend-api/export", handler.HandleOntologyExport(db))
	mux.HandleFunc("/internal/backend-api/import", handler.HandleOntologyImport(db))

	// 2026-04-25: per-user MCP API key management (internal side unused in
	// practice — UI calls /api/ontology/mcp-keys via registerPublicAPI).
	mux.HandleFunc("/internal/backend-api/mcp-keys", handler.HandleMCPKeys(db))
	mux.HandleFunc("/internal/backend-api/mcp-keys/", handler.HandleMCPKeyByID(db))

	// Phase 4C.3: auth + project + config routes migrated from monolith
	// handler pkg (→ services/backend-api/core). They mount directly at
	// /api/auth/*, /api/projects, /api/prompt-config, /api/llm-config,
	// /api/llm-role-binding — the public-bearer-auth side only, since
	// /internal/* equivalents would be meaningless for browser-level auth.
	core.RegisterAuthRoutes(mux, db)
	core.RegisterAdminRoutes(mux, db)
	core.RegisterProjectRoutes(mux, db)
	core.RegisterPromptConfigRoutes(mux, db)
	core.RegisterLLMConfigRoutes(mux, db)

	// Phase 4C: public /api/* routes for browser-direct access. Same
	// handler factories as the /internal/backend-api/* surface above;
	// authmw.Wrap enforces user-bearer auth on /api/* (vs INTERNAL_TOKEN
	// on /internal/*) so the two paths are auth-isolated. When
	// NEXT_PUBLIC_BACKEND_API_URL points the frontend here, the monolith
	// proxy becomes optional.
	registerPublicAPI(mux, db)

	auth := authmw.New(db, authmw.NewDBAuditWriter(db))
	// Middleware order (outer → inner):
	//   CORSMiddleware         — OPTIONS preflight + Access-Control-*
	//     headers for browser-direct fetches. Reads CORS_ALLOW_ORIGINS
	//     env (comma-separated). OUTERMOST so preflight short-circuits
	//     before auth / tracing.
	//   TraceContextMiddleware — extract W3C traceparent so spans nest
	//     under the caller's span in Jaeger.
	//   ServerSpanMiddleware   — open a server span per request so
	//     backend-api shows up as its own service in Jaeger.
	//   auth.Wrap              — INTERNAL_TOKEN on /internal/* +
	//     user-bearer on /api/* (see pkg/authmw).
	//   mux                    — business handlers.
	//   srvkit.RecoverMiddleware — recover handler panics (→ 500) just
	//     inside CORS, so panics are caught while CORS headers are still set.
	handler := httputil.CORSMiddleware(os.Getenv("CORS_ALLOW_ORIGINS"))(
		srvkit.RecoverMiddleware(
			observability.TraceContextMiddleware(
				observability.ServerSpanMiddleware([]string{"/healthz", "/metrics"})(
					auth.Wrap(mux)))))

	addr := ":" + *port
	log.Printf("▼// backend-api listening on %s (DB OK, tracer=backend-api)", addr)

	// Graceful shutdown: srvkit.Run drains HTTP on SIGINT/SIGTERM, then the
	// deferred db.Close() + obsShutdown() run (traces flush last).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srvkit.Run(ctx, addr, handler); err != nil {
		log.Fatal(err)
	}
}
