package recall

import (
	"database/sql"
	"log"
)

// propertyLineage indexes ont_causality(join_key) edges so recall hits can be
// rewritten to point at the canonical (most-upstream) Od for the value they
// carry.
//
// Two layers are indexed because real ontologies use join_key edges to mean
// two different things:
//
//  1. **Property-level edges** (e.g. PRODUCT.PRODUCT_OFFERING_SHORT_NAME →
//     MTM.CODE_NAME) explicitly map a CANONICAL column on the parent to a
//     DENORMALISED copy on the child, possibly under a different column name.
//     This is the easy case — walk the edge directly.
//
//  2. **Object-level edges that join on key X but denormalise value Y**
//     (e.g. MTM → MTM_CHAR_VALUE via MODEL_NUMBER, where CODE_NAME is a
//     denormalised attribute that lives on both tables under the same name).
//     The property-level graph won't have CODE_NAME→CODE_NAME because the
//     join_key is MODEL_NUMBER. So we walk via the Od-level edge and check
//     whether the parent Od has a same-named column; if yes, that's the
//     upstream of this value.
//
// Walk priority at each hop: property-level direct edge first (explicit
// canonical mapping wins), then Od-level + same-name fallback. Repeat until
// no edge applies or the cap is hit.
type propertyLineage struct {
	// Property-level: child_propID → upstream property info.
	parent map[string]lineageParent

	// Od-level: child_odID → list of parent_odIDs via ANY join_key edge.
	// Deduped across multiple edges that share the same (parent, child) pair.
	parentOds map[string][]string

	// Per-Od property index: odID → propName (lowercased) → property info.
	// Used to resolve "does parent Od have a property with this name?" during
	// the Od-level fallback walk. Lowercasing folds case mismatches between
	// the two schemas — real-world data sometimes spells the same column
	// differently in case across the parent and child tables.
	propsByOd map[string]map[string]lineageParent
}

// lineageParent captures everything a hit needs after being rewritten to its
// upstream — every field the search functions stuff into lakehouseHit / the
// embedded KeywordHit's display columns.
type lineageParent struct {
	PropID   string
	OdID     string
	OdName   string
	PropName string
	SrcCol   string
	DataType string
	PropDesc string
}

// maxLineageHops bounds the upstream walk. Real snowflake schemas are <8 deep;
// the cap is an insurance policy against accidental cycles in ont_causality
// (defense-in-depth alongside the visited set).
const maxLineageHops = 8

