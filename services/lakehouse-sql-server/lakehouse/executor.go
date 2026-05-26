package lakehouse

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"
)

// execSetLocaler is the minimal subset of *sql.Tx that applyReadOnlyGuards
// needs. Extracted as an interface so the guard idiom can be unit-tested
// without a live database.
type execSetLocaler interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// readOnlyStatementTimeoutMs bounds how long a single LLM/user SQL statement
// may run inside the read-only transaction. A runaway scan on a large
// lakehouse must not pin a connection indefinitely.
const readOnlyStatementTimeoutMs = 30000

// applyReadOnlyGuards issues the SET LOCAL statements that turn the current
// transaction into a sandbox for untrusted (LLM/user) SQL:
//
//   - transaction_read_only = on  → Postgres rejects any INSERT/UPDATE/DELETE/
//     DDL with a "cannot execute … in a read-only transaction" error.
//   - statement_timeout            → caps wall-clock time per statement.
//
// Both are SET LOCAL, so they are scoped to this transaction and reverted on
// rollback. Returns the first error so the caller can abort before running the
// untrusted query.
func applyReadOnlyGuards(tx execSetLocaler, timeoutMs int) error {
	if _, err := tx.Exec(`SET LOCAL transaction_read_only = on`); err != nil {
		return fmt.Errorf("set read-only: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`SET LOCAL statement_timeout = %d`, timeoutMs)); err != nil {
		return fmt.Errorf("set statement_timeout: %w", err)
	}
	return nil
}

// ExecuteSQL runs a SQL query against the project's lakehouse schema using
// SET LOCAL search_path for isolation. Pattern from ingest/pbitlakehouse/routes.go:2745.
//
// The query runs inside a read-only transaction (transaction_read_only = on +
// statement_timeout) so LLM/user-supplied SQL can SELECT but never mutate the
// lakehouse, and never run unbounded. The tx is always rolled back — no commit
// is needed for a read-only workload.
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

	// 3. Lock the transaction down to read-only with a statement timeout
	// BEFORE running the untrusted query, so any write/DDL is rejected by
	// Postgres.
	if err := applyReadOnlyGuards(tx, readOnlyStatementTimeoutMs); err != nil {
		return false, "", fmt.Sprintf("只读事务设置失败: %v", err), time.Since(start).Milliseconds()
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

// ExecuteSQLParams is the parameterized sibling of ExecuteSQL. It runs the
// identical read-only sandbox (schema lookup → SET LOCAL search_path →
// applyReadOnlyGuards → tx.Query → rollback) but binds args via positional
// driver placeholders ($1, $2, …) instead of executing a literal string.
//
// This is the SQL-mode metric execution path: the human-authored, OD-name SQL
// has already had its `{{param}}` placeholders rewritten to $N by
// sqlrewrite.SubstitutePlaceholders, validated by sqlrewrite.RejectDDL, and
// wrapped in canonical_query CTEs. Every user/LLM-supplied VALUE arrives here
// in args and is bound by the driver — never concatenated into sqlQuery.
//
// Return shape matches ExecuteSQL exactly (ok, resultJSON, errMsg, durationMs)
// so callers can treat the two paths uniformly.
func ExecuteSQLParams(db *sql.DB, projectID, sqlQuery string, args ...interface{}) (ok bool, resultJSON string, errMsg string, durationMs int64) {
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

	// 3. Lock the transaction down to read-only with a statement timeout BEFORE
	// running the untrusted query, so any write/DDL is rejected by Postgres.
	if err := applyReadOnlyGuards(tx, readOnlyStatementTimeoutMs); err != nil {
		return false, "", fmt.Sprintf("只读事务设置失败: %v", err), time.Since(start).Milliseconds()
	}

	// A list-valued param arrives as a []string (sqlrewrite keeps pkg/* driver-
	// agnostic, so the pq.Array wrap happens here at the DB boundary). It binds to
	// a single $N used as `= ANY($N)`. Scalars pass through untouched.
	for i, a := range args {
		if s, ok := a.([]string); ok {
			args[i] = pq.Array(s)
		}
	}

	rows, err := tx.Query(sqlQuery, args...)
	if err != nil {
		return false, "", fmt.Sprintf("SQL 执行失败: %v", err), time.Since(start).Milliseconds()
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return false, "", fmt.Sprintf("列信息读取失败: %v", err), time.Since(start).Milliseconds()
	}

	// Empty (non-nil) slice so a 0-row query serialises to "[]" not "null"
	// (same rationale as ExecuteSQL).
	result := []map[string]interface{}{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			log.Printf("LAKEHOUSE-EXEC-PARAMS: row scan error: %v", err)
			continue
		}
		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			v := vals[i]
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
