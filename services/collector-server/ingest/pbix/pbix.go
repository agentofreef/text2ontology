// Package pbix implements the .pbix (Power BI Desktop binary) import path for
// collector-server.
//
// Unlike .pbit (a text DataModelSchema parsed natively in Go), a .pbix carries
// compressed VertiPaq columnar data that only the Python `pbix_to_csv.py`
// (pbixray + pandas) can read, so extraction is delegated to a subprocess.
// That subprocess is heavy (cold interpreter + whole-table-in-RAM decode), so
// to survive concurrent uploads we do NOT run it inline per request. Instead:
//
//   - the upload handler stores the file and enqueues an ingest_job
//     (kind=pbix_extract), returning 202-style {jobId} immediately;
//   - the job runs on the existing worker pool, further bounded by a counting
//     semaphore (COLLECTOR_PBIX_CONCURRENCY, default 2) so only N decodes run
//     at once regardless of request rate, plus a per-job timeout
//     (COLLECTOR_PBIX_TIMEOUT, default 10m) that kills a stuck subprocess.
//
// The Python emits per-table CSVs + a pbit-shaped metadata.json, which we load
// into the project's lakehouse schema and feed through the same
// pbit.PopulateOntology used by the .pbit path.
//
// Route: POST /api/connector/pbix/upload  (multipart: file, project_id, label?)
package pbix

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
	"github.com/lakehouse2ontology/services/collector-server/job"
)

// errCancelled is the in-flight sentinel for a user-cancelled extract job: it
// unwinds the project-locked load closure without marking the data_source
// failed (the worker's rep.Cancelled() path sets the terminal status).
var errCancelled = errors.New("pbix: cancelled by user")

// dupErr signals that an upload's content already exists in the project, so the
// handler should answer 409 instead of 500.
type dupErr struct{ existingID string }

func (e *dupErr) Error() string { return "duplicate content (data_source " + e.existingID + ")" }

// pbixSlots is a counting semaphore bounding concurrent pbix extractions. The
// decode is RAM/CPU heavy, so parallelism is capped here (NOT by request rate
// or worker count) to keep the host from OOMing under a burst of uploads.
var pbixSlots chan struct{}

func init() {
	n := 2
	if v := os.Getenv("COLLECTOR_PBIX_CONCURRENCY"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			n = p
		}
	}
	pbixSlots = make(chan struct{}, n)
}