// buildPropertyLineage loads every join_key edge for a project + all visible
// properties, and indexes them at both property and Od granularity.
//
// Two SQL round-trips per recall invocation; the result is read-only for the
// rest of the request. Multi-parent handling: if a child property has more
// than one upstream parent at the property level (shouldn't happen in a clean
// ontology), the first row wins with a warning. At the Od level, multiple
// parents are kept — the walk tries them in DB order until one yields a
// same-named property.
func buildPropertyLineage(db *sql.DB, projectID string) *propertyLineage {
	pl := &propertyLineage{
		parent:    map[string]lineageParent{},
		parentOds: map[string][]string{},
		propsByOd: map[string]map[string]lineageParent{},
	}

	// ── Pass 1: property-level join_key edges + Od-level edges ──
	rows, err := db.Query(`
		SELECT fp.id::text AS parent_prop_id,
		       fp.object_type_id::text AS parent_od_id,
		       fo.name AS parent_od_name,
		       fp.name AS parent_prop_name,
		       COALESCE(fp.source_column,'') AS parent_src_col,
		       COALESCE(fp.data_type,'') AS parent_dtype,
		       COALESCE(fp.description,'') AS parent_desc,
		       tp.id::text AS child_prop_id,
		       tp.object_type_id::text AS child_od_id
		FROM ont_causality c
		JOIN ont_knowledge fk ON c.from_knowledge_id = fk.id AND fk.anchor_type = 'property'
		JOIN ont_property fp ON fk.anchor_id = fp.id
		JOIN ont_object_type fo ON fp.object_type_id = fo.id
		JOIN ont_knowledge tk ON c.to_knowledge_id = tk.id AND tk.anchor_type = 'property'
		JOIN ont_property tp ON tk.anchor_id = tp.id
		JOIN ont_object_type to_ ON tp.object_type_id = to_.id
		WHERE c.project_id = $1 AND c.relation_type = 'join_key'
		  AND COALESCE(fo.mark, true) = true
		  AND COALESCE(to_.mark, true) = true`, projectID)
	if err != nil {
		log.Printf("recall_lakehouse: buildPropertyLineage edges error: %v", err)
		return pl
	}
	odPairSeen := map[string]bool{} // dedup "childOd|parentOd" pairs
	func() {
		defer rows.Close()
		for rows.Next() {
			var p lineageParent
			var childPropID, childOdID string
			if err := rows.Scan(&p.PropID, &p.OdID, &p.OdName, &p.PropName,
				&p.SrcCol, &p.DataType, &p.PropDesc,
				&childPropID, &childOdID); err != nil {
				log.Printf("recall_lakehouse: lineage edge scan: %v", err)
				continue
			}
			// Property-level: first-wins for multi-parent children.
			if existing, dup := pl.parent[childPropID]; dup {
				if existing.PropID != p.PropID {
					log.Printf("recall_lakehouse: property %s has multiple join_key parents (keeping %s, ignoring %s)",
						childPropID, existing.PropID, p.PropID)
				}
			} else {
				pl.parent[childPropID] = p
			}
			// Od-level: dedup (child, parent) pairs.
			pairKey := childOdID + "|" + p.OdID
			if !odPairSeen[pairKey] && childOdID != p.OdID {
				odPairSeen[pairKey] = true
				pl.parentOds[childOdID] = append(pl.parentOds[childOdID], p.OdID)
			}
		}
	}()

	// ── Pass 2: every visible property indexed by Od + lowercased name ──
	// Powers the Od-level fallback walk: given an Od and a column name, return
	// the property info. Loading the whole table is O(properties) per request
	// but it's a flat read with no JOINs; cheap compared to recall itself.
	propRows, err := db.Query(`
		SELECT p.id::text, p.object_type_id::text, o.name, p.name,
		       COALESCE(p.source_column,''), COALESCE(p.data_type,''),
		       COALESCE(p.description,'')
		FROM ont_property p
		JOIN ont_object_type o ON o.id = p.object_type_id
		WHERE o.project_id = $1 AND COALESCE(o.mark, true) = true`, projectID)
	if err != nil {
		log.Printf("recall_lakehouse: buildPropertyLineage props error: %v", err)
		return pl
	}
	defer propRows.Close()
	for propRows.Next() {
		var p lineageParent
		if err := propRows.Scan(&p.PropID, &p.OdID, &p.OdName, &p.PropName,
			&p.SrcCol, &p.DataType, &p.PropDesc); err != nil {
			log.Printf("recall_lakehouse: lineage prop scan: %v", err)
			continue
		}
		byName, ok := pl.propsByOd[p.OdID]
		if !ok {
			byName = map[string]lineageParent{}
			pl.propsByOd[p.OdID] = byName
		}
		byName[lower(p.PropName)] = p
	}
	return pl
}

// lower is a tiny helper to keep the lineage index lookup case-insensitive
// without dragging in strings.ToLower at every call site. Pure ASCII fast-
// path; we only ever feed it column names (no Unicode case folding needed).
func lower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// walkUpstream resolves a (propID, propName, odID) to its topmost canonical
// upstream. Two-tier walk at each hop:
//
//  1. **Property-level direct edge** — handles canonical mappings between
//     differently-named columns (e.g. PRODUCT.PRODUCT_OFFERING_SHORT_NAME →
//     MTM.CODE_NAME).
//  2. **Od-level edge + same-name lookup** — handles denormalised same-name
//     columns where the join key is a DIFFERENT column (e.g. MTM.CODE_NAME
//     ←copies via MODEL_NUMBER— MTM_CHAR_VALUE.CODE_NAME).
//
// Visited set + maxLineageHops guard against cycles and pathological depth.
// Returns (zero, false) if the input is already at the root or the lineage
// map is empty.
func (pl *propertyLineage) walkUpstream(propID, propName, odID string) (lineageParent, bool) {
	if pl == nil || propID == "" {
		return lineageParent{}, false
	}
	if len(pl.parent) == 0 && len(pl.parentOds) == 0 {
		return lineageParent{}, false
	}
	visited := map[string]bool{propID: true}
	currentProp, currentName, currentOd := propID, propName, odID
	var top lineageParent
	found := false
	for hop := 0; hop < maxLineageHops; hop++ {
		// Tier 1: property-level direct edge.
		if p, ok := pl.parent[currentProp]; ok {
			if visited[p.PropID] {
				log.Printf("recall_lakehouse: lineage cycle at prop %s; stopping walk", p.PropID)
				break
			}
			visited[p.PropID] = true
			top = p
			found = true
			currentProp, currentName, currentOd = p.PropID, p.PropName, p.OdID
			continue
		}
		// Tier 2: Od-level edge + same-name property on a parent Od.
		next, ok := pl.findSameNameParent(currentOd, currentName, visited)
		if !ok {
			break
		}
		visited[next.PropID] = true
		top = next
		found = true
		currentProp, currentName, currentOd = next.PropID, next.PropName, next.OdID
	}
	return top, found
}

