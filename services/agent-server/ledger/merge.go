package ledger

import (
	"strings"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// MergeRecallResult folds a recall.RecallResult into the ledger,
// recording `turn` as the firstSeen / loadedInTurn marker for any
// genuinely new entry. Existing entries are updated in place — the
// operation is idempotent on re-merge and commutative across turns.
//
// Returns a Delta describing what was NEW in this merge (vs what was
// already in the ledger). Callers use the delta to render the "本轮新
// 识别" block without re-rendering cached items from the ledger.
func (l *Ledger) MergeRecallResult(r recall.RecallResult, turn int) Delta {
	l.EnsureMaps()
	d := Delta{}

	// Ods — merge both matched and direct Od blocks.
	for _, blk := range r.OdBlocks {
		if l.mergeOd(blk, "recall-hit", turn) {
			d.NewOdIDs = append(d.NewOdIDs, blk.OdID)
		}
	}
	for _, blk := range r.DirectOds {
		if l.mergeOd(blk, "recall-fallback", turn) {
			d.NewOdIDs = append(d.NewOdIDs, blk.OdID)
		}
	}

	// Intents.
	for _, mi := range r.MetricIntents {
		if l.mergeIntent(mi, turn) {
			d.NewIntentIDs = append(d.NewIntentIDs, mi.IntentID)
		}
	}

	// Ok entries.
	for _, ok := range r.OkEntries {
		if _, already := l.OkEntries[ok.ID]; !already {
			l.OkEntries[ok.ID] = &LedgerOk{OkEntry: ok, FirstSeenInTurn: turn}
			d.NewOkIDs = append(d.NewOkIDs, ok.ID)
		}
	}

	// Ol entries.
	for _, ol := range r.OlEntries {
		if _, already := l.OlEntries[ol.ID]; !already {
			l.OlEntries[ol.ID] = &LedgerOl{OlEntry: ol, FirstSeenInTurn: turn}
			d.NewOlIDs = append(d.NewOlIDs, ol.ID)
		}
	}

	// Tokens — this is the core of hot/cold partition. Record every
	// token we saw this turn even if it had no hits (MISS tokens stay
	// in ledger with StrongHit=false so the partition knows they're
	// not cold). But StrongHit only flips to true on EXACT / Intent
	// match.
	for tok, hits := range r.TokenDetails {
		if strings.TrimSpace(tok) == "" {
			continue
		}
		entry, existed := l.Tokens[tok]
		if !existed {
			entry = &LedgerToken{FirstSeen: turn, LastSeen: turn}
			l.Tokens[tok] = entry
		}
		entry.LastSeen = turn

		// StrongHit = any EXACT keyword-hit OR any Intent matched on
		// this token. FUZZY / VEC stays soft — don't poison the ledger.
		for _, h := range hits {
			if h.Tier == "EXACT" {
				entry.StrongHit = true
			}
		}
		// Attach Od / Prop back-refs from this turn's hits.
		for _, h := range hits {
			// KeywordHit doesn't carry PropertyID / OdID directly;
			// recall attaches them via MappedTable/MappedField. Use
			// RecallResult.OdBlocks to resolve.
			for _, blk := range r.OdBlocks {
				if !strings.EqualFold(blk.Name, h.MappedTable) {
					continue
				}
				entry.MatchedOds = appendUniqueStr(entry.MatchedOds, blk.OdID)
				for _, p := range blk.MatchedProps {
					if strings.EqualFold(p.Name, h.MappedField) ||
						strings.EqualFold(p.DisplayName, h.MappedField) {
						entry.MatchedProps = appendUniquePropRef(entry.MatchedProps,
							LedgerPropRef{PropID: p.PropertyID, OdID: blk.OdID})
					}
				}
			}
			for _, blk := range r.DirectOds {
				if strings.EqualFold(blk.Name, h.MappedTable) {
					entry.MatchedOds = appendUniqueStr(entry.MatchedOds, blk.OdID)
				}
			}
		}
		// Intent back-refs via MetricIntent.MatchedTokens.
		for _, mi := range r.MetricIntents {
			for _, mt := range mi.MatchedTokens {
				if strings.EqualFold(mt, tok) {
					entry.MatchedIntents = appendUniqueStr(entry.MatchedIntents, mi.IntentID)
					entry.StrongHit = true
				}
			}
		}
	}

	return d
}

// MergeLookupOd upserts a fully-loaded Od (from lakehouseToolLookup's
// direct path) with LoadMethod="lookup". Used by the handler when
// the LLM explicitly names an Od — those carry more detail than recall
// can produce (all properties + all Ok entries).
//
// Returns true if the Od was new to the ledger.
func (l *Ledger) MergeLookupOd(blk recall.OdBlock, turn int) bool {
	l.EnsureMaps()
	return l.mergeOd(blk, "lookup", turn)
}

// mergeOd is the shared upsert path for all Od merges. It picks the
// strongest load method between existing and incoming (lookup wins over
// recall-hit wins over recall-fallback wins over legacy-migrated).
func (l *Ledger) mergeOd(blk recall.OdBlock, method string, turn int) bool {
	if blk.OdID == "" {
		return false
	}
	existing, isNew := l.Ods[blk.OdID], false
	if existing == nil {
		isNew = true
		existing = &LedgerOd{
			OdBlock:      blk,
			LoadedInTurn: turn,
			LoadMethod:   method,
		}
		l.Ods[blk.OdID] = existing
		return isNew
	}

	// Stronger load method overrides the recorded method + refreshes
	// the block contents. "lookup" > "recall-hit" > "recall-fallback"
	// > "legacy-migrated".
	if loadMethodStrength(method) > loadMethodStrength(existing.LoadMethod) {
		existing.OdBlock = blk
		existing.LoadMethod = method
	}

	// Always refresh AllPropNames / AllPropDescs if the incoming is
	// more complete (length is the cheapest proxy). Also union
	// MatchedProps and Links.
	if len(blk.AllPropNames) > len(existing.AllPropNames) {
		existing.AllPropNames = blk.AllPropNames
	}
	if len(blk.AllPropDescs) > len(existing.AllPropDescs) {
		existing.AllPropDescs = blk.AllPropDescs
	}
	existing.MatchedProps = mergePropertyMatches(existing.MatchedProps, blk.MatchedProps)
	existing.Links = mergeLinks(existing.Links, blk.Links)
	existing.MatchedVia = mergeStrings(existing.MatchedVia, blk.MatchedVia)

	return isNew
}

// mergeIntent upserts a MetricIntent, union'ing MatchedTokens and
// replacing authoritative fields from the incoming DB row. The Intent
// definition itself is controlled by ontology admin; recall just
// surfaces it, so the incoming copy is always the source of truth for
// canonical_metric / filters / groupBy / pivot config.
//
// Returns true if the Intent was new to the ledger.
func (l *Ledger) mergeIntent(mi recall.MetricIntent, turn int) bool {
	if mi.IntentID == "" {
		return false
	}
	existing := l.Intents[mi.IntentID]
	if existing == nil {
		l.Intents[mi.IntentID] = &LedgerIntent{
			MetricIntent:    mi,
			FirstSeenInTurn: turn,
		}
		return true
	}
	// Refresh authoritative fields; preserve FirstSeenInTurn.
	tokens := mergeStrings(existing.MatchedTokens, mi.MatchedTokens)
	existing.MetricIntent = mi
	existing.MatchedTokens = tokens
	return false
}

// Delta records what entries this merge added net-new to the ledger.
// Callers render this as the "本轮新识别" block to avoid duplicating
// already-cached content.
type Delta struct {
	NewOdIDs     []string
	NewIntentIDs []string
	NewOkIDs     []string
	NewOlIDs     []string
}

// IsEmpty reports whether the delta added nothing — useful for
// rendering "本轮无新上下文" hint.
func (d Delta) IsEmpty() bool {
	return len(d.NewOdIDs) == 0 && len(d.NewIntentIDs) == 0 &&
		len(d.NewOkIDs) == 0 && len(d.NewOlIDs) == 0
}

// --- internal helpers -------------------------------------------------

func loadMethodStrength(m string) int {
	switch m {
	case "lookup":
		return 4
	case "recall-hit":
		return 3
	case "recall-fallback":
		return 2
	case "legacy-migrated":
		return 1
	}
	return 0
}

func appendUniqueStr(xs []string, s string) []string {
	if s == "" {
		return xs
	}
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

func appendUniquePropRef(xs []LedgerPropRef, r LedgerPropRef) []LedgerPropRef {
	if r.PropID == "" {
		return xs
	}
	for _, x := range xs {
		if x.PropID == r.PropID {
			return xs
		}
	}
	return append(xs, r)
}

func mergeStrings(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, s := range b {
		out = appendUniqueStr(out, s)
	}
	return out
}

// mergePropertyMatches unions two PropertyMatch slices by PropertyID.
// Keywords lists are merged (deduped by Keyword+MatchedToken).
func mergePropertyMatches(a, b []recall.PropertyMatch) []recall.PropertyMatch {
	by := map[string]*recall.PropertyMatch{}
	order := []string{}
	for i := range a {
		cp := a[i]
		by[cp.PropertyID] = &cp
		order = append(order, cp.PropertyID)
	}
	for i := range b {
		if existing, ok := by[b[i].PropertyID]; ok {
			existing.Keywords = mergeKeywordHits(existing.Keywords, b[i].Keywords)
			// Preserve ok fields if incoming has richer info.
			if existing.OkTitle == "" && b[i].OkTitle != "" {
				existing.OkID = b[i].OkID
				existing.OkTitle = b[i].OkTitle
				existing.OkSummary = b[i].OkSummary
				existing.OkDefs = b[i].OkDefs
			}
			continue
		}
		cp := b[i]
		by[cp.PropertyID] = &cp
		order = append(order, cp.PropertyID)
	}
	out := make([]recall.PropertyMatch, 0, len(order))
	for _, id := range order {
		out = append(out, *by[id])
	}
	return out
}

func mergeKeywordHits(a, b []recall.KeywordHit) []recall.KeywordHit {
	seen := map[string]bool{}
	out := make([]recall.KeywordHit, 0, len(a)+len(b))
	key := func(k recall.KeywordHit) string { return k.Keyword + "|" + k.MatchedToken + "|" + k.Tier }
	for _, k := range a {
		if !seen[key(k)] {
			seen[key(k)] = true
			out = append(out, k)
		}
	}
	for _, k := range b {
		if !seen[key(k)] {
			seen[key(k)] = true
			out = append(out, k)
		}
	}
	return out
}

// mergeLinks unions two OdLink slices by target name.
func mergeLinks(a, b []recall.OdLink) []recall.OdLink {
	seen := map[string]bool{}
	out := make([]recall.OdLink, 0, len(a)+len(b))
	for _, x := range a {
		k := strings.ToLower(x.TargetOdName)
		if !seen[k] {
			seen[k] = true
			out = append(out, x)
		}
	}
	for _, x := range b {
		k := strings.ToLower(x.TargetOdName)
		if !seen[k] {
			seen[k] = true
			out = append(out, x)
		}
	}
	return out
}
