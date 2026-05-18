package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/observability"

	. "github.com/lakehouse2ontology/httputil"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Shared SQL for loading join_key causalities between Ods.
const joinKeyCausalitySQL = `
	SELECT c.direction,
	       fo.id::text AS from_od_id, fo.name AS from_od_name,
	       to_.id::text AS to_od_id, to_.name AS to_od_name
	FROM ont_causality c
	JOIN ont_knowledge fk ON c.from_knowledge_id = fk.id AND fk.anchor_type = 'property'
	JOIN ont_property fp ON fk.anchor_id = fp.id
	JOIN ont_object_type fo ON fp.object_type_id = fo.id
	JOIN ont_knowledge tk ON c.to_knowledge_id = tk.id AND tk.anchor_type = 'property'
	JOIN ont_property tp ON tk.anchor_id = tp.id
	JOIN ont_object_type to_ ON tp.object_type_id = to_.id
	WHERE c.project_id = $1 AND c.relation_type = 'join_key'
	  AND COALESCE(fo.mark, true) = true AND COALESCE(to_.mark, true) = true`

// BuildLakehouseContext performs the token recall pipeline for lakehouse projects:
//
//	Token → lakehouse_keyword (2-tier: exact/fuzzy) → ont_property → Od → Ok
//
// Fallback paths reuse the standard pipeline:
//   - ont_knowledge_keyword → Ok (non-property concepts)
//   - ont_object_type.name direct match → Od
//   - ont_learned_fact → Ol (confirmed facts via tags/vector)
func BuildLakehouseContext(ctx context.Context, db *sql.DB, projectID string, tokens []string, question string) RecallResult {
	_, span := observability.Tracer().Start(ctx, "recall.build_context",
		trace.WithAttributes(
			attribute.String("project_id", projectID),
			attribute.Int("token_count", len(tokens)),
		))
	defer span.End()
	start := time.Now()
	defer func() {
		observability.RecallBuildDuration.Observe(float64(time.Since(start).Milliseconds()))
	}()

	result := RecallResult{
		TokenDetails: make(map[string][]KeywordHit),
	}

	// ── Pre-embed all tokens once (batched) ──
	// Reused both for the new Tier 3 VEC inside searchLakehouseKeywordFull and
	// for the Ol fact recall in Step 5.5. Single embedding API call (the
	// llmclient batches 4-at-a-time internally) instead of one per goroutine.
	tokenEmbeddings, _ := llmclient.EmbedTexts(db, tokens)
	tokenVec := make(map[string][]float64, len(tokens))
	for i, tok := range tokens {
		if i < len(tokenEmbeddings) {
			tokenVec[tok] = tokenEmbeddings[i]
		}
	}

	// ── Step 1: Concurrent 3-tier keyword search per token via lakehouse_keyword ──
	// Tier 1 EXACT → Tier 2 FUZZY → Tier 3 VEC (≥0.85). VEC only fires if both
	// text tiers miss; uses keyword_vector ∪ alias_vectors.

	var wg sync.WaitGroup

	type tokenResultFull struct {
		token  string
		hits   []KeywordHit
		lhHits []lakehouseHit
	}
	chFull := make(chan tokenResultFull, len(tokens))

	for _, tok := range tokens {
		if strings.TrimSpace(tok) == "" {
			continue
		}
		wg.Add(1)
		go func(tok string, vec []float64) {
			defer wg.Done()
			kwHits, lhHits := searchLakehouseKeywordFull(ctx, db, projectID, tok, vec)
			// Od-alias rows (property_id NULL, object_id NOT NULL) run alongside
			// the property search. Their hits are appended to the same slice and
			// are picked up downstream by the Step-2 loop via PropertyID=="".
			odKwHits, odLhHits := searchLakehouseOdAlias(ctx, db, projectID, tok, vec)
			kwHits = append(kwHits, odKwHits...)
			lhHits = append(lhHits, odLhHits...)
			chFull <- tokenResultFull{token: tok, hits: kwHits, lhHits: lhHits}
		}(tok, tokenVec[tok])
	}
	go func() { wg.Wait(); close(chFull) }()

	// Collect all hits, track per-token details.
	var allHits []lakehouseHit
	matchedTokens := map[string]bool{}
	for tr := range chFull {
		if tr.hits == nil {
			tr.hits = []KeywordHit{}
		}
		result.TokenDetails[tr.token] = tr.hits
		if len(tr.hits) > 0 {
			matchedTokens[tr.token] = true
		}
		allHits = append(allHits, tr.lhHits...)
	}

	// ── Step 1.6: EXACT-tier disambiguation cascade ──
	// When a single token produces N>1 EXACT hits across different properties:
	//   1. Prefer candidates whose Od is already pinned by ANOTHER single-hit
	//      token (using existing recall context to narrow scope)
	//   2. Else if the candidate Ods are connected by an ont_causality(join_key)
	//      edge, keep only the "1" side (dimension); drop the "N" side (fact).
	//      Convention: from_od of the join_key causality is the "1" side.
	//   3. Else keep all — leaves it for the existing ambiguity detector.
	// FUZZY and VEC tier hits are unaffected.
	allHits = applyExactDisambiguation(db, projectID, allHits)
	// Rebuild TokenDetails to reflect the dedupe (preserve MISS tokens).
	for tok := range result.TokenDetails {
		result.TokenDetails[tok] = []KeywordHit{}
	}
	for _, h := range allHits {
		result.TokenDetails[h.MatchedToken] = append(result.TokenDetails[h.MatchedToken], h.KeywordHit)
	}

	// ── Step 1.5: Metric Intent recall ──
	// Canonical smartquery templates keyed off lakehouse_keyword.metric_intent_id.
	// These take priority over raw Property matches when both hit the same token —
	// the Intent carries the "how to query" decision pre-baked.
	result.MetricIntents = recallMetricIntents(db, projectID, tokens, matchedTokens)

	// ── Step 2: Resolve lakehouse keyword hits → ont_property → Od ──

	// Group by property_id (already resolved via JOIN in search).
	propMap := map[string]*PropertyMatch{}
	odMap := map[string]*OdBlock{}

	for _, h := range allHits {
		// Od-alias hit — no property, but the Od block must exist so the agent
		// sees this token as anchored to a specific Od rather than floating.
		if h.PropertyID == "" && h.OdID != "" {
			if _, ok := odMap[h.OdID]; !ok {
				if od := resolveOd(db, h.OdID); od != nil {
					odMap[h.OdID] = od
				}
			}
			if blk, ok := odMap[h.OdID]; ok {
				blk.MatchedVia = appendUnique(blk.MatchedVia, "od-alias-keyword")
			}
			continue
		}
		if h.PropertyID == "" {
			continue
		}

		if existing, ok := propMap[h.PropertyID]; ok {
			existing.Keywords = append(existing.Keywords, h.KeywordHit)
		} else {
			pm := PropertyMatch{
				PropertyID:   h.PropertyID,
				Name:         h.PropName,
				DisplayName:  h.PropName,
				SourceColumn: h.SourceColumn,
				DataType:     h.DataType,
				Description:  h.PropDesc,
				ObjectTypeID: h.OdID,
				Keywords:     []KeywordHit{h.KeywordHit},
			}
			// MC annotation in description.
			if h.IsMachineCode {
				if pm.Description != "" {
					pm.Description += " "
				}
				pm.Description += "[MC: 高基数列，值不可枚举]"
			}
			// Resolve Ok for this property.
			resolvePropertyOk(db, &pm)
			propMap[h.PropertyID] = &pm
		}

		// Ensure Od block.
		if _, ok := odMap[h.OdID]; !ok {
			od := resolveOd(db, h.OdID)
			if od != nil {
				odMap[h.OdID] = od
			}
		}
		if blk, ok := odMap[h.OdID]; ok {
			blk.MatchedVia = appendUnique(blk.MatchedVia, "property")
		}
	}

	// ── Step 3: Group properties by Od ──

	for _, pm := range propMap {
		if blk, ok := odMap[pm.ObjectTypeID]; ok {
			blk.MatchedProps = append(blk.MatchedProps, *pm)
		}
	}

	// Load all property names for each Od.
	for odID, blk := range odMap {
		blk.AllPropNames, blk.AllPropDescs = loadAllPropNames(db, odID)
	}

	for _, blk := range odMap {
		result.OdBlocks = append(result.OdBlocks, *blk)
	}

	// ── Step 4: Resolve Od↔Od links via ont_causality(join_key) ──

	if len(odMap) > 1 {
		resolveLakehouseOdLinks(db, projectID, &result)
	}

	// ── Step 4.5: Detect per-keyword ambiguities (lakehouse-specific) ──

	if len(result.OdBlocks) >= 2 {
		result.Ambiguities = detectLakehouseAmbiguities(db, projectID, result.OdBlocks)
	}

	// ── Step 5: Supplementary lookups ──
	//
	// fallbackDirectOd ALWAYS runs for every non-empty token — it surfaces Od
	// matches by name / display_name / ont_alias regardless of whether the
	// token already hit a property/od-alias-keyword. When the Od is already
	// present in OdBlocks the call merges MatchedVia tags in place; otherwise
	// it appends to DirectOds. This is the core "trigger keyword → Od by name
	// or alias" path users observed missing on /lakehouse-agent/token-recall.
	//
	// fallbackOkEntries stays gated on matchedTokens — Ok entries are only
	// useful for tokens with no other anchor.

	// Reuse the pre-computed embeddings from Step 1 for Ol vector search.
	embeddings := tokenEmbeddings

	for _, tok := range tokens {
		if strings.TrimSpace(tok) == "" {
			continue
		}
		fallbackDirectOd(db, projectID, tok, &result)
		// analysis_pattern skill cards surface UNCONDITIONALLY — a skill must
		// be visible even when its trigger token also matched an Intent.
		fallbackAnalysisPatterns(db, projectID, tok, &result)
		if matchedTokens[tok] {
			continue
		}
		fallbackOkEntries(db, projectID, tok, &result)
	}

	// Fallback: property name direct match for still-unmatched tokens.
	for _, tok := range tokens {
		if matchedTokens[tok] || strings.TrimSpace(tok) == "" {
			continue
		}
		if len(result.TokenDetails[tok]) > 0 {
			continue // already matched by Ok/DirectOd fallback
		}
		fallbackPropertyMatch(db, projectID, tok, &result)
	}

	// ── Step 5.5: Recall confirmed Ol facts ──
	result.OlEntries = recallOlFacts(db, projectID, tokens, embeddings)

	// ── Step 6: Has matches? ──

	result.HasMatches = len(result.OdBlocks) > 0 || len(result.OkEntries) > 0 ||
		len(result.DirectOds) > 0 || len(result.OlEntries) > 0 ||
		len(result.MetricIntents) > 0

	if result.OdBlocks == nil {
		result.OdBlocks = []OdBlock{}
	}
	if result.OkEntries == nil {
		result.OkEntries = []OkEntry{}
	}
	if result.OlEntries == nil {
		result.OlEntries = []OlEntry{}
	}
	if result.DirectOds == nil {
		result.DirectOds = []OdBlock{}
	}

	// ── Step 7: Format context markdown ──
	result.ContextMD = FormatContext(result, tokens, question)

	return result
}

