package builder_ledger

import (
	"fmt"
	"sort"
	"strings"
)

// FormatPrefix returns a Markdown block summarising the builder ledger for
// prepending to the current user message. The block gives the LLM a compact,
// structured view of what it already knows so it doesn't need to re-issue
// expensive tool calls for information it gathered on previous turns.
//
// Layout:
//
//	## 📚 本会话已知信息
//	### 已勘探的湖仓表 (N)
//	  - table1 ...
//	### 已分析的关系 (N)
//	  - table1 ↔ table2 ...
//	### 已搜索的关键词 (N)
//	  - ...
//	### 本会话已提议的草稿 (N)
//	  - ...
//	### 项目现有本体 (snapshot @ TN)
//	  - ...
//	---
//	(原用户问题在下面)
//
// Empty sections emit a single "[空]" line rather than the section header,
// to keep the block minimal when the session is just starting.
// Each subsection is capped at 10 entries to bound context size.
func (l *BuilderLedger) FormatPrefix() string {
	if l == nil || l.IsEmpty() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## 📚 本会话已知信息\n\n")

	// ── Tables explored ──
	exploredCount := len(l.TablesExplored)
	if exploredCount > 0 {
		// Sort by exploredInTurn DESC for most-recent-first.
		type tableEntry struct {
			key  string
			val  *TableExplored
		}
		entries := make([]tableEntry, 0, exploredCount)
		for k, v := range l.TablesExplored {
			entries = append(entries, tableEntry{k, v})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].val.ExploredInTurn != entries[j].val.ExploredInTurn {
				return entries[i].val.ExploredInTurn > entries[j].val.ExploredInTurn
			}
			return entries[i].key < entries[j].key
		})
		cap := exploredCount
		if cap > 10 {
			cap = 10
		}

		sb.WriteString(fmt.Sprintf("### 已勘探的湖仓表 (%d)\n\n", exploredCount))
		for _, e := range entries[:cap] {
			t := e.val
			sb.WriteString(fmt.Sprintf("- **%s** (%s 行 · %d 列)", t.Table, formatCount(t.RowCount), t.ColumnCount))
			if t.TruncatedColumns {
				sb.WriteString(" [列超30个，已截断]")
			}
			sb.WriteString("\n")

			// Key columns.
			pkCols := filterKeyColumns(t.KeyColumns, func(kc KeyColumn) bool { return kc.IsLikelyPK })
			fkCols := filterKeyColumns(t.KeyColumns, func(kc KeyColumn) bool { return kc.IsLikelyFK })
			tsCols := filterKeyColumns(t.KeyColumns, func(kc KeyColumn) bool { return kc.IsLikelyTS })
			if len(pkCols) > 0 {
				sb.WriteString(fmt.Sprintf("  - 主键: %s\n", joinKeyColNames(pkCols)))
			}
			if len(fkCols) > 0 {
				sb.WriteString(fmt.Sprintf("  - 外键候选: %s\n", joinKeyColNames(fkCols)))
			}
			if len(tsCols) > 0 {
				sb.WriteString(fmt.Sprintf("  - 时间列: %s\n", joinKeyColNames(tsCols)))
			}

			// Low-cardinality enums (top 3 per column).
			if len(t.LowCardinalityCols) > 0 {
				enumParts := make([]string, 0, len(t.LowCardinalityCols))
				for _, ce := range t.LowCardinalityCols {
					if len(ce.ValueDistribution) == 0 {
						continue
					}
					vals := ce.ValueDistribution
					if len(vals) > 3 {
						vals = vals[:3]
					}
					parts := make([]string, 0, len(vals))
					for _, vc := range vals {
						parts = append(parts, fmt.Sprintf("%s %.1f%%", vc.Value, vc.Pct))
					}
					enumParts = append(enumParts, fmt.Sprintf("%s [%s]", ce.Name, strings.Join(parts, ", ")))
				}
				if len(enumParts) > 0 {
					sb.WriteString(fmt.Sprintf("  - 枚举列: %s\n", strings.Join(enumParts, "; ")))
				}
			}

			// Hypotheses (up to 3).
			hyps := t.Hypotheses
			if len(hyps) > 3 {
				hyps = hyps[:3]
			}
			for _, h := range hyps {
				sb.WriteString(fmt.Sprintf("  - 假设: %s\n", truncStr(h, 80)))
			}
		}
		if exploredCount > 10 {
			sb.WriteString(fmt.Sprintf("  _(还有 %d 张表已勘探，未显示)_\n", exploredCount-10))
		}
		sb.WriteString("\n")
	}

	// ── Relationships analyzed ──
	relCount := len(l.RelationshipsAnalyzed)
	if relCount > 0 {
		type relEntry struct {
			key string
			val *RelationshipAnalyzed
		}
		rels := make([]relEntry, 0, relCount)
		for k, v := range l.RelationshipsAnalyzed {
			rels = append(rels, relEntry{k, v})
		}
		sort.Slice(rels, func(i, j int) bool {
			if rels[i].val.AnalyzedInTurn != rels[j].val.AnalyzedInTurn {
				return rels[i].val.AnalyzedInTurn > rels[j].val.AnalyzedInTurn
			}
			return rels[i].key < rels[j].key
		})
		capR := relCount
		if capR > 10 {
			capR = 10
		}

		sb.WriteString(fmt.Sprintf("### 已分析的关系 (%d)\n\n", relCount))
		for _, re := range rels[:capR] {
			ra := re.val
			tableLabel := strings.Join(ra.Tables, " ↔ ")
			sb.WriteString(fmt.Sprintf("- %s (%d 个候选)\n", tableLabel, ra.TotalCandidates))
			top := ra.TopCandidates
			if len(top) > 3 {
				top = top[:3]
			}
			for _, c := range top {
				sb.WriteString(fmt.Sprintf("  - 顶: %s.%s ↔ %s.%s (置信度 %.2f, 值重叠 %.2f, %s)\n",
					c.FromTable, c.FromColumn, c.ToTable, c.ToColumn,
					c.Confidence, c.ValueOverlap, c.Cardinality))
			}
		}
		sb.WriteString("\n")
	}

	// ── Search keywords ──
	kwCount := len(l.SearchKeywords)
	if kwCount > 0 {
		type kwEntry struct {
			key string
			val *SearchKeyword
		}
		kws := make([]kwEntry, 0, kwCount)
		for k, v := range l.SearchKeywords {
			kws = append(kws, kwEntry{k, v})
		}
		sort.Slice(kws, func(i, j int) bool {
			if kws[i].val.SearchedInTurn != kws[j].val.SearchedInTurn {
				return kws[i].val.SearchedInTurn > kws[j].val.SearchedInTurn
			}
			return kws[i].key < kws[j].key
		})
		capK := kwCount
		if capK > 10 {
			capK = 10
		}

		sb.WriteString(fmt.Sprintf("### 已搜索的关键词 (%d)\n\n", kwCount))
		for _, ke := range kws[:capK] {
			sk := ke.val
			colParts := make([]string, 0, len(sk.Matches))
			for _, m := range sk.Matches {
				colParts = append(colParts, fmt.Sprintf("%s (%d 次)", m.Column, m.TotalOccurrences))
			}
			if len(colParts) == 0 {
				sb.WriteString(fmt.Sprintf("- \"%s\" 在 %s 未命中\n", sk.Keyword, sk.InTable))
			} else {
				sb.WriteString(fmt.Sprintf("- \"%s\" 在 %s 命中: %s\n",
					sk.Keyword, sk.InTable, strings.Join(colParts, ", ")))
			}
		}
		sb.WriteString("\n")
	}

	// ── Drafts proposed ──
	allDrafts := make([]*DraftProposed, 0, len(l.DraftsProposed))
	for _, d := range l.DraftsProposed {
		allDrafts = append(allDrafts, d)
	}
	sort.Slice(allDrafts, func(i, j int) bool {
		return allDrafts[i].ProposedInTurn < allDrafts[j].ProposedInTurn
	})
	if len(allDrafts) > 0 {
		capD := len(allDrafts)
		if capD > 10 {
			capD = 10
		}
		sb.WriteString(fmt.Sprintf("### 本会话已提议的草稿 (%d)\n\n", len(allDrafts)))
		for _, d := range allDrafts[:capD] {
			icon := "✏️"
			switch d.Status {
			case "activated":
				icon = "✅"
			case "deleted":
				icon = "🗑️"
			}
			sb.WriteString(fmt.Sprintf("- %s %s (%s/%s) [%s] · %s\n",
				icon, d.Name, d.Type, d.Kind, d.Status,
				truncStr(d.Summary, 80)))
		}
		if len(allDrafts) > 10 {
			sb.WriteString(fmt.Sprintf("  _(还有 %d 个草稿未显示)_\n", len(allDrafts)-10))
		}
		sb.WriteString("\n")
	}

	// ── Ontology snapshot ──
	if l.OntologySnapshot != nil {
		snap := l.OntologySnapshot
		activeOds := filterOdsByMark(snap.Ods, true)
		activeIntents := filterIntentsByMark(snap.Intents, true)
		activeLinks := filterLinksByMark(snap.Links, true)

		sb.WriteString(fmt.Sprintf("### 项目现有本体 (snapshot @ T%d)\n\n", snap.SnapshottedInTurn))

		if len(activeOds) > 0 {
			names := make([]string, 0, len(activeOds))
			for _, od := range activeOds {
				names = append(names, od.Name)
			}
			if len(names) > 12 {
				names = names[:12]
			}
			sb.WriteString(fmt.Sprintf("- %d 个 active OD: %s\n", len(activeOds), strings.Join(names, ", ")))
		} else {
			sb.WriteString("- OD: 暂无 active\n")
		}

		if len(activeIntents) > 0 {
			names := make([]string, 0, len(activeIntents))
			for _, i := range activeIntents {
				names = append(names, i.Name)
			}
			if len(names) > 8 {
				names = names[:8]
			}
			sb.WriteString(fmt.Sprintf("- %d 个 active Intent: %s\n", len(activeIntents), strings.Join(names, ", ")))
		}

		if len(activeLinks) > 0 {
			parts := make([]string, 0, len(activeLinks))
			for _, lk := range activeLinks {
				parts = append(parts, lk.FromOdName+"→"+lk.ToOdName)
			}
			if len(parts) > 8 {
				parts = parts[:8]
			}
			sb.WriteString(fmt.Sprintf("- %d 个 active Link: %s\n", len(activeLinks), strings.Join(parts, ", ")))
		}
		sb.WriteString("\n")
	}

	// Also surface cached table list summary if available.
	if l.LakehouseTables != nil && len(l.LakehouseTables.Tables) > 0 {
		total := len(l.LakehouseTables.Tables)
		names := make([]string, 0, total)
		for _, t := range l.LakehouseTables.Tables {
			names = append(names, t.Name)
		}
		if len(names) > 10 {
			names = names[:10]
		}
		unexplored := 0
		for _, t := range l.LakehouseTables.Tables {
			if _, done := l.TablesExplored[t.Name]; !done {
				unexplored++
			}
		}
		sb.WriteString(fmt.Sprintf("### 湖仓表总览 (%d 张表, %d 张未勘探)\n\n", total, unexplored))
		sb.WriteString(fmt.Sprintf("- 已知: %s", strings.Join(names, ", ")))
		if total > 10 {
			sb.WriteString(fmt.Sprintf(" ...共 %d 张", total))
		}
		sb.WriteString("\n\n")
	}

	sb.WriteString("---\n\n")
	return sb.String()
}

