// Builder FAT analysis tools — replaces the simple describe_table passthrough
// with rich per-column statistics, multi-table FK discovery, and a generic
// safe SELECT / cross-column keyword search. These tools are the algorithmic
// uplift specified by Wave 1 of the OD Builder Agent plan.
//
// All three functions are READ-ONLY and validate identifiers against
// information_schema BEFORE any string interpolation (SQL-injection guard).
// Wave 2 will hook them into dispatchTool; this file only contains the pure
// algorithmic + DB layer.
package handler

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lib/pq"

	. "github.com/lakehouse2ontology/httputil"
)

// identRe is the validation regex for any identifier interpolated into SQL.
// Allows ASCII letters, digits, underscore, and `$` (Postgres allows `$` in
// identifiers after the first char). Tables/columns failing this regex are
// rejected before any DB call.
var identRe = regexp.MustCompile(`^[A-Za-z_][\w$]*$`)

// quoteIdent doubles embedded `"` for safe Postgres double-quote quoting.
// Caller MUST validate against identRe first; this is defence-in-depth.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// readLakehouseSchema returns the project's lakehouse schema or an error M
// (caller checks for ok=false to short-circuit).
func readLakehouseSchema(ctx context.Context, db *sql.DB, projectID string) (string, M) {
	var schema string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`,
		projectID).Scan(&schema); err != nil {
		return "", M{"error": "failed to read project lakehouse_schema: " + err.Error()}
	}
	if schema == "" {
		return "", M{"error": "project has no lakehouse_schema configured"}
	}
	return schema, nil
}

// validateTable verifies `tableName` exists in information_schema for the given
// lakehouse schema. Returns ok or an error M.
//
// We do NOT pre-validate with a regex: lakehouse table names from PBIT/Excel
// imports legitimately include spaces, Chinese characters, dashes, etc.
// SQL safety comes from quoteIdent (doubled `"`) at usage sites, plus the
// existence check below — non-existent names error out cleanly without
// reaching string-interpolation code paths.
func validateTable(ctx context.Context, db *sql.DB, schema, tableName string) M {
	if tableName == "" {
		return M{"error": "tableName is required"}
	}
	var dummy int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2`,
		schema, tableName).Scan(&dummy)
	if err != nil {
		return M{"error": "table not found in lakehouse schema: " + tableName}
	}
	return nil
}

// columnInfo is the unvarnished metadata read from information_schema.
type columnInfo struct {
	Name            string
	DataType        string
	IsNullable      bool
	OrdinalPosition int
}

func readColumns(ctx context.Context, db *sql.DB, schema, table string) ([]columnInfo, int, error) {
	// total count first (for truncatedColumns flag)
	var total int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2`,
		schema, table).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT column_name, data_type, is_nullable, ordinal_position
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position
		LIMIT 30`, schema, table)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	var out []columnInfo
	for rows.Next() {
		var c columnInfo
		var nullable string
		if err := rows.Scan(&c.Name, &c.DataType, &nullable, &c.OrdinalPosition); err != nil {
			continue
		}
		c.IsNullable = strings.EqualFold(nullable, "YES")
		out = append(out, c)
	}
	return out, total, nil
}

// isNumericType returns true if the Postgres data_type is one we can min/max/avg.
func isNumericType(dt string) bool {
	switch strings.ToLower(dt) {
	case "smallint", "integer", "bigint", "decimal", "numeric",
		"real", "double precision", "money":
		return true
	}
	return false
}

func isTextType(dt string) bool {
	dl := strings.ToLower(dt)
	return strings.Contains(dl, "char") || dl == "text" || dl == "citext"
}

func isTimestampType(dt string) bool {
	dl := strings.ToLower(dt)
	return dl == "date" || strings.HasPrefix(dl, "timestamp") || dl == "time"
}

// idLikeType returns true for types we accept on a "is_likely_id" guess.
func idLikeType(dt string) bool {
	dl := strings.ToLower(dt)
	switch dl {
	case "uuid", "integer", "bigint", "smallint", "text", "citext":
		return true
	}
	return strings.Contains(dl, "char")
}

// scalarFromInterface unboxes []byte → string for sample/min/max output.
func scalarFromInterface(v interface{}) interface{} {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// computeColumnStats runs (cardinality + null_count) + (sample/distribution +
// numeric stats if applicable) for one column. The cardinality query samples
// the first 50000 rows for cap; the null query and numeric stats span the
// full table (cheap aggregate scans).
func computeColumnStats(ctx context.Context, db *sql.DB, schema, table string,
	col columnInfo, rowCount int64) M {
	qSchema := quoteIdent(schema)
	qTable := quoteIdent(table)
	qCol := quoteIdent(col.Name)

	stats := M{
		"name":                col.Name,
		"dataType":            col.DataType,
		"ordinalPosition":     col.OrdinalPosition,
		"isNullable":          col.IsNullable,
		"nullCount":           int64(0),
		"nullRatio":           0.0,
		"cardinality":         int64(0),
		"uniqueRatio":         0.0,
		"sampleValues":        []interface{}{},
		"isLikelyId":          false,
		"isLikelyPrimaryKey":  false,
		"isLikelyForeignKey":  false,
		"isLikelyMachineCode": false,
		"isLikelyTimestamp":   false,
		"isLikelyEnum":        false,
	}

	// 1) null_count
	var nullCount int64
	_ = db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s.%s WHERE %s IS NULL`, qSchema, qTable, qCol)).Scan(&nullCount)
	stats["nullCount"] = nullCount
	if rowCount > 0 {
		stats["nullRatio"] = float64(nullCount) / float64(rowCount)
	}

	// 2) cardinality (sampled at 50k rows for speed; documented cap).
	//
	// If rowCount > 50000 the value is computed over the first 50k rows only —
	// it's an LOWER BOUND on actual cardinality (more rows could only ever
	// reveal more distinct values). cardinalitySampled tells the LLM the value
	// is an estimate so it shouldn't claim "exactly N distinct values" in user-
	// facing replies.
	const cardinalitySampleSize = 50000
	var cardinality int64
	_ = db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(DISTINCT %s) FROM (SELECT %s FROM %s.%s LIMIT %d) sub`,
		qCol, qCol, qSchema, qTable, cardinalitySampleSize)).Scan(&cardinality)
	stats["cardinality"] = cardinality
	stats["cardinalitySampled"] = rowCount > cardinalitySampleSize
	if rowCount > cardinalitySampleSize {
		stats["cardinalitySampleSize"] = int64(cardinalitySampleSize)
	}
	if rowCount > 0 {
		uniqueRatio := float64(cardinality) / float64(rowCount)
		if uniqueRatio > 1 {
			uniqueRatio = 1
		}
		stats["uniqueRatio"] = uniqueRatio
	}

	// 3) sample top values by frequency. Cap at 3 (was 10) to keep inspect
	// output compact — replaying past inspect tool results into the LLM
	// every turn balloons context (4 inspects of a 14-col table at 10
	// samples each cleared 250K tokens of K2 context). Three reps is
	// enough for the LLM to pattern-match what the column carries.
	// Sample strings >80 chars are truncated for the same reason — long
	// description/notes columns blow up otherwise.
	const sampleLimit = 3
	const sampleStrCap = 80
	sampleRows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s, COUNT(*) FROM %s.%s WHERE %s IS NOT NULL GROUP BY %s ORDER BY COUNT(*) DESC LIMIT %d`,
		qCol, qSchema, qTable, qCol, qCol, sampleLimit))
	var samples []interface{}
	if err == nil {
		defer sampleRows.Close()
		for sampleRows.Next() {
			var v interface{}
			var cnt int64
			if err := sampleRows.Scan(&v, &cnt); err != nil {
				continue
			}
			scalar := scalarFromInterface(v)
			if s, ok := scalar.(string); ok && len(s) > sampleStrCap {
				scalar = s[:sampleStrCap] + "…"
			}
			samples = append(samples, scalar)
		}
	}
	if samples == nil {
		samples = []interface{}{}
	}
	stats["sampleValues"] = samples

	// 4) numeric min/max/avg
	if isNumericType(col.DataType) {
		var minV, maxV, avgV sql.NullFloat64
		_ = db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT MIN(%s)::double precision, MAX(%s)::double precision, AVG(%s)::double precision FROM %s.%s`,
			qCol, qCol, qCol, qSchema, qTable)).Scan(&minV, &maxV, &avgV)
		if minV.Valid {
			stats["minValue"] = minV.Float64
		}
		if maxV.Valid {
			stats["maxValue"] = maxV.Float64
		}
		if avgV.Valid {
			stats["avgValue"] = avgV.Float64
		}
	} else if isTimestampType(col.DataType) {
		var minStr, maxStr sql.NullString
		_ = db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT MIN(%s)::text, MAX(%s)::text FROM %s.%s`,
			qCol, qCol, qSchema, qTable)).Scan(&minStr, &maxStr)
		if minStr.Valid {
			stats["minValue"] = minStr.String
		}
		if maxStr.Valid {
			stats["maxValue"] = maxStr.String
		}
	}

	// 5) value distribution for low/mid-cardinality columns.
	//
	// Threshold ≤100 (was 30): lakehouse tables imported from Excel/PBIT often
	// have enum-like columns with 30-80 distinct values (e.g., country codes,
	// product family codes, sub-status enums) that benefit from distribution
	// inspection even though they're not strict "machine codes". The query is
	// still capped at LIMIT 30 in the SELECT itself so we don't blow up the
	// result on the wide tail.
	if cardinality > 0 && cardinality <= 100 {
		const distLimit = 30
		distRows, err := db.QueryContext(ctx, fmt.Sprintf(
			`SELECT %s, COUNT(*) FROM %s.%s GROUP BY %s ORDER BY COUNT(*) DESC LIMIT %d`,
			qCol, qSchema, qTable, qCol, distLimit))
		var dist []M
		if err == nil {
			defer distRows.Close()
			for distRows.Next() {
				var v interface{}
				var cnt int64
				if err := distRows.Scan(&v, &cnt); err != nil {
					continue
				}
				pct := 0.0
				if rowCount > 0 {
					pct = float64(cnt) / float64(rowCount) * 100
					pct = float64(int(pct*100+0.5)) / 100 // 2 decimals
				}
				dist = append(dist, M{
					"value": scalarFromInterface(v),
					"count": cnt,
					"pct":   pct,
				})
			}
		}
		if dist == nil {
			dist = []M{}
		}
		stats["valueDistribution"] = dist
		// Mark truncation when cardinality exceeds the LIMIT — the dist[] only
		// shows top-30 by frequency; the long tail is not represented.
		if cardinality > int64(distLimit) {
			stats["valueDistributionTruncated"] = true
			stats["valueDistributionShown"] = int64(distLimit)
		}
	}

	// 6) heuristics
	uniqueRatio, _ := stats["uniqueRatio"].(float64)
	lowerName := strings.ToLower(col.Name)
	endsWithID := strings.HasSuffix(lowerName, "_id") || strings.HasSuffix(lowerName, "id")
	containsID := strings.Contains(lowerName, "_id_")

	stats["isLikelyId"] = uniqueRatio > 0.95 && idLikeType(col.DataType)
	// PK: unique=1 AND not nullable AND (ordinal=1 OR ends with _id)
	isPK := false
	if uniqueRatio >= 0.999 && !col.IsNullable {
		if col.OrdinalPosition == 1 || endsWithID {
			stats["isLikelyPrimaryKey"] = true
			isPK = true
		}
	}
	// FK detection — three paths, designed to catch lakehouse imports from
	// Excel/PBIT (no _id naming convention) without false-flagging measures:
	//
	//   A) explicit _id naming + repetitive values     → strong FK signal
	//   B) ID-keyword name (id/code/key/no/num/item) + repetitive values + numeric
	//   C) anonymous TEXT column with low uniqueRatio  → likely categorical/FK
	//
	// We deliberately do NOT auto-flag generic numeric columns (bigint/integer)
	// without name hints because measure columns (Quantity, Amount, Total, Price,
	// Count, ...) are also numeric with discrete values; the user must confirm
	// FK status via conversation rather than the heuristic auto-claiming it.
	if !isPK && idLikeType(col.DataType) && uniqueRatio > 0 {
		hasIDKeyword := strings.Contains(lowerName, "id") ||
			strings.Contains(lowerName, "code") ||
			strings.Contains(lowerName, "key") ||
			strings.Contains(lowerName, "_no") || strings.HasSuffix(lowerName, "no") ||
			strings.Contains(lowerName, "_num") || strings.HasSuffix(lowerName, "num")
		switch {
		case (endsWithID || containsID) && uniqueRatio < 0.5:
			stats["isLikelyForeignKey"] = true // Path A
		case hasIDKeyword && uniqueRatio < 0.5 && isNumericType(col.DataType):
			stats["isLikelyForeignKey"] = true // Path B
		case isTextType(col.DataType) && uniqueRatio < 0.1 && cardinality >= 5 && cardinality <= 5000:
			stats["isLikelyForeignKey"] = true // Path C — text-only anonymous
		}
	}
	// Machine code: small enum-like text columns (≤30 distinct, all text).
	// Stays tight on purpose — real machine codes are status / type / region
	// fields with very few values; mid-cardinality columns (30-100) get
	// surfaced via valueDistribution but are not flagged as machine codes.
	if cardinality >= 2 && cardinality <= 30 && isTextType(col.DataType) {
		stats["isLikelyMachineCode"] = true
		stats["isLikelyEnum"] = true
	}
	if isTimestampType(col.DataType) {
		stats["isLikelyTimestamp"] = true
	}

	return stats
}

