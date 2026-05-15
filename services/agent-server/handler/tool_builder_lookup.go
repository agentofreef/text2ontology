// Builder lookup tools — read-only inspectors used by the OD Builder Agent
// during the interview phase. See plan Step 1.3 (US-002).
//
// builderToolDescribeTable and builderToolListExistingOds have been removed —
// they are superseded by builderToolAnalyzeTable (tool_builder_analyze.go) and
// builderToolListOds (tool_builder_list.go) respectively.
package handler

import (
	"database/sql"
	"fmt"

	. "github.com/lakehouse2ontology/httputil"
)

// builderToolListLakehouseTables returns physical tables under the project's
// lakehouse_schema. Used by the AI to confirm which staging table backs the
// OD being designed. Cap at 100 tables to bound output.
//
// Truncation contract: result.totalTableCount is always the FULL count from
// information_schema; result.tables[] is capped at limit. result.truncated
// is true when totalTableCount > limit, so the LLM can see "I returned 100
// of N tables" and decide to drill deeper or ask the user to narrow the
// candidate table set.
func builderToolListLakehouseTables(db *sql.DB, projectID string) M {
	const limit = 100
	var schema string
	if err := db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`, projectID).Scan(&schema); err != nil {
		return M{"error": "failed to read project lakehouse_schema: " + err.Error()}
	}
	if schema == "" {
		return M{"error": "project has no lakehouse_schema configured"}
	}

	// Total count first so we can report truncation honestly.
	var total int
	_ = db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = $1`, schema).Scan(&total)

	rows, err := db.Query(`
		SELECT table_name, table_type
		FROM information_schema.tables
		WHERE table_schema = $1
		ORDER BY table_name
		LIMIT $2`, schema, limit)
	if err != nil {
		return M{"error": "list tables failed: " + err.Error()}
	}
	defer rows.Close()

	var tables []M
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			continue
		}
		// reltuples is a planner estimate; cast to bigint and fall back to 0
		// on error or negative values (planner returns -1 before first ANALYZE).
		var rowCount int64
		_ = db.QueryRow(`
			SELECT GREATEST(c.reltuples::bigint, 0)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&rowCount)

		tables = append(tables, M{
			"name":          name,
			"type":          kind, // BASE TABLE | VIEW | etc.
			"estimatedRows": rowCount,
		})
	}
	if tables == nil {
		tables = []M{}
	}
	out := M{
		"tables":          tables,
		"returnedCount":   len(tables),
		"totalTableCount": total,
		"limit":           limit,
		"truncated":       total > limit,
	}
	if total > limit {
		out["truncationNote"] = fmt.Sprintf("仅返回前 %d 张表（按表名升序），实际总数 %d。如需查看其他表，请告知用户表名提示后用 inspect(mode=schema, table=…) 直接定位。", limit, total)
	}
	return out
}

