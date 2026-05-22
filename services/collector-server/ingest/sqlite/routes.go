package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/contracts"
	"github.com/lakehouse2ontology/services/collector-server/ingest/pgschema"
)

// RegisterRoutes mounts SQLite connector routes on mux.
//
//	POST /api/connector/sqlite/sources                  (multipart upload + auto-discover)
//	GET  /api/connector/sqlite/sources/{id}/catalog     (re-open file + Discover)
//	POST /api/connector/sqlite/sources/{id}/sync        (SSE: copy rows to Postgres staging)
//	GET  /api/connector/sqlite/sources/{id}/status      (read data_source.status)
func RegisterRoutes(mux *http.ServeMux, db *sql.DB) {
	s := &Service{
		DB:         db,
		UploadRoot: getEnv("FILE_UPLOAD_ROOT", "/data/uploads"),
		MaxBytes:   getMaxBytes(),
	}
	mux.HandleFunc("/api/connector/sqlite/sources", s.HandleUpload)
	mux.HandleFunc("/api/connector/sqlite/sources/", s.handleSourcesByID)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// SQLite-specific upload size cap. File-connector and SQLite-connector were
// originally sharing COLLECTOR_FILE_MAX_BYTES (100 MB default), but typical
// SQLite test/sample DBs (Northwind ~6 MB, Sakila ~5 MB, BookCorpus ~200 MB,
// IMDb dump ~400 MB) regularly exceed 100 MB. SQLite gets its own knob with
// a 500 MB default so the file connector's smaller-by-design limit stays.
func getMaxBytes() int64 {
	const def = 500 * 1024 * 1024
	s := os.Getenv("COLLECTOR_SQLITE_MAX_BYTES")
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		return def
	}
	return n
}

// handleSourcesByID dispatches /catalog /sync /status sub-routes.
func (s *Service) handleSourcesByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/connector/sqlite/sources/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid source id")
		return
	}
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}
	switch {
	case sub == "catalog" && r.Method == http.MethodGet:
		s.handleCatalog(w, r, id)
	case sub == "sync" && r.Method == http.MethodPost:
		s.handleSync(w, r, id)
	case sub == "status" && r.Method == http.MethodGet:
		s.handleStatus(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "not found")
	}
}

// GET /api/connector/sqlite/sources/{id}/catalog
// Re-opens the on-disk .sqlite file and re-discovers — same semantics as
// postgres /catalog. Useful when the wizard wants a fresh view (e.g. after
// the user replaces the file out-of-band).
func (s *Service) handleCatalog(w http.ResponseWriter, r *http.Request, id string) {
	// IDOR guard: by-id route, resolve project from the source and check access.
	if !authmw.EnforceEntityProject(w, r, s.DB, "data_source", "id", id) {
		return
	}
	dbPath, err := loadDiskPath(r.Context(), s.DB, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sdb, err := Open(ctx, dbPath)
	if err != nil {
		writeError(w, http.StatusBadGateway, "OPEN_FAILED", err.Error())
		return
	}
	defer sdb.Close()

	tables, err := Discover(ctx, sdb)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CATALOG_ERROR", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(contracts.CatalogResp{Tables: tables})
}

// POST /api/connector/sqlite/sources/{id}/sync (SSE)
// Body: { "tables": ["Album", "Artist", ...] }
//
// Same SSE shape as postgres connector: emits sync_started, optional per-table
// sync_progress, and sync_complete / sync_failed.
func (s *Service) handleSync(w http.ResponseWriter, r *http.Request, id string) {
	// IDOR guard: this by-id route carries no projectId; resolve the source's
	// project and confirm the caller can access it before any DB/file work.
	if !authmw.EnforceEntityProject(w, r, s.DB, "data_source", "id", id) {
		return
	}

	var req struct {
		Tables []string `json:"tables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Tables) == 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "tables array required")
		return
	}

	dbPath, err := loadDiskPath(r.Context(), s.DB, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	stagingSchema := "collector_" + strings.ReplaceAll(id, "-", "_")
	if err := pgschema.CreateSchemaWithGrants(r.Context(), s.DB, stagingSchema); err != nil {
		writeError(w, http.StatusInternalServerError, "SCHEMA_CREATE", err.Error())
		return
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE data_source SET status='syncing', staging_schema=$1, updated_at=now() WHERE id=$2`,
		stagingSchema, id,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	send := func(ev contracts.SyncProgressEvent) {
		data, _ := json.Marshal(ev)
		_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	send(contracts.SyncProgressEvent{Phase: "sync_started"})

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	sdb, err := Open(ctx, dbPath)
	if err != nil {
		send(contracts.SyncProgressEvent{Phase: "sync_failed", Error: err.Error()})
		_, _ = s.DB.ExecContext(r.Context(), `UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, id)
		return
	}
	defer sdb.Close()

	progress := func(table string, rowsDone int64) {
		send(contracts.SyncProgressEvent{
			Phase: "sync_progress", TableName: table, RowsSynced: rowsDone,
		})
	}
	if err := SyncTables(ctx, sdb, s.DB, stagingSchema, req.Tables, progress); err != nil {
		send(contracts.SyncProgressEvent{Phase: "sync_failed", Error: err.Error()})
		_, _ = s.DB.ExecContext(r.Context(), `UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, id)
		return
	}

	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE data_source SET status='ready', last_sync_at=now(), updated_at=now() WHERE id=$1`, id,
	); err != nil {
		send(contracts.SyncProgressEvent{Phase: "sync_failed", Error: err.Error()})
		return
	}
	send(contracts.SyncProgressEvent{Phase: "sync_complete"})
}

// GET /api/connector/sqlite/sources/{id}/status
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request, id string) {
	var status, stagingSchema sql.NullString
	var updatedAt time.Time
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT status, staging_schema, updated_at FROM data_source WHERE id=$1`, id,
	).Scan(&status, &stagingSchema, &updatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "data source not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":             id,
		"status":         status.String,
		"staging_schema": stagingSchema.String,
		"updated_at":     updatedAt,
	})
}