// lakehouseHit extends KeywordHit with direct property/Od resolution data.
type lakehouseHit struct {
	KeywordHit
	PropertyID    string
	PropName      string
	SourceColumn  string
	DataType      string
	PropDesc      string
	OdID          string
	OdName        string
	IsMachineCode bool
}

// searchLakehouseKeywordFull returns both KeywordHit and lakehouseHit slices.
//
// Match set = lk.keyword ∪ lk.aliases (property-scoped). Aliases broaden which
// tokens can hit a row; the row's resolved property/Od is unchanged and the
// returned Keyword field is always the canonical lk.keyword (downstream sees
// the regular value, not the alias spelling).
//
// 3-tier cascade:
//
//	Tier 1 EXACT (LOWER equality on keyword OR alias)
//	Tier 2 FUZZY (ILIKE substring on keyword OR alias)
//	Tier 3 VEC   (cosine ≥0.85 against keyword_vector ∪ alias_vector) —
//	             only when both text tiers miss AND tokVec was pre-computed.
func searchLakehouseKeywordFull(ctx context.Context, db *sql.DB, projectID, token string, tokVec []float64) ([]KeywordHit, []lakehouseHit) {
	var hits []KeywordHit
	var lhHits []lakehouseHit

	// ── Tier 1: EXACT (keyword OR any alias, case-insensitive) ──
	// Stopword rows are skipped across all tiers — they exist to silence noise
	// tokens without deleting them, so recall treats them as absent.
	rows, err := db.Query(`
		SELECT lk.id::text, lk.keyword, lk.is_machine_code, COALESCE(lk.is_column_name, false),
		       p.id::text, p.name, COALESCE(p.source_column,''), COALESCE(p.data_type,''),
		       COALESCE(p.description,''),
		       o.id::text, o.name
		FROM lakehouse_keyword lk
		JOIN ont_property p ON lk.property_id = p.id
		JOIN ont_object_type o ON p.object_type_id = o.id
		WHERE lk.project_id = $1
		  AND COALESCE(lk.is_stopword, false) = false
		  AND (
		        LOWER(lk.keyword) = LOWER($2)
		     OR EXISTS (
		          SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		          WHERE LOWER(a) = LOWER($2))
		      )
		  AND COALESCE(o.mark, true) = true
		LIMIT 10`, projectID, token)
	if err == nil {
		for rows.Next() {
			var kwID, keyword string
			var isMC, isColName bool
			var propID, propName, srcCol, dtype, pdesc, odID, odName string
			rows.Scan(&kwID, &keyword, &isMC, &isColName,
				&propID, &propName, &srcCol, &dtype, &pdesc, &odID, &odName)
			h := KeywordHit{
				KeywordID:    kwID,
				Keyword:      keyword,
				MappedTable:  odName,
				MappedField:  propName,
				Tier:         "EXACT",
				Score:        1.0,
				MatchedToken: token,
				IsColumnRef:  isColName,
			}
			hits = append(hits, h)
			lhHits = append(lhHits, lakehouseHit{
				KeywordHit: h, PropertyID: propID, PropName: propName,
				SourceColumn: srcCol, DataType: dtype, PropDesc: pdesc,
				OdID: odID, OdName: odName, IsMachineCode: isMC,
			})
		}
		rows.Close()
	}

	if len(hits) > 0 {
		return hits, lhHits // exact hit → skip fuzzy
	}

	// ── Tier 2: FUZZY (keyword OR any alias contains token, but neither equals it) ──
	tokenRuneLen := len([]rune(token))
	maxKeywordLen := tokenRuneLen * 3

	// Machine-code rows are excluded from FUZZY: a SKU/code value like
	// 'X11ABC' must never be substring-hit by a token 'X11'. EXACT keeps them
	// (the user typed the literal value); VEC also skips them (see Tier 3).
	rows, err = db.Query(`
		SELECT lk.id::text, lk.keyword, lk.is_machine_code, COALESCE(lk.is_column_name, false),
		       p.id::text, p.name, COALESCE(p.source_column,''), COALESCE(p.data_type,''),
		       COALESCE(p.description,''),
		       o.id::text, o.name
		FROM lakehouse_keyword lk
		JOIN ont_property p ON lk.property_id = p.id
		JOIN ont_object_type o ON p.object_type_id = o.id
		WHERE lk.project_id = $1
		  AND COALESCE(lk.is_stopword, false) = false
		  AND COALESCE(lk.is_machine_code, false) = false
		  AND COALESCE(p.is_machine_code, false) = false
		  AND (
		        lk.keyword ILIKE '%'||$2||'%'
		     OR EXISTS (
		          SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		          WHERE a ILIKE '%'||$2||'%')
		      )
		  AND LOWER(lk.keyword) != LOWER($2)
		  AND NOT EXISTS (
		        SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		        WHERE LOWER(a) = LOWER($2))
		  AND COALESCE(o.mark, true) = true
		LIMIT 10`, projectID, token)
	if err == nil {
		for rows.Next() {
			var kwID, keyword string
			var isMC, isColName bool
			var propID, propName, srcCol, dtype, pdesc, odID, odName string
			rows.Scan(&kwID, &keyword, &isMC, &isColName,
				&propID, &propName, &srcCol, &dtype, &pdesc, &odID, &odName)
			if len([]rune(keyword)) > maxKeywordLen {
				continue
			}
			h := KeywordHit{
				KeywordID:    kwID,
				Keyword:      keyword,
				MappedTable:  odName,
				MappedField:  propName,
				Tier:         "FUZZY",
				Score:        0.75,
				MatchedToken: token,
				IsColumnRef:  isColName,
			}
			hits = append(hits, h)
			lhHits = append(lhHits, lakehouseHit{
				KeywordHit: h, PropertyID: propID, PropName: propName,
				SourceColumn: srcCol, DataType: dtype, PropDesc: pdesc,
				OdID: odID, OdName: odName, IsMachineCode: isMC,
			})
		}
		rows.Close()
	}

	// ── FUZZY collapse: per-property, > 4 value hits → 1 ILIKE hint ──
	// When a token fuzzy-matches more than 4 distinct VALUES on the same
	// column, dumping every value into the LLM context is noise. Collapse
	// them into a single FUZZY_LIKE hit so the LLM emits a `contains` filter
	// on the column instead of an enumerated value list. Column-name aliases
	// (is_column_name=true) are kept intact — those identify the column itself
	// and benefit from showing every spelling variant.
	if len(hits) > 0 {
		hits, lhHits = collapseFuzzyValueHits(hits, lhHits)
		return hits, lhHits // fuzzy hit (possibly collapsed) → skip vector tier
	}

	// ── Tier 3: VEC (cosine ≥0.85 against keyword_vector ∪ alias_vector) ──
	// Only fires when both text tiers missed AND we have a pre-computed
	// embedding for this token (BuildLakehouseContext batch-embeds upstream).
	// Returns the single closest match — the canonical lk.keyword regardless
	// of whether the hit was via the keyword's own vector or an alias vector.
	if len(tokVec) == 0 {
		return hits, lhHits
	}
	// recall.vector_search — Tier 3 VEC cosine search against
	// lakehouse_keyword.keyword_vector ∪ alias_vector. Fires on the
	// production recall path after EXACT+FUZZY miss and a precomputed
	// embedding is available. Span covers pgvector SQL latency only —
	// embedding happens upstream in BuildLakehouseContext batch.
	_, vecSpan := observability.Tracer().Start(ctx, "recall.vector_search",
		trace.WithAttributes(
			attribute.Int("batch_size", 1),
			attribute.Int("top_k", 1),
			attribute.String("tier", "keyword_full"),
		))
	defer vecSpan.End()
	vecStr := PgVec(tokVec)
	row := db.QueryRow(`
		WITH candidates AS (
		    SELECT lk.id::text AS kw_id, lk.keyword AS canonical, 'keyword' AS source,
		           lk.keyword_vector AS vec, lk.is_machine_code AS is_mc,
		           COALESCE(lk.is_column_name,false) AS is_col,
		           p.id::text AS prop_id, p.name AS prop_name,
		           COALESCE(p.source_column,'') AS src_col,
		           COALESCE(p.data_type,'') AS dtype,
		           COALESCE(p.description,'') AS pdesc,
		           o.id::text AS od_id, o.name AS od_name
		      FROM lakehouse_keyword lk
		      JOIN ont_property p ON lk.property_id = p.id
		      JOIN ont_object_type o ON p.object_type_id = o.id
		     WHERE lk.project_id = $1 AND lk.keyword_vector IS NOT NULL
		       AND COALESCE(lk.is_stopword, false) = false
		       AND COALESCE(o.mark, true) = true
		       AND COALESCE(p.is_machine_code, false) = false
		       AND COALESCE(lk.is_machine_code, false) = false
		    UNION ALL
		    SELECT lk.id::text, lk.keyword, 'alias',
		           la.alias_vector, lk.is_machine_code,
		           COALESCE(lk.is_column_name,false),
		           p.id::text, p.name, COALESCE(p.source_column,''),
		           COALESCE(p.data_type,''), COALESCE(p.description,''),
		           o.id::text, o.name
		      FROM lakehouse_keyword_alias_vector la
		      JOIN lakehouse_keyword lk ON lk.id = la.keyword_id
		      JOIN ont_property p ON lk.property_id = p.id
		      JOIN ont_object_type o ON p.object_type_id = o.id
		     WHERE lk.project_id = $1 AND la.alias_vector IS NOT NULL
		       AND COALESCE(lk.is_stopword, false) = false
		       AND COALESCE(o.mark, true) = true
		       AND COALESCE(p.is_machine_code, false) = false
		       AND COALESCE(lk.is_machine_code, false) = false
		)
		SELECT kw_id, canonical, source,
		       prop_id, prop_name, src_col, dtype, pdesc, od_id, od_name,
		       is_mc, is_col, 1 - (vec <=> $2::vector) AS sim
		  FROM candidates
		 WHERE 1 - (vec <=> $2::vector) >= 0.85
		 ORDER BY vec <=> $2::vector ASC
		 LIMIT 1`, projectID, vecStr)

	var (
		kwID, keyword, source                                string
		propID, propName, srcCol, dtype, pdesc, odID, odName string
		isMC, isColName                                      bool
		sim                                                  float64
	)
	if err := row.Scan(&kwID, &keyword, &source,
		&propID, &propName, &srcCol, &dtype, &pdesc, &odID, &odName,
		&isMC, &isColName, &sim); err == nil {
		_ = source // currently unused in the hit; kept for future "matched via alias" annotation
		h := KeywordHit{
			KeywordID:    kwID,
			Keyword:      keyword,
			MappedTable:  odName,
			MappedField:  propName,
			Tier:         "VEC",
			Score:        sim,
			MatchedToken: token,
			IsColumnRef:  isColName,
		}
		hits = append(hits, h)
		lhHits = append(lhHits, lakehouseHit{
			KeywordHit:    h,
			PropertyID:    propID,
			PropName:      propName,
			SourceColumn:  srcCol,
			DataType:      dtype,
			PropDesc:      pdesc,
			OdID:          odID,
			OdName:        odName,
			IsMachineCode: isMC,
		})
	}

	return hits, lhHits
}

