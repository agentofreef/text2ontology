package recall

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/observability"

	. "github.com/lakehouse2ontology/httputil"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// VectorCandidate is one row from LakehouseVectorTopN.
//
// Note: this struct is debug-only — used by the token-recall page to show
// "what the vector tier would have considered" for unmatched tokens. It is
// NEVER injected into LLM context. Tier 4 vectorMatch in correction.go uses
// the same UNION but with a 0.85 cutoff and returns only the canonical
// keyword string.
type VectorCandidate struct {
	KeywordID   string  `json:"keywordId"`
	Keyword     string  `json:"keyword"`     // canonical lakehouse_keyword.keyword
	Matched     string  `json:"matched"`     // the actual text whose vector hit (= keyword OR an alias)
	Source      string  `json:"source"`      // "keyword" | "alias"
	Sim         float64 `json:"sim"`         // cosine similarity in [0, 1]
	MappedTable string  `json:"mappedTable"` // Od name
	MappedField string  `json:"mappedField"` // property name
}

// LakehouseVectorTopN returns the top-N nearest candidates from
// lakehouse_keyword.keyword_vector ∪ lakehouse_keyword_alias_vector.alias_vector
// for the given token, regardless of similarity threshold.
//
// Same UNION the correction.vectorMatch (Tier 4) builds, but without the ≥0.85
// cutoff so the user can see "almost matches". Returns nil if the embedding
// service is unavailable or the project has no vectors yet.
func LakehouseVectorTopN(ctx context.Context, db *sql.DB, projectID, token string, n int) []VectorCandidate {
	if n <= 0 {
		n = 5
	}
	_, span := observability.Tracer().Start(ctx, "recall.vector_search",
		trace.WithAttributes(
			attribute.Int("batch_size", 1),
			attribute.Int("top_k", n),
		))
	defer span.End()
	start := time.Now()
	defer func() {
		observability.RecallEmbedDuration.
			WithLabelValues(observability.BatchSizeBucket(1)).
			Observe(float64(time.Since(start).Milliseconds()))
	}()

	vecs, err := llmclient.EmbedTexts(db, []string{token})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil
	}
	vecStr := PgVec(vecs[0])

	rows, err := db.Query(`
		WITH candidates AS (
		    SELECT lk.id::text  AS keyword_id,
		           lk.keyword   AS canonical,
		           lk.keyword   AS matched,
		           'keyword'    AS source,
		           lk.keyword_vector AS vec,
		           COALESCE(p.name,'') AS prop_name,
		           COALESCE(o.name,'') AS od_name
		      FROM lakehouse_keyword lk
		      LEFT JOIN ont_property p ON p.id = lk.property_id
		      LEFT JOIN ont_object_type o ON o.id = lk.object_type_id
		     WHERE lk.project_id = $1 AND lk.keyword_vector IS NOT NULL
		       AND COALESCE(p.is_machine_code, false) = false
		       AND COALESCE(lk.is_machine_code, false) = false
		    UNION ALL
		    SELECT lk.id::text  AS keyword_id,
		           lk.keyword   AS canonical,
		           la.alias     AS matched,
		           'alias'      AS source,
		           la.alias_vector AS vec,
		           COALESCE(p.name,'') AS prop_name,
		           COALESCE(o.name,'') AS od_name
		      FROM lakehouse_keyword_alias_vector la
		      JOIN lakehouse_keyword lk ON lk.id = la.keyword_id
		      LEFT JOIN ont_property p ON p.id = lk.property_id
		      LEFT JOIN ont_object_type o ON o.id = lk.object_type_id
		     WHERE lk.project_id = $1 AND la.alias_vector IS NOT NULL
		       AND COALESCE(p.is_machine_code, false) = false
		       AND COALESCE(lk.is_machine_code, false) = false
		)
		SELECT keyword_id, canonical, matched, source, prop_name, od_name,
		       1 - (vec <=> $2::vector) AS sim
		  FROM candidates
		 ORDER BY vec <=> $2::vector ASC
		 LIMIT $3`,
		projectID, vecStr, n)
	if err != nil {
		log.Printf("LakehouseVectorTopN: query failed: %v", err)
		return nil
	}
	defer rows.Close()

	var out []VectorCandidate
	for rows.Next() {
		var c VectorCandidate
		if err := rows.Scan(&c.KeywordID, &c.Keyword, &c.Matched, &c.Source,
			&c.MappedField, &c.MappedTable, &c.Sim); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}
