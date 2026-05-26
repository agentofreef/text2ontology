// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lib/pq"
)

// getStagingRoot returns the filesystem root under which per-import staging
// directories live. Reads PBIT_STAGING_ROOT env; defaults to /data/pbit-staging.
func getStagingRoot() string {
	if v := os.Getenv("PBIT_STAGING_ROOT"); v != "" {
		return v
	}
	return "/data/pbit-staging"
}

// RegisterRoutes wires pbit endpoints under the default collector path.
// Phase 2: collector-server mounts at /api/connector/pbit by default.
func RegisterRoutes(mux *http.ServeMux, db *sql.DB) {
	RegisterRoutesAt(mux, db, "/api/connector/pbit")
}

// RegisterRoutesAt mounts the handlers under the given base path (no trailing slash).
func RegisterRoutesAt(mux *http.ServeMux, db *sql.DB, base string) {
	mux.HandleFunc(base+"/import", handleImport(db))
	mux.HandleFunc(base+"/add-table", handleAddTable(db))
	mux.HandleFunc(base+"/progress", handleProgress(db))
	mux.HandleFunc(base+"/validate-sql", handleValidateSQL(db))
	mux.HandleFunc(base+"/solidify-sql", handleSolidifySQL(db))
	mux.HandleFunc(base+"/test-canonical", handleTestCanonical(db))
	mux.HandleFunc(base+"/sync-property-keywords", handleSyncPropertyKeywords(db))
	mux.HandleFunc(base+"/lakehouse-keywords/summary", handleLakehouseKeywordsSummary(db))
	mux.HandleFunc(base+"/lakehouse-keywords/tree", handleLakehouseKeywordsTree(db))
	mux.HandleFunc(base+"/lakehouse-keywords/toggle-column-name", handleToggleColumnName(db))
	mux.HandleFunc(base+"/lakehouse-keywords/aliases", handleLakehouseKeywordAliases(db))
	mux.HandleFunc(base+"/lakehouse-keywords/column-alias", handleColumnAliasCanonical(db))
	mux.HandleFunc(base+"/lakehouse-keywords/vector-status", handleVectorStatus(db))
	mux.HandleFunc(base+"/lakehouse-keywords/compute-vectors", handleComputeVectors(db))
	// Bulk operations — registered as specific paths so they win over the
	// /lakehouse-keywords catch-all (Go mux: longer paths beat shorter ones).
	mux.HandleFunc(base+"/lakehouse-keywords/bulk-impact", handleLakehouseKeywordsBulkImpact(db))
	mux.HandleFunc(base+"/lakehouse-keywords/bulk-delete", handleLakehouseKeywordsBulkDelete(db))
	mux.HandleFunc(base+"/lakehouse-keywords/bulk-update", handleLakehouseKeywordsBulkUpdate(db))
	mux.HandleFunc(base+"/lakehouse-keywords/bulk-reanchor", handleLakehouseKeywordsBulkReanchor(db))
	mux.HandleFunc(base+"/lakehouse-keywords/bulk-create", handleLakehouseKeywordsBulkCreate(db))
	mux.HandleFunc(base+"/lakehouse-keywords", handleLakehouseKeywords(db))
	mux.HandleFunc(base+"/ensure-property-knowledge", handleEnsurePropertyKnowledge(db))
}

// ─── SSE helpers ─────────────────────────────────────────────────────────────

