package handler

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

const annotationsImportMaxBytes = 10 << 20 // 10 MB

var (
	utf8BOM       = []byte{0xEF, 0xBB, 0xBF}
	statusTrueSet = map[string]bool{
		"已确认": true, "确认": true, "true": true, "1": true, "yes": true, "y": true, "confirmed": true,
	}
)

func parseStatusCell(s string) bool {
	return statusTrueSet[strings.ToLower(strings.TrimSpace(s))]
}

func headerIndex(headers []string, aliases ...string) int {
	for i, h := range headers {
		h = strings.TrimSpace(strings.ToLower(h))
		for _, a := range aliases {
			if h == strings.ToLower(a) {
				return i
			}
		}
	}
	return -1
}

// handleAnnotationsImport accepts a multipart/form-data CSV upload and bulk
// inserts annotations under the current project. Duplicate questions (by
// project_id + md5(question)) are skipped via ON CONFLICT DO NOTHING.
//
// Request: POST /api/ontology/agent-annotations-import?projectId=<uuid>
//
//	multipart/form-data:
//	  file=<csv>   columns: 序号,问题,分词,状态,创建时间
//
// Response: { total, inserted, skipped, failed, errors[] }
func handleAnnotationsImport(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "POST only"})
			return
		}
		pid := GetProjectID(r)
		if pid == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, annotationsImportMaxBytes)
		if err := r.ParseMultipartForm(annotationsImportMaxBytes); err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "invalid multipart form: " + err.Error()})
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "missing 'file' field: " + err.Error()})
			return
		}
		defer f.Close()

		buf, err := io.ReadAll(f)
		if err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "read upload: " + err.Error()})
			return
		}
		buf = bytes.TrimPrefix(buf, utf8BOM)

		rd := csv.NewReader(bytes.NewReader(buf))
		rd.FieldsPerRecord = -1 // tolerate ragged rows
		headers, err := rd.Read()
		if err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "csv header read failed: " + err.Error()})
			return
		}

		idxQuestion := headerIndex(headers, "问题", "question")
		idxTokens := headerIndex(headers, "分词", "tokens")
		idxStatus := headerIndex(headers, "状态", "status")
		// 创建时间 is informational only — DB always sets created_at = now()
		// on insert; we ignore the column to keep the import behavior simple.
		if idxQuestion < 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "missing required column '问题' (or 'question')"})
			return
		}

		stmt, err := db.Prepare(`
			INSERT INTO ont_agent_annotation (project_id, question, tokens, status)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (project_id, md5(question)) DO NOTHING`)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": "db prepare: " + err.Error()})
			return
		}
		defer stmt.Close()

		total, inserted, skipped, failed := 0, 0, 0, 0
		var errs []M
		rowIdx := 1 // header was row 1
		for {
			rec, err := rd.Read()
			if err == io.EOF {
				break
			}
			rowIdx++
			if err != nil {
				failed++
				if len(errs) < 20 {
					errs = append(errs, M{"row": rowIdx, "error": err.Error()})
				}
				continue
			}
			total++
			get := func(i int) string {
				if i < 0 || i >= len(rec) {
					return ""
				}
				return strings.TrimSpace(rec[i])
			}
			question := get(idxQuestion)
			if question == "" {
				failed++
				if len(errs) < 20 {
					errs = append(errs, M{"row": rowIdx, "error": "empty question"})
				}
				continue
			}
			tokens := get(idxTokens)
			status := parseStatusCell(get(idxStatus))

			res, err := stmt.Exec(pid, question, tokens, status)
			if err != nil {
				failed++
				if len(errs) < 20 {
					errs = append(errs, M{"row": rowIdx, "error": err.Error()})
				}
				continue
			}
			n, _ := res.RowsAffected()
			if n > 0 {
				inserted++
			} else {
				skipped++
			}
		}

		JsonResp(w, M{
			"total":    total,
			"inserted": inserted,
			"skipped":  skipped,
			"failed":   failed,
			"errors":   errs,
		})
	}
}

// handleAnnotationsExport streams a UTF-8 CSV (with BOM) of every annotation
// under the current project. Columns mirror the import format exactly so a
// round-trip is loss-free for the human-relevant fields.
//
// Request:  GET /api/ontology/agent-annotations-export?projectId=<uuid>
// Response: text/csv; charset=utf-8 with BOM
//
//	Filename: annotations_<projectId>_<yyyyMMdd>.csv
func handleAnnotationsExport(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "GET only"})
			return
		}
		pid := GetProjectID(r)
		if pid == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}

		rows, err := db.Query(`
			SELECT question, COALESCE(tokens,''), status, created_at
			  FROM ont_agent_annotation
			 WHERE project_id = $1
			 ORDER BY created_at DESC, id`, pid)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		defer rows.Close()

		filename := fmt.Sprintf("annotations_%s_%s.csv", pid, time.Now().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		w.Header().Set("Cache-Control", "no-store")

		bw := bufio.NewWriter(w)
		defer bw.Flush()
		bw.Write(utf8BOM)
		cw := csv.NewWriter(bw)
		cw.Write([]string{"序号", "问题", "分词", "状态", "创建时间"})

		seq := 0
		for rows.Next() {
			var question, tokens string
			var status bool
			var ca time.Time
			if err := rows.Scan(&question, &tokens, &status, &ca); err != nil {
				continue
			}
			seq++
			statusStr := "待确认"
			if status {
				statusStr = "已确认"
			}
			cw.Write([]string{
				fmt.Sprintf("%d", seq),
				question,
				tokens,
				statusStr,
				ca.Format(time.RFC3339),
			})
		}
		cw.Flush()
	}
}

