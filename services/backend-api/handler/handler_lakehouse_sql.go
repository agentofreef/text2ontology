package handler

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/sqlrewrite"
)

// =========================== Handlers ===========================

// POST /api/lakehouse-sql/execute — execute raw SQL against project's lakehouse schema
// Automatic pagination: wraps user SQL to return page + total count.
func handleLakehouseSQLExecute(db *sql.DB) http.HandlerFunc {
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
		page := 1
		pageSize := 50
		if v, ok := body["page"].(float64); ok && int(v) >= 1 {
			page = int(v)
		}
		if v, ok := body["pageSize"].(float64); ok && int(v) > 0 && int(v) <= 1000 {
			pageSize = int(v)
		}

		if projectID != "" {
			if !authmw.EnforceProjectFromRequest(w, r, db, projectID) {
				return
			}
		}
		if userSQL == "" || projectID == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "sql and projectId required"})
			return
		}

		// Look up project's lakehouse schema
		var schema string
		db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`, projectID).Scan(&schema)
		if schema == "" {
			JsonResp(w, M{"error": "项目尚未配置数据湖仓 schema，请先导入 PBIT 或上传数据"})
			return
		}

		// Reject DDL / DML (single source of truth: pkg/sqlrewrite).
		if err := sqlrewrite.RejectDDL(userSQL); err != nil {
			_ = logLakehouseSQL(db, projectID, userSQL, 0, 0, err.Error())
			JsonResp(w, M{"error": err.Error(), "blocked": true})
			return
		}

		// Only allow SELECT/WITH as the statement type (first word)
		first := firstSQLWord(strings.ToLower(userSQL))
		if first != "select" && first != "with" && first != "table" && first != "values" {
			errMsg := fmt.Sprintf("只允许 SELECT / WITH 查询，检测到: %s", first)
			_ = logLakehouseSQL(db, projectID, userSQL, 0, 0, errMsg)
			JsonResp(w, M{"error": errMsg, "blocked": true})
			return
		}

		// Wrap with pagination: strip trailing ;, wrap in subquery
		clean := strings.TrimRight(userSQL, "; \t\n\r")
		offset := (page - 1) * pageSize
		paginatedSQL := fmt.Sprintf(
			`SELECT q.*, COUNT(*) OVER() AS __total FROM (%s) AS q LIMIT %d OFFSET %d`,
			clean, pageSize, offset,
		)

		start := time.Now()
		cols, rows, total, execErr := executePaginated(db, paginatedSQL, schema)
		duration := int(time.Since(start).Milliseconds())

		rowCount := len(rows)
		_ = logLakehouseSQL(db, projectID, userSQL, rowCount, duration, stringOr(execErr))

		resp := M{
			"durationMs": duration,
			"rowCount":   rowCount,
			"page":       page,
			"pageSize":   pageSize,
			"totalCount": total,
		}
		if execErr != nil {
			// Raw Postgres errors leak schema details (table/column names,
			// constraint identifiers, internal positions). Log the full error
			// server-side (also persisted via logLakehouseSQL above) and return
			// a generic, classified message to the client.
			log.Printf("lakehouse-sql: exec error (project=%s): %v", projectID, execErr)
			resp["error"] = sanitizeSQLExecError(execErr)
		} else {
			resp["columns"] = cols
			resp["rows"] = rows
		}
		JsonResp(w, resp)
	}
}

// GET /api/lakehouse-sql/schema?projectId=... — list tables + columns in the lakehouse schema
func handleLakehouseSQLSchema(db *sql.DB) http.HandlerFunc {
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

		var schema string
		db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`, projectID).Scan(&schema)
		if schema == "" {
			ListResp(w, []M{}, 0)
			return
		}

		rows, err := db.Query(`
			SELECT c.table_name, c.column_name, c.data_type
			FROM information_schema.columns c
			WHERE c.table_schema = $1
			ORDER BY c.table_name, c.ordinal_position`, schema)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		type col struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		tables := map[string][]col{}
		var order []string
		for rows.Next() {
			var t, n, dt string
			rows.Scan(&t, &n, &dt)
			if _, ok := tables[t]; !ok {
				order = append(order, t)
			}
			tables[t] = append(tables[t], col{Name: n, Type: dt})
		}

		// Also load approximate row counts (optional, best-effort)
		rowCountMap := map[string]int64{}
		crows, _ := db.Query(`
			SELECT relname, n_live_tup
			FROM pg_stat_user_tables
			WHERE schemaname = $1`, schema)
		if crows != nil {
			for crows.Next() {
				var n string
				var c int64
				crows.Scan(&n, &c)
				rowCountMap[n] = c
			}
			crows.Close()
		}

		var out []M
		for _, t := range order {
			out = append(out, M{
				"name":     t,
				"columns":  tables[t],
				"rowCount": rowCountMap[t],
			})
		}
		if out == nil {
			out = []M{}
		}
		ListResp(w, out, len(out))
	}
}