func sseSetup(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeSSE(w http.ResponseWriter, data interface{}) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// ─── CORS / method guard ──────────────────────────────────────────────────────

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func jsonResp(w http.ResponseWriter, status int, data interface{}) {
	corsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
type tablePreview struct {
	Name          string              `json:"name"`
	SourceType    string              `json:"sourceType"`
	ColumnCount   int                 `json:"columnCount"`
	Columns       []map[string]string `json:"columns"`
	PartitionKind string              `json:"partitionKind"`
	RequiredFiles []string            `json:"requiredFiles,omitempty"`
	RawM          string              `json:"rawM,omitempty"`
}

type relPreview struct {
	FromTable              string `json:"fromTable"`
	FromColumn             string `json:"fromColumn"`
	ToTable                string `json:"toTable"`
	ToColumn               string `json:"toColumn"`
	Cardinality            string `json:"cardinality"`
	IsActive               bool   `json:"isActive"`
	CrossFilteringBehavior string `json:"crossFilteringBehavior"`
}

type measurePreview struct {
	Name        string `json:"name"`
	Table       string `json:"table"`
	Description string `json:"description,omitempty"`
}

type parseResp struct {
	ImportId       string           `json:"importId"`
	SourceFilename string           `json:"sourceFilename"`
	Tables         []tablePreview   `json:"tables"`
	Relationships  []relPreview     `json:"relationships"`
	Measures       []measurePreview `json:"measures"`
	DerivedCount   int              `json:"derivedCount"`
	ParsedAt       string           `json:"parsedAt"`
}
type confirmBinding struct {
	FileName  string `json:"fileName"`
	TableName string `json:"tableName"`
	State     string `json:"state"`
}

// ─── POST /api/pbit-lakehouse/import (SSE) ───────────────────────────────────

type importRequest struct {
	ImportId  string `json:"importId"`
	ProjectId string `json:"projectId"`
	// Bindings from the confirm step, carried through for the actual load.
	Bindings []confirmBinding `json:"bindings"`
}

func handleImport(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			corsHeaders(w)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req importRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			corsHeaders(w)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if req.ImportId == "" || req.ProjectId == "" {
			corsHeaders(w)
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "importId and projectId required"})
			return
		}

		// IDOR guard: projectId arrives in the JSON body (not gated by the auth
		// middleware). Verify BEFORE switching to the SSE stream so a 401/403 can
		// still be written as a normal JSON response.
		if !authmw.EnforceProjectAccess(w, r, db, req.ProjectId) {
			return
		}

		sseSetup(w)

		stagingDir := filepath.Join(getStagingRoot(), req.ImportId)
		xlsxDir := filepath.Join(stagingDir, "xlsx")
		pbitPath := filepath.Join(stagingDir, "source.pbit")

		// Pre-check: ensure all per-table uploads are complete (if per-table flow was used).
		var pendingCount int
		_ = db.QueryRow(`
			SELECT count(*) FROM lakehouse_table_status
			WHERE project_id=$1 AND import_id=$2
			  AND source_type IN ('excel','sql')
			  AND status NOT IN ('loaded','skipped')`,
			req.ProjectId, req.ImportId,
		).Scan(&pendingCount)
		if pendingCount > 0 {
			sseSetup(w)
			writeSSE(w, map[string]interface{}{"phase": "error", "error": fmt.Sprintf("%d tables still pending upload. Complete per-table uploads first.", pendingCount)})
			return
		}

		// Build per-table file lookup from lakehouse_table_status (per-table flow).
		perTableFiles := map[string]string{} // tableName → fileName
		rows, queryErr := db.Query(`
			SELECT table_name, file_name FROM lakehouse_table_status
			WHERE project_id=$1 AND import_id=$2 AND status='loaded' AND file_name IS NOT NULL`,
			req.ProjectId, req.ImportId,
		)
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var tName, fName string
				if scanErr := rows.Scan(&tName, &fName); scanErr == nil {
					perTableFiles[tName] = fName
				}
			}
		}

		// Acquire in-progress lock via ont_import_log.
		var importLogID string
		err := db.QueryRow(`
			INSERT INTO ont_import_log (project_id, source_type, status)
			VALUES ($1, 'pbit-lakehouse', 'loading')
			RETURNING id`,
			req.ProjectId,
		).Scan(&importLogID)
		if err != nil {
			writeSSE(w, map[string]interface{}{"phase": "error", "error": "import already in progress or DB error: " + err.Error()})
			return
		}

		failImport := func(msg string) {
			_ = db.QueryRow(`UPDATE ont_import_log SET status='failed', error_message=$1 WHERE id=$2`,
				msg, importLogID).Err()
			writeSSE(w, map[string]interface{}{"phase": "error", "error": msg})
		}

		// Re-parse PBIT.
		pbit, err := ParsePbit(pbitPath)
		if err != nil {
			failImport("parse pbit: " + err.Error())
			return
		}

		finalSchema := SanitizeSchemaName(req.ProjectId)
		stagingSchema := StagingName(finalSchema)

		// --- Step 1: Pre-check ---
		preCheckTx, err := db.Begin()
		if err != nil {
			failImport("begin precheck tx: " + err.Error())
			return
		}
		if err := CheckTargetSchemaExists(preCheckTx, finalSchema); err != nil {
			preCheckTx.Rollback() //nolint:errcheck
			if errors.Is(err, ErrTargetSchemaExists) {
				failImport("TARGET_SCHEMA_EXISTS: " + finalSchema + " already present; clean up orphan before retry")
			} else {
				failImport("precheck schema: " + err.Error())
			}
			return
		}
		preCheckTx.Rollback() //nolint:errcheck
		writeSSE(w, map[string]interface{}{"phase": "precheck_ok"})

		// --- Step 2: Create staging schema ---
		if err := CreateStagingSchema(db, stagingSchema); err != nil {
			failImport("create staging schema: " + err.Error())
			return
		}
		writeSSE(w, map[string]interface{}{"phase": "schema_created", "schema": stagingSchema})

		// Build lookup: tableName → binding (confirmed only).
		confirmedBindings := map[string]confirmBinding{}
		for _, b := range req.Bindings {
			if b.State == "confirmed" {
				confirmedBindings[b.TableName] = b
			}
		}

		// Track any table failures.
		anyTableFailed := false
		var derivedSpecs []DerivedSpec

		// --- Step 3: Per-table load ---
		for _, t := range pbit.Tables {
			tableName := t.Name

			// Build DerivedSpec for non-external, non-unsupported partitions.
			if len(t.Partitions) > 0 {
				mStr := string(t.Partitions[0].Source.Expression)
				kind, meta, _ := ClassifyPartition(mStr)
				if kind != KindUnsupported {
					derivedSpecs = append(derivedSpecs, DerivedSpec{
						ViewName: tableName,
						Kind:     kind,
						Meta:     meta,
						Cols:     t.Columns,
					})
					continue // derived views are handled after base tables
				}
				// External sources (sql/sharepoint) → empty table, no load.
				lower := strings.ToLower(mStr)
				if strings.Contains(lower, "sql.database") ||
					strings.Contains(lower, "sharepoint.files") ||
					strings.Contains(lower, "excel.workbook") {
					// Create empty table.
					if err := CreateLakehouseTable(db, stagingSchema, tableName, t.Columns); err != nil {
						log.Printf("[pbitlakehouse] create empty table %q: %v", tableName, err)
					}
					continue
				}
			}

			b, hasBinding := confirmedBindings[tableName]
			// Also check per-table upload flow.
			perTableFileName, hasPerTable := perTableFiles[tableName]
			if !hasBinding && !hasPerTable {
				// No confirmed binding and no per-table upload → create empty placeholder.
				if err := CreateLakehouseTable(db, stagingSchema, tableName, t.Columns); err != nil {
					log.Printf("[pbitlakehouse] create empty table %q (no binding): %v", tableName, err)
				}
				continue
			}

			writeSSE(w, map[string]interface{}{"phase": "table_loading", "tableName": tableName})

			if err := CreateLakehouseTable(db, stagingSchema, tableName, t.Columns); err != nil {
				anyTableFailed = true
				writeSSE(w, map[string]interface{}{
					"phase":     "table_failed",
					"tableName": tableName,
					"error":     err.Error(),
				})
				continue
			}

			// Resolve the file path: per-table flow takes priority over batch binding.
			var resolvedFileName string
			if hasPerTable {
				resolvedFileName = perTableFileName
			} else {
				resolvedFileName = b.FileName
			}
			xlsxPath := filepath.Join(xlsxDir, resolvedFileName)
			headers, sheet, err := ReadXlsxHeaders(xlsxPath)
			if err != nil {
				anyTableFailed = true
				writeSSE(w, map[string]interface{}{
					"phase":     "table_failed",
					"tableName": tableName,
					"error":     "read headers: " + err.Error(),
				})
				continue
			}

			rowIter, closeFn, err := ReadXlsxRows(xlsxPath, sheet)
			if err != nil {
				anyTableFailed = true
				writeSSE(w, map[string]interface{}{
					"phase":     "table_failed",
					"tableName": tableName,
					"error":     "open row iter: " + err.Error(),
				})
				continue
			}

			rowCount, copyErr := CopyRowsInto(db, stagingSchema, tableName, headers, rowIter)
			closeFn() //nolint:errcheck
			if copyErr != nil {
				anyTableFailed = true
				writeSSE(w, map[string]interface{}{
					"phase":     "table_failed",
					"tableName": tableName,
					"error":     copyErr.Error(),
				})
				continue
			}

			writeSSE(w, map[string]interface{}{
				"phase":     "table_loaded",
				"tableName": tableName,
				"rowCount":  rowCount,
			})
		}

		// --- Step 4: Derived views ---
		var derivedResults []DerivedResult
		if len(derivedSpecs) > 0 {
			results, err := BuildDerivedViews(db, stagingSchema, derivedSpecs)
			if err != nil {
				log.Printf("[pbitlakehouse] BuildDerivedViews: %v", err)
			}
			derivedResults = results

			var warnings []string
			for _, dr := range results {
				if dr.Warning != "" {
					warnings = append(warnings, dr.ViewName+": "+dr.Warning)
				}
			}
			if warnings == nil {
				warnings = []string{}
			}
			writeSSE(w, map[string]interface{}{
				"phase":     "views_built",
				"viewCount": len(results),
				"warnings":  warnings,
			})
		}

		// --- Step 5: Terminal transaction ---
		writeSSE(w, map[string]interface{}{"phase": "committing"})

		finalStatus := "success"
		if anyTableFailed {
			finalStatus = "partial"
		}

		// Serialize the staging→final merge + ontology write per-project so a
		// concurrent import into the SAME project can't interleave on the shared
		// proj_<hex> schema (see WithProjectLock). A nil return means the closure
		// finished cleanly; a non-nil error has NOT yet been surfaced to the SSE
		// stream, so the caller does that once below.
		mergeErr := WithProjectLock(r.Context(), db, req.ProjectId, func(lctx context.Context) error {
			tx, err := db.Begin()
			if err != nil {
				_ = DropSchemaCascade(db, stagingSchema)
				_ = os.RemoveAll(stagingDir)
				return fmt.Errorf("begin terminal tx: %w", err)
			}

			// Merge staging tables into final schema (incremental — safe even if
			// final schema already exists from a prior import).
			if err := MergeStagingIntoFinal(tx, stagingSchema, finalSchema); err != nil {
				tx.Rollback() //nolint:errcheck
				_ = DropSchemaCascade(db, stagingSchema)
				_ = os.RemoveAll(stagingDir)
				return fmt.Errorf("merge schema: %w", err)
			}

			// Remove only the ontology rows for tables in this import (not all Od).
			var tableNames []string
			for _, t := range pbit.Tables {
				tableNames = append(tableNames, t.Name)
			}
			if err := CleanOntologyByTableNames(lctx, tx, req.ProjectId, tableNames); err != nil {
				tx.Rollback() //nolint:errcheck
				return fmt.Errorf("clean ontology by table names: %w", err)
			}

			// The PBIT/PBIX batch import is keyed by import_id and has no data_source
			// row, so leave data_source_id NULL ("").
			if _, err := PopulateOntology(tx, req.ProjectId, finalSchema, pbit, derivedResults, ""); err != nil {
				tx.Rollback() //nolint:errcheck
				return fmt.Errorf("populate ontology: %w", err)
			}

			if _, err := tx.Exec(`UPDATE ont_import_log SET status=$1 WHERE id=$2`, finalStatus, importLogID); err != nil {
				tx.Rollback() //nolint:errcheck
				return fmt.Errorf("update import log: %w", err)
			}

			if _, err := tx.Exec(`
				UPDATE project
				SET source_type='pbit-lakehouse', lakehouse_schema=$1, status='active'
				WHERE id=$2`,
				finalSchema, req.ProjectId,
			); err != nil {
				tx.Rollback() //nolint:errcheck
				return fmt.Errorf("update project: %w", err)
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit terminal tx: %w", err)
			}
			return nil
		})
		if mergeErr != nil {
			failImport(mergeErr.Error())
			return
		}

		writeSSE(w, map[string]interface{}{"phase": "committed"})

		// Best-effort cleanup.
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Printf("[pbitlakehouse] WARNING: cleanup staging dir %q: %v", stagingDir, err)
		}

		// Trigger vector embedding in the background (best-effort).
		go func(pid string) {
			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := RecomputeVectorsForProject(bgCtx, db, pid); err != nil {
				log.Printf("[pbitlakehouse] auto vector compute (pbit import): %v", err)
			}
		}(req.ProjectId)

		writeSSE(w, map[string]interface{}{
			"phase":       "done",
			"projectId":   req.ProjectId,
			"status":      finalStatus,
			"importLogId": importLogID,
		})
	}
}


// ─── POST /api/pbit-lakehouse/add-table ──────────────────────────────────────

