package recall

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
)

// resolveProperties finds ont_property rows whose source_column matches mapped_table.mapped_field.
func resolveProperties(db *sql.DB, projectID, mappedTable, mappedField string) []PropertyMatch {
	if mappedField == "" {
		return nil
	}

	rows, err := db.Query(`
		SELECT p.id::text, p.name, COALESCE(p.display_name,''), COALESCE(p.source_column,''),
		       COALESCE(p.data_type,''), COALESCE(p.description,''), p.object_type_id::text
		FROM ont_property p
		JOIN ont_object_type o ON o.id = p.object_type_id
		WHERE o.project_id = $1
		  AND (p.source_column = $2||'.'||$3
		    OR p.source_column = $3
		    OR SPLIT_PART(p.source_column,'.',2) = $3)`,
		projectID, mappedTable, mappedField)
	if err != nil {
		log.Printf("recall: resolveProperties error: %v", err)
		return nil
	}
	defer rows.Close()

	var props []PropertyMatch
	for rows.Next() {
		var p PropertyMatch
		rows.Scan(&p.PropertyID, &p.Name, &p.DisplayName, &p.SourceColumn,
			&p.DataType, &p.Description, &p.ObjectTypeID)
		props = append(props, p)
	}
	return props
}

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

// resolveOdLinks populates link info between Od blocks in the result.
func resolveOdLinks(db *sql.DB, result *RecallResult) {
	odIDs := map[string]string{} // id → name
	for i := range result.OdBlocks {
		odIDs[result.OdBlocks[i].OdID] = result.OdBlocks[i].Name
	}
	for i := range result.DirectOds {
		odIDs[result.DirectOds[i].OdID] = result.DirectOds[i].Name
	}

	ids := make([]string, 0, len(odIDs))
	for id := range odIDs {
		ids = append(ids, id)
	}

	for a := 0; a < len(ids); a++ {
		for b := a + 1; b < len(ids); b++ {
			var cardinality string
			db.QueryRow(`
				SELECT cardinality FROM ont_link_type
				WHERE (from_object_id = $1 AND to_object_id = $2)
				   OR (from_object_id = $2 AND to_object_id = $1)
				LIMIT 1`, ids[a], ids[b]).Scan(&cardinality)
			if cardinality == "" {
				continue
			}
			// Add link to both blocks
			for i := range result.OdBlocks {
				if result.OdBlocks[i].OdID == ids[a] {
					result.OdBlocks[i].Links = append(result.OdBlocks[i].Links,
						OdLink{TargetOdName: odIDs[ids[b]], Cardinality: cardinality})
				}
				if result.OdBlocks[i].OdID == ids[b] {
					result.OdBlocks[i].Links = append(result.OdBlocks[i].Links,
						OdLink{TargetOdName: odIDs[ids[a]], Cardinality: cardinality})
				}
			}
		}
	}
}

// pruneOneToMany suppresses redundant "many"-side Od blocks when the
// "one" (dimensional) side is already present in the recall result.
//
// When a filter-value keyword hits properties on BOTH sides of a 1→N link,
// the "one" side is the correct dimensional anchor. The "many" side matched
// only because the same value exists in its denormalised/bridge column.
//
// Rules:
//  1. Remove matched properties from the "many" Od whose keywords ALL
//     overlap with the "one" Od's keywords.
//  2. If the "many" Od has zero remaining MatchedProps AND no knowledge
//     entries anchored to it → drop it from the result entirely.
func pruneOneToMany(db *sql.DB, result *RecallResult) {
	if len(result.OdBlocks) < 2 {
		return
	}

	// Index Od blocks by ID
	idxMap := map[string]int{}
	for i := range result.OdBlocks {
		idxMap[result.OdBlocks[i].OdID] = i
	}

	ids := make([]string, 0, len(idxMap))
	for id := range idxMap {
		ids = append(ids, id)
	}

	// Find one-to-many links where both sides are in the result.
	// from_object_id = "one" side, to_object_id = "many" side.
	type oneMany struct{ oneIdx, manyIdx int }
	var pairs []oneMany

	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			var fromID, toID string
			db.QueryRow(`
				SELECT from_object_id::text, to_object_id::text
				FROM ont_link_type
				WHERE cardinality = 'one-to-many' AND mark = true
				  AND ((from_object_id = $1 AND to_object_id = $2)
				    OR (from_object_id = $2 AND to_object_id = $1))
				LIMIT 1`, ids[i], ids[j]).Scan(&fromID, &toID)
			if fromID == "" {
				continue
			}
			fi, okF := idxMap[fromID]
			ti, okT := idxMap[toID]
			if okF && okT {
				pairs = append(pairs, oneMany{oneIdx: fi, manyIdx: ti})
			}
		}
	}

	if len(pairs) == 0 {
		return
	}

	// For each pair, collect "one" side keywords and prune "many" side
	toRemove := map[int]bool{}

	for _, p := range pairs {
		oneBlk := &result.OdBlocks[p.oneIdx]
		manyBlk := &result.OdBlocks[p.manyIdx]

		// Collect all keyword strings from the "one" side
		oneKWs := map[string]bool{}
		for _, prop := range oneBlk.MatchedProps {
			for _, kw := range prop.Keywords {
				oneKWs[kw.Keyword] = true
			}
		}
		if len(oneKWs) == 0 {
			continue
		}

		// Remove "many" props whose keywords ALL overlap with "one"
		var kept []PropertyMatch
		for _, prop := range manyBlk.MatchedProps {
			hasUnique := false
			for _, kw := range prop.Keywords {
				if !oneKWs[kw.Keyword] {
					hasUnique = true
					break
				}
			}
			if hasUnique {
				kept = append(kept, prop)
			}
		}
		manyBlk.MatchedProps = kept

		// If "many" has no remaining props, check for anchored knowledge
		if len(kept) == 0 {
			var okCount int
			db.QueryRow(`SELECT COUNT(*) FROM ont_knowledge
				WHERE anchor_type = 'object' AND anchor_id = $1 AND mark = true`,
				manyBlk.OdID).Scan(&okCount)
			if okCount == 0 {
				toRemove[p.manyIdx] = true
				log.Printf("recall: pruneOneToMany: removing Od %q (many-side of %q, no unique props/knowledge)",
					manyBlk.Name, oneBlk.Name)
			}
		}
	}

	// Remove empty "many" Ods
	if len(toRemove) > 0 {
		var kept []OdBlock
		for i, blk := range result.OdBlocks {
			if !toRemove[i] {
				kept = append(kept, blk)
			}
		}
		if kept == nil {
			kept = []OdBlock{}
		}
		result.OdBlocks = kept
	}
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