// GET|DELETE /api/lakehouse-sql/history
func handleLakehouseSQLHistory(db *sql.DB) http.HandlerFunc {
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
				if !authmw.EnforceEntityProject(w, r, db, "ont_lakehouse_sql_log", "id", id) {
					return
				}
				db.Exec(`DELETE FROM ont_lakehouse_sql_log WHERE id = $1`, id)
			}
			JsonResp(w, M{"success": true})
			return
		}
		projectID := GetProjectID(r)
		if projectID == "" {
			ListResp(w, []M{}, 0)
			return
		}
		rows, err := db.Query(`SELECT id, sql_text, row_count, duration_ms, COALESCE(error,''), created_at
			FROM ont_lakehouse_sql_log WHERE project_id = $1 ORDER BY created_at DESC LIMIT 50`, projectID)
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

// GET|POST /api/lakehouse-sql/snippets
func handleLakehouseSQLSnippets(db *sql.DB) http.HandlerFunc {
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
				FROM ont_lakehouse_sql_snippet WHERE project_id = $1 ORDER BY name`, projectID)
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
			err := db.QueryRow(`INSERT INTO ont_lakehouse_sql_snippet
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

// DELETE /api/lakehouse-sql/snippets/{id}
func handleLakehouseSQLSnippetByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/lakehouse-sql/snippets")
		// Cross-project IDOR guard: verify project access before touching this snippet.
		if !authmw.EnforceEntityProject(w, r, db, "ont_lakehouse_sql_snippet", "id", id) {
			return
		}
		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_lakehouse_sql_snippet WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}
		http.NotFound(w, r)
	}
}

// =========================== Core logic ===========================

// executePaginated runs the paginated SQL with search_path set, extracts __total
// column from results, and returns cols/rows/total/err.
func executePaginated(db *sql.DB, sqlText, schema string) ([]M, [][]interface{}, int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	_, _ = tx.Exec(`SET LOCAL statement_timeout = '30s'`)
	_, _ = tx.Exec(`SET LOCAL transaction_read_only = on`)
	if _, err := tx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(schema))); err != nil {
		return nil, nil, 0, fmt.Errorf("search_path 设置失败: %v", err)
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

	// Separate __total column from user columns
	totalIdx := -1
	for i, ct := range colTypes {
		if ct.Name() == "__total" {
			totalIdx = i
			break
		}
	}

	userCols := make([]M, 0, len(colTypes))
	userColIdx := make([]int, 0, len(colTypes))
	for i, ct := range colTypes {
		if i == totalIdx {
			continue
		}
		userCols = append(userCols, M{"name": ct.Name(), "type": ct.DatabaseTypeName()})
		userColIdx = append(userColIdx, i)
	}

	var out [][]interface{}
	var total int64
	for rows.Next() {
		vals := make([]interface{}, len(colTypes))
		ptrs := make([]interface{}, len(colTypes))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return userCols, out, total, err
		}
		// Capture total from first row
		if totalIdx >= 0 {
			if v, ok := vals[totalIdx].(int64); ok {
				total = v
			}
		}
		// Build user row without __total
		row := make([]interface{}, 0, len(userColIdx))
		for _, idx := range userColIdx {
			v := vals[idx]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row = append(row, v)
		}
		out = append(out, row)
	}
	return userCols, out, total, nil
}

