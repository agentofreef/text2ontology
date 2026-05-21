// services/collector-server/cmd/server/main.go
//
// Phase 1: collector-server shell. Empty service exposing /healthz +
// /metrics + observability middleware. Business routes (PBI/Postgres/File
// ingest) come in Phase 2-4. See:
//   .omc/plans/collector-unified-consensus.md § Phase 1
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	_ "github.com/lib/pq"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/observability"
	fileingest "github.com/lakehouse2ontology/services/collector-server/ingest/file"
	pbit "github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
	pbixingest "github.com/lakehouse2ontology/services/collector-server/ingest/pbix"
	"github.com/lakehouse2ontology/services/collector-server/ingest/postgres"
	sqliteingest "github.com/lakehouse2ontology/services/collector-server/ingest/sqlite"
	"github.com/lakehouse2ontology/services/collector-server/ingest/wizard"
	"github.com/lakehouse2ontology/services/collector-server/job"
	"github.com/lakehouse2ontology/srvkit"
)

// listSourcesHandler handles GET /api/connector/sources?project_id=<uuid>
// Returns all data_source rows for a project, newest first.
func listSourcesHandler(db *sql.DB) http.HandlerFunc {
	type dataSourceRow struct {
		ID            string  `json:"id"`
		ProjectID     string  `json:"project_id"`
		Type          string  `json:"type"`
		Label         string  `json:"label"`
		Status        string  `json:"status"`
		ConfigJSON    string  `json:"config_json"`
		StagingSchema string  `json:"staging_schema"`
		LastSyncAt    *string `json:"last_sync_at"`
		CreatedAt     string  `json:"created_at"`
		UpdatedAt     string  `json:"updated_at"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		pid := r.URL.Query().Get("project_id")
		if pid == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "project_id required"})
			return
		}
		rows, err := db.QueryContext(r.Context(), `
			SELECT id, project_id, type, label, status,
			       COALESCE(config_json::text, '{}'),
			       COALESCE(staging_schema, ''),
			       to_char(last_sync_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
			       to_char(created_at  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
			       to_char(updated_at  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
			FROM data_source WHERE project_id = $1 ORDER BY created_at DESC
		`, pid)
		if err != nil {
			log.Printf("listSources: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		result := make([]dataSourceRow, 0)
		for rows.Next() {
			var s dataSourceRow
			if err := rows.Scan(&s.ID, &s.ProjectID, &s.Type, &s.Label, &s.Status,
				&s.ConfigJSON, &s.StagingSchema, &s.LastSyncAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
				continue
			}
			s.ConfigJSON = redactConfigJSON(s.ConfigJSON)
			result = append(result, s)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// lakehouseTablesHandler handles GET /api/connector/lakehouse/tables?project_id=<uuid>.
// Returns the actual tables that landed in the project's lakehouse schema
// (proj_<hex>) — what Confirm merged from staging. Source-type-agnostic;
// includes tables from postgres / pbi / file imports indistinguishably.
//
// Distinct from /api/ontology/objects which lists USER-AUTHORED ontology
// objects. The lakehouse view is "what raw data exists"; ontology is
// "what concepts the user has modelled on top".
func lakehouseTablesHandler(db *sql.DB) http.HandlerFunc {
	type column struct {
		Name     string `json:"name"`
		DataType string `json:"data_type"`
	}
	type table struct {
		Name      string   `json:"name"`
		Columns   []column `json:"columns"`
		RowCount  int64    `json:"row_count"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := r.URL.Query().Get("project_id")
		if pid == "" {
			pid = r.URL.Query().Get("projectId")
		}
		if _, err := uuid.Parse(pid); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "project_id required (uuid)"})
			return
		}
		// Schema name follows pbit.SanitizeSchemaName: "proj_" + hex (no dashes).
		schema := "proj_" + strings.ReplaceAll(pid, "-", "")

		// Collect column metadata in one round-trip.
		rows, err := db.QueryContext(r.Context(), `
			SELECT table_name, column_name, COALESCE(data_type, '')
			FROM information_schema.columns
			WHERE table_schema = $1
			ORDER BY table_name, ordinal_position`, schema)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		byTable := map[string]*table{}
		var order []string
		for rows.Next() {
			var tn, cn, dt string
			if err := rows.Scan(&tn, &cn, &dt); err != nil {
				continue
			}
			t, ok := byTable[tn]
			if !ok {
				t = &table{Name: tn, Columns: []column{}}
				byTable[tn] = t
				order = append(order, tn)
			}
			t.Columns = append(t.Columns, column{Name: cn, DataType: dt})
		}

		// Per-table row count. Use parameterised schema/table is impossible
		// (identifiers can't be parameters); we already validated pid is a
		// UUID, so the schema name is safe. Table names come from
		// information_schema and are always quoted via %q.
		out := make([]table, 0, len(order))
		for _, tn := range order {
			var n int64
			countSQL := fmt.Sprintf(`SELECT count(*) FROM %q.%q`, schema, tn)
			_ = db.QueryRowContext(r.Context(), countSQL).Scan(&n)
			t := byTable[tn]
			t.RowCount = n
			out = append(out, *t)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema": schema, "tables": out})
	}
}

// sourceByIDHandler handles /api/connector/sources/{id}:
//
//	GET    → return the single data_source row (cross-type, type-agnostic)
//	DELETE → drop the data_source row + its staging schema (cross-type cleanup)
//
// Used by the data-sources list "abandon" button and the wizard's pre-flight
// type discovery so the frontend can dispatch /catalog to the right connector
// (postgres / file / pbi).
func sourceByIDHandler(db *sql.DB) http.HandlerFunc {
	type dataSourceRow struct {
		ID            string  `json:"id"`
		ProjectID     string  `json:"project_id"`
		Type          string  `json:"type"`
		Label         string  `json:"label"`
		Status        string  `json:"status"`
		ConfigJSON    string  `json:"config_json"`
		StagingSchema string  `json:"staging_schema"`
		LastSyncAt    *string `json:"last_sync_at"`
		CreatedAt     string  `json:"created_at"`
		UpdatedAt     string  `json:"updated_at"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/connector/sources/{id}
		const prefix = "/api/connector/sources/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, prefix)
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			var s dataSourceRow
			err := db.QueryRowContext(r.Context(), `
				SELECT id, project_id, type, label, status,
				       COALESCE(config_json::text, '{}'),
				       COALESCE(staging_schema, ''),
				       to_char(last_sync_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
				       to_char(created_at  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
				       to_char(updated_at  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
				FROM data_source WHERE id = $1`, id).Scan(
				&s.ID, &s.ProjectID, &s.Type, &s.Label, &s.Status,
				&s.ConfigJSON, &s.StagingSchema, &s.LastSyncAt, &s.CreatedAt, &s.UpdatedAt)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "data source not found"})
				return
			}
			s.ConfigJSON = redactConfigJSON(s.ConfigJSON)
			_ = json.NewEncoder(w).Encode(s)

		case http.MethodDelete:
			// 1. Read project_id + staging_schema + wizard_state.table_roles
			//    so we can clean up both the staging schema (this source's
			//    private staging area) and the final lakehouse tables this
			//    source contributed.
			var projectID, stagingSchema string
			var tableNames []string
			_ = db.QueryRowContext(r.Context(), `
				SELECT project_id::text, COALESCE(staging_schema,''),
				       COALESCE(
				         array(SELECT jsonb_object_keys(wizard_state->'table_roles')),
				         '{}'::text[]
				       )
				FROM data_source WHERE id = $1`, id).Scan(&projectID, &stagingSchema, pq.Array(&tableNames))

			if _, err := db.ExecContext(r.Context(), `DELETE FROM data_source WHERE id = $1`, id); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}

			// 2. Drop the staging schema (CASCADE cleans up any leftover tables).
			if stagingSchema != "" {
				_, _ = db.ExecContext(r.Context(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, stagingSchema))
			}

			// 3. Drop the lakehouse tables this source merged into proj_<hex>.
			//    Schema name follows pbit.SanitizeSchemaName ("proj_" + hex).
			//    NOTE: if two sources contributed the same table name, deleting
			//    one will remove it for both — this is acceptable since data
			//    sources currently aren't expected to share table names.
			droppedTables := 0
			if projectID != "" && len(tableNames) > 0 {
				finalSchema := "proj_" + strings.ReplaceAll(projectID, "-", "")
				for _, tbl := range tableNames {
					if tbl == "" {
						continue
					}
					if _, err := db.ExecContext(r.Context(), fmt.Sprintf(`DROP TABLE IF EXISTS %q.%q CASCADE`, finalSchema, tbl)); err == nil {
						droppedTables++
					}
				}
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":         "deleted",
				"dropped_tables": droppedTables,
			})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		}
	}
}

func main() {
	port := flag.String("port", "8096", "listen port")
	flag.Parse()

	obsCtx, obsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	obsShutdown, err := observability.Init(obsCtx, "collector-server")
	obsCancel()
	if err != nil {
		log.Fatalf("observability.Init: %v", err)
	}
	defer func() {
		sdCtx, sdCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sdCancel()
		_ = obsShutdown(sdCtx)
	}()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := db.PingContext(pingCtx); err != nil {
		cancel()
		log.Fatalf("db.Ping: %v", err)
	}
	cancel()
	srvkit.TunePool(db)
	observability.SetDB(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service": "collector-server",
			"db":      "ok",
		})
	})
	mux.Handle("/metrics", observability.MetricsHandler())

	// Phase 2: PBI ingest routes at /api/connector/pbit/*.
	// PBIT_STAGING_ROOT env controls staging dir (default /data/pbit-staging).
	// PBIX_SCRIPTS_DIR env controls script lookup dir (default: relative ./scripts).
	pbit.RegisterRoutes(mux, db)
	// Phase 3: Postgres source connector + wizard state machine.
	postgres.RegisterRoutes(mux, db)
	// Phase 7: SQLite source connector (file upload → sqlite_master + PRAGMA
	// → bulk-copy into Postgres staging schema). Mirrors postgres routes.
	sqliteingest.RegisterRoutes(mux, db)
	wizard.RegisterRoutes(mux, db)
	go wizard.SweepStaleOnBoot(db)

	// Phase 6: ingest_job worker pool. Workers claim queued jobs (FOR UPDATE
	// SKIP LOCKED) and dispatch to the registered Handler for each kind.
	// Heartbeat every 5s; sweeper every 30s marks stale (>2min) jobs failed.
	jobRunner := job.NewRunner(db, 4)
	jobRunner.RegisterHandler(job.KindFileUpload, fileingest.HandleFileUploadJob)
	jobRunner.RegisterHandler(job.KindPbixExtract, pbixingest.HandlePbixExtractJob)
	go job.SweepStaleJobs(db) // boot scan
	go job.SweepOldJobs(db)   // boot retention cleanup
	jobCtx, jobCancel := context.WithCancel(context.Background())
	defer jobCancel()
	jobRunner.Start(jobCtx)
	go jobRunner.RunSweeper(jobCtx, 30*time.Second)

	// Phase 4: File source connector (upload + URL fetch + SSRF defence).
	fileingest.RegisterRoutes(mux, db)
	// pbix (.pbix binary) import: async via ingest_job + bounded semaphore.
	pbixingest.RegisterRoutes(mux, db)
	// Phase 5: cross-type sources listing endpoint.
	mux.HandleFunc("/api/connector/sources", listSourcesHandler(db))
	mux.HandleFunc("/api/connector/sources/", sourceByIDHandler(db))
	mux.HandleFunc("/api/connector/lakehouse/tables", lakehouseTablesHandler(db))

	// Auth middleware. authmw.Wrap passes /healthz, /metrics, OPTIONS, and
	// non-/api/* paths through unconditionally; every /api/connector/* call
	// now requires a valid bearer token. Without this, the entire ingest
	// surface (uploads, DB-credential storage, schema drops) is exposed
	// to anyone who can reach the port.
	auth := authmw.New(db, authmw.NewStdoutAuditWriter())

	// srvkit.RecoverMiddleware sits just inside CORS so handler panics are
	// recovered (→ 500) while CORS headers are still applied. Existing order
	// is otherwise preserved: CORS → recover → trace → span → auth → mux.
	handler := httputil.CORSMiddleware(os.Getenv("CORS_ALLOW_ORIGINS"))(
		srvkit.RecoverMiddleware(
			observability.TraceContextMiddleware(
				observability.ServerSpanMiddleware([]string{"/healthz", "/metrics"})(
					auth.Wrap(mux)))))

	addr := ":" + *port
	log.Printf("▼// collector-server listening on %s (DB OK, tracer=collector-server)", addr)

	// Graceful shutdown ordering:
	//   1. srvkit.Run drains in-flight HTTP requests on SIGINT/SIGTERM.
	//   2. OnShutdown cancels jobCtx → the ingest_job worker pool + sweeper
	//      stop claiming work (FOR UPDATE SKIP LOCKED design tolerates a hard
	//      stop: any 'running' rows are re-claimed or swept on next boot).
	//      We give workers a brief grace window to wind down before the
	//      deferred db.Close() fires, so they don't write to a closed pool.
	//   3. deferred db.Close() then obsShutdown() run (traces flush last).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err = srvkit.Run(ctx, addr, handler,
		srvkit.WithOnShutdown(func(context.Context) {
			jobCancel()
			// Brief grace period for in-flight job goroutines to observe the
			// cancellation and return before the DB pool closes.
			time.Sleep(500 * time.Millisecond)
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
}