func pbixTimeout() time.Duration {
	if v := os.Getenv("COLLECTOR_PBIX_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Minute
}

func uploadRoot() string {
	if v := os.Getenv("FILE_UPLOAD_ROOT"); v != "" {
		return v
	}
	return "/data/uploads"
}

func scriptsDir() string {
	if v := os.Getenv("PBIX_SCRIPTS_DIR"); v != "" {
		return v
	}
	return "./scripts"
}

func pythonBin() string {
	if v := os.Getenv("COLLECTOR_PYTHON"); v != "" {
		return v
	}
	return "python3"
}

func maxBytes() int64 {
	if v := os.Getenv("COLLECTOR_PBIX_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 500 * 1024 * 1024 // 500 MB
}

// RegisterRoutes wires the pbix upload endpoint.
func RegisterRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/connector/pbix/upload", handleUpload(db))
}

// pbixPayload is the JSONB written into ingest_job.payload for kind pbix_extract.
type pbixPayload struct {
	DiskPath string `json:"diskPath"`
	Filename string `json:"filename"`
}

// handleUpload — POST /api/connector/pbix/upload (multipart).
// Streams the .pbix to disk while hashing it, then under the per-project lock
// rejects byte-identical re-uploads (409), inserts data_source(status=syncing),
// and enqueues the extract job. Heavy decode happens later on the worker pool.
func handleUpload(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			writeCORS(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		writeCORS(w)
		ctx := r.Context()

		r.Body = http.MaxBytesReader(w, r.Body, maxBytes()+10*1024*1024)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "file too large: " + err.Error()})
			return
		}
		defer r.MultipartForm.RemoveAll()

		projectID := r.FormValue("project_id")
		label := r.FormValue("label")
		if projectID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "project_id required"})
			return
		}

		// IDOR guard: the auth middleware only gates ?projectId= in the query
		// string; this route carries project_id in the multipart body, so verify
		// the bearer caller can access it (writes 401/403 on failure).
		if !authmw.EnforceProjectAccess(w, r, db, projectID) {
			return
		}

		file, hdr, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing file"})
			return
		}
		defer file.Close()

		if strings.ToLower(filepath.Ext(hdr.Filename)) != ".pbix" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "only .pbix supported"})
			return
		}

		dsID := uuid.New().String()
		if label == "" {
			label = hdr.Filename
		}

		// Stream to disk while computing the content hash (used for dedup below).
		dir := filepath.Join(uploadRoot(), dsID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		diskPath := filepath.Join(dir, "source.pbix")
		out, err := os.Create(diskPath)
		if err != nil {
			os.RemoveAll(dir)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(file, maxBytes()+1))
		out.Close()
		if copyErr != nil {
			os.RemoveAll(dir)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": copyErr.Error()})
			return
		}
		if written > maxBytes() {
			os.RemoveAll(dir)
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "file exceeds size limit"})
			return
		}
		contentHash := hex.EncodeToString(hasher.Sum(nil))

		// Dedup + register atomically under the per-project lock, so two
		// byte-identical concurrent uploads cannot both pass the check.
		var jobID string
		lockErr := pbit.WithProjectLock(ctx, db, projectID, func(ctx context.Context) error {
			var existingID string
			err := db.QueryRowContext(ctx, `
				SELECT id FROM data_source
				WHERE project_id = $1 AND content_hash = $2 AND status <> 'failed'
				LIMIT 1`, projectID, contentHash).Scan(&existingID)
			if err == nil {
				return &dupErr{existingID: existingID}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("dedup check: %w", err)
			}

			cfg, _ := json.Marshal(map[string]any{"filename": hdr.Filename, "size": hdr.Size, "ext": ".pbix"})
			if _, err := db.ExecContext(ctx, `
				INSERT INTO data_source (id, project_id, type, label, config_json, status, content_hash)
				VALUES ($1, $2, 'pbi', $3, $4, 'syncing', $5)
			`, dsID, projectID, label, cfg, contentHash); err != nil {
				return fmt.Errorf("db insert: %w", err)
			}

			jid, err := job.Enqueue(ctx, db, job.EnqueueArgs{
				DataSourceID: &dsID,
				ProjectID:    projectID,
				Kind:         job.KindPbixExtract,
				Payload:      pbixPayload{DiskPath: diskPath, Filename: hdr.Filename},
			})
			if err != nil {
				_, _ = db.ExecContext(ctx, `DELETE FROM data_source WHERE id = $1`, dsID)
				return fmt.Errorf("enqueue: %w", err)
			}
			jobID = jid
			return nil
		})
		if lockErr != nil {
			os.RemoveAll(dir)
			var de *dupErr
			if errors.As(lockErr, &de) {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":      "identical content already imported in this project; delete the existing source to re-upload",
					"existingId": de.existingID,
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": lockErr.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"id": dsID, "jobId": jobID, "status": "queued"})
	}
}

// ── metadata.json (written by pbix_to_csv.py) ───────────────────────────────

type pbixMeta struct {
	Tables        []pbixMetaTable `json:"tables"`
	Relationships []pbixMetaRel   `json:"relationships"`
}

type pbixMetaTable struct {
	Name     string        `json:"name"`
	RowCount int64         `json:"rowCount"`
	CSVFile  string        `json:"csvFile"`
	Columns  []pbixMetaCol `json:"columns"`
}

type pbixMetaCol struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	IsKey    bool   `json:"isKey"`
}

type pbixMetaRel struct {
	FromTable   string `json:"fromTable"`
	FromColumn  string `json:"fromColumn"`
	ToTable     string `json:"toTable"`
	ToColumn    string `json:"toColumn"`
	Cardinality string `json:"cardinality"`
	IsActive    bool   `json:"isActive"`
}

// toPbitSchema maps the python metadata into the pbit schema struct so the
// ontology layer is generated by the same pbit.PopulateOntology used for .pbit.
func toPbitSchema(m *pbixMeta) *pbit.PbitSchema {
	s := &pbit.PbitSchema{}
	for _, t := range m.Tables {
		pt := pbit.PbitTable{Name: t.Name}
		for _, c := range t.Columns {
			pt.Columns = append(pt.Columns, pbit.PbitColumn{Name: c.Name, DataType: c.DataType})
		}
		s.Tables = append(s.Tables, pt)
	}
	for _, r := range m.Relationships {
		from, to := splitCard(r.Cardinality)
		s.Relationships = append(s.Relationships, pbit.PbitRelationship{
			FromTable:       r.FromTable,
			FromColumn:      r.FromColumn,
			ToTable:         r.ToTable,
			ToColumn:        r.ToColumn,
			FromCardinality: from,
			ToCardinality:   to,
			IsActive:        r.IsActive,
		})
	}
	return s
}