func handleAddTable(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseMultipartForm(200 << 20); err != nil {
			jsonResp(w, 400, map[string]string{"error": "parse form: " + err.Error()})
			return
		}

		projectID := r.FormValue("projectId")
		tableName := r.FormValue("tableName")
		if projectID == "" || tableName == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId and tableName required"})
			return
		}

		// IDOR guard: projectId arrives in the multipart body (not gated by the
		// auth middleware). Verify access before touching the project's lakehouse.
		if !authmw.EnforceProjectAccess(w, r, db, projectID) {
			return
		}

		// Load project's lakehouse_schema.
		var lakehouseSchema string
		if err := db.QueryRow(`SELECT lakehouse_schema FROM project WHERE id=$1`, projectID).Scan(&lakehouseSchema); err != nil {
			jsonResp(w, 404, map[string]string{"error": "project not found"})
			return
		}
		if lakehouseSchema == "" {
			jsonResp(w, 400, map[string]string{"error": "project has no lakehouse_schema; run import first"})
			return
		}

		f, fh, err := r.FormFile("file")
		if err != nil {
			jsonResp(w, 400, map[string]string{"error": "file field required: " + err.Error()})
			return
		}
		defer f.Close()

		// Stage file to a temp location.
		tmpDir, err := os.MkdirTemp("", "pbitlakehouse-addtable-")
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "tmpdir: " + err.Error()})
			return
		}
		defer os.RemoveAll(tmpDir)

		tmpPath := filepath.Join(tmpDir, fh.Filename)
		outFile, err := os.Create(tmpPath)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "create tmp file: " + err.Error()})
			return
		}
		if _, err := io.Copy(outFile, f); err != nil {
			outFile.Close()
			jsonResp(w, 500, map[string]string{"error": "write tmp file: " + err.Error()})
			return
		}
		outFile.Close()

		lowerName := strings.ToLower(fh.Filename)
		var headers []string
		var rowIter func() ([]string, error)
		var closeIter func() error

		if strings.HasSuffix(lowerName, ".csv") {
			headers, rowIter, closeIter, err = readCSVFile(tmpPath)
		} else {
			// xlsx
			var sheet string
			headers, sheet, err = ReadXlsxHeaders(tmpPath)
			if err == nil {
				rowIter, closeIter, err = ReadXlsxRows(tmpPath, sheet)
			}
		}
		if err != nil {
			jsonResp(w, 400, map[string]string{"error": "read file: " + err.Error()})
			return
		}

		// Collect up to 20 sample rows for type inference.
		var sampleRows [][]string
		for i := 0; i < 20; i++ {
			row, err := rowIter()
			if err != nil || row == nil {
				break
			}
			sampleRows = append(sampleRows, row)
		}
		if closeIter != nil {
			closeIter() //nolint:errcheck
		}

		inferredCols := InferColumnTypes(headers, sampleRows)

		// Convert InferredCol → PbitColumn for CreateLakehouseTable.
		pbitCols := make([]PbitColumn, len(inferredCols))
		for i, ic := range inferredCols {
			pbitCols[i] = PbitColumn{Name: ic.Name, DataType: inferredColTypeToPbit(ic.DataType)}
		}

		if err := CreateLakehouseTable(db, lakehouseSchema, tableName, pbitCols); err != nil {
			jsonResp(w, 500, map[string]string{"error": "create table: " + err.Error()})
			return
		}

		// Re-open file for actual data load.
		var rowIter2 func() ([]string, error)
		var closeIter2 func() error
		if strings.HasSuffix(lowerName, ".csv") {
			_, rowIter2, closeIter2, err = readCSVFile(tmpPath)
		} else {
			var sheet string
			_, sheet, err = ReadXlsxHeaders(tmpPath)
			if err == nil {
				rowIter2, closeIter2, err = ReadXlsxRows(tmpPath, sheet)
			}
		}
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "re-open file for load: " + err.Error()})
			return
		}

		rowCount, copyErr := CopyRowsInto(db, lakehouseSchema, tableName, headers, rowIter2)
		if closeIter2 != nil {
			closeIter2() //nolint:errcheck
		}
		if copyErr != nil {
			jsonResp(w, 500, map[string]string{"error": "copy rows: " + copyErr.Error()})
			return
		}

		sourceTable := lakehouseSchema + "." + tableName
		var objectTypeID string
		if err := db.QueryRow(`
			INSERT INTO ont_object_type
			  (project_id, name, display_name, kind,
			   source_table, mark, source_type, origin)
			VALUES
			  ($1, $2, $3, 'entity', $4, false, 'csv', 'manual-upload')
			ON CONFLICT (project_id, name) DO UPDATE
			  SET source_table = EXCLUDED.source_table,
			      origin       = EXCLUDED.origin
			RETURNING id`,
			projectID, tableName, tableName, sourceTable,
		).Scan(&objectTypeID); err != nil {
			jsonResp(w, 500, map[string]string{"error": "insert object type: " + err.Error()})
			return
		}

		for _, ic := range inferredCols {
			_, _ = db.Exec(`
				INSERT INTO ont_property
				  (project_id, object_type_id, name, display_name,
				   data_type, source_column, is_filterable, is_groupable, mark)
				VALUES
				  ($1, $2, $3, $4, $5, $6, true, true, false)
				ON CONFLICT (object_type_id, name) DO UPDATE
				  SET data_type = EXCLUDED.data_type`,
				projectID, objectTypeID, ic.Name, ic.Name, ic.DataType, ic.Name,
			)
		}

		// Insert lakehouse_table_status so the progress endpoint sees this table.
		// Reuse the latest import_id for this project so the progress query doesn't filter out PBIX rows.
		var importID string
		_ = db.QueryRow(`SELECT import_id FROM lakehouse_table_status WHERE project_id=$1 ORDER BY created_at DESC LIMIT 1`, projectID).Scan(&importID)
		if importID == "" {
			importID = "manual-" + objectTypeID[:8]
		}
		_, _ = db.Exec(`
			INSERT INTO lakehouse_table_status
			  (project_id, import_id, table_name, source_type, partition_kind, status, row_count, column_count)
			VALUES ($1, $2, $3, 'csv', 'data', 'loaded', $4, $5)
			ON CONFLICT (project_id, import_id, table_name) DO UPDATE SET
			  status=EXCLUDED.status, row_count=EXCLUDED.row_count, column_count=EXCLUDED.column_count, updated_at=now()`,
			projectID, importID, tableName, int(rowCount), len(inferredCols),
		)

		// Merge new table into pbit_config JSON so the lakehouse page shows it.
		var configRaw []byte
		_ = db.QueryRow(`SELECT pbit_config FROM project WHERE id=$1`, projectID).Scan(&configRaw)
		var cfg parseResp
		if configRaw != nil {
			_ = json.Unmarshal(configRaw, &cfg)
		}
		// Build column list.
		newCols := make([]map[string]string, len(inferredCols))
		for ci, ic := range inferredCols {
			newCols[ci] = map[string]string{"name": ic.Name, "dataType": ic.DataType}
		}
		cfg.Tables = append(cfg.Tables, tablePreview{
			Name:        tableName,
			SourceType:  "csv",
			ColumnCount: len(inferredCols),
			Columns:     newCols,
		})
		if newConfig, err := json.Marshal(cfg); err == nil {
			_, _ = db.Exec(`UPDATE project SET pbit_config=$1 WHERE id=$2`, newConfig, projectID)
		}

		jsonResp(w, 200, map[string]interface{}{
			"objectTypeId": objectTypeID,
			"rowCount":     rowCount,
		})
	}
}

// readCSVFile returns headers + streaming row iterator for a CSV file.
func readCSVFile(path string) (headers []string, rowIter func() ([]string, error), closeFn func() error, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	headerRow, err := r.Read()
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("read CSV header: %w", err)
	}
	headers = headerRow

	iterFn := func() ([]string, error) {
		row, err := r.Read()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return row, nil
	}
	closeFn = func() error { return f.Close() }
	return headers, iterFn, closeFn, nil
}

// inferredColTypeToPbit converts InferredCol DataType strings (e.g. "bigint")
// back to the PBIT-style type names that pbitTypeToSQL understands, or just
// passes the SQL type through since CreateLakehouseTable uses pbitTypeToSQL
// which falls back to "text" for unknowns.
func inferredColTypeToPbit(sqlType string) string {
	// InferColumnTypes already returns SQL types: "text", "bigint", "double precision", "timestamp", "boolean".
	// pbitTypeToSQL maps PBIT names → SQL. We need the reverse, but since
	// CreateLakehouseTable calls pbitTypeToSQL, we use a synthetic PBIT-like value
	// that round-trips, or we just set the column DDL directly by reusing the
	// fact that unknown → "text" is a safe fallback.
	// Simpler: store the SQL type in the DataType field and rely on the fact that
	// pbitTypeToSQL("text") → "text", etc. For types not in the map, return "text".
	switch sqlType {
	case "bigint":
		return "int64"
	case "double precision":
		return "double"
	case "timestamp":
		return "datetime"
	case "boolean":
		return "boolean"
	default:
		return "string"
	}
}

// ─── GET /api/pbit-lakehouse/progress ────────────────────────────────────────

