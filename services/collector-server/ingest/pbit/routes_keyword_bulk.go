package pbit

// Bulk operations for lakehouse_keyword. Same shape + UX contract as the
// objects bulk endpoints (services/backend-api/handler/handler_object_bulk.go):
// every body has `ids` or `items`, every response has counts + per-row errors.
//
// All endpoints are scoped by project_id (taken from query string ?projectId=).
// Forged ids from another project produce a 403 (impact) or are silently
// no-op'd by the SQL WHERE project_id=$1 (delete/update/reanchor).

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// projectIDFromQuery returns ?projectId=... when valid, empty otherwise.
func projectIDFromQuery(r *http.Request) string {
	pid := r.URL.Query().Get("projectId")
	if _, err := uuid.Parse(pid); err != nil {
		return ""
	}
	return pid
}

// validUUIDs filters out non-string / non-UUID entries and returns the
// remainder. Returns nil if zero valid UUIDs were found.
func validUUIDs(arr []interface{}) []string {
	if len(arr) == 0 {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if _, err := uuid.Parse(s); err != nil {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validUUIDOrEmpty returns the input UUID if valid, "" otherwise.
func validUUIDOrEmpty(v interface{}) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	if _, err := uuid.Parse(s); err != nil {
		return ""
	}
	return s
}

// ─── POST /api/connector/pbit/lakehouse-keywords/bulk-impact ────────────────
//
// Body: {"ids": ["uuid1", ...]}
// Returns: {"keywords": N, "aliasVectors": N}
//
// Counts how many keyword rows match (project-scoped) plus how many
// alias-vector child rows would be cascade-deleted. Used by the UI delete
// modal so users see scope before confirming.
func handleLakehouseKeywordsBulkImpact(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pid := projectIDFromQuery(r)
		if pid == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}
		var req struct {
			IDs []interface{} `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid body"})
			return
		}
		ids := validUUIDs(req.IDs)
		if len(ids) == 0 {
			jsonResp(w, 400, map[string]string{"error": "ids required"})
			return
		}
		idArr := pq.Array(ids)

		var matched, aliasVectors int
		db.QueryRow(`SELECT count(*) FROM lakehouse_keyword WHERE project_id=$1 AND id = ANY($2)`, pid, idArr).Scan(&matched)
		if matched != len(ids) {
			jsonResp(w, 403, map[string]string{"error": "one or more ids do not belong to current project"})
			return
		}
		db.QueryRow(`SELECT count(*) FROM lakehouse_keyword_alias_vector WHERE keyword_id = ANY($1)`, idArr).Scan(&aliasVectors)

		jsonResp(w, 200, map[string]interface{}{
			"keywords":     len(ids),
			"aliasVectors": aliasVectors,
		})
	}
}

// ─── POST /api/connector/pbit/lakehouse-keywords/bulk-delete ────────────────
//
// Body: {"ids": ["uuid1", ...]}
// Returns: {"deleted": N}
//
// Schema CASCADE handles lakehouse_keyword_alias_vector. recall-server
// reads keywords on each request (no in-memory cache to invalidate).
func handleLakehouseKeywordsBulkDelete(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pid := projectIDFromQuery(r)
		if pid == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}
		var req struct {
			IDs []interface{} `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid body"})
			return
		}
		ids := validUUIDs(req.IDs)
		if len(ids) == 0 {
			jsonResp(w, 400, map[string]string{"error": "ids required"})
			return
		}
		res, err := db.Exec(`DELETE FROM lakehouse_keyword WHERE project_id=$1 AND id = ANY($2)`, pid, pq.Array(ids))
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		deleted, _ := res.RowsAffected()
		jsonResp(w, 200, map[string]interface{}{"deleted": deleted})
	}
}

