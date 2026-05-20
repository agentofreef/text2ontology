package handler

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

func handleLinks(db *sql.DB) http.HandlerFunc {
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
			err := db.QueryRow(`INSERT INTO ont_link_type (project_id, from_object_id, to_object_id,
				link_name, fk_column, cardinality, reject_reason, description, mark, note, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, $9, $10) RETURNING id`,
				pid, StrVal(body, "fromObjectId"), StrVal(body, "toObjectId"),
				StrVal(body, "linkName"), StrVal(body, "fkColumn"), StrVal(body, "cardinality"),
				StrVal(body, "rejectReason"), StrVal(body, "description"),
				StrVal(body, "note"), NilIfEmpty(StrVal(body, "createdBy"))).Scan(&id)
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
		rows, err := db.Query(`SELECT lt.id, lt.project_id, lt.from_object_id, lt.to_object_id,
			COALESCE(lt.link_name,''), COALESCE(lt.fk_column,''), lt.cardinality,
			COALESCE(lt.reject_reason,''), COALESCE(lt.description,''), lt.bridged_from,
			lt.mark, COALESCE(lt.note,''), lt.created_at, lt.updated_at,
			COALESCE(fo.name,'') AS from_name, COALESCE(to2.name,'') AS to_name
			FROM ont_link_type lt
			LEFT JOIN ont_object_type fo ON lt.from_object_id = fo.id
			LEFT JOIN ont_object_type to2 ON lt.to_object_id = to2.id
			WHERE lt.project_id = $1
			ORDER BY fo.name, to2.name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, fromObjID, toObjID, linkName, fkCol, card, reject, desc, note string
			var fromName, toName string
			var bridgedFrom sql.NullString
			var mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &fromObjID, &toObjID, &linkName, &fkCol, &card,
				&reject, &desc, &bridgedFrom, &mark, &note, &createdAt, &updatedAt, &fromName, &toName)
			list = append(list, M{
				"id": id, "projectId": projectID,
				"fromObjectId": fromObjID, "toObjectId": toObjID,
				"fromObjectName": fromName, "toObjectName": toName,
				"linkName": linkName, "fkColumn": fkCol, "cardinality": card,
				"rejectReason": reject, "description": desc,
				"bridgedFrom": NullStr(bridgedFrom), "mark": mark, "note": note,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleLinkByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/links")
		// Cross-project IDOR guard: verify project access before touching this link type.
		if !authmw.EnforceEntityProject(w, r, db, "ont_link_type", "id", id) {
			return
		}

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_link_type SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE ont_link_type SET from_object_id = $2, to_object_id = $3,
				link_name = $4, fk_column = $5, cardinality = $6, reject_reason = $7,
				description = $8, note = $9, updated_at = now() WHERE id = $1`,
				id, StrVal(body, "fromObjectId"), StrVal(body, "toObjectId"),
				StrVal(body, "linkName"), StrVal(body, "fkColumn"), StrVal(body, "cardinality"),
				StrVal(body, "rejectReason"), StrVal(body, "description"), StrVal(body, "note"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_link_type WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
