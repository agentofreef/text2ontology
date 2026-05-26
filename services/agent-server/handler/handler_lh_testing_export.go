package handler

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/xuri/excelize/v2"

	. "github.com/lakehouse2ontology/httputil"
)

// handleLHTestSuiteExport — GET /api/ontology/lh-test-suites/{id}/export?format=csv|xlsx
//
// Exports a test suite to CSV or Excel with 5 columns:
//
//	问题            (user_question)
//	最后回答        (final_answer)        — the assistant's last reply markdown
//	完整回答        (rendered rounds)     — flattened text of every function-call round
//	                                       + final answer; NOT raw JSON
//	运行时间        (duration_ms)         — formatted as "12.3s" (or "—" if unrun)
//	Thread ID       (case_id)             — stable case/thread identifier
//
// "完整回答" extracts only the human-readable parts of each round (tool name,
// arguments by tool kind, then content / SQL / execution result / error). The
// raw JSON of `function_calls` is never emitted — that's the whole point.
func handleLHTestSuiteExport(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	CorsHeaders(w)

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "xlsx" {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "format must be csv or xlsx"})
		return
	}

	// Suite name (for filename).
	var suiteName string
	if err := db.QueryRow(`SELECT name FROM ont_test_suite WHERE id = $1`, suiteID).Scan(&suiteName); err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "suite not found"})
		return
	}

	// All cases in display order.
	rows, err := db.Query(`SELECT id::text, user_question, COALESCE(final_answer,''),
		function_calls, COALESCE(generated_sql,''), COALESCE(execution_status,''),
		COALESCE(execution_result,''), COALESCE(execution_error,''),
		COALESCE(duration_ms, 0)
		FROM ont_test_case WHERE suite_id = $1 ORDER BY sort_order, created_at`, suiteID)
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	defer rows.Close()

	headers := []string{"问题", "最后回答", "完整回答", "运行时间", "Thread ID"}
	var dataRows [][]string
	for rows.Next() {
		var caseID, question, finalAns, genSQL, execStatus, execResult, execError string
		var fcRaw sql.NullString
		var durationMs int64
		if err := rows.Scan(&caseID, &question, &finalAns, &fcRaw,
			&genSQL, &execStatus, &execResult, &execError, &durationMs); err != nil {
			continue
		}
		full := renderFullAnswerText(fcRaw.String, genSQL, execStatus, execResult, execError, finalAns)
		dataRows = append(dataRows, []string{question, finalAns, full, formatDuration(durationMs), caseID})
	}

	baseName := sanitizeFilenameASCII(suiteName)
	if baseName == "" {
		baseName = "lh-test-suite"
	}

	if format == "xlsx" {
		writeXLSXExport(w, baseName, suiteName, headers, dataRows)
		return
	}
	writeCSVExport(w, baseName, suiteName, headers, dataRows)
}

// writeCSVExport emits a UTF-8 CSV with BOM (so Excel autodetects encoding).
func writeCSVExport(w http.ResponseWriter, baseName, suiteName string, headers []string, dataRows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", buildContentDisposition(baseName, suiteName, "csv"))
	w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	cw.Write(headers)
	for _, r := range dataRows {
		cw.Write(r)
	}
	cw.Flush()
}

// writeXLSXExport emits a single-sheet xlsx with sensible column widths and
// wrapped text on the long cells.
func writeXLSXExport(w http.ResponseWriter, baseName, suiteName string, headers []string, dataRows [][]string) {
	f := excelize.NewFile()
	defer f.Close()

	sheet := "Sheet1"
	// Wrap-text style for long-content cells.
	wrapStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"},
	})
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})

	// Header row.
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}
	f.SetRowHeight(sheet, 1, 22)
	if hdrEnd, err := excelize.CoordinatesToCellName(len(headers), 1); err == nil {
		f.SetCellStyle(sheet, "A1", hdrEnd, headerStyle)
	}

	// Data rows.
	for ri, row := range dataRows {
		for ci, v := range row {
			cell, _ := excelize.CoordinatesToCellName(ci+1, ri+2)
			f.SetCellValue(sheet, cell, v)
		}
	}

	// Column widths: question / final / full / duration / thread-id.
	f.SetColWidth(sheet, "A", "A", 40)
	f.SetColWidth(sheet, "B", "B", 60)
	f.SetColWidth(sheet, "C", "C", 90)
	f.SetColWidth(sheet, "D", "D", 12)
	f.SetColWidth(sheet, "E", "E", 38)
	if len(dataRows) > 0 {
		// Apply wrap style to the data block.
		endCell, _ := excelize.CoordinatesToCellName(len(headers), len(dataRows)+1)
		f.SetCellStyle(sheet, "A2", endCell, wrapStyle)
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", buildContentDisposition(baseName, suiteName, "xlsx"))

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(buf.Bytes())
}

