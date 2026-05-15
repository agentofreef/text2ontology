package file

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
)

// RegisterRoutes mounts File connector routes on mux.
// Routes:
//
//	POST /api/connector/file/upload            (multipart: file + project_id + label)
//	POST /api/connector/file/url               (JSON: {url, project_id, label})
//	GET  /api/connector/file/sources/{id}/catalog
func RegisterRoutes(mux *http.ServeMux, db *sql.DB) {
	s := &Service{
		DB:         db,
		UploadRoot: getEnv("FILE_UPLOAD_ROOT", "/data/uploads"),
		MaxBytes:   getMaxBytes(),
	}
	mux.HandleFunc("/api/connector/file/upload", s.HandleUpload)
	mux.HandleFunc("/api/connector/file/url", s.HandleURL)
	mux.HandleFunc("/api/connector/file/sources/", s.HandleSourcesByID)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getMaxBytes() int64 {
	s := os.Getenv("COLLECTOR_FILE_MAX_BYTES")
	if s == "" {
		return 100 * 1024 * 1024 // 100 MB default
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		return 100 * 1024 * 1024
	}
	return n
}
