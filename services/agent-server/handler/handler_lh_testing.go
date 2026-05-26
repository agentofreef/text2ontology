package handler

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

// ======================== CRUD handlers ========================

func handleLHTestSuites(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		projectID := GetProjectID(r)

		switch r.Method {
		case http.MethodGet:
			rows, err := db.Query(`SELECT id, name, status, total, passed, failed,
				COALESCE(concurrency,1), last_run_at, created_at
				FROM ont_test_suite WHERE project_id = $1 AND COALESCE(source_type,'pipeline') = 'lakehouse'
				ORDER BY created_at DESC`, projectID)
			if err != nil {
				ListResp(w, []M{}, 0)
				return
			}
			defer rows.Close()
			var suites []M
			for rows.Next() {
				var id, name, status string
				var total, passed, failed, concurrency int
				var lastRunAt sql.NullTime
				var createdAt time.Time
				rows.Scan(&id, &name, &status, &total, &passed, &failed, &concurrency, &lastRunAt, &createdAt)
				s := M{
					"id": id, "name": name, "status": status,
					"total": total, "passed": passed, "failed": failed,
					"concurrency": concurrency,
					"createdAt":   createdAt.Format(time.RFC3339),
				}
				if lastRunAt.Valid {
					s["lastRunAt"] = lastRunAt.Time.Format(time.RFC3339)
				}
				var caseCount int
				db.QueryRow(`SELECT COUNT(*) FROM ont_test_case WHERE suite_id = $1`, id).Scan(&caseCount)
				s["caseCount"] = caseCount
				suites = append(suites, s)
			}
			if suites == nil {
				suites = []M{}
			}
			ListResp(w, suites, len(suites))

		case http.MethodPost:
			body := ReadBody(r)
			name := StrVal(body, "name")
			pid := StrVal(body, "projectId")
			concurrency := 1
			if v, ok := body["concurrency"].(float64); ok && v >= 1 && v <= 10 {
				concurrency = int(v)
			}
			if name == "" || !IsValidUUID(pid) {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "name and projectId required"})
				return
			}
			// Body projectId is not gated by the middleware (only ?projectId=
			// query is) — verify access before creating in that project.
			if !authmw.EnforceProjectAccess(w, r, db, pid) {
				return
			}
			var id string
			err := db.QueryRow(`INSERT INTO ont_test_suite (project_id, name, source_type, concurrency)
				VALUES ($1, $2, 'lakehouse', $3) RETURNING id`, pid, name, concurrency).Scan(&id)
			if err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id, "name": name, "concurrency": concurrency})

		default:
			w.WriteHeader(405)
		}
	}
}

func handleLHTestSuiteByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		path := r.URL.Path
		suiteID := ExtractID(path, "/api/ontology/lh-test-suites")

		// Cross-project IDOR guard: every sub-route below dispatches on this
		// suiteID (cases, runs, tags, compare, mark, retry, export, upload,
		// bulk-*), so one check here protects the whole subtree. Skip when
		// suiteID == "" — that case falls through to list/create, which is
		// already middleware-gated via ?projectId=.
		if suiteID != "" {
			if !authmw.EnforceEntityProject(w, r, db, "ont_test_suite", "id", suiteID) {
				return
			}
		}

		// Route: /api/ontology/lh-test-suites/{id}/compare/case/{caseId}?runs=...
		// Lazy detail loader for the N-way Excel compare. Returns the FULL
		// per-run case detail for ONE case across N runs — keeps the bulk
		// /compare endpoint summary-only so the initial payload is bounded.
		if strings.HasSuffix(path, "/compare/export") {
			handleLHCompareExport(db, suiteID, w, r)
			return
		}
		if strings.Contains(path, "/compare/case/") {
			cp := strings.Split(path, "/compare/case/")
			if len(cp) == 2 {
				caseID := strings.TrimSuffix(cp[1], "/")
				handleLHCompareCase(db, suiteID, caseID, w, r)
				return
			}
		}
		// Route: /api/ontology/lh-test-suites/{id}/compare?runs=id1,id2,id3,...
		// Returns lightweight summary only (no functionCalls / executionResult /
		// generatedSql / finalAnswer / executionError). Use /compare/case/{id}
		// to lazy-load detail for an individual question.
		if strings.HasSuffix(path, "/compare") {
			handleLHCompare(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/runs/{runId}/...
		if strings.Contains(path, "/runs/") {
			parts := strings.Split(path, "/runs/")
			if len(parts) == 2 {
				runID := strings.TrimSuffix(parts[1], "/")
				subPath := parts[1]
				// /runs/{runId}/run
				if strings.HasSuffix(subPath, "/run") && !strings.Contains(strings.TrimSuffix(subPath, "/run"), "/") {
					handleLHTestRunEnqueue(db, suiteID, strings.TrimSuffix(runID, "/run"), w, r)
					return
				}
				// /runs/{runId}/retry-errors
				if strings.HasSuffix(subPath, "/retry-errors") && !strings.Contains(strings.TrimSuffix(subPath, "/retry-errors"), "/") {
					handleLHTestRunRetryErrors(db, suiteID, strings.TrimSuffix(runID, "/retry-errors"), w, r)
					return
				}
				// /runs/{runId}/bulk-retry — reset a caller-specified set of run-case ids to pending and re-queue.
				// Used by the "重跑选中" / "重跑标错" buttons (checkbox-selection or mark='incorrect' driven).
				if strings.HasSuffix(subPath, "/bulk-retry") && !strings.Contains(strings.TrimSuffix(subPath, "/bulk-retry"), "/") {
					handleLHTestRunBulkRetry(db, suiteID, strings.TrimSuffix(runID, "/bulk-retry"), w, r)
					return
				}
				// /runs/{runId}/default
				if strings.HasSuffix(subPath, "/default") {
					handleLHTestRunDefault(db, suiteID, strings.TrimSuffix(runID, "/default"), w, r)
					return
				}
				// /runs/{runId}/cases/{rcId}/mark
				if strings.Contains(subPath, "/cases/") && strings.HasSuffix(subPath, "/mark") {
					cp := strings.Split(subPath, "/cases/")
					if len(cp) == 2 {
						rcID := strings.TrimSuffix(cp[1], "/mark")
						handleLHTestRunCaseMark(db, rcID, suiteID, strings.Split(subPath, "/")[0], w, r)
					}
					return
				}
				// /runs/{runId}/cases/{rcId}/retry
				if strings.Contains(subPath, "/cases/") && strings.HasSuffix(subPath, "/retry") {
					cp := strings.Split(subPath, "/cases/")
					if len(cp) == 2 {
						rcID := strings.TrimSuffix(cp[1], "/retry")
						handleLHTestRunCaseRetry(db, rcID, suiteID, strings.Split(subPath, "/")[0], w, r)
					}
					return
				}
				// /runs/{runId}/cases/{rcId}/note
				if strings.Contains(subPath, "/cases/") && strings.HasSuffix(subPath, "/note") {
					cp := strings.Split(subPath, "/cases/")
					if len(cp) == 2 {
						rcID := strings.TrimSuffix(cp[1], "/note")
						handleLHTestRunCaseNote(db, rcID, suiteID, strings.Split(subPath, "/")[0], w, r)
					}
					return
				}
				// /runs/{runId}/cases/{rcId}/ai-judge
				if strings.Contains(subPath, "/cases/") && strings.HasSuffix(subPath, "/ai-judge") {
					cp := strings.Split(subPath, "/cases/")
					if len(cp) == 2 {
						rcID := strings.TrimSuffix(cp[1], "/ai-judge")
						handleLHTestRunCaseAIJudge(db, rcID, suiteID, strings.Split(subPath, "/")[0], w, r)
					}
					return
				}
				// /runs/{runId}/export
				if strings.HasSuffix(subPath, "/export") {
					handleLHTestRunExport(db, suiteID, strings.TrimSuffix(runID, "/export"), w, r)
					return
				}
				// /runs/{runId}/cases/{rcId} — GET single-case full detail (heavy fields).
				// Must come AFTER the more specific /cases/{rcId}/{action} matches
				// (mark / retry / note / ai-judge) so those keep their dispatch;
				// falls through to here only when the leaf has no action suffix.
				if strings.Contains(subPath, "/cases/") {
					cp := strings.Split(subPath, "/cases/")
					leaf := strings.TrimSuffix(cp[1], "/")
					if len(cp) == 2 && !strings.Contains(leaf, "/") {
						runIDStripped := strings.Split(subPath, "/")[0]
						handleLHTestRunCaseDetail(db, leaf, suiteID, runIDStripped, w, r)
						return
					}
				}
				// /runs/{runId} GET/DELETE
				handleLHTestRunByID(db, suiteID, runID, w, r)
				return
			}
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/runs (list/create)
		if strings.HasSuffix(path, "/runs") {
			handleLHTestRuns(db, suiteID, w, r)
			return
		}

		// Route: /api/ontology/lh-test-suites/{id}/cases/bulk-tag
		// Must come before the /cases/ suffix rules below.
		if strings.HasSuffix(path, "/cases/bulk-tag") {
			handleLHBulkTag(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/cases/bulk-delete
		if strings.HasSuffix(path, "/cases/bulk-delete") {
			handleLHBulkDeleteCases(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/cases/{caseId}/tags
		if strings.Contains(path, "/cases/") && strings.HasSuffix(path, "/tags") {
			parts := strings.Split(path, "/cases/")
			if len(parts) == 2 {
				caseID := strings.TrimSuffix(parts[1], "/tags")
				handleLHCaseTags(db, suiteID, caseID, w, r)
			}
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/tags (list / create)
		if strings.HasSuffix(path, "/tags") && !strings.Contains(path, "/tags/") {
			handleLHSuiteTags(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/tags/{tagId} (rename / delete)
		if strings.Contains(path, "/tags/") {
			parts := strings.Split(path, "/tags/")
			if len(parts) == 2 {
				tagID := strings.TrimSuffix(parts[1], "/")
				handleLHSuiteTagByID(db, suiteID, tagID, w, r)
			}
			return
		}

		// Route: /api/ontology/lh-test-suites/{id}/cases
		if strings.HasSuffix(path, "/cases") && !strings.Contains(path, "/cases/") {
			handleLHTestCases(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/upload
		if strings.HasSuffix(path, "/upload") {
			handleLHTestCaseUpload(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/add-questions
		if strings.HasSuffix(path, "/add-questions") {
			handleLHAddQuestions(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/export?format=csv|xlsx
		if strings.HasSuffix(path, "/export") {
			handleLHTestSuiteExport(db, suiteID, w, r)
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/cases/{caseId}/mark
		if strings.Contains(path, "/cases/") && strings.HasSuffix(path, "/mark") {
			parts := strings.Split(path, "/cases/")
			if len(parts) == 2 {
				caseID := strings.TrimSuffix(parts[1], "/mark")
				handleLHTestCaseMark(db, caseID, suiteID, w, r)
			}
			return
		}
		// Route: /api/ontology/lh-test-suites/{id}/cases/{caseId}/retry
		if strings.Contains(path, "/cases/") && strings.HasSuffix(path, "/retry") {
			parts := strings.Split(path, "/cases/")
			if len(parts) == 2 {
				caseID := strings.TrimSuffix(parts[1], "/retry")
				handleLHTestCaseRetry(db, caseID, suiteID, w, r)
			}
			return
		}
		// Route: PUT (edit) / DELETE /api/ontology/lh-test-suites/{id}/cases/{caseId}
		if strings.Contains(path, "/cases/") {
			parts := strings.Split(path, "/cases/")
			if len(parts) == 2 {
				caseID := strings.TrimSuffix(parts[1], "/")
				switch r.Method {
				case http.MethodPut:
					handleLHTestCaseEdit(db, caseID, suiteID, w, r)
				case http.MethodDelete:
					db.Exec(`DELETE FROM ont_test_case WHERE id = $1 AND suite_id = $2`, caseID, suiteID)
					JsonResp(w, M{"ok": true})
				default:
					w.WriteHeader(405)
				}
			}
			return
		}

		// Direct suite operations
		switch r.Method {
		case http.MethodGet:
			handleLHTestSuiteDetail(db, suiteID, w)

		case http.MethodPut:
			body := ReadBody(r)
			if name := StrVal(body, "name"); name != "" {
				db.Exec(`UPDATE ont_test_suite SET name = $1, updated_at = now() WHERE id = $2`, name, suiteID)
			}
			if v, ok := body["concurrency"].(float64); ok && v >= 1 && v <= 10 {
				db.Exec(`UPDATE ont_test_suite SET concurrency = $1, updated_at = now() WHERE id = $2`, int(v), suiteID)
			}
			JsonResp(w, M{"ok": true})

		case http.MethodDelete:
			db.Exec(`DELETE FROM ont_test_suite WHERE id = $1`, suiteID)
			JsonResp(w, M{"ok": true})

		default:
			w.WriteHeader(405)
		}
	}
}

func handleLHTestSuiteDetail(db *sql.DB, suiteID string, w http.ResponseWriter) {
	var name, status string
	var total, passed, failed, concurrency int
	var lastRunAt sql.NullTime
	var createdAt time.Time
	err := db.QueryRow(`SELECT name, status, total, passed, failed, COALESCE(concurrency,1), last_run_at, created_at
		FROM ont_test_suite WHERE id = $1`, suiteID).
		Scan(&name, &status, &total, &passed, &failed, &concurrency, &lastRunAt, &createdAt)
	if err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "suite not found"})
		return
	}
	suite := M{
		"id": suiteID, "name": name, "status": status,
		"total": total, "passed": passed, "failed": failed,
		"concurrency": concurrency,
		"createdAt":   createdAt.Format(time.RFC3339),
	}
	if lastRunAt.Valid {
		suite["lastRunAt"] = lastRunAt.Time.Format(time.RFC3339)
	}

	caseRows, _ := db.Query(`SELECT id, user_question, sort_order, status, COALESCE(code,''),
		function_calls, COALESCE(final_answer,''), COALESCE(generated_sql,''),
		COALESCE(execution_status,''), COALESCE(execution_result,''), COALESCE(execution_error,''),
		duration_ms, COALESCE(model_name,''), prompt_tokens, completion_tokens, total_tokens,
		mark, created_at
		FROM ont_test_case WHERE suite_id = $1 ORDER BY sort_order, created_at`, suiteID)
	var cases []M
	if caseRows != nil {
		for caseRows.Next() {
			var cid, question, cstatus, code string
			var sortOrder, durMs, pt, ct, tt int
			var fcRaw sql.NullString
			var finalAnswer, genSQL, execStatus, execResult, execError, modelName string
			var mark sql.NullString
			var cCreatedAt time.Time
			caseRows.Scan(&cid, &question, &sortOrder, &cstatus, &code,
				&fcRaw, &finalAnswer, &genSQL,
				&execStatus, &execResult, &execError,
				&durMs, &modelName, &pt, &ct, &tt, &mark, &cCreatedAt)
			c := M{
				"id": cid, "userQuestion": question, "sortOrder": sortOrder,
				"status": cstatus, "code": code,
				"finalAnswer": finalAnswer, "generatedSql": genSQL,
				"executionStatus": execStatus, "executionResult": execResult, "executionError": execError,
				"durationMs": durMs, "modelName": modelName,
				"promptTokens": pt, "completionTokens": ct, "totalTokens": tt,
				"createdAt": cCreatedAt.Format(time.RFC3339),
			}
			if fcRaw.Valid && fcRaw.String != "" {
				var fcs interface{}
				json.Unmarshal([]byte(fcRaw.String), &fcs)
				c["functionCalls"] = fcs
			}
			if mark.Valid {
				c["mark"] = mark.String
			}
			cases = append(cases, c)
		}
		caseRows.Close()
	}
	if cases == nil {
		cases = []M{}
	}
	suite["cases"] = cases
	JsonResp(w, suite)
}

// ======================== Add questions (JSON body) ========================

func handleLHAddQuestions(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	body := ReadBody(r)
	questionsRaw, _ := body["questions"].([]interface{})
	if len(questionsRaw) == 0 {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "questions required"})
		return
	}

	var maxOrder int
	db.QueryRow(`SELECT COALESCE(MAX(sort_order), 0) FROM ont_test_case WHERE suite_id = $1`, suiteID).Scan(&maxOrder)

	inserted := 0
	for _, q := range questionsRaw {
		question := strings.TrimSpace(fmt.Sprintf("%v", q))
		if question == "" {
			continue
		}
		maxOrder++
		code := fmt.Sprintf("Q%03d", maxOrder)
		_, err := db.Exec(`INSERT INTO ont_test_case (suite_id, user_question, sort_order, code)
			VALUES ($1, $2, $3, $4)`, suiteID, question, maxOrder, code)
		if err == nil {
			inserted++
		}
	}
	JsonResp(w, M{"inserted": inserted})
}

// ======================== Test cases (GET list) ========================

// handleLHTestCases — GET returns the master question list for this suite,
// with tags attached. This is the data source for the Questions template
// editor page (distinct from run-specific case snapshots).
func handleLHTestCases(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	rows, err := db.Query(`SELECT id, COALESCE(code,''), user_question, COALESCE(expected_answer,''), sort_order, created_at, updated_at
		FROM ont_test_case WHERE suite_id = $1 ORDER BY sort_order, created_at`, suiteID)
	if err != nil {
		ListResp(w, []M{}, 0)
		return
	}
	defer rows.Close()

	var cases []M
	caseIDs := make([]string, 0, 64)
	for rows.Next() {
		var id, code, question, expected string
		var sortOrder int
		var createdAt, updatedAt time.Time
		rows.Scan(&id, &code, &question, &expected, &sortOrder, &createdAt, &updatedAt)
		cases = append(cases, M{
			"id": id, "code": code, "userQuestion": question,
			"expectedAnswer": expected,
			"sortOrder":      sortOrder,
			"createdAt":      createdAt.Format(time.RFC3339),
			"updatedAt":      updatedAt.Format(time.RFC3339),
		})
		caseIDs = append(caseIDs, id)
	}
	tagMap := loadTagsForCases(db, caseIDs)
	for _, c := range cases {
		cid, _ := c["id"].(string)
		if tags, ok := tagMap[cid]; ok {
			c["tags"] = tags
		} else {
			c["tags"] = []M{}
		}
	}
	if cases == nil {
		cases = []M{}
	}
	ListResp(w, cases, len(cases))
}

// handleLHTestCaseEdit — PUT /cases/{caseId}  {userQuestion?, code?}
// Edits the master question template. Only affects NEW runs / re-runs — existing
// run_case snapshots are intentionally frozen so historical results stay accurate.
func handleLHTestCaseEdit(db *sql.DB, caseID, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	if !IsValidUUID(caseID) {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "invalid caseId"})
		return
	}
	body := ReadBody(r)
	// Verify case belongs to suite before any write.
	var owner string
	db.QueryRow(`SELECT suite_id::text FROM ont_test_case WHERE id = $1`, caseID).Scan(&owner)
	if owner != suiteID {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "case not found in this suite"})
		return
	}
	if q, ok := body["userQuestion"].(string); ok {
		q = strings.TrimSpace(q)
		if q == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "userQuestion cannot be empty"})
			return
		}
		db.Exec(`UPDATE ont_test_case SET user_question = $1, updated_at = now() WHERE id = $2`, q, caseID)
	}
	if code, ok := body["code"].(string); ok {
		db.Exec(`UPDATE ont_test_case SET code = $1, updated_at = now() WHERE id = $2`, code, caseID)
	}
	// expected_answer 允许空串覆盖（用户可能想清空）。仅在字段存在时写入。
	if v, ok := body["expectedAnswer"]; ok {
		expected, _ := v.(string)
		db.Exec(`UPDATE ont_test_case SET expected_answer = $1, updated_at = now() WHERE id = $2`, expected, caseID)
	}
	JsonResp(w, M{"ok": true})
}

// handleLHBulkDeleteCases — POST /cases/bulk-delete  {caseIds}
func handleLHBulkDeleteCases(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	body := ReadBody(r)
	caseIDs := uuidArrayFromBody(body["caseIds"])
	if len(caseIDs) == 0 {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "caseIds required"})
		return
	}
	// Scope the delete by suiteID so a stale/forged id from another suite can't slip through.
	res, err := db.Exec(`DELETE FROM ont_test_case
		WHERE suite_id = $1 AND id = ANY($2)`, suiteID, pq.Array(caseIDs))
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	deleted, _ := res.RowsAffected()
	JsonResp(w, M{"ok": true, "deleted": deleted})
}

// ======================== Upload CSV/Excel ========================

func handleLHTestCaseUpload(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "file required"})
		return
	}
	defer file.Close()

	raw, _ := io.ReadAll(file)
	if !utf8.Valid(raw) {
		if decoded, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw); err == nil {
			raw = decoded
		}
	}
	content := string(raw)

	var maxOrder int
	db.QueryRow(`SELECT COALESCE(MAX(sort_order), 0) FROM ont_test_case WHERE suite_id = $1`, suiteID).Scan(&maxOrder)

	scanner := bufio.NewScanner(strings.NewReader(content))
	inserted := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.EqualFold(line, "question") || strings.EqualFold(line, "user_question") || strings.EqualFold(line, "问题") {
			continue
		}
		// Remove CSV quotes
		if len(line) > 1 && line[0] == '"' && line[len(line)-1] == '"' {
			line = line[1 : len(line)-1]
		}
		maxOrder++
		code := fmt.Sprintf("Q%03d", maxOrder)
		if _, err := db.Exec(`INSERT INTO ont_test_case (suite_id, user_question, sort_order, code)
			VALUES ($1, $2, $3, $4)`, suiteID, line, maxOrder, code); err == nil {
			inserted++
		}
	}
	JsonResp(w, M{"inserted": inserted})
}