// builderToolAnalyzeTable returns rich per-column analysis for one table.
// Replaces the old describe_table (which only returned columns + 5 rows).
//
// Args: tableName (string, required).
func builderToolAnalyzeTable(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	tableName, _ := args["tableName"].(string)
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return M{"error": "tableName is required"}
	}

	schema, errM := readLakehouseSchema(ctx, db, projectID)
	if errM != nil {
		return errM
	}
	if errM := validateTable(ctx, db, schema, tableName); errM != nil {
		return errM
	}

	cols, totalCols, err := readColumns(ctx, db, schema, tableName)
	if err != nil {
		return M{"error": "list columns failed: " + err.Error()}
	}
	if len(cols) == 0 {
		return M{"error": "table has no readable columns"}
	}

	qSchema := quoteIdent(schema)
	qTable := quoteIdent(tableName)

	// Total row count — cap at 10M via a wrapper subquery.
	// rowCountCappedAt10M tells the LLM that the count may be saturated when
	// it equals exactly the cap (real value could be larger).
	const rowCountCap = 10_000_000
	var rowCount int64
	if err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT 1 FROM %s.%s LIMIT %d) sub`, qSchema, qTable, rowCountCap),
	).Scan(&rowCount); err != nil {
		return M{"error": "count rows failed: " + err.Error()}
	}
	rowCountCapped := rowCount >= rowCountCap

	// Per-column stats (sequential to keep DB load bounded; analyze is rare).
	colStats := make([]M, 0, len(cols))
	for _, c := range cols {
		colStats = append(colStats, computeColumnStats(ctx, db, schema, tableName, c, rowCount))
	}

	// Sample rows — capped at 2 rows × 80 chars/cell to keep inspect output
	// compact. Re-emitted into LLM context every turn, so each KB matters
	// (Northwind's Employees.Photo column carries ~60KB hex-encoded blobs;
	// without truncation a single inspect blew K2's 262K prompt budget).
	const maxSampleRows = 2
	const maxCellLen = 80
	colNames := make([]string, 0, len(cols))
	quotedCols := make([]string, 0, len(cols))
	for _, c := range cols {
		colNames = append(colNames, c.Name)
		quotedCols = append(quotedCols, quoteIdent(c.Name))
	}
	sampleRows := []M{}
	srows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s FROM %s.%s LIMIT %d`,
		strings.Join(quotedCols, ", "), qSchema, qTable, maxSampleRows))
	if err == nil {
		defer srows.Close()
		for srows.Next() {
			vals := make([]interface{}, len(colNames))
			ptrs := make([]interface{}, len(colNames))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := srows.Scan(ptrs...); err != nil {
				continue
			}
			row := M{}
			for i, n := range colNames {
				v := scalarFromInterface(vals[i])
				if s, ok := v.(string); ok && len(s) > maxCellLen {
					v = s[:maxCellLen] + "…"
				}
				row[n] = v
			}
			sampleRows = append(sampleRows, row)
		}
	}

	// Hypotheses — natural-language summary of what the AI should notice.
	hypotheses := []string{}
	for _, cs := range colStats {
		name, _ := cs["name"].(string)
		if pk, _ := cs["isLikelyPrimaryKey"].(bool); pk {
			hypotheses = append(hypotheses, fmt.Sprintf("%s 是主键 (100%% 唯一, 非空)", name))
		} else if fk, _ := cs["isLikelyForeignKey"].(bool); fk {
			card, _ := cs["cardinality"].(int64)
			ratio, _ := cs["uniqueRatio"].(float64)
			dt, _ := cs["dataType"].(string)
			hypotheses = append(hypotheses, fmt.Sprintf(
				"%s 看似外键候选 (%s, %d 个不同值, 重复率 %.0f%%)",
				name, dt, card, (1.0-ratio)*100))
		}
		if mc, _ := cs["isLikelyMachineCode"].(bool); mc {
			card, _ := cs["cardinality"].(int64)
			hypotheses = append(hypotheses, fmt.Sprintf("%s 是机器码列 (%d 个枚举值)", name, card))
		}
		if ts, _ := cs["isLikelyTimestamp"].(bool); ts {
			minV, hasMin := cs["minValue"]
			maxV, hasMax := cs["maxValue"]
			if hasMin && hasMax {
				hypotheses = append(hypotheses, fmt.Sprintf("%s 是时间列 (%v ~ %v)", name, minV, maxV))
			} else {
				hypotheses = append(hypotheses, fmt.Sprintf("%s 是时间列", name))
			}
		}
	}

	// Replace the verbose JSON columns array with a TOON-formatted string.
	// TOON declares fields once + emits each column as a compact row, saving
	// ~70% tokens vs JSON {…}-per-column. The agent reads it like a CSV table.
	columnsToon := encodeColumnsToon(colStats)

	out := M{
		"table":            tableName,
		"schema":           schema,
		"rowCount":         rowCount,
		"rowCountCapped":   rowCountCapped, // true → real row count could be larger than rowCount
		"rowCountCap":      int64(rowCountCap),
		"totalColumnCount": totalCols,
		"truncatedColumns": totalCols > 30,
		"columnsLimit":     30,
		// columns_toon is the LLM-facing tabular view; field meanings below.
		"columns_toon":        columnsToon,
		"columns_toon_format": "columns[N]{name,type,nullable,card,uniqRatio,kind,samples} where kind ∈ pk|id|fk|code|enum|ts (|-joined when multiple), samples = [v1|v2|v3] capped to 80 chars",
		"sampleRows":          sampleRows,
		"sampleRowCount":      len(sampleRows),
		"sampleRowsRequested": maxSampleRows,
		"hypotheses":          hypotheses,
	}
	if rowCountCapped {
		out["rowCountNote"] = fmt.Sprintf("行数 ≥ %d（已饱和上限）。如需精确行数请用 query_data 跑 SELECT COUNT(*)。", rowCountCap)
	}
	if totalCols > 30 {
		out["columnsNote"] = fmt.Sprintf("仅返回前 30 列（按 ordinal_position 升序），表共 %d 列。", totalCols)
	}
	return out
}