// renderFullAnswerText flattens the function-call rounds + final answer into
// human-readable plain text. Used as the "完整回答" column value.
//
// Format per round:
//
//	=== Round N: <tool> ===
//	<tool-specific argument bullets>
//	[空行]
//	返回:
//	<result.content>
//	生成 SQL:
//	<result.generated_sql>
//	执行结果:
//	<result.execution_result>
//	执行错误:
//	<result.execution_error>
//
// Followed by "=== 最终回答 ===" + finalAnswer if present.
//
// If function_calls is empty, falls back to dumping case-level SQL/result/error.
func renderFullAnswerText(fcJSON, genSQL, execStatus, execResult, execError, finalAnswer string) string {
	var sb strings.Builder

	if strings.TrimSpace(fcJSON) != "" && fcJSON != "null" {
		var fcs []struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
			Result    map[string]interface{} `json:"result"`
		}
		if err := json.Unmarshal([]byte(fcJSON), &fcs); err == nil && len(fcs) > 0 {
			for i, fc := range fcs {
				sb.WriteString(fmt.Sprintf("=== Round %d: %s ===\n", i+1, fc.Name))
				writeRoundArguments(&sb, fc.Name, fc.Arguments)
				writeRoundResult(&sb, fc.Result)
				sb.WriteString("\n")
			}
		}
	} else {
		// No structured rounds — fall back to the case-level SQL/result/error.
		if genSQL != "" {
			sb.WriteString("生成 SQL:\n")
			sb.WriteString(genSQL)
			sb.WriteString("\n\n")
		}
		if strings.EqualFold(execStatus, "success") && execResult != "" {
			sb.WriteString("执行结果:\n")
			sb.WriteString(execResult)
			sb.WriteString("\n\n")
		}
		if execError != "" {
			sb.WriteString("执行错误:\n")
			sb.WriteString(execError)
			sb.WriteString("\n\n")
		}
	}

	if strings.TrimSpace(finalAnswer) != "" {
		sb.WriteString("=== 最终回答 ===\n")
		sb.WriteString(finalAnswer)
		sb.WriteString("\n")
	}
	return sb.String()
}

func writeRoundArguments(sb *strings.Builder, toolName string, args map[string]interface{}) {
	switch toolName {
	case "lookup":
		if v := stringSliceFromAny(args["ontology_name"]); len(v) > 0 {
			sb.WriteString("本体: " + strings.Join(v, ", ") + "\n")
		}
		if v := stringSliceFromAny(args["keyword"]); len(v) > 0 {
			sb.WriteString("关键词: " + strings.Join(v, ", ") + "\n")
		}
	case "smartquery":
		if v := stringSliceFromAny(args["objects"]); len(v) > 0 {
			sb.WriteString("对象: " + strings.Join(v, ", ") + "\n")
		}
		if v, ok := args["metric"].(string); ok && v != "" {
			sb.WriteString("口径: " + v + "\n")
		}
		if v := stringSliceFromAny(args["groupBy"]); len(v) > 0 {
			sb.WriteString("分组: " + strings.Join(v, ", ") + "\n")
		}
		if v, ok := args["filters"]; ok && v != nil {
			if filtersJSON, err := json.Marshal(v); err == nil && string(filtersJSON) != "null" && string(filtersJSON) != "[]" {
				sb.WriteString("过滤: " + string(filtersJSON) + "\n")
			}
		}
	default:
		// Unknown tool — emit compact JSON of arguments so info isn't lost.
		if len(args) > 0 {
			if argsJSON, err := json.Marshal(args); err == nil {
				sb.WriteString("参数: " + string(argsJSON) + "\n")
			}
		}
	}
}

