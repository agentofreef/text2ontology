// Package handler is the HTTP handler layer for services/backend-api.
//
// Phase 3 B2 shadow-copies a first batch of leaf CRUD handlers from
// services/backend-api/handler/: versions, aliases, rules, methods, skills, links.
// The files themselves (handler_version.go, handler_alias.go, etc.)
// are near-verbatim copies of the monolith originals — only the
// package declaration changed from `ontology` to `handler`, so
// `git diff --stat` against the monolith stays readable.
//
// This file (exports.go) lifts the lowercase symbols to uppercase so
// cmd/server/main.go (package main) can reference them. The pattern
// mirrors services/agent-server/handler/exports.go. No Phase-3 main.go
// route table references these yet — B3 is where /internal/backend-api/*
// routes get wired + the monolith adds a reverse proxy.
package handler

import (
	"database/sql"
	"net/http"
)

// ── ont_alias ──────────────────────────────────────────────────────────────

// HandleAliases returns the ont_alias list + create factory.
func HandleAliases(db *sql.DB) http.HandlerFunc { return handleAliases(db) }

// HandleAliasByID returns the ont_alias get/update/delete factory.
func HandleAliasByID(db *sql.DB) http.HandlerFunc { return handleAliasByID(db) }

// ── ont_resolution_rule ────────────────────────────────────────────────────

// HandleRules returns the ont_resolution_rule list + create factory.
func HandleRules(db *sql.DB) http.HandlerFunc { return handleRules(db) }

// HandleRuleByID returns the ont_resolution_rule get/update/delete factory.
func HandleRuleByID(db *sql.DB) http.HandlerFunc { return handleRuleByID(db) }

// ── ont_method ─────────────────────────────────────────────────────────────

// HandleMethods returns the ont_method list + create factory.
func HandleMethods(db *sql.DB) http.HandlerFunc { return handleMethods(db) }

// HandleMethodByID returns the ont_method get/update/delete factory.
func HandleMethodByID(db *sql.DB) http.HandlerFunc { return handleMethodByID(db) }

// ── ont_skill ──────────────────────────────────────────────────────────────

// HandleSkills returns the ont_skill list + create factory.
func HandleSkills(db *sql.DB) http.HandlerFunc { return handleSkills(db) }

// HandleSkillByID returns the ont_skill get/update/delete factory.
func HandleSkillByID(db *sql.DB) http.HandlerFunc { return handleSkillByID(db) }

// ── ont_link_type ──────────────────────────────────────────────────────────

// HandleLinks returns the ont_link_type list + create factory.
func HandleLinks(db *sql.DB) http.HandlerFunc { return handleLinks(db) }

// HandleLinkByID returns the ont_link_type get/update/delete factory.
func HandleLinkByID(db *sql.DB) http.HandlerFunc { return handleLinkByID(db) }

// ── Phase 3 B5 exports ─────────────────────────────────────────────────────

// ── ont_object_type + ont_property ─────────────────────────────────────────

// HandleObjects returns the ont_object_type list + create factory.
func HandleObjects(db *sql.DB) http.HandlerFunc { return handleObjects(db) }

// HandleObjectByID returns the ont_object_type get/update/delete factory.
func HandleObjectByID(db *sql.DB) http.HandlerFunc { return handleObjectByID(db) }

// HandleProperties returns the ont_property list + create factory
// (auto-creates a backing Ok via autoCreatePropertyKnowledge on save).
func HandleProperties(db *sql.DB) http.HandlerFunc { return handleProperties(db) }

// HandlePropertyByID returns the ont_property get/update/delete factory.
func HandlePropertyByID(db *sql.DB) http.HandlerFunc { return handlePropertyByID(db) }

// HandlePropertyNodes returns the property-tree-for-graph endpoint.
func HandlePropertyNodes(db *sql.DB) http.HandlerFunc { return handlePropertyNodes(db) }

// ── ont_metric ─────────────────────────────────────────────────────────────

// HandleMetrics returns the ont_metric list + create factory.
func HandleMetrics(db *sql.DB) http.HandlerFunc { return handleMetrics(db) }

// HandleMetricByID returns the ont_metric get/update/delete factory.
func HandleMetricByID(db *sql.DB) http.HandlerFunc { return handleMetricByID(db) }

// ── ont_knowledge (Ok) ─────────────────────────────────────────────────────

// HandleKnowledgeEntries returns the ont_knowledge list + create factory.
func HandleKnowledgeEntries(db *sql.DB) http.HandlerFunc { return handleKnowledgeEntries(db) }

// HandleKnowledgeEntryByID returns the ont_knowledge get/update/delete factory.
func HandleKnowledgeEntryByID(db *sql.DB) http.HandlerFunc { return handleKnowledgeEntryByID(db) }