// encodeColumnsToon emits a TOON tabular block for inspect's columns array.
// Format:
//
//	columns[N]{name,type,nullable,card,uniqRatio,kind,samples}:
//	  OrderID,text,true,16282,1.000,id|pk,[20139|16307|15321]
//	  CustomerID,text,true,89,0.005,fk,[ALFKI|ANATR|ANTON]
//
// `kind` packs the legacy isLikely* booleans into a |-joined tag list:
//
//	pk   = primary key candidate
//	id   = generic id column (high uniqueRatio)
//	fk   = foreign key candidate
//	code = machine code (small text enum)
//	enum = enumerable text column
//	ts   = timestamp/date column
//
// `samples` lists up to 3 top-frequency values, each capped at 80 chars,
// with literal `|` characters replaced by `/` to keep the delimiter clean.
// Empty samples and zero-tag kind become `-`.
//
// Saves ~70% tokens vs the JSON map-per-column form. Builder agents are
// expected to read this directly (the format header is also surfaced
// alongside as columns_toon_format for self-documentation).
func encodeColumnsToon(cols []M) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("columns[%d]{name,type,nullable,card,uniqRatio,kind,samples}:\n", len(cols)))
	for _, c := range cols {
		name, _ := c["name"].(string)
		dt, _ := c["dataType"].(string)
		nullable, _ := c["isNullable"].(bool)
		card, _ := c["cardinality"].(int64)
		ur, _ := c["uniqueRatio"].(float64)
		var tags []string
		if v, ok := c["isLikelyPrimaryKey"].(bool); ok && v {
			tags = append(tags, "pk")
		}
		if v, ok := c["isLikelyId"].(bool); ok && v {
			tags = append(tags, "id")
		}
		if v, ok := c["isLikelyForeignKey"].(bool); ok && v {
			tags = append(tags, "fk")
		}
		if v, ok := c["isLikelyMachineCode"].(bool); ok && v {
			tags = append(tags, "code")
		}
		if v, ok := c["isLikelyEnum"].(bool); ok && v {
			tags = append(tags, "enum")
		}
		if v, ok := c["isLikelyTimestamp"].(bool); ok && v {
			tags = append(tags, "ts")
		}
		kind := strings.Join(tags, "|")
		if kind == "" {
			kind = "-"
		}
		var sampleStrs []string
		if vs, ok := c["sampleValues"].([]interface{}); ok {
			for _, v := range vs {
				s := fmt.Sprintf("%v", v)
				if len(s) > 80 {
					s = s[:80] + "…"
				}
				s = strings.ReplaceAll(s, "|", "/")
				s = strings.ReplaceAll(s, ",", " ")
				s = strings.ReplaceAll(s, "\n", " ")
				sampleStrs = append(sampleStrs, s)
			}
		}
		samples := "-"
		if len(sampleStrs) > 0 {
			samples = "[" + strings.Join(sampleStrs, "|") + "]"
		}
		sb.WriteString("  ")
		sb.WriteString(toonField(name))
		sb.WriteString(",")
		sb.WriteString(toonField(dt))
		sb.WriteString(",")
		sb.WriteString(strconv.FormatBool(nullable))
		sb.WriteString(",")
		sb.WriteString(strconv.FormatInt(card, 10))
		sb.WriteString(",")
		sb.WriteString(strconv.FormatFloat(ur, 'f', 3, 64))
		sb.WriteString(",")
		sb.WriteString(kind)
		sb.WriteString(",")
		sb.WriteString(samples)
		sb.WriteString("\n")
	}
	return sb.String()
}

