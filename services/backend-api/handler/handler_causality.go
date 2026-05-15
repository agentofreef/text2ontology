package handler

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

func handleCausalities(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			var id string
			err := db.QueryRow(`INSERT INTO ont_causality
				(project_id, from_knowledge_id, to_knowledge_id,
				 relation_type, direction, description, sort_order, mark, note)
				VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8) RETURNING id`,
				pid,
				StrVal(body, "fromKnowledgeId"), StrVal(body, "toKnowledgeId"),
				StrVal(body, "relationType"), StrVal(body, "direction"),
				StrVal(body, "description"), numVal(body, "sortOrder"),
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

		knowledgeID := r.URL.Query().Get("knowledgeId")

		q := `SELECT c.id, c.project_id,
			c.from_knowledge_id, c.to_knowledge_id,
			c.relation_type, c.direction,
			COALESCE(c.description,''), c.sort_order, c.mark, COALESCE(c.note,''),
			c.created_at, c.updated_at,
			COALESCE(fk.title,'') AS from_title, COALESCE(tk.title,'') AS to_title
			FROM ont_causality c
			LEFT JOIN ont_knowledge fk ON c.from_knowledge_id = fk.id
			LEFT JOIN ont_knowledge tk ON c.to_knowledge_id = tk.id
			WHERE c.project_id = $1`
		args := []interface{}{pid}

		if IsValidUUID(knowledgeID) {
			q += ` AND (c.from_knowledge_id = $2 OR c.to_knowledge_id = $2)`
			args = append(args, knowledgeID)
		}
		q += ` ORDER BY c.sort_order, c.created_at`

		rows, err := db.Query(q, args...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, fromKID, toKID string
			var relType, direction, desc, note string
			var fromTitle, toTitle string
			var sortOrder int
			var mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &fromKID, &toKID,
				&relType, &direction, &desc, &sortOrder, &mark, &note,
				&createdAt, &updatedAt, &fromTitle, &toTitle)
			list = append(list, M{
				"id": id, "projectId": projectID,
				"fromKnowledgeId": fromKID, "toKnowledgeId": toKID,
				"fromKnowledgeTitle": fromTitle, "toKnowledgeTitle": toTitle,
				"relationType": relType, "direction": direction,
				"description": desc, "sortOrder": sortOrder,
				"mark": mark, "note": note,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleCausalityByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/causality")

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_causality SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE ont_causality SET
				from_knowledge_id = $2, to_knowledge_id = $3,
				relation_type = $4, direction = $5, description = $6,
				sort_order = $7, note = $8, updated_at = now() WHERE id = $1`,
				id, StrVal(body, "fromKnowledgeId"), StrVal(body, "toKnowledgeId"),
				StrVal(body, "relationType"), StrVal(body, "direction"),
				StrVal(body, "description"), numVal(body, "sortOrder"),
				StrVal(body, "note"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_causality WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
