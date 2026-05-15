// services/mcp-tools-server/cmd/server/main.go
//
// Phase 3 MCP-1..MCP-3: mcp-tools-server is the external-facing MCP
// tool gateway. Unlike the other sidecar services (agent-server,
// recall-server, lakehouse-sql-server, backend-api) which are
// INTERNAL to the stack and authenticated via INTERNAL_TOKEN, MCP is
// exposed to third-party consumers — Claude Code, operator scripts,
// custom agents — and carries its own bearer-token auth
// (MCP_API_KEY).
//
// Routes (all require MCP_API_KEY via Authorization: Bearer … or
// X-API-Key):
//
//	POST /api/mcp/v1/tools/lookup_od          — Od schema lookup by name
//	POST /api/mcp/v1/tools/execute_smartquery — execute a QuerySpec
//	POST /api/mcp/v1/tools/recall_tokens      — ontology-aware recall
//
// v0 exposes REST only. The MCP stdio / HTTP+SSE protocol handshake
// is a follow-up (MCP-4).
//
// Required env:
//   MCP_API_KEY          incoming bearer token (external auth)
//   INTERNAL_TOKEN       outgoing bearer token (to call siblings)
//   RECALL_SERVER_URL    http://recall-server:8093 (compose) or :18093 (host)
//   LAKEHOUSE_SQL_URL    same pattern
//   BACKEND_API_URL      same pattern
//
// No DATABASE_URL — this service intentionally has no DB credentials.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/mcp-tools-server/auth"
	"github.com/lakehouse2ontology/services/mcp-tools-server/tools"
)

func main() {
	port := flag.String("port", "8095", "listen port")
	flag.Parse()

	obsCtx, obsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	obsShutdown, err := observability.Init(obsCtx, "mcp-tools-server")
	obsCancel()
	if err != nil {
		log.Fatalf("observability.Init: %v", err)
	}
	defer func() {
		sdCtx, sdCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sdCancel()
		_ = obsShutdown(sdCtx)
	}()

	// Fail fast on missing outgoing credentials so we surface config
	// errors at boot rather than on the first tool invocation.
	for _, v := range []string{"INTERNAL_TOKEN", "RECALL_SERVER_URL", "LAKEHOUSE_SQL_URL", "BACKEND_API_URL", "DATABASE_URL"} {
		if os.Getenv(v) == "" {
			log.Fatalf("%s is required for mcp-tools-server", v)
		}
	}

	// DB connection for the auth key store only. mcp-tools-server
	// intentionally has NO access to ontology / lakehouse tables —
	// ops/db-roles.sql restricts mcp_tools_server_user to SELECT +
	// UPDATE(last_used_at) on mcp_api_key.
	db, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
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
	observability.SetDB(db)
	authz := auth.New(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "mcp-tools-server",
			"tools":   []string{"lookup_od", "execute_smartquery", "recall_tokens"},
		})
	})
	mux.Handle("/metrics", observability.MetricsHandler())

	// REST dispatcher covers the plain /api/mcp/v1/tools/<name> shape.
	mux.HandleFunc("/api/mcp/v1/tools/", tools.Dispatch)

	// MCP Streamable-HTTP transport (JSON-RPC 2.0 over POST /mcp). Tool
	// set is the same as the REST surface; initialize + tools/list +
	// tools/call work for spec-compliant clients (Claude Code et al.).
	mux.HandleFunc("/mcp", tools.MCPHandler())

	// Middleware order (outer → inner):
	//   TraceContextMiddleware      — extract W3C parent span (if any).
	//   ServerSpanMiddlewareForPrefixes — span only /api/mcp/* (skip
	//     /healthz + /metrics probes; useful when external consumers
	//     poll healthz aggressively).
	//   auth.Middleware             — MCP_API_KEY bearer-token check
	//                                 (skips /healthz + /metrics
	//                                 internally so probes work without
	//                                 credentials).
	//   mux                         — /api/mcp/v1/tools/ dispatcher.
	// Phase 4C.2: CORSMiddleware wraps outermost so browser-driven
	// Claude Code-style clients can reach /api/mcp/* + /mcp from a
	// cross-origin page without running into ACAO blocks.
	handler := httputil.CORSMiddleware(os.Getenv("CORS_ALLOW_ORIGINS"))(
		observability.TraceContextMiddleware(
			observability.ServerSpanMiddlewareForPrefixes([]string{"/api/mcp/", "/mcp"})(
				authz.Middleware()(mux))))

	addr := ":" + *port
	log.Printf("▼// mcp-tools-server listening on %s (tracer=mcp-tools-server)", addr)
	log.Printf("   recall-server  → %s", os.Getenv("RECALL_SERVER_URL"))
	log.Printf("   lakehouse-sql  → %s", os.Getenv("LAKEHOUSE_SQL_URL"))
	log.Printf("   backend-api    → %s", os.Getenv("BACKEND_API_URL"))
	log.Fatal(http.ListenAndServe(addr, handler))
}