// toonField escapes a TOON field that may contain commas or other delimiters.
// We use a CSV-ish convention: wrap in double-quotes only when required.
func toonField(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// =============================================================================
// builderToolAnalyzeRelationships — multi-table FK discovery
// =============================================================================

// columnNameSimilarity is a port of the Python compute_overlap.py helper.
// Returns max of: exact-lowercase, suffix-stripped ratio, raw ratio.
func columnNameSimilarity(a, b string) float64 {
	la := strings.ToLower(a)
	lb := strings.ToLower(b)
	if la == lb {
		return 1.0
	}
	base := stringRatio(la, lb)
	stripA := stripCommonAffixes(la)
	stripB := stripCommonAffixes(lb)
	stripped := 0.0
	if stripA != "" && stripB != "" {
		stripped = stringRatio(stripA, stripB)
		if stripA == stripB {
			stripped = 1.0
		}
	}
	if base > stripped {
		return base
	}
	return stripped
}

var commonAffixRe = regexp.MustCompile(`(?i)(_id|_no|_num|id|name)$`)

func stripCommonAffixes(s string) string {
	return strings.TrimSpace(commonAffixRe.ReplaceAllString(s, ""))
}

// stringRatio is a simple difflib-style similarity (0..1) based on longest
// common subsequence over total length. Adequate for short identifiers; we
// don't ship full python difflib to keep deps minimal.
func stringRatio(a, b string) float64 {
	if a == "" && b == "" {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}
	// Use simple LCS length.
	ra := []rune(a)
	rb := []rune(b)
	la := len(ra)
	lb := len(rb)
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			if ra[i-1] == rb[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] > cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
		for k := range cur {
			cur[k] = 0
		}
	}
	lcs := prev[lb]
	return 2.0 * float64(lcs) / float64(la+lb)
}