// collapseFuzzyValueHits scans a slice of FUZZY hits (already filtered to a
// single token by the caller) and groups them by property. Any property with
// more than 4 VALUE hits is collapsed to a single FUZZY_LIKE representative
// carrying FuzzyValueCount = original count. Column-name aliases (IsColumnRef)
// are left untouched — they identify the column itself and benefit from being
// fully enumerated (the LLM may need to map the user's spelling to the
// canonical column name).
//
// Returns the rebuilt (hits, lhHits) pair in lockstep.
const fuzzyValueCollapseThreshold = 4

func collapseFuzzyValueHits(hits []KeywordHit, lhHits []lakehouseHit) ([]KeywordHit, []lakehouseHit) {
	if len(hits) <= fuzzyValueCollapseThreshold {
		return hits, lhHits
	}

	// Group indices by property. Skip column-name rows (kept verbatim) and
	// rows missing a property id (defensive — Od-alias hits flow through a
	// different function so this should not happen, but guard regardless).
	type group struct{ indices []int }
	groups := map[string]*group{}
	for i, lh := range lhHits {
		if lh.IsColumnRef || lh.PropertyID == "" {
			continue
		}
		g, ok := groups[lh.PropertyID]
		if !ok {
			g = &group{}
			groups[lh.PropertyID] = g
		}
		g.indices = append(g.indices, i)
	}

	// Build a drop set + per-representative count.
	drop := make([]bool, len(hits))
	collapseCount := map[int]int{} // representative index → original count
	for _, g := range groups {
		if len(g.indices) <= fuzzyValueCollapseThreshold {
			continue
		}
		representative := g.indices[0]
		collapseCount[representative] = len(g.indices)
		for _, idx := range g.indices[1:] {
			drop[idx] = true
		}
	}

	if len(collapseCount) == 0 {
		return hits, lhHits
	}

	newHits := make([]KeywordHit, 0, len(hits))
	newLh := make([]lakehouseHit, 0, len(lhHits))
	for i := range hits {
		if drop[i] {
			continue
		}
		if cnt, ok := collapseCount[i]; ok {
			h := hits[i]
			h.Tier = "FUZZY_LIKE"
			h.FuzzyValueCount = cnt
			newHits = append(newHits, h)
			lh := lhHits[i]
			lh.KeywordHit = h
			newLh = append(newLh, lh)
		} else {
			newHits = append(newHits, hits[i])
			newLh = append(newLh, lhHits[i])
		}
	}
	return newHits, newLh
}