// handleAnnotationsBulkStatus flips the `status` flag (confirmed / pending) on
// many rows in one shot. Accepts two shapes:
//
//	{ "ids": ["uuid", ...], "status": true|false }
//	{ "selectAll": true, "search": "...", "statusFilter": "true"|"false"|"",
//	  "status": true|false }
//
// The second form mirrors the GET list filter exactly so the UI "select all
// matching" affordance can reuse it without round-tripping ids. Either form is
// scoped to the current project; supplying both ids and selectAll is rejected.
//
// Returns: { "updated": N }
//
// Side-effect: when status flips to true and the row's question_vector is
// NULL, we schedule a best-effort background embed (one per row). This is
// fire-and-forget — operators who want strict deterministic vector coverage
// should follow up with the /agent-annotations-recompute SSE endpoint.
//
//	POST /api/ontology/agent-annotations-bulk-status?projectId=<uuid>
func handleAnnotationsBulkStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "POST only"})
			return
		}
		pid := GetProjectID(r)
		if pid == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}

		var body struct {
			IDs          []string `json:"ids"`
			SelectAll    bool     `json:"selectAll"`
			Search       string   `json:"search"`
			StatusFilter string   `json:"statusFilter"`
			Status       *bool    `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "invalid JSON: " + err.Error()})
			return
		}
		if body.Status == nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "'status' (bool) is required"})
			return
		}
		if !body.SelectAll && len(body.IDs) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "either 'ids' or 'selectAll' must be provided"})
			return
		}
		if body.SelectAll && len(body.IDs) > 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "'ids' and 'selectAll' are mutually exclusive"})
			return
		}

		var (
			q    string
			args []interface{}
		)
		if body.SelectAll {
			q = `UPDATE ont_agent_annotation
			        SET status = $1, updated_at = now()
			      WHERE project_id = $2`
			args = []interface{}{*body.Status, pid}
			argIdx := 3
			if body.Search != "" {
				q += fmt.Sprintf(` AND (question ILIKE $%d OR tokens ILIKE $%d)`, argIdx, argIdx)
				args = append(args, "%"+body.Search+"%")
				argIdx++
			}
			if body.StatusFilter == "true" {
				q += fmt.Sprintf(` AND status = $%d`, argIdx)
				args = append(args, true)
				argIdx++
			} else if body.StatusFilter == "false" {
				q += fmt.Sprintf(` AND status = $%d`, argIdx)
				args = append(args, false)
				argIdx++
			}
			q += " RETURNING id, question, question_vector IS NULL AS need_embed"
		} else {
			// ids[] form — keep ids scoped to project_id (defense-in-depth IDOR).
			q = `UPDATE ont_agent_annotation
			        SET status = $1, updated_at = now()
			      WHERE project_id = $2
			        AND id = ANY($3::uuid[])
			      RETURNING id, question, question_vector IS NULL AS need_embed`
			args = []interface{}{*body.Status, pid, "{" + strings.Join(body.IDs, ",") + "}"}
		}

		rows, err := db.Query(q, args...)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		defer rows.Close()

		type pending struct{ id, question string }
		var toEmbed []pending
		updated := 0
		for rows.Next() {
			var id, question string
			var needEmbed bool
			if err := rows.Scan(&id, &question, &needEmbed); err != nil {
				continue
			}
			updated++
			if *body.Status && needEmbed && question != "" {
				toEmbed = append(toEmbed, pending{id, question})
			}
		}

		// Best-effort embed for rows that just flipped to confirmed. Single
		// goroutine, no rate guarantee — for batches > a few hundred the user
		// should hit the SSE recompute endpoint, which fans out 20×8.
		if len(toEmbed) > 0 {
			go func() {
				for _, p := range toEmbed {
					embedAndSaveAnnotationVector(db, p.id, p.question)
				}
			}()
		}

		JsonResp(w, M{"updated": updated, "embedQueued": len(toEmbed)})
	}
}

// handleAnnotationsVectorStatus returns the coverage so the UI badge can show
// "需要计算 (N)". Counts every annotation under the project regardless of
// status — pending (status=false) imports are also embeddable and feed the
// few-shot vector search once promoted.
//
// Request:  GET /api/ontology/agent-annotations-vector-status?projectId=<uuid>
// Response: { total, withVector, missing, needsCompute }
func handleAnnotationsVectorStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "GET only"})
			return
		}
		pid := GetProjectID(r)
		if pid == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}

		var total, missing, confirmed, pending int
		err := db.QueryRow(`
			SELECT COUNT(*),
			       COUNT(*) FILTER (WHERE question_vector IS NULL),
			       COUNT(*) FILTER (WHERE status),
			       COUNT(*) FILTER (WHERE NOT status)
			  FROM ont_agent_annotation
			 WHERE project_id = $1`, pid).Scan(&total, &missing, &confirmed, &pending)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		JsonResp(w, M{
			"total":        total,
			"withVector":   total - missing,
			"missing":      missing,
			"needsCompute": missing,
			// Project-wide status counts so the UI header can show real totals
			// (the list endpoint paginates, so its `items` slice only carries
			// 50 rows worth of confirmed/pending — misleading when the bulk
			// "select all matching" path has touched rows across pages).
			"confirmed": confirmed,
			"pending":   pending,
		})
	}
}