// HandleKnowledgeGenerate returns the LLM-assisted Ok-generation endpoint.
func HandleKnowledgeGenerate(db *sql.DB) http.HandlerFunc { return handleKnowledgeGenerate(db) }

// HandleKnowledgeSyncProperties returns the bulk property→Ok reconciler.
func HandleKnowledgeSyncProperties(db *sql.DB) http.HandlerFunc {
	return handleKnowledgeSyncProperties(db)
}

// ── ont_learned_fact (Ol) + fact definitions / links ───────────────────────

// HandleLearnedFacts returns the ont_learned_fact list + create factory
// (writes embedAndSaveFactVector asynchronously on insert).
func HandleLearnedFacts(db *sql.DB) http.HandlerFunc { return handleLearnedFacts(db) }

// HandleLearnedFactByID returns the Ol get/update/delete factory.
func HandleLearnedFactByID(db *sql.DB) http.HandlerFunc { return handleLearnedFactByID(db) }

// HandleLearnedFactsRecomputeVectors re-embeds all Ol rows in batch.
func HandleLearnedFactsRecomputeVectors(db *sql.DB) http.HandlerFunc {
	return handleLearnedFactsRecomputeVectors(db)
}

// HandleFactDefinitions returns the fact-definition list + create factory.
func HandleFactDefinitions(db *sql.DB) http.HandlerFunc { return handleFactDefinitions(db) }

// HandleFactDefinitionByID returns the fact-definition get/update/delete factory.
func HandleFactDefinitionByID(db *sql.DB) http.HandlerFunc {
	return handleFactDefinitionByID(db)
}

// HandleFactLinks returns the fact-link list + create factory.
func HandleFactLinks(db *sql.DB) http.HandlerFunc { return handleFactLinks(db) }

// HandleFactLinkByID returns the fact-link get/update/delete factory.
func HandleFactLinkByID(db *sql.DB) http.HandlerFunc { return handleFactLinkByID(db) }

// ── Phase 3 X1 exports (causality / query-logs / token-annotations) ────────

// HandleCausalities returns the ont_causality list + create factory.
func HandleCausalities(db *sql.DB) http.HandlerFunc { return handleCausalities(db) }

// HandleCausalityByID returns the ont_causality get/update/delete factory.
func HandleCausalityByID(db *sql.DB) http.HandlerFunc { return handleCausalityByID(db) }

// HandleQueryLogs returns the query-log list + create factory.
func HandleQueryLogs(db *sql.DB) http.HandlerFunc { return handleQueryLogs(db) }

// HandleQueryLogByID returns the query-log get/update/delete factory.
func HandleQueryLogByID(db *sql.DB) http.HandlerFunc { return handleQueryLogByID(db) }

// HandleQueryLogTemplate returns the CSV template download endpoint.
func HandleQueryLogTemplate(db *sql.DB) http.HandlerFunc { return handleQueryLogTemplate(db) }

// HandleQueryLogUpload returns the CSV batch-import endpoint.
func HandleQueryLogUpload(db *sql.DB) http.HandlerFunc { return handleQueryLogUpload(db) }

// HandleQueryLogExport returns the CSV export endpoint.
func HandleQueryLogExport(db *sql.DB) http.HandlerFunc { return handleQueryLogExport(db) }

// HandleExampleQuestions returns the example-questions list endpoint.
func HandleExampleQuestions(db *sql.DB) http.HandlerFunc { return handleExampleQuestions(db) }

// HandleTokenAnnotations returns the token-annotation list + create factory.
func HandleTokenAnnotations(db *sql.DB) http.HandlerFunc { return handleTokenAnnotations(db) }

// HandleTokenAnnotationByID returns the token-annotation get/update/delete factory.
func HandleTokenAnnotationByID(db *sql.DB) http.HandlerFunc { return handleTokenAnnotationByID(db) }

// ── Phase 3 X2 exports (metric-intents + keyword-triage) ───────────────────

// HandleMetricIntents returns the lakehouse_metric_intent list + create factory.
func HandleMetricIntents(db *sql.DB) http.HandlerFunc { return handleMetricIntents(db) }

// HandleMetricIntentByID returns the metric-intent get/update/delete factory.
func HandleMetricIntentByID(db *sql.DB) http.HandlerFunc { return handleMetricIntentByID(db) }

// HandleIntentTriggers returns the metric-intent triggers CRUD factory.
func HandleIntentTriggers(db *sql.DB) http.HandlerFunc { return handleIntentTriggers(db) }

// HandleTriageQueue returns the keyword-triage queue endpoint.
func HandleTriageQueue(db *sql.DB) http.HandlerFunc { return handleTriageQueue(db) }

// HandleTriageToken returns the keyword-triage per-token detail endpoint.
func HandleTriageToken(db *sql.DB) http.HandlerFunc { return handleTriageToken(db) }