// ─── POST /api/connector/pbit/lakehouse-keywords/bulk-update ────────────────
//
// Body: {"ids": [...], "isColumnName"?: bool, "isStopword"?: bool}
// Returns: {"updated": N}
//
// At least one of isColumnName / isStopword must be set. The two are
// independent flags — set both, set one, or set the other.
func handleLakehouseKeywordsBulkUpdate(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pid := projectIDFromQuery(r)
		if pid == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}
		var req struct {
			IDs          []interface{} `json:"ids"`
			IsColumnName *bool         `json:"isColumnName"`
			IsStopword   *bool         `json:"isStopword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid body"})
			return
		}
		ids := validUUIDs(req.IDs)
		if len(ids) == 0 {
			jsonResp(w, 400, map[string]string{"error": "ids required"})
			return
		}
		var sets []string
		args := []interface{}{pid, pq.Array(ids)}
		argIdx := 3
		if req.IsColumnName != nil {
			sets = append(sets, "is_column_name = $"+itoa(argIdx))
			args = append(args, *req.IsColumnName)
			argIdx++
		}
		if req.IsStopword != nil {
			sets = append(sets, "is_stopword = $"+itoa(argIdx))
			args = append(args, *req.IsStopword)
			argIdx++
		}
		if len(sets) == 0 {
			jsonResp(w, 400, map[string]string{"error": "at least one of isColumnName|isStopword must be set"})
			return
		}
		q := "UPDATE lakehouse_keyword SET " + strings.Join(sets, ", ") + " WHERE project_id = $1 AND id = ANY($2)"
		res, err := db.Exec(q, args...)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		updated, _ := res.RowsAffected()
		jsonResp(w, 200, map[string]interface{}{"updated": updated})
	}
}

// ─── POST /api/connector/pbit/lakehouse-keywords/bulk-reanchor ──────────────
//
// Body: {"ids": [...], "propertyId"?: uuid|null, "metricIntentId"?: uuid|null, "objectId"?: uuid|null}
// Returns: {"reanchored": N}
//
// Move the selected keywords to a new anchor. Pass `null` (JSON null) to
// CLEAR an anchor field. Skipping a field leaves the existing value alone.
// At least one anchor field must be set, and the resulting row must satisfy
// the schema CHECK (is_stopword OR property_id OR object_id OR metric_intent_id).
func handleLakehouseKeywordsBulkReanchor(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pid := projectIDFromQuery(r)
		if pid == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid body"})
			return
		}
		idsRaw, _ := req["ids"].([]interface{})
		ids := validUUIDs(idsRaw)
		if len(ids) == 0 {
			jsonResp(w, 400, map[string]string{"error": "ids required"})
			return
		}

		var sets []string
		args := []interface{}{pid, pq.Array(ids)}
		argIdx := 3
		// Each of the three anchor fields is independently settable: `null`
		// clears it, an absent key leaves it alone, a UUID string sets it.
		for _, spec := range []struct {
			column string
			key    string
		}{
			{"property_id", "propertyId"},
			{"metric_intent_id", "metricIntentId"},
			{"object_id", "objectId"},
		} {
			v, present := req[spec.key]
			if !present {
				continue
			}
			if v == nil {
				sets = append(sets, spec.column+" = NULL")
				continue
			}
			s := validUUIDOrEmpty(v)
			if s == "" {
				jsonResp(w, 400, map[string]string{"error": spec.key + " must be uuid or null"})
				return
			}
			sets = append(sets, spec.column+" = $"+itoa(argIdx))
			args = append(args, s)
			argIdx++

			// Validate that the target FK exists in the same project.
			var ok int
			var checkSQL string
			switch spec.column {
			case "property_id":
				checkSQL = `SELECT count(*) FROM ont_property WHERE project_id=$1 AND id=$2`
			case "metric_intent_id":
				checkSQL = `SELECT count(*) FROM lakehouse_metric_intent WHERE project_id=$1 AND id=$2`
			case "object_id":
				checkSQL = `SELECT count(*) FROM ont_object_type WHERE project_id=$1 AND id=$2`
			}
			db.QueryRow(checkSQL, pid, s).Scan(&ok)
			if ok != 1 {
				jsonResp(w, 400, map[string]string{"error": spec.key + " not found in current project"})
				return
			}
		}

		if len(sets) == 0 {
			jsonResp(w, 400, map[string]string{"error": "at least one of propertyId|metricIntentId|objectId must be set"})
			return
		}

		q := "UPDATE lakehouse_keyword SET " + strings.Join(sets, ", ") + " WHERE project_id = $1 AND id = ANY($2)"
		res, err := db.Exec(q, args...)
		if err != nil {
			// CHECK constraint violation surfaces as `_check` in error string.
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		reanchored, _ := res.RowsAffected()
		jsonResp(w, 200, map[string]interface{}{"reanchored": reanchored})
	}
}

// ─── POST /api/connector/pbit/lakehouse-keywords/bulk-create ────────────────
//
// Body: {"items": [{"keyword", "propertyId"?, "metricIntentId"?, "objectId"?, "isColumnName"?, "aliases"?}]}
// Returns: {"created": N, "ids": [...], "errors": [{index, keyword?, error}], "total": N}
//
// At least one of propertyId / metricIntentId / objectId must be set per row,
// or the row is rejected with the schema CHECK violation. Aliases are written
// to the keyword.aliases column; lakehouse_keyword_alias_vector child rows
// are NOT auto-created here — vectors get computed via the existing
// /compute-vectors SSE endpoint.
func handleLakehouseKeywordsBulkCreate(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pid := projectIDFromQuery(r)
		if pid == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}
		var req struct {
			Items []map[string]interface{} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid body"})
			return
		}
		if len(req.Items) == 0 {
			jsonResp(w, 400, map[string]string{"error": "items required"})
			return
		}

		ids := make([]string, 0, len(req.Items))
		errs := make([]map[string]interface{}, 0)

		for i, it := range req.Items {
			keyword, _ := it["keyword"].(string)
			keyword = strings.TrimSpace(keyword)
			if keyword == "" {
				errs = append(errs, map[string]interface{}{"index": i, "error": "keyword required"})
				continue
			}
			propertyID := validUUIDOrEmpty(it["propertyId"])
			metricIntentID := validUUIDOrEmpty(it["metricIntentId"])
			objectID := validUUIDOrEmpty(it["objectId"])

			// We need the object_type_id (the schema's anchor for object_type) — derive
			// it from propertyID if available, else use objectID directly. If neither
			// is set but metricIntentId is, look up the intent's object_id.
			objectTypeID := objectID
			if propertyID != "" {
				db.QueryRow(`SELECT object_type_id FROM ont_property WHERE project_id=$1 AND id=$2`, pid, propertyID).Scan(&objectTypeID)
				if objectTypeID == "" {
					errs = append(errs, map[string]interface{}{"index": i, "keyword": keyword, "error": "propertyId not found"})
					continue
				}
			}
			if objectTypeID == "" && metricIntentID != "" {
				db.QueryRow(`SELECT object_id FROM lakehouse_metric_intent WHERE project_id=$1 AND id=$2`, pid, metricIntentID).Scan(&objectTypeID)
			}
			if objectTypeID == "" {
				errs = append(errs, map[string]interface{}{"index": i, "keyword": keyword, "error": "must set propertyId, objectId, or metricIntentId (with intent's object_id)"})
				continue
			}

			isColumnName := false
			if v, ok := it["isColumnName"].(bool); ok {
				isColumnName = v
			}

			var aliases []string
			if a, ok := it["aliases"].([]interface{}); ok {
				for _, x := range a {
					if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
						aliases = append(aliases, strings.TrimSpace(s))
					}
				}
			}
			if aliases == nil {
				aliases = []string{}
			}

			var newID string
			err := db.QueryRow(`INSERT INTO lakehouse_keyword
				(project_id, object_type_id, property_id, metric_intent_id, object_id,
				 keyword, is_column_name, aliases, synced_at)
				VALUES ($1, $2, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid, NULLIF($5,'')::uuid,
				        $6, $7, $8, now())
				RETURNING id`,
				pid, objectTypeID, propertyID, metricIntentID, objectID,
				keyword, isColumnName, pq.Array(aliases)).Scan(&newID)
			if err != nil {
				errs = append(errs, map[string]interface{}{"index": i, "keyword": keyword, "error": err.Error()})
				continue
			}
			ids = append(ids, newID)
		}

		jsonResp(w, 200, map[string]interface{}{
			"created": len(ids),
			"ids":     ids,
			"errors":  errs,
			"total":   len(req.Items),
		})
	}
}

// itoa — small int formatter for SQL placeholder construction (avoid strconv import).
func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
