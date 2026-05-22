// Package file implements the File source connector for collector-server.
// Routes:
//
//	POST /api/connector/file/upload  (multipart)
//	POST /api/connector/file/url     (JSON body)
package file

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/authmw"
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

	// IDOR guard: the auth middleware only gates ?projectId= in the query
	// string; this route carries project_id in the multipart body, so verify
	// the bearer caller can access it (writes 401/403 on failure).
	if !authmw.EnforceProjectAccess(w, r, s.DB, projectID) {
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

	dsID := uuid.New().String()
	if label == "" {
		label = hdr.Filename
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
		os.RemoveAll(dirPath)
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	// Stream to disk while computing the content hash (used for dedup below).
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(file, s.MaxBytes+1))
	out.Close()

	if copyErr != nil {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusInternalServerError, "WRITE_FAILED", copyErr.Error())
		return
	}
	if written > s.MaxBytes {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE",
			fmt.Sprintf("uploaded %d bytes > limit %d", written, s.MaxBytes))
		return
	}
	contentHash := hex.EncodeToString(hasher.Sum(nil))

	// Parse headers synchronously — fast, returns the catalog the wizard needs.
	sheets, parseErr := parseHeaders(diskPath, ext)
	if parseErr != nil {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusUnprocessableEntity, "PARSE_FAILED", parseErr.Error())
		return
	}
	if len(sheets) == 0 {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusUnprocessableEntity, "EMPTY_FILE", "no readable sheets / headers found")
		return
	}

	stagingSchema := "collector_" + strings.ReplaceAll(dsID, "-", "_")
	configJSON := map[string]any{
		"filename": hdr.Filename,
		"size":     hdr.Size,
		"ext":      ext,
	}
	cfgRaw, _ := json.Marshal(configJSON)

	// Dedup + register atomically under the per-project lock so two
	// byte-identical concurrent uploads cannot both pass the check. The
	// data_source row, staging_schema, and the COPY job are all created here.
	var jobID string
	lockErr := pbit.WithProjectLock(ctx, s.DB, projectID, func(lctx context.Context) error {
		var existingID string
		dErr := s.DB.QueryRowContext(lctx, `
			SELECT id FROM data_source
			WHERE project_id = $1 AND content_hash = $2 AND status <> 'failed'
			LIMIT 1`, projectID, contentHash).Scan(&existingID)
		if dErr == nil {
			return &dupErr{existingID: existingID}
		}
		if !errors.Is(dErr, sql.ErrNoRows) {
			return fmt.Errorf("dedup check: %w", dErr)
		}

		// Create data_source row (status=syncing). Status starts as 'syncing'
		// rather than 'connecting' so existing UI that distinguishes
		// "in-progress" works without changes.
		if _, err := s.DB.ExecContext(lctx, `
			INSERT INTO data_source (id, project_id, type, label, config_json, status, staging_schema, content_hash)
			VALUES ($1, $2, 'file', $3, $4, 'syncing', $5, $6)
		`, dsID, projectID, label, cfgRaw, stagingSchema, contentHash); err != nil {
			return fmt.Errorf("db insert: %w", err)
		}

		// Enqueue COPY job. Worker pool runs writeAllSheetsToStaging asynchronously.
		jid, err := job.Enqueue(lctx, s.DB, job.EnqueueArgs{
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
			_, _ = s.DB.ExecContext(lctx, `DELETE FROM data_source WHERE id = $1`, dsID)
			return fmt.Errorf("enqueue: %w", err)
		}
		jobID = jid
		return nil
	})
	if lockErr != nil {
		os.RemoveAll(dirPath)
		var de *dupErr
		if errors.As(lockErr, &de) {
			writeError(w, http.StatusConflict, "DUPLICATE_CONTENT",
				"identical content already imported in this project; delete the existing source to re-upload")
			return
		}
		writeError(w, http.StatusInternalServerError, "DB_INSERT_FAILED", lockErr.Error())
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

// dupErr signals that an upload's content already exists in the project, so the
// handler should answer 409 instead of 500.
type dupErr struct{ existingID string }

func (e *dupErr) Error() string { return "duplicate content (data_source " + e.existingID + ")" }

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