// searchLakehouseOdAlias matches tokens against lakehouse_keyword rows that
// point at an Od directly (object_id IS NOT NULL, property_id IS NULL). These
// are first-class aliases for the whole object — e.g. "early order" aliased to
// the Order Od. 3-tier cascade (mirrors the property path):
//
//	Tier 1 EXACT (LOWER equality on keyword OR alias)
//	Tier 2 FUZZY (ILIKE substring on keyword OR alias)
//	Tier 3 VEC   (cosine ≥0.85 against keyword_vector ∪ alias_vector) —
//	             only when both text tiers miss AND tokVec was pre-computed.
//
// Returned lakehouseHit entries carry PropertyID="" as the signal to the Step-2
// loop that this is an Od-only hit. OdID / OdName are populated so the odMap
// path can create the block.
func searchLakehouseOdAlias(ctx context.Context, db *sql.DB, projectID, token string, tokVec []float64) ([]KeywordHit, []lakehouseHit) {
	var hits []KeywordHit
	var lhHits []lakehouseHit

	// ── Tier 1: EXACT ──
	rows, err := db.Query(`
		SELECT lk.id::text, lk.keyword, o.id::text, o.name
		FROM lakehouse_keyword lk
		JOIN ont_object_type o ON lk.object_id = o.id
		WHERE lk.project_id = $1
		  AND lk.object_id IS NOT NULL
		  AND lk.property_id IS NULL
		  AND COALESCE(lk.is_stopword, false) = false
		  AND COALESCE(o.mark, true) = true
		  AND (
		        LOWER(lk.keyword) = LOWER($2)
		     OR EXISTS (
		          SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		          WHERE LOWER(a) = LOWER($2))
		      )
		LIMIT 10`, projectID, token)
	if err == nil {
		for rows.Next() {
			var kwID, keyword, odID, odName string
			rows.Scan(&kwID, &keyword, &odID, &odName)
			h := KeywordHit{
				KeywordID: kwID, Keyword: keyword,
				MappedTable: odName, MappedField: "",
				Tier: "EXACT", Score: 1.0, MatchedToken: token,
			}
			hits = append(hits, h)
			lhHits = append(lhHits, lakehouseHit{
				KeywordHit: h, PropertyID: "", PropName: "",
				OdID: odID, OdName: odName,
			})
		}
		rows.Close()
	}

	if len(hits) > 0 {
		return hits, lhHits
	}

	// ── Tier 2: FUZZY ──
	tokenRuneLen := len([]rune(token))
	maxKeywordLen := tokenRuneLen * 3

	// Machine-code Od-alias rows excluded from FUZZY (mirrors property-level
	// FUZZY guard above). Od aliases are usually business terms, but a row
	// flagged is_machine_code=true must never be substring-hit.
	rows, err = db.Query(`
		SELECT lk.id::text, lk.keyword, o.id::text, o.name
		FROM lakehouse_keyword lk
		JOIN ont_object_type o ON lk.object_id = o.id
		WHERE lk.project_id = $1
		  AND lk.object_id IS NOT NULL
		  AND lk.property_id IS NULL
		  AND COALESCE(lk.is_stopword, false) = false
		  AND COALESCE(lk.is_machine_code, false) = false
		  AND COALESCE(o.mark, true) = true
		  AND (
		        lk.keyword ILIKE '%'||$2||'%'
		     OR EXISTS (
		          SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		          WHERE a ILIKE '%'||$2||'%')
		      )
		  AND LOWER(lk.keyword) != LOWER($2)
		  AND NOT EXISTS (
		        SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		        WHERE LOWER(a) = LOWER($2))
		LIMIT 10`, projectID, token)
	if err == nil {
		for rows.Next() {
			var kwID, keyword, odID, odName string
			rows.Scan(&kwID, &keyword, &odID, &odName)
			if len([]rune(keyword)) > maxKeywordLen {
				continue
			}
			h := KeywordHit{
				KeywordID: kwID, Keyword: keyword,
				MappedTable: odName, MappedField: "",
				Tier: "FUZZY", Score: 0.75, MatchedToken: token,
			}
			hits = append(hits, h)
			lhHits = append(lhHits, lakehouseHit{
				KeywordHit: h, PropertyID: "", PropName: "",
				OdID: odID, OdName: odName,
			})
			if len(hits) >= 5 {
				break
			}
		}
		rows.Close()
	}

	if len(hits) > 0 {
		return hits, lhHits
	}

	// ── Tier 3: VEC (cosine ≥0.85 against keyword_vector ∪ alias_vector) ──
	// Only fires when both text tiers missed AND we have a pre-computed
	// embedding for this token. Mirrors the property-path Tier 3 in
	// searchLakehouseKeywordFull but joins via lk.object_id (Od-alias rows).
	if len(tokVec) == 0 {
		return hits, lhHits
	}
	// recall.vector_search — Od-alias variant of the Tier 3 VEC search.
	// Same token-against-vector cosine query as the property-path, but
	// resolves to Od rows rather than properties. tier label differentiates
	// the two in Jaeger / Prometheus.
	_, vecSpan := observability.Tracer().Start(ctx, "recall.vector_search",
		trace.WithAttributes(
			attribute.Int("batch_size", 1),
			attribute.Int("top_k", 1),
			attribute.String("tier", "od_alias"),
		))
	defer vecSpan.End()
	vecStr := PgVec(tokVec)
	row := db.QueryRow(`
		WITH candidates AS (
		    SELECT lk.id::text AS kw_id, lk.keyword AS canonical,
		           lk.keyword_vector AS vec,
		           o.id::text AS od_id, o.name AS od_name
		      FROM lakehouse_keyword lk
		      JOIN ont_object_type o ON lk.object_id = o.id
		     WHERE lk.project_id = $1
		       AND lk.object_id IS NOT NULL
		       AND lk.property_id IS NULL
		       AND lk.keyword_vector IS NOT NULL
		       AND COALESCE(lk.is_stopword, false) = false
		       AND COALESCE(lk.is_machine_code, false) = false
		       AND COALESCE(o.mark, true) = true
		    UNION ALL
		    SELECT lk.id::text, lk.keyword,
		           la.alias_vector,
		           o.id::text, o.name
		      FROM lakehouse_keyword_alias_vector la
		      JOIN lakehouse_keyword lk ON lk.id = la.keyword_id
		      JOIN ont_object_type o ON lk.object_id = o.id
		     WHERE lk.project_id = $1
		       AND lk.object_id IS NOT NULL
		       AND lk.property_id IS NULL
		       AND la.alias_vector IS NOT NULL
		       AND COALESCE(lk.is_stopword, false) = false
		       AND COALESCE(lk.is_machine_code, false) = false
		       AND COALESCE(o.mark, true) = true
		)
		SELECT kw_id, canonical, od_id, od_name,
		       1 - (vec <=> $2::vector) AS sim
		  FROM candidates
		 WHERE 1 - (vec <=> $2::vector) >= 0.85
		 ORDER BY vec <=> $2::vector ASC
		 LIMIT 1`, projectID, vecStr)

	var (
		kwID, keyword, odID, odName string
		sim                         float64
	)
	if err := row.Scan(&kwID, &keyword, &odID, &odName, &sim); err == nil {
		h := KeywordHit{
			KeywordID: kwID, Keyword: keyword,
			MappedTable: odName, MappedField: "",
			Tier: "VEC", Score: sim, MatchedToken: token,
		}
		hits = append(hits, h)
		lhHits = append(lhHits, lakehouseHit{
			KeywordHit: h, PropertyID: "", PropName: "",
			OdID: odID, OdName: odName,
		})
	}

	return hits, lhHits
}

