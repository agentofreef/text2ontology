package main

import (
	"database/sql"
	"net/http"

	"github.com/lakehouse2ontology/services/backend-api/handler"
)

// registerPublicAPI mirrors the /internal/backend-api/* handler surface
// at public /api/* paths for Phase 4C (真分离). Same handler factories;
// authmw.Wrap enforces user-bearer auth on /api/* (vs INTERNAL_TOKEN on
// /internal/*), so the two entry points are auth-isolated and can coexist.
//
// Path mapping chosen to keep the legacy monolith URLs stable (so the
// frontend just swaps its base URL — no per-endpoint path changes):
//
//	/internal/backend-api/<entity>     →  /api/ontology/<entity>
//	/internal/backend-api/lakehouse-sql → /api/lakehouse-sql
//	/internal/backend-api/sql-passthrough → /api/ontology/sql-passthrough
//	/internal/backend-api/export|import   → /api/ontology/{export,import}
func registerPublicAPI(mux *http.ServeMux, db *sql.DB) {
	// B2/B3 leaf CRUD
	mux.HandleFunc("/api/ontology/aliases", handler.HandleAliases(db))
	mux.HandleFunc("/api/ontology/aliases/", handler.HandleAliasByID(db))
	mux.HandleFunc("/api/ontology/rules", handler.HandleRules(db))
	mux.HandleFunc("/api/ontology/rules/", handler.HandleRuleByID(db))
	mux.HandleFunc("/api/ontology/methods", handler.HandleMethods(db))
	mux.HandleFunc("/api/ontology/methods/", handler.HandleMethodByID(db))
	mux.HandleFunc("/api/ontology/skills", handler.HandleSkills(db))
	mux.HandleFunc("/api/ontology/skills/", handler.HandleSkillByID(db))
	mux.HandleFunc("/api/ontology/links", handler.HandleLinks(db))
	mux.HandleFunc("/api/ontology/links/", handler.HandleLinkByID(db))

	// B5 richer CRUD
	mux.HandleFunc("/api/ontology/objects", handler.HandleObjects(db))
	// Bulk operations — registered as specific paths so they win over the
	// /objects/ catch-all (Go mux: exact paths beat trailing-slash prefixes).
	mux.HandleFunc("/api/ontology/objects/bulk-impact", handler.HandleObjectsBulkImpact(db))
	mux.HandleFunc("/api/ontology/objects/bulk-delete", handler.HandleObjectsBulkDelete(db))
	mux.HandleFunc("/api/ontology/objects/bulk-update", handler.HandleObjectsBulkUpdate(db))
	mux.HandleFunc("/api/ontology/objects/bulk-create", handler.HandleObjectsBulkCreate(db))
	mux.HandleFunc("/api/ontology/objects/", handler.HandleObjectByID(db))
	mux.HandleFunc("/api/ontology/properties", handler.HandleProperties(db))
	mux.HandleFunc("/api/ontology/properties/", handler.HandlePropertyByID(db))
	mux.HandleFunc("/api/ontology/property-nodes", handler.HandlePropertyNodes(db))
	mux.HandleFunc("/api/ontology/metrics", handler.HandleMetrics(db))
	mux.HandleFunc("/api/ontology/metrics/", handler.HandleMetricByID(db))
	mux.HandleFunc("/api/ontology/knowledge", handler.HandleKnowledgeEntries(db))
	mux.HandleFunc("/api/ontology/knowledge/", handler.HandleKnowledgeEntryByID(db))
	mux.HandleFunc("/api/ontology/knowledge-generate", handler.HandleKnowledgeGenerate(db))
	mux.HandleFunc("/api/ontology/knowledge-sync-properties", handler.HandleKnowledgeSyncProperties(db))
	mux.HandleFunc("/api/ontology/learned-facts", handler.HandleLearnedFacts(db))
	mux.HandleFunc("/api/ontology/learned-facts/", handler.HandleLearnedFactByID(db))
	mux.HandleFunc("/api/ontology/learned-facts-recompute", handler.HandleLearnedFactsRecomputeVectors(db))
	mux.HandleFunc("/api/ontology/fact-definitions", handler.HandleFactDefinitions(db))
	mux.HandleFunc("/api/ontology/fact-definitions/", handler.HandleFactDefinitionByID(db))
	mux.HandleFunc("/api/ontology/fact-links", handler.HandleFactLinks(db))
	mux.HandleFunc("/api/ontology/fact-links/", handler.HandleFactLinkByID(db))

	// X1 leaf CRUD
	mux.HandleFunc("/api/ontology/causality", handler.HandleCausalities(db))
	mux.HandleFunc("/api/ontology/causality/", handler.HandleCausalityByID(db))
	mux.HandleFunc("/api/ontology/query-logs", handler.HandleQueryLogs(db))
	mux.HandleFunc("/api/ontology/query-logs/", handler.HandleQueryLogByID(db))
	mux.HandleFunc("/api/ontology/query-logs-template", handler.HandleQueryLogTemplate(db))
	mux.HandleFunc("/api/ontology/query-logs-upload", handler.HandleQueryLogUpload(db))
	mux.HandleFunc("/api/ontology/query-logs-export", handler.HandleQueryLogExport(db))
	mux.HandleFunc("/api/ontology/example-questions", handler.HandleExampleQuestions(db))
	mux.HandleFunc("/api/ontology/token-annotations", handler.HandleTokenAnnotations(db))
	mux.HandleFunc("/api/ontology/token-annotations/", handler.HandleTokenAnnotationByID(db))

	// X2 keyword-triage (metric-intents routes removed — /api/ontology/metric-intents* → 404)
	// Unified metric (lakehouse_metric).
	mux.HandleFunc("/api/ontology/lakehouse-metrics", handler.HandleLakehouseMetrics(db))
	mux.HandleFunc("/api/ontology/lakehouse-metrics/", handler.HandleLakehouseMetricByID(db))
	mux.HandleFunc("/api/ontology/keyword-triage/queue", handler.HandleTriageQueue(db))
	mux.HandleFunc("/api/ontology/keyword-triage/token", handler.HandleTriageToken(db))
	mux.HandleFunc("/api/ontology/keyword-triage/assign", handler.HandleTriageAssign(db))
	mux.HandleFunc("/api/ontology/keyword-triage/objects-tree", handler.HandleTriageObjectsTree(db))

	// X3 SQL passthrough + lakehouse-sql
	mux.HandleFunc("/api/ontology/sql-passthrough", handler.HandleSQLPassthrough(db))
	mux.HandleFunc("/api/ontology/sql-passthrough/schema", handler.HandleSQLPassthroughSchema(db))
	mux.HandleFunc("/api/ontology/sql-passthrough/history", handler.HandleSQLPassthroughHistory(db))
	mux.HandleFunc("/api/ontology/sql-passthrough/snippets", handler.HandleSQLPassthroughSnippets(db))
	mux.HandleFunc("/api/ontology/sql-passthrough/snippets/", handler.HandleSQLPassthroughSnippetByID(db))
	mux.HandleFunc("/api/lakehouse-sql/execute", handler.HandleLakehouseSQLExecute(db))
	mux.HandleFunc("/api/lakehouse-sql/schema", handler.HandleLakehouseSQLSchema(db))
	mux.HandleFunc("/api/lakehouse-sql/history", handler.HandleLakehouseSQLHistory(db))
	mux.HandleFunc("/api/lakehouse-sql/snippets", handler.HandleLakehouseSQLSnippets(db))
	mux.HandleFunc("/api/lakehouse-sql/snippets/", handler.HandleLakehouseSQLSnippetByID(db))

	// X4 ontology export/import
	mux.HandleFunc("/api/ontology/export", handler.HandleOntologyExport(db))
	mux.HandleFunc("/api/ontology/import", handler.HandleOntologyImport(db))

	// 2026-04-25 · per-user MCP API keys (settings UI)
	mux.HandleFunc("/api/ontology/mcp-keys", handler.HandleMCPKeys(db))
	mux.HandleFunc("/api/ontology/mcp-keys/", handler.HandleMCPKeyByID(db))

	// 2026-04-28 · OD Builder Agent activation endpoints (US-005, US-006).
	// Each does a single tx that flips a pending builder-proposed row
	// (origin='builder', mark=false) into an activated row + side-effects.
	mux.HandleFunc("/api/ontology/builder/activate-od", handler.HandleBuilderActivateOd(db))
	mux.HandleFunc("/api/ontology/builder/activate-intent", handler.HandleBuilderActivateIntent(db))
	mux.HandleFunc("/api/ontology/builder/activate-link", handler.HandleBuilderActivateLink(db))

	// 2026-04-29 · ingest_job — durable background-task queue.
	// List + detail + cancel for the global Tasks Drawer.
	mux.HandleFunc("/api/jobs", handler.HandleJobs(db))
	mux.HandleFunc("/api/jobs/", handler.HandleJobByID(db))
}