// detectAmbiguities checks for filter-value ambiguity across result Ods.
//
// Rule: when the same filter value (keyword that is NOT a column reference)
// hits ≥2 distinct Ods, check whether any of those Ods dominates all the
// others (i.e., is the "one" side with 1→N links to every other hit Od).
//   - If a dominator exists AND is in the hit set → not ambiguous (the "one"
//     is present as a dimensional anchor).
//   - If no dominator exists in the hit set → ambiguous (the "one" is missing,
//     all hit Ods are "many" siblings → system cannot decide which to filter).
//
// Additionally, if different tokens hit completely unrelated Ods (no shared
// parent at all), that is also flagged because no dominator can be found.
func detectAmbiguities(db *sql.DB, blocks []OdBlock) []Ambiguity {
	if len(blocks) < 2 {
		return nil
	}

	// ── Step A: collect filter-value hits per keyword (value matches only) ──
	// keyword → list of (odBlock, propName, propDesc)
	type hitInfo struct {
		block    *OdBlock
		propName string
		propDesc string
	}
	keywordHits := map[string][]hitInfo{}
	seen := map[string]map[string]bool{} // keyword → odID → dedup

	for i := range blocks {
		blk := &blocks[i]
		for _, p := range blk.MatchedProps {
			dn := p.DisplayName
			if dn == "" {
				dn = p.Name
			}
			for _, kw := range p.Keywords {
				// Only consider filter values, not column references
				if isColumnRef(kw, p.Name, dn) {
					continue
				}
				if seen[kw.Keyword] == nil {
					seen[kw.Keyword] = map[string]bool{}
				}
				if seen[kw.Keyword][blk.OdID] {
					continue
				}
				seen[kw.Keyword][blk.OdID] = true
				keywordHits[kw.Keyword] = append(keywordHits[kw.Keyword], hitInfo{
					block: blk, propName: dn, propDesc: p.Description,
				})
			}
		}
	}

	// ── Step B: for each keyword with ≥2 Od hits, check for in-set dominator ──
	resultOdIDs := map[string]bool{}
	for _, blk := range blocks {
		resultOdIDs[blk.OdID] = true
	}

	var ambiguities []Ambiguity
	for kw, hits := range keywordHits {
		if len(hits) < 2 {
			continue
		}
		hitIDs := make([]string, 0, len(hits))
		for _, h := range hits {
			hitIDs = append(hitIDs, h.block.OdID)
		}

		// Find dominator: an Od that has 1→N links to all other hit Ods (or is self)
		dominatorID := findDominator(db, hitIDs)

		// If dominator found AND it's in the result set → not ambiguous
		if dominatorID != "" && resultOdIDs[dominatorID] {
			continue
		}

		// No in-set dominator → ambiguous
		candidates := make([]AmbiguityCandidate, 0, len(hits))
		for _, h := range hits {
			candidates = append(candidates, AmbiguityCandidate{
				OdID:          h.block.OdID,
				OdName:        h.block.Name,
				OdDescription: h.block.Description,
				PropertyName:  h.propName,
				PropertyDesc:  h.propDesc,
			})
		}
		ambiguities = append(ambiguities, Ambiguity{
			Keyword:    kw,
			Candidates: candidates,
		})
	}

	// ── Step C: check for disconnected token groups ──
	// Even if no single keyword is ambiguous on its own, different tokens
	// hitting completely unrelated Ods (no link between them, no shared
	// parent in result) is also ambiguous.
	if len(ambiguities) == 0 && len(blocks) >= 2 {
		// Collect all unique Od IDs from result
		allIDs := make([]string, 0, len(blocks))
		for _, blk := range blocks {
			allIDs = append(allIDs, blk.OdID)
		}
		domID := findDominator(db, allIDs)
		if domID == "" || !resultOdIDs[domID] {
			// Check graph connectivity as fallback
			nameToID := map[string]string{}
			for _, blk := range blocks {
				nameToID[blk.Name] = blk.OdID
			}
			adj := map[string]map[string]bool{}
			for i := range blocks {
				blk := &blocks[i]
				if adj[blk.OdID] == nil {
					adj[blk.OdID] = map[string]bool{}
				}
				for _, lnk := range blk.Links {
					if tid, ok := nameToID[lnk.TargetOdName]; ok {
						adj[blk.OdID][tid] = true
						if adj[tid] == nil {
							adj[tid] = map[string]bool{}
						}
						adj[tid][blk.OdID] = true
					}
				}
			}
			visited := map[string]bool{blocks[0].OdID: true}
			queue := []string{blocks[0].OdID}
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				for nb := range adj[cur] {
					if !visited[nb] {
						visited[nb] = true
						queue = append(queue, nb)
					}
				}
			}
			if len(visited) < len(blocks) {
				// Graph is disconnected → build ambiguity from all Ods
				var cands []AmbiguityCandidate
				var tokens []string
				for i := range blocks {
					blk := &blocks[i]
					c := AmbiguityCandidate{
						OdID: blk.OdID, OdName: blk.Name, OdDescription: blk.Description,
					}
					if len(blk.MatchedProps) > 0 {
						dn := blk.MatchedProps[0].DisplayName
						if dn == "" {
							dn = blk.MatchedProps[0].Name
						}
						c.PropertyName = dn
						c.PropertyDesc = blk.MatchedProps[0].Description
					}
					cands = append(cands, c)
					for _, p := range blk.MatchedProps {
						for _, kw := range p.Keywords {
							tokens = appendUnique(tokens, kw.MatchedToken)
						}
					}
				}
				ambiguities = append(ambiguities, Ambiguity{
					Keyword:    strings.Join(tokens, " + "),
					Candidates: cands,
				})
			}
		}
	}

	return ambiguities
}

