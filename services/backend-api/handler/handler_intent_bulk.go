package handler

// Bulk operations for lakehouse_metric_intent. Mirrors the shape of
// handler_object_bulk.go: every body has `ids` or `items`, every response
// reports counts + per-row errors. All endpoints are project-scoped via
// the standard auth middleware (GetProjectID) so forged ids from another
// project produce 403 (impact) or are silently no-op'd by WHERE project_id.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/ontology"
	"github.com/lib/pq"
)

// HandleMetricIntentsBulkImpact — POST /api/ontology/metric-intents/bulk-impact
//
// Body: {"ids": ["uuid1", ...]}
// Returns: {"intents": N, "keywords": N}
//
// Reports the count of cascading rows (lakehouse_keyword.metric_intent_id
// CASCADE). Used by the UI delete-confirmation modal to surface scope
// before the user types DELETE.
func HandleMetricIntentsBulkImpact(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		body := ReadBody(r)
		ids := uuidsFromBody(body["ids"])
		if len(ids) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "ids required"})
			return
		}
		pid := GetProjectID(r)
		idArr := pq.Array(ids)

		var matched, keywords int
		db.QueryRow(`SELECT count(*) FROM lakehouse_metric_intent WHERE project_id=$1 AND id = ANY($2)`, pid, idArr).Scan(&matched)
		if matched != len(ids) {
			w.WriteHeader(403)
			JsonResp(w, M{"error": "one or more ids do not belong to current project"})
			return
		}
		db.QueryRow(`SELECT count(*) FROM lakehouse_keyword WHERE metric_intent_id = ANY($1)`, idArr).Scan(&keywords)

		JsonResp(w, M{
			"intents":  len(ids),
			"keywords": keywords,
		})
	}
}

// HandleMetricIntentsBulkDelete — POST /api/ontology/metric-intents/bulk-delete
//
// Body: {"ids": [...]}
// Returns: {"deleted": N}
//
// Schema CASCADE drops lakehouse_keyword rows that reference the deleted
// intents. recall-server reads intents per request, no in-memory cache.
func HandleMetricIntentsBulkDelete(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		body := ReadBody(r)
		ids := uuidsFromBody(body["ids"])
		if len(ids) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "ids required"})
			return
		}
		pid := GetProjectID(r)
		res, err := db.Exec(`DELETE FROM lakehouse_metric_intent WHERE project_id=$1 AND id = ANY($2)`, pid, pq.Array(ids))
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		deleted, _ := res.RowsAffected()
		JsonResp(w, M{"deleted": deleted})
	}
}

// HandleMetricIntentsBulkUpdate — POST /api/ontology/metric-intents/bulk-update
//
// Apply a uniform partial update to every intent in `ids`.
//
// Body shape:
//
//	{
//	  "ids":      ["uuid1", "uuid2"],
//	  "mark":     true,         // optional bool
//	  "priority": 10,           // optional int (any int — clients usually use 0..100)
//	  "objectId": "<uuid>"      // optional — re-anchor to a different Od (must be in same project)
//	}
//
// Returns: {"updated": N}
//
// Bulk-edit on canonical_metric / canonical_filters / pivot_* is intentionally
// NOT supported — those fields are intent-specific and best edited one-by-one.
func HandleMetricIntentsBulkUpdate(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		body := ReadBody(r)
		ids := uuidsFromBody(body["ids"])
		if len(ids) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "ids required"})
			return
		}
		pid := GetProjectID(r)

		var sets []string
		args := []interface{}{pid, pq.Array(ids)}
		argIdx := 3

		if v, ok := body["mark"].(bool); ok {
			sets = append(sets, "mark = $"+intToStr(argIdx))
			args = append(args, v)
			argIdx++
		}
		// priority is sent as JSON number → float64 in Go
		if v, ok := body["priority"].(float64); ok {
			sets = append(sets, "priority = $"+intToStr(argIdx))
			args = append(args, int(v))
			argIdx++
		}
		if v, ok := body["objectId"].(string); ok && v != "" {
			if !IsValidUUID(v) {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "objectId must be a valid uuid"})
				return
			}
			// FK target must exist in the same project.
			var ok int
			db.QueryRow(`SELECT count(*) FROM ont_object_type WHERE project_id=$1 AND id=$2`, pid, v).Scan(&ok)
			if ok != 1 {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "objectId not found in current project"})
				return
			}
			sets = append(sets, "object_id = $"+intToStr(argIdx))
			args = append(args, v)
			argIdx++
		}

		if len(sets) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "at least one of mark|priority|objectId must be set"})
			return
		}
		sets = append(sets, "updated_at = now()")

		q := "UPDATE lakehouse_metric_intent SET " + strings.Join(sets, ", ") + " WHERE project_id = $1 AND id = ANY($2)"
		res, err := db.Exec(q, args...)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		updated, _ := res.RowsAffected()
		JsonResp(w, M{"updated": updated})
	}
}