// splitCard turns a pbixray cardinality like "M:1" into ("M","1"). Anything
// without a ":" yields empty strings (pbit.mapCardinality falls back to M:M).
func splitCard(c string) (from, to string) {
	if i := strings.Index(c, ":"); i >= 0 {
		return c[:i], c[i+1:]
	}
	return "", ""
}

// decollideSchema rewrites the parsed schema's table names — and the table
// endpoints of every relationship — using the raw→final map produced by
// pbit.ResolveCollisionFreeNames, so the ont_object_type names PopulateOntology
// generates match the physical tables actually created (and links still resolve).
func decollideSchema(s *pbit.PbitSchema, nameMap map[string]string) *pbit.PbitSchema {
	mapName := func(n string) string {
		if v, ok := nameMap[n]; ok {
			return v
		}
		return n
	}
	for i := range s.Tables {
		s.Tables[i].Name = mapName(s.Tables[i].Name)
	}
	for i := range s.Relationships {
		s.Relationships[i].FromTable = mapName(s.Relationships[i].FromTable)
		s.Relationships[i].ToTable = mapName(s.Relationships[i].ToTable)
	}
	return s
}

// HandlePbixExtractJob is registered with job.Runner under KindPbixExtract.
// Bounded by the pbixSlots semaphore + a per-job timeout; runs pbix_to_csv.py,
// loads each table CSV into the project lakehouse schema, then populates the
// ontology. On any failure: data_source.status='failed' and the error bubbles
// to ingest_job.error.
func HandlePbixExtractJob(ctx context.Context, db *sql.DB, j *job.Job, rep *job.Reporter) error {
	var p pbixPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	if j.DataSourceID == nil {
		return fmt.Errorf("missing data_source_id")
	}
	dsID := *j.DataSourceID
	projectID := j.ProjectID

	// Bound concurrency before doing any heavy work.
	rep.Update("waiting_slot", 0, 0, 0, 0, "等待解析槽位")
	select {
	case pbixSlots <- struct{}{}:
		defer func() { <-pbixSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// Hard per-job timeout — exec.CommandContext kills the subprocess on expiry.
	runCtx, cancel := context.WithTimeout(ctx, pbixTimeout())
	defer cancel()

	outDir := filepath.Join(filepath.Dir(p.DiskPath), "extract")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		failDS(ctx, db, dsID)
		return fmt.Errorf("mkdir extract dir: %w", err)
	}

	rep.Update("extracting", 5, 0, 0, 0, "运行 pbixray 提取 (Python)")
	script := filepath.Join(scriptsDir(), "pbix_to_csv.py")
	cmd := exec.CommandContext(runCtx, pythonBin(), script, p.DiskPath, outDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		failDS(ctx, db, dsID)
		if runCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("pbix extract timed out after %s", pbixTimeout())
		}
		return fmt.Errorf("pbix extract failed: %v: %s", err, truncate(stderr.String(), 500))
	}

	metaRaw, err := os.ReadFile(filepath.Join(outDir, "metadata.json"))
	if err != nil {
		failDS(ctx, db, dsID)
		return fmt.Errorf("read metadata.json (stdout=%s): %w", truncate(string(stdout), 300), err)
	}
	var meta pbixMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		failDS(ctx, db, dsID)
		return fmt.Errorf("parse metadata.json: %w", err)
	}
	if len(meta.Tables) == 0 {
		failDS(ctx, db, dsID)
		return fmt.Errorf("pbix yielded no tables")
	}

	// Load each CSV into the project lakehouse schema. Physical columns are
	// created as text (robust under COPY); the typed ontology comes from
	// PopulateOntology below — mirrors the existing file-upload staging model.
	finalSchema := pbit.SanitizeSchemaName(projectID)
	lockErr := pbit.WithProjectLock(ctx, db, projectID, func(ctx context.Context) error {
		if err := pbit.CreateStagingSchema(db, finalSchema); err != nil {
			return fmt.Errorf("create lakehouse schema: %w", err)
		}

		// Map raw table names → names unique within the project (across all data
		// sources). Same-source retries reuse the original names.
		rawNames := make([]string, 0, len(meta.Tables))
		for _, t := range meta.Tables {
			rawNames = append(rawNames, t.Name)
		}
		nameMap, err := pbit.ResolveCollisionFreeNames(db, finalSchema, projectID, dsID, p.Filename, rawNames)
		if err != nil {
			return fmt.Errorf("resolve collision-free table names: %w", err)
		}

		total := len(meta.Tables)
		for i, t := range meta.Tables {
			if rep.Cancelled() {
				return errCancelled
			}
			tableName := nameMap[t.Name]
			header, rowIter, closeFn, oerr := openCSV(filepath.Join(outDir, t.CSVFile))
			if oerr != nil {
				return fmt.Errorf("open csv %q: %w", t.CSVFile, oerr)
			}

			cols := make([]pbit.PbitColumn, 0, len(header))
			for _, h := range header {
				cols = append(cols, pbit.PbitColumn{Name: h, DataType: "string"})
			}
			if err := pbit.CreateLakehouseTable(db, finalSchema, tableName, cols); err != nil {
				closeFn()
				return fmt.Errorf("create table %q: %w", tableName, err)
			}
			if len(header) > 0 {
				if _, err := pbit.CopyRowsInto(db, finalSchema, tableName, header, rowIter); err != nil {
					closeFn()
					return fmt.Errorf("copy into %q: %w", tableName, err)
				}
			}
			closeFn()

			pct := 10 + int(float64(i+1)/float64(total)*80)
			rep.Update("loading", pct, int64(i+1), int64(total), 0, fmt.Sprintf("加载表 %s", tableName))
		}

		// Ontology rows (typed from metadata, table names de-collided) in one
		// terminal transaction.
		rep.Update("ontology", 92, 0, 0, 0, "生成本体")
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin ontology tx: %w", err)
		}
		if _, err := pbit.PopulateOntology(tx, projectID, finalSchema, decollideSchema(toPbitSchema(&meta), nameMap), nil, dsID); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("populate ontology: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit ontology tx: %w", err)
		}

		if _, err := db.ExecContext(ctx, `
			UPDATE data_source SET status='ready', staging_schema=$1, last_sync_at=now(), updated_at=now()
			WHERE id=$2
		`, finalSchema, dsID); err != nil {
			return fmt.Errorf("mark data_source ready: %w", err)
		}

		// Point the project at the lakehouse schema so SmartQuery, the
		// lakehouse-sql UI, and the builder agent recognise the data as
		// configured — without this the project reads as empty even though the
		// tables + ontology landed. finalSchema is deterministic per project
		// (shared across all of its data sources), so this is idempotent under
		// multi-source coexistence; preserve an existing *-lakehouse label.
		if _, err := db.ExecContext(ctx, `
			UPDATE project
			SET lakehouse_schema = $1,
			    source_type = CASE WHEN source_type LIKE '%-lakehouse' THEN source_type ELSE 'pbix-lakehouse' END,
			    updated_at = now()
			WHERE id = $2
		`, finalSchema, projectID); err != nil {
			return fmt.Errorf("set project.lakehouse_schema: %w", err)
		}
		return nil
	})
	if lockErr != nil {
		if errors.Is(lockErr, errCancelled) {
			return nil // worker sets the cancelled terminal status via rep.Cancelled()
		}
		failDS(ctx, db, dsID)
		return lockErr
	}

	// Best-effort: drop the extracted CSVs (keep source.pbix for re-runs).
	_ = os.RemoveAll(outDir)
	rep.Update("done", 100, 0, 0, 0, "完成")
	return nil
}

// openCSV opens a CSV and returns its header plus a rowIter compatible with
// pbit.CopyRowsInto (returns nil row at EOF). closeFn closes the file.
func openCSV(path string) (header []string, rowIter func() ([]string, error), closeFn func(), err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	rd := csv.NewReader(f)
	rd.FieldsPerRecord = -1 // tolerate ragged rows
	rd.ReuseRecord = false
	header, err = rd.Read()
	if err != nil {
		f.Close()
		if err == io.EOF {
			// Empty file: no header, no rows.
			return []string{}, func() ([]string, error) { return nil, nil }, func() {}, nil
		}
		return nil, nil, nil, err
	}
	iter := func() ([]string, error) {
		rec, e := rd.Read()
		if e == io.EOF {
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		return rec, nil
	}
	return header, iter, func() { f.Close() }, nil
}

// ── small helpers ───────────────────────────────────────────────────────────

func writeCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func failDS(ctx context.Context, db *sql.DB, dsID string) {
	_, _ = db.ExecContext(ctx, `UPDATE data_source SET status='failed', updated_at=now() WHERE id=$1`, dsID)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