type progressTableRow struct {
	TableName     string `json:"tableName"`
	SourceType    string `json:"sourceType"`
	PartitionKind string `json:"partitionKind"`
	Status        string `json:"status"`
	FileName      string `json:"fileName,omitempty"`
	RowCount      *int   `json:"rowCount,omitempty"`
	ColumnCount   *int   `json:"columnCount,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
}

type progressResp struct {
	PbitConfig interface{}        `json:"pbitConfig"`
	Tables     []progressTableRow `json:"tables"`
}

func handleProgress(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		projectID := r.URL.Query().Get("projectId")
		if projectID == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}

		// Read pbit_config from project.
		var configRaw []byte
		err := db.QueryRow(`SELECT pbit_config FROM project WHERE id=$1`, projectID).Scan(&configRaw)
		if err != nil {
			jsonResp(w, 404, map[string]string{"error": "project not found"})
			return
		}

		if configRaw == nil {
			jsonResp(w, 200, progressResp{PbitConfig: nil, Tables: []progressTableRow{}})
			return
		}

		var pbitConfig interface{}
		if jsonErr := json.Unmarshal(configRaw, &pbitConfig); jsonErr != nil {
			pbitConfig = nil
		}

		// Read table status rows for this project — only the latest import_id.
		rows, err := db.Query(`
			SELECT table_name, source_type, partition_kind, status,
			       COALESCE(file_name,''), row_count, column_count, COALESCE(error_message,'')
			FROM lakehouse_table_status
			WHERE project_id=$1
			  AND import_id = (SELECT import_id FROM lakehouse_table_status WHERE project_id=$1 ORDER BY created_at DESC LIMIT 1)
			ORDER BY table_name`,
			projectID,
		)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "query status: " + err.Error()})
			return
		}
		defer rows.Close()

		var tables []progressTableRow
		for rows.Next() {
			var t progressTableRow
			var rowCount, colCount sql.NullInt64
			if scanErr := rows.Scan(&t.TableName, &t.SourceType, &t.PartitionKind, &t.Status,
				&t.FileName, &rowCount, &colCount, &t.ErrorMessage); scanErr != nil {
				continue
			}
			if rowCount.Valid {
				n := int(rowCount.Int64)
				t.RowCount = &n
			}
			if colCount.Valid {
				n := int(colCount.Int64)
				t.ColumnCount = &n
			}
			tables = append(tables, t)
		}
		if tables == nil {
			tables = []progressTableRow{}
		}

		jsonResp(w, 200, progressResp{PbitConfig: pbitConfig, Tables: tables})
	}
}

// ─── POST /api/pbit-lakehouse/validate-sql ──────────────────────────────────
// Validates that an object's semantic_sql + properties form a runnable query.
// Executes: SELECT od."p1", od."p2", ... FROM (semantic_sql) AS od LIMIT 10

func handleValidateSQL(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ObjectId  string `json:"objectId"`
			ProjectId string `json:"projectId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "decode body: " + err.Error()})
			return
		}
		if req.ObjectId == "" || req.ProjectId == "" {
			jsonResp(w, 400, map[string]string{"error": "objectId and projectId required"})
			return
		}

		// Read semantic_sql from object.
		var semanticSQL string
		if err := db.QueryRow(`SELECT COALESCE(semantic_sql,'') FROM ont_object_type WHERE id=$1`, req.ObjectId).Scan(&semanticSQL); err != nil {
			jsonResp(w, 404, map[string]string{"error": "object not found"})
			return
		}
		if semanticSQL == "" {
			jsonResp(w, 400, map[string]string{"error": "semantic_sql is empty"})
			return
		}

		// Validate the semantic SQL itself — just wrap with SELECT * ... LIMIT 10.
		// Column mapping to properties is a separate step.
		validationQuery := fmt.Sprintf(`SELECT * FROM (%s) AS od LIMIT 10`, semanticSQL)

		// Execute against the lakehouse schema with search_path set.
		var lakehouseSchema string
		_ = db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id=$1`, req.ProjectId).Scan(&lakehouseSchema)

		// Use a transaction so SET LOCAL takes effect.
		tx, txErr := db.Begin()
		if txErr != nil {
			jsonResp(w, 500, map[string]string{"error": "begin tx: " + txErr.Error()})
			return
		}
		defer tx.Rollback() //nolint:errcheck

		if lakehouseSchema != "" {
			if _, spErr := tx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(lakehouseSchema))); spErr != nil {
				jsonResp(w, 200, map[string]interface{}{"valid": false, "error": "set search_path: " + spErr.Error(), "query": validationQuery})
				return
			}
		}

		rows, err := tx.Query(validationQuery)
		if err != nil {
			jsonResp(w, 200, map[string]interface{}{
				"valid": false,
				"error": err.Error(),
				"query": validationQuery,
			})
			return
		}
		defer rows.Close()

		// Read columns, types, and rows.
		cols, _ := rows.Columns()
		colTypes, _ := rows.ColumnTypes()
		columnMeta := make([]map[string]string, len(cols))
		for i, col := range cols {
			dbType := "text"
			if colTypes != nil && i < len(colTypes) {
				dbType = colTypes[i].DatabaseTypeName()
				if dbType == "" {
					dbType = "text"
				}
			}
			columnMeta[i] = map[string]string{"name": col, "dataType": strings.ToLower(dbType)}
		}
		var sampleRows []map[string]interface{}
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			row := map[string]interface{}{}
			for i, col := range cols {
				v := vals[i]
				if b, ok := v.([]byte); ok {
					row[col] = string(b)
				} else {
					row[col] = v
				}
			}
			sampleRows = append(sampleRows, row)
		}
		if sampleRows == nil {
			sampleRows = []map[string]interface{}{}
		}

		jsonResp(w, 200, map[string]interface{}{
			"valid":      true,
			"query":      validationQuery,
			"columns":    cols,
			"columnMeta": columnMeta,
			"sampleRows": sampleRows,
			"rowCount":   len(sampleRows),
		})
	}
}

// ─── canonical query construction (duplicate-safe) ──────────────────────────
//
// Canonical wraps the user's semantic SQL into a derived table and re-projects
// each ontology property to its declared name. The semantic SQL can legally
// emit duplicate output column names (e.g. `SELECT T1.*, T2.*, T3."SS update"`
// where T1 already has a `"SS update"` column): PG accepts duplicate names in
// a top-level SELECT result set, which is why validate-sql passes. The
// wrapping `SELECT od."colname" ...` then has to resolve names on the derived
// table and bails with `column reference ... is ambiguous`.
//
// Fix — probe the semantic SQL's actual output columns with `LIMIT 0`, then
// give the derived table positional aliases `(_c1, _c2, ..., _cN)` so every
// column has a unique name regardless of duplicates upstream. Property
// resolution maps each property's source_column to the FIRST matching
// position (case/whitespace/underscore-folded key) and emits
// `od._cN AS "<propName>"`. Names never collide because we never reference
// the original (possibly duplicated) names from the wrapping SELECT.

type canonicalPropMapping struct {
	name      string
	sourceCol string
}

// normalizeCanonicalKey folds case, whitespace, and underscores into a single
// equivalence class so "SS update", "SS_update", "ss  update" all collapse to
// the same key. Mirrors normalizeColKey in
// services/lakehouse-sql-server/lakehouse/column_probe.go.
func normalizeCanonicalKey(s string) string {
	s = strings.TrimSpace(s)
	var sb strings.Builder
	prevSep := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '_' {
			if !prevSep {
				sb.WriteRune('_')
				prevSep = true
			}
			continue
		}
		sb.WriteRune(r)
		prevSep = false
	}
	return strings.ToUpper(strings.Trim(sb.String(), "_"))
}

// buildAlignedCanonical assembles the canonical query for an object using
// the positional-alias strategy. Must run inside a tx that already has
// SET LOCAL search_path applied — the LIMIT 0 probe needs it to resolve
// unqualified table references inside the semantic SQL.
//
// Returns (canonical, mapped, unmapped, err). `unmapped` lists property names
// that either had no source_column or whose source_column didn't match any
// semantic output column (after normalization).
func buildAlignedCanonical(tx *sql.Tx, objectID string) (string, []canonicalPropMapping, []string, error) {
	var semanticSQL string
	if err := tx.QueryRow(`SELECT COALESCE(semantic_sql,'') FROM ont_object_type WHERE id=$1`, objectID).Scan(&semanticSQL); err != nil {
		return "", nil, nil, fmt.Errorf("read object: %w", err)
	}
	semanticSQL = strings.TrimSpace(semanticSQL)
	for strings.HasSuffix(semanticSQL, ";") {
		semanticSQL = strings.TrimSpace(strings.TrimSuffix(semanticSQL, ";"))
	}
	if semanticSQL == "" {
		return "", nil, nil, fmt.Errorf("semantic_sql is empty")
	}

	propRows, err := tx.Query(`SELECT name, COALESCE(source_column,'') FROM ont_property WHERE object_type_id=$1 ORDER BY name`, objectID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("query properties: %w", err)
	}
	var props []canonicalPropMapping
	for propRows.Next() {
		var pm canonicalPropMapping
		if scanErr := propRows.Scan(&pm.name, &pm.sourceCol); scanErr != nil {
			propRows.Close()
			return "", nil, nil, scanErr
		}
		props = append(props, pm)
	}
	propRows.Close()

	var mapped []canonicalPropMapping
	var unmapped []string
	for _, p := range props {
		if p.sourceCol != "" {
			mapped = append(mapped, p)
		} else {
			unmapped = append(unmapped, p.name)
		}
	}

	if len(mapped) == 0 {
		return fmt.Sprintf(`SELECT od.* FROM (%s) AS od`, semanticSQL), mapped, unmapped, nil
	}

	probeRows, err := tx.Query(fmt.Sprintf("SELECT * FROM (%s) AS __probe LIMIT 0", semanticSQL))
	if err != nil {
		return "", mapped, unmapped, fmt.Errorf("probe semantic columns: %w", err)
	}
	realCols, colsErr := probeRows.Columns()
	probeRows.Close()
	if colsErr != nil {
		return "", mapped, unmapped, fmt.Errorf("probe columns: %w", colsErr)
	}
	if len(realCols) == 0 {
		return "", mapped, unmapped, fmt.Errorf("semantic SQL has no output columns")
	}

	// First-occurrence index — deterministic for duplicates and matches PG's
	// "first column wins" semantics on SELECT *.
	indexByKey := make(map[string]int, len(realCols))
	for i, c := range realCols {
		k := normalizeCanonicalKey(c)
		if _, exists := indexByKey[k]; !exists {
			indexByKey[k] = i
		}
	}

	posAliases := make([]string, len(realCols))
	for i := range realCols {
		posAliases[i] = fmt.Sprintf("_c%d", i+1)
	}
	posListQuoted := make([]string, len(posAliases))
	for i, a := range posAliases {
		posListQuoted[i] = `"` + a + `"`
	}

	var selectParts []string
	var resolvedMapped []canonicalPropMapping
	for _, p := range mapped {
		idx, ok := indexByKey[normalizeCanonicalKey(p.sourceCol)]
		if !ok {
			unmapped = append(unmapped, p.name)
			continue
		}
		selectParts = append(selectParts, fmt.Sprintf(`od."%s" AS %q`, posAliases[idx], p.name))
		resolvedMapped = append(resolvedMapped, p)
	}
	if len(selectParts) == 0 {
		return fmt.Sprintf(`SELECT od.* FROM (%s) AS od`, semanticSQL), nil, unmapped, nil
	}

	canonical := fmt.Sprintf(`SELECT %s FROM (%s) AS od(%s)`,
		strings.Join(selectParts, ", "),
		semanticSQL,
		strings.Join(posListQuoted, ", "))
	return canonical, resolvedMapped, unmapped, nil
}

// ─── POST /api/pbit-lakehouse/solidify-sql ──────────────────────────────────
// Persists the canonical query built by buildAlignedCanonical so downstream
// consumers (sync-property-keywords, ResolveQuery) see the duplicate-safe form.

func handleSolidifySQL(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ObjectId  string `json:"objectId"`
			ProjectId string `json:"projectId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "decode body: " + err.Error()})
			return
		}
		if req.ObjectId == "" || req.ProjectId == "" {
			jsonResp(w, 400, map[string]string{"error": "objectId and projectId required"})
			return
		}

		var lakehouseSchema string
		_ = db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id=$1`, req.ProjectId).Scan(&lakehouseSchema)

		tx, txErr := db.Begin()
		if txErr != nil {
			jsonResp(w, 500, map[string]string{"error": "begin tx: " + txErr.Error()})
			return
		}
		defer tx.Rollback() //nolint:errcheck

		if lakehouseSchema != "" {
			if _, spErr := tx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(lakehouseSchema))); spErr != nil {
				jsonResp(w, 500, map[string]string{"error": "set search_path: " + spErr.Error()})
				return
			}
		}

		canonicalQuery, mapped, unmapped, err := buildAlignedCanonical(tx, req.ObjectId)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}

		if _, err := tx.Exec(`UPDATE ont_object_type SET canonical_query=$1, validated_at=now(),
			user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['canonical_query']::text[]))),
			updated_at=now() WHERE id=$2`,
			canonicalQuery, req.ObjectId); err != nil {
			jsonResp(w, 500, map[string]string{"error": "update canonical_query: " + err.Error()})
			return
		}

		if err := tx.Commit(); err != nil {
			jsonResp(w, 500, map[string]string{"error": "commit: " + err.Error()})
			return
		}

		resp := map[string]interface{}{
			"success":        true,
			"canonicalQuery": canonicalQuery,
			"mappedCount":    len(mapped),
		}
		if len(unmapped) > 0 {
			resp["unmapped"] = unmapped
			resp["warning"] = fmt.Sprintf("%d properties unmapped (no source_column or no semantic output match): %s", len(unmapped), strings.Join(unmapped, ", "))
		}
		jsonResp(w, 200, resp)
	}
}

// ─── POST /api/pbit-lakehouse/test-canonical ────────────────────────────────
// Rebuilds the canonical query fresh from semantic_sql + properties (via
// buildAlignedCanonical) and runs `SELECT * FROM (canonical) cq LIMIT 10`.
// We do NOT trust the stored canonical_query — it may have been written by
// a prior solidify that used a different algorithm, which is exactly how a
// passing validate-sql can coexist with a failing test-canonical. Always
// rebuilding keeps the invariant "validate ok → test ok" intact, and we
// persist the fresh canonical so downstream consumers benefit too.

func handleTestCanonical(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ObjectId  string `json:"objectId"`
			ProjectId string `json:"projectId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "decode body: " + err.Error()})
			return
		}
		if req.ObjectId == "" || req.ProjectId == "" {
			jsonResp(w, 400, map[string]string{"error": "objectId and projectId required"})
			return
		}

		var lakehouseSchema string
		_ = db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id=$1`, req.ProjectId).Scan(&lakehouseSchema)

		tx, txErr := db.Begin()
		if txErr != nil {
			jsonResp(w, 500, map[string]string{"error": "begin tx: " + txErr.Error()})
			return
		}
		defer tx.Rollback() //nolint:errcheck

		if lakehouseSchema != "" {
			if _, spErr := tx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(lakehouseSchema))); spErr != nil {
				jsonResp(w, 200, map[string]interface{}{"valid": false, "error": "set search_path: " + spErr.Error()})
				return
			}
		}

		canonicalQuery, _, _, err := buildAlignedCanonical(tx, req.ObjectId)
		if err != nil {
			jsonResp(w, 200, map[string]interface{}{"valid": false, "error": err.Error()})
			return
		}

		// Refresh the stored canonical so sync-property-keywords + ResolveQuery
		// stop seeing stale, duplicate-unsafe SQL. Non-fatal on failure.
		if _, refreshErr := tx.Exec(`UPDATE ont_object_type SET canonical_query=$1, updated_at=now() WHERE id=$2`,
			canonicalQuery, req.ObjectId); refreshErr != nil {
			log.Printf("test-canonical: refresh canonical_query failed: %v", refreshErr)
		}

		testQuery := fmt.Sprintf(`SELECT * FROM (%s) AS cq LIMIT 10`, canonicalQuery)
		rows, qErr := tx.Query(testQuery)
		if qErr != nil {
			jsonResp(w, 200, map[string]interface{}{"valid": false, "error": qErr.Error(), "query": testQuery})
			return
		}

		cols, _ := rows.Columns()
		var sampleRows []map[string]interface{}
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...) //nolint:errcheck
			row := map[string]interface{}{}
			for i, col := range cols {
				v := vals[i]
				if b, ok := v.([]byte); ok {
					row[col] = string(b)
				} else {
					row[col] = v
				}
			}
			sampleRows = append(sampleRows, row)
		}
		rows.Close()
		if sampleRows == nil {
			sampleRows = []map[string]interface{}{}
		}

		if commitErr := tx.Commit(); commitErr != nil {
			log.Printf("test-canonical: commit refresh failed: %v", commitErr)
		}

		jsonResp(w, 200, map[string]interface{}{
			"valid":      true,
			"query":      testQuery,
			"columns":    cols,
			"sampleRows": sampleRows,
			"rowCount":   len(sampleRows),
		})
	}
}

// ─── POST /api/pbit-lakehouse/sync-property-keywords ────────────────────────
// Extracts DISTINCT values for a property's source_column from the canonical query,
// then saves them as lakehouse_keyword rows.
// MC=true → only first 5 non-null + property name. MC=false → all + property name.

func handleSyncPropertyKeywords(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			PropertyId string `json:"propertyId"`
			ProjectId  string `json:"projectId"`
			Force      bool   `json:"force"`
			// MachineCode, when non-nil, flips the property's is_machine_code flag
			// before syncing (true → ≤5 sampled values, false → full distinct set).
			// Lets callers like the keywords page do "启用 / 启用MC" in one atomic
			// call without a full-property PUT. nil = keep the current flag.
			MachineCode *bool `json:"machineCode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "decode body: " + err.Error()})
			return
		}
		if req.PropertyId == "" || req.ProjectId == "" {
			jsonResp(w, 400, map[string]string{"error": "propertyId and projectId required"})
			return
		}

		// Load property info.
		var propName, sourceColumn, objectTypeID string
		var isMC bool
		if err := db.QueryRow(`SELECT name, COALESCE(source_column,''), object_type_id, COALESCE(is_machine_code,false)
			FROM ont_property WHERE id=$1`, req.PropertyId).Scan(&propName, &sourceColumn, &objectTypeID, &isMC); err != nil {
			jsonResp(w, 404, map[string]string{"error": "property not found"})
			return
		}
		if sourceColumn == "" {
			jsonResp(w, 400, map[string]string{"error": "property has no source_column mapping"})
			return
		}

		// Optional MC override: update ONLY is_machine_code (never touches the
		// property's other fields), then let the rest of the handler treat the
		// new flag as authoritative — so "启用MC" prunes to ≤5 and "启用" restores
		// the full set, all via this one endpoint.
		if req.MachineCode != nil {
			isMC = *req.MachineCode
			db.Exec(`UPDATE ont_property SET is_machine_code=$2, updated_at=now() WHERE id=$1`, req.PropertyId, isMC) //nolint:errcheck
		}

		// Load canonical_query from the object.
		var canonicalQuery string
		if err := db.QueryRow(`SELECT COALESCE(canonical_query,'') FROM ont_object_type WHERE id=$1`, objectTypeID).Scan(&canonicalQuery); err != nil {
			jsonResp(w, 404, map[string]string{"error": "object not found"})
			return
		}
		if canonicalQuery == "" {
			jsonResp(w, 400, map[string]string{"error": "object has no canonical_query — run SOLIDIFY first"})
			return
		}

		// Build query using property name (= canonical query output alias), not source_column (= raw SQL column).
		// Canonical query: SELECT od."src_col" AS "prop_name" ... → output column is prop_name.
		// Validate: if property name is not in canonical_query output (not mapped), use source_column as fallback.
		queryCol := propName
		if !strings.Contains(canonicalQuery, `"`+propName+`"`) && !strings.Contains(canonicalQuery, "AS "+`"`+propName+`"`) {
			// Property name not found in canonical_query output — try source_column as fallback.
			if sourceColumn != "" && (strings.Contains(canonicalQuery, `"`+sourceColumn+`"`) || strings.Contains(canonicalQuery, sourceColumn)) {
				queryCol = sourceColumn
			} else {
				jsonResp(w, 400, map[string]interface{}{
					"error":   fmt.Sprintf("属性 %q 未在 canonical_query 中映射。请先在 Lakehouse Objects 详情页更新 SQL 映射，确保包含该列。", propName),
					"success": false,
				})
				return
			}
		}
		limitClause := ""
		if isMC {
			limitClause = " LIMIT 5"
		}
		distinctQuery := fmt.Sprintf(`SELECT DISTINCT cq.%q FROM (%s) AS cq WHERE cq.%q IS NOT NULL AND CAST(cq.%q AS TEXT) != ''%s`,
			queryCol, canonicalQuery, queryCol, queryCol, limitClause)

		// Execute with search_path.
		var lakehouseSchema string
		_ = db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id=$1`, req.ProjectId).Scan(&lakehouseSchema)

		// Pre-check: for non-MC, count distinct values first. If >200 and not forced, return count for confirmation.
		if !isMC && !req.Force {
			countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT DISTINCT cq.%q FROM (%s) AS cq WHERE cq.%q IS NOT NULL AND CAST(cq.%q AS TEXT) != '') AS sub`,
				queryCol, canonicalQuery, queryCol, queryCol)
			ctxTx, ctxErr := db.Begin()
			if ctxErr == nil {
				if lakehouseSchema != "" {
					ctxTx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(lakehouseSchema))) //nolint:errcheck
				}
				var cnt int
				if ctxTx.QueryRow(countQuery).Scan(&cnt) == nil && cnt > 200 {
					ctxTx.Rollback() //nolint:errcheck
					jsonResp(w, 200, map[string]interface{}{
						"needsConfirmation": true,
						"distinctCount":     cnt,
						"propertyName":      propName,
					})
					return
				}
				ctxTx.Rollback() //nolint:errcheck
			}
		}

		tx, txErr := db.Begin()
		if txErr != nil {
			jsonResp(w, 500, map[string]string{"error": "begin tx: " + txErr.Error()})
			return
		}
		defer tx.Rollback() //nolint:errcheck

		if lakehouseSchema != "" {
			if _, spErr := tx.Exec(fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pq.QuoteIdentifier(lakehouseSchema))); spErr != nil {
				jsonResp(w, 500, map[string]string{"error": "set search_path: " + spErr.Error()})
				return
			}
		}

		rows, err := tx.Query(distinctQuery)
		if err != nil {
			jsonResp(w, 200, map[string]interface{}{"success": false, "error": err.Error(), "query": distinctQuery})
			return
		}
		defer rows.Close()

		var values []string
		for rows.Next() {
			var val sql.NullString
			rows.Scan(&val)
			if val.Valid && val.String != "" {
				values = append(values, val.String)
			}
		}

		// Always add the property name itself as a keyword.
		values = append(values, propName)

		// Delete old keywords for this property, then insert new ones.
		tx.Exec(`DELETE FROM lakehouse_keyword WHERE property_id=$1`, req.PropertyId) //nolint:errcheck

		inserted := 0
		seen := map[string]bool{}
		for _, v := range values {
			lower := strings.ToLower(v)
			if seen[lower] {
				continue
			}
			seen[lower] = true
			isColName := strings.EqualFold(v, propName)
			_, err := tx.Exec(`INSERT INTO lakehouse_keyword (project_id, object_type_id, property_id, keyword, is_machine_code, is_column_name)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (property_id, keyword) DO UPDATE SET is_machine_code=EXCLUDED.is_machine_code, is_column_name=EXCLUDED.is_column_name, synced_at=now()`,
				req.ProjectId, objectTypeID, req.PropertyId, v, isMC, isColName)
			if err == nil {
				inserted++
			}
		}

		// Update property's keywords_synced_at.
		tx.Exec(`UPDATE ont_property SET keywords_synced_at=now(), updated_at=now() WHERE id=$1`, req.PropertyId) //nolint:errcheck

		if err := tx.Commit(); err != nil {
			jsonResp(w, 500, map[string]string{"error": "commit: " + err.Error()})
			return
		}

		jsonResp(w, 200, map[string]interface{}{
			"success":        true,
			"propertyId":     req.PropertyId,
			"propertyName":   propName,
			"isMachineCode":  isMC,
			"keywordCount":   inserted,
			"distinctValues": len(values) - 1, // exclude the property name itself
		})
	}
}

