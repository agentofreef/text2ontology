package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

func handleMethods(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			twJSON, _ := json.Marshal(body["triggerWords"])
			paramsJSON, _ := json.Marshal(body["parameters"])
			execJSON, _ := json.Marshal(body["executionConfig"])
			var id string
			err := db.QueryRow(`INSERT INTO ont_method (project_id, method_name, display_name,
				description, trigger_words, parameters, execution_config, is_enabled, mark, note)
				VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, true, false, $8) RETURNING id`,
				pid, StrVal(body, "methodName"), StrVal(body, "displayName"),
				StrVal(body, "description"), string(twJSON), string(paramsJSON), string(execJSON),
				StrVal(body, "note")).Scan(&id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id})
			return
		}

		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}
		rows, err := db.Query(`SELECT id, project_id, method_name, COALESCE(display_name,''),
			COALESCE(description,''), trigger_words, parameters, execution_config,
			is_enabled, mark, COALESCE(note,''), created_at, updated_at
			FROM ont_method WHERE project_id = $1 ORDER BY method_name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, methodName, displayName, desc, note string
			var twRaw, paramsRaw, execRaw sql.NullString
			var isEnabled, mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &methodName, &displayName, &desc,
				&twRaw, &paramsRaw, &execRaw, &isEnabled, &mark, &note, &createdAt, &updatedAt)

			var tw, params, exec interface{}
			if twRaw.Valid {
				json.Unmarshal([]byte(twRaw.String), &tw)
			}
			if paramsRaw.Valid {
				json.Unmarshal([]byte(paramsRaw.String), &params)
			}
			if execRaw.Valid {
				json.Unmarshal([]byte(execRaw.String), &exec)
			}

			list = append(list, M{
				"id": id, "projectId": projectID,
				"methodName": methodName, "displayName": displayName, "description": desc,
				"triggerWords": tw, "parameters": params, "executionConfig": exec,
				"isEnabled": isEnabled, "mark": mark, "note": note,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleMethodByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/methods")
		// Cross-project IDOR guard: verify project access before touching this method.
		if !authmw.EnforceEntityProject(w, r, db, "ont_method", "id", id) {
			return
		}

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_method SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			twJSON, _ := json.Marshal(body["triggerWords"])
			paramsJSON, _ := json.Marshal(body["parameters"])
			execJSON, _ := json.Marshal(body["executionConfig"])
			_, err := db.Exec(`UPDATE ont_method SET method_name = $2, display_name = $3,
				description = $4, trigger_words = $5::jsonb, parameters = $6::jsonb,
				execution_config = $7::jsonb, is_enabled = $8, note = $9, updated_at = now() WHERE id = $1`,
				id, StrVal(body, "methodName"), StrVal(body, "displayName"),
				StrVal(body, "description"), string(twJSON), string(paramsJSON), string(execJSON),
				BoolVal(body, "isEnabled"), StrVal(body, "note"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_method WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