// HandleMetricIntentsBulkCreate — POST /api/ontology/metric-intents/bulk-create
//
// Body: {"items": [{"name", "objectId", "canonicalMetric", "displayName"?,
//                    "priority"?, "description"?, "mark"?}]}
//
// Returns: {"created": N, "ids": [...], "errors": [{index, name?, error}], "total": N}
//
// Minimum viable bulk-create — only the trivial scalar fields. Complex
// configuration (canonical_filters, pivot_*, auto_group_by) defaults to
// the schema defaults; users who need them set up the intent normally
// then bulk-edit later.
func HandleMetricIntentsBulkCreate(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		body := ReadBody(r)
		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}
		rawItems, ok := body["items"].([]interface{})
		if !ok || len(rawItems) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "items required"})
			return
		}

		ids := make([]string, 0, len(rawItems))
		errs := make([]M, 0)

		for i, raw := range rawItems {
			it, ok := raw.(map[string]interface{})
			if !ok {
				errs = append(errs, M{"index": i, "error": "not an object"})
				continue
			}
			name := strings.TrimSpace(StrVal(it, "name"))
			if name == "" {
				errs = append(errs, M{"index": i, "error": "name required"})
				continue
			}
			objectID := strings.TrimSpace(StrVal(it, "objectId"))
			if !IsValidUUID(objectID) {
				errs = append(errs, M{"index": i, "name": name, "error": "objectId required (uuid)"})
				continue
			}
			canonicalMetric := strings.TrimSpace(StrVal(it, "canonicalMetric"))
			if canonicalMetric == "" {
				errs = append(errs, M{"index": i, "name": name, "error": "canonicalMetric required"})
				continue
			}

			// Verify objectId belongs to current project.
			var matched int
			db.QueryRow(`SELECT count(*) FROM ont_object_type WHERE project_id=$1 AND id=$2`, pid, objectID).Scan(&matched)
			if matched != 1 {
				errs = append(errs, M{"index": i, "name": name, "error": "objectId not found in current project"})
				continue
			}

			displayName := StrVal(it, "displayName")
			description := StrVal(it, "description")
			priority := 0
			if v, ok := it["priority"].(float64); ok {
				priority = int(v)
			}
			mark := true
			if v, ok := it["mark"].(bool); ok {
				mark = v
			}
			markPtr := mark

			triggers := bulkStringSlice(it, "triggerKeywords")

			spec := ontology.IntentSpec{
				ProjectID:       pid,
				ObjectID:        objectID,
				Name:            name,
				DisplayName:     displayName,
				CanonicalMetric: canonicalMetric,
				Description:     description,
				Priority:        priority,
				Mark:            &markPtr,
				AutoGroupBy:     bulkStringSlice(it, "autoGroupBy"),
			}

			newID, err := writeIntentInTx(r.Context(), db, spec, triggers)
			if err != nil {
				if errors.Is(err, ontology.ErrNoTriggers) {
					errs = append(errs, M{"index": i, "name": name, "error": err.Error(), "code": "NO_TRIGGERS"})
				} else {
					errs = append(errs, M{"index": i, "name": name, "error": err.Error()})
				}
				continue
			}
			ids = append(ids, newID)
		}

		JsonResp(w, M{
			"created": len(ids),
			"ids":     ids,
			"errors":  errs,
			"total":   len(rawItems),
		})
	}
}

// writeIntentInTx wraps the shared ontology.WriteIntentWithTriggers helper
// in a small per-item transaction so a single bad row in a bulk request
// does not poison the whole batch — each item commits or rolls back
// independently.
func writeIntentInTx(ctx context.Context, db *sql.DB, spec ontology.IntentSpec, triggers []string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := ontology.WriteIntentWithTriggers(ctx, tx, spec, triggers)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// bulkStringSlice — local helper, mirrors stringSliceFromBody in
// handler_intent.go. Kept private to this file to avoid coupling the bulk
// path to the single-create path's util surface.
func bulkStringSlice(m map[string]interface{}, key string) []string {
	v, ok := m[key]
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

// intToStr — minimal int→string for SQL placeholder building. Avoids the
// strconv import and is namespaced separately from itoa in util.go.
func intToStr(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	// Two-digit shortcut — placeholders never exceed two digits in practice
	// (max 99 SET clauses), but fall through to strconv-equivalent below.
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