func typesCompatible(a, b string) bool {
	la := strings.ToLower(a)
	lb := strings.ToLower(b)
	if la == lb {
		return true
	}
	if isNumericType(a) && isNumericType(b) {
		return true
	}
	if idLikeType(a) && idLikeType(b) {
		return true
	}
	if isTextType(a) && isTextType(b) {
		return true
	}
	return false
}

// confidenceFor returns the confidence band per the spec table.
func confidenceFor(jaccard, nameSim float64) float64 {
	switch {
	case jaccard > 0.9 && nameSim > 0.6:
		return 0.95
	case jaccard > 0.7 && nameSim > 0.5:
		return 0.85
	case jaccard > 0.5 && nameSim > 0.4:
		return 0.7
	case jaccard > 0.3:
		return 0.5
	}
	return 0.0
}

// jaccardSampleCap is the per-side LIMIT inside computeJaccard. Exported as
// a constant so analyzeRelationships can echo it back in the result for
// LLM transparency.
const jaccardSampleCap = 10000

// computeJaccard runs a single Postgres query that materialises distinct
// values from each side (capped at jaccardSampleCap each) into CTEs, then
// INTERSECT and UNION-counts them. Returns inter, union counts and a flag
// indicating whether either side likely hit the cap (= sampled estimate).
func computeJaccard(ctx context.Context, db *sql.DB, schema, tableA, colA, tableB, colB string) (int64, int64, bool, error) {
	q := fmt.Sprintf(`
		WITH a AS (SELECT DISTINCT %s::text AS v FROM %s.%s WHERE %s IS NOT NULL LIMIT %d),
		     b AS (SELECT DISTINCT %s::text AS v FROM %s.%s WHERE %s IS NOT NULL LIMIT %d)
		SELECT
		  (SELECT COUNT(*) FROM a) AS distinct_a,
		  (SELECT COUNT(*) FROM b) AS distinct_b,
		  (SELECT COUNT(*) FROM (SELECT v FROM a INTERSECT SELECT v FROM b) i) AS inter,
		  (SELECT COUNT(*) FROM (SELECT v FROM a UNION SELECT v FROM b) u) AS uni`,
		quoteIdent(colA), quoteIdent(schema), quoteIdent(tableA), quoteIdent(colA), jaccardSampleCap,
		quoteIdent(colB), quoteIdent(schema), quoteIdent(tableB), quoteIdent(colB), jaccardSampleCap,
	)
	var distA, distB, inter, uni int64
	if err := db.QueryRowContext(ctx, q).Scan(&distA, &distB, &inter, &uni); err != nil {
		return 0, 0, false, err
	}
	// If either side equals the cap, the distinct values were truncated and
	// inter/uni are lower bounds — the real overlap could be higher.
	sampled := distA >= jaccardSampleCap || distB >= jaccardSampleCap
	return inter, uni, sampled, nil
}

