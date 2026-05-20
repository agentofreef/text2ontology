package handler

// handler_query_log.go — Query log CRUD and example-questions handlers formerly
// in handler_query.go. Rescued because routes.go still registers these endpoints.
// The 7-stage pipeline handler (handleOntologyQuery) was removed with the pipeline.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

func handleQueryLogs(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)
		if pid == "" {
			ListResp(w, []M{}, 0)
			return
		}
		sourceType := r.URL.Query().Get("sourceType")
		sourceVal := r.URL.Query().Get("source")
		suiteID := r.URL.Query().Get("suiteId")
		markFilter := r.URL.Query().Get("mark")
		searchStr := r.URL.Query().Get("search")
		pageStr := r.URL.Query().Get("page")
		pageSizeStr := r.URL.Query().Get("pageSize")

		page := 1
		pageSize := 20
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
		if ps, err := strconv.Atoi(pageSizeStr); err == nil && ps > 0 && ps <= 200 {
			pageSize = ps
		}

		where := ` WHERE project_id = $1`
		var args []interface{}
		idx := 2
		args = append(args, pid)
		if sourceType != "" {
			where += fmt.Sprintf(` AND COALESCE(source_type,'pipeline') = $%d`, idx)
			args = append(args, sourceType)
			idx++
		}
		if sourceVal != "" {
			where += fmt.Sprintf(` AND COALESCE(source,'chat') = $%d`, idx)
			args = append(args, sourceVal)
			idx++
		}
		if suiteID != "" {
			where += fmt.Sprintf(` AND test_suite_id = $%d`, idx)
			args = append(args, suiteID)
			idx++
		}
		if markFilter == "true" {
			where += ` AND mark = true`
		} else if markFilter == "false" {
			where += ` AND mark = false`
		}
		if searchStr != "" {
			where += fmt.Sprintf(` AND (user_question ILIKE $%d OR COALESCE(objects,'') ILIKE $%d)`, idx, idx)
			args = append(args, "%"+searchStr+"%")
			idx++
		}

		// Total count for pagination
		var total int
		db.QueryRow(`SELECT COUNT(*) FROM ont_query_log`+where, args...).Scan(&total)

		// Paginated data query
		offset := (page - 1) * pageSize
		dataArgs := make([]interface{}, len(args), len(args)+2)
		copy(dataArgs, args)
		dataArgs = append(dataArgs, pageSize, offset)

		baseQ := `SELECT id, project_id, user_question,
			tokens, intent_signals, vector_hits, anchor_result, disambig_result,
			method_call, COALESCE(generated_sql,''),
			COALESCE(execution_status,''),
			COALESCE(execution_result,''), COALESCE(execution_error,''),
			COALESCE(execution_duration,0), stage_latencies, COALESCE(summary,''),
			COALESCE(confidence,0), used_llm, COALESCE(model_name,''),
			mark, COALESCE(note,''), COALESCE(objects,''), COALESCE(metric,''), COALESCE(group_by,''),
			COALESCE(is_example, false), COALESCE(source, 'chat'),
			created_at
			FROM ont_query_log` + where +
			fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, idx, idx+1)

		rows, err := db.Query(baseQ, dataArgs...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, question, genSQL, status, result, execErr, summary, modelName, note string
			var tokensRaw, intentRaw, vectorRaw, anchorRaw, disambigRaw, methodRaw, latencyRaw sql.NullString
			var execDuration, confidence float64
			var usedLLM, mark, isExample bool
			var createdAt time.Time
			var objects, metric, groupByStr, source string
			rows.Scan(&id, &projectID, &question,
				&tokensRaw, &intentRaw, &vectorRaw, &anchorRaw, &disambigRaw,
				&methodRaw, &genSQL, &status, &result, &execErr,
				&execDuration, &latencyRaw, &summary, &confidence, &usedLLM, &modelName,
				&mark, &note, &objects, &metric, &groupByStr, &isExample, &source, &createdAt)

			entry := M{
				"id": id, "projectId": projectID,
				"userQuestion": question, "generatedSql": genSQL,
				"executionStatus": status, "executionResult": result,
				"executionError": execErr, "executionDuration": execDuration,
				"summary": summary, "confidence": confidence,
				"usedLLM": usedLLM, "modelName": modelName,
				"mark": mark, "note": note,
				"objects": objects, "metric": metric, "groupBy": groupByStr, "isExample": isExample,
				"source":    source,
				"createdAt": createdAt.Format(time.RFC3339),
			}

			// Parse JSON fields
			parseJSONField(tokensRaw, "tokens", entry)
			parseJSONField(intentRaw, "intentSignals", entry)
			parseJSONField(vectorRaw, "vectorHits", entry)
			parseJSONField(anchorRaw, "anchorResult", entry)
			parseJSONField(disambigRaw, "disambigResult", entry)
			parseJSONField(methodRaw, "methodCall", entry)
			parseJSONField(latencyRaw, "stageLatencies", entry)

			list = append(list, entry)
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, total)
	}
}

func handleQueryLogByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/query-logs")
		// Cross-project IDOR guard: verify project access before touching this
		// query log (covers the /{id}/feedback subtree too).
		if !authmw.EnforceEntityProject(w, r, db, "ont_query_log", "id", id) {
			return
		}

		// POST /api/ontology/query-logs/{id}/feedback
		if strings.HasSuffix(path, "/feedback") && r.Method == http.MethodPost {
			body := ReadBody(r)
			var fbID string
			correctionJSON, _ := json.Marshal(body["correction"])
			err := db.QueryRow(`INSERT INTO ont_query_feedback (query_log_id, feedback_type, correction, comment, created_by)
				VALUES ($1, $2, $3::jsonb, $4, $5) RETURNING id`,
				id, StrVal(body, "feedbackType"), string(correctionJSON),
				StrVal(body, "comment"), NilIfEmpty(StrVal(body, "createdBy"))).Scan(&fbID)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": fbID})
			return
		}

		// PUT /api/ontology/query-logs/{id}/mark
		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_query_log SET mark = $1 WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		// PUT /api/ontology/query-logs/{id}/example
		if strings.HasSuffix(path, "/example") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_query_log SET is_example = $1 WHERE id = $2`, BoolVal(body, "isExample"), id)
			JsonResp(w, M{"success": true})
			return
		}

		// PUT /api/ontology/query-logs/{id} — update tokens, objects, metric, groupBy
		if r.Method == http.MethodPut && !strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			if tokensJSON := StrVal(body, "tokens"); tokensJSON != "" {
				db.Exec(`UPDATE ont_query_log SET tokens = $1::jsonb WHERE id = $2`, tokensJSON, id)
			}
			if objects, ok := body["objects"].(string); ok {
				db.Exec(`UPDATE ont_query_log SET objects = $1 WHERE id = $2`, objects, id)
			}
			if metric, ok := body["metric"].(string); ok {
				db.Exec(`UPDATE ont_query_log SET metric = $1 WHERE id = $2`, metric, id)
			}
			if groupBy, ok := body["groupBy"].(string); ok {
				db.Exec(`UPDATE ont_query_log SET group_by = $1 WHERE id = $2`, groupBy, id)
			}
			JsonResp(w, M{"success": true})
			return
		}

		// DELETE
		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_query_log WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		// GET /api/ontology/query-logs/{id} — detail with function_calls
		if r.Method == http.MethodGet {
			var question, genSQL, status, result, execErr, note, objects, metric, groupByStr, source string
			var testSuiteID sql.NullString
			var mark, isExample bool
			var createdAt time.Time
			err := db.QueryRow(`SELECT user_question, COALESCE(generated_sql,''),
				COALESCE(execution_status,''), COALESCE(execution_result,''), COALESCE(execution_error,''),
				mark, COALESCE(note,''), COALESCE(objects,''), COALESCE(metric,''), COALESCE(group_by,''),
				COALESCE(source,'chat'), COALESCE(is_example,false), test_suite_id, created_at
				FROM ont_query_log WHERE id = $1`, id).
				Scan(&question, &genSQL, &status, &result, &execErr,
					&mark, &note, &objects, &metric, &groupByStr,
					&source, &isExample, &testSuiteID, &createdAt)
			if err != nil {
				w.WriteHeader(404)
				JsonResp(w, M{"error": "not found"})
				return
			}

			entry := M{
				"id": id, "userQuestion": question,
				"generatedSql": genSQL,
				"executionStatus": status, "executionResult": result, "executionError": execErr,
				"mark": mark, "note": note,
				"objects": objects, "metric": metric, "groupBy": groupByStr,
				"source": source, "isExample": isExample,
				"createdAt": createdAt.Format(time.RFC3339),
			}

			// If from a test suite, load function_calls + final_answer from ont_test_case
			if testSuiteID.Valid && testSuiteID.String != "" {
				entry["testSuiteId"] = testSuiteID.String
				var fcRaw sql.NullString
				var finalAnswer string
				db.QueryRow(`SELECT function_calls, COALESCE(final_answer,'')
					FROM ont_test_case WHERE suite_id = $1 AND user_question = $2
					LIMIT 1`, testSuiteID.String, question).Scan(&fcRaw, &finalAnswer)
				if fcRaw.Valid && fcRaw.String != "" {
					var fcs interface{}
					json.Unmarshal([]byte(fcRaw.String), &fcs)
					entry["functionCalls"] = fcs
				}
				entry["finalAnswer"] = finalAnswer
			}

			JsonResp(w, entry)
			return
		}

		http.NotFound(w, r)
	}
}

func parseJSONField(raw sql.NullString, key string, target M) {
	if raw.Valid && raw.String != "" {
		var parsed interface{}
		if json.Unmarshal([]byte(raw.String), &parsed) == nil {
			target[key] = parsed
		}
	}
}

func handleExampleQuestions(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}
		rows, _ := db.Query(`SELECT user_question FROM ont_query_log
			WHERE project_id = $1 AND is_example = true ORDER BY created_at DESC LIMIT 10`, pid)
		var list []M
		if rows != nil {
			for rows.Next() {
				var q string
				rows.Scan(&q)
				list = append(list, M{"question": q})
			}
			rows.Close()
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}
