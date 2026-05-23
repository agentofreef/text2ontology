package ledger

import (
	"sort"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// BuildCachedContext projects a Ledger into the read-only view that
// recall.BuildLakehouseContextCached consumes. Called by the handler
// once per turn after Load (and possibly Rebuild) but before invoking
// the cached recall.
//
// Only StrongHit tokens flow through — the cache's job is to feed the
// hot/cold partition. Weak (FUZZY / VEC) tokens stay hot so a future
// turn with sharper context refreshes them.
//
// Ods and Intents unfold whole (all entries), independent of tokens.
// This lets recall splice a cached Od even when the user this turn
// names it via a fresh token not yet seen (the token will still be
// hot and hit DB for its own details, but the Od itself remains
// cached through its odId).
func BuildCachedContext(l *Ledger) *recall.CachedContext {
	if l == nil {
		return nil
	}
	c := &recall.CachedContext{
		Tokens:    make(map[string]recall.CachedToken, len(l.Tokens)),
		Ods:       make(map[string]recall.OdBlock, len(l.Ods)),
		Intents:   make(map[string]recall.MetricIntent, len(l.Intents)),
		OkEntries: make(map[string]recall.OkEntry, len(l.OkEntries)),
		OlEntries: make(map[string]recall.OlEntry, len(l.OlEntries)),
	}
	for tok, t := range l.Tokens {
		if !t.StrongHit {
			continue
		}
		props := make([]recall.CachedPropRef, 0, len(t.MatchedProps))
		for _, p := range t.MatchedProps {
			props = append(props, recall.CachedPropRef{PropID: p.PropID, OdID: p.OdID})
		}
		c.Tokens[tok] = recall.CachedToken{
			StrongHit:        t.StrongHit,
			MatchedOdIDs:     append([]string(nil), t.MatchedOds...),
			MatchedIntentIDs: append([]string(nil), t.MatchedIntents...),
			MatchedProps:     props,
		}
	}
	for id, od := range l.Ods {
		c.Ods[id] = od.OdBlock
	}
	for id, mi := range l.Intents {
		c.Intents[id] = mi.MetricIntent
	}
	for id, ok := range l.OkEntries {
		c.OkEntries[id] = ok.OkEntry
	}
	for id, ol := range l.OlEntries {
		c.OlEntries[id] = ol.OlEntry
	}
	return c
}

// AccumulatedMetricIntents returns every MetricIntent the thread has
// surfaced across all turns so far (the ledger merges them via
// mergeIntent on each turn's recall). The reachability judge unions
// these with the CURRENT turn's recall so a follow-up that reuses an
// earlier turn's metric ("那 AP 呢?") is not refused for "no metric".
// Returns a stable, deterministic order (sorted by intent name) so the
// decompose prompt is reproducible. Nil-safe: a nil/empty ledger
// returns nil.
func (l *Ledger) AccumulatedMetricIntents() []recall.MetricIntent {
	if l == nil || len(l.Intents) == 0 {
		return nil
	}
	out := make([]recall.MetricIntent, 0, len(l.Intents))
	for _, mi := range l.Intents {
		out = append(out, mi.MetricIntent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