// findDominator returns the ID of a **root** Od that dominates all given Ods
// (self + 1→N links to the rest), or "" if none found.
// A root Od is one that has no incoming one-to-many links from any parent —
// it sits at the top of its dimension hierarchy. Intermediate Ods (like
// character-value which is "one" of MTM but "many" of Product) are NOT
// considered valid dominators because they are not the true dimensional anchor.
func findDominator(db *sql.DB, odIDs []string) string {
	if len(odIDs) < 2 {
		return ""
	}
	valuesList := make([]string, len(odIDs))
	args := make([]interface{}, len(odIDs))
	for i, id := range odIDs {
		valuesList[i] = fmt.Sprintf("($%d::uuid)", i+1)
		args[i] = id
	}
	query := fmt.Sprintf(`
		WITH input_ids(od_id) AS (VALUES %s),
		candidate_doms AS (
			SELECT od_id AS candidate_id, od_id AS dominated_id FROM input_ids
			UNION
			SELECT lt.from_object_id, lt.to_object_id
			FROM ont_link_type lt
			JOIN input_ids i ON i.od_id = lt.to_object_id
			WHERE lt.cardinality = 'one-to-many' AND lt.mark = true
		)
		SELECT candidate_id::text FROM candidate_doms
		WHERE NOT EXISTS (
			-- Exclude non-root candidates: those that are "many" of some parent
			SELECT 1 FROM ont_link_type lt2
			WHERE lt2.cardinality = 'one-to-many' AND lt2.mark = true
			  AND lt2.to_object_id = candidate_doms.candidate_id
		)
		GROUP BY candidate_id
		HAVING COUNT(DISTINCT dominated_id) = %d
		LIMIT 1`, strings.Join(valuesList, ","), len(odIDs))

	var candID string
	err := db.QueryRow(query, args...).Scan(&candID)
	if err == nil {
		return candID
	}
	return ""
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
