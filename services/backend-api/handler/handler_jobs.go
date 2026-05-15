package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

// jobRow is the JSON wire-shape for ingest_job rows. Field names use
// camelCase so the frontend can consume without remapping.
type jobRow struct {
	ID              string  `json:"id"`
	DataSourceID    *string `json:"dataSourceId,omitempty"`
	ProjectID       string  `json:"projectId"`
	Kind            string  `json:"kind"`
	Status          string  `json:"status"`
	Phase           *string `json:"phase,omitempty"`
	Percent         int     `json:"percent"`
	RowsDone        int64   `json:"rowsDone"`
	RowsTotal       int64   `json:"rowsTotal"`
	BytesDone       int64   `json:"bytesDone"`
	Message         *string `json:"message,omitempty"`
	WorkerID        *string `json:"workerId,omitempty"`
	HeartbeatAt     *string `json:"heartbeatAt,omitempty"`
	StartedAt       *string `json:"startedAt,omitempty"`
	CompletedAt     *string `json:"completedAt,omitempty"`
	Error           *string `json:"error,omitempty"`
	RetryCount      int     `json:"retryCount"`
	CancelRequested bool    `json:"cancelRequested"`
	CreatedAt       string  `json:"createdAt"`
}

// HandleJobs — GET /api/jobs?project_id=<uuid>&status=running,recent&limit=50
//
// `status` is a CSV; valid tokens:
//   - queued / running / succeeded / failed / cancelled — direct match
//   - recent — succeeded/failed/cancelled within last 24h
//
// Default: running + recent (the drawer's typical view).
// Default limit: 50, max: 200.
func HandleJobs(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		if r.Method != http.MethodGet {
			JsonError(w, http.StatusMethodNotAllowed, M{"error": "method not allowed"})
			return
		}
		projectID := GetProjectID(r)
		if projectID == "" || !IsValidUUID(projectID) {
			JsonError(w, http.StatusBadRequest, M{"error": "project_id required"})
			return
		}

		statuses := strings.Split(r.URL.Query().Get("status"), ",")
		if len(statuses) == 1 && statuses[0] == "" {
			statuses = []string{"running", "recent"}
		}
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				if n > 200 {
					n = 200
				}
				limit = n
			}
		}

		// Build status filter. "recent" expands into terminal-status + 24h window.
		var statusClauses []string
		var args []any
		args = append(args, projectID)
		argIdx := 2
		hasRecent := false
		var directStatuses []string
		for _, s := range statuses {
			s = strings.TrimSpace(s)
			switch s {
			case "queued", "running", "succeeded", "failed", "cancelled":
				directStatuses = append(directStatuses, s)
			case "recent":
				hasRecent = true
			}
		}
		if len(directStatuses) > 0 {
			placeholders := make([]string, len(directStatuses))
			for i, s := range directStatuses {
				placeholders[i] = "$" + strconv.Itoa(argIdx)
				args = append(args, s)
				argIdx++
			}
			statusClauses = append(statusClauses,
				"status IN ("+strings.Join(placeholders, ",")+")")
		}
		if hasRecent {
			statusClauses = append(statusClauses,
				"(status IN ('succeeded','failed','cancelled') AND completed_at > now() - interval '24 hours')")
		}
		if len(statusClauses) == 0 {
			JsonResp(w, M{"jobs": []jobRow{}})
			return
		}

		args = append(args, limit)
		query := `
			SELECT id, data_source_id, project_id, kind, status, phase, percent,
			       rows_done, rows_total, bytes_done, message, worker_id,
			       heartbeat_at, started_at, completed_at, error, retry_count,
			       cancel_requested, created_at
			FROM ingest_job
			WHERE project_id = $1
			  AND (` + strings.Join(statusClauses, " OR ") + `)
			ORDER BY created_at DESC
			LIMIT $` + strconv.Itoa(argIdx)

		rows, err := db.QueryContext(r.Context(), query, args...)
		if err != nil {
			JsonError(w, http.StatusInternalServerError, M{"error": err.Error()})
			return
		}
		defer rows.Close()

		out := make([]jobRow, 0, limit)
		for rows.Next() {
			var jr jobRow
			var heartbeatAt, startedAt, completedAt sql.NullTime
			var createdAt time.Time
			err := rows.Scan(
				&jr.ID, &jr.DataSourceID, &jr.ProjectID, &jr.Kind, &jr.Status,
				&jr.Phase, &jr.Percent, &jr.RowsDone, &jr.RowsTotal, &jr.BytesDone,
				&jr.Message, &jr.WorkerID,
				&heartbeatAt, &startedAt, &completedAt,
				&jr.Error, &jr.RetryCount, &jr.CancelRequested, &createdAt,
			)
			if err != nil {
				JsonError(w, http.StatusInternalServerError, M{"error": err.Error()})
				return
			}
			jr.HeartbeatAt = nullTimeStrPtr(heartbeatAt)
			jr.StartedAt = nullTimeStrPtr(startedAt)
			jr.CompletedAt = nullTimeStrPtr(completedAt)
			jr.CreatedAt = createdAt.UTC().Format(time.RFC3339)
			out = append(out, jr)
		}
		JsonResp(w, M{"jobs": out})
	}
}

