package handler

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

func handleAliases(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			// `mark` is the "active/recallable" flag. Legacy callers (keyword
			// triage flow) leave it unset → false (draft). Od-detail page
			// passes `mark: true` so explicitly-curated Od aliases are
			// immediately visible to BuildLakehouseContext's fallbackDirectOd.
			markFlag := false
			if v, ok := body["mark"].(bool); ok {
				markFlag = v
			}
			var id string
			err := db.QueryRow(`INSERT INTO ont_alias (project_id, alias_text, alias_type,
				target_id, target_kind, canonical_value, is_exact_match, priority, synonyms, mark, note, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
				pid, StrVal(body, "aliasText"), StrVal(body, "aliasType"),
				NilIfEmpty(StrVal(body, "targetId")), StrVal(body, "targetKind"),
				StrVal(body, "canonicalValue"), BoolVal(body, "isExactMatch"),
				0, StringsToPgArray(body, "synonyms"),
				markFlag,
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
		rows, err := db.Query(`SELECT id, project_id, alias_text, alias_type,
			target_id, COALESCE(target_kind,''), COALESCE(canonical_value,''),
			is_exact_match, priority, synonyms, bridged_from, mark, COALESCE(note,''),
			created_at, updated_at
			FROM ont_alias WHERE project_id = $1 ORDER BY alias_type, alias_text`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, aliasText, aliasType, targetKind, canonical, note string
			var targetID, bridgedFrom sql.NullString
			var synonyms sql.NullString
			var isExact, mark bool
			var priority int
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &aliasText, &aliasType,
				&targetID, &targetKind, &canonical, &isExact, &priority, &synonyms,
				&bridgedFrom, &mark, &note, &createdAt, &updatedAt)
			list = append(list, M{
				"id": id, "projectId": projectID,
				"aliasText": aliasText, "aliasType": aliasType,
				"targetId": NullStr(targetID), "targetKind": targetKind,
				"canonicalValue": canonical, "isExactMatch": isExact,
				"priority": priority, "synonyms": PgArrayToStrings(synonyms),
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

func handleAliasByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/aliases")

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_alias SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE ont_alias SET alias_text = $2, alias_type = $3,
				target_id = $4, target_kind = $5, canonical_value = $6,
				is_exact_match = $7, synonyms = $8, note = $9, updated_at = now() WHERE id = $1`,
				id, StrVal(body, "aliasText"), StrVal(body, "aliasType"),
				NilIfEmpty(StrVal(body, "targetId")), StrVal(body, "targetKind"),
				StrVal(body, "canonicalValue"), BoolVal(body, "isExactMatch"),
				StringsToPgArray(body, "synonyms"), StrVal(body, "note"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_alias WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
