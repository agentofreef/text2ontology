package builder_ledger

import (
	"fmt"
	"sort"
	"strings"
)

// M is a convenience alias matching the handler package convention.
type M = map[string]interface{}

// ── helpers ──────────────────────────────────────────────────────────────────

func strVal(m M, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func int64Val(m M, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

func intVal(m M, key string) int {
	return int(int64Val(m, key))
}

func boolVal(m M, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func float64Val(m M, key string) float64 {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// strSlice extracts a string slice under key. Like mSlice, handles both the
// fresh-from-tool shape ([]string) and the post-JSON-roundtrip shape
// ([]interface{}-of-strings).
func strSlice(m M, key string) []string {
	if m == nil {
		return nil
	}
	switch raw := m[key].(type) {
	case []string:
		return raw
	case []interface{}:
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// mSlice extracts a slice-of-map under key. Tool functions return values as []M
// directly; after a JSON unmarshal (ledger reload) the same data shows up as
// []interface{} containing map[string]interface{}. We handle all three shapes
// because the merge functions are called from BOTH paths.
func mSlice(m M, key string) []M {
	if m == nil {
		return nil
	}
	switch raw := m[key].(type) {
	case []M:
		// Tool functions return this directly. M is map[string]interface{},
		// so []map[string]interface{} also matches this case.
		return raw
	case []interface{}:
		out := make([]M, 0, len(raw))
		for _, v := range raw {
			if mv, ok := v.(M); ok {
				out = append(out, mv)
			} else if mv, ok := v.(map[string]interface{}); ok {
				out = append(out, M(mv))
			}
		}
		return out
	default:
		return nil
	}
}

func truncStr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// sortedTableKey produces the canonical key for a RelationshipAnalyzed entry:
// table names sorted and joined by "|".
func sortedTableKey(tables []string) string {
	cp := append([]string(nil), tables...)
	sort.Strings(cp)
	return strings.Join(cp, "|")
}

// ── MergeListLakehouseTables ─────────────────────────────────────────────────

// MergeListLakehouseTables stores the table listing result. Only written once
// per session; subsequent calls are no-ops unless the caller explicitly passes
// force=true by calling MergeListLakehouseTablesForce.
func (l *BuilderLedger) MergeListLakehouseTables(result M, turn int) {
	if l.LakehouseTables != nil {
		// Already cached — do not overwrite (list_lakehouse_tables result
		// doesn't change within a session unless tables are added, which
		// doesn't happen in builder mode).
		return
	}
	l.mergeListLakehouseTablesInner(result, turn)
}

// MergeListLakehouseTablesForce refreshes the cache unconditionally. Called
// when the LLM explicitly re-fetches the table list.
func (l *BuilderLedger) MergeListLakehouseTablesForce(result M, turn int) {
	l.mergeListLakehouseTablesInner(result, turn)
}

func (l *BuilderLedger) mergeListLakehouseTablesInner(result M, turn int) {
	rawTables := mSlice(result, "tables")
	if rawTables == nil {
		// Try "data" key (some list variants wrap results).
		rawTables = mSlice(result, "data")
	}
	summaries := make([]TableSummary, 0, len(rawTables))
	for _, t := range rawTables {
		name := strVal(t, "name")
		if name == "" {
			name = strVal(t, "tableName")
		}
		if name == "" {
			continue
		}
		summaries = append(summaries, TableSummary{
			Name:          name,
			Type:          strVal(t, "type"),
			EstimatedRows: int64Val(t, "estimatedRows"),
		})
	}
	l.LakehouseTables = &TablesIndex{
		Tables:       summaries,
		LoadedInTurn: turn,
	}
}

// ── MergeAnalyzeTable ────────────────────────────────────────────────────────

// MergeAnalyzeTable folds a builderToolAnalyzeTable result into TablesExplored.
// Idempotent: re-calling with the same tableName replaces (refreshes) the entry.
func (l *BuilderLedger) MergeAnalyzeTable(args M, result M, turn int) {
	tableName := strVal(args, "tableName")
	if tableName == "" {
		tableName = strVal(result, "table")
	}
	if tableName == "" {
		return
	}
	// Check for error result — don't cache failures.
	if strVal(result, "error") != "" {
		return
	}

	entry := &TableExplored{
		Table:            tableName,
		RowCount:         int64Val(result, "rowCount"),
		ColumnCount:      intVal(result, "totalColumnCount"),
		TruncatedColumns: boolVal(result, "truncatedColumns"),
		ExploredInTurn:   turn,
	}
	if entry.ColumnCount == 0 {
		// Fallback: count columns slice length.
		if cols := mSlice(result, "columns"); cols != nil {
			entry.ColumnCount = len(cols)
		}
	}

	// Hypotheses — verbatim strings from the result.
	entry.Hypotheses = strSlice(result, "hypotheses")
	if entry.Hypotheses == nil {
		entry.Hypotheses = []string{}
	}

	// Key columns and low-cardinality enums — extracted from columns array.
	entry.KeyColumns = []KeyColumn{}
	entry.LowCardinalityCols = []ColumnEnum{}

	cols := mSlice(result, "columns")
	for _, col := range cols {
		name := strVal(col, "name")
		if name == "" {
			continue
		}
		cardinality := int64Val(col, "cardinality")
		uniqueRatio := float64Val(col, "uniqueRatio")
		isPK := boolVal(col, "isLikelyPrimaryKey")
		isFK := boolVal(col, "isLikelyForeignKey")
		isMC := boolVal(col, "isLikelyMachineCode")
		isTS := boolVal(col, "isLikelyTimestamp")

		if isPK || isFK || isMC || isTS {
			entry.KeyColumns = append(entry.KeyColumns, KeyColumn{
				Name:        name,
				DataType:    strVal(col, "dataType"),
				Cardinality: cardinality,
				UniqueRatio: uniqueRatio,
				IsLikelyPK:  isPK,
				IsLikelyFK:  isFK,
				IsLikelyMC:  isMC,
				IsLikelyTS:  isTS,
			})
		}

		// Low-cardinality value distribution (cardinality 1..30).
		if cardinality > 0 && cardinality <= 30 {
			dist := mSlice(col, "valueDistribution")
			if dist != nil {
				vals := make([]ValueCount, 0, len(dist))
				for _, d := range dist {
					v := fmt.Sprintf("%v", d["value"])
					vals = append(vals, ValueCount{
						Value: v,
						Count: int64Val(d, "count"),
						Pct:   float64Val(d, "pct"),
					})
				}
				entry.LowCardinalityCols = append(entry.LowCardinalityCols, ColumnEnum{
					Name:              name,
					Cardinality:       int(cardinality),
					ValueDistribution: vals,
				})
			}
		}
	}

	l.TablesExplored[tableName] = entry
}

// ── MergeAnalyzeRelationships ────────────────────────────────────────────────

// MergeAnalyzeRelationships folds a builderToolAnalyzeRelationships result
// into RelationshipsAnalyzed. Key is sorted(tables) joined by "|".
// Re-calling refreshes the entry (the probe re-runs when the LLM asks again).
func (l *BuilderLedger) MergeAnalyzeRelationships(args M, result M, turn int) {
	if strVal(result, "error") != "" {
		return
	}

	// Recover table list from result (args may also carry it).
	var tables []string
	if rt := strSlice(result, "tables"); rt != nil {
		tables = rt
	} else if at := strSlice(args, "tables"); at != nil {
		tables = at
	}
	if len(tables) < 2 {
		return
	}

	key := sortedTableKey(tables)

	// Merge high-confidence + uncertain candidates into a unified slice,
	// then keep top 5 by confidence.
	allCandidates := append(mSlice(result, "candidates"), mSlice(result, "uncertain")...)
	sort.Slice(allCandidates, func(i, j int) bool {
		return float64Val(allCandidates[i], "confidence") > float64Val(allCandidates[j], "confidence")
	})
	if len(allCandidates) > 5 {
		allCandidates = allCandidates[:5]
	}

	top := make([]RelationshipCandidate, 0, len(allCandidates))
	for _, c := range allCandidates {
		ev, _ := c["evidence"].(M)
		overlap := float64Val(ev, "valueOverlap")
		nameSim := float64Val(ev, "nameSimilarity")
		card := strVal(ev, "cardinalityHint")
		if card == "" {
			card = strVal(c, "suggestedCardinality")
		}
		top = append(top, RelationshipCandidate{
			FromTable:    strVal(c, "fromTable"),
			FromColumn:   strVal(c, "fromColumn"),
			ToTable:      strVal(c, "toTable"),
			ToColumn:     strVal(c, "toColumn"),
			Confidence:   float64Val(c, "confidence"),
			ValueOverlap: overlap,
			NameSim:      nameSim,
			Cardinality:  card,
		})
	}

	total := int(int64Val(result, "totalPairsExamined"))

	l.RelationshipsAnalyzed[key] = &RelationshipAnalyzed{
		Tables:          tables,
		TopCandidates:   top,
		AnalyzedInTurn:  turn,
		TotalCandidates: total,
	}
}

// ── MergeQueryData ───────────────────────────────────────────────────────────

// MergeQueryData only caches keyword_search results (mode="keyword_search").
// SQL mode results are one-off and not worth caching per the design spec.
func (l *BuilderLedger) MergeQueryData(args M, result M, turn int) {
	mode := strVal(result, "mode")
	if mode != "keyword_search" {
		return
	}
	if strVal(result, "error") != "" {
		return
	}

	keyword := strVal(result, "keyword")
	if keyword == "" {
		keyword = strVal(args, "searchKeyword")
	}
	inTable := strVal(result, "table")
	if inTable == "" {
		inTable = strVal(args, "inTable")
	}
	if keyword == "" || inTable == "" {
		return
	}

	key := keyword + ":" + inTable
	matchRows := mSlice(result, "matches")
	cols := make([]SearchMatchedCol, 0, len(matchRows))
	for _, m := range matchRows {
		colName := strVal(m, "column")
		if colName == "" {
			continue
		}
		sampleCount := 0
		if sv := mSlice(m, "sampleValues"); sv != nil {
			sampleCount = len(sv)
		}
		cols = append(cols, SearchMatchedCol{
			Column:           colName,
			TotalOccurrences: int64Val(m, "totalOccurrences"),
			SampleValueCount: sampleCount,
		})
	}

	l.SearchKeywords[key] = &SearchKeyword{
		Keyword:        keyword,
		InTable:        inTable,
		Matches:        cols,
		SearchedInTurn: turn,
	}
}

// ── MergeListOds ─────────────────────────────────────────────────────────────

// MergeListOds refreshes OntologySnapshot.Ods from a list_ods result.
func (l *BuilderLedger) MergeListOds(result M, turn int) {
	if strVal(result, "error") != "" {
		return
	}
	ods := mSlice(result, "ods")
	if l.OntologySnapshot == nil {
		l.OntologySnapshot = &OntologySnapshot{SnapshottedInTurn: turn}
	}
	summaries := make([]OdSummary, 0, len(ods))
	for _, od := range ods {
		props := mSlice(od, "properties")
		summaries = append(summaries, OdSummary{
			ID:          strVal(od, "id"),
			Name:        strVal(od, "name"),
			Kind:        strVal(od, "kind"),
			PropCount:   len(props),
			SourceTable: strVal(od, "sourceTable"),
			Mark:        boolVal(od, "mark"),
		})
	}
	l.OntologySnapshot.Ods = summaries
	l.OntologySnapshot.SnapshottedInTurn = turn
}

// MergeListIntents refreshes OntologySnapshot.Intents from a list_intents result.
func (l *BuilderLedger) MergeListIntents(result M, turn int) {
	if strVal(result, "error") != "" {
		return
	}
	intents := mSlice(result, "intents")
	if l.OntologySnapshot == nil {
		l.OntologySnapshot = &OntologySnapshot{SnapshottedInTurn: turn}
	}
	summaries := make([]IntentSummary, 0, len(intents))
	for _, i := range intents {
		summaries = append(summaries, IntentSummary{
			ID:              strVal(i, "id"),
			Name:            strVal(i, "name"),
			ObjectName:      strVal(i, "objectName"),
			CanonicalMetric: strVal(i, "canonicalMetric"),
			Mark:            boolVal(i, "mark"),
		})
	}
	l.OntologySnapshot.Intents = summaries
	l.OntologySnapshot.SnapshottedInTurn = turn
}

// MergeListLinks refreshes OntologySnapshot.Links from a list_links result.
func (l *BuilderLedger) MergeListLinks(result M, turn int) {
	if strVal(result, "error") != "" {
		return
	}
	links := mSlice(result, "links")
	if l.OntologySnapshot == nil {
		l.OntologySnapshot = &OntologySnapshot{SnapshottedInTurn: turn}
	}
	summaries := make([]LinkSummary, 0, len(links))
	for _, lk := range links {
		summaries = append(summaries, LinkSummary{
			ID:         strVal(lk, "id"),
			FromOdName: strVal(lk, "fromObjectName"),
			ToOdName:   strVal(lk, "toObjectName"),
			FkColumn:   strVal(lk, "fkColumn"),
			Mark:       boolVal(lk, "mark"),
		})
	}
	l.OntologySnapshot.Links = summaries
	l.OntologySnapshot.SnapshottedInTurn = turn
}

// ── MergePropose ─────────────────────────────────────────────────────────────

// MergePropose records a newly proposed draft entity.
// toolName ∈ {"propose_od", "propose_intent", "propose_link"}.
// Idempotent: re-proposing the same UUID is a no-op (first-write wins).
func (l *BuilderLedger) MergePropose(toolName string, args M, result M, turn int) {
	if strVal(result, "error") != "" {
		return
	}

	var id, name, draftType, kind, summary, semanticSQL string
	var linkedOdName, canonicalMetric, fromOdName, toOdName string

	switch toolName {
	case "propose_od":
		id = strVal(result, "objectId")
		name = strVal(result, "name")
		draftType = "od"
		kind = strVal(result, "kind")
		semanticSQL = truncStr(strVal(result, "semanticSql"), 100)
		propCount := 0
		if props := mSlice(result, "properties"); props != nil {
			propCount = len(props)
		}
		summary = fmt.Sprintf("%s (%s, %d props)", name, kind, propCount)

	case "propose_intent":
		id = strVal(result, "intentId")
		name = strVal(result, "name")
		draftType = "intent"
		canonicalMetric = strVal(result, "canonicalMetric")
		// objectName not directly in result — carry from args if available.
		linkedOdName = strVal(args, "objectId") // UUID; render.go will show it as-is
		summary = fmt.Sprintf("%s (intent, %s)", name, canonicalMetric)

	case "propose_link":
		id = strVal(result, "linkId")
		name = strVal(result, "linkName")
		draftType = "link"
		fromOdName = strVal(result, "fromObjectId") // UUID placeholder
		toOdName = strVal(result, "toObjectId")
		fkCol := strVal(result, "fkColumn")
		summary = fmt.Sprintf("%s→%s via %s", fromOdName, toOdName, fkCol)

	default:
		return
	}

	if id == "" {
		return
	}

	// Idempotent: if already present, do not overwrite (preserve proposedInTurn).
	if _, exists := l.DraftsProposed[id]; exists {
		return
	}

	l.DraftsProposed[id] = &DraftProposed{
		ID:                id,
		Type:              draftType,
		Name:              name,
		Status:            "pending",
		Kind:              kind,
		SemanticSqlPreview: semanticSQL,
		Summary:           summary,
		ProposedInTurn:    turn,
		LastUpdatedInTurn: turn,
		LinkedOdName:      linkedOdName,
		CanonicalMetric:   canonicalMetric,
		FromOdName:        fromOdName,
		ToOdName:          toOdName,
	}
}

// ── MergeUpdate ──────────────────────────────────────────────────────────────

// MergeUpdate applies changes to an existing draft in DraftsProposed.
// toolName ∈ {"update_od", "update_intent", "update_link"}.
func (l *BuilderLedger) MergeUpdate(toolName string, args M, result M, turn int) {
	if strVal(result, "error") != "" {
		return
	}

	var id string
	switch toolName {
	case "update_od":
		id = strVal(result, "objectId")
		if id == "" {
			id = strVal(args, "objectId")
		}
	case "update_intent":
		id = strVal(result, "intentId")
		if id == "" {
			id = strVal(args, "intentId")
		}
	case "update_link":
		id = strVal(result, "linkId")
		if id == "" {
			id = strVal(args, "linkId")
		}
	}

	if id == "" {
		return
	}

	draft, exists := l.DraftsProposed[id]
	if !exists {
		// Not in this thread's ledger — could be a pre-existing entity.
		return
	}
	draft.LastUpdatedInTurn = turn

	// Refresh name if changed (edits["name"] path).
	if edits, ok := args["edits"].(M); ok {
		if n := strVal(edits, "name"); n != "" {
			draft.Name = n
		}
		if cm := strVal(edits, "canonicalMetric"); cm != "" {
			draft.CanonicalMetric = cm
		}
		if sq := strVal(edits, "semanticSql"); sq != "" {
			draft.SemanticSqlPreview = truncStr(sq, 100)
		}
	}

	// Refresh summary to reflect latest state.
	switch draft.Type {
	case "od":
		draft.Summary = fmt.Sprintf("%s (%s) [updated T%d]", draft.Name, draft.Kind, turn)
	case "intent":
		draft.Summary = fmt.Sprintf("%s (intent, %s) [updated T%d]", draft.Name, draft.CanonicalMetric, turn)
	case "link":
		draft.Summary = fmt.Sprintf("%s→%s [updated T%d]", draft.FromOdName, draft.ToOdName, turn)
	}
}

// ── MergeDelete ──────────────────────────────────────────────────────────────

// MergeDelete marks a draft as deleted. Does NOT remove from the map so the
// LLM still sees it as a deleted entry in FormatPrefix.
// toolName ∈ {"delete_od", "delete_intent", "delete_link"}.
func (l *BuilderLedger) MergeDelete(toolName string, args M, result M, turn int) {
	if strVal(result, "error") != "" {
		return
	}

	var id string
	switch toolName {
	case "delete_od":
		id = strVal(result, "objectId")
		if id == "" {
			id = strVal(args, "objectId")
		}
	case "delete_intent":
		id = strVal(result, "intentId")
		if id == "" {
			id = strVal(args, "intentId")
		}
	case "delete_link":
		id = strVal(result, "linkId")
		if id == "" {
			id = strVal(args, "linkId")
		}
	}

	if id == "" {
		return
	}

	if draft, exists := l.DraftsProposed[id]; exists {
		draft.Status = "deleted"
		draft.LastUpdatedInTurn = turn
	}
	// Also clean up OntologySnapshot if it referenced the deleted entity
	// by scanning and removing matching IDs.
	if l.OntologySnapshot != nil {
		switch toolName {
		case "delete_od":
			ods := l.OntologySnapshot.Ods[:0]
			for _, od := range l.OntologySnapshot.Ods {
				if od.ID != id {
					ods = append(ods, od)
				}
			}
			l.OntologySnapshot.Ods = ods
		case "delete_intent":
			intents := l.OntologySnapshot.Intents[:0]
			for _, i := range l.OntologySnapshot.Intents {
				if i.ID != id {
					intents = append(intents, i)
				}
			}
			l.OntologySnapshot.Intents = intents
		case "delete_link":
			links := l.OntologySnapshot.Links[:0]
			for _, lk := range l.OntologySnapshot.Links {
				if lk.ID != id {
					links = append(links, lk)
				}
			}
			l.OntologySnapshot.Links = links
		}
	}
}

// ── MarkActivated ────────────────────────────────────────────────────────────

// MarkActivated flips a draft's status to "activated". Called after the
// frontend activation endpoint succeeds (or from any path that detects
// an entity has transitioned to mark=true in the ontology).
func (l *BuilderLedger) MarkActivated(id string, turn int) {
	if draft, exists := l.DraftsProposed[id]; exists {
		draft.Status = "activated"
		draft.LastUpdatedInTurn = turn
	}
}
