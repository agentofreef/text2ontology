package recall

import (
	"database/sql"
	"encoding/json"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
)

// resolveOd loads an ont_object_type by ID.
func resolveOd(db *sql.DB, objectTypeID string) *OdBlock {
	var blk OdBlock
	// Skip unmarked Ods (ontology-level disable toggle set from
	// /dax/ontology/lakehouse-objects). Treat NULL mark as enabled for
	// backward compatibility with older rows.
	err := db.QueryRow(`
		SELECT id::text, name, COALESCE(kind,''), COALESCE(description,'')
		FROM ont_object_type WHERE id = $1 AND COALESCE(mark, true) = true`, objectTypeID).
		Scan(&blk.OdID, &blk.Name, &blk.Kind, &blk.Description)
	if err != nil {
		return nil
	}
	return &blk
}

// resolvePropertyOk loads the Ok entry (anchor_type='property') and its positive definitions.
func resolvePropertyOk(db *sql.DB, pm *PropertyMatch) {
	err := db.QueryRow(`
		SELECT k.id::text, k.title, COALESCE(k.summary,'')
		FROM ont_knowledge k
		WHERE k.anchor_type = 'property' AND k.anchor_id = $1`,
		pm.PropertyID).Scan(&pm.OkID, &pm.OkTitle, &pm.OkSummary)
	if err != nil {
		return // no Ok for this property, that's fine
	}

	defRows, err := db.Query(`
		SELECT COALESCE(content,'') FROM ont_knowledge_definition
		WHERE knowledge_id = $1 AND def_type = 'positive'
		ORDER BY sort_order`, pm.OkID)
	if err != nil {
		return
	}
	defer defRows.Close()
	for defRows.Next() {
		var c string
		defRows.Scan(&c)
		if c != "" {
			pm.OkDefs = append(pm.OkDefs, c)
		}
	}
}

// loadAllPropNames returns all property display names and descriptions for an Od.
func loadAllPropNames(db *sql.DB, objectTypeID string) ([]string, map[string]string) {
	rows, err := db.Query(`
		SELECT COALESCE(display_name, name), COALESCE(description, '')
		FROM ont_property WHERE object_type_id = $1 ORDER BY name`, objectTypeID)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	var names []string
	descs := map[string]string{}
	for rows.Next() {
		var n, d string
		rows.Scan(&n, &d)
		names = append(names, n)
		if d != "" {
			descs[n] = d
		}
	}
	return names, descs
}

// okEntrySelect is the shared SELECT list for the two OK-recall queries below.
const okEntrySelect = `
	SELECT DISTINCT kk.knowledge_id::text, ok.title, COALESCE(ok.summary,''),
	       COALESCE(ok.anchor_type,''),
	       COALESCE(ok.entry_type,''),
	       COALESCE(ok.skill_config, '{}'::jsonb)::text
	FROM ont_knowledge_keyword kk
	JOIN ont_knowledge ok ON ok.id = kk.knowledge_id
	WHERE kk.project_id = $1
	  AND (LOWER(kk.keyword) = LOWER($2) OR kk.keyword ILIKE '%'||$2||'%')`

// fallbackOkEntries searches ont_knowledge_keyword for ordinary (non-property,
// non-analysis-pattern) Ok entries. The caller gates this on matchedTokens —
// ordinary OK knowledge is only useful for tokens with no stronger anchor.
func fallbackOkEntries(db *sql.DB, projectID, token string, result *RecallResult) {
	rows, err := db.Query(okEntrySelect+`
	  AND COALESCE(ok.anchor_type,'') NOT IN ('property','analysis_pattern')
		LIMIT 3`, projectID, token)
	if err != nil {
		return
	}
	defer rows.Close()
	appendOkRows(rows, token, result)
}