// HandleJobByID handles `/api/jobs/{id}` and `/api/jobs/{id}/cancel`.
func HandleJobByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		// Parse trailing path. Patterns:
		//   /api/jobs/<id>              GET
		//   /api/jobs/<id>/cancel       POST
		rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
		segments := strings.Split(rest, "/")
		if len(segments) == 0 || segments[0] == "" {
			JsonError(w, http.StatusBadRequest, M{"error": "job id required"})
			return
		}
		jobID := segments[0]
		if !IsValidUUID(jobID) {
			JsonError(w, http.StatusBadRequest, M{"error": "invalid job id"})
			return
		}

		switch {
		case len(segments) == 1 && r.Method == http.MethodGet:
			handleJobDetail(w, r, db, jobID)
		case len(segments) == 2 && segments[1] == "cancel" && r.Method == http.MethodPost:
			handleJobCancel(w, r, db, jobID)
		default:
			JsonError(w, http.StatusNotFound, M{"error": "not found"})
		}
	}
}

func handleJobDetail(w http.ResponseWriter, r *http.Request, db *sql.DB, jobID string) {
	row := db.QueryRowContext(r.Context(), `
		SELECT id, data_source_id, project_id, kind, status, phase, percent,
		       rows_done, rows_total, bytes_done, message, worker_id,
		       heartbeat_at, started_at, completed_at, error, retry_count,
		       cancel_requested, created_at
		FROM ingest_job WHERE id = $1
	`, jobID)
	var jr jobRow
	var heartbeatAt, startedAt, completedAt sql.NullTime
	var createdAt time.Time
	err := row.Scan(
		&jr.ID, &jr.DataSourceID, &jr.ProjectID, &jr.Kind, &jr.Status,
		&jr.Phase, &jr.Percent, &jr.RowsDone, &jr.RowsTotal, &jr.BytesDone,
		&jr.Message, &jr.WorkerID,
		&heartbeatAt, &startedAt, &completedAt,
		&jr.Error, &jr.RetryCount, &jr.CancelRequested, &createdAt,
	)
	if err == sql.ErrNoRows {
		JsonError(w, http.StatusNotFound, M{"error": "job not found"})
		return
	}
	if err != nil {
		JsonError(w, http.StatusInternalServerError, M{"error": err.Error()})
		return
	}
	jr.HeartbeatAt = nullTimeStrPtr(heartbeatAt)
	jr.StartedAt = nullTimeStrPtr(startedAt)
	jr.CompletedAt = nullTimeStrPtr(completedAt)
	jr.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	JsonResp(w, jr)
}

func handleJobCancel(w http.ResponseWriter, r *http.Request, db *sql.DB, jobID string) {
	// Only allow cancelling jobs that are still queued or running. Terminal
	// statuses are immutable.
	res, err := db.ExecContext(r.Context(), `
		UPDATE ingest_job SET cancel_requested = true
		WHERE id = $1 AND status IN ('queued','running')
	`, jobID)
	if err != nil {
		JsonError(w, http.StatusInternalServerError, M{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		JsonError(w, http.StatusConflict, M{"error": "job not active"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func nullTimeStrPtr(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339)
	return &s
}

// Compile-time check we still know about encoding/json (used implicitly via
// JsonResp); silences an unused-import nag if helpers above ever drop the
// dependency.
var _ = json.NewEncoder
