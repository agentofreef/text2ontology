package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lib/pq"

	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/ontology"
)

// handleMetricIntents handles /api/ontology/metric-intents:
//   - POST  create  (body: projectId, objectId, name, displayName,
//     canonicalMetric, canonicalFilters, autoGroupBy,
//     responseTemplate, description, priority)
//   - GET   list    (query: projectId)
func handleMetricIntents(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)

			// P7.4 dry-run validation: verify the Intent's canonical_metric +
			// auto_group_by + parameters schema produce a valid spec under
			// strict-mode dispatch BEFORE we INSERT. Catches author errors
			// (bare metric+groupBy, invalid param shape, bad orderBy dir,
			// invalid filter op) at save time so they never reach the LLM.
			if vr := validateIntentRemote(r.Context(), buildIntentValidateInput(db, body)); !vr.Ok {
				w.WriteHeader(400)
				JsonResp(w, M{
					"error":  fmt.Sprintf("Intent 校验失败 (%s)", vr.Code),
					"errors": vr.Errors,
					"code":   vr.Code,
				})
				return
			}

			objectID := StrVal(body, "objectId")
			if !IsValidUUID(objectID) {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "objectId is required"})
				return
			}

			triggers := stringSliceFromBody(body, "triggerKeywords")
			filtersJSON := jsonArrayBytes(body["canonicalFilters"])

			spec := ontology.IntentSpec{
				ProjectID:             pid,
				ObjectID:              objectID,
				Name:                  StrVal(body, "name"),
				DisplayName:           StrVal(body, "displayName"),
				CanonicalMetric:       StrVal(body, "canonicalMetric"),
				CanonicalFilters:      string(filtersJSON),
				AutoGroupBy:           stringSliceFromBody(body, "autoGroupBy"),
				PivotOn:               StrVal(body, "pivotOn"),
				PivotValues:           stringSliceFromBody(body, "pivotValues"),
				PivotColumnLabels:     stringSliceFromBody(body, "pivotColumnLabels"),
				PivotTotalLabel:       StrVal(body, "pivotTotalLabel"),
				PivotPercentAxis:      StrVal(body, "pivotPercentAxis"),
				PivotPercentScope:     StrVal(body, "pivotPercentScope"),
				PivotPercentSuffix:    StrVal(body, "pivotPercentSuffix"),
				PivotWithPercent:      optBoolPtr(body, "pivotWithPercent"),
				PivotAppendGrandTotal: optBoolPtr(body, "pivotAppendGrandTotal"),
				ResponseTemplate:      StrVal(body, "responseTemplate"),
				Description:           StrVal(body, "description"),
				Priority:              intValDefault(body, "priority", 0),
				Mark:                  optBoolPtr(body, "mark"),
			}

			tx, err := db.BeginTx(r.Context(), nil)
			if err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": "begin tx: " + err.Error()})
				return
			}
			defer tx.Rollback()

			intentID, err := ontology.WriteIntentWithTriggers(r.Context(), tx, spec, triggers)
			if err != nil {
				if errors.Is(err, ontology.ErrNoTriggers) {
					w.WriteHeader(400)
					JsonResp(w, M{"error": err.Error(), "code": "NO_TRIGGERS"})
					return
				}
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			if err := tx.Commit(); err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": "commit: " + err.Error()})
				return
			}
			JsonResp(w, M{"id": intentID})
			return
		}

		// GET list
		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}
		rows, err := db.Query(metricIntentListSQL+` WHERE mi.project_id = $1
			ORDER BY mi.priority DESC, mi.name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			item, scanErr := scanMetricIntent(rows)
			if scanErr != nil {
				continue
			}
			list = append(list, item)
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

// handleMetricIntentByID: GET / PUT / DELETE on /api/ontology/metric-intents/{id}
func handleMetricIntentByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/ontology/metric-intents")
		if !IsValidUUID(id) {
			http.NotFound(w, r)
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)

			// P7.4 dry-run validation (same gate as POST). Re-runs on UPDATE
			// because the user may have changed canonical_metric / parameters
			// schema and we don't want a previously-valid Intent to become
			// invalid in DB without warning.
			if vr := validateIntentRemote(r.Context(), buildIntentValidateInput(db, body)); !vr.Ok {
				w.WriteHeader(400)
				JsonResp(w, M{
					"error":  fmt.Sprintf("Intent 校验失败 (%s)", vr.Code),
					"errors": vr.Errors,
					"code":   vr.Code,
				})
				return
			}

			filtersJSON := jsonArrayBytes(body["canonicalFilters"])
			pivotPercentAxis := StrVal(body, "pivotPercentAxis")
			if pivotPercentAxis != "row" && pivotPercentAxis != "column" {
				pivotPercentAxis = "row"
			}
			pivotPercentScope := StrVal(body, "pivotPercentScope")
			if pivotPercentScope != "global" {
				pivotPercentScope = "filtered"
			}
			percentSuffix := StrVal(body, "pivotPercentSuffix")
			if percentSuffix == "" {
				percentSuffix = "占比"
			}
			// triggerKeywords semantics on PUT:
			//   - field absent  → leave existing trigger bindings untouched
			//   - field present → replace; empty list rejected with NO_TRIGGERS so
			//     callers can't accidentally orphan an intent via an "edit" action.
			_, hasTriggers := body["triggerKeywords"]
			triggers := stringSliceFromBody(body, "triggerKeywords")

			tx, err := db.BeginTx(r.Context(), nil)
			if err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": "begin tx: " + err.Error()})
				return
			}
			defer tx.Rollback()

			if _, err := tx.ExecContext(r.Context(), `UPDATE lakehouse_metric_intent SET
				name=$2, display_name=$3, object_id=$4,
				canonical_metric=$5, canonical_filters=$6::jsonb,
				auto_group_by=$7,
				pivot_on=$8, pivot_values=$9, pivot_column_labels=$10,
				pivot_total_label=COALESCE(NULLIF($11,''),'Total'),
				pivot_percent_axis=$12, pivot_percent_scope=$13,
				pivot_percent_suffix=$14,
				pivot_with_percent=COALESCE($15, pivot_with_percent),
				pivot_append_grand_total=COALESCE($16, pivot_append_grand_total),
				response_template=$17, description=$18,
				priority=$19, mark=COALESCE($20, mark), updated_at=now()
				WHERE id=$1`,
				id, StrVal(body, "name"), StrVal(body, "displayName"),
				NilIfEmpty(StrVal(body, "objectId")), StrVal(body, "canonicalMetric"),
				string(filtersJSON), StringsToPgArray(body, "autoGroupBy"),
				NilIfEmpty(StrVal(body, "pivotOn")),
				StringsToPgArray(body, "pivotValues"),
				StringsToPgArray(body, "pivotColumnLabels"),
				StrVal(body, "pivotTotalLabel"),
				pivotPercentAxis,
				pivotPercentScope,
				percentSuffix,
				optBoolPtr(body, "pivotWithPercent"),
				optBoolPtr(body, "pivotAppendGrandTotal"),
				StrVal(body, "responseTemplate"), StrVal(body, "description"),
				intValDefault(body, "priority", 0), optBoolPtr(body, "mark"),
			); err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}

			if hasTriggers {
				// Resolve the current project_id + object_id post-update so the
				// new keyword rows land under the correct anchor (UPDATE may
				// have moved the intent to a different OD).
				var objID, projID string
				if err := tx.QueryRowContext(r.Context(),
					`SELECT object_id, project_id FROM lakehouse_metric_intent WHERE id=$1`, id,
				).Scan(&objID, &projID); err != nil {
					w.WriteHeader(500)
					JsonResp(w, M{"error": "fetch intent anchors: " + err.Error()})
					return
				}
				if err := ontology.UpdateIntentTriggers(r.Context(), tx, projID, id, objID, triggers); err != nil {
					if errors.Is(err, ontology.ErrNoTriggers) {
						w.WriteHeader(400)
						JsonResp(w, M{"error": err.Error(), "code": "NO_TRIGGERS"})
						return
					}
					w.WriteHeader(400)
					JsonResp(w, M{"error": err.Error()})
					return
				}
			}

			if err := tx.Commit(); err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": "commit: " + err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM lakehouse_metric_intent WHERE id=$1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		// GET single
		row := db.QueryRow(metricIntentListSQL+` WHERE mi.id=$1`, id)
		item, err := scanMetricIntent(row)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		JsonResp(w, item)
	}
}

// metricIntentListSQL is the shared SELECT for list and getByID.
const metricIntentListSQL = `
	SELECT mi.id, mi.project_id, mi.object_id,
	       mi.name, COALESCE(mi.display_name,''),
	       COALESCE(mi.canonical_metric,''),
	       COALESCE(mi.canonical_filters::text,'[]'),
	       COALESCE(mi.auto_group_by, '{}'::text[]),
	       COALESCE(mi.pivot_on,''),
	       COALESCE(mi.pivot_values, '{}'::text[]),
	       COALESCE(mi.pivot_column_labels, '{}'::text[]),
	       COALESCE(mi.pivot_total_label,'Total'),
	       COALESCE(mi.pivot_percent_axis,'row'),
	       COALESCE(mi.pivot_percent_scope,'filtered'),
	       COALESCE(mi.pivot_percent_suffix,'占比'),
	       COALESCE(mi.pivot_with_percent, false),
	       COALESCE(mi.pivot_append_grand_total, false),
	       COALESCE(mi.response_template,''), COALESCE(mi.description,''),
	       COALESCE(mi.priority, 0), mi.mark, mi.created_at, mi.updated_at,
	       COALESCE(o.name,'') AS object_name
	FROM lakehouse_metric_intent mi
	LEFT JOIN ont_object_type o ON mi.object_id = o.id`

// scanMetricIntent scans a row (matching metricIntentListSQL) into a response map.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanMetricIntent(row rowScanner) (M, error) {
	var id, projectID, objectID, name, displayName string
	var metric, filtersJSON, pivotOn, pivotTotalLabel, pivotPercentAxis, pivotPercentScope, pivotPercentSuffix, respTpl, desc, objName string
	var autoGB, pivotVals, pivotLabels pq.StringArray
	var prio int
	var mark, pivotWithPercent, pivotAppendGrandTotal bool
	var ca, ua time.Time
	if err := row.Scan(&id, &projectID, &objectID,
		&name, &displayName, &metric, &filtersJSON, &autoGB,
		&pivotOn, &pivotVals, &pivotLabels,
		&pivotTotalLabel, &pivotPercentAxis, &pivotPercentScope, &pivotPercentSuffix,
		&pivotWithPercent, &pivotAppendGrandTotal,
		&respTpl, &desc, &prio, &mark, &ca, &ua, &objName); err != nil {
		return nil, err
	}
	var filters interface{}
	if filtersJSON != "" {
		_ = json.Unmarshal([]byte(filtersJSON), &filters)
	}
	if filters == nil {
		filters = []interface{}{}
	}
	autoGroupBy := []string(autoGB)
	if autoGroupBy == nil {
		autoGroupBy = []string{}
	}
	pivotValues := []string(pivotVals)
	if pivotValues == nil {
		pivotValues = []string{}
	}
	pivotColumnLabels := []string(pivotLabels)
	if pivotColumnLabels == nil {
		pivotColumnLabels = []string{}
	}
	return M{
		"id": id, "projectId": projectID,
		"objectId": objectID, "objectName": objName,
		"name": name, "displayName": displayName,
		"canonicalMetric": metric, "canonicalFilters": filters,
		"autoGroupBy":           autoGroupBy,
		"pivotOn":               pivotOn,
		"pivotValues":           pivotValues,
		"pivotColumnLabels":     pivotColumnLabels,
		"pivotTotalLabel":       pivotTotalLabel,
		"pivotPercentAxis":      pivotPercentAxis,
		"pivotPercentScope":     pivotPercentScope,
		"pivotPercentSuffix":    pivotPercentSuffix,
		"pivotWithPercent":      pivotWithPercent,
		"pivotAppendGrandTotal": pivotAppendGrandTotal,
		"responseTemplate":      respTpl, "description": desc,
		"priority": prio, "mark": mark,
		"createdAt": ca.Format(time.RFC3339),
		"updatedAt": ua.Format(time.RFC3339),
	}, nil
}

// jsonArrayBytes normalises a JSON-ish value from ReadBody into a JSON array byte
// slice. Accepts []interface{} directly, or nil → "[]".
func jsonArrayBytes(v interface{}) []byte {
	if v == nil {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return []byte("[]")
	}
	return b
}

// optBoolPtr returns *bool for a field if present, else nil (so SQL uses COALESCE default).
func optBoolPtr(body M, key string) *bool {
	v, ok := body[key]
	if !ok || v == nil {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

// intValDefault reads an int from a body field, defaulting if absent or unparseable.
// JSON numbers decode to float64; YAML-ish conversions also work.
func intValDefault(body M, key string, def int) int {
	v, ok := body[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}

// stringSliceFromBody coerces body[key] (a JSON array decoded as []interface{})
// into []string, dropping nil / non-string entries. Returns nil on miss so the
// caller can detect "field absent" vs "field empty".
func stringSliceFromBody(body M, key string) []string {
	v, ok := body[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