// FormatTooLargeWarning returns a brief warning to prepend when the ledger
// is unusually large (> 30 KB), suggesting the LLM should not re-request
// already-cached data.
func (l *BuilderLedger) FormatTooLargeWarning(approxBytes int) string {
	return fmt.Sprintf(
		"_[builder_ledger: 会话记忆约 %d KB，已达上限。请优先使用上方摘要，避免重复调用大型探查工具。]_\n\n",
		approxBytes/1024,
	)
}

// ── internal helpers ──────────────────────────────────────────────────────────

func formatCount(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func filterKeyColumns(cols []KeyColumn, pred func(KeyColumn) bool) []KeyColumn {
	out := cols[:0:0]
	for _, c := range cols {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

func joinKeyColNames(cols []KeyColumn) string {
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

func filterOdsByMark(ods []OdSummary, mark bool) []OdSummary {
	out := ods[:0:0]
	for _, od := range ods {
		if od.Mark == mark {
			out = append(out, od)
		}
	}
	return out
}

func filterIntentsByMark(intents []IntentSummary, mark bool) []IntentSummary {
	out := intents[:0:0]
	for _, i := range intents {
		if i.Mark == mark {
			out = append(out, i)
		}
	}
	return out
}

func filterLinksByMark(links []LinkSummary, mark bool) []LinkSummary {
	out := links[:0:0]
	for _, lk := range links {
		if lk.Mark == mark {
			out = append(out, lk)
		}
	}
	return out
}