// writeRoundResultOntology is like writeRoundResult but omits generated_sql
// (the DuckDB/lakehouse SQL), keeping only the ontology-level query spec and execution results.
func writeRoundResultOntology(sb *strings.Builder, result map[string]interface{}) {
	if result == nil {
		return
	}
	if v, ok := result["error"].(string); ok && v != "" {
		sb.WriteString("\n错误:\n" + v + "\n")
	}
	if v, ok := result["content"].(string); ok && v != "" {
		sb.WriteString("\n返回:\n" + v + "\n")
	}
	// Skip generated_sql — that's the lakehouse DuckDB SQL, not ontology spec
	execStatus, _ := result["execution_status"].(string)
	if execStatus == "success" {
		if v, ok := result["execution_result"].(string); ok && v != "" {
			sb.WriteString("\n执行结果:\n" + v + "\n")
		}
	} else if execStatus == "error" {
		if v, ok := result["execution_error"].(string); ok && v != "" {
			sb.WriteString("\n执行错误:\n" + v + "\n")
		}
	}
}

// renderOntologyFullAnswer is like renderFullAnswerText but uses writeRoundResultOntology
// (omits lakehouse SQL). Used for compare exports where only ontology-level spec matters.
func renderOntologyFullAnswer(fcJSON, finalAnswer string) string {
	var sb strings.Builder

	if strings.TrimSpace(fcJSON) != "" && fcJSON != "null" {
		var fcs []struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
			Result    map[string]interface{} `json:"result"`
		}
		if err := json.Unmarshal([]byte(fcJSON), &fcs); err == nil && len(fcs) > 0 {
			for i, fc := range fcs {
				sb.WriteString(fmt.Sprintf("=== Round %d: %s ===\n", i+1, fc.Name))
				writeRoundArguments(&sb, fc.Name, fc.Arguments)
				writeRoundResultOntology(&sb, fc.Result)
				sb.WriteString("\n")
			}
		}
	}

	if strings.TrimSpace(finalAnswer) != "" {
		sb.WriteString("=== 最终回答 ===\n")
		sb.WriteString(finalAnswer)
		sb.WriteString("\n")
	}
	return sb.String()
}

func writeRoundResult(sb *strings.Builder, result map[string]interface{}) {
	if result == nil {
		return
	}
	if v, ok := result["error"].(string); ok && v != "" {
		sb.WriteString("\n错误:\n" + v + "\n")
	}
	if v, ok := result["content"].(string); ok && v != "" {
		sb.WriteString("\n返回:\n" + v + "\n")
	}
	if v, ok := result["generated_sql"].(string); ok && v != "" {
		sb.WriteString("\n生成 SQL:\n" + v + "\n")
	}
	execStatus, _ := result["execution_status"].(string)
	if execStatus == "success" {
		if v, ok := result["execution_result"].(string); ok && v != "" {
			sb.WriteString("\n执行结果:\n" + v + "\n")
		}
	} else if execStatus == "error" {
		if v, ok := result["execution_error"].(string); ok && v != "" {
			sb.WriteString("\n执行错误:\n" + v + "\n")
		}
	}
}

// formatDuration renders a duration in a CSV/Excel-friendly form. <1s shows
// as "<ms>ms"; otherwise shows as "<seconds>s" with one decimal. Returns "—"
// for zero (case never ran).
func formatDuration(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
}

// stringSliceFromAny accepts either []interface{} (from json.Unmarshal) or
// []string and returns []string. Returns empty slice on nil/wrong-type.
func stringSliceFromAny(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprintf("%v", x))
		}
		return out
	case []string:
		return t
	}
	return nil
}

// sanitizeFilenameASCII strips disallowed filename chars and non-ASCII
// (Chinese suite names) so the fallback `filename=` stays browser-safe.
func sanitizeFilenameASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 32 || r > 126 {
			continue
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.ReplaceAll(out, " ", "_")
	if out == "" {
		return ""
	}
	return out
}

// buildContentDisposition returns a header with both an ASCII fallback
// (filename="...") and the RFC 5987 UTF-8 form (filename*=UTF-8”...) so
// browsers can recover the original (possibly Chinese) suite name.
func buildContentDisposition(asciiBase, fullName, ext string) string {
	asciiPart := asciiBase + "." + ext
	if asciiPart == "."+ext {
		asciiPart = "lh-test-suite." + ext
	}
	encoded := url.PathEscape(fullName + "." + ext)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, asciiPart, encoded)
}