// fallbackAnalysisPatterns searches ont_knowledge_keyword specifically for
// analysis_pattern OK cards. UNLIKE fallbackOkEntries, the caller runs this
// UNCONDITIONALLY (not gated on matchedTokens): an analysis_pattern card is a
// callable skill (spec §0) and its trigger keyword must surface the skill even
// when the same token also resolved to a property / metric Intent — the LLM
// needs to *see* the skill block to decide whether to enter plan-mode (§3.2).
func fallbackAnalysisPatterns(db *sql.DB, projectID, token string, result *RecallResult) {
	rows, err := db.Query(okEntrySelect+`
	  AND COALESCE(ok.anchor_type,'') = 'analysis_pattern'
		LIMIT 3`, projectID, token)
	if err != nil {
		return
	}
	defer rows.Close()
	appendOkRows(rows, token, result)
}

// appendOkRows scans OK rows into result.OkEntries, deduplicating by id and
// merging the triggering token onto an entry already present.
func appendOkRows(rows *sql.Rows, token string, result *RecallResult) {
	seen := map[string]bool{}
	for _, e := range result.OkEntries {
		seen[e.ID] = true
	}
	for rows.Next() {
		var id, title, summary, anchorType, entryType, skillConfigStr string
		rows.Scan(&id, &title, &summary, &anchorType, &entryType, &skillConfigStr)
		if seen[id] {
			for i := range result.OkEntries {
				if result.OkEntries[i].ID == id {
					result.OkEntries[i].Tokens = append(result.OkEntries[i].Tokens, token)
					break
				}
			}
			continue
		}
		seen[id] = true
		entry := OkEntry{
			ID: id, Title: title, Summary: summary, Tokens: []string{token},
			EntryType: entryType, AnchorType: anchorType,
		}
		if skillConfigStr != "" && skillConfigStr != "{}" {
			entry.SkillConfig = json.RawMessage(skillConfigStr)
		}
		result.OkEntries = append(result.OkEntries, entry)
	}
}