// ======================== Mark ========================

func handleLHTestCaseMark(db *sql.DB, caseID, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	body := ReadBody(r)
	markVal := StrVal(body, "mark")
	if markVal == "" || markVal == "null" {
		db.Exec(`UPDATE ont_test_case SET mark = NULL, updated_at = now() WHERE id = $1`, caseID)
	} else {
		db.Exec(`UPDATE ont_test_case SET mark = $1, updated_at = now() WHERE id = $2`, markVal, caseID)
	}
	updateLHSuiteStats(db, suiteID)
	JsonResp(w, M{"ok": true})
}

// handleLHTestCaseRetry resets one case to pending, re-runs it synchronously, and
// returns the updated case payload as JSON. Used by the "retry" button on the
// dataset-testing page.
func handleLHTestCaseRetry(db *sql.DB, caseID, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	// Load the case + project
	var question, code, projectID string
	var sortOrder int
	err := db.QueryRow(`SELECT c.user_question, COALESCE(c.code,''), c.sort_order, s.project_id
		FROM ont_test_case c JOIN ont_test_suite s ON s.id = c.suite_id
		WHERE c.id = $1 AND c.suite_id = $2`, caseID, suiteID).
		Scan(&question, &code, &sortOrder, &projectID)
	if err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "case not found"})
		return
	}
	// Clear prior result so the UI shows fresh state mid-run
	db.Exec(`UPDATE ont_test_case SET status = 'running', generated_sql = '', execution_status = '',
		execution_result = '', execution_error = '', final_answer = '', function_calls = NULL,
		duration_ms = 0, updated_at = now() WHERE id = $1`, caseID)

	// Resolve LLM config — single "agent" role (was: 3-tier fallback).
	baseURL, apiKey, modelName, _, isToolCall, vendor := llmclient.GetConfigForRole(db, "agent")

	result := runLakehouseTestCase(r.Context(), db, projectID, suiteID, caseID, question, code, sortOrder,
		baseURL, apiKey, modelName, isToolCall, vendor)

	fcJSON, _ := json.Marshal(result.FunctionCalls)
	db.Exec(`UPDATE ont_test_case SET
		status = $1, generated_sql = $2, execution_status = $3,
		execution_result = $4, execution_error = $5, final_answer = $6,
		function_calls = $7::jsonb, duration_ms = $8, model_name = $9,
		prompt_tokens = $10, completion_tokens = $11, total_tokens = $12,
		updated_at = now()
		WHERE id = $13`,
		result.Status, result.GeneratedSQL, result.ExecutionStatus,
		result.ExecutionResult, result.ExecutionError, result.FinalAnswer,
		string(fcJSON), result.DurationMs, result.ModelName,
		result.PromptTokens, result.CompletionTokens, result.TotalTokens,
		caseID)

	updateLHSuiteStats(db, suiteID)

	JsonResp(w, M{
		"caseId":          caseID,
		"code":            result.Code,
		"status":          result.Status,
		"generatedSql":    result.GeneratedSQL,
		"executionStatus": result.ExecutionStatus,
		"executionResult": result.ExecutionResult,
		"executionError":  result.ExecutionError,
		"finalAnswer":     result.FinalAnswer,
		"functionCalls":   result.FunctionCalls,
		"durationMs":      result.DurationMs,
		"modelName":       result.ModelName,
	})
}

func updateLHSuiteStats(db *sql.DB, suiteID string) {
	var total, passed, failed int
	db.QueryRow(`SELECT COUNT(*) FROM ont_test_case WHERE suite_id = $1`, suiteID).Scan(&total)
	db.QueryRow(`SELECT COUNT(*) FROM ont_test_case WHERE suite_id = $1 AND mark = 'correct'`, suiteID).Scan(&passed)
	db.QueryRow(`SELECT COUNT(*) FROM ont_test_case WHERE suite_id = $1 AND mark = 'incorrect'`, suiteID).Scan(&failed)
	db.Exec(`UPDATE ont_test_suite SET total = $1, passed = $2, failed = $3, updated_at = now() WHERE id = $4`,
		total, passed, failed, suiteID)
}

// ======================== Test Runner result type ========================