// handleLHTestRunExport — GET /api/ontology/lh-test-suites/{id}/runs/{runId}/export?format=csv|xlsx
//
// Exports a single run's results. Same 5-column layout as suite export but reads from ont_test_run_case.
func handleLHTestRunExport(db *sql.DB, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	CorsHeaders(w)

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "xlsx" {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "format must be csv or xlsx"})
		return
	}

	// Run title for filename
	var runTitle string
	if err := db.QueryRow(`SELECT title FROM ont_test_run WHERE id = $1 AND suite_id = $2`, runID, suiteID).Scan(&runTitle); err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "run not found"})
		return
	}

	rows, err := db.Query(`SELECT id::text, user_question, COALESCE(final_answer,''),
		function_calls, COALESCE(generated_sql,''), COALESCE(execution_status,''),
		COALESCE(execution_result,''), COALESCE(execution_error,''),
		COALESCE(duration_ms, 0)
		FROM ont_test_run_case WHERE run_id = $1 ORDER BY sort_order`, runID)
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	defer rows.Close()

	headers := []string{"问题", "最后回答", "完整回答", "运行时间", "Thread ID"}
	var dataRows [][]string
	for rows.Next() {
		var caseID, question, finalAns, genSQL, execStatus, execResult, execError string
		var fcRaw sql.NullString
		var durationMs int64
		if err := rows.Scan(&caseID, &question, &finalAns, &fcRaw,
			&genSQL, &execStatus, &execResult, &execError, &durationMs); err != nil {
			continue
		}
		full := renderFullAnswerText(fcRaw.String, genSQL, execStatus, execResult, execError, finalAns)
		dataRows = append(dataRows, []string{question, finalAns, full, formatDuration(durationMs), caseID})
	}

	baseName := sanitizeFilenameASCII(runTitle)
	if baseName == "" {
		baseName = "lh-test-run"
	}

	if format == "xlsx" {
		writeXLSXExport(w, baseName, runTitle, headers, dataRows)
		return
	}
	writeCSVExport(w, baseName, runTitle, headers, dataRows)
}

// handleLHCompareExport — GET /api/ontology/lh-test-suites/{id}/compare/export?runs=id1,id2&format=csv|xlsx
//
// Exports an N-way compare as CSV/Excel. Columns:
//
//	问题, {RunA Title}-回答, {RunA Title}-完整回答, {RunB Title}-回答, {RunB Title}-完整回答, ...
//
// "完整回答" contains user question → tool call rounds → final answer (no system prompt).
func handleLHCompareExport(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	CorsHeaders(w)

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "xlsx" {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "format must be csv or xlsx"})
		return
	}

	// Parse run IDs.
	var runIDs []string
	if raw := r.URL.Query().Get("runs"); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				runIDs = append(runIDs, id)
			}
		}
	}
	if len(runIDs) < 2 {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "at least 2 run IDs required"})
		return
	}

	// Validate + load run titles.
	type runInfo struct {
		ID, Title string
	}
	var runs []runInfo
	for _, rid := range runIDs {
		var title string
		err := db.QueryRow(`SELECT title FROM ont_test_run WHERE id = $1 AND suite_id = $2`, rid, suiteID).Scan(&title)
		if err != nil {
			w.WriteHeader(404)
			JsonResp(w, M{"error": "run not found: " + rid})
			return
		}
		runs = append(runs, runInfo{ID: rid, Title: title})
	}

	// Load all run-cases for each run, keyed by case_id.
	type caseData struct {
		RunCaseID   string // thread ID
		FinalAnswer string
		FullText    string
	}
	casesByRun := make([]map[string]caseData, len(runs))
	for i, run := range runs {
		casesByRun[i] = map[string]caseData{}
		rows, err := db.Query(`SELECT rc.id, rc.case_id, COALESCE(rc.final_answer,''),
			rc.function_calls, rc.user_question
			FROM ont_test_run_case rc WHERE rc.run_id = $1`, run.ID)
		if err != nil {
			continue
		}
		for rows.Next() {
			var rcID, caseID, finalAns, question string
			var fcRaw sql.NullString
			if err := rows.Scan(&rcID, &caseID, &finalAns, &fcRaw, &question); err != nil {
				continue
			}
			// Build full text using ontology-only render (no lakehouse SQL)
			var sb strings.Builder
			sb.WriteString("用户提问: ")
			sb.WriteString(question)
			sb.WriteString("\n\n")
			sb.WriteString(renderOntologyFullAnswer(fcRaw.String, finalAns))
			casesByRun[i][caseID] = caseData{RunCaseID: rcID, FinalAnswer: finalAns, FullText: sb.String()}
		}
		rows.Close()
	}

	// Sheet 1 headers: 问题, {Title}-回答, {Title}-完整回答, ...
	headers := []string{"问题"}
	for _, run := range runs {
		headers = append(headers, run.Title+"-回答", run.Title+"-完整回答")
	}

	// Sheet 2 headers: 问题, {Title} Thread ID, ...
	threadHeaders := []string{"问题"}
	for _, run := range runs {
		threadHeaders = append(threadHeaders, run.Title)
	}

	// Load master questions and build rows for both sheets.
	qRows, err := db.Query(`SELECT id, user_question FROM ont_test_case
		WHERE suite_id = $1 ORDER BY sort_order`, suiteID)
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	defer qRows.Close()

	var dataRows [][]string
	var threadRows [][]string
	for qRows.Next() {
		var caseID, question string
		if err := qRows.Scan(&caseID, &question); err != nil {
			continue
		}
		row := []string{question}
		tRow := []string{question}
		for i := range runs {
			if cd, ok := casesByRun[i][caseID]; ok {
				row = append(row, cd.FinalAnswer, cd.FullText)
				tRow = append(tRow, cd.RunCaseID)
			} else {
				row = append(row, "", "")
				tRow = append(tRow, "")
			}
		}
		dataRows = append(dataRows, row)
		threadRows = append(threadRows, tRow)
	}

	// Suite name for filename.
	var suiteName string
	db.QueryRow(`SELECT name FROM ont_test_suite WHERE id = $1`, suiteID).Scan(&suiteName)
	baseName := sanitizeFilenameASCII(suiteName + "-compare")
	if baseName == "" || baseName == "-compare" {
		baseName = "lh-compare"
	}
	displayName := suiteName + "-对比"

	if format == "csv" {
		// CSV only supports one sheet — export the main compare sheet
		writeCSVExport(w, baseName, displayName, headers, dataRows)
		return
	}
	writeCompareXLSXExport(w, baseName, displayName, headers, dataRows, threadHeaders, threadRows)
}