// fallbackDirectOd matches a token against three Od identification channels:
//
//	1. ont_object_type.name          → MatchedVia="name"
//	2. ont_object_type.display_name  → MatchedVia="display_name"
//	3. ont_alias.alias_text          → MatchedVia="alias"   (target_kind='object_type', mark=true)
//
// Behaviour:
//   - Always called per token by BuildLakehouseContext (no "only when MISS"
//     guard) — a token that fully matched a property still gets to surface its
//     Od name match so the UI can badge it.
//   - If the Od is ALREADY in result.OdBlocks (added via property/od-alias-keyword
//     hit) → MatchedVia tags are merged in place and no DirectOd entry is created.
//   - Else if the Od is in result.DirectOds → tags merged in place.
//   - Else → new entry appended to result.DirectOds.
//
// EXACT (case-insensitive equality) is preferred; falls back to ILIKE substring.
// LIMIT 3 caps noise on common-substring tokens.
func fallbackDirectOd(db *sql.DB, projectID, token string, result *RecallResult) {
	rows, err := db.Query(`
		WITH od AS (
		    SELECT id::text AS od_id, name, COALESCE(display_name,'') AS display_name,
		           COALESCE(kind,'') AS kind, COALESCE(description,'') AS description
		      FROM ont_object_type
		     WHERE project_id = $1 AND COALESCE(mark, true) = true
		),
		hits AS (
		    -- channel: name
		    SELECT od.od_id, od.name, od.display_name, od.kind, od.description,
		           'name'::text AS via,
		           CASE WHEN LOWER(od.name) = LOWER($2) THEN 0 ELSE 1 END AS rank
		      FROM od
		     WHERE LOWER(od.name) = LOWER($2) OR od.name ILIKE '%'||$2||'%'
		    UNION ALL
		    -- channel: display_name
		    SELECT od.od_id, od.name, od.display_name, od.kind, od.description,
		           'display_name'::text AS via,
		           CASE WHEN LOWER(od.display_name) = LOWER($2) THEN 0 ELSE 1 END AS rank
		      FROM od
		     WHERE od.display_name <> ''
		       AND (LOWER(od.display_name) = LOWER($2) OR od.display_name ILIKE '%'||$2||'%')
		    UNION ALL
		    -- channel: ont_alias (target_kind='object_type')
		    SELECT od.od_id, od.name, od.display_name, od.kind, od.description,
		           'alias'::text AS via,
		           CASE WHEN LOWER(a.alias_text) = LOWER($2) THEN 0 ELSE 1 END AS rank
		      FROM ont_alias a
		      JOIN od ON od.od_id = a.target_id::text
		     WHERE a.project_id = $1
		       AND a.target_kind = 'object_type'
		       AND COALESCE(a.mark, true) = true
		       AND (LOWER(a.alias_text) = LOWER($2) OR a.alias_text ILIKE '%'||$2||'%')
		)
		SELECT od_id, name, kind, description, via, MIN(rank) AS best_rank
		  FROM hits
		 GROUP BY od_id, name, kind, description, via
		 ORDER BY best_rank, od_id, via
		 LIMIT 9`, projectID, token)
	if err != nil {
		return
	}
	defer rows.Close()

	// Aggregate: per Od, collect distinct via channels found this round.
	type odMatch struct {
		odID, name, kind, description string
		vias                          []string
	}
	matches := map[string]*odMatch{}
	order := []string{} // preserve insertion order so ranking by SQL is honoured

	for rows.Next() {
		var odID, name, kind, description, via string
		var rank int
		if err := rows.Scan(&odID, &name, &kind, &description, &via, &rank); err != nil {
			continue
		}
		m, ok := matches[odID]
		if !ok {
			m = &odMatch{odID: odID, name: name, kind: kind, description: description}
			matches[odID] = m
			order = append(order, odID)
		}
		m.vias = appendUnique(m.vias, via)
	}

	if len(matches) == 0 {
		return
	}

	// Helper: locate Od in OdBlocks/DirectOds for in-place MatchedVia merge.
	for _, odID := range order {
		m := matches[odID]
		if mergeMatchedViaInBlocks(result, odID, m.vias) {
			continue
		}
		// Not found anywhere — append to DirectOds.
		blk := OdBlock{
			OdID: m.odID, Name: m.name, Kind: m.kind, Description: m.description,
			MatchedVia: append([]string{}, m.vias...),
		}
		blk.AllPropNames, blk.AllPropDescs = loadAllPropNames(db, blk.OdID)
		result.DirectOds = append(result.DirectOds, blk)
	}
}

// mergeMatchedViaInBlocks finds the Od by ID in result.OdBlocks or
// result.DirectOds and appends the given via tags to its MatchedVia (deduped).
// Returns true when the Od was found and merged; false means the caller should
// create a new DirectOds entry.
func mergeMatchedViaInBlocks(result *RecallResult, odID string, vias []string) bool {
	for i := range result.OdBlocks {
		if result.OdBlocks[i].OdID == odID {
			result.OdBlocks[i].MatchedVia = appendUnique(result.OdBlocks[i].MatchedVia, vias...)
			return true
		}
	}
	for i := range result.DirectOds {
		if result.DirectOds[i].OdID == odID {
			result.DirectOds[i].MatchedVia = appendUnique(result.DirectOds[i].MatchedVia, vias...)
			return true
		}
	}
	return false
}

func appendUnique(ss []string, vals ...string) []string {
	set := map[string]bool{}
	for _, s := range ss {
		set[s] = true
	}
	for _, v := range vals {
		if v != "" && !set[v] {
			ss = append(ss, v)
			set[v] = true
		}
	}
	return ss
}

