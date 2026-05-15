package core

import (
	"database/sql"
	"net/http"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

func RegisterProjectRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/projects", handleProjects(db))
	mux.HandleFunc("/api/projects/", handleProjectByID(db))
}

func handleProjects(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method == http.MethodPost {
			body := ReadBody(r)
			name := StrVal(body, "name")
			var exists bool
			db.QueryRow(`SELECT EXISTS(SELECT 1 FROM project WHERE name=$1)`, name).Scan(&exists)
			if exists {
				JsonResp(w, M{"success": false, "error": "项目名称已存在"})
				return
			}
			var id string
			err := db.QueryRow(`INSERT INTO project (name, description, owner_id, source_type, status)
				VALUES ($1, $2, 'a0000000-0000-0000-0000-000000000001', $3, 'active') RETURNING id`,
				name, StrVal(body, "description"), StrVal(body, "sourceType")).Scan(&id)
			if err != nil {
				JsonResp(w, M{"success": false, "error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true, "data": M{"id": id, "name": name}})
			return
		}
		rows, err := db.Query(`SELECT id, name, description, owner_id, COALESCE(source_type,''), COALESCE(source_file,''),
			COALESCE(compatibility,0), status, created_at, updated_at FROM project ORDER BY created_at`)
		if err != nil {
			JsonResp(w, M{"data": []M{}, "total": 0, "error": err.Error()})
			return
		}
		defer rows.Close()
		var projects []M
		for rows.Next() {
			var id, name, desc, ownerID, srcType, srcFile, status string
			var compat int
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &name, &desc, &ownerID, &srcType, &srcFile, &compat, &status, &createdAt, &updatedAt)
			projects = append(projects, M{
				"id": id, "name": name, "description": desc, "ownerId": ownerID,
				"sourceType": srcType, "sourceFile": srcFile, "compatibility": compat,
				"status": status, "createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if projects == nil {
			projects = []M{}
		}
		ListResp(w, projects, len(projects))
	}
}

func handleProjectByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/projects")

		if r.Method == http.MethodDelete {
			// Clean up references that may not have ON DELETE CASCADE
			db.Exec(`DELETE FROM graph_node_position WHERE project_id = $1`, id)
			_, err := db.Exec(`DELETE FROM project WHERE id = $1`, id)
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
