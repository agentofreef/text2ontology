package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lakehouse2ontology/llmclient"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

func handleTokenAnnotations(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			token := StrVal(body, "token")
			objectName := StrVal(body, "objectName")
			propertyName := StrVal(body, "propertyName")
			metricName := StrVal(body, "metricName")
			note := StrVal(body, "note")

			// Normalize multi-object: sort so "o2,o1" == "o1,o2"
			parts := strings.Split(objectName, ",")
			var cleaned []string
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					cleaned = append(cleaned, p)
				}
			}
			sort.Strings(cleaned)
			objectName = strings.Join(cleaned, ",")

			if token == "" || !IsValidUUID(pid) {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "token and projectId required"})
				return
			}

			// Compute embedding for the token
			var vecStr *string
			if vecs, err := llmclient.EmbedTexts(db, []string{token}); err == nil && len(vecs) > 0 {
				parts := make([]string, len(vecs[0]))
				for i, v := range vecs[0] {
					parts[i] = fmt.Sprintf("%f", v)
				}
				s := "[" + strings.Join(parts, ",") + "]"
				vecStr = &s
			}

			var id string
			err := db.QueryRow(`INSERT INTO ont_token_annotation (project_id, token, object_name, property_name, metric_name, note, embedding)
				VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
				pid, token, objectName, propertyName, metricName, note, vecStr).Scan(&id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id})
			return
		}

		// GET: list annotations for project
		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}
		rows, err := db.Query(`SELECT id, token, object_name, COALESCE(property_name,''), metric_name, note, mark, created_at, updated_at
			FROM ont_token_annotation WHERE project_id = $1 ORDER BY created_at DESC`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()
		var list []M
		for rows.Next() {
			var id, token, objName, propertyName, metricName, note string
			var mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &token, &objName, &propertyName, &metricName, &note, &mark, &createdAt, &updatedAt)
			list = append(list, M{
				"id": id, "token": token, "objectName": objName, "propertyName": propertyName, "metricName": metricName,
				"note": note, "mark": mark,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleTokenAnnotationByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/ontology/token-annotations")
		// Cross-project IDOR guard: verify project access before touching this annotation.
		if !authmw.EnforceEntityProject(w, r, db, "ont_token_annotation", "id", id) {
			return
		}

		if strings.HasSuffix(r.URL.Path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_token_annotation SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_token_annotation SET object_name = $2, metric_name = $3, note = $4, updated_at = now() WHERE id = $1`,
				id, StrVal(body, "objectName"), StrVal(body, "metricName"), StrVal(body, "note"))
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_token_annotation WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
