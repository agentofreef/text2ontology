package handler

import (
	"database/sql"
	"net/http"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
	"github.com/lib/pq"
)

// uuidsFromBody pulls a string-array of UUIDs from a JSON body field, dropping
// non-strings and non-UUIDs silently. Returns nil if zero valid UUIDs.
func uuidsFromBody(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, ok := x.(string)
		if !ok || !IsValidUUID(s) {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HandleObjectsBulkImpact — POST /api/ontology/objects/bulk-impact
//
// Pre-flight cascade summary. Given a list of Od ids, return how many
// child rows would be deleted (FK CASCADE in schema) plus how many
// orphaned rows would survive (no FK constraint). Used by the UI's
// delete-confirmation modal so users see exactly what they're nuking.
func HandleObjectsBulkImpact(db *sql.DB) http.HandlerFunc {
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

		// Verify all ids belong to the current project (defense-in-depth
		// against forged ids from other projects).
		var matched int
		db.QueryRow(`SELECT count(*) FROM ont_object_type WHERE project_id = $1 AND id = ANY($2)`, pid, idArr).Scan(&matched)
		if matched != len(ids) {
			w.WriteHeader(403)
			JsonResp(w, M{"error": "one or more ids do not belong to current project"})
			return
		}

		var props, links, keywords, intents, knowledge, aliases int
		db.QueryRow(`SELECT count(*) FROM ont_property WHERE object_type_id = ANY($1)`, idArr).Scan(&props)
		db.QueryRow(`SELECT count(*) FROM ont_link_type WHERE from_object_id = ANY($1) OR to_object_id = ANY($1)`, idArr).Scan(&links)
		db.QueryRow(`SELECT count(*) FROM lakehouse_keyword WHERE object_id = ANY($1)`, idArr).Scan(&keywords)
		db.QueryRow(`SELECT count(*) FROM lakehouse_metric_intent WHERE object_id = ANY($1)`, idArr).Scan(&intents)
		// orphans (no FK CASCADE): survive deletion as dangling references
		db.QueryRow(`SELECT count(*) FROM ont_knowledge WHERE anchor_type = 'object_type' AND anchor_id = ANY($1)`, idArr).Scan(&knowledge)
		db.QueryRow(`SELECT count(*) FROM ont_alias WHERE target_kind = 'object_type' AND target_id = ANY($1)`, idArr).Scan(&aliases)

		JsonResp(w, M{
			"objects":    len(ids),
			"properties": props,
			"links":      links,
			"keywords":   keywords,
			"intents":    intents,
			"orphans": M{
				"knowledge": knowledge,
				"aliases":   aliases,
			},
		})
	}
}

// HandleObjectsBulkDelete — POST /api/ontology/objects/bulk-delete
//
// Body: {"ids": ["uuid1", "uuid2", ...]}
// Returns: {"deleted": N}
//
// Schema CASCADE handles ont_property, ont_link_type, lakehouse_keyword,
// lakehouse_metric_intent. Orphans (ont_knowledge anchor_type='object_type',
// ont_alias) survive — they're surfaced via /bulk-impact for user awareness.
func HandleObjectsBulkDelete(db *sql.DB) http.HandlerFunc {
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
		// Project-scoped delete — forged ids from other projects are silently no-op.
		res, err := db.Exec(`DELETE FROM ont_object_type WHERE project_id = $1 AND id = ANY($2)`, pid, pq.Array(ids))
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		deleted, _ := res.RowsAffected()
		JsonResp(w, M{"deleted": deleted})
	}
}