// resolveLakehouseOdLinks populates link info using ont_causality(join_key)
// instead of ont_link_type (which is for DAX relationships).
func resolveLakehouseOdLinks(db *sql.DB, projectID string, result *RecallResult) {
	odIDs := map[string]string{} // id → name
	for i := range result.OdBlocks {
		odIDs[result.OdBlocks[i].OdID] = result.OdBlocks[i].Name
	}

	// Load join_key causalities that reference Ods in the result.
	rows, err := db.Query(joinKeyCausalitySQL, projectID)
	if err != nil {
		log.Printf("recall_lakehouse: resolveLakehouseOdLinks error: %v", err)
		return
	}
	defer rows.Close()

	// Dedup: track which (odID → targetName) pairs we've already added.
	linkSeen := map[string]bool{} // "odID→targetName"

	for rows.Next() {
		var cardinality, fromOdID, fromOdName, toOdID, toOdName string
		rows.Scan(&cardinality, &fromOdID, &fromOdName, &toOdID, &toOdName)

		// Only add links between Ods in the result.
		_, fromInResult := odIDs[fromOdID]
		_, toInResult := odIDs[toOdID]
		if !fromInResult || !toInResult {
			continue
		}

		for i := range result.OdBlocks {
			if result.OdBlocks[i].OdID == fromOdID {
				key := fromOdID + "→" + toOdName
				if !linkSeen[key] {
					linkSeen[key] = true
					result.OdBlocks[i].Links = append(result.OdBlocks[i].Links,
						OdLink{TargetOdName: toOdName, Cardinality: cardinality})
				}
			}
			if result.OdBlocks[i].OdID == toOdID {
				key := toOdID + "→" + fromOdName
				if !linkSeen[key] {
					linkSeen[key] = true
					result.OdBlocks[i].Links = append(result.OdBlocks[i].Links,
						OdLink{TargetOdName: fromOdName, Cardinality: cardinality})
				}
			}
		}
	}
}