func logLakehouseSQL(db *sql.DB, projectID, sqlText string, rowCount, durationMs int, errStr string) error {
	var pid interface{}
	if projectID != "" {
		pid = projectID
	} else {
		pid = nil
	}
	_, err := db.Exec(`INSERT INTO ont_lakehouse_sql_log
		(project_id, sql_text, row_count, duration_ms, error)
		VALUES ($1, $2, $3, $4, $5)`,
		pid, sqlText, rowCount, durationMs, errStr)
	return err
}

// (DDL rejection now lives in pkg/sqlrewrite.RejectDDL; stringOr reused from handler_sql_passthrough.go)

func firstSQLWord(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "(") {
		s = strings.TrimSpace(s[1:])
	}
	// strip leading comments
	for {
		orig := s
		if strings.HasPrefix(s, "--") {
			if nl := strings.Index(s, "\n"); nl >= 0 {
				s = strings.TrimSpace(s[nl+1:])
			} else {
				return ""
			}
		}
		if strings.HasPrefix(s, "/*") {
			if end := strings.Index(s, "*/"); end >= 0 {
				s = strings.TrimSpace(s[end+2:])
			} else {
				return ""
			}
		}
		if s == orig {
			break
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

// sanitizeSQLExecError maps a raw Postgres execution error to a safe,
// generic, classified message. The raw error (which can expose table/column
// names, constraint identifiers, byte positions, and internal hints) is logged
// server-side by the caller and never returned to the client.
//
// Classification uses the SQLSTATE class when the driver exposes it (*pq.Error),
// falling back to a coarse keyword scan otherwise. The goal is "useful enough to
// fix your own SQL" without leaking schema internals.
func sanitizeSQLExecError(err error) string {
	if err == nil {
		return ""
	}
	if pqErr, ok := err.(*pq.Error); ok {
		// SQLSTATE is normally 5 chars, but some driver/connection errors leave
		// it empty; guard the slice so classification never panics on a short code.
		code := string(pqErr.Code)
		if len(code) < 2 {
			return "数据库执行错误：请检查 SQL 语句后重试。"
		}
		switch code[:2] {
		case "42": // syntax error or access rule violation (incl. undefined table/column)
			return "SQL 语法或对象引用错误：请检查语句语法以及引用的表/列名是否存在且拼写正确。"
		case "22": // data exception (type mismatch, invalid value, division by zero)
			return "数据值错误：类型不匹配或值非法（如类型转换失败、除零等），请检查筛选值与列类型。"
		case "23": // integrity constraint violation
			return "约束冲突：操作违反了数据完整性约束。"
		case "53": // insufficient resources
			return "查询资源不足：结果集或计算过大，请缩小查询范围或加上 LIMIT。"
		case "57": // operator intervention (query canceled, timeout)
			return "查询被取消或超时：请简化查询或缩小数据范围后重试。"
		}
		return "查询执行失败：请检查 SQL 语句是否正确。"
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "syntax"), strings.Contains(low, "does not exist"),
		strings.Contains(low, "column"), strings.Contains(low, "relation"):
		return "SQL 语法或对象引用错误：请检查语句语法以及引用的表/列名是否存在且拼写正确。"
	case strings.Contains(low, "timeout"), strings.Contains(low, "canceled"):
		return "查询被取消或超时：请简化查询或缩小数据范围后重试。"
	default:
		return "查询执行失败：请检查 SQL 语句是否正确。"
	}
}

// Guard against unused re-import of regexp
var _ = regexp.MustCompile
