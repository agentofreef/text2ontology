package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/contracts"
)

// RegisterRoutes mounts Postgres connector routes on mux.
// db is the collector's own Postgres (used for data_source CRUD).
// Routes:
//
//	POST /api/connector/postgres/test-connection
//	POST /api/connector/postgres/sources
//	GET  /api/connector/postgres/sources/{id}/catalog
//	POST /api/connector/postgres/sources/{id}/sync   (SSE)
//	GET  /api/connector/postgres/sources/{id}/status
func RegisterRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/connector/postgres/test-connection", handleTestConnection())
	mux.HandleFunc("/api/connector/postgres/sources", handleSources(db))
	mux.HandleFunc("/api/connector/postgres/sources/", handleSourcesByID(db))
}

// jsonResp writes status + JSON body.
func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ── POST /api/connector/postgres/test-connection ─────────────────────────────

func handleTestConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req contracts.TestConnectionReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, http.StatusBadRequest, contracts.ErrorEnvelope{
				Code: "BAD_REQUEST", Message: err.Error(),
			})
			return
		}
		port := req.Port
		if port == 0 {
			port = 5432
		}
		cfg := Config{
			Host:     req.Host,
			Port:     port,
			Database: req.Database,
			User:     req.User,
			Password: req.Password,
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := TestConnection(ctx, cfg); err != nil {
			jsonResp(w, http.StatusOK, contracts.TestConnectionResp{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		jsonResp(w, http.StatusOK, contracts.TestConnectionResp{OK: true})
	}
}

// ── POST /api/connector/postgres/sources ─────────────────────────────────────

func handleSources(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			createSource(w, r, db)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func createSource(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var req contracts.DataSourceCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, http.StatusBadRequest, contracts.ErrorEnvelope{
			Code: "BAD_REQUEST", Message: err.Error(),
		})
		return
	}
	if req.ProjectID == "" || req.Label == "" {
		jsonResp(w, http.StatusBadRequest, contracts.ErrorEnvelope{
			Code: "BAD_REQUEST", Message: "project_id and label are required",
		})
		return
	}
	cfgJSON, _ := json.Marshal(req.ConfigJSON)

	var id string
	err := db.QueryRowContext(r.Context(), `
		INSERT INTO data_source (project_id, type, label, config_json, status)
		VALUES ($1, 'postgres', $2, $3, 'pending')
		RETURNING id
	`, req.ProjectID, req.Label, string(cfgJSON)).Scan(&id)
	if err != nil {
		log.Printf("postgres/sources create: %v", err)
		jsonResp(w, http.StatusInternalServerError, contracts.ErrorEnvelope{
			Code: "DB_ERROR", Message: err.Error(),
		})
		return
	}
	jsonResp(w, http.StatusCreated, contracts.DataSourceCreateResp{
		ID:     id,
		Status: "pending",
	})
}

// ── /api/connector/postgres/sources/{id}/* ───────────────────────────────────

func handleSourcesByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/connector/postgres/sources/{id}[/catalog|/sync|/status]
		rest := strings.TrimPrefix(r.URL.Path, "/api/connector/postgres/sources/")
		parts := strings.SplitN(rest, "/", 2)
		id := parts[0]
		if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "invalid source id", http.StatusBadRequest)
			return
		}
		sub := ""
		if len(parts) == 2 {
			sub = parts[1]
		}

		switch {
		case sub == "catalog" && r.Method == http.MethodGet:
			getSourceCatalog(w, r, db, id)
		case sub == "sync" && r.Method == http.MethodPost:
			postSourceSync(w, r, db, id)
		case sub == "status" && r.Method == http.MethodGet:
			getSourceStatus(w, r, db, id)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

// GET /api/connector/postgres/sources/{id}/catalog
func getSourceCatalog(w http.ResponseWriter, r *http.Request, db *sql.DB, id string) {
	cfg, err := loadSourceConfig(r.Context(), db, id)
	if err != nil {
		jsonResp(w, http.StatusNotFound, contracts.ErrorEnvelope{
			Code: "NOT_FOUND", Message: err.Error(),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	srcDB, err := Open(ctx, cfg)
	if err != nil {
		jsonResp(w, http.StatusBadGateway, contracts.ErrorEnvelope{
			Code: "CONNECTION_FAILED", Message: err.Error(),
		})
		return
	}
	defer srcDB.Close()

	tables, err := Discover(ctx, srcDB)
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, contracts.ErrorEnvelope{
			Code: "CATALOG_ERROR", Message: err.Error(),
		})
		return
	}
	jsonResp(w, http.StatusOK, contracts.CatalogResp{Tables: tables})
}

// POST /api/connector/postgres/sources/{id}/sync  (SSE streaming)
//
// External PostgreSQL is wired in zero-copy via postgres_fdw foreign tables at
// wizard Confirm time (see wizard.confirmPostgresFDW) — no rows are copied and
// there is no staging schema. This endpoint is kept so the wizard's
// connect→sync→confirm progress flow still has a "sync" step to call: it just
// flips the source to 'ready' and emits the SSE events the frontend expects.
func postSourceSync(w http.ResponseWriter, r *http.Request, db *sql.DB, id string) {
	if _, err := db.ExecContext(r.Context(),
		`UPDATE data_source SET status='ready', updated_at=now() WHERE id=$1`, id,
	); err != nil {
		jsonResp(w, http.StatusInternalServerError, contracts.ErrorEnvelope{
			Code: "DB_ERROR", Message: err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	sendEvent := func(ev contracts.SyncProgressEvent) {
		data, _ := json.Marshal(ev)
		_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	sendEvent(contracts.SyncProgressEvent{Phase: "sync_started"})
	sendEvent(contracts.SyncProgressEvent{Phase: "sync_complete"})
}

// GET /api/connector/postgres/sources/{id}/status
func getSourceStatus(w http.ResponseWriter, r *http.Request, db *sql.DB, id string) {
	var status, stagingSchema sql.NullString
	var updatedAt time.Time
	err := db.QueryRowContext(r.Context(),
		`SELECT status, staging_schema, updated_at FROM data_source WHERE id=$1`, id,
	).Scan(&status, &stagingSchema, &updatedAt)
	if err == sql.ErrNoRows {
		jsonResp(w, http.StatusNotFound, contracts.ErrorEnvelope{
			Code: "NOT_FOUND", Message: "data source not found",
		})
		return
	}
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, contracts.ErrorEnvelope{
			Code: "DB_ERROR", Message: err.Error(),
		})
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"id":             id,
		"status":         status.String,
		"staging_schema": stagingSchema.String,
		"updated_at":     updatedAt,
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// loadSourceConfig reads config_json from data_source and builds a Config.
func loadSourceConfig(ctx context.Context, db *sql.DB, id string) (Config, error) {
	var cfgJSON []byte
	err := db.QueryRowContext(ctx,
		`SELECT config_json FROM data_source WHERE id=$1 AND type='postgres'`, id,
	).Scan(&cfgJSON)
	if err != nil {
		return Config{}, err
	}
	var m map[string]any
	if err := json.Unmarshal(cfgJSON, &m); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Host:     strVal(m, "host"),
		Database: strVal(m, "database"),
		User:     strVal(m, "user"),
		Password: strVal(m, "password"),
		SSLMode:  strVal(m, "ssl_mode"),
	}
	if p, ok := m["port"].(float64); ok {
		cfg.Port = int(p)
	}
	if cfg.Port == 0 {
		cfg.Port = 5432
	}
	return cfg, nil
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