// ─── GET /api/pbit-lakehouse/lakehouse-keywords ─────────────────────────────
// Lists all lakehouse keywords for a project, with Od/property names.

func handleLakehouseKeywords(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method == http.MethodDelete {
			// DELETE with ?id=xxx removes a single keyword
			kwID := r.URL.Query().Get("id")
			if kwID == "" {
				jsonResp(w, 400, map[string]string{"error": "id required"})
				return
			}
			db.Exec(`DELETE FROM lakehouse_keyword WHERE id=$1`, kwID) //nolint:errcheck
			jsonResp(w, 200, map[string]interface{}{"success": true})
			return
		}
		if r.Method == http.MethodPost {
			// POST: add a single keyword. Accepts either a property anchor or
			// a metric-intent anchor. If metricIntentId is provided, propertyId
			// is optional and object_type_id is resolved from the intent instead.
			var req struct {
				ProjectID      string `json:"projectId"`
				PropertyID     string `json:"propertyId"`
				MetricIntentID string `json:"metricIntentId"`
				Keyword        string `json:"keyword"`
				IsColumnName   bool   `json:"isColumnName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ProjectID == "" || req.Keyword == "" {
				jsonResp(w, 400, map[string]string{"error": "projectId, keyword required"})
				return
			}
			if req.PropertyID == "" && req.MetricIntentID == "" {
				jsonResp(w, 400, map[string]string{"error": "propertyId or metricIntentId required"})
				return
			}

			var objectTypeID string
			var propertyIDArg interface{}
			var metricIntentIDArg interface{}
			if req.PropertyID != "" {
				if err := db.QueryRow(`SELECT object_type_id::text FROM ont_property WHERE id = $1`, req.PropertyID).Scan(&objectTypeID); err != nil {
					jsonResp(w, 400, map[string]string{"error": "property not found"})
					return
				}
				propertyIDArg = req.PropertyID
			}
			if req.MetricIntentID != "" {
				// Intent-anchored keyword: resolve object_id from the intent.
				// If a propertyId is also provided we trust the caller but
				// intent's object takes precedence when property is absent.
				var intentOD string
				if err := db.QueryRow(`SELECT object_id::text FROM lakehouse_metric_intent WHERE id = $1`, req.MetricIntentID).Scan(&intentOD); err != nil {
					jsonResp(w, 400, map[string]string{"error": "metric intent not found"})
					return
				}
				if objectTypeID == "" {
					objectTypeID = intentOD
				}
				metricIntentIDArg = req.MetricIntentID
			}

			_, err := db.Exec(`INSERT INTO lakehouse_keyword
					(project_id, object_type_id, property_id, metric_intent_id, keyword, is_column_name, is_machine_code)
				VALUES ($1, $2, $3, $4, $5, $6, false)
				ON CONFLICT (property_id, keyword) DO UPDATE SET
					is_column_name = EXCLUDED.is_column_name,
					metric_intent_id = EXCLUDED.metric_intent_id`,
				req.ProjectID, objectTypeID, propertyIDArg, metricIntentIDArg,
				req.Keyword, req.IsColumnName)
			if err != nil {
				jsonResp(w, 500, map[string]string{"error": err.Error()})
				return
			}
			jsonResp(w, 200, map[string]interface{}{"success": true})
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		projectID := r.URL.Query().Get("projectId")
		if projectID == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}

		// Pagination params.
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		if pageSize < 1 || pageSize > 1000 {
			pageSize = 200
		}

		// Filter params.
		searchQ := strings.TrimSpace(r.URL.Query().Get("search"))
		typeQ := r.URL.Query().Get("type")              // column | value
		mcQ := r.URL.Query().Get("mc")                  // mc | semantic
		odQ := r.URL.Query().Get("od")                  // exact od name
		propIDQ := r.URL.Query().Get("propertyId")      // exact property id
		matchMode := r.URL.Query().Get("matchMode")     // "exact" | "fuzzy" | "" (default: old ILIKE)
		hasOntology := r.URL.Query().Get("hasOntology") // "yes" | "no"
		propNameQ := r.URL.Query().Get("prop")          // property name filter

		// Build dynamic WHERE.
		where := "lk.project_id = $1"
		args := []interface{}{projectID}
		argIdx := 2

		if propIDQ != "" {
			where += fmt.Sprintf(" AND lk.property_id = $%d", argIdx)
			args = append(args, propIDQ)
			argIdx++
		}
		if searchQ != "" {
			if matchMode == "exact" {
				where += fmt.Sprintf(` AND (LOWER(lk.keyword) = LOWER($%d) OR EXISTS (SELECT 1 FROM unnest(COALESCE(lk.aliases,'{}')) _a WHERE LOWER(_a) = LOWER($%d)))`, argIdx, argIdx)
			} else if matchMode == "fuzzy" {
				where += fmt.Sprintf(` AND (lk.keyword ILIKE '%%'||$%d||'%%' OR EXISTS (SELECT 1 FROM unnest(COALESCE(lk.aliases,'{}')) _a WHERE _a ILIKE '%%'||$%d||'%%'))`, argIdx, argIdx)
			} else {
				where += fmt.Sprintf(` AND (lk.keyword ILIKE '%%'||$%d||'%%' OR o.name ILIKE '%%'||$%d||'%%' OR p.name ILIKE '%%'||$%d||'%%')`, argIdx, argIdx, argIdx)
			}
			args = append(args, searchQ)
			argIdx++
		}
		if typeQ == "column" {
			where += " AND COALESCE(lk.is_column_name, false) = true"
		} else if typeQ == "value" {
			where += " AND COALESCE(lk.is_column_name, false) = false"
		}
		if mcQ == "mc" {
			where += " AND lk.is_machine_code = true"
		} else if mcQ == "semantic" {
			where += " AND lk.is_machine_code = false"
		}
		if odQ != "" {
			where += fmt.Sprintf(" AND o.name = $%d", argIdx)
			args = append(args, odQ)
			argIdx++
		}
		if hasOntology == "yes" {
			where += " AND lk.orphan_at IS NULL"
		} else if hasOntology == "no" {
			where += " AND lk.orphan_at IS NOT NULL"
		}
		if propNameQ != "" {
			where += fmt.Sprintf(" AND p.name ILIKE '%%'||$%d||'%%'", argIdx)
			args = append(args, propNameQ)
			argIdx++
		}

		offset := (page - 1) * pageSize
		// property_id is optional (anchor-by-intent or anchor-by-metric rows
		// set it NULL), so we LEFT JOIN ont_property. Two anchor flavors are
		// surfaced:
		//   · metric_intent_id → legacy lakehouse_metric_intent (old indicator).
		//   · metric_id        → unified lakehouse_metric (new indicator).
		// New rows authored via the metric editor land on metric_id; without
		// this LEFT JOIN those rows showed up as "floating" (orphan) on the
		// keywords page even though they have a perfectly good anchor.
		query := fmt.Sprintf(`
			SELECT lk.id, lk.keyword, lk.is_machine_code, COALESCE(lk.is_column_name, false), lk.synced_at,
			       o.name AS od_name,
			       COALESCE(p.name,'') AS prop_name,
			       COALESCE(p.source_column,''), COALESCE(p.data_type, ''),
			       array_to_json(COALESCE(lk.aliases, '{}'))::text AS aliases_json,
			       (lk.orphan_at IS NOT NULL)::bool AS is_orphaned,
			       COALESCE(lk.metric_intent_id::text,'') AS metric_intent_id,
			       COALESCE(mi.name,'') AS intent_name,
			       COALESCE(mi.display_name,'') AS intent_display_name,
			       COALESCE(lk.metric_id::text,'') AS metric_id,
			       COALESCE(m.name,'') AS metric_name,
			       COALESCE(m.display_name,'') AS metric_display_name,
			       COUNT(*) OVER() AS total_count
			FROM lakehouse_keyword lk
			JOIN ont_object_type o ON o.id = lk.object_type_id
			LEFT JOIN ont_property p ON p.id = lk.property_id
			LEFT JOIN lakehouse_metric_intent mi ON mi.id = lk.metric_intent_id
			LEFT JOIN lakehouse_metric m ON m.id = lk.metric_id
			WHERE %s
			ORDER BY o.name, COALESCE(p.name, mi.name, m.name), lk.keyword
			LIMIT %d OFFSET %d`, where, pageSize, offset)

		rows, err := db.Query(query, args...)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()

		type kwRow struct {
			ID                string   `json:"id"`
			Keyword           string   `json:"keyword"`
			IsMachineCode     bool     `json:"isMachineCode"`
			IsColumnName      bool     `json:"isColumnName"`
			SyncedAt          string   `json:"syncedAt"`
			OdName            string   `json:"odName"`
			PropName          string   `json:"propName"`
			SourceColumn      string   `json:"sourceColumn"`
			DataType          string   `json:"dataType"`
			Aliases           []string `json:"aliases"`
			IsOrphaned        bool     `json:"isOrphaned"`
			MetricIntentID    string   `json:"metricIntentId,omitempty"`
			IntentName        string   `json:"intentName,omitempty"`
			IntentDisplayName string   `json:"intentDisplayName,omitempty"`
			// Unified-metric anchor (new lakehouse_metric table).
			MetricID          string   `json:"metricId,omitempty"`
			MetricName        string   `json:"metricName,omitempty"`
			MetricDisplayName string   `json:"metricDisplayName,omitempty"`
		}

		var list []kwRow
		var totalCount int
		for rows.Next() {
			var kw kwRow
			var syncedAt time.Time
			var aliasesJSON string
			rows.Scan(&kw.ID, &kw.Keyword, &kw.IsMachineCode, &kw.IsColumnName, &syncedAt,
				&kw.OdName, &kw.PropName, &kw.SourceColumn, &kw.DataType,
				&aliasesJSON, &kw.IsOrphaned,
				&kw.MetricIntentID, &kw.IntentName, &kw.IntentDisplayName,
				&kw.MetricID, &kw.MetricName, &kw.MetricDisplayName,
				&totalCount)
			kw.SyncedAt = syncedAt.Format(time.RFC3339)
			if aliasesJSON != "" && aliasesJSON != "[]" {
				json.Unmarshal([]byte(aliasesJSON), &kw.Aliases) //nolint:errcheck
			}
			if kw.Aliases == nil {
				kw.Aliases = []string{}
			}
			list = append(list, kw)
		}
		if list == nil {
			list = []kwRow{}
		}

		jsonResp(w, 200, map[string]interface{}{"data": list, "total": totalCount})
	}
}

// ─── GET /api/pbit-lakehouse/lakehouse-keywords/summary ──────────────────────
// Lightweight stats for the keywords page filter bar (no row data).

func handleLakehouseKeywordsSummary(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		projectID := r.URL.Query().Get("projectId")
		if projectID == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}

		var total, colCount, valCount, mcCount int
		db.QueryRow(`
			SELECT COUNT(*),
			       COUNT(*) FILTER (WHERE COALESCE(lk.is_column_name, false)),
			       COUNT(*) FILTER (WHERE NOT COALESCE(lk.is_column_name, false)),
			       COUNT(*) FILTER (WHERE lk.is_machine_code)
			FROM lakehouse_keyword lk WHERE lk.project_id = $1`, projectID).
			Scan(&total, &colCount, &valCount, &mcCount) //nolint:errcheck

		// List all enabled Ods (mark=true) for the project, regardless of
		// whether they currently have keywords — the dropdown is a navigation
		// aid, and empty-keyword Ods are exactly what the user wants to
		// discover here. mark=false Ods are disabled and excluded by recall,
		// so they don't belong in this picker either.
		rows, err := db.Query(`
			SELECT DISTINCT name
			FROM ont_object_type
			WHERE project_id = $1 AND mark = true
			ORDER BY name`, projectID)
		var odNames []string
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var n string
				rows.Scan(&n) //nolint:errcheck
				odNames = append(odNames, n)
			}
		}
		if odNames == nil {
			odNames = []string{}
		}

		jsonResp(w, 200, map[string]interface{}{
			"total":       total,
			"columnCount": colCount,
			"valueCount":  valCount,
			"mcCount":     mcCount,
			"odNames":     odNames,
		})
	}
}

// ─── GET /api/pbit-lakehouse/lakehouse-keywords/tree ─────────────────────────
// Structural Od→Prop tree for the grouped view. Independent of keyword
// pagination so that large projects (e.g. 20k+ keywords) still render every
// mark=true Od and every Prop that has at least one keyword, instead of the
// accidental alphabetical slice that page-1 of /lakehouse-keywords produces.
//
// Scope: only mark=true Ods. A property shows up if it has any keyword under
// one of those Ods — props with zero keywords are omitted (the page exists
// to manage keyword bindings, so empty props add no value here).
//
// Shape:
// {
//   "ods": [
//     { "name": "ORDER",
//       "colCount": 35, "valCount": 22841, "propCount": 15,
//       "props": [
//         { "name": "COUNTRY", "dataType": "text",
//           "colCount": 5, "valCount": 12 },
//         ...
//       ] },
//     ...
//   ]
// }

func handleLakehouseKeywordsTree(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		projectID := r.URL.Query().Get("projectId")
		if projectID == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}

		// Single query aggregates per (od, prop). COUNT(p.id) is 0 when
		// property_id is NULL (intent-anchored rows), which we don't list in
		// the tree — grouping those by metric intent is a separate concern.
		rows, err := db.Query(`
			SELECT o.name  AS od_name,
			       p.id::text AS prop_id,
			       COALESCE(p.name,'')      AS prop_name,
			       COALESCE(p.data_type,'') AS data_type,
			       COALESCE(p.is_machine_code, false) AS is_mc,
			       COUNT(*) FILTER (WHERE COALESCE(lk.is_column_name, false))     AS col_count,
			       COUNT(*) FILTER (WHERE NOT COALESCE(lk.is_column_name, false)) AS val_count
			FROM lakehouse_keyword lk
			JOIN ont_object_type o ON o.id = lk.object_type_id AND o.mark = true
			JOIN ont_property    p ON p.id = lk.property_id
			WHERE lk.project_id = $1
			GROUP BY o.name, p.id, p.name, p.data_type, p.is_machine_code
			ORDER BY o.name, p.name`, projectID)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()

		type propEntry struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			DataType      string `json:"dataType"`
			IsMachineCode bool   `json:"isMachineCode"`
			ColCount      int    `json:"colCount"`
			ValCount      int    `json:"valCount"`
		}
		type odEntry struct {
			Name      string      `json:"name"`
			PropCount int         `json:"propCount"`
			ColCount  int         `json:"colCount"`
			ValCount  int         `json:"valCount"`
			Props     []propEntry `json:"props"`
		}

		odIdx := map[string]int{}
		var ods []odEntry
		for rows.Next() {
			var odName, propID, propName, dataType string
			var colCount, valCount int
			var isMC bool
			if err := rows.Scan(&odName, &propID, &propName, &dataType, &isMC, &colCount, &valCount); err != nil {
				continue
			}
			i, ok := odIdx[odName]
			if !ok {
				i = len(ods)
				odIdx[odName] = i
				ods = append(ods, odEntry{Name: odName})
			}
			ods[i].Props = append(ods[i].Props, propEntry{
				ID: propID, Name: propName, DataType: dataType, IsMachineCode: isMC,
				ColCount: colCount, ValCount: valCount,
			})
			ods[i].ColCount += colCount
			ods[i].ValCount += valCount
			ods[i].PropCount++
		}
		if ods == nil {
			ods = []odEntry{}
		}

		jsonResp(w, 200, map[string]interface{}{"ods": ods})
	}
}

// ─── POST /api/pbit-lakehouse/lakehouse-keywords/toggle-column-name ──────────
// Toggles is_column_name for a single keyword.

func handleToggleColumnName(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID           string `json:"id"`
			IsColumnName bool   `json:"isColumnName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			jsonResp(w, 400, map[string]string{"error": "id required"})
			return
		}
		_, err := db.Exec(`UPDATE lakehouse_keyword SET is_column_name=$1 WHERE id=$2`, req.IsColumnName, req.ID)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, map[string]interface{}{"success": true})
	}
}

