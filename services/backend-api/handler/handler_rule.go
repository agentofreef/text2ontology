package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

func handleRules(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			configJSON, _ := json.Marshal(body["ruleConfig"])
			var id string
			err := db.QueryRow(`INSERT INTO ont_resolution_rule (project_id, rule_type,
				trigger_key, rule_config, priority, mark, note)
				VALUES ($1, $2, $3, $4::jsonb, $5, false, $6) RETURNING id`,
				pid, StrVal(body, "ruleType"),
				StrVal(body, "triggerKey"), string(configJSON), 0, StrVal(body, "note")).Scan(&id)
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
		rows, err := db.Query(`SELECT id, project_id, rule_type, trigger_key,
			rule_config, priority, mark, COALESCE(note,''), created_at
			FROM ont_resolution_rule WHERE project_id = $1 ORDER BY priority DESC, created_at`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, ruleType, triggerKey, note string
			var ruleConfigRaw string
			var priority int
			var mark bool
			var createdAt time.Time
			rows.Scan(&id, &projectID, &ruleType, &triggerKey, &ruleConfigRaw,
				&priority, &mark, &note, &createdAt)
			var ruleConfig interface{}
			json.Unmarshal([]byte(ruleConfigRaw), &ruleConfig)
			list = append(list, M{
				"id": id, "projectId": projectID,
				"ruleType": ruleType, "triggerKey": triggerKey, "ruleConfig": ruleConfig,
				"priority": priority, "mark": mark, "note": note,
				"createdAt": createdAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleRuleByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/rules")

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_resolution_rule SET mark = $1 WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			configJSON, _ := json.Marshal(body["ruleConfig"])
			_, err := db.Exec(`UPDATE ont_resolution_rule SET rule_type = $2, trigger_key = $3,
				rule_config = $4::jsonb, note = $5 WHERE id = $1`,
				id, StrVal(body, "ruleType"), StrVal(body, "triggerKey"),
				string(configJSON), StrVal(body, "note"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_resolution_rule WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