// builderToolAnalyzeRelationships discovers FK candidates among the given
// tables. Args: tables (array of strings, length 2..10).
func builderToolAnalyzeRelationships(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	rawTables, _ := args["tables"].([]interface{})
	if len(rawTables) < 2 {
		return M{"error": "tables must contain at least 2 names"}
	}
	if len(rawTables) > 10 {
		return M{"error": "tables capped at 10 per call"}
	}
	tables := make([]string, 0, len(rawTables))
	for _, t := range rawTables {
		if s, ok := t.(string); ok {
			tables = append(tables, strings.TrimSpace(s))
		}
	}

	schema, errM := readLakehouseSchema(ctx, db, projectID)
	if errM != nil {
		return errM
	}
	for _, t := range tables {
		if errM := validateTable(ctx, db, schema, t); errM != nil {
			return M{"error": fmt.Sprintf("invalid table %q: not found in lakehouse schema", t)}
		}
	}

	// Read columns + row count for each table once.
	type tblMeta struct {
		Name        string
		Cols        []columnInfo
		Cardinality int64
	}
	metas := make([]tblMeta, 0, len(tables))
	for _, t := range tables {
		cols, _, err := readColumns(ctx, db, schema, t)
		if err != nil {
			return M{"error": fmt.Sprintf("read columns for %q failed: %s", t, err.Error())}
		}
		var rc int64
		_ = db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT 1 FROM %s.%s LIMIT 10000000) sub`,
				quoteIdent(schema), quoteIdent(t))).Scan(&rc)
		metas = append(metas, tblMeta{Name: t, Cols: cols, Cardinality: rc})
	}

	type candidate struct {
		FromTable           string
		FromColumn          string
		FromColumnDtype     string
		ToTable             string
		ToColumn            string
		ToColumnDtype       string
		NameSim             float64
		Jaccard             float64
		Inter               int64
		Union               int64
		ValueOverlapSampled bool // true → values truncated at jaccardSampleCap, real overlap may be higher
		TypeCompat          bool
		FromCardinality     int64
		ToCardinality       int64
		CardinalityHint     string
		Confidence          float64
	}
	var candidates []candidate
	totalPairsExamined := 0

	for i := 0; i < len(metas); i++ {
		for j := i + 1; j < len(metas); j++ {
			a := metas[i]
			b := metas[j]
			for _, ca := range a.Cols {
				for _, cb := range b.Cols {
					totalPairsExamined++
					nameSim := columnNameSimilarity(ca.Name, cb.Name)
					if nameSim <= 0.4 {
						continue
					}
					compat := typesCompatible(ca.DataType, cb.DataType)
					if !compat {
						continue
					}
					inter, uni, sampled, err := computeJaccard(ctx, db, schema, a.Name, ca.Name, b.Name, cb.Name)
					if err != nil {
						continue
					}
					var jaccard float64
					if uni > 0 {
						jaccard = float64(inter) / float64(uni)
					}
					if jaccard <= 0.3 {
						continue
					}
					conf := confidenceFor(jaccard, nameSim)
					if conf == 0 {
						continue
					}
					// Cardinality hint based on table row counts.
					hint := "many_to_many"
					if a.Cardinality > 0 && b.Cardinality > 0 {
						ratio := float64(a.Cardinality) / float64(b.Cardinality)
						switch {
						case ratio > 5:
							hint = "many_to_one"
						case ratio < 0.2:
							hint = "one_to_many"
						case ratio >= 0.8 && ratio <= 1.25:
							hint = "one_to_one"
						}
					}
					candidates = append(candidates, candidate{
						FromTable:           a.Name,
						FromColumn:          ca.Name,
						FromColumnDtype:     ca.DataType,
						ToTable:             b.Name,
						ToColumn:            cb.Name,
						ToColumnDtype:       cb.DataType,
						NameSim:             nameSim,
						Jaccard:             jaccard,
						Inter:               inter,
						Union:               uni,
						ValueOverlapSampled: sampled,
						TypeCompat:          compat,
						FromCardinality:     a.Cardinality,
						ToCardinality:       b.Cardinality,
						CardinalityHint:     hint,
						Confidence:          conf,
					})
				}
			}
		}
	}

	// sort desc by confidence, then jaccard
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		return candidates[i].Jaccard > candidates[j].Jaccard
	})
	if len(candidates) > 50 {
		candidates = candidates[:50]
	}

	totalCandidatesFound := len(candidates)
	const candidatesShownCap = 50
	if len(candidates) > candidatesShownCap {
		candidates = candidates[:candidatesShownCap]
	}

	high := []M{}
	low := []M{}
	anySampled := false
	for _, c := range candidates {
		if c.ValueOverlapSampled {
			anySampled = true
		}
		entry := M{
			"fromTable":          c.FromTable,
			"fromColumn":         c.FromColumn,
			"fromColumnDataType": c.FromColumnDtype,
			"toTable":            c.ToTable,
			"toColumn":           c.ToColumn,
			"toColumnDataType":   c.ToColumnDtype,
			"evidence": M{
				"nameSimilarity":      c.NameSim,
				"valueOverlap":        c.Jaccard,
				"valuesIntersect":     c.Inter,
				"valuesUnion":         c.Union,
				"valueOverlapSampled": c.ValueOverlapSampled, // true → 10K-distinct sample, lower bound only
				"typeCompat":          c.TypeCompat,
				"cardinalityHint":     c.CardinalityHint,
				"fromCardinality":     c.FromCardinality,
				"toCardinality":       c.ToCardinality,
			},
			"confidence":           c.Confidence,
			"suggestedCardinality": c.CardinalityHint,
		}
		if c.Confidence >= 0.7 {
			high = append(high, entry)
		} else {
			low = append(low, entry)
		}
	}

	out := M{
		"tables":               tables,
		"candidates":           high,
		"uncertain":            low,
		"totalPairsExamined":   totalPairsExamined,
		"totalCandidatesFound": totalCandidatesFound,
		"candidatesLimit":      candidatesShownCap,
		"jaccardSampleCap":     int64(jaccardSampleCap),
		"jaccardAnySampled":    anySampled,
	}
	if totalCandidatesFound > candidatesShownCap {
		out["candidatesTruncated"] = true
		out["candidatesNote"] = fmt.Sprintf("找到 %d 个候选对，仅返回置信度最高的前 %d 个。", totalCandidatesFound, candidatesShownCap)
	}
	if anySampled {
		out["jaccardSampleNote"] = fmt.Sprintf("部分候选对的 valueOverlap 是基于每边前 %d 个 distinct 值的采样，标记 valueOverlapSampled=true 的项实际重叠率只是下限。", jaccardSampleCap)
	}
	return out
}

// =============================================================================
// builderToolQueryData — generic safe SELECT + cross-column keyword search
// =============================================================================

// reservedKeywordsForBan is the list of words the SQL passthrough already
// rejects. We replicate them locally to avoid coupling to backend-api.
var bannedSQLKeywords = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bdrop\b`),
	regexp.MustCompile(`(?i)\btruncate\b`),
	regexp.MustCompile(`(?i)\balter\b`),
	regexp.MustCompile(`(?i)\bcreate\b`),
	regexp.MustCompile(`(?i)\bgrant\b`),
	regexp.MustCompile(`(?i)\brevoke\b`),
	regexp.MustCompile(`(?i)\bvacuum\b`),
	regexp.MustCompile(`(?i)\breindex\b`),
	regexp.MustCompile(`(?i)\binsert\b`),
	regexp.MustCompile(`(?i)\bupdate\b`),
	regexp.MustCompile(`(?i)\bdelete\b`),
	regexp.MustCompile(`(?i)\bcopy\b`),
}