// ─── PUT /api/pbit-lakehouse/lakehouse-keywords/aliases ───────────────────────
// Updates the aliases array for a single keyword entry.
// Body: { "id": "<uuid>", "aliases": ["alias1", "alias2"] }

func handleLakehouseKeywordAliases(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID      string   `json:"id"`
			Aliases []string `json:"aliases"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			jsonResp(w, 400, map[string]string{"error": "id required"})
			return
		}
		if req.Aliases == nil {
			req.Aliases = []string{}
		}
		// Deduplicate and trim aliases.
		// Initialize as empty slice (not nil) so json.Marshal produces "[]" not
		// "null" — PostgreSQL's jsonb_array_elements_text rejects scalar JSON.
		seen := map[string]bool{}
		clean := []string{}
		for _, a := range req.Aliases {
			t := strings.TrimSpace(a)
			if t != "" && !seen[t] {
				seen[t] = true
				clean = append(clean, t)
			}
		}
		// Encode as JSON and use PostgreSQL's jsonb_array_elements_text to update TEXT[].
		aliasesJSON, _ := json.Marshal(clean)

		// Transactional 3-step sync so the alias_vector child table stays
		// consistent with lakehouse_keyword.aliases:
		//   1. Update main row aliases array
		//   2. DELETE child rows whose alias is no longer in the new set
		//   3. INSERT child rows for new aliases (alias_vector=NULL → "needs compute")
		// The keyword_vector itself is intentionally untouched — the keyword
		// string didn't change so its embedding is still valid.
		tx, err := db.Begin()
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "tx begin: " + err.Error()})
			return
		}
		defer tx.Rollback()

		if _, err := tx.Exec(
			`UPDATE lakehouse_keyword
			    SET aliases = ARRAY(SELECT jsonb_array_elements_text($1::jsonb)),
			        updated_at = now()
			  WHERE id = $2`,
			string(aliasesJSON), req.ID,
		); err != nil {
			jsonResp(w, 500, map[string]string{"error": "update aliases: " + err.Error()})
			return
		}

		if _, err := tx.Exec(
			`DELETE FROM lakehouse_keyword_alias_vector
			  WHERE keyword_id = $1
			    AND alias NOT IN (SELECT jsonb_array_elements_text($2::jsonb))`,
			req.ID, string(aliasesJSON),
		); err != nil {
			jsonResp(w, 500, map[string]string{"error": "prune alias vectors: " + err.Error()})
			return
		}

		if _, err := tx.Exec(
			`INSERT INTO lakehouse_keyword_alias_vector (keyword_id, alias, alias_vector)
			 SELECT $1, a, NULL
			   FROM jsonb_array_elements_text($2::jsonb) a
			 ON CONFLICT (keyword_id, alias) DO NOTHING`,
			req.ID, string(aliasesJSON),
		); err != nil {
			jsonResp(w, 500, map[string]string{"error": "insert alias placeholders: " + err.Error()})
			return
		}

		if err := tx.Commit(); err != nil {
			jsonResp(w, 500, map[string]string{"error": "tx commit: " + err.Error()})
			return
		}

		jsonResp(w, 200, map[string]interface{}{"success": true, "aliases": clean})
	}
}

// ─── /api/pbit-lakehouse/lakehouse-keywords/column-alias ────────────────────
//
// Canonical-aware add/remove for column aliases. Backs the "列别名" dialog on
// /lakehouse/ontology/lakehouse-objects/detail so each property keeps a SINGLE
// canonical keyword row (keyword = property.name, is_column_name = true) and
// every user-visible alias lives inside that row's aliases[] array. This
// mirrors the pattern used by /ontology/keyword-triage/assign (see
// handler_triage.go recordAliasOnCanonical) — kept in this package so the
// detail page can stay on its existing /pbit-lakehouse/* namespace.
//
//	POST   body: { projectId, propertyId, alias }
//	DELETE query: projectId, propertyId, alias
//
// Both sync the alias_vector child table (INSERT NULL-vector on add so the
// recompute job fills it in; DELETE the matching child row on remove).
func handleColumnAliasCanonical(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch r.Method {
		case http.MethodPost:
			var req struct {
				ProjectID  string `json:"projectId"`
				PropertyID string `json:"propertyId"`
				Alias      string `json:"alias"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				jsonResp(w, 400, map[string]string{"error": "decode body: " + err.Error()})
				return
			}
			alias := strings.TrimSpace(req.Alias)
			if req.ProjectID == "" || req.PropertyID == "" || alias == "" {
				jsonResp(w, 400, map[string]string{"error": "projectId, propertyId, alias required"})
				return
			}

			tx, err := db.Begin()
			if err != nil {
				jsonResp(w, 500, map[string]string{"error": "tx begin: " + err.Error()})
				return
			}
			defer tx.Rollback()

			var odID, propName string
			if err := tx.QueryRow(
				`SELECT object_type_id::text, name FROM ont_property WHERE id = $1`,
				req.PropertyID,
			).Scan(&odID, &propName); err != nil {
				jsonResp(w, 400, map[string]string{"error": "property not found: " + err.Error()})
				return
			}

			// Find canonical row: is_column_name=true on this property. Prefer
			// the row whose keyword equals property.name (the natural canonical
			// spelling) but fall back to any column-name row if legacy data
			// predates that convention.
			var canonicalID string
			err = tx.QueryRow(`
				SELECT id::text FROM lakehouse_keyword
				 WHERE project_id = $1 AND property_id = $2
				   AND COALESCE(is_column_name,false) = true
				   AND COALESCE(is_stopword,false) = false
				 ORDER BY (LOWER(keyword) = LOWER($3)) DESC, synced_at DESC
				 LIMIT 1`,
				req.ProjectID, req.PropertyID, propName,
			).Scan(&canonicalID)
			switch err {
			case nil:
				// found
			case sql.ErrNoRows:
				if err := tx.QueryRow(`
					INSERT INTO lakehouse_keyword
					    (project_id, object_type_id, property_id, keyword, is_column_name)
					VALUES ($1, $2, $3, $4, true) RETURNING id::text`,
					req.ProjectID, odID, req.PropertyID, propName,
				).Scan(&canonicalID); err != nil {
					jsonResp(w, 500, map[string]string{"error": "create canonical: " + err.Error()})
					return
				}
			default:
				jsonResp(w, 500, map[string]string{"error": "lookup canonical: " + err.Error()})
				return
			}

			// Skip when alias == canonical — the row alone expresses the mapping.
			if !strings.EqualFold(alias, propName) {
				if _, err := tx.Exec(`
					UPDATE lakehouse_keyword
					   SET aliases = ARRAY(
					       SELECT DISTINCT a FROM (
					           SELECT a FROM unnest(COALESCE(aliases,'{}'::text[])) a
					            WHERE LOWER(a) <> LOWER($2)
					           UNION ALL
					           SELECT $2
					       ) sub(a)
					       ORDER BY a
					   ),
					       updated_at = now(),
					       synced_at  = now()
					 WHERE id = $1`,
					canonicalID, alias,
				); err != nil {
					jsonResp(w, 500, map[string]string{"error": "update aliases: " + err.Error()})
					return
				}
				if _, err := tx.Exec(`
					INSERT INTO lakehouse_keyword_alias_vector (keyword_id, alias)
					VALUES ($1, $2)
					ON CONFLICT (keyword_id, alias) DO NOTHING`,
					canonicalID, alias,
				); err != nil {
					jsonResp(w, 500, map[string]string{"error": "insert alias vector: " + err.Error()})
					return
				}
			}

			if err := tx.Commit(); err != nil {
				jsonResp(w, 500, map[string]string{"error": "tx commit: " + err.Error()})
				return
			}
			jsonResp(w, 200, map[string]interface{}{"success": true, "canonicalId": canonicalID})

		case http.MethodDelete:
			projectID := r.URL.Query().Get("projectId")
			propertyID := r.URL.Query().Get("propertyId")
			alias := strings.TrimSpace(r.URL.Query().Get("alias"))
			if projectID == "" || propertyID == "" || alias == "" {
				jsonResp(w, 400, map[string]string{"error": "projectId, propertyId, alias required"})
				return
			}

			tx, err := db.Begin()
			if err != nil {
				jsonResp(w, 500, map[string]string{"error": "tx begin: " + err.Error()})
				return
			}
			defer tx.Rollback()

			// Strip alias from every column-name row on this property (not just
			// the canonical one) so legacy duplicates are swept along.
			if _, err := tx.Exec(`
				UPDATE lakehouse_keyword
				   SET aliases = ARRAY(
				       SELECT a FROM unnest(COALESCE(aliases,'{}'::text[])) a
				        WHERE LOWER(a) <> LOWER($3)
				   ),
				       updated_at = now(),
				       synced_at  = now()
				 WHERE project_id = $1 AND property_id = $2
				   AND COALESCE(is_column_name,false) = true
				   AND EXISTS (
				       SELECT 1 FROM unnest(COALESCE(aliases,'{}'::text[])) a
				        WHERE LOWER(a) = LOWER($3)
				   )`,
				projectID, propertyID, alias,
			); err != nil {
				jsonResp(w, 500, map[string]string{"error": "strip alias: " + err.Error()})
				return
			}
			if _, err := tx.Exec(`
				DELETE FROM lakehouse_keyword_alias_vector lav
				 USING lakehouse_keyword lk
				 WHERE lav.keyword_id = lk.id
				   AND lk.project_id = $1 AND lk.property_id = $2
				   AND COALESCE(lk.is_column_name,false) = true
				   AND LOWER(lav.alias) = LOWER($3)`,
				projectID, propertyID, alias,
			); err != nil {
				jsonResp(w, 500, map[string]string{"error": "strip alias vector: " + err.Error()})
				return
			}

			if err := tx.Commit(); err != nil {
				jsonResp(w, 500, map[string]string{"error": "tx commit: " + err.Error()})
				return
			}
			jsonResp(w, 200, map[string]interface{}{"success": true})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// ─── POST /api/pbit-lakehouse/ensure-property-knowledge ─────────────────────