// HandleObjectsBulkUpdate — POST /api/ontology/objects/bulk-update
//
// Apply a uniform partial update to every Od in `ids`. Each optional field
// is independent — set only the keys you want to change.
//
// Body shape:
//
//	{
//	  "ids":             ["uuid1", "uuid2"],
//	  "mark":            true,                 // optional bool — sets mark
//	  "kind":            "entity",             // optional — entity|event|attribute
//	  "description":     "...",                // optional new value
//	  "descriptionMode": "replace" | "append", // default "replace"
//	  "note":            "...",                // optional new value
//	  "noteMode":        "replace" | "append"  // default "replace"
//	}
//
// Returns: {"updated": N}
func HandleObjectsBulkUpdate(db *sql.DB) http.HandlerFunc {
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

		// Build SET clause + args dynamically. Each branch appends to the
		// edited-fields tracker so PBIT re-imports respect user edits.
		var sets []string
		var args []interface{}
		args = append(args, pid)             // $1 = project_id
		args = append(args, pq.Array(ids))   // $2 = id array
		var editedFields []string
		argIdx := 3 // next placeholder index

		if v, ok := body["mark"].(bool); ok {
			sets = append(sets, "mark = $"+itoa(argIdx))
			args = append(args, v)
			editedFields = append(editedFields, "mark")
			argIdx++
		}
		if v, ok := body["kind"].(string); ok && v != "" {
			if v != "entity" && v != "event" && v != "attribute" {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "kind must be entity|event|attribute"})
				return
			}
			sets = append(sets, "kind = $"+itoa(argIdx))
			args = append(args, v)
			editedFields = append(editedFields, "kind")
			argIdx++
		}
		if v, ok := body["description"].(string); ok {
			mode := strings.ToLower(StrVal(body, "descriptionMode"))
			if mode == "append" {
				sets = append(sets, "description = COALESCE(description,'') || $"+itoa(argIdx))
				args = append(args, "\n"+v)
			} else {
				sets = append(sets, "description = $"+itoa(argIdx))
				args = append(args, v)
			}
			editedFields = append(editedFields, "description")
			argIdx++
		}
		if v, ok := body["note"].(string); ok {
			mode := strings.ToLower(StrVal(body, "noteMode"))
			if mode == "append" {
				sets = append(sets, "note = COALESCE(note,'') || $"+itoa(argIdx))
				args = append(args, "\n"+v)
			} else {
				sets = append(sets, "note = $"+itoa(argIdx))
				args = append(args, v)
			}
			editedFields = append(editedFields, "note")
			argIdx++
		}

		if len(sets) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "at least one of mark|kind|description|note must be set"})
			return
		}

		// Append edited-field tracker. Postgres array literal built inline.
		efLit := "ARRAY[" + sqlStringArray(editedFields) + "]::text[]"
		sets = append(sets, "user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || "+efLit+")))")
		sets = append(sets, "updated_at = now()")

		q := "UPDATE ont_object_type SET " + strings.Join(sets, ", ") + " WHERE project_id = $1 AND id = ANY($2)"
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

// HandleObjectsBulkCreate — POST /api/ontology/objects/bulk-create
//
// Body: {"items": [{"name":..., "kind":..., "displayName":..., "description":..., "note":...}]}
// Returns: {"created": N, "ids": [...], "errors": [{index, name, error}]}
//
// One INSERT per row inside a single tx. Failures are collected per-row,
// not aborted (caller can retry the failed subset). Uniqueness violations
// (duplicate name within project) become row-level errors.
func HandleObjectsBulkCreate(db *sql.DB) http.HandlerFunc {
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

		validKind := map[string]bool{"entity": true, "event": true, "attribute": true}
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
			kind := strings.ToLower(strings.TrimSpace(StrVal(it, "kind")))
			if kind == "" {
				kind = "entity" // default
			}
			if !validKind[kind] {
				errs = append(errs, M{"index": i, "name": name, "error": "kind must be entity|event|attribute"})
				continue
			}
			displayName := StrVal(it, "displayName")
			description := StrVal(it, "description")
			note := StrVal(it, "note")
			sourceTable := StrVal(it, "sourceTable")

			var newID string
			err := db.QueryRow(`INSERT INTO ont_object_type
				(project_id, name, display_name, kind, description, source_table, source_type, mark, note)
				VALUES ($1, $2, $3, $4, $5, $6, 'manual', false, $7) RETURNING id`,
				pid, name, displayName, kind, description, sourceTable, note).Scan(&newID)
			if err != nil {
				errs = append(errs, M{"index": i, "name": name, "error": err.Error()})
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

// sqlStringArray formats a Go []string as the comma-separated literal list
// suitable for `ARRAY[...]::text[]`. Single quotes are escaped by doubling.
func sqlStringArray(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return strings.Join(parts, ",")
}
