package recall

import (
	"context"
	"database/sql"
	"log"
)

// Phase 1 R4c: this file replaces the in-process recall pipeline that
// used to live in recall_lakehouse.go + recall_cached.go. The monolith
// now delegates every BuildLakehouseContext[Cached] call to recall-server
// over HTTP (see client.go). RECALL_SERVER_URL + INTERNAL_TOKEN are
// mandatory — log.Fatal at first use rather than silently returning an
// empty result.
//
// The *sql.DB parameter is retained on both public entry points so
// callers (handler_agent_lakehouse, handler_ledger_debug,
// handler_lh_testing*, handler_agent_annotations, ledger/rebuild.go,
// etc.) keep their existing signatures. It is no longer used inside
// these functions.

// BuildLakehouseContext performs the uncached Od recall via the remote
// recall-server. Wire shape matches the former in-process function.
func BuildLakehouseContext(ctx context.Context, db *sql.DB, projectID string, tokens []string, question string) RecallResult {
	_ = db
	c := recallRemote()
	if c == nil {
		log.Fatal("RECALL_SERVER_URL is required — monolith no longer runs an in-process recall pipeline after Phase 1 R4c. Set RECALL_SERVER_URL=http://127.0.0.1:18093 + INTERNAL_TOKEN (see .env.shared).")
	}
	return c.BuildContext(ctx, projectID, tokens, question, nil)
}

// BuildLakehouseContextCached is the ledger-aware variant — sends the
// CachedContext along so the service dispatches to its cached path.
func BuildLakehouseContextCached(ctx context.Context, db *sql.DB, projectID string,
	tokens []string, question string, cached *CachedContext,
) RecallResult {
	_ = db
	c := recallRemote()
	if c == nil {
		log.Fatal("RECALL_SERVER_URL is required — monolith no longer runs an in-process recall pipeline after Phase 1 R4c.")
	}
	return c.BuildContext(ctx, projectID, tokens, question, cached)
}
