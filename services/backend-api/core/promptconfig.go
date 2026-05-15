package core

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

func RegisterPromptConfigRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/prompt-config", handlePromptConfig(db))
	mux.HandleFunc("/api/prompt-config/", handlePromptConfigByID(db))
}

func handlePromptConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			var maxVer int
			db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM prompt_config WHERE project_id=$1 AND config_key=$2`,
				pid, StrVal(body, "configKey")).Scan(&maxVer)
			var id string
			err := db.QueryRow(`INSERT INTO prompt_config (project_id, config_key, config_value, version, is_active, mark, note, created_by)
				VALUES ($1, $2, $3, $4, false, false, $5, 'a0000000-0000-0000-0000-000000000001') RETURNING id`,
				pid, StrVal(body, "configKey"), StrVal(body, "configValue"), maxVer+1, StrVal(body, "note")).Scan(&id)
			if err != nil {
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id, "version": maxVer + 1})
			return
		}

		q := `SELECT id, project_id, config_key, config_value, version, is_active, mark, COALESCE(note,''),
			COALESCE(created_by::text,''), created_at, updated_at FROM prompt_config`
		args := []interface{}{}
		if pid != "" {
			q += " WHERE project_id = $1"
			args = append(args, pid)
		}
		q += " ORDER BY config_key, version DESC"

		rows, err := db.Query(q, args...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var configs []M
		for rows.Next() {
			var id, projectID, key, value, note, createdBy string
			var version int
			var isActive, mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &key, &value, &version, &isActive, &mark, &note, &createdBy, &createdAt, &updatedAt)
			configs = append(configs, M{
				"id": id, "projectId": projectID, "configKey": key, "configValue": value,
				"version": version, "isActive": isActive, "mark": mark, "note": note,
				"createdBy": createdBy,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if configs == nil {
			configs = []M{}
		}
		ListResp(w, configs, len(configs))
	}
}

func handlePromptConfigByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/prompt-config")

		if strings.HasSuffix(r.URL.Path, "/activate") {
			var pid, key string
			db.QueryRow(`SELECT project_id, config_key FROM prompt_config WHERE id=$1`, id).Scan(&pid, &key)
			db.Exec(`UPDATE prompt_config SET is_active = false, updated_at = now() WHERE project_id=$1 AND config_key=$2`, pid, key)
			db.Exec(`UPDATE prompt_config SET is_active = true, updated_at = now() WHERE id=$1`, id)
			JsonResp(w, M{"success": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE prompt_config SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}
		JsonResp(w, M{"success": true})
	}
}
