package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/sqlrewrite"
)

// =========================== Handlers ===========================

// POST /api/ontology/sql-passthrough — execute ontology-level SQL
// User writes queries against Od names as tables; backend transparently
// rewrites to physical SQL by injecting canonical_query as CTEs.
func handleSQLPassthrough(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "POST only"})
			return
		}

		body := ReadBody(r)
		userSQL := strings.TrimSpace(StrVal(body, "sql"))
		projectID := StrVal(body, "projectId")
		rowLimit := 10000
		if v, ok := body["rowLimit"].(float64); ok && int(v) > 0 && int(v) < 50000 {
			rowLimit = int(v)
		}

		if userSQL == "" || projectID == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "sql and projectId required"})
			return
		}
		if !authmw.EnforceProjectFromRequest(w, r, db, projectID) {
			return
		}

		// Load project Od map: name (lowercased) → canonical_query
		odMap, odNames, err := loadProjectOdCanonical(db, projectID)
		if err != nil {
			JsonResp(w, M{"error": err.Error()})
			return
		}
		if len(odMap) == 0 {
			JsonResp(w, M{"error": "当前项目尚未配置任何 Ontology 对象（或对象未完成 canonical_query 固化）"})
			return
		}

		// Reject DDL (single source of truth: pkg/sqlrewrite).
		if err := sqlrewrite.RejectDDL(userSQL); err != nil {
			_ = logPassthrough(db, projectID, userSQL, 0, 0, err.Error())
			JsonResp(w, M{"error": err.Error(), "blocked": true})
			return
		}

		// Extract referenced "tables" from FROM/JOIN; validate they are all Ods.
		refs := sqlrewrite.ExtractReferencedNames(userSQL)
		var unknown []string
		var used []string
		seen := map[string]bool{}
		for _, r := range refs {
			lr := strings.ToLower(r)
			if _, ok := odMap[lr]; !ok {
				unknown = append(unknown, r)
				continue
			}
			if !seen[lr] {
				seen[lr] = true
				used = append(used, lr)
			}
		}
		if len(unknown) > 0 {
			errMsg := fmt.Sprintf("未知的 Ontology 对象: %s。可用对象: %s",
				strings.Join(unknown, ", "), strings.Join(odNames, ", "))
			_ = logPassthrough(db, projectID, userSQL, 0, 0, errMsg)
			JsonResp(w, M{"error": errMsg, "blocked": true})
			return
		}
		if len(used) == 0 {
			errMsg := "查询中未引用任何 Ontology 对象（FROM/JOIN 至少需要一个 Od 名）"
			_ = logPassthrough(db, projectID, userSQL, 0, 0, errMsg)
			JsonResp(w, M{"error": errMsg, "blocked": true})
			return
		}

		// Build WITH clause injecting canonical_queries for used Ods
		rewritten := buildCTEPrefix(odMap, used) + "\n" + userSQL
		rewritten = sqlrewrite.MaybeInjectLimit(rewritten, rowLimit)

		// Look up project lakehouse schema (same pattern as smartquery executor)
		var lakehouseSchema string
		db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`, projectID).Scan(&lakehouseSchema)

		start := time.Now()
		cols, rows, rowCount, execErr := executePassthrough(db, rewritten, lakehouseSchema)
		duration := int(time.Since(start).Milliseconds())

		_ = logPassthrough(db, projectID, userSQL, rowCount, duration, stringOr(execErr))

		resp := M{
			"durationMs":   duration,
			"rowCount":     rowCount,
			"rewrittenSql": rewritten, // for debug / transparency toggle
			"usedObjects":  used,
		}
		if execErr != nil {
			resp["error"] = sanitizeError(execErr.Error(), odMap)
		} else {
			resp["columns"] = cols
			resp["rows"] = rows
		}
		JsonResp(w, resp)
	}
}

// GET /api/ontology/sql-passthrough/schema?projectId=... — Ontology-level schema
// Returns Ods with their properties and join-key relationships.
func handleSQLPassthroughSchema(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		projectID := GetProjectID(r)
		if projectID == "" {
			ListResp(w, []M{}, 0)
			return
		}

		// Load Ods
		odRows, err := db.Query(`SELECT o.id::text, o.name, COALESCE(o.kind,''), COALESCE(o.description,''),
			COALESCE(o.canonical_query,'') <> '' AS has_canonical
			FROM ont_object_type o
			WHERE o.project_id = $1 AND COALESCE(o.mark, true) = true
			ORDER BY o.name`, projectID)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer odRows.Close()
		type odMeta struct {
			ID, Name, Kind, Description string
			HasCanonical                bool
		}
		var ods []odMeta
		odByID := map[string]*odMeta{}
		for odRows.Next() {
			var o odMeta
			odRows.Scan(&o.ID, &o.Name, &o.Kind, &o.Description, &o.HasCanonical)
			ods = append(ods, o)
		}
		for i := range ods {
			odByID[ods[i].ID] = &ods[i]
		}

		// Load properties for these Ods
		propRows, err := db.Query(`SELECT p.id::text, p.object_type_id::text, p.name,
			COALESCE(p.data_type,''), COALESCE(p.description,''),
			COALESCE(p.is_machine_code,false)
			FROM ont_property p
			JOIN ont_object_type o ON o.id = p.object_type_id
			WHERE o.project_id = $1 AND COALESCE(o.mark,true) = true
			ORDER BY p.name`, projectID)
		type propMeta struct {
			ID, OdID, Name, DataType, Description string
			IsMC                                  bool
		}
		propsByOd := map[string][]propMeta{}
		propByID := map[string]*propMeta{}
		if err == nil {
			defer propRows.Close()
			for propRows.Next() {
				var p propMeta
				propRows.Scan(&p.ID, &p.OdID, &p.Name, &p.DataType, &p.Description, &p.IsMC)
				propsByOd[p.OdID] = append(propsByOd[p.OdID], p)
			}
			for odID, list := range propsByOd {
				for i := range list {
					propByID[list[i].ID] = &list[i]
				}
				_ = odID
			}
		}

		// Load join-key relationships. Causality rows link Ok entries; each Ok entries' anchor is a property.
		type joinLink struct {
			FromOdID, FromOdName, FromPropName string
			ToOdID, ToOdName, ToPropName       string
			Direction                          string
		}
		var links []joinLink
		joinRows, err := db.Query(`SELECT c.direction,
			fo.id::text, fo.name, fp.name,
			to_.id::text, to_.name, tp.name
			FROM ont_causality c
			JOIN ont_knowledge fk ON c.from_knowledge_id = fk.id AND fk.anchor_type = 'property'
			JOIN ont_property fp ON fk.anchor_id = fp.id
			JOIN ont_object_type fo ON fp.object_type_id = fo.id
			JOIN ont_knowledge tk ON c.to_knowledge_id = tk.id AND tk.anchor_type = 'property'
			JOIN ont_property tp ON tk.anchor_id = tp.id
			JOIN ont_object_type to_ ON tp.object_type_id = to_.id
			WHERE c.project_id = $1 AND c.relation_type = 'join_key'
			  AND fo.project_id = $1 AND to_.project_id = $1`, projectID)
		if err == nil {
			defer joinRows.Close()
			for joinRows.Next() {
				var l joinLink
				joinRows.Scan(&l.Direction, &l.FromOdID, &l.FromOdName, &l.FromPropName,
					&l.ToOdID, &l.ToOdName, &l.ToPropName)
				links = append(links, l)
			}
		}

		// Group links by Od (bidirectional view)
		linksByOd := map[string][]M{}
		for _, l := range links {
			// from → to
			linksByOd[l.FromOdID] = append(linksByOd[l.FromOdID], M{
				"targetOd":    l.ToOdName,
				"fromProp":    l.FromPropName,
				"toProp":      l.ToPropName,
				"cardinality": l.Direction,
			})
			// to → from (reverse view, invert cardinality if possible)
			linksByOd[l.ToOdID] = append(linksByOd[l.ToOdID], M{
				"targetOd":    l.FromOdName,
				"fromProp":    l.ToPropName,
				"toProp":      l.FromPropName,
				"cardinality": invertCardinality(l.Direction),
			})
		}

		var out []M
		for _, o := range ods {
			var propList []M
			for _, p := range propsByOd[o.ID] {
				propList = append(propList, M{
					"name":          p.Name,
					"dataType":      p.DataType,
					"description":   p.Description,
					"isMachineCode": p.IsMC,
				})
			}
			if propList == nil {
				propList = []M{}
			}
			linksList := linksByOd[o.ID]
			if linksList == nil {
				linksList = []M{}
			}
			out = append(out, M{
				"name":         o.Name,
				"kind":         o.Kind,
				"description":  o.Description,
				"hasCanonical": o.HasCanonical,
				"properties":   propList,
				"links":        linksList,
			})
		}
		if out == nil {
			out = []M{}
		}
		ListResp(w, out, len(out))
	}
}

// GET /api/ontology/sql-passthrough/history?projectId=...
// DELETE /api/ontology/sql-passthrough/history?id=...
func handleSQLPassthroughHistory(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method == http.MethodDelete {
			id := r.URL.Query().Get("id")
			if id != "" {
				// Cross-project IDOR guard: confirm the caller can access the
				// log row's project before deleting it by id.
				if !authmw.EnforceEntityProject(w, r, db, "ont_sql_passthrough_log", "id", id) {
					return
				}
				db.Exec(`DELETE FROM ont_sql_passthrough_log WHERE id = $1`, id)
			}
			JsonResp(w, M{"success": true})
			return
		}
		projectID := GetProjectID(r)
		if projectID == "" {
			ListResp(w, []M{}, 0)
			return
		}
		rows, err := db.Query(`SELECT id, sql_text, row_count, duration_ms,
			COALESCE(error,''), created_at
			FROM ont_sql_passthrough_log
			WHERE project_id = $1 ORDER BY created_at DESC LIMIT 50`, projectID)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()
		var list []M
		for rows.Next() {
			var id, sqlText, errStr string
			var rowCount, duration int
			var createdAt time.Time
			rows.Scan(&id, &sqlText, &rowCount, &duration, &errStr, &createdAt)
			list = append(list, M{
				"id": id, "sql": sqlText,
				"rowCount": rowCount, "durationMs": duration,
				"error": errStr, "createdAt": createdAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

// GET|POST /api/ontology/sql-passthrough/snippets
func handleSQLPassthroughSnippets(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		projectID := GetProjectID(r)
		switch r.Method {
		case http.MethodGet:
			if projectID == "" {
				ListResp(w, []M{}, 0)
				return
			}
			rows, _ := db.Query(`SELECT id, name, sql_text, COALESCE(description,''), created_at
				FROM ont_sql_passthrough_snippet WHERE project_id = $1 ORDER BY name`, projectID)
			var list []M
			if rows != nil {
				for rows.Next() {
					var id, name, sqlText, desc string
					var createdAt time.Time
					rows.Scan(&id, &name, &sqlText, &desc, &createdAt)
					list = append(list, M{
						"id": id, "name": name, "sql": sqlText,
						"description": desc, "createdAt": createdAt.Format(time.RFC3339),
					})
				}
				rows.Close()
			}
			if list == nil {
				list = []M{}
			}
			ListResp(w, list, len(list))
		case http.MethodPost:
			body := ReadBody(r)
			projID := StrVal(body, "projectId")
			if projID == "" {
				projID = projectID
			}
			name := strings.TrimSpace(StrVal(body, "name"))
			sqlText := strings.TrimSpace(StrVal(body, "sql"))
			desc := StrVal(body, "description")
			if projID == "" || name == "" || sqlText == "" {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "projectId, name, sql required"})
				return
			}
			// Body projectId bypasses the middleware query gate; verify access.
			if !authmw.EnforceProjectAccess(w, r, db, projID) {
				return
			}
			var id string
			err := db.QueryRow(`INSERT INTO ont_sql_passthrough_snippet
				(project_id, name, sql_text, description) VALUES ($1, $2, $3, $4) RETURNING id`,
				projID, name, sqlText, desc).Scan(&id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id})
		}
	}
}

// DELETE /api/ontology/sql-passthrough/snippets/{id}
func handleSQLPassthroughSnippetByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/ontology/sql-passthrough/snippets")
		// Cross-project IDOR guard: verify project access before touching this snippet.
		if !authmw.EnforceEntityProject(w, r, db, "ont_sql_passthrough_snippet", "id", id) {
			return
		}
		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_sql_passthrough_snippet WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}
		http.NotFound(w, r)
	}
}

// =========================== Core logic ===========================

// odInfo holds canonical_query and original-case name for an Od.
type odInfo struct {
	Name      string // original case
	Canonical string
}

// loadProjectOdCanonical returns a map from lowercased Od name → odInfo,
// along with a display-order list of original-cased Od names.
func loadProjectOdCanonical(db *sql.DB, projectID string) (map[string]odInfo, []string, error) {
	rows, err := db.Query(`SELECT o.name, COALESCE(o.canonical_query,'')
		FROM ont_object_type o
		WHERE o.project_id = $1 AND COALESCE(o.mark,true) = true
		ORDER BY o.name`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	m := map[string]odInfo{}
	var names []string
	for rows.Next() {
		var n, cq string
		rows.Scan(&n, &cq)
		if cq == "" {
			continue
		} // exclude Ods without canonical_query
		m[strings.ToLower(n)] = odInfo{Name: n, Canonical: cq}
		names = append(names, n)
	}
	sort.Strings(names)
	return m, names, nil
}

// buildCTEPrefix builds `WITH "Od1" AS (cq1), "Od2" AS (cq2)` using the ORIGINAL
// Od names so quoted identifier case matches the user's SQL. Thin adapter over
// the shared sqlrewrite.BuildCTEPrefix: this handler keys its odMap by lowercased
// name → odInfo{Name(original), Canonical}, while the shared builder wants
// originalName → canonical, so we project before delegating.
func buildCTEPrefix(odMap map[string]odInfo, usedLower []string) string {
	canonical := make(map[string]string, len(usedLower))
	used := make([]string, 0, len(usedLower))
	for _, lname := range usedLower {
		info := odMap[lname]
		canonical[info.Name] = info.Canonical
		used = append(used, info.Name)
	}
	return sqlrewrite.BuildCTEPrefix(canonical, used)
}

func executePassthrough(db *sql.DB, sqlText, lakehouseSchema string) ([]M, [][]interface{}, int, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	_, _ = tx.Exec(`SET LOCAL statement_timeout = '30s'`)
	_, _ = tx.Exec(`SET LOCAL transaction_read_only = on`)
	// Set search_path so canonical_query's unqualified table names resolve
	// against the project's lakehouse schema (same as smartquery executor).
	if lakehouseSchema != "" {
		if _, err := tx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(lakehouseSchema))); err != nil {
			return nil, nil, 0, fmt.Errorf("search_path 设置失败: %v", err)
		}
	}

	rows, err := tx.Query(sqlText)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, 0, err
	}

	cols := make([]M, len(colTypes))
	for i, ct := range colTypes {
		cols[i] = M{"name": ct.Name(), "type": ct.DatabaseTypeName()}
	}

	var out [][]interface{}
	count := 0
	for rows.Next() {
		vals := make([]interface{}, len(colTypes))
		ptrs := make([]interface{}, len(colTypes))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return cols, out, count, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
		count++
	}
	return cols, out, count, nil
}

func logPassthrough(db *sql.DB, projectID, sqlText string, rowCount, durationMs int, errStr string) error {
	var pid interface{}
	if projectID != "" {
		pid = projectID
	} else {
		pid = nil
	}
	_, err := db.Exec(`INSERT INTO ont_sql_passthrough_log
		(project_id, sql_text, mode, row_count, duration_ms, error)
		VALUES ($1, $2, 'readonly', $3, $4, $5)`,
		pid, sqlText, rowCount, durationMs, errStr)
	return err
}

func stringOr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// sanitizeError strips any PG-internal table names leaking through (e.g. from canonical_query inlining)
// so the user never sees ont_* / lakehouse_* / proj_* table references in error messages.
func sanitizeError(msg string, _ map[string]odInfo) string {
	// Best-effort: replace references to schema-qualified tables with "<ontology-internal>"
	re := regexp.MustCompile(`(?i)"?(ont_[a-z_]+|lakehouse_[a-z_]+|proj_[a-z0-9_]+)"?`)
	return re.ReplaceAllString(msg, "<internal>")
}

// invertCardinality swaps the direction string (e.g. "1:N" → "N:1").
func invertCardinality(d string) string {
	switch strings.ToUpper(strings.ReplaceAll(d, " ", "")) {
	case "1:N":
		return "N:1"
	case "N:1":
		return "1:N"
	case "1:1":
		return "1:1"
	case "N:N", "N:M", "M:N":
		return "N:N"
	}
	return d
}
