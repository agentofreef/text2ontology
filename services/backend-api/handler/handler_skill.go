package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

// builtinSkills returns the list of built-in skills that are always available.
func builtinSkills() []M {
	return []M{
		{
			"id": "builtin-ontology-query", "skillName": "ontology_query",
			"displayName": "Ontology Query", "description": "Query the ontology knowledge graph to answer data questions using SQL.",
			"skillBody": "", "tools": []string{"ontology_query"},
			"isEnabled": true, "isBuiltin": true, "sortOrder": 0,
		},
		{
			"id": "builtin-ok-crud", "skillName": "ok_crud",
			"displayName": "OK CRUD", "description": "Create, read, update and delete ontology-knowledge entities (topics, knowledge entries, definitions, examples, causality).",
			"skillBody": "", "tools": []string{"ok_list_topics", "ok_get_topic", "ok_create_topic", "ok_update_topic", "ok_delete_topic", "ok_list_knowledge", "ok_get_knowledge", "ok_create_knowledge", "ok_update_knowledge", "ok_delete_knowledge"},
			"isEnabled": true, "isBuiltin": true, "sortOrder": 1,
		},
	}
}

func handleSkills(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			toolsJSON, _ := json.Marshal(body["tools"])
			var id string
			err := db.QueryRow(`INSERT INTO ont_skill (project_id, skill_name, display_name,
				description, skill_body, tools, is_enabled, sort_order)
				VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8) RETURNING id`,
				pid, StrVal(body, "skillName"), StrVal(body, "displayName"),
				StrVal(body, "description"), StrVal(body, "skillBody"), string(toolsJSON),
				BoolVal(body, "isEnabled"), numVal(body, "sortOrder")).Scan(&id)
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
		rows, err := db.Query(`SELECT id, project_id, skill_name, COALESCE(display_name,''),
			COALESCE(description,''), COALESCE(skill_body,''), tools,
			is_enabled, sort_order, created_at, updated_at
			FROM ont_skill WHERE project_id = $1 ORDER BY sort_order, skill_name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		// Prepend built-in skills
		list = append(list, builtinSkills()...)

		for rows.Next() {
			var id, projectID, skillName, displayName, desc, skillBody string
			var toolsRaw sql.NullString
			var isEnabled bool
			var sortOrder int
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &skillName, &displayName, &desc,
				&skillBody, &toolsRaw, &isEnabled, &sortOrder, &createdAt, &updatedAt)

			var tools interface{}
			if toolsRaw.Valid {
				json.Unmarshal([]byte(toolsRaw.String), &tools)
			}

			list = append(list, M{
				"id": id, "projectId": projectID,
				"skillName": skillName, "displayName": displayName, "description": desc,
				"skillBody": skillBody, "tools": tools,
				"isEnabled": isEnabled, "isBuiltin": false, "sortOrder": sortOrder,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleSkillByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/skills")
		// Cross-project IDOR guard: verify project access before touching this skill.
		if !authmw.EnforceEntityProject(w, r, db, "ont_skill", "id", id) {
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			toolsJSON, _ := json.Marshal(body["tools"])
			_, err := db.Exec(`UPDATE ont_skill SET skill_name = $2, display_name = $3,
				description = $4, skill_body = $5, tools = $6::jsonb,
				is_enabled = $7, sort_order = $8, updated_at = now() WHERE id = $1`,
				id, StrVal(body, "skillName"), StrVal(body, "displayName"),
				StrVal(body, "description"), StrVal(body, "skillBody"), string(toolsJSON),
				BoolVal(body, "isEnabled"), numVal(body, "sortOrder"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodGet {
			var skillName, displayName, desc, skillBody string
			var toolsRaw sql.NullString
			var isEnabled bool
			var sortOrder int
			var createdAt, updatedAt time.Time
			var projectID string
			err := db.QueryRow(`SELECT id, project_id, skill_name, COALESCE(display_name,''),
				COALESCE(description,''), COALESCE(skill_body,''), tools,
				is_enabled, sort_order, created_at, updated_at
				FROM ont_skill WHERE id = $1`, id).Scan(
				&id, &projectID, &skillName, &displayName, &desc,
				&skillBody, &toolsRaw, &isEnabled, &sortOrder, &createdAt, &updatedAt)
			if err != nil {
				w.WriteHeader(404)
				JsonResp(w, M{"error": "not found"})
				return
			}

			var tools interface{}
			if toolsRaw.Valid {
				json.Unmarshal([]byte(toolsRaw.String), &tools)
			}

			JsonResp(w, M{
				"id": id, "projectId": projectID,
				"skillName": skillName, "displayName": displayName, "description": desc,
				"skillBody": skillBody, "tools": tools,
				"isEnabled": isEnabled, "isBuiltin": false, "sortOrder": sortOrder,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
			return
		}

		if r.Method == http.MethodDelete {
			_, err := db.Exec(`DELETE FROM ont_skill WHERE id = $1`, id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
