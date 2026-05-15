// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"database/sql"
	"fmt"

	"github.com/lib/pq"
)

// OntologyStats reports how many ontology rows were inserted.
type OntologyStats struct {
	ObjectCount   int
	PropertyCount int
	LinkCount     int
	MetricCount   int
}

// PopulateOntology inserts all PBIT-derived ontology rows inside the supplied
// terminal transaction.  The transaction must already be open; the caller
// commits or rolls back.
//
// Column names are taken verbatim from docs/schema/schema.sql:
//
//	ont_object_type : id, project_id, name, display_name, kind,
//	                  description, source_table, source_config, mark,
//	                  source_type (ALTER ADD), origin (ALTER ADD)
//	ont_property    : id, project_id, object_type_id, name, display_name,
//	                  data_type, source_column, is_filterable, is_groupable, mark
//	ont_link_type   : id, project_id, from_object_id, to_object_id,
//	                  link_name, fk_column, cardinality, mark
//	ont_metric      : id, project_id, name, display_name,
//	                  metric_type, mark
func PopulateOntology(
	tx *sql.Tx,
	projectID, schema string,
	pbit *PbitSchema,
	derived []DerivedResult,
) (OntologyStats, error) {
	var stats OntologyStats

	// Build a lookup: tableName → ont_object_type UUID, needed for links.
	objectIDs := make(map[string]string, len(pbit.Tables))

	// --- ont_object_type + ont_property ---
	for _, t := range pbit.Tables {
		// Determine origin: "derived-view" if this table appears in derived results.
		origin := "pbit-bootstrap"
		for _, dr := range derived {
			if dr.ViewName == t.Name {
				origin = "derived-view"
				break
			}
		}

		sourceTable := schema + "." + t.Name

		var objID string
		err := tx.QueryRow(`
			INSERT INTO ont_object_type
			  (project_id, name, display_name, kind,
			   source_table, mark, source_type, origin)
			VALUES
			  ($1, $2, $3, 'entity',
			   $4, false, 'csv', $5)
			ON CONFLICT (project_id, name) DO UPDATE
			  SET source_table = EXCLUDED.source_table,
			      source_type  = EXCLUDED.source_type,
			      origin       = EXCLUDED.origin
			RETURNING id`,
			projectID, t.Name, t.Name,
			sourceTable, origin,
		).Scan(&objID)
		if err != nil {
			return stats, fmt.Errorf("pbitlakehouse: insert ont_object_type %q: %w", t.Name, err)
		}
		objectIDs[t.Name] = objID
		stats.ObjectCount++

		// Insert properties for non-calculated columns.
		for _, col := range t.Columns {
			if col.Expression != "" {
				continue // skip calculated columns
			}
			sqlType := pbitTypeToSQL(col.DataType)
			_, err := tx.Exec(`
				INSERT INTO ont_property
				  (project_id, object_type_id, name, display_name,
				   data_type, source_column, is_filterable, is_groupable, mark)
				VALUES
				  ($1, $2, $3, $4, $5, $6, true, true, false)
				ON CONFLICT (object_type_id, name) DO UPDATE
				  SET data_type    = CASE
				        WHEN 'data_type' = ANY(ont_property.user_edited_fields)
				          THEN ont_property.data_type
				        ELSE EXCLUDED.data_type
				      END,
				      source_column = EXCLUDED.source_column`,
				projectID, objID, col.Name, col.Name,
				sqlType, col.Name,
			)
			if err != nil {
				return stats, fmt.Errorf("pbitlakehouse: insert ont_property %q.%q: %w", t.Name, col.Name, err)
			}
			stats.PropertyCount++
		}
	}

	// --- ont_link_type ---
	for _, rel := range pbit.Relationships {
		fromID, ok1 := objectIDs[rel.FromTable]
		toID, ok2 := objectIDs[rel.ToTable]
		if !ok1 || !ok2 {
			// Referenced table not in schema — skip gracefully.
			continue
		}

		cardinality := mapCardinality(rel.FromCardinality, rel.ToCardinality)

		// crossFilteringBehavior='BothDirections' → mark=true (is_active implied via mark)
		mark := rel.IsActive || rel.CrossFilteringBehavior == "BothDirections"

		linkName := rel.FromTable + "_" + rel.ToTable
		if rel.Name != "" {
			linkName = rel.Name
		}

		_, err := tx.Exec(`
			INSERT INTO ont_link_type
			  (project_id, from_object_id, to_object_id,
			   link_name, fk_column, cardinality, mark)
			VALUES
			  ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT DO NOTHING`,
			projectID, fromID, toID,
			linkName, rel.FromColumn, cardinality, mark,
		)
		if err != nil {
			return stats, fmt.Errorf("pbitlakehouse: insert ont_link_type %q: %w", linkName, err)
		}
		stats.LinkCount++
	}

	// --- ont_metric ---
	for _, t := range pbit.Tables {
		for _, m := range t.Measures {
			_, err := tx.Exec(`
				INSERT INTO ont_metric
				  (project_id, name, display_name,
				   metric_type, mark)
				VALUES
				  ($1, $2, $3, 'simple', false)
				ON CONFLICT DO NOTHING`,
				projectID, m.Name, m.Name,
			)
			if err != nil {
				return stats, fmt.Errorf("pbitlakehouse: insert ont_metric %q: %w", m.Name, err)
			}
			stats.MetricCount++
		}
	}

	// --- activate mark + seed lakehouse_keyword ---
	if err := activateAndSeedKeywords(tx, projectID); err != nil {
		return stats, fmt.Errorf("pbitlakehouse: activate+seed: %w", err)
	}

	// --- lakehouse_derived_view catalog rows ---
	for _, dr := range derived {
		kindStr := kindToString(dr.Kind)
		baseTables := pq.Array(dr.BaseTables)
		if dr.BaseTables == nil {
			baseTables = pq.Array([]string{})
		}

		_, err := tx.Exec(`
			INSERT INTO lakehouse_derived_view
			  (project_id, pg_schema, view_name, m_expression,
			   base_tables, kind, warning)
			VALUES
			  ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (project_id, view_name) DO UPDATE
			  SET m_expression = EXCLUDED.m_expression,
			      base_tables  = EXCLUDED.base_tables,
			      kind         = EXCLUDED.kind,
			      warning      = EXCLUDED.warning`,
			projectID, schema, dr.ViewName, dr.MExpression,
			baseTables, kindStr, nullableString(dr.Warning),
		)
		if err != nil {
			return stats, fmt.Errorf("pbitlakehouse: insert lakehouse_derived_view %q: %w", dr.ViewName, err)
		}
	}

	return stats, nil
}

