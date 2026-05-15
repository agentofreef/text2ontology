// Package file implements the File source connector for collector-server.
// Routes:
//
//	POST /api/connector/file/upload  (multipart)
//	POST /api/connector/file/url     (JSON body)
package file

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/contracts"
	"github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
	"github.com/lakehouse2ontology/services/collector-server/job"
)

// HandleUpload — POST /api/connector/file/upload (multipart)
// Form fields: file (multipart), project_id, label (optional)
//
// Flow:
//  1. Save file to disk + INSERT data_source(status=syncing) + parseHeaders
//     (fast — only reads first row of each sheet)
//  2. Return {id, jobId, status='queued', catalog} immediately so the user
//     can start configuring the wizard while the COPY runs in background
//  3. Worker picks up the queued job, runs writeAllSheetsToStaging, sets
//     data_source.status='ready' on success / 'failed' on error
func (s *Service) HandleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}

	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "multipart/form-data") {
		writeError(w, http.StatusBadRequest, "INVALID_CONTENT_TYPE",
			"Content-Type must be multipart/form-data")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.MaxBytes+10*1024*1024)
	if err := r.ParseMultipartForm(s.MaxBytes); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()

	projectID := r.FormValue("project_id")
	label := r.FormValue("label")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_PROJECT_ID", "project_id required")
		return
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_FILE", err.Error())
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext != ".csv" && ext != ".xlsx" && ext != ".xls" {
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_FILE_TYPE",
			"only .csv .xlsx .xls supported")
		return
	}

	// Create data_source row (status=syncing) — see flow comment above.
	// Status starts as 'syncing' rather than 'connecting' so existing UI that
	// distinguishes "in-progress" works without changes.
	dsID := uuid.New().String()
	if label == "" {
		label = hdr.Filename
	}
	configJSON := map[string]any{
		"filename": hdr.Filename,
		"size":     hdr.Size,
		"ext":      ext,
	}
	cfgRaw, _ := json.Marshal(configJSON)
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO data_source (id, project_id, type, label, config_json, status)
		VALUES ($1, $2, 'file', $3, $4, 'syncing')
	`, dsID, projectID, label, cfgRaw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_INSERT_FAILED", err.Error())
		return
	}

	dirPath := filepath.Join(s.UploadRoot, dsID)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "MKDIR_FAILED", err.Error())
		return
	}
	// Strip directory components from the attacker-controlled multipart
	// filename. filepath.Join does NOT sanitize ".." traversal, so a
	// filename like "../../etc/cron.d/x" would escape UploadRoot.
	// filepath.Base reduces the name to its last path component on every OS.
	safeName := filepath.Base(hdr.Filename)
	if safeName == "" || safeName == "." || safeName == string(filepath.Separator) {
		safeName = "upload"
	}
	diskPath := filepath.Join(dirPath, safeName)
	out, err := os.Create(diskPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	written, copyErr := io.Copy(out, io.LimitReader(file, s.MaxBytes+1))
	out.Close()

	if copyErr != nil {
		os.Remove(diskPath)
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusInternalServerError, "WRITE_FAILED", copyErr.Error())
		return
	}
	if written > s.MaxBytes {
		os.Remove(diskPath)
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE",
			fmt.Sprintf("uploaded %d bytes > limit %d", written, s.MaxBytes))
		return
	}

	// Parse headers synchronously — fast, returns the catalog the wizard needs.
	sheets, parseErr := parseHeaders(diskPath, ext)
	if parseErr != nil {
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusUnprocessableEntity, "PARSE_FAILED", parseErr.Error())
		return
	}
	if len(sheets) == 0 {
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusUnprocessableEntity, "EMPTY_FILE", "no readable sheets / headers found")
		return
	}

	stagingSchema := "collector_" + strings.ReplaceAll(dsID, "-", "_")
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE data_source SET staging_schema=$1, updated_at=now() WHERE id=$2`,
		stagingSchema, dsID); err != nil {
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusInternalServerError, "DB_UPDATE_FAILED", err.Error())
		return
	}

	// Enqueue COPY job. Worker pool runs writeAllSheetsToStaging asynchronously.
	jobID, err := job.Enqueue(ctx, s.DB, job.EnqueueArgs{
		DataSourceID: &dsID,
		ProjectID:    projectID,
		Kind:         job.KindFileUpload,
		Payload: fileUploadPayload{
			DiskPath:      diskPath,
			Ext:           ext,
			StagingSchema: stagingSchema,
			Filename:      hdr.Filename,
		},
	})
	if err != nil {
		s.failDataSource(ctx, dsID)
		writeError(w, http.StatusInternalServerError, "JOB_ENQUEUE_FAILED", err.Error())
		return
	}

	tables := make([]contracts.TableInfo, 0, len(sheets))
	for _, sh := range sheets {
		tables = append(tables, contracts.TableInfo{Name: sh.Name, Columns: sh.Columns})
	}
	resp := struct {
		ID      string                `json:"id"`
		JobID   string                `json:"jobId"`
		Status  string                `json:"status"`
		Catalog contracts.CatalogResp `json:"catalog"`
	}{
		ID:      dsID,
		JobID:   jobID,
		Status:  "queued",
		Catalog: contracts.CatalogResp{Tables: tables},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// fileUploadPayload is the JSONB written into ingest_job.payload for jobs of
// kind file_upload. Both upload.go (multipart) and url.go (URL fetch) write
// the same shape so HandleFileUploadJob is source-agnostic.
type fileUploadPayload struct {
	DiskPath      string `json:"diskPath"`
	Ext           string `json:"ext"`
	StagingSchema string `json:"stagingSchema"`
	Filename      string `json:"filename"` // for progress messages
}

// HandleFileUploadJob is registered with job.Runner under KindFileUpload.
// It re-parses headers (fast) and runs writeAllSheetsToStaging with progress
// reporting. On success: data_source.status='ready'. On failure: 'failed'.
func HandleFileUploadJob(ctx context.Context, db *sql.DB, j *job.Job, rep *job.Reporter) error {
	var p fileUploadPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	if j.DataSourceID == nil {
		return fmt.Errorf("missing data_source_id")
	}
	dsID := *j.DataSourceID

	rep.Update("parsing", 0, 0, 0, 0, "解析表头")
	sheets, err := parseHeaders(p.DiskPath, p.Ext)
	if err != nil {
		failDataSource(ctx, db, dsID)
		return fmt.Errorf("parse headers: %w", err)
	}
	if len(sheets) == 0 {
		failDataSource(ctx, db, dsID)
		return fmt.Errorf("no readable sheets")
	}

	rep.Update("copy_staging", 0, 0, 0, 0,
		fmt.Sprintf("写入 staging · %d 个 sheet", len(sheets)))
	if err := writeAllSheetsToStaging(ctx, db, p.DiskPath, p.Ext, sheets, p.StagingSchema, rep); err != nil {
		failDataSource(ctx, db, dsID)
		return fmt.Errorf("write staging: %w", err)
	}

	if _, err := db.ExecContext(ctx,
		`UPDATE data_source SET status='ready', last_sync_at=now(), updated_at=now() WHERE id=$1`,
		dsID); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	rep.Update("done", 100, 0, 0, 0, "完成")
	return nil
}

// failDataSource is a package-level helper because the job handler doesn't
// have a Service receiver. Both Service.failDataSource and this delegate to
// the same UPDATE.
func failDataSource(ctx context.Context, db *sql.DB, dsID string) {
	_, _ = db.ExecContext(ctx,
		`UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, dsID)
}

// failDataSource on Service forwards to the package-level helper.
func (s *Service) failDataSource(ctx context.Context, dsID string) {
	failDataSource(ctx, s.DB, dsID)
}

// SheetInfo is one logical "sheet" (xlsx sheet or the entire CSV).
type SheetInfo struct {
	Name    string
	Columns []contracts.ColumnInfo
}

// parseHeaders returns one SheetInfo per logical sheet:
//   - .xlsx / .xls: one entry per non-empty sheet
//   - .csv:        one entry, name = filename without extension
func parseHeaders(path string, ext string) ([]SheetInfo, error) {
	switch ext {
	case ".xlsx", ".xls":
		shList, err := pbit.ReadXlsxAllSheetsHeaders(path)
		if err != nil {
			return nil, fmt.Errorf("xlsx headers: %w", err)
		}
		out := make([]SheetInfo, 0, len(shList))
		for _, sh := range shList {
			cols := make([]contracts.ColumnInfo, 0, len(sh.Headers))
			for _, h := range sh.Headers {
				if h == "" {
					continue
				}
				cols = append(cols, contracts.ColumnInfo{Name: h, DataType: "text"})
			}
			if len(cols) > 0 {
				out = append(out, SheetInfo{Name: sh.Name, Columns: cols})
			}
		}
		return out, nil

	case ".csv":
		headers, err := readCSVHeaders(path)
		if err != nil {
			return nil, fmt.Errorf("csv headers: %w", err)
		}
		cols := make([]contracts.ColumnInfo, 0, len(headers))
		for _, h := range headers {
			if h == "" {
				continue
			}
			cols = append(cols, contracts.ColumnInfo{Name: h, DataType: "text"})
		}
		if len(cols) == 0 {
			return nil, nil
		}
		base := filepath.Base(path)
		tableName := strings.TrimSuffix(base, filepath.Ext(base))
		return []SheetInfo{{Name: tableName, Columns: cols}}, nil

	default:
		return nil, fmt.Errorf("unsupported extension: %s", ext)
	}
}