// recallOlFacts performs 3-tier cascade recall over confirmed learned facts (Ol):
//
//	Tier 1 (TAG_EXACT): any tag exactly matches a token (case-insensitive)
//	Tier 2 (TAG_FUZZY): ILIKE substring match on unnested tags (only if tier 1 missed)
//	Tier 3 (VEC):       cosine similarity on content_vector (only if tiers 1+2 missed)
//
// Returns deduplicated OlEntry list; each entry's Tokens field records which input
// tokens triggered the match. Only facts with confidence='confirmed' and mark=true
// are considered.
func recallOlFacts(db *sql.DB, projectID string, tokens []string, embeddings [][]float64) []OlEntry {
	if !strings.Contains(projectID, "-") {
		return []OlEntry{}
	}
	// factID → entry (dedupe across tokens, merge token list)
	byID := map[string]*OlEntry{}
	// Preserve first-seen insertion order for deterministic output
	var order []string

	addHit := func(id, title, summary, tagsRaw, tier string, score float64, tok string) {
		if e, ok := byID[id]; ok {
			// Merge token list (dedupe)
			for _, t := range e.Tokens {
				if t == tok {
					return
				}
			}
			e.Tokens = append(e.Tokens, tok)
			return
		}
		e := &OlEntry{
			ID:      id,
			Title:   title,
			Summary: summary,
			Tags:    ParsePgTextArray(tagsRaw),
			Tier:    tier,
			Score:   score,
			Tokens:  []string{tok},
		}
		byID[id] = e
		order = append(order, id)
	}

	for ti, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}

		tokenHit := false

		// ── Tier 1: TAG_EXACT ──
		rows, err := db.Query(`
			SELECT f.id::text, COALESCE(f.title,''), f.summary, COALESCE(f.tags,'{}')::text
			FROM ont_learned_fact f
			WHERE f.project_id = $1
			  AND f.confidence = 'confirmed'
			  AND LOWER($2) = ANY(SELECT LOWER(unnest(f.tags)))
			LIMIT 10`, projectID, tok)
		if err == nil {
			for rows.Next() {
				var id, title, summary, tagsRaw string
				rows.Scan(&id, &title, &summary, &tagsRaw)
				addHit(id, title, summary, tagsRaw, "TAG_EXACT", 1.0, tok)
				tokenHit = true
			}
			rows.Close()
		}

		// ── Tier 2: TAG_FUZZY (only if tier 1 missed for this token) ──
		if !tokenHit && len([]rune(tok)) >= 2 {
			rows, err = db.Query(`
				SELECT f.id::text, COALESCE(f.title,''), f.summary, COALESCE(f.tags,'{}')::text
				FROM ont_learned_fact f
				WHERE f.project_id = $1
				  AND f.confidence = 'confirmed'
				  AND EXISTS (SELECT 1 FROM unnest(f.tags) t WHERE t ILIKE '%'||$2||'%')
				LIMIT 5`, projectID, tok)
			if err == nil {
				for rows.Next() {
					var id, title, summary, tagsRaw string
					rows.Scan(&id, &title, &summary, &tagsRaw)
					addHit(id, title, summary, tagsRaw, "TAG_FUZZY", 0.75, tok)
					tokenHit = true
				}
				rows.Close()
			}
		}

		// ── Tier 3: VEC (only if tiers 1+2 missed for this token) ──
		if !tokenHit && ti < len(embeddings) && len(embeddings[ti]) > 0 {
			vecStr := PgVec(embeddings[ti])
			rows, err = db.Query(`
				SELECT f.id::text, COALESCE(f.title,''), f.summary, COALESCE(f.tags,'{}')::text,
				       f.content_vector <=> $2::vector AS dist
				FROM ont_learned_fact f
				WHERE f.project_id = $1
				  AND f.confidence = 'confirmed'
				  AND f.content_vector IS NOT NULL
				  AND f.content_vector <=> $2::vector < 0.15
				ORDER BY dist LIMIT 3`, projectID, vecStr)
			if err == nil {
				for rows.Next() {
					var id, title, summary, tagsRaw string
					var dist float64
					rows.Scan(&id, &title, &summary, &tagsRaw, &dist)
					addHit(id, title, summary, tagsRaw, "VEC", 1.0-dist, tok)
				}
				rows.Close()
			}
		}
	}

	result := make([]OlEntry, 0, len(order))
	for _, id := range order {
		result = append(result, *byID[id])
	}
	return result
}