// HandleTriageAssign returns the keyword-triage assignment mutation factory.
func HandleTriageAssign(db *sql.DB) http.HandlerFunc { return handleTriageAssign(db) }

// HandleTriageObjectsTree returns the keyword-triage objects tree view.
func HandleTriageObjectsTree(db *sql.DB) http.HandlerFunc { return handleTriageObjectsTree(db) }

// ── Phase 3 X3 exports (sql-passthrough + lakehouse-sql) ───────────────────

// HandleSQLPassthrough returns the /sql-passthrough execute factory.
func HandleSQLPassthrough(db *sql.DB) http.HandlerFunc { return handleSQLPassthrough(db) }

// HandleSQLPassthroughSchema returns the /sql-passthrough schema endpoint.
func HandleSQLPassthroughSchema(db *sql.DB) http.HandlerFunc { return handleSQLPassthroughSchema(db) }

// HandleSQLPassthroughHistory returns the /sql-passthrough history endpoint.
func HandleSQLPassthroughHistory(db *sql.DB) http.HandlerFunc { return handleSQLPassthroughHistory(db) }

// HandleSQLPassthroughSnippets returns the /sql-passthrough snippets CRUD.
func HandleSQLPassthroughSnippets(db *sql.DB) http.HandlerFunc { return handleSQLPassthroughSnippets(db) }

// HandleSQLPassthroughSnippetByID returns the per-snippet factory.
func HandleSQLPassthroughSnippetByID(db *sql.DB) http.HandlerFunc {
	return handleSQLPassthroughSnippetByID(db)
}

// HandleLakehouseSQLExecute returns the /lakehouse-sql execute factory.
func HandleLakehouseSQLExecute(db *sql.DB) http.HandlerFunc { return handleLakehouseSQLExecute(db) }

// HandleLakehouseSQLSchema returns the /lakehouse-sql schema endpoint.
func HandleLakehouseSQLSchema(db *sql.DB) http.HandlerFunc { return handleLakehouseSQLSchema(db) }

// HandleLakehouseSQLHistory returns the /lakehouse-sql history endpoint.
func HandleLakehouseSQLHistory(db *sql.DB) http.HandlerFunc { return handleLakehouseSQLHistory(db) }

// HandleLakehouseSQLSnippets returns the /lakehouse-sql snippets CRUD.
func HandleLakehouseSQLSnippets(db *sql.DB) http.HandlerFunc { return handleLakehouseSQLSnippets(db) }

// HandleLakehouseSQLSnippetByID returns the per-snippet factory.
func HandleLakehouseSQLSnippetByID(db *sql.DB) http.HandlerFunc {
	return handleLakehouseSQLSnippetByID(db)
}

// ── Phase 3 X4 exports (ontology export/import) ────────────────────────────

// HandleOntologyExport returns the ontology bundle-download factory.
func HandleOntologyExport(db *sql.DB) http.HandlerFunc { return handleOntologyExport(db) }

// HandleOntologyImport returns the ontology bundle-upload factory.
func HandleOntologyImport(db *sql.DB) http.HandlerFunc { return handleOntologyImport(db) }

// ── MCP API keys (per-user) ─────────────────────────────────────────────────

// HandleMCPKeys returns the list/create factory for /api/ontology/mcp-keys.
func HandleMCPKeys(db *sql.DB) http.HandlerFunc { return handleMCPKeys(db) }

// HandleMCPKeyByID returns the revoke factory for /api/ontology/mcp-keys/{id}.
func HandleMCPKeyByID(db *sql.DB) http.HandlerFunc { return handleMCPKeyByID(db) }

// ── Builder Agent activation (US-005, US-006) ──────────────────────────────

// HandleBuilderActivateOd activates a pending builder-proposed Od:
// applies edits, flips mark=true on object + properties, inlines the
// SOLIDIFY canonical_query build, runs LIMIT-10 trial-run, and creates
// Ok knowledge per property. Single transaction, all-or-nothing.
func HandleBuilderActivateOd(db *sql.DB) http.HandlerFunc { return handleBuilderActivateOd(db) }

// HandleBuilderActivateIntent activates a pending builder-proposed Intent:
// applies edits, flips mark=true, and inserts trigger keywords into
// lakehouse_keyword (ON CONFLICT DO NOTHING).
func HandleBuilderActivateIntent(db *sql.DB) http.HandlerFunc { return handleBuilderActivateIntent(db) }

// HandleBuilderActivateLink activates a pending builder-proposed Link:
// applies edits, flips mark=true, ensures Ok knowledge exists for both
// endpoint properties, and inserts an ont_causality(relation_type='join_key')
// edge between them.
func HandleBuilderActivateLink(db *sql.DB) http.HandlerFunc { return handleBuilderActivateLink(db) }