var sqlCommentLineRe = regexp.MustCompile(`--[^\n]*`)
var sqlCommentBlockRe = regexp.MustCompile(`/\*[\s\S]*?\*/`)
var hasLimitReBA = regexp.MustCompile(`(?i)\blimit\s+\d+`)

func stripSQLComments(s string) string {
	s = sqlCommentLineRe.ReplaceAllString(s, " ")
	s = sqlCommentBlockRe.ReplaceAllString(s, " ")
	return s
}

// firstSQLKeyword returns the first non-comment keyword (lowercased) from a
// SQL string. Used to gate "must be SELECT/WITH".
func firstSQLKeyword(s string) string {
	s = stripSQLComments(strings.TrimSpace(s))
	s = strings.TrimLeft(s, "( \t\n\r")
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

// hasMultipleStatements returns true if a `;` is followed by a new SQL
// keyword (not just trailing whitespace). Cheap protective check.
func hasMultipleStatements(s string) bool {
	clean := stripSQLComments(s)
	for i, ch := range clean {
		if ch != ';' {
			continue
		}
		rest := strings.TrimSpace(clean[i+1:])
		if rest == "" {
			return false
		}
		// Look for a leading word.
		f := strings.Fields(rest)
		if len(f) > 0 {
			return true
		}
	}
	return false
}

// likeEscape escapes the keyword for ILIKE pattern. We escape `\`, `%`, `_`
// (bind via $1 so SQL-quotes are not our problem).
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// builderToolQueryData runs either a direct user SELECT (Mode A) or a
// cross-column ILIKE search (Mode B). Args:
//   sql           — direct SELECT statement, optional
//   searchKeyword — keyword for cross-column search, optional
//   inTable       — required when searchKeyword is set
//   limit         — optional, default 50, max 500
func builderToolQueryData(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	sqlText, _ := args["sql"].(string)
	keyword, _ := args["searchKeyword"].(string)
	inTable, _ := args["inTable"].(string)

	limit := 50
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	} else if v, ok := args["limit"].(int); ok {
		limit = v
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	schema, errM := readLakehouseSchema(ctx, db, projectID)
	if errM != nil {
		return errM
	}

	sqlText = strings.TrimSpace(sqlText)
	keyword = strings.TrimSpace(keyword)

	if sqlText == "" && keyword == "" {
		return M{"error": "either sql or searchKeyword is required"}
	}

	// ----- Mode A: direct SELECT -----
	if sqlText != "" {
		// Reject DDL/DML
		for _, re := range bannedSQLKeywords {
			if re.MatchString(stripSQLComments(sqlText)) {
				return M{"error": "only read-only SELECT/WITH allowed", "blocked": true}
			}
		}
		first := firstSQLKeyword(sqlText)
		if first != "select" && first != "with" {
			return M{"error": fmt.Sprintf("only SELECT/WITH allowed, got: %s", first), "blocked": true}
		}
		if hasMultipleStatements(sqlText) {
			return M{"error": "multiple statements not allowed"}
		}
		// Inject LIMIT if missing.
		clean := strings.TrimRight(sqlText, "; \t\n\r")
		executed := clean
		if !hasLimitReBA.MatchString(clean) {
			executed = fmt.Sprintf("%s LIMIT %d", clean, limit)
		}

		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return M{"error": "begin tx failed: " + err.Error()}
		}
		defer tx.Rollback() //nolint:errcheck

		_, _ = tx.ExecContext(ctx, `SET LOCAL statement_timeout = '30s'`)
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`SET LOCAL search_path TO %s, public`, quoteIdent(schema))); err != nil {
			return M{"error": "search_path failed: " + err.Error()}
		}

		rows, err := tx.QueryContext(ctx, executed)
		if err != nil {
			return M{"error": "query failed: " + err.Error()}
		}
		defer rows.Close()

		colTypes, err := rows.ColumnTypes()
		if err != nil {
			return M{"error": err.Error()}
		}
		colNames := make([]string, len(colTypes))
		for i, ct := range colTypes {
			colNames[i] = ct.Name()
		}

		var outRows []M
		for rows.Next() {
			vals := make([]interface{}, len(colNames))
			ptrs := make([]interface{}, len(colNames))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				continue
			}
			row := M{}
			for i, n := range colNames {
				row[n] = scalarFromInterface(vals[i])
			}
			outRows = append(outRows, row)
		}
		if outRows == nil {
			outRows = []M{}
		}
		return M{
			"mode":        "sql",
			"executedSql": executed,
			"rowCount":    len(outRows),
			"columns":     colNames,
			"rows":        outRows,
		}
	}

	// ----- Mode B: keyword search -----
	if inTable == "" {
		return M{"error": "inTable is required when searchKeyword is set"}
	}
	if errM := validateTable(ctx, db, schema, inTable); errM != nil {
		return errM
	}

	// Collect text-like columns of the table.
	textCols, err := db.QueryContext(ctx, `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema=$1 AND table_name=$2
		ORDER BY ordinal_position`, schema, inTable)
	if err != nil {
		return M{"error": "list columns failed: " + err.Error()}
	}
	type tcol struct {
		Name string
		Type string
	}
	var cols []tcol
	for textCols.Next() {
		var n, dt string
		// Column name from information_schema is trusted; quoteIdent below makes
		// the dynamic SQL safe even for names with spaces / unicode / dashes.
		if err := textCols.Scan(&n, &dt); err == nil && isTextType(dt) {
			cols = append(cols, tcol{Name: n, Type: dt})
		}
	}
	textCols.Close()

	pattern := "%" + likeEscape(keyword) + "%"
	type sampleVal struct {
		Value string
		Count int64
	}
	matches := []M{}
	totalCols := 0
	const sampleValuesPerCol = 10
	anyTruncated := false
	for _, c := range cols {
		totalCols++
		// First: real total occurrence count (independent of distinct-value
		// truncation below). One row per matching cell.
		var totalOccurrences int64
		_ = db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT COUNT(*) FROM %s.%s WHERE %s ILIKE $1 ESCAPE '\'`,
			quoteIdent(schema), quoteIdent(inTable), quoteIdent(c.Name),
		), pattern).Scan(&totalOccurrences)
		if totalOccurrences == 0 {
			continue
		}
		// Then: top-N distinct matching values + their counts.
		q := fmt.Sprintf(
			`SELECT %s::text AS v, COUNT(*) AS cnt FROM %s.%s WHERE %s ILIKE $1 ESCAPE '\' GROUP BY %s ORDER BY cnt DESC LIMIT %d`,
			quoteIdent(c.Name), quoteIdent(schema), quoteIdent(inTable),
			quoteIdent(c.Name), quoteIdent(c.Name), sampleValuesPerCol,
		)
		rows, err := db.QueryContext(ctx, q, pattern)
		if err != nil {
			// Some columns (citext / weird collations) may explode. Skip.
			continue
		}
		var samples []M
		for rows.Next() {
			var v sql.NullString
			var cnt int64
			if err := rows.Scan(&v, &cnt); err != nil {
				continue
			}
			val := ""
			if v.Valid {
				val = v.String
			}
			samples = append(samples, M{"value": val, "count": cnt})
		}
		rows.Close()
		// Truncation: if we returned exactly the LIMIT, more distinct values
		// likely exist in the long tail.
		truncated := len(samples) >= sampleValuesPerCol
		if truncated {
			anyTruncated = true
		}
		matches = append(matches, M{
			"column":               c.Name,
			"totalOccurrences":     totalOccurrences,
			"sampleValues":         samples,
			"distinctValuesShown":  len(samples),
			"sampleValuesTruncated": truncated,
		})
	}

	// Suppress unused import warnings if pq isn't needed in some build paths.
	_ = pq.StringArray(nil)

	out := M{
		"mode":                 "keyword_search",
		"table":                inTable,
		"keyword":              keyword,
		"matches":              matches,
		"totalColumnsSearched": totalCols,
		"sampleValuesPerCol":   sampleValuesPerCol,
	}
	if anyTruncated {
		out["sampleValuesNote"] = fmt.Sprintf("某些列的 sampleValues 截断在前 %d 个最高频 distinct 值。totalOccurrences 仍是该列的真实匹配次数。", sampleValuesPerCol)
	}
	return out
}
