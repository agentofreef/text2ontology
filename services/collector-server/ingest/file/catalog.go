package file

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/contracts"
)

// HandleSourcesByID dispatches sub-routes under /api/connector/file/sources/{id}/.
//
//	GET /api/connector/file/sources/{id}/catalog
func (s *Service) HandleSourcesByID(w http.ResponseWriter, r *http.Request) {
	// Path: /api/connector/file/sources/{id}/catalog
	rest := strings.TrimPrefix(r.URL.Path, "/api/connector/file/sources/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	id, sub := parts[0], parts[1]
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "invalid source id", http.StatusBadRequest)
		return
	}

	switch {
	case sub == "catalog" && r.Method == http.MethodGet:
		s.handleCatalog(w, r, id)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// handleCatalog — GET /api/connector/file/sources/{id}/catalog
// Reads config_json from data_source to find the on-disk file path, re-parses
// its header row, and returns a CatalogResp with one table and its columns.
func (s *Service) handleCatalog(w http.ResponseWriter, r *http.Request, dsID string) {
	ctx := r.Context()

	var cfgRaw []byte
	err := s.DB.QueryRowContext(ctx,
		`SELECT config_json FROM data_source WHERE id = $1 AND type = 'file'`, dsID,
	).Scan(&cfgRaw)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "data source not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}

	var cfg map[string]any
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "CONFIG_ERROR", err.Error())
		return
	}

	// config_json stores "filename" and optionally "disk_path".
	// upload.go stores filename + ext; disk files live at $UploadRoot/$dsID/$filename.
	filename, _ := cfg["filename"].(string)
	if filename == "" {
		writeError(w, http.StatusUnprocessableEntity, "NO_FILE", "no filename in config_json")
		return
	}

	diskPath := filepath.Join(s.UploadRoot, dsID, filename)
	ext := strings.ToLower(filepath.Ext(filename))

	sheets, err := parseHeaders(diskPath, ext)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "PARSE_FAILED", err.Error())
		return
	}

	tables := make([]contracts.TableInfo, 0, len(sheets))
	for _, sh := range sheets {
		tables = append(tables, contracts.TableInfo{Name: sh.Name, Columns: sh.Columns})
	}
	resp := contracts.CatalogResp{Tables: tables}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