// findSameNameParent looks for an upstream Od that has a property with the
// given name. Iterates parents in the order they were inserted (DB-order
// determinism). Skips parents that are already visited (cycle guard inherited
// from the caller) and parents missing from propsByOd.
//
// Returns the first match; if multiple parents have the column, the choice is
// arbitrary-but-deterministic. Real ontologies with multiple "same name on
// multiple parents" are exceedingly rare (a denormalised value typically lives
// on one canonical parent chain); when they do occur, downstream Ambiguity
// detection still has a chance to surface the conflict.
func (pl *propertyLineage) findSameNameParent(odID, propName string, visited map[string]bool) (lineageParent, bool) {
	parents := pl.parentOds[odID]
	if len(parents) == 0 || propName == "" {
		return lineageParent{}, false
	}
	target := lower(propName)
	for _, parentOdID := range parents {
		props, ok := pl.propsByOd[parentOdID]
		if !ok {
			continue
		}
		p, ok := props[target]
		if !ok {
			continue
		}
		if visited[p.PropID] {
			continue
		}
		return p, true
	}
	return lineageParent{}, false
}

// normalizeValueAliasHits rewrites every value-alias hit in place so its
// PropertyID / OdID / display columns point at the canonical upstream — the
// "原始出发路径" root. Column-name aliases (is_column_name=true) are passed
// through untouched: those identify a column itself, not a value stored in it,
// and upstreaming them would corrupt the column reference the LLM needs.
//
// Returns the deduped slice — after rewrite, multiple hits for the same token
// can collapse onto identical (token, propID, odID) tuples; the caller should
// see only one canonical hit per token+root so applyExactDisambiguation's per-
// token counter reflects post-normalisation cardinality.
func normalizeValueAliasHits(hits []lakehouseHit, pl *propertyLineage) []lakehouseHit {
	if len(hits) == 0 || pl == nil {
		return hits
	}
	if len(pl.parent) == 0 && len(pl.parentOds) == 0 {
		return hits
	}
	out := make([]lakehouseHit, 0, len(hits))
	seen := make(map[string]bool, len(hits))
	for _, h := range hits {
		// Skip Od-alias hits (PropertyID==""), column-name aliases, and rows
		// whose upstream doesn't exist in the graph (already canonical).
		if h.PropertyID != "" && !h.IsColumnRef {
			if up, ok := pl.walkUpstream(h.PropertyID, h.PropName, h.OdID); ok {
				h.PropertyID = up.PropID
				h.PropName = up.PropName
				h.SourceColumn = up.SrcCol
				h.DataType = up.DataType
				h.PropDesc = up.PropDesc
				h.OdID = up.OdID
				h.OdName = up.OdName
				// Re-point the embedded KeywordHit's display columns so
				// TokenDetails and the formatted context show the upstream
				// table/field, not the downstream copy.
				h.MappedTable = up.OdName
				h.MappedField = up.PropName
			}
		}
		// Dedupe by (token, propID, odID, tier) — same root reached from
		// multiple downstream copies must collapse, but a token that genuinely
		// hits two distinct upstream roots stays as two hits.
		key := h.MatchedToken + "|" + h.PropertyID + "|" + h.OdID + "|" + h.Tier
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, h)
	}
	return out
}
