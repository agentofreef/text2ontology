package lakehouse

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// LakehouseCorrector implements smartquery.KeywordCorrector against
// the lakehouse_keyword table with a 4-tier cascade:
// Tier 1: exact case-insensitive (property-scoped)
// Tier 2: fuzzy ILIKE with length guard ≤1.5x (property-scoped)
// Tier 3: project-level exact (cross-property, preserved from original)
// Tier 4: vector similarity ≥0.85 (non-MC only)
type LakehouseCorrector struct {
	DB *sql.DB
}

// Correct checks the lakehouse_keyword table for a matching value.
func (c *LakehouseCorrector) Correct(projectID string, prop smartquery.PropertyInfo, userValue string) (string, string) {
	// 0. Check machine-code flag on the property.
	if c.isMachineCode(projectID, prop) {
		return userValue, "machineCode_passthrough"
	}

	// Tier 1: exact case-insensitive match (property-scoped).
	if kw := c.exactMatch(projectID, prop, userValue); kw != "" {
		return kw, "matched"
	}

	// Tier 2: fuzzy ILIKE with length constraint ≤1.5x (property-scoped).
	if kw := c.fuzzyMatch(projectID, prop, userValue); kw != "" {
		return kw, "fuzzy"
	}

	// Tier 3: project-level exact match (cross-property fallback).
	if kw := c.projectExactMatch(projectID, userValue); kw != "" {
		return kw, "project_matched"
	}

	// Tier 4: vector similarity ≥0.85 (non-MC only).
	if kw, sim := c.vectorMatch(prop, userValue); kw != "" {
		return kw, fmt.Sprintf("vector_matched(%.2f)", sim)
	}

	return userValue, "no_match"
}

// isMachineCode checks the is_machine_code flag on the property.
func (c *LakehouseCorrector) isMachineCode(projectID string, prop smartquery.PropertyInfo) bool {
	var isMC bool
	c.DB.QueryRow(`
		SELECT COALESCE(p.is_machine_code, false)
		FROM ont_property p
		JOIN ont_object_type ot ON ot.id = p.object_type_id
		WHERE p.name = $1 AND ot.name = $2 AND ot.project_id = $3
		LIMIT 1`, prop.Name, prop.ObjectName, projectID).Scan(&isMC)
	return isMC
}

// exactMatch: Tier 1 — case-insensitive full-word match, property-scoped.
// Match set = lk.keyword ∪ lk.aliases. Returns canonical lk.keyword on hit, so
// downstream SQL always sees the regular value (e.g. alias "X11代" → "X11").
func (c *LakehouseCorrector) exactMatch(projectID string, prop smartquery.PropertyInfo, userValue string) string {
	var corrected string
	c.DB.QueryRow(`
		SELECT lk.keyword FROM lakehouse_keyword lk
		JOIN ont_property p ON lk.property_id = p.id
		JOIN ont_object_type o ON p.object_type_id = o.id
		WHERE lk.project_id = $1
		  AND o.name = $2
		  AND p.name = $3
		  AND (
		        LOWER(lk.keyword) = LOWER($4)
		     OR EXISTS (
		          SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		          WHERE LOWER(a) = LOWER($4))
		      )
		LIMIT 1`, projectID, prop.ObjectName, prop.Name, userValue).Scan(&corrected)
	return corrected
}