// Batch-creates missing ont_knowledge entries for all properties that lack one.

func handleEnsurePropertyKnowledge(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ProjectId string `json:"projectId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "decode body: " + err.Error()})
			return
		}
		if req.ProjectId == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}

		// Find all properties that don't have a matching ont_knowledge entry.
		rows, err := db.Query(`
			SELECT p.id, p.name, COALESCE(p.display_name,''), COALESCE(p.description,''),
			       COALESCE(p.source_column,''), COALESCE(p.data_type,''), p.object_type_id,
			       COALESCE(o.name,'')
			FROM ont_property p
			JOIN ont_object_type o ON o.id = p.object_type_id
			WHERE p.project_id = $1
			  AND NOT EXISTS (
			    SELECT 1 FROM ont_knowledge k
			    WHERE k.anchor_type = 'property' AND k.anchor_id = p.id
			  )`, req.ProjectId)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()

		created := 0
		for rows.Next() {
			var propID, propName, displayName, desc, sourceCol, dataType, objTypeID, objName string
			rows.Scan(&propID, &propName, &displayName, &desc, &sourceCol, &dataType, &objTypeID, &objName)

			title := propName
			if displayName != "" {
				title = displayName
			}
			summary := desc
			if summary == "" {
				summary = title
			}
			content := "## " + title + "\n\n- 所属对象: " + objName + "\n"
			if sourceCol != "" {
				content += "- 来源列: " + sourceCol + "\n"
			}
			if dataType != "" {
				content += "- 数据类型: " + dataType + "\n"
			}
			if desc != "" {
				content += "\n" + desc
			}

			var kid string
			err := db.QueryRow(`INSERT INTO ont_knowledge
				(project_id, title, summary, content, entry_type, anchor_type, anchor_id, sort_order, mark, note)
				VALUES ($1, $2, $3, $4, 'concept', 'property', $5, 0, true, '') RETURNING id`,
				req.ProjectId, title, summary, content, propID).Scan(&kid)
			if err == nil {
				created++
				// Create initial positive definition
				if desc != "" {
					db.Exec(`INSERT INTO ont_knowledge_definition (knowledge_id, def_type, content, sort_order, mark)
						VALUES ($1, 'positive', $2, 0, true)`, kid, desc) //nolint:errcheck
				}
			}
		}

		jsonResp(w, 200, map[string]interface{}{"created": created})
	}
}

// cleanOntologyRows removes prior ontology rows for a project (before re-population).
func cleanOntologyRows(tx *sql.Tx, projectID string) {
	tx.Exec(`DELETE FROM ont_property WHERE object_type_id IN (SELECT id FROM ont_object_type WHERE project_id=$1)`, projectID) //nolint:errcheck
	tx.Exec(`DELETE FROM ont_link_type WHERE project_id=$1`, projectID)                                                         //nolint:errcheck
	tx.Exec(`DELETE FROM ont_metric WHERE project_id=$1`, projectID)                                                            //nolint:errcheck
	tx.Exec(`DELETE FROM ont_object_type WHERE project_id=$1`, projectID)                                                       //nolint:errcheck
	tx.Exec(`DELETE FROM lakehouse_derived_view WHERE project_id=$1`, projectID)                                                //nolint:errcheck
}