// writeCompareXLSXExport writes an xlsx with 2 sheets:
// Sheet1 "对比" — question + answer/full per run
// Sheet2 "Thread IDs" — question + run-case ID per run
func writeCompareXLSXExport(w http.ResponseWriter, baseName, displayName string, headers []string, dataRows [][]string, threadHeaders []string, threadRows [][]string) {
	f := excelize.NewFile()
	defer f.Close()

	// ── Sheet 1: 对比 ──
	sheet1 := "对比"
	f.SetSheetName("Sheet1", sheet1)

	wrapStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"},
	})
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})

	// Header row.
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet1, cell, h)
	}
	f.SetRowHeight(sheet1, 1, 22)
	if hdrEnd, err := excelize.CoordinatesToCellName(len(headers), 1); err == nil {
		f.SetCellStyle(sheet1, "A1", hdrEnd, headerStyle)
	}

	// Data rows.
	for ri, row := range dataRows {
		for ci, v := range row {
			cell, _ := excelize.CoordinatesToCellName(ci+1, ri+2)
			f.SetCellValue(sheet1, cell, v)
		}
	}

	// Column widths: question=40, answer=50, full=80 (repeating per run).
	f.SetColWidth(sheet1, "A", "A", 40)
	for i := range headers[1:] {
		col, _ := excelize.ColumnNumberToName(i + 2)
		if i%2 == 0 {
			f.SetColWidth(sheet1, col, col, 50) // 回答
		} else {
			f.SetColWidth(sheet1, col, col, 80) // 完整回答
		}
	}
	if len(dataRows) > 0 {
		endCell, _ := excelize.CoordinatesToCellName(len(headers), len(dataRows)+1)
		f.SetCellStyle(sheet1, "A2", endCell, wrapStyle)
	}

	// ── Sheet 2: Thread IDs ──
	sheet2 := "Thread IDs"
	f.NewSheet(sheet2)

	for i, h := range threadHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet2, cell, h)
	}
	f.SetRowHeight(sheet2, 1, 22)
	if hdrEnd, err := excelize.CoordinatesToCellName(len(threadHeaders), 1); err == nil {
		f.SetCellStyle(sheet2, "A1", hdrEnd, headerStyle)
	}

	for ri, row := range threadRows {
		for ci, v := range row {
			cell, _ := excelize.CoordinatesToCellName(ci+1, ri+2)
			f.SetCellValue(sheet2, cell, v)
		}
	}

	f.SetColWidth(sheet2, "A", "A", 40)
	for i := range threadHeaders[1:] {
		col, _ := excelize.ColumnNumberToName(i + 2)
		f.SetColWidth(sheet2, col, col, 38)
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", buildContentDisposition(baseName, displayName, "xlsx"))

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(buf.Bytes())
}
