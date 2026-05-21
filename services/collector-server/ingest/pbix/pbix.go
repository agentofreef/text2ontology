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
	"database/sql"
	"encoding/csv"
	"encoding/json"
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
	"github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
	"github.com/lakehouse2ontology/services/collector-server/job"
)

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
// Saves the .pbix, inserts data_source(status=syncing), enqueues the extract
// job, and returns immediately. The heavy work happens on the worker pool.
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
		cfg, _ := json.Marshal(map[string]any{"filename": hdr.Filename, "size": hdr.Size, "ext": ".pbix"})
		if _, err := db.ExecContext(ctx, `
			INSERT INTO data_source (id, project_id, type, label, config_json, status)
			VALUES ($1, $2, 'pbi', $3, $4, 'syncing')
		`, dsID, projectID, label, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "db insert: " + err.Error()})
			return
		}

		dir := filepath.Join(uploadRoot(), dsID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			failDS(ctx, db, dsID)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		diskPath := filepath.Join(dir, "source.pbix")
		out, err := os.Create(diskPath)
		if err != nil {
			failDS(ctx, db, dsID)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		written, copyErr := io.Copy(out, io.LimitReader(file, maxBytes()+1))
		out.Close()
		if copyErr != nil {
			os.Remove(diskPath)
			failDS(ctx, db, dsID)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": copyErr.Error()})
			return
		}
		if written > maxBytes() {
			os.Remove(diskPath)
			failDS(ctx, db, dsID)
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "file exceeds size limit"})
			return
		}

		jobID, err := job.Enqueue(ctx, db, job.EnqueueArgs{
			DataSourceID: &dsID,
			ProjectID:    projectID,
			Kind:         job.KindPbixExtract,
			Payload:      pbixPayload{DiskPath: diskPath, Filename: hdr.Filename},
		})
		if err != nil {
			failDS(ctx, db, dsID)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "enqueue: " + err.Error()})
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
	if err := pbit.CreateStagingSchema(db, finalSchema); err != nil {
		failDS(ctx, db, dsID)
		return fmt.Errorf("create lakehouse schema: %w", err)
	}

	total := len(meta.Tables)
	for i, t := range meta.Tables {
		if rep.Cancelled() {
			return nil
		}
		header, rowIter, closeFn, oerr := openCSV(filepath.Join(outDir, t.CSVFile))
		if oerr != nil {
			failDS(ctx, db, dsID)
			return fmt.Errorf("open csv %q: %w", t.CSVFile, oerr)
		}

		cols := make([]pbit.PbitColumn, 0, len(header))
		for _, h := range header {
			cols = append(cols, pbit.PbitColumn{Name: h, DataType: "string"})
		}
		if err := pbit.CreateLakehouseTable(db, finalSchema, t.Name, cols); err != nil {
			closeFn()
			failDS(ctx, db, dsID)
			return fmt.Errorf("create table %q: %w", t.Name, err)
		}
		if len(header) > 0 {
			if _, err := pbit.CopyRowsInto(db, finalSchema, t.Name, header, rowIter); err != nil {
				closeFn()
				failDS(ctx, db, dsID)
				return fmt.Errorf("copy into %q: %w", t.Name, err)
			}
		}
		closeFn()

		pct := 10 + int(float64(i+1)/float64(total)*80)
		rep.Update("loading", pct, int64(i+1), int64(total), 0, fmt.Sprintf("加载表 %s", t.Name))
	}

	// Ontology rows (typed from metadata) in one terminal transaction.
	rep.Update("ontology", 92, 0, 0, 0, "生成本体")
	tx, err := db.Begin()
	if err != nil {
		failDS(ctx, db, dsID)
		return fmt.Errorf("begin ontology tx: %w", err)
	}
	if _, err := pbit.PopulateOntology(tx, projectID, finalSchema, toPbitSchema(&meta), nil, dsID); err != nil {
		tx.Rollback() //nolint:errcheck
		failDS(ctx, db, dsID)
		return fmt.Errorf("populate ontology: %w", err)
	}
	if err := tx.Commit(); err != nil {
		failDS(ctx, db, dsID)
		return fmt.Errorf("commit ontology tx: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE data_source SET status='ready', staging_schema=$1, last_sync_at=now(), updated_at=now()
		WHERE id=$2
	`, finalSchema, dsID); err != nil {
		return fmt.Errorf("mark data_source ready: %w", err)
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
