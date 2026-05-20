package handler

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

func handleMetrics(db *sql.DB) http.HandlerFunc {
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
			err := db.QueryRow(`INSERT INTO ont_metric (project_id, name, display_name,
				metric_type, aggregation, target_object_id, target_property, formula, depends_on,
				format_string, description, mark, note, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false, $12, $13) RETURNING id`,
				pid, StrVal(body, "name"), StrVal(body, "displayName"),
				StrVal(body, "metricType"), StrVal(body, "aggregation"),
				NilIfEmpty(StrVal(body, "targetObjectId")), StrVal(body, "targetProperty"),
				StrVal(body, "formula"), StringsToPgArray(body, "dependsOn"),
				StrVal(body, "formatString"),
				StrVal(body, "description"), StrVal(body, "note"),
				NilIfEmpty(StrVal(body, "createdBy"))).Scan(&id)
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
		rows, err := db.Query(`SELECT m.id, m.project_id, m.name, COALESCE(m.display_name,''),
			m.metric_type, COALESCE(m.aggregation,''), m.target_object_id, COALESCE(m.target_property,''),
			COALESCE(m.formula,''), m.depends_on, COALESCE(m.format_string,''),
			COALESCE(m.description,''), m.bridged_from, m.mark, COALESCE(m.note,''), m.created_at, m.updated_at,
			COALESCE(ot.name,'') AS target_object_name
			FROM ont_metric m
			LEFT JOIN ont_object_type ot ON m.target_object_id = ot.id
			WHERE m.project_id = $1 ORDER BY m.metric_type, m.name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, name, displayName, metricType, agg string
			var targetProp, formula, fmtStr, desc, note, targetObjName string
			var targetObjID, bridgedFrom sql.NullString
			var dependsOn sql.NullString
			var mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &name, &displayName, &metricType, &agg,
				&targetObjID, &targetProp, &formula, &dependsOn, &fmtStr, &desc,
				&bridgedFrom, &mark, &note, &createdAt, &updatedAt, &targetObjName)
			list = append(list, M{
				"id": id, "projectId": projectID,
				"name": name, "displayName": displayName, "metricType": metricType,
				"aggregation": agg, "targetObjectId": NullStr(targetObjID),
				"targetObjectName": targetObjName, "targetProperty": targetProp,
				"formula": formula, "dependsOn": PgArrayToStrings(dependsOn),
				"formatString": fmtStr,
				"description": desc, "bridgedFrom": NullStr(bridgedFrom),
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

func handleMetricByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/metrics")
		// Cross-project IDOR guard: verify project access before touching this metric.
		if !authmw.EnforceEntityProject(w, r, db, "ont_metric", "id", id) {
			return
		}

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_metric SET mark = $1,
				user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['mark']::text[]))),
				updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE ont_metric SET name = $2, display_name = $3, metric_type = $4,
				aggregation = $5, target_object_id = $6, target_property = $7, formula = $8,
				depends_on = $9, format_string = $10, description = $11,
				note = $12,
				user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['display_name','formula','description','format_string']::text[]))),
				updated_at = now() WHERE id = $1`,
				id, StrVal(body, "name"), StrVal(body, "displayName"), StrVal(body, "metricType"),
				StrVal(body, "aggregation"), NilIfEmpty(StrVal(body, "targetObjectId")),
				StrVal(body, "targetProperty"), StrVal(body, "formula"),
				StringsToPgArray(body, "dependsOn"),
				StrVal(body, "formatString"), StrVal(body, "description"), StrVal(body, "note"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_metric WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
