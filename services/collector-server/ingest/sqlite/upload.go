package sqlite

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
)

// Service holds shared state for SQLite connector handlers. Mirrors
// file.Service so the upload/disk-layout conventions stay aligned.
type Service struct {
	DB         *sql.DB
	UploadRoot string // FILE_UPLOAD_ROOT env (default /data/uploads)
	MaxBytes   int64  // COLLECTOR_FILE_MAX_BYTES env (default 100MB)
}

// HandleUpload — POST /api/connector/sqlite/sources (multipart)
//
// Form fields:
//
//	file       — .sqlite / .db / .sqlite3 (required)
//	project_id — UUID (required)
//	label      — optional display label (defaults to filename)
//
// Flow (synchronous — SQLite catalog is fast):
//  1. Save file to /data/uploads/{dsID}/{filename}
//  2. Open file via modernc.org/sqlite + PRAGMA schema_version (validates header)
//  3. Discover catalog (sqlite_master + per-table PRAGMA)
//  4. INSERT data_source(type='sqlite', status='pending') with disk_path in config_json
//  5. Return {id, status, catalog} so the wizard can render selections immediately.
//
// Sync (the actual rows-to-staging copy) happens later in POST /sources/{id}/sync.
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

	// Cap total body at MaxBytes + 10 MB headroom (multipart boundaries +
	// form fields). ParseMultipartForm's argument is the IN-MEMORY threshold,
	// not the total cap — anything past it spills to a temp file. Setting
	// it to s.MaxBytes would force a 500 MB SQLite to live entirely in RAM,
	// which is gratuitous; 32 MB lets typical headers stay in memory while
	// the .sqlite payload streams to disk.
	r.Body = http.MaxBytesReader(w, r.Body, s.MaxBytes+10*1024*1024)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
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

	// IDOR guard: project_id arrives in the multipart body, which the auth
	// middleware does NOT gate (it only gates ?projectId=). Verify access.
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
	if ext != ".sqlite" && ext != ".db" && ext != ".sqlite3" {
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_FILE_TYPE",
			"only .sqlite / .db / .sqlite3 supported")
		return
	}

	dsID := uuid.New().String()
	if label == "" {
		label = hdr.Filename
	}

	// 1. Save file to disk under /data/uploads/{dsID}/{filename}, hashing as we
	//    go (the sha256 is used for same-content dedup below).
	dirPath := filepath.Join(s.UploadRoot, dsID)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "MKDIR_FAILED", err.Error())
		return
	}
	diskPath := filepath.Join(dirPath, hdr.Filename)
	out, err := os.Create(diskPath)
	if err != nil {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}
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

	// 2. Validate by opening + 3. discover catalog (synchronous, fast for typical SQLite files).
	sqliteDB, err := Open(ctx, diskPath)
	if err != nil {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SQLITE", err.Error())
		return
	}
	catalog, err := Discover(ctx, sqliteDB)
	sqliteDB.Close()
	if err != nil {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusUnprocessableEntity, "DISCOVER_FAILED", err.Error())
		return
	}
	if len(catalog) == 0 {
		os.RemoveAll(dirPath)
		writeError(w, http.StatusUnprocessableEntity, "EMPTY_DB",
			"no user tables found in SQLite file")
		return
	}

	// 4. Dedup + insert data_source row under the per-project lock so two
	//    byte-identical concurrent uploads cannot both pass. status='pending' —
	//    staging is created in /sync.
	configJSON := map[string]any{
		"filename":  hdr.Filename,
		"size":      hdr.Size,
		"ext":       ext,
		"disk_path": diskPath,
	}
	cfgRaw, _ := json.Marshal(configJSON)
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
		if _, err := s.DB.ExecContext(lctx, `
			INSERT INTO data_source (id, project_id, type, label, config_json, status, content_hash)
			VALUES ($1, $2, 'sqlite', $3, $4, 'pending', $5)
		`, dsID, projectID, label, cfgRaw, contentHash); err != nil {
			return fmt.Errorf("db insert: %w", err)
		}
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

	// 5. Respond with id + catalog so the wizard renders immediately.
	resp := struct {
		ID      string                `json:"id"`
		Status  string                `json:"status"`
		Catalog contracts.CatalogResp `json:"catalog"`
	}{
		ID:      dsID,
		Status:  "pending",
		Catalog: contracts.CatalogResp{Tables: catalog},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// dupErr signals that an upload's content already exists in the project, so the
// handler should answer 409 instead of 500.
type dupErr struct{ existingID string }

func (e *dupErr) Error() string { return "duplicate content (data_source " + e.existingID + ")" }

// writeError writes a contracts.ErrorEnvelope as JSON.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(contracts.ErrorEnvelope{Code: code, Message: msg})
}

// loadDiskPath reads config_json from data_source and extracts disk_path.
// Returned to wizard.Confirm + the /catalog and /sync handlers so they can
// re-open the SQLite file.
func loadDiskPath(ctx context.Context, db *sql.DB, dsID string) (string, error) {
	var cfgRaw []byte
	err := db.QueryRowContext(ctx,
		`SELECT config_json FROM data_source WHERE id=$1 AND type='sqlite'`, dsID,
	).Scan(&cfgRaw)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(cfgRaw, &m); err != nil {
		return "", err
	}
	dp, _ := m["disk_path"].(string)
	if dp == "" {
		return "", fmt.Errorf("disk_path missing in config_json")
	}
	return dp, nil
}