// lhCaseResult carries the result from a worker goroutine back to the
// background-queue collector loop in lh_test_worker.go.
type lhCaseResult struct {
	CaseID           string
	Code             string
	Index            int
	Question         string
	Status           string
	GeneratedSQL     string
	ExecutionStatus  string
	ExecutionResult  string
	ExecutionError   string
	FinalAnswer      string
	FunctionCalls    []M
	DurationMs       int64
	ModelName        string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ======================== Core: run single lakehouse test case ========================

func runLakehouseTestCase(ctx context.Context, db *sql.DB, projectID, suiteID, caseID, question, code string, index int,
	llmBaseURL, llmAPIKey, llmModelName string, llmIsToolCall bool, llmVendor string) lhCaseResult {
	start := time.Now()
	result := lhCaseResult{
		CaseID:   caseID,
		Code:     code,
		Index:    index,
		Question: question,
		Status:   "error",
	}

	// NOTE: do NOT mark ont_test_case.status='running' here — caseID is the
	// caller-chosen row ID (ont_test_run_case.id when invoked from the worker
	// pool, ont_test_case.id only on the synchronous retry path). The worker
	// pool already updates ont_test_run_case in lh_test_worker.go; updating
	// ont_test_case by the same id was a leftover from the legacy table layout
	// and silently no-ops (or worse, hits an unrelated row) for run-case ids.

	// Use explicitly passed LLM config
	if llmBaseURL == "" {
		result.ExecutionError = "无可用的 LLM 配置"
		return result
	}
	baseURL, apiKey, modelName, isToolCall, vendor := llmBaseURL, llmAPIKey, llmModelName, llmIsToolCall, llmVendor
	result.ModelName = modelName

	// Pre-processing: tokenize + recall context
	fewShots := loadAnnotationFewShots(db, projectID, question)
	tokens := tokenizeWithAnnotationFewShots(db, projectID, question, fewShots)
	// Sync save: avoids the previous goroutine race where parallel test cases
	// fired duplicate INSERTs against the same (projectID, question) row before
	// the first transaction committed, surfacing as PK conflicts in logs.
	// saveAnnotation logs internal errors itself; no return value to check.
	saveAnnotation(db, projectID, "", question, tokens, nil)
	// runLakehouseTestCase is invoked both from an HTTP handler (with r.Context())
	// and from the background worker pool (with the worker ctx); the caller's ctx
	// is threaded here so cancellation/shutdown propagates to recall + LLM calls.
	recallResult := recall.BuildLakehouseContext(ctx, db, projectID, tokens, question)
	enrichedQuestion := recallResult.ContextMD + "\n\n---\n\n" + question

	// Build system prompt (simplified for testing — no thread history)
	var projectName string
	db.QueryRow(`SELECT COALESCE(name,'') FROM project WHERE id = $1`, projectID).Scan(&projectName)
	if projectName == "" {
		projectName = "当前项目"
	}

	xmlToolSection := ""
	if !isToolCall {
		xmlToolSection = `
## 工具调用格式

<function_call>
{"name":"工具名","arguments":{...}}
</function_call>

### smartquery — 执行数据查询
{"objects": [...], "metric": "口径名", "groupBy": [...], "filters": [{"prop":"属性名","op":"=","value":"值"}], "orderBy": [...], "limit": N, "displayMode": "table|bar|pie|line"}

### lookup — 补充查询本体信息
{"ontology_name":["Od或Ok名称"],"keyword":["业务词"]}
`
	}

	systemPrompt := `你是 ` + projectName + ` 的数据湖仓分析助手。

## 工作方式

用户的问题已经过预处理，相关的数据对象（Od）和知识（Ok）已在【已识别的数据上下文】中提供。

**数据类问题**（查数量、排名、趋势等）：
- 直接根据上下文中的 Od 和属性调用 smartquery 执行查询
- 如上下文中信息不足，可调用 lookup 补充

**知识类问题**（概念解释）：
- 直接根据上下文中的知识参考回答

## 严格约束

- **smartquery.objects 必须使用 Od 英文名**
- **查询引擎为 PostgreSQL SQL**
- 如对象提示"尚未配置 canonical_query"，返回错误说明

## 执行优先原则

- 当数据上下文已经明确映射了 token → Od.Property 的对应关系时，直接执行 smartquery
- 涉及多个 Od 时，smartquery 支持多对象 JOIN
- 只有在上下文出现 "⚠ 需要澄清" 标记时才向用户澄清

## 错误恢复策略

- **"column X does not exist"**：调用 lookup 确认正确属性名后重试
- **连续2次相同错误**：停止重试，返回错误信息

## 当前日期

` + time.Now().Format("2006-01-02") + xmlToolSection

	// Inject Ol (learned facts) index
	olIndex := BuildOlIndex(db, projectID, "")
	if !strings.HasPrefix(olIndex, "暂无学习事实") {
		systemPrompt += "\n\n" + olIndex
	}

	// Build LLM messages
	llmMessages := []M{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": enrichedQuestion},
	}

	// Define tools (only smartquery and lookup for testing)
	v2Tools := []llmclient.ToolDef{
		{Name: "lookup", Description: "查本体定义和业务关键词", Parameters: M{
			"type": "object",
			"properties": M{
				"ontology_name": M{"type": "array", "items": M{"type": "string"}, "description": "Od对象名或Ok知识标题"},
				"keyword":       M{"type": "array", "items": M{"type": "string"}, "description": "业务关键词"},
			},
		}},
		{Name: "smartquery", Description: "执行 SQL 数据查询", Parameters: M{
			"type": "object", "required": []string{"objects", "groupBy"},
			"properties": M{
				"objects":      M{"type": "array", "items": M{"type": "string"}},
				"metric":       M{"type": "string"},
				"groupBy":      M{"type": "array", "items": M{"type": "string"}},
				"filters":      M{"type": "array", "items": M{"type": "object", "properties": M{"prop": M{"type": "string"}, "op": M{"type": "string"}, "value": M{"type": "string"}, "fuzzyMatch": M{"type": "boolean"}}}},
				"metricFilter": M{"type": "object", "properties": M{"op": M{"type": "string"}, "value": M{"type": "string"}}},
				"orderBy":      M{"type": "array", "items": M{"type": "object", "properties": M{"prop": M{"type": "string"}, "dir": M{"type": "string", "enum": []string{"ASC", "DESC"}}}}},
				"limit":        M{"type": "integer"},
				"displayMode":  M{"type": "string", "enum": []string{"table", "bar", "pie", "line"}},
			},
		}},
	}

	// Tool dispatch
	dispatchTool := func(name string, args map[string]interface{}) M {
		switch name {
		case "lookup":
			// Thread the caller's ctx (request ctx on the HTTP path, worker ctx
			// on the background path) so tool DB/LLM work is cancellable.
			return lakehouseToolLookup(ctx, db, projectID, args)
		case "smartquery":
			// Dataset-testing path runs no reachability judge → no required
			// dimensions to inject (nil is behavior-preserving).
			return lakehouseToolSmartQuery(ctx, db, projectID, question, args, nil)
		default:
			return M{"error": "未知工具: " + name}
		}
	}

	var functionCalls []M
	const maxRounds = 10

	for round := 0; round < maxRounds; round++ {
		if isToolCall {
			// Native tool_call path
			content, toolCalls, usage, err := llmclient.DoChatWithTools(
				baseURL, apiKey,
				M{"model": modelName, "messages": llmMessages, "max_tokens": 4096, "temperature": 0.1},
				v2Tools, "", vendor,
			)
			if err != nil {
				result.ExecutionError = "LLM 失败: " + err.Error()
				break
			}
			if usage != nil {
				result.PromptTokens += usage.PromptTokens
				result.CompletionTokens += usage.CompletionTokens
				result.TotalTokens += usage.TotalTokens
			}

			if len(toolCalls) == 0 {
				// Check for XML tool calls in content
				fcName, fcArgs, _, hasFc := llmclient.ExtractFunctionCallXML(content)
				if hasFc {
					toolResult := dispatchTool(fcName, fcArgs)
					fc := M{"name": fcName, "arguments": fcArgs, "result": toolResult}
					functionCalls = append(functionCalls, fc)
					extractSmartQueryResult(&result, fcName, toolResult)
					llmMessages = append(llmMessages, M{"role": "assistant", "content": content})
					llmMessages = append(llmMessages, M{"role": "user", "content": toolResultToMarkdown(fcName, fcArgs, toolResult) + "\n\n请直接用中文回答用户的问题。"})
					continue
				}
				// Final answer
				result.FinalAnswer = llmclient.StripThinkTags(content)
				result.Status = "completed"
				break
			}

			tc := toolCalls[0]
			toolResult := dispatchTool(tc.Name, tc.Arguments)
			fc := M{"name": tc.Name, "arguments": tc.Arguments, "result": toolResult}
			functionCalls = append(functionCalls, fc)
			extractSmartQueryResult(&result, tc.Name, toolResult)

			llmMessages = append(llmMessages, llmclient.BuildAssistantToolCallMessage([]llmclient.ToolCallResult{{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}}))
			llmMessages = append(llmMessages, llmclient.BuildToolResultMessage(tc.ID, toolResultToMarkdown(tc.Name, tc.Arguments, toolResult)))

		} else {
			// XML fallback path (non-streaming for testing)
			content, usage, err := llmclient.DoChatWithUsage(
				baseURL, apiKey,
				M{"model": modelName, "messages": llmMessages, "max_tokens": 4096, "temperature": 0.1, "_vendor": vendor},
			)
			if err != nil {
				result.ExecutionError = "LLM 失败: " + err.Error()
				break
			}
			if usage != nil {
				result.PromptTokens += usage.PromptTokens
				result.CompletionTokens += usage.CompletionTokens
				result.TotalTokens += usage.TotalTokens
			}

			fcName, fcArgs, _, hasFc := llmclient.ExtractFunctionCallXML(content)
			if !hasFc {
				result.FinalAnswer = llmclient.StripThinkTags(content)
				result.Status = "completed"
				break
			}

			toolResult := dispatchTool(fcName, fcArgs)
			fc := M{"name": fcName, "arguments": fcArgs, "result": toolResult}
			functionCalls = append(functionCalls, fc)
			extractSmartQueryResult(&result, fcName, toolResult)

			llmMessages = append(llmMessages, M{"role": "assistant", "content": content})
			var followUp string
			switch fcName {
			case "smartquery":
				followUp = "\n\n请直接用中文回答用户的问题，不要再调用工具。"
			case "lookup":
				followUp = "\n\n请根据以上结果，立即调用 smartquery 执行数据查询。"
			default:
				followUp = "\n\n请继续完成任务。"
			}
			llmMessages = append(llmMessages, M{"role": "user", "content": toolResultToMarkdown(fcName, fcArgs, toolResult) + followUp})
		}
	}

	result.FunctionCalls = functionCalls
	result.DurationMs = time.Since(start).Milliseconds()

	if result.Status != "completed" && result.ExecutionError == "" {
		result.Status = "completed"
	}

	// Persist to flywheel (source = 'test')
	go func() {
		db.Exec(`INSERT INTO ont_query_log (project_id, user_question,
			generated_sql, objects, metric, group_by,
			execution_status, execution_result, execution_error, source_type, source, test_suite_id, used_llm, mark)
			VALUES ($1,$2,$3,'','','','','','','lakehouse','test',$4,true,false)
			ON CONFLICT (project_id, user_question) DO UPDATE SET
				generated_sql=EXCLUDED.generated_sql, source=EXCLUDED.source, test_suite_id=EXCLUDED.test_suite_id`,
			projectID, question, result.GeneratedSQL, suiteID)
	}()

	log.Printf("LH-TEST [%s] %s → %s (%dms)", code, question, result.Status, result.DurationMs)
	return result
}

// extractSmartQueryResult pulls generated_sql and execution fields from a smartquery tool result.
func extractSmartQueryResult(result *lhCaseResult, toolName string, toolResult M) {
	if toolName != "smartquery" {
		return
	}
	if sql, ok := toolResult["generated_sql"].(string); ok && sql != "" {
		result.GeneratedSQL = sql
	}
	if s, ok := toolResult["execution_status"].(string); ok {
		result.ExecutionStatus = s
	}
	if r, ok := toolResult["execution_result"].(string); ok {
		result.ExecutionResult = r
	}
	if e, ok := toolResult["execution_error"].(string); ok {
		result.ExecutionError = e
	}
}

// ======================== Run Versioning Handlers ========================

// handleLHTestRuns — GET: list runs for a suite; POST: create a new run (snapshots questions)
func handleLHTestRuns(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT r.id, r.title, r.llm_config_id, r.status,
			r.concurrency, r.total, r.completed_count, r.error_count,
			r.is_default, r.started_at, r.finished_at, r.created_at,
			COALESCE(NULLIF(r.llm_alias,''), c.alias, NULLIF(r.llm_model_name,''), c.model_name, '')
			FROM ont_test_run r
			LEFT JOIN llm_config c ON r.llm_config_id = c.id
			WHERE r.suite_id = $1
			ORDER BY r.created_at DESC`, suiteID)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()
		var runs []M
		for rows.Next() {
			var id, title, llmConfigID, status, modelName string
			var concurrency, total, completedCount, errorCount int
			var isDefault bool
			var startedAt, finishedAt sql.NullTime
			var createdAt time.Time
			rows.Scan(&id, &title, &llmConfigID, &status,
				&concurrency, &total, &completedCount, &errorCount,
				&isDefault, &startedAt, &finishedAt, &createdAt,
				&modelName)
			m := M{
				"id": id, "title": title, "llmConfigId": llmConfigID,
				"status": status, "concurrency": concurrency,
				"total": total, "completedCount": completedCount, "errorCount": errorCount,
				"isDefault": isDefault, "modelName": modelName,
				"createdAt": createdAt.Format(time.RFC3339),
			}
			if startedAt.Valid {
				m["startedAt"] = startedAt.Time.Format(time.RFC3339)
			}
			if finishedAt.Valid {
				m["finishedAt"] = finishedAt.Time.Format(time.RFC3339)
			}
			runs = append(runs, m)
		}
		if runs == nil {
			runs = []M{}
		}
		ListResp(w, runs, len(runs))

	case http.MethodPost:
		body := ReadBody(r)
		title := StrVal(body, "title")
		concurrency := 1
		if v, ok := body["concurrency"].(float64); ok && v >= 1 && v <= 10 {
			concurrency = int(v)
		}
		if title == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "title required"})
			return
		}

		// Snapshot the current "agent" LLM config (was: 3-tier role chain
		// ok_workbench/ont_route/sql_generate, simplified 2026-04-20). Resolve
		// to llm_config.id so later runs/retries use this exact config even if
		// the global default changes.
		var llmConfigID string
		db.QueryRow(`SELECT config_id FROM llm_role_binding WHERE role_name = 'agent'`).Scan(&llmConfigID)
		if llmConfigID == "" {
			db.QueryRow(
				`SELECT id FROM llm_config WHERE config_type='chat' AND is_active=TRUE ORDER BY updated_at DESC NULLS LAST, created_at DESC LIMIT 1`,
			).Scan(&llmConfigID)
		}
		if !IsValidUUID(llmConfigID) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "no default LLM config available — configure a chat role binding or an active chat config"})
			return
		}

		// Snapshot the llm_config fields at creation time so the run retains
		// vendor/model/alias metadata even if the live config is later deleted
		// (FK is ON DELETE SET NULL — see migration 20260428b).
		var snapVendor, snapModel, snapAlias sql.NullString
		_ = db.QueryRow(`SELECT vendor, model_name, COALESCE(alias,'') FROM llm_config WHERE id = $1`, llmConfigID).
			Scan(&snapVendor, &snapModel, &snapAlias)

		// Create the run
		var runID string
		err := db.QueryRow(`INSERT INTO ont_test_run
				(suite_id, title, llm_config_id, llm_vendor, llm_model_name, llm_alias, concurrency)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), $7) RETURNING id`,
			suiteID, title, llmConfigID, snapVendor.String, snapModel.String, snapAlias.String, concurrency).Scan(&runID)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}

		// Snapshot questions from ont_test_case into ont_test_run_case
		snapRes, _ := db.Exec(`INSERT INTO ont_test_run_case (run_id, case_id, sort_order, code, user_question)
			SELECT $1, id, sort_order, COALESCE(code,''), user_question
			FROM ont_test_case WHERE suite_id = $2
			ORDER BY sort_order`, runID, suiteID)
		snapCount, _ := snapRes.RowsAffected()

		// Update total on the run
		db.Exec(`UPDATE ont_test_run SET total = $1 WHERE id = $2`, snapCount, runID)

		JsonResp(w, M{"id": runID, "title": title, "total": snapCount})

	default:
		w.WriteHeader(405)
	}
}

// handleLHTestRunByID — GET: run detail with all run-cases; DELETE: remove run
//
// Query param ?lite=1 strips the heavy per-case payload (functionCalls,
// generatedSql, executionResult, executionError). For a 86-case run this
// shrinks the response from ~2.6 MB to ~200 KB and is what the detail page's
// list view asks for; the right-hand CaseDetailPanel then lazy-fetches the
// heavy fields for just the currently-selected case via
// GET /runs/{runId}/cases/{rcId}. Default (no ?lite) is still the full
// payload so the export/compare paths keep working unchanged.
func handleLHTestRunByID(db *sql.DB, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		lite := r.URL.Query().Get("lite") == "1"
		var title, status, modelName string
		var concurrency, total, completedCount, errorCount int
		var isDefault bool
		var startedAt, finishedAt sql.NullTime
		var createdAt time.Time
		err := db.QueryRow(`SELECT r.title, r.status, r.concurrency,
			r.total, r.completed_count, r.error_count, r.is_default,
			r.started_at, r.finished_at, r.created_at,
			COALESCE(NULLIF(r.llm_alias,''), c.alias, NULLIF(r.llm_model_name,''), c.model_name, '')
			FROM ont_test_run r
			LEFT JOIN llm_config c ON r.llm_config_id = c.id
			WHERE r.id = $1 AND r.suite_id = $2`, runID, suiteID).
			Scan(&title, &status, &concurrency,
				&total, &completedCount, &errorCount, &isDefault,
				&startedAt, &finishedAt, &createdAt,
				&modelName)
		if err != nil {
			w.WriteHeader(404)
			JsonResp(w, M{"error": "run not found"})
			return
		}
		run := M{
			"id": runID, "suiteId": suiteID, "title": title,
			"status": status, "concurrency": concurrency,
			"total": total, "completedCount": completedCount, "errorCount": errorCount,
			"isDefault": isDefault, "modelName": modelName,
			"createdAt": createdAt.Format(time.RFC3339),
		}
		if startedAt.Valid {
			run["startedAt"] = startedAt.Time.Format(time.RFC3339)
		}
		if finishedAt.Valid {
			run["finishedAt"] = finishedAt.Time.Format(time.RFC3339)
		}

		// Load run-cases. In lite mode, skip the heavy columns at the SQL
		// level so we don't pay the DB read or JSON serialization cost for
		// fields the list view never renders — those come from the per-case
		// endpoint when the user opens the detail panel.
		var caseRows *sql.Rows
		if lite {
			caseRows, _ = db.Query(`SELECT rc.id, rc.case_id, rc.sort_order, rc.code, rc.user_question,
				rc.status, COALESCE(rc.final_answer,''),
				COALESCE(rc.execution_status,''),
				rc.duration_ms, COALESCE(rc.model_name,''),
				rc.prompt_tokens, rc.completion_tokens, rc.total_tokens,
				rc.mark, rc.note, rc.created_at
				FROM ont_test_run_case rc
				WHERE rc.run_id = $1 ORDER BY rc.sort_order`, runID)
		} else {
			caseRows, _ = db.Query(`SELECT rc.id, rc.case_id, rc.sort_order, rc.code, rc.user_question,
				rc.status, rc.function_calls, COALESCE(rc.final_answer,''),
				COALESCE(rc.generated_sql,''), COALESCE(rc.execution_status,''),
				COALESCE(rc.execution_result,''), COALESCE(rc.execution_error,''),
				rc.duration_ms, COALESCE(rc.model_name,''),
				rc.prompt_tokens, rc.completion_tokens, rc.total_tokens,
				rc.mark, rc.note, rc.created_at
				FROM ont_test_run_case rc
				WHERE rc.run_id = $1 ORDER BY rc.sort_order`, runID)
		}
		var cases []M
		caseIDList := make([]string, 0, 64)
		if caseRows != nil {
			for caseRows.Next() {
				var rcID, caseID, code, question, rcStatus string
				var sortOrder, durMs, pt, ct, tt int
				var fcRaw sql.NullString
				var finalAnswer, genSQL, execStatus, execResult, execError, mModel string
				var mark, note sql.NullString
				var cCreatedAt time.Time
				if lite {
					caseRows.Scan(&rcID, &caseID, &sortOrder, &code, &question,
						&rcStatus, &finalAnswer,
						&execStatus,
						&durMs, &mModel, &pt, &ct, &tt, &mark, &note, &cCreatedAt)
				} else {
					caseRows.Scan(&rcID, &caseID, &sortOrder, &code, &question,
						&rcStatus, &fcRaw, &finalAnswer,
						&genSQL, &execStatus, &execResult, &execError,
						&durMs, &mModel, &pt, &ct, &tt, &mark, &note, &cCreatedAt)
				}
				c := M{
					"id": rcID, "caseId": caseID, "sortOrder": sortOrder,
					"code": code, "userQuestion": question,
					"status": rcStatus, "finalAnswer": finalAnswer,
					"executionStatus": execStatus,
					"durationMs":      durMs, "modelName": mModel,
					"promptTokens": pt, "completionTokens": ct, "totalTokens": tt,
					"createdAt": cCreatedAt.Format(time.RFC3339),
				}
				if !lite {
					c["generatedSql"] = genSQL
					c["executionResult"] = execResult
					c["executionError"] = execError
					if fcRaw.Valid && fcRaw.String != "" {
						var fcs interface{}
						json.Unmarshal([]byte(fcRaw.String), &fcs)
						c["functionCalls"] = fcs
					}
				}
				if mark.Valid {
					c["mark"] = mark.String
				}
				if note.Valid {
					c["note"] = note.String
				}
				caseIDList = append(caseIDList, caseID)
				cases = append(cases, c)
			}
			caseRows.Close()
		}
		// Bulk-load tags keyed by case_id, then attach. One query, no N+1.
		tagMap := loadTagsForCases(db, caseIDList)
		for _, c := range cases {
			cid, _ := c["caseId"].(string)
			if tags, ok := tagMap[cid]; ok {
				c["tags"] = tags
			} else {
				c["tags"] = []M{}
			}
		}
		if cases == nil {
			cases = []M{}
		}
		run["cases"] = cases
		JsonResp(w, run)

	case http.MethodDelete:
		db.Exec(`DELETE FROM ont_test_run WHERE id = $1 AND suite_id = $2`, runID, suiteID)
		JsonResp(w, M{"ok": true})

	default:
		w.WriteHeader(405)
	}
}

// handleLHTestRunCaseDetail — GET /runs/{runId}/cases/{rcId}
//
// Returns the single run-case with all heavy fields (functionCalls,
// generatedSql, executionResult, executionError) the CaseDetailPanel needs.
// Paired with the lite=1 listing endpoint — the list view never pulls these
// fields for 86 cases at once, and the detail panel fetches them only for the
// case the user just opened.
func handleLHTestRunCaseDetail(db *sql.DB, rcID, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	CorsHeaders(w)

	if !IsValidUUID(rcID) || !IsValidUUID(runID) || !IsValidUUID(suiteID) {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "invalid id"})
		return
	}

	var rcIDOut, caseID, code, question, rcStatus string
	var sortOrder, durMs, pt, ct, tt int
	var fcRaw sql.NullString
	var finalAnswer, genSQL, execStatus, execResult, execError, mModel string
	var mark, note sql.NullString
	var cCreatedAt time.Time
	err := db.QueryRow(`SELECT rc.id, rc.case_id, rc.sort_order, rc.code, rc.user_question,
		rc.status, rc.function_calls, COALESCE(rc.final_answer,''),
		COALESCE(rc.generated_sql,''), COALESCE(rc.execution_status,''),
		COALESCE(rc.execution_result,''), COALESCE(rc.execution_error,''),
		rc.duration_ms, COALESCE(rc.model_name,''),
		rc.prompt_tokens, rc.completion_tokens, rc.total_tokens,
		rc.mark, rc.note, rc.created_at
		FROM ont_test_run_case rc
		JOIN ont_test_run r ON r.id = rc.run_id
		WHERE rc.id = $1 AND rc.run_id = $2 AND r.suite_id = $3`, rcID, runID, suiteID).
		Scan(&rcIDOut, &caseID, &sortOrder, &code, &question,
			&rcStatus, &fcRaw, &finalAnswer,
			&genSQL, &execStatus, &execResult, &execError,
			&durMs, &mModel, &pt, &ct, &tt, &mark, &note, &cCreatedAt)
	if err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "run-case not found"})
		return
	}
	resp := M{
		"id": rcIDOut, "caseId": caseID, "sortOrder": sortOrder,
		"code": code, "userQuestion": question,
		"status": rcStatus, "finalAnswer": finalAnswer,
		"generatedSql": genSQL, "executionStatus": execStatus,
		"executionResult": execResult, "executionError": execError,
		"durationMs": durMs, "modelName": mModel,
		"promptTokens": pt, "completionTokens": ct, "totalTokens": tt,
		"createdAt": cCreatedAt.Format(time.RFC3339),
	}
	if fcRaw.Valid && fcRaw.String != "" {
		var fcs interface{}
		json.Unmarshal([]byte(fcRaw.String), &fcs)
		resp["functionCalls"] = fcs
	}
	if mark.Valid {
		resp["mark"] = mark.String
	}
	if note.Valid {
		resp["note"] = note.String
	}
	JsonResp(w, resp)
}

// handleLHTestRunDefault — PUT: set a run as the default for its suite
func handleLHTestRunDefault(db *sql.DB, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	// Clear default on all runs for this suite, then set on this one
	db.Exec(`UPDATE ont_test_run SET is_default = false, updated_at = now() WHERE suite_id = $1`, suiteID)
	db.Exec(`UPDATE ont_test_run SET is_default = true, updated_at = now() WHERE id = $1 AND suite_id = $2`, runID, suiteID)
	JsonResp(w, M{"ok": true})
}

// updateLHRunStats recalculates completed_count and error_count for a run.
// Single SQL with subqueries so all three counts are read in one DB roundtrip
// and the UPDATE sees a consistent snapshot — replaces the previous 4-query
// pattern that could interleave with concurrent worker UPDATEs and produce
// stats that didn't add up.
func updateLHRunStats(db *sql.DB, runID string) {
	db.Exec(`UPDATE ont_test_run SET
		total           = (SELECT COUNT(*) FROM ont_test_run_case WHERE run_id = $1),
		completed_count = (SELECT COUNT(*) FROM ont_test_run_case WHERE run_id = $1 AND status = 'completed'),
		error_count     = (SELECT COUNT(*) FROM ont_test_run_case WHERE run_id = $1 AND (status = 'error' OR execution_status = 'error')),
		updated_at      = now()
		WHERE id = $1`, runID)
}

// handleLHTestRunCancel — dispatch handler for the flat
//
//	/api/ontology/lh-test-runs/{runId}/{action}
//
// path family. Currently supports:
//
//	POST .../{id}/cancel    — cooperative cancel (sets cancel_requested=true)
//	GET  .../{id}/progress  — lightweight {status,total,completed,error,cancel} JSON
//
// Kept on a separate flat prefix (not nested under /lh-test-suites/{id}/runs/...)
// because: (a) the spec asks for it, (b) cancel/progress are run-scoped and
// don't logically need the suite in the URL, and (c) the polling endpoint is
// hot — a shorter URL keeps middleware and route matching cheaper.
func handleLHTestRunCancel(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		// Parse path: /api/ontology/lh-test-runs/{id}/{action}
		const prefix = "/api/ontology/lh-test-runs/"
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
		if len(parts) < 2 {
			w.WriteHeader(404)
			JsonResp(w, M{"error": "expected /lh-test-runs/{id}/{action}"})
			return
		}
		runID, action := parts[0], parts[1]
		if !IsValidUUID(runID) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "invalid run id"})
			return
		}

		// Cross-project IDOR guard: ont_test_run has no project_id of its own,
		// so resolve it via JOIN up to the owning suite before any access.
		if !authmw.EnforceEntityProjectVia(w, r, db,
			"SELECT s.project_id FROM ont_test_run r JOIN ont_test_suite s ON s.id = r.suite_id WHERE r.id = $1", runID) {
			return
		}

		switch action {
		case "cancel":
			if r.Method != http.MethodPost {
				w.WriteHeader(405)
				return
			}
			// Only cancellable while queued or running. Already-finished runs
			// should not silently flip to cancelled — that would mislead the UI.
			res, err := db.Exec(`UPDATE ont_test_run
				SET cancel_requested = true, updated_at = now()
				WHERE id = $1 AND status IN ('queued','running')`, runID)
			if err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				JsonResp(w, M{"ok": false, "error": "run not found or not active"})
				return
			}
			JsonResp(w, M{"ok": true})

		case "progress":
			if r.Method != http.MethodGet {
				w.WriteHeader(405)
				return
			}
			// Single-row hot-path query. Front-end polls this every few seconds
			// while a run is active; keep the payload tiny (~100B) so it stays
			// cheap regardless of suite size.
			var status string
			var total, completed, errCount int
			var cancelReq bool
			err := db.QueryRow(`SELECT status, total, completed_count, error_count, cancel_requested
				FROM ont_test_run WHERE id = $1`, runID).
				Scan(&status, &total, &completed, &errCount, &cancelReq)
			if err != nil {
				w.WriteHeader(404)
				JsonResp(w, M{"error": "run not found"})
				return
			}
			JsonResp(w, M{
				"status":          status,
				"totalCount":      total,
				"completedCount":  completed,
				"errorCount":      errCount,
				"cancelRequested": cancelReq,
			})

		default:
			w.WriteHeader(404)
			JsonResp(w, M{"error": "unknown action: " + action})
		}
	}
}

// handleLHTestRunEnqueue — POST: enqueue a run for background execution
func handleLHTestRunEnqueue(db *sql.DB, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}

	// Check run exists and is not already active
	var currentStatus string
	err := db.QueryRow(`SELECT r.status FROM ont_test_run r WHERE r.id = $1 AND r.suite_id = $2`, runID, suiteID).Scan(&currentStatus)
	if err != nil {
		JsonResp(w, M{"error": "run not found"})
		return
	}
	if currentStatus == "running" || currentStatus == "queued" {
		JsonResp(w, M{"error": "run 正在执行或排队中", "status": currentStatus})
		return
	}

	// Check there are pending cases
	var pendingCount int
	db.QueryRow(`SELECT COUNT(*) FROM ont_test_run_case WHERE run_id = $1 AND status = 'pending'`, runID).Scan(&pendingCount)
	if pendingCount == 0 {
		JsonResp(w, M{"error": "没有待执行的测试用例"})
		return
	}

	// Enqueue: background worker will pick it up. Clear any stale
	// cancel_requested flag so a re-queue after a previous cancel actually runs.
	db.Exec(`UPDATE ont_test_run SET status = 'queued', cancel_requested = false, updated_at = now() WHERE id = $1`, runID)
	JsonResp(w, M{"status": "queued", "pendingCount": pendingCount})
}

// handleLHTestRunRetryErrors — POST: reset error cases to pending and re-queue
func handleLHTestRunRetryErrors(db *sql.DB, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}

	// Check run exists and is not active
	var currentStatus string
	err := db.QueryRow(`SELECT r.status FROM ont_test_run r WHERE r.id = $1 AND r.suite_id = $2`, runID, suiteID).Scan(&currentStatus)
	if err != nil {
		JsonResp(w, M{"error": "run not found"})
		return
	}
	if currentStatus == "running" || currentStatus == "queued" {
		JsonResp(w, M{"error": "run 正在执行或排队中"})
		return
	}

	// Reset error AND cancelled cases to pending — the user re-queues to retry,
	// and a previously-cancelled case is just as valid a retry candidate as a
	// failed one (cancellation is a "didn't run" state, not a "ran badly" one).
	res, _ := db.Exec(`UPDATE ont_test_run_case SET
		status = 'pending', generated_sql = '', execution_status = '', execution_result = '',
		execution_error = '', final_answer = '', function_calls = NULL,
		duration_ms = 0, model_name = '', prompt_tokens = 0, completion_tokens = 0, total_tokens = 0,
		updated_at = now()
		WHERE run_id = $1 AND (status = 'error' OR status = 'cancelled' OR (status = 'completed' AND execution_status = 'error'))`, runID)

	resetCount := int64(0)
	if res != nil {
		resetCount, _ = res.RowsAffected()
	}

	if resetCount == 0 {
		JsonResp(w, M{"error": "没有需要重试的错误用例"})
		return
	}

	// Enqueue. Clear cancel_requested so a previously-cancelled run actually
	// re-runs the freshly reset cases instead of immediately cancelling them.
	db.Exec(`UPDATE ont_test_run SET status = 'queued', cancel_requested = false, updated_at = now() WHERE id = $1`, runID)
	JsonResp(w, M{"status": "queued", "resetCount": resetCount})
}

// handleLHTestRunBulkRetry — POST: reset a caller-specified set of run-case ids
// to pending and re-queue the run. Body: { "rcIds": ["uuid", ...] }.
//
// Scoped by run_id so a stale/forged id from another run can't slip through.
// Only run-cases that are NOT currently pending or running are reset — worker
// is already going to pick those up, re-resetting would race its UPDATE.
// mark/note are preserved so the user sees "prev: ✗ → now: pending" continuity
// on the row; they'll re-judge once the fresh result lands. Same policy as
// retry-errors.
func handleLHTestRunBulkRetry(db *sql.DB, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		HandleOptions(w)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}

	var currentStatus string
	err := db.QueryRow(`SELECT r.status FROM ont_test_run r WHERE r.id = $1 AND r.suite_id = $2`, runID, suiteID).Scan(&currentStatus)
	if err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "run not found"})
		return
	}
	if currentStatus == "running" || currentStatus == "queued" {
		w.WriteHeader(409)
		JsonResp(w, M{"error": "run 正在执行或排队中"})
		return
	}

	body := ReadBody(r)
	rcIDs := uuidArrayFromBody(body["rcIds"])
	if len(rcIDs) == 0 {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "rcIds required"})
		return
	}

	res, err := db.Exec(`UPDATE ont_test_run_case SET
		status = 'pending', generated_sql = '', execution_status = '', execution_result = '',
		execution_error = '', final_answer = '', function_calls = NULL,
		duration_ms = 0, model_name = '', prompt_tokens = 0, completion_tokens = 0, total_tokens = 0,
		updated_at = now()
		WHERE run_id = $1 AND id = ANY($2) AND status NOT IN ('pending','running')`,
		runID, pq.Array(rcIDs))
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	resetCount := int64(0)
	if res != nil {
		resetCount, _ = res.RowsAffected()
	}
	if resetCount == 0 {
		JsonResp(w, M{"error": "没有可重跑的用例（可能都已在排队或运行中）"})
		return
	}

	db.Exec(`UPDATE ont_test_run SET status = 'queued', cancel_requested = false, updated_at = now() WHERE id = $1`, runID)
	JsonResp(w, M{"status": "queued", "resetCount": resetCount})
}

// handleLHTestRunCaseRetry — POST: retry a single run-case synchronously
func handleLHTestRunCaseRetry(db *sql.DB, rcID, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	// Load run-case + run (for llm_config_id) + project
	var question, code string
	var sortOrder int
	var llmConfigID, projectID string
	err := db.QueryRow(`SELECT rc.user_question, COALESCE(rc.code,''), rc.sort_order,
		r.llm_config_id, s.project_id
		FROM ont_test_run_case rc
		JOIN ont_test_run r ON r.id = rc.run_id
		JOIN ont_test_suite s ON s.id = r.suite_id
		WHERE rc.id = $1 AND rc.run_id = $2`, rcID, runID).
		Scan(&question, &code, &sortOrder, &llmConfigID, &projectID)
	if err != nil {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "run-case not found"})
		return
	}

	// Resolve LLM config
	llBaseURL, llAPIKey, llModelName, _, llIsToolCall, _, llVendor := llmclient.GetConfigByID(db, llmConfigID)
	if llBaseURL == "" {
		JsonResp(w, M{"error": "LLM 配置不可用"})
		return
	}

	// Clear prior result
	db.Exec(`UPDATE ont_test_run_case SET status = 'running', generated_sql = '', execution_status = '',
		execution_result = '', execution_error = '', final_answer = '', function_calls = NULL,
		duration_ms = 0, updated_at = now() WHERE id = $1`, rcID)

	result := runLakehouseTestCase(r.Context(), db, projectID, suiteID, rcID, question, code, sortOrder,
		llBaseURL, llAPIKey, llModelName, llIsToolCall, llVendor)

	fcJSON, _ := json.Marshal(result.FunctionCalls)
	db.Exec(`UPDATE ont_test_run_case SET
		status = $1, generated_sql = $2, execution_status = $3,
		execution_result = $4, execution_error = $5, final_answer = $6,
		function_calls = $7::jsonb, duration_ms = $8, model_name = $9,
		prompt_tokens = $10, completion_tokens = $11, total_tokens = $12,
		updated_at = now()
		WHERE id = $13`,
		result.Status, result.GeneratedSQL, result.ExecutionStatus,
		result.ExecutionResult, result.ExecutionError, result.FinalAnswer,
		string(fcJSON), result.DurationMs, result.ModelName,
		result.PromptTokens, result.CompletionTokens, result.TotalTokens,
		rcID)

	updateLHRunStats(db, runID)

	JsonResp(w, M{
		"caseId":          rcID,
		"code":            result.Code,
		"status":          result.Status,
		"generatedSql":    result.GeneratedSQL,
		"executionStatus": result.ExecutionStatus,
		"executionResult": result.ExecutionResult,
		"executionError":  result.ExecutionError,
		"finalAnswer":     result.FinalAnswer,
		"functionCalls":   result.FunctionCalls,
		"durationMs":      result.DurationMs,
		"modelName":       result.ModelName,
	})
}

// handleLHTestRunCaseMark — PUT: mark a run-case as correct/incorrect
func handleLHTestRunCaseMark(db *sql.DB, rcID, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	body := ReadBody(r)
	markVal := StrVal(body, "mark")
	if markVal == "" || markVal == "null" {
		db.Exec(`UPDATE ont_test_run_case SET mark = NULL, updated_at = now() WHERE id = $1`, rcID)
	} else {
		db.Exec(`UPDATE ont_test_run_case SET mark = $1, updated_at = now() WHERE id = $2`, markVal, rcID)
	}
	updateLHRunStats(db, runID)
	JsonResp(w, M{"ok": true})
}

// handleLHTestRunCaseNote — PUT: set or clear the free-text reviewer note on a run-case.
// Body: {"note": "..."}. Empty string or missing key clears the note. Does not
// affect stats (note is annotation metadata only; mark drives pass/fail counts).
func handleLHTestRunCaseNote(db *sql.DB, rcID, suiteID, runID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	body := ReadBody(r)
	noteVal := StrVal(body, "note")
	if noteVal == "" {
		db.Exec(`UPDATE ont_test_run_case SET note = NULL, updated_at = now() WHERE id = $1`, rcID)
	} else {
		db.Exec(`UPDATE ont_test_run_case SET note = $1, updated_at = now() WHERE id = $2`, noteVal, rcID)
	}
	JsonResp(w, M{"ok": true})
}

// handleLHCompare — GET: side-by-side comparison of N runs.
//
// Query params (in priority order):
//
//	?runs=id1,id2,id3,...   — N-way compare (preferred, ≥2 ids)
//	?runA=&runB=            — legacy 2-way; auto-translated to runs=runA,runB
//
// Response shape:
//
//	{
//	  "runs": [{id,title,modelName}, ...]   // N entries, requested order
//	  "rows": [{caseId, question, code, sortOrder,
//	            results: [runCase|null, ...]}]   // per row, results aligned with runs
//	  // Legacy aliases (only when len(runs)==2):
//	  //   "runA","runB" mirror runs[0],[1]
//	  //   per-row "resultA","resultB" mirror results[0],[1]
//	}
func handleLHCompare(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	CorsHeaders(w)

	// Parse run IDs: prefer ?runs=...; fall back to legacy ?runA=&runB=.
	var runIDs []string
	if raw := r.URL.Query().Get("runs"); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				runIDs = append(runIDs, id)
			}
		}
	} else {
		for _, k := range []string{"runA", "runB"} {
			if v := r.URL.Query().Get(k); v != "" {
				runIDs = append(runIDs, v)
			}
		}
	}

	if len(runIDs) < 2 {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "at least 2 run IDs required (?runs=id1,id2[,id3...])"})
		return
	}
	for _, rid := range runIDs {
		if !IsValidUUID(rid) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "invalid run id: " + rid})
			return
		}
	}

	// Dedupe (preserve first-seen order) + validate all belong to this suite.
	seen := map[string]bool{}
	dedup := runIDs[:0:len(runIDs)]
	for _, rid := range runIDs {
		if seen[rid] {
			continue
		}
		var check string
		db.QueryRow(`SELECT id FROM ont_test_run WHERE id = $1 AND suite_id = $2`, rid, suiteID).Scan(&check)
		if check == "" {
			w.WriteHeader(404)
			JsonResp(w, M{"error": "run not found in this suite: " + rid})
			return
		}
		seen[rid] = true
		dedup = append(dedup, rid)
	}
	runIDs = dedup

	// Load run metadata for each id, preserving requested order.
	type runMeta struct {
		ID, Title, ModelName string
	}
	runMetas := make([]runMeta, len(runIDs))
	for i, rid := range runIDs {
		db.QueryRow(`SELECT r.id, r.title, COALESCE(c.model_name,'')
			FROM ont_test_run r LEFT JOIN llm_config c ON r.llm_config_id = c.id
			WHERE r.id = $1`, rid).Scan(&runMetas[i].ID, &runMetas[i].Title, &runMetas[i].ModelName)
	}

	// Load run-case SUMMARIES for each run, keyed by case_id.
	//
	// Summary excludes the heavy fields (final_answer, generated_sql,
	// execution_result, execution_error, function_calls) so a 50-case x 5-run
	// compare is ~25KB instead of ~7MB. Detail is lazy-loaded one row at a
	// time via /compare/case/{caseId}?runs=... when the user picks a row.
	//
	// Kept fields are exactly what the question-list badges + WIN/LOSS
	// detection need: status, executionStatus, mark, note, durationMs, modelName.
	loadCases := func(runID string) map[string]M {
		rows, _ := db.Query(`SELECT rc.case_id, rc.id, rc.status,
			COALESCE(rc.execution_status,''),
			rc.duration_ms, COALESCE(rc.model_name,''),
			rc.mark, rc.note
			FROM ont_test_run_case rc WHERE rc.run_id = $1`, runID)
		m := map[string]M{}
		if rows != nil {
			for rows.Next() {
				var caseID, rcID, status, execStatus, mModel string
				var durMs int
				var mark, note sql.NullString
				rows.Scan(&caseID, &rcID, &status, &execStatus,
					&durMs, &mModel, &mark, &note)
				entry := M{
					"runCaseId":       rcID,
					"status":          status,
					"executionStatus": execStatus,
					"durationMs":      durMs,
					"modelName":       mModel,
				}
				if mark.Valid {
					entry["mark"] = mark.String
				}
				if note.Valid {
					entry["note"] = note.String
				}
				m[caseID] = entry
			}
			rows.Close()
		}
		return m
	}

	casesByRun := make([]map[string]M, len(runIDs))
	for i, rid := range runIDs {
		casesByRun[i] = loadCases(rid)
	}

	// Load all master questions for this suite, then stitch with each run's results.
	rows, _ := db.Query(`SELECT id, user_question, sort_order, COALESCE(code,'')
		FROM ont_test_case WHERE suite_id = $1 ORDER BY sort_order`, suiteID)
	var rows_out []M
	caseIDList := make([]string, 0, 64)
	if rows != nil {
		for rows.Next() {
			var caseID, question, code string
			var sortOrder int
			rows.Scan(&caseID, &question, &sortOrder, &code)
			results := make([]interface{}, len(runIDs))
			for i, m := range casesByRun {
				if v, ok := m[caseID]; ok {
					results[i] = v
				} else {
					results[i] = nil
				}
			}
			entry := M{
				"caseId": caseID, "question": question,
				"code": code, "sortOrder": sortOrder,
				"results": results,
			}
			// Legacy 2-way aliases (only when exactly 2 runs requested).
			if len(runIDs) == 2 {
				entry["resultA"] = results[0]
				entry["resultB"] = results[1]
			}
			caseIDList = append(caseIDList, caseID)
			rows_out = append(rows_out, entry)
		}
		rows.Close()
	}
	// Attach tags per row so the compare UI can filter by tag without a second fetch.
	tagMap := loadTagsForCases(db, caseIDList)
	for _, row := range rows_out {
		cid, _ := row["caseId"].(string)
		if tags, ok := tagMap[cid]; ok {
			row["tags"] = tags
		} else {
			row["tags"] = []M{}
		}
	}
	if rows_out == nil {
		rows_out = []M{}
	}

	runsOut := make([]M, len(runMetas))
	for i, rm := range runMetas {
		runsOut[i] = M{"id": rm.ID, "title": rm.Title, "modelName": rm.ModelName}
	}
	resp := M{
		"runs": runsOut,
		"rows": rows_out,
	}
	if len(runMetas) == 2 {
		resp["runA"] = runsOut[0]
		resp["runB"] = runsOut[1]
	}
	JsonResp(w, resp)
}

// handleLHCompareCase — GET: full per-run detail for a single case across N runs.
//
// Companion of handleLHCompare which is intentionally summary-only. The user
// picks a row from the question list → frontend lazy-fetches that one row's
// full content for every selected run via this endpoint. Payload bound:
// `len(runs) * single-case-detail` (typically tens of KB), independent of suite size.
//
// Path: /api/ontology/lh-test-suites/{suiteID}/compare/case/{caseID}?runs=id1,id2,id3,...
//
// Response: { caseId, results: [ { runId, runCaseId, status, finalAnswer,
//
//	generatedSql, executionStatus, executionResult,
//	executionError, durationMs, modelName, promptTokens,
//	completionTokens, totalTokens, functionCalls,
//	mark, note } | null, ... ] }
//
// `results` is aligned 1:1 with the requested `runs` order. Entries are null
// if (run not in suite) OR (run has no case row for this case_id yet — pending).
func handleLHCompareCase(db *sql.DB, suiteID, caseID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	CorsHeaders(w)
	if !IsValidUUID(caseID) {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "invalid case id"})
		return
	}

	// Validate caseID belongs to suite (cheap; avoids cross-suite leakage).
	var checkCase string
	db.QueryRow(`SELECT id FROM ont_test_case WHERE id = $1 AND suite_id = $2`,
		caseID, suiteID).Scan(&checkCase)
	if checkCase == "" {
		w.WriteHeader(404)
		JsonResp(w, M{"error": "case not found in this suite"})
		return
	}

	// Parse runs query.
	var runIDs []string
	if raw := r.URL.Query().Get("runs"); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				runIDs = append(runIDs, id)
			}
		}
	}
	if len(runIDs) == 0 {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "runs query param required (?runs=id1,id2,...)"})
		return
	}
	for _, rid := range runIDs {
		if !IsValidUUID(rid) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "invalid run id: " + rid})
			return
		}
	}

	results := make([]interface{}, len(runIDs))
	for i, rid := range runIDs {
		// Verify run belongs to suite; otherwise null (do NOT 404 the whole
		// request — partial visibility is more useful in compare context).
		var checkRun string
		db.QueryRow(`SELECT id FROM ont_test_run WHERE id = $1 AND suite_id = $2`,
			rid, suiteID).Scan(&checkRun)
		if checkRun == "" {
			results[i] = nil
			continue
		}

		var rcID, status, finalAns, genSQL, execStatus, execResult, execError, mModel string
		var durMs, pt, ct, tt int
		var fcRaw sql.NullString
		var mark, note sql.NullString
		err := db.QueryRow(`SELECT rc.id, rc.status, COALESCE(rc.final_answer,''),
			COALESCE(rc.generated_sql,''), COALESCE(rc.execution_status,''),
			COALESCE(rc.execution_result,''), COALESCE(rc.execution_error,''),
			rc.duration_ms, COALESCE(rc.model_name,''),
			rc.prompt_tokens, rc.completion_tokens, rc.total_tokens,
			rc.function_calls, rc.mark, rc.note
			FROM ont_test_run_case rc WHERE rc.run_id = $1 AND rc.case_id = $2`,
			rid, caseID).Scan(
			&rcID, &status, &finalAns, &genSQL, &execStatus, &execResult, &execError,
			&durMs, &mModel, &pt, &ct, &tt, &fcRaw, &mark, &note)
		if err != nil {
			// No row yet for this (run, case) pair — pending case.
			results[i] = nil
			continue
		}

		entry := M{
			"runId":            rid,
			"runCaseId":        rcID,
			"status":           status,
			"finalAnswer":      finalAns,
			"generatedSql":     genSQL,
			"executionStatus":  execStatus,
			"executionResult":  execResult,
			"executionError":   execError,
			"durationMs":       durMs,
			"modelName":        mModel,
			"promptTokens":     pt,
			"completionTokens": ct,
			"totalTokens":      tt,
		}
		if fcRaw.Valid && fcRaw.String != "" {
			var fcs interface{}
			json.Unmarshal([]byte(fcRaw.String), &fcs)
			entry["functionCalls"] = fcs
		}
		if mark.Valid {
			entry["mark"] = mark.String
		}
		if note.Valid {
			entry["note"] = note.String
		}
		results[i] = entry
	}

	JsonResp(w, M{
		"caseId":  caseID,
		"results": results,
	})
}