// fuzzyMatch: Tier 2 — ILIKE substring with length constraint ≤1.5x input length.
// Match set = lk.keyword ∪ lk.aliases (length guard applied to whichever side
// matched: keyword length OR shortest matching alias length).
func (c *LakehouseCorrector) fuzzyMatch(projectID string, prop smartquery.PropertyInfo, userValue string) string {
	var corrected string
	c.DB.QueryRow(`
		SELECT lk.keyword FROM lakehouse_keyword lk
		JOIN ont_property p ON lk.property_id = p.id
		JOIN ont_object_type o ON p.object_type_id = o.id
		WHERE lk.project_id = $1
		  AND o.name = $2
		  AND p.name = $3
		  AND (
		        (lk.keyword ILIKE '%' || $4 || '%' AND LENGTH(lk.keyword) <= LENGTH($4) * 3 / 2 + 1)
		     OR EXISTS (
		          SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		          WHERE a ILIKE '%' || $4 || '%' AND LENGTH(a) <= LENGTH($4) * 3 / 2 + 1)
		      )
		ORDER BY LENGTH(lk.keyword) ASC
		LIMIT 1`, projectID, prop.ObjectName, prop.Name, userValue).Scan(&corrected)
	return corrected
}

// projectExactMatch: Tier 3 — project-level exact match across all properties.
// Match set = lk.keyword ∪ lk.aliases.
func (c *LakehouseCorrector) projectExactMatch(projectID string, userValue string) string {
	var corrected string
	c.DB.QueryRow(`
		SELECT lk.keyword FROM lakehouse_keyword lk
		WHERE lk.project_id = $1
		  AND (
		        LOWER(lk.keyword) = LOWER($2)
		     OR EXISTS (
		          SELECT 1 FROM unnest(COALESCE(lk.aliases, '{}'::text[])) a
		          WHERE LOWER(a) = LOWER($2))
		      )
		LIMIT 1`, projectID, userValue).Scan(&corrected)
	return corrected
}

// vectorMatch: Tier 4 — embedding similarity ≥0.85, non-MC properties only.
//
// Candidate set = keyword's own vector (lakehouse_keyword.keyword_vector)
// UNION the per-alias vectors (lakehouse_keyword_alias_vector.alias_vector).
// Whichever vector is closest wins, but the returned value is always the
// canonical lk.keyword — alias hits get rewritten to the regular value.
//
// Uses sub-select to resolve property_id from (prop.Name, prop.ObjectID).
// Gracefully returns empty on embedding failure.
func (c *LakehouseCorrector) vectorMatch(prop smartquery.PropertyInfo, userValue string) (string, float64) {
	// Generate embedding for the user value.
	vecs, err := llmclient.EmbedTexts(c.DB, []string{userValue})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		return "", 0 // graceful fallback: skip vector tier on embedding failure
	}
	vecStr := httputil.PgVec(vecs[0])

	var keyword string
	var sim float64
	// Resolve property_id once via sub-select; UNION ALL the keyword vector and
	// every alias vector for that property. ORDER BY raw distance ASC then take
	// the closest single row.
	err = c.DB.QueryRow(`
		WITH prop AS (
		    SELECT id FROM ont_property WHERE name = $2 AND object_type_id = $3 LIMIT 1
		),
		candidates AS (
		    SELECT lk.keyword, lk.keyword_vector AS vec
		      FROM lakehouse_keyword lk
		     WHERE lk.property_id = (SELECT id FROM prop)
		       AND lk.keyword_vector IS NOT NULL
		    UNION ALL
		    SELECT lk.keyword, la.alias_vector AS vec
		      FROM lakehouse_keyword lk
		      JOIN lakehouse_keyword_alias_vector la ON la.keyword_id = lk.id
		     WHERE lk.property_id = (SELECT id FROM prop)
		       AND la.alias_vector IS NOT NULL
		)
		SELECT keyword, 1 - (vec <=> $1::vector) AS sim
		  FROM candidates
		 WHERE 1 - (vec <=> $1::vector) >= 0.85
		 ORDER BY vec <=> $1::vector ASC
		 LIMIT 1`, vecStr, prop.Name, prop.ObjectID).Scan(&keyword, &sim)

	if err != nil || keyword == "" {
		return "", 0
	}
	// Normalize: don't replace if the vector match is the same as user input
	if strings.EqualFold(keyword, userValue) {
		return keyword, sim
	}
	return keyword, sim
}