// detectLakehouseAmbiguities checks per-keyword ambiguity for lakehouse recall.
//
// Simplified logic (vs the generic detectAmbiguities):
//  1. Per keyword: if it hits only 1 Od → no ambiguity.
//  2. Per keyword: if it hits N Ods and one is the "one" side via join_key → not ambiguous.
//  3. Per keyword: if it hits N Ods with no join_key relationship → ambiguous.
//  4. No cross-keyword disconnected graph check (Step C removed) — different keywords
//     hitting different Ods is a normal multi-table query; JOIN is the SQL engine's job.
func detectLakehouseAmbiguities(db *sql.DB, projectID string, blocks []OdBlock) []Ambiguity {
	if len(blocks) < 2 {
		return nil
	}

	// ── Step A: collect filter-value hits per keyword ──
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

	// ── Step B: per-keyword check using join_key "one" side ──
	var ambiguities []Ambiguity
	for kw, hits := range keywordHits {
		if len(hits) < 2 {
			continue
		}
		hitIDs := make([]string, 0, len(hits))
		for _, h := range hits {
			hitIDs = append(hitIDs, h.block.OdID)
		}

		if findJoinKeyOneSide(db, projectID, hitIDs) != "" {
			continue // "one" side exists → not ambiguous
		}

		// No join_key relationship → ambiguous
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

	return ambiguities
}

// applyExactDisambiguation reduces ambiguous EXACT-tier hits with a 3-step cascade:
//
//  1. Pin filter — if a token has N>1 hits across Ods AND another single-hit
//     token already pinned a specific Od set, drop candidates outside that set.
//
//  2. Join-key 1-side filter — if step 1 didn't help and the candidate Ods are
//     connected via ont_causality(relation_type='join_key'), keep only the
//     "1" (dimension) side. Convention: from_od is the "1" side.
//
//  3. No change — leave all candidates so the existing ambiguity detector can
//     surface a clarification prompt.
//
// Operates only on EXACT hits. FUZZY and VEC are passed through unchanged.
// allHits is filtered in place; deduped slice is returned.
func applyExactDisambiguation(db *sql.DB, projectID string, allHits []lakehouseHit) []lakehouseHit {
	if len(allHits) < 2 {
		return allHits
	}

	// Build "pinned Ods" set: Ods uniquely identified by some single-hit token
	// (only one entry in allHits with that MatchedToken).
	tokenHitCount := map[string]int{}
	for _, h := range allHits {
		tokenHitCount[h.MatchedToken]++
	}
	pinnedOds := map[string]bool{}
	for _, h := range allHits {
		if tokenHitCount[h.MatchedToken] == 1 {
			pinnedOds[h.OdID] = true
		}
	}

	// Group multi-hit token indices.
	tokenIdx := map[string][]int{}
	for i, h := range allHits {
		tokenIdx[h.MatchedToken] = append(tokenIdx[h.MatchedToken], i)
	}

	keep := make([]bool, len(allHits))
	for i := range keep {
		keep[i] = true
	}

	for _, indices := range tokenIdx {
		if len(indices) <= 1 {
			continue
		}
		// Only act when ALL candidates for this token are EXACT — mixed tiers
		// (rare, since EXACT short-circuits in searchLakehouseKeywordFull) are
		// handled by their own logic.
		allExact := true
		for _, idx := range indices {
			if allHits[idx].Tier != "EXACT" {
				allExact = false
				break
			}
		}
		if !allExact {
			continue
		}

		// Step 1: pin filter via other tokens' singletons.
		var inPinned []int
		for _, idx := range indices {
			if pinnedOds[allHits[idx].OdID] {
				inPinned = append(inPinned, idx)
			}
		}
		if len(inPinned) > 0 && len(inPinned) < len(indices) {
			for _, idx := range indices {
				if !pinnedOds[allHits[idx].OdID] {
					keep[idx] = false
				}
			}
			continue
		}

		// Step 2: join_key 1-side filter when candidate Ods differ.
		odSet := map[string]bool{}
		var odList []string
		for _, idx := range indices {
			od := allHits[idx].OdID
			if !odSet[od] {
				odSet[od] = true
				odList = append(odList, od)
			}
		}
		if len(odList) < 2 {
			continue // same-Od different-properties → keep all
		}
		oneSide := findJoinKeyOneSide(db, projectID, odList)
		if oneSide == "" {
			continue // no join_key edge → keep all (Step 3)
		}
		for _, idx := range indices {
			if allHits[idx].OdID != oneSide {
				keep[idx] = false
			}
		}
	}

	out := allHits[:0]
	for i, h := range allHits {
		if keep[i] {
			out = append(out, h)
		}
	}
	return out
}

// findJoinKeyOneSide checks whether any Od in the set is the "one" side (1:N from)
// of at least one other Od in the set, via ont_causality(join_key).
// Returns the "one" side Od ID if found, "" otherwise.
func findJoinKeyOneSide(db *sql.DB, projectID string, odIDs []string) string {
	if len(odIDs) < 2 {
		return ""
	}

	placeholders := make([]string, len(odIDs))
	args := make([]interface{}, len(odIDs)+1)
	args[0] = projectID
	for i, id := range odIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Find any Od that is the "one" side (from_od with direction 1:N)
	// of another Od in the set.
	query := fmt.Sprintf(`
		SELECT fo.id::text
		FROM ont_causality c
		JOIN ont_knowledge fk ON c.from_knowledge_id = fk.id AND fk.anchor_type = 'property'
		JOIN ont_property fp ON fk.anchor_id = fp.id
		JOIN ont_object_type fo ON fp.object_type_id = fo.id
		JOIN ont_knowledge tk ON c.to_knowledge_id = tk.id AND tk.anchor_type = 'property'
		JOIN ont_property tp ON tk.anchor_id = tp.id
		JOIN ont_object_type to_ ON tp.object_type_id = to_.id
		WHERE c.project_id = $1
		  AND c.relation_type = 'join_key'
		  AND fo.id IN (%s) AND to_.id IN (%s)
		LIMIT 1`, inClause, inClause)

	var oneID string
	if err := db.QueryRow(query, args...).Scan(&oneID); err != nil {
		return ""
	}
	return oneID
}

// fallbackPropertyMatch searches ont_property.name directly for unmatched tokens.
// If a token matches a property name, adds the parent Od to result.OdBlocks with
// the matched property. This catches cases where the user mentions a property name
// (e.g. "order_quantity") that isn't in lakehouse_keyword.
func fallbackPropertyMatch(db *sql.DB, projectID, token string, result *RecallResult) {
	rows, err := db.Query(`
		SELECT p.id::text, p.name, COALESCE(p.data_type,''), COALESCE(p.description,''),
		       o.id::text, o.name, COALESCE(o.kind,''), COALESCE(o.description,'')
		FROM ont_property p
		JOIN ont_object_type o ON o.id = p.object_type_id
		WHERE o.project_id = $1
		  AND COALESCE(o.mark, true) = true
		  AND (LOWER(p.name) = LOWER($2) OR p.name ILIKE '%'||$2||'%')
		  AND LENGTH(p.name) <= LENGTH($2) * 2
		  AND LENGTH($2) <= LENGTH(p.name) * 2
		ORDER BY CASE WHEN LOWER(p.name) = LOWER($2) THEN 0 ELSE 1 END
		LIMIT 3`, projectID, token)
	if err != nil {
		return
	}
	defer rows.Close()

	existingOds := map[string]bool{}
	for _, blk := range result.OdBlocks {
		existingOds[blk.OdID] = true
	}
	for _, blk := range result.DirectOds {
		existingOds[blk.OdID] = true
	}

	for rows.Next() {
		var propID, propName, propDT, propDesc string
		var odID, odName, odKind, odDesc string
		rows.Scan(&propID, &propName, &propDT, &propDesc, &odID, &odName, &odKind, &odDesc)

		kw := KeywordHit{
			Keyword: propName, MappedTable: odName, MappedField: propName,
			Tier: "PROP_NAME", Score: 0.9, MatchedToken: token, IsColumnRef: true,
		}
		result.TokenDetails[token] = append(result.TokenDetails[token], kw)

		if existingOds[odID] {
			// Merge property into existing OdBlock.
			for i := range result.OdBlocks {
				if result.OdBlocks[i].OdID == odID {
					result.OdBlocks[i].MatchedProps = append(result.OdBlocks[i].MatchedProps,
						PropertyMatch{
							PropertyID: propID, Name: propName, DisplayName: propName,
							DataType: propDT, Description: propDesc, ObjectTypeID: odID,
							Keywords: []KeywordHit{kw},
						})
					result.OdBlocks[i].MatchedVia = appendUnique(result.OdBlocks[i].MatchedVia, "property")
					break
				}
			}
			continue
		}

		// New Od block.
		blk := OdBlock{
			OdID: odID, Name: odName, Kind: odKind, Description: odDesc,
			MatchedProps: []PropertyMatch{{
				PropertyID: propID, Name: propName, DisplayName: propName,
				DataType: propDT, Description: propDesc, ObjectTypeID: odID,
				Keywords: []KeywordHit{kw},
			}},
			MatchedVia: []string{"property"},
		}
		blk.AllPropNames, blk.AllPropDescs = loadAllPropNames(db, odID)
		result.OdBlocks = append(result.OdBlocks, blk)
		existingOds[odID] = true
	}
}

// recallMetricIntents looks up lakehouse_keyword rows whose metric_intent_id is
// non-null, joins to lakehouse_metric_intent, and returns a deduped list of
// MetricIntent objects. Each intent records every token that hit it. A token
// hitting an intent is also recorded in matchedTokens so downstream fallback
// paths (Ok, direct Od, property name) do NOT re-process it.
//
// Tier rule: if any token hits an intent via EXACT, tier is "EXACT"; else "FUZZY".
// Order: priority DESC, then first-hit order.
func recallMetricIntents(db *sql.DB, projectID string, tokens []string, matchedTokens map[string]bool) []MetricIntent {
	if len(tokens) == 0 {
		return nil
	}

	intentMap := map[string]*MetricIntent{} // intentID → intent

	// Tier 1: EXACT per token (parallel not needed — simple IN query).
	for _, tok := range tokens {
		if strings.TrimSpace(tok) == "" {
			continue
		}
		hits := lookupIntentsForToken(db, projectID, tok, true)
		for _, mi := range hits {
			if existing, ok := intentMap[mi.IntentID]; ok {
				existing.MatchedTokens = appendUnique(existing.MatchedTokens, tok)
			} else {
				cp := mi
				cp.MatchedTokens = []string{tok}
				cp.Tier = "EXACT"
				intentMap[mi.IntentID] = &cp
			}
			if matchedTokens != nil {
				matchedTokens[tok] = true
			}
		}
	}

	// Tier 2: FUZZY only for tokens that had no EXACT hit anywhere.
	for _, tok := range tokens {
		if strings.TrimSpace(tok) == "" {
			continue
		}
		// Skip if already matched by EXACT (on any intent or via property recall).
		if matchedTokens != nil && matchedTokens[tok] {
			continue
		}
		hits := lookupIntentsForToken(db, projectID, tok, false)
		for _, mi := range hits {
			if existing, ok := intentMap[mi.IntentID]; ok {
				existing.MatchedTokens = appendUnique(existing.MatchedTokens, tok)
			} else {
				cp := mi
				cp.MatchedTokens = []string{tok}
				cp.Tier = "FUZZY"
				intentMap[mi.IntentID] = &cp
			}
			if matchedTokens != nil {
				matchedTokens[tok] = true
			}
		}
	}

	if len(intentMap) == 0 {
		return nil
	}

	// Materialise sorted by priority DESC, then name.
	out := make([]MetricIntent, 0, len(intentMap))
	for _, v := range intentMap {
		out = append(out, *v)
	}
	// Simple insertion sort — list is tiny (≤ a handful).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if out[j].Priority > out[j-1].Priority ||
				(out[j].Priority == out[j-1].Priority && out[j].Name < out[j-1].Name) {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out
}

// lookupIntentsForToken runs one SQL query resolving a single token to
// MetricIntents, either via EXACT keyword match (exact=true) or ILIKE FUZZY.
//
// Match set = lk.keyword ∪ lk.aliases (property-scoped via the keyword row).
func lookupIntentsForToken(db *sql.DB, projectID, token string, exact bool) []MetricIntent {
	var cond string
	if exact {
		cond = `(
		           LOWER(lk.keyword) = LOWER($2)
		        OR EXISTS (
		             SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		             WHERE LOWER(a) = LOWER($2))
		        )`
	} else {
		// FUZZY excludes machine-code keyword rows (mirrors the property/Od
		// FUZZY guards in searchLakehouseKeywordFull / searchLakehouseOdAlias).
		cond = `(
		           lk.keyword ILIKE '%'||$2||'%'
		        OR EXISTS (
		             SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		             WHERE a ILIKE '%'||$2||'%')
		        )
		        AND COALESCE(lk.is_machine_code, false) = false
		        AND LOWER(lk.keyword) != LOWER($2)
		        AND NOT EXISTS (
		             SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		             WHERE LOWER(a) = LOWER($2))`
	}
	q := `
		SELECT DISTINCT
		    mi.id::text, mi.name, COALESCE(mi.display_name,''),
		    mi.object_id::text, COALESCE(o.name,''),
		    COALESCE(mi.canonical_metric,''),
		    COALESCE(mi.canonical_filters::text,'[]'),
		    COALESCE(mi.auto_group_by, '{}'::text[]),
		    COALESCE(mi.pivot_on,''),
		    COALESCE(mi.pivot_values, '{}'::text[]),
		    COALESCE(mi.pivot_total_label,'Total'),
		    COALESCE(mi.pivot_percent_axis,'row'),
		    COALESCE(mi.pivot_percent_scope,'filtered'),
		    COALESCE(mi.pivot_percent_suffix,'占比'),
		    COALESCE(mi.response_template,''),
		    COALESCE(mi.description,''),
		    COALESCE(mi.priority, 0),
		    mi.default_order_by_label, mi.default_order_by_dir, mi.default_limit,
		    COALESCE(mi.parameters::text, '[]')
		FROM lakehouse_keyword lk
		JOIN lakehouse_metric_intent mi ON lk.metric_intent_id = mi.id
		JOIN ont_object_type o ON mi.object_id = o.id
		WHERE lk.project_id = $1
		  AND COALESCE(lk.is_stopword, false) = false
		  AND COALESCE(mi.mark, true) = true
		  AND COALESCE(o.mark, true) = true
		  AND ` + cond + `
		LIMIT 10`

	rows, err := db.Query(q, projectID, token)
	if err != nil {
		log.Printf("recall_lakehouse: lookupIntentsForToken error: %v", err)
		return nil
	}
	defer rows.Close()

	var out []MetricIntent
	for rows.Next() {
		var id, name, dn, objID, objName, metric, filtersJSON, pivotOn, pivotTotalLabel, pivotPercentAxis, pivotPercentScope, pivotPercentSuffix, respTpl, desc, paramsJSON string
		var autoGB, pivotVals pq.StringArray
		var prio int
		var defOrderByLabel, defOrderByDir sql.NullString
		var defLimit sql.NullInt64
		if err := rows.Scan(&id, &name, &dn, &objID, &objName, &metric, &filtersJSON, &autoGB,
			&pivotOn, &pivotVals, &pivotTotalLabel, &pivotPercentAxis, &pivotPercentScope, &pivotPercentSuffix, &respTpl, &desc, &prio,
			&defOrderByLabel, &defOrderByDir, &defLimit, &paramsJSON); err != nil {
			log.Printf("recall_lakehouse: scan intent: %v", err)
			continue
		}
		var filters []FilterSpec
		if filtersJSON != "" {
			if err := json.Unmarshal([]byte(filtersJSON), &filters); err != nil {
				log.Printf("recall_lakehouse: unmarshal canonical_filters %q: %v", filtersJSON, err)
				filters = nil
			}
		}
		var params []MetricIntentParameter
		if paramsJSON != "" {
			if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
				log.Printf("recall_lakehouse: unmarshal parameters %q: %v", paramsJSON, err)
				params = nil
			}
		}
		mi := MetricIntent{
			IntentID:           id,
			Name:               name,
			DisplayName:        dn,
			ObjectID:           objID,
			ObjectName:         objName,
			CanonicalMetric:    metric,
			CanonicalFilters:   filters,
			AutoGroupBy:        []string(autoGB),
			PivotOn:            pivotOn,
			PivotValues:        []string(pivotVals),
			PivotTotalLabel:    pivotTotalLabel,
			PivotPercentAxis:   pivotPercentAxis,
			PivotPercentScope:  pivotPercentScope,
			PivotPercentSuffix: pivotPercentSuffix,
			ResponseTemplate:   respTpl,
			Description:        desc,
			Priority:           prio,
			Parameters:         params,
		}
		if defOrderByLabel.Valid {
			mi.DefaultOrderByLabel = defOrderByLabel.String
		}
		if defOrderByDir.Valid {
			mi.DefaultOrderByDir = defOrderByDir.String
		}
		if defLimit.Valid && defLimit.Int64 > 0 {
			mi.DefaultLimit = int(defLimit.Int64)
		}
		out = append(out, mi)
	}
	return out
}