// activateAndSeedKeywords is called at the end of PopulateOntology to:
//  1. Flip mark=true on every ont_object_type / ont_property row that was just
//     inserted with mark=false (recall-server filters COALESCE(mark,true)=true).
//  2. Insert a default lakehouse_keyword row for each Od and each property that
//     does not already have one — so recall can match user queries immediately
//     after import without a manual keyword-sync step.
//
// All INSERT statements are guarded by NOT EXISTS so the function is idempotent;
// re-running never overwrites keywords the user has customised.
func activateAndSeedKeywords(tx *sql.Tx, projectID string) error {
	// 1. Activate ont_object_type rows.
	if _, err := tx.Exec(`
		UPDATE ont_object_type SET mark = true
		WHERE project_id = $1 AND COALESCE(mark, false) = false
	`, projectID); err != nil {
		return fmt.Errorf("activate ont_object_type: %w", err)
	}

	// 2. Activate ont_property rows.
	if _, err := tx.Exec(`
		UPDATE ont_property SET mark = true
		WHERE project_id = $1 AND COALESCE(mark, false) = false
	`, projectID); err != nil {
		return fmt.Errorf("activate ont_property: %w", err)
	}

	// 3. Seed Od-level keywords (one per object type, property_id NULL).
	if _, err := tx.Exec(`
		INSERT INTO lakehouse_keyword
			(project_id, object_type_id, object_id, property_id,
			 keyword, is_column_name, is_machine_code, is_stopword,
			 synced_at, updated_at)
		SELECT o.project_id, o.id, o.id, NULL,
		       LOWER(o.name), false, false, false,
		       now(), now()
		FROM ont_object_type o
		WHERE o.project_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM lakehouse_keyword k
		      WHERE k.object_type_id = o.id
		        AND k.property_id IS NULL
		        AND k.metric_intent_id IS NULL
		        AND k.is_stopword = false
		  )
	`, projectID); err != nil {
		return fmt.Errorf("seed Od keywords: %w", err)
	}

	// 4. Seed property-level keywords (one per property).
	if _, err := tx.Exec(`
		INSERT INTO lakehouse_keyword
			(project_id, object_type_id, property_id,
			 keyword, is_column_name, is_machine_code, is_stopword,
			 synced_at, updated_at)
		SELECT p.project_id, p.object_type_id, p.id,
		       LOWER(p.name), true, false, false,
		       now(), now()
		FROM ont_property p
		WHERE p.project_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM lakehouse_keyword k
		      WHERE k.property_id = p.id
		  )
	`, projectID); err != nil {
		return fmt.Errorf("seed property keywords: %w", err)
	}

	return nil
}

// mapCardinality converts PBIT fromCardinality/toCardinality strings to a
// canonical cardinality label used in ont_link_type.cardinality.
// PBIT uses "many" / "one"; fall back to "M:M" if unknown.
func mapCardinality(from, to string) string {
	f := normCard(from)
	t := normCard(to)
	switch {
	case f == "M" && t == "1":
		return "M:1"
	case f == "1" && t == "M":
		return "1:M"
	case f == "1" && t == "1":
		return "1:1"
	default:
		return "M:M"
	}
}

func normCard(s string) string {
	switch s {
	case "many", "Many", "m", "*":
		return "M"
	case "one", "One", "1":
		return "1"
	default:
		return "M"
	}
}

func kindToString(k PartitionKind) string {
	switch k {
	case KindCombine:
		return "combine"
	case KindConstantCsv:
		return "constant"
	case KindUnpivot:
		return "unpivot"
	default:
		return "unsupported"
	}
}

// nullableString returns nil if s is empty, otherwise the string value.
// Used to pass optional warning strings to Postgres.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
