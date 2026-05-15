package ledger

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// FormatContextWithLedger renders a context markdown that augments the
// standard recall.FormatContext with a thread-memory header and an
// orphan footer.
//
// Layout:
//
//	🧠 线程记忆 (前 N 轮) — 1 line summary: Od count / Intent count / token count
//	<recall.FormatContext output for THIS TURN's result>
//	📚 线程其它记忆 (未在本轮命中) — Ods/Intents from earlier turns that
//	                                this turn's tokens did NOT surface
//
// The orphan footer prevents the LLM from forgetting that, e.g., Order
// was loaded two turns ago even though this turn's question is about
// Customer. 1 line per entry keeps token cost low; if the LLM needs
// detail, it can call lookup and we'll serve a pointer (Phase 5).
//
// currentTurn is used purely for cosmetics ("前 N 轮已确立") and for
// filtering orphan vs. current-turn entries via odId membership in
// result.OdBlocks. The ledger's own loadedInTurn field is the source
// of truth.
func FormatContextWithLedger(result recall.RecallResult, tokens []string, question string,
	l *Ledger, currentTurn int,
) string {
	if l == nil || l.IsEmpty() {
		// No ledger yet — behave identically to legacy recall.
		return recall.FormatContext(result, tokens, question)
	}

	var sb strings.Builder

	// Header block — sets the scene for the LLM.
	sb.WriteString(renderDigestHeader(l, currentTurn))

	// Main body — current turn's recall result, full detail for all
	// Ods/Intents (including cached-spliced ones; the LLM benefits
	// from seeing their props in context of this turn's question).
	sb.WriteString(recall.FormatContext(result, tokens, question))

	// Orphan footer — ledger entries NOT referenced by this turn's
	// result, so the LLM sees them as still-available context. One
	// line per entry.
	orphanBlock := renderOrphanFooter(result, l)
	if orphanBlock != "" {
		sb.WriteString(orphanBlock)
	}

	return sb.String()
}

func renderDigestHeader(l *Ledger, currentTurn int) string {
	if l == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### 🧠 线程记忆\n\n")
	// Prior-turn count for framing — if this is turn 1 with a fresh
	// rebuild, "前 N 轮" might be misleading, so use "已确立上下文" as
	// the safer label.
	priorTurns := currentTurn - 1
	if priorTurns < 0 {
		priorTurns = 0
	}
	if l.RebuiltFromStep > 0 {
		sb.WriteString(fmt.Sprintf("（从历史 %d 步重建；当前第 %d 轮）已确立：", l.RebuiltFromStep, currentTurn))
	} else if priorTurns == 0 {
		sb.WriteString("（本线程首轮；ledger 为空）")
	} else {
		sb.WriteString(fmt.Sprintf("（前 %d 轮已确立）", priorTurns))
	}
	sb.WriteString(fmt.Sprintf(" Od=%d · Intent=%d · token=%d\n\n",
		len(l.Ods), len(l.Intents), len(l.Tokens)))
	return sb.String()
}

// renderOrphanFooter lists ledger Ods / Intents that are NOT already
// rendered in this turn's result. Returns "" if everything in the
// ledger is covered by the result (i.e. this turn touches all
// established context).
func renderOrphanFooter(result recall.RecallResult, l *Ledger) string {
	if l == nil {
		return ""
	}
	inResultOd := map[string]bool{}
	for _, b := range result.OdBlocks {
		inResultOd[b.OdID] = true
	}
	for _, b := range result.DirectOds {
		inResultOd[b.OdID] = true
	}
	inResultIntent := map[string]bool{}
	for _, mi := range result.MetricIntents {
		inResultIntent[mi.IntentID] = true
	}

	var orphanOds []*LedgerOd
	for id, od := range l.Ods {
		if !inResultOd[id] {
			orphanOds = append(orphanOds, od)
		}
	}
	var orphanIntents []*LedgerIntent
	for id, mi := range l.Intents {
		if !inResultIntent[id] {
			orphanIntents = append(orphanIntents, mi)
		}
	}
	if len(orphanOds) == 0 && len(orphanIntents) == 0 {
		return ""
	}

	// Stable ordering by LoadedInTurn DESC (most recent first) then name.
	sort.Slice(orphanOds, func(i, j int) bool {
		if orphanOds[i].LoadedInTurn != orphanOds[j].LoadedInTurn {
			return orphanOds[i].LoadedInTurn > orphanOds[j].LoadedInTurn
		}
		return orphanOds[i].Name < orphanOds[j].Name
	})
	sort.Slice(orphanIntents, func(i, j int) bool {
		if orphanIntents[i].FirstSeenInTurn != orphanIntents[j].FirstSeenInTurn {
			return orphanIntents[i].FirstSeenInTurn > orphanIntents[j].FirstSeenInTurn
		}
		return orphanIntents[i].Name < orphanIntents[j].Name
	})

	var sb strings.Builder
	sb.WriteString("### 📚 线程其它记忆（未在本轮命中）\n\n")
	sb.WriteString("以下本体在早先轮次已加载，但本轮问题未直接涉及。如需引用，可直接照抄名称（无需再次 lookup）：\n\n")
	if len(orphanOds) > 0 {
		sb.WriteString(fmt.Sprintf("orphan_ods[%d|]{od|kind|loadedTurn|matchedProps|desc}:\n", len(orphanOds)))
		for _, od := range orphanOds {
			desc := truncRunes(od.Description, 40)
			propCount := fmt.Sprintf("%d", len(od.MatchedProps))
			sb.WriteString(fmt.Sprintf("  %s|%s|T%d|%s|%s\n",
				safeTOON(od.Name), safeTOON(od.Kind),
				od.LoadedInTurn, propCount, safeTOON(desc)))
		}
		sb.WriteString("\n")
	}
	if len(orphanIntents) > 0 {
		sb.WriteString(fmt.Sprintf("orphan_intents[%d|]{intent|metric|tokens|loadedTurn}:\n", len(orphanIntents)))
		for _, mi := range orphanIntents {
			tokens := strings.Join(mi.MatchedTokens, ",")
			sb.WriteString(fmt.Sprintf("  %s|%s|%s|T%d\n",
				safeTOON(mi.Name), safeTOON(mi.CanonicalMetric),
				safeTOON(tokens), mi.FirstSeenInTurn))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// truncRunes trims whitespace and truncates by rune count.
func truncRunes(s string, maxRunes int) string {
	flat := strings.Join(strings.Fields(s), " ")
	r := []rune(flat)
	if len(r) <= maxRunes {
		return flat
	}
	return string(r[:maxRunes]) + "..."
}

// safeTOON escapes a value for a pipe-delimited TOON row — wraps in
// double-quotes if it contains special chars. Mirrors recall.toonVal.
func safeTOON(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "|\"\n\r:") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
