package recall

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/lakehouse2ontology/observability"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// BuildLakehouseContextCached is the ledger-aware variant of
// BuildLakehouseContext. Tokens that are strongly cached (per the
// CachedContext) are skipped from DB recall — their Ods / Intents are
// spliced directly from the cache, and a "CACHED" pseudo-hit is
// recorded in TokenDetails so operator debug can see the reuse.
//
// Invariant: the returned RecallResult is the SAME shape as
// BuildLakehouseContext's return — downstream formatters and the
// handler don't care which path produced each Od/Intent.
//
// cached==nil is equivalent to calling BuildLakehouseContext directly.
//
// After splicing, Od-link resolution and ambiguity detection are
// re-run over the UNION of hot+cold Ods. This is the D3 requirement
// from the plan: join_key edges between Ods first seen in different
// turns must surface in the current turn's render, even though
// neither Od is "new" this turn.
func BuildLakehouseContextCached(ctx context.Context, db *sql.DB, projectID string,
	tokens []string, question string, cached *CachedContext,
) RecallResult {
	_, span := observability.Tracer().Start(ctx, "recall.build_context",
		trace.WithAttributes(
			attribute.String("project_id", projectID),
			attribute.Int("token_count", len(tokens)),
			attribute.Bool("cached", cached != nil),
		))
	defer span.End()
	start := time.Now()
	defer func() {
		observability.RecallBuildDuration.Observe(float64(time.Since(start).Milliseconds()))
	}()

	if cached == nil || len(cached.Tokens) == 0 {
		// No cache — legacy path, identical behaviour.
		return BuildLakehouseContext(ctx, db, projectID, tokens, question)
	}

	hot, cold := partitionTokens(tokens, cached)

	// Run legacy recall on the hot tokens only. If nothing is hot,
	// we still need to call this with an empty token slice so the
	// RecallResult scaffolding (TokenDetails map, etc.) is initialised
	// — but BuildLakehouseContext returns early with a meaningful
	// empty result in that case.
	var result RecallResult
	if len(hot) > 0 {
		result = BuildLakehouseContext(ctx, db, projectID, hot, question)
	} else {
		result = RecallResult{TokenDetails: make(map[string][]KeywordHit)}
	}

	// Splice cold tokens' cached Ods / Intents into the result.
	spliceColdTokens(&result, cold, cached)

	// Re-run Od-link resolution on the union of hot+cold Ods. Legacy
	// BuildLakehouseContext only called this when it had ≥2 Ods of
	// its own; after the splice the count may have grown past 2, OR
	// the cross-turn link (cold Od ↔ hot Od) is what we're trying to
	// surface. Safe to call unconditionally — internal linkSeen dedup
	// makes it idempotent.
	if len(result.OdBlocks) >= 2 {
		// Reset Links on all blocks so the re-resolve produces a
		// clean union (without this, the first resolve's dedup is
		// still in memory but targets may no longer exist).
		for i := range result.OdBlocks {
			result.OdBlocks[i].Links = nil
		}
		resolveLakehouseOdLinks(db, projectID, &result)
		result.Ambiguities = detectLakehouseAmbiguities(db, projectID, result.OdBlocks)
	}

	// HasMatches + Format with all tokens (cold + hot) so the context
	// header lists every token the user actually used this turn.
	result.HasMatches = len(result.OdBlocks) > 0 || len(result.OkEntries) > 0 ||
		len(result.DirectOds) > 0 || len(result.OlEntries) > 0 ||
		len(result.MetricIntents) > 0
	result.ContextMD = FormatContext(result, tokens, question)
	return result
}

// partitionTokens splits the incoming token list into (hot, cold)
// according to CachedContext.IsCold. Order within each bucket is
// preserved so downstream rendering is deterministic.
func partitionTokens(tokens []string, cached *CachedContext) (hot, cold []string) {
	for _, t := range tokens {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if cached.IsCold(t) {
			cold = append(cold, t)
		} else {
			hot = append(hot, t)
		}
	}
	return hot, cold
}

// spliceColdTokens folds cached Ods / Intents into the result for
// tokens classified cold. A synthetic TokenDetails entry is added
// with Tier="CACHED" so the debug panel / operator logs can tell
// which tokens hit the cache vs. fresh DB.
func spliceColdTokens(result *RecallResult, coldTokens []string, cached *CachedContext) {
	if result.TokenDetails == nil {
		result.TokenDetails = make(map[string][]KeywordHit)
	}
	// Dedup Od/Intent IDs already present in result.
	seenOd := map[string]bool{}
	for _, b := range result.OdBlocks {
		seenOd[b.OdID] = true
	}
	seenDirectOd := map[string]bool{}
	for _, b := range result.DirectOds {
		seenDirectOd[b.OdID] = true
	}
	seenIntent := map[string]bool{}
	for _, mi := range result.MetricIntents {
		seenIntent[mi.IntentID] = true
	}
	seenOk := map[string]bool{}
	for _, e := range result.OkEntries {
		seenOk[e.ID] = true
	}
	seenOl := map[string]bool{}
	for _, e := range result.OlEntries {
		seenOl[e.ID] = true
	}

	for _, tok := range coldTokens {
		tc, ok := cached.Tokens[tok]
		if !ok {
			continue
		}
		// Pseudo-hit for debugging / pretty-print.
		result.TokenDetails[tok] = []KeywordHit{{
			Keyword:      tok,
			Tier:         "CACHED",
			Score:        1.0,
			MatchedToken: tok,
		}}
		for _, odID := range tc.MatchedOdIDs {
			if seenOd[odID] {
				continue
			}
			if od, present := cached.Ods[odID]; present {
				result.OdBlocks = append(result.OdBlocks, od)
				seenOd[odID] = true
			}
		}
		for _, iid := range tc.MatchedIntentIDs {
			if seenIntent[iid] {
				continue
			}
			if mi, present := cached.Intents[iid]; present {
				result.MetricIntents = append(result.MetricIntents, mi)
				seenIntent[iid] = true
			}
		}
	}
	// Replay any cached Ok/Ol that were keyed to these cold tokens.
	// (Current ledger design puts all Ok/Ol globally — they're per-
	// thread, not per-token — so unconditionally splice everything
	// in the cache that isn't already in the result. This mirrors
	// the BuildOlIndex system-prompt injection.)
	for id, e := range cached.OkEntries {
		if !seenOk[id] {
			result.OkEntries = append(result.OkEntries, e)
			seenOk[id] = true
		}
	}
	for id, e := range cached.OlEntries {
		if !seenOl[id] {
			result.OlEntries = append(result.OlEntries, e)
			seenOl[id] = true
		}
	}
}
