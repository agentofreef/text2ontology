package lakehouse

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"
)

// ExecuteSQL runs a SQL query against the project's lakehouse schema using
// SET LOCAL search_path for isolation. Pattern from ingest/pbitlakehouse/routes.go:2745.
func ExecuteSQL(db *sql.DB, projectID, sqlQuery string) (ok bool, resultJSON string, errMsg string, durationMs int64) {
	start := time.Now()

	// 1. Look up the project's lakehouse schema.
	var schema string
	if err := db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`, projectID).Scan(&schema); err != nil {
		return false, "", fmt.Sprintf("项目未找到: %v", err), time.Since(start).Milliseconds()
	}
	if schema == "" {
		return false, "", "项目尚未配置数据湖仓 schema，请先导入 PBIT 或上传数据", time.Since(start).Milliseconds()
	}

	// 2. Execute within a transaction with SET LOCAL search_path.
	tx, err := db.Begin()
	if err != nil {
		return false, "", fmt.Sprintf("事务创建失败: %v", err), time.Since(start).Milliseconds()
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(schema))); err != nil {
		return false, "", fmt.Sprintf("search_path 设置失败: %v", err), time.Since(start).Milliseconds()
	}

	rows, err := tx.Query(sqlQuery)
	if err != nil {
		return false, "", fmt.Sprintf("SQL 执行失败: %v", err), time.Since(start).Milliseconds()
	}
	defer rows.Close()

	// 3. Scan rows into []map[string]interface{}.
	cols, err := rows.Columns()
	if err != nil {
		return false, "", fmt.Sprintf("列信息读取失败: %v", err), time.Since(start).Milliseconds()
	}

	// Initialise as an empty (non-nil) slice so a 0-row query serialises to
	// "[]" instead of "null". The LLM can't tell "null" from "query failed",
	// and has historically reacted to "null" by burning through 8+ retry
	// rounds guessing different filter combos. "[]" + empty_result_hint
	// cleanly communicates "query succeeded, 0 matches — try loosening".
	result := []map[string]interface{}{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			log.Printf("LAKEHOUSE-EXEC: row scan error: %v", err)
			continue
		}
		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			v := vals[i]
			// Convert []byte to string for JSON marshalling.
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}
		result = append(result, row)
	}

	b, err := json.Marshal(result)
	if err != nil {
		return false, "", fmt.Sprintf("JSON 序列化失败: %v", err), time.Since(start).Milliseconds()
	}

	return true, string(b), "", time.Since(start).Milliseconds()
}
