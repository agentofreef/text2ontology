package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

// ─── Learned Facts (Ol) ───────────────────────────────────────

func handleLearnedFacts(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}

		confidence := r.URL.Query().Get("confidence")
		search := r.URL.Query().Get("search")

		q := `SELECT f.id, f.project_id,
			f.summary, COALESCE(f.content,''), f.confidence,
			f.source_thread_id, f.source_type,
			f.sort_order, f.mark, COALESCE(f.note,''),
			COALESCE(f.title,''), COALESCE(f.keywords,''),
			COALESCE(f.tags,'{}')::text, COALESCE(f.fact_type,'business_rule'),
			f.created_at, f.updated_at,
			(SELECT COUNT(*) FROM ont_fact_definition WHERE fact_id = f.id) AS def_count,
			(SELECT COUNT(*) FROM ont_fact_link WHERE fact_id = f.id) AS link_count
			FROM ont_learned_fact f
			WHERE f.project_id = $1`
		args := []interface{}{pid}
		argIdx := 2

		if confidence != "" {
			q += ` AND f.confidence = $` + itoa(argIdx)
			args = append(args, confidence)
			argIdx++
		}
		if search != "" {
			q += ` AND (f.summary ILIKE $` + itoa(argIdx) + ` OR COALESCE(f.content,'') ILIKE $` + itoa(argIdx) + `)`
			args = append(args, "%"+search+"%")
			argIdx++
		}
		_ = argIdx
		q += ` ORDER BY f.created_at DESC`

		rows, err := db.Query(q, args...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, summary, content string
			var confidence, sourceType, note string
			var title, keywords, tagsRaw, factType string
			var sourceThreadID sql.NullString
			var sortOrder int
			var mark bool
			var defCount, linkCount int
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID,
				&summary, &content, &confidence,
				&sourceThreadID, &sourceType,
				&sortOrder, &mark, &note,
				&title, &keywords, &tagsRaw, &factType,
				&createdAt, &updatedAt,
				&defCount, &linkCount)
			tags := ParsePgTextArray(tagsRaw)
			list = append(list, M{
				"id": id, "projectId": projectID,
				"summary": summary, "content": content, "confidence": confidence,
				"sourceThreadId": NullStr(sourceThreadID), "sourceType": sourceType,
				"sortOrder": sortOrder, "mark": mark, "note": note,
				"title": title, "keywords": keywords, "tags": tags, "factType": factType,
				"definitionCount": defCount, "linkCount": linkCount,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleLearnedFactByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/learned-facts")
		// Cross-project IDOR guard: verify project access before touching this learned fact.
		if !authmw.EnforceEntityProject(w, r, db, "ont_learned_fact", "id", id) {
			return
		}

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_learned_fact SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			// PATCH-style: only update fields explicitly present in the request body
			sets := []string{"updated_at = now()"}
			vals := []interface{}{id}
			idx := 2
			for _, field := range []string{"summary", "content", "confidence", "note", "title", "keywords"} {
				if v, ok := body[field]; ok {
					sets = append(sets, field+" = $"+itoa(idx))
					vals = append(vals, v)
					idx++
				}
			}
			// factType → fact_type column (camelCase → snake_case)
			if v, ok := body["factType"]; ok {
				sets = append(sets, "fact_type = $"+itoa(idx))
				vals = append(vals, v)
				idx++
			}
			// tags: text[] — accept array from body, store as pg literal
			if _, ok := body["tags"]; ok {
				tagsLit := StringsToPgArray(body, "tags")
				if tagsLit == nil {
					tagsLit = "{}"
				}
				sets = append(sets, "tags = $"+itoa(idx))
				vals = append(vals, tagsLit)
				idx++
				// Also sync legacy keywords column (first 2 tags pipe-joined) for backward compat
				if arr, ok := body["tags"].([]interface{}); ok {
					var kws []string
					for _, it := range arr {
						s := strings.TrimSpace(fmt.Sprintf("%v", it))
						if s != "" {
							kws = append(kws, s)
						}
						if len(kws) >= 2 {
							break
						}
					}
					if _, hasKw := body["keywords"]; !hasKw {
						sets = append(sets, "keywords = $"+itoa(idx))
						vals = append(vals, strings.Join(kws, "|"))
						idx++
					}
				}
			}
			// mark is boolean — needs special handling
			if v, ok := body["mark"]; ok {
				sets = append(sets, "mark = $"+itoa(idx))
				vals = append(vals, BoolVal(body, "mark"))
				idx++
				_ = v
			}
			q := "UPDATE ont_learned_fact SET " + strings.Join(sets, ", ") + " WHERE id = $1"
			if _, err := db.Exec(q, vals...); err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			// Recompute vector if title, keywords, or tags changed
			_, hasTitle := body["title"]
			_, hasKw := body["keywords"]
			_, hasTags := body["tags"]
			if hasTitle || hasKw || hasTags {
				var newTitle, newTagsRaw string
				db.QueryRow(`SELECT COALESCE(title,''), COALESCE(tags,'{}')::text FROM ont_learned_fact WHERE id = $1`, id).Scan(&newTitle, &newTagsRaw)
				go embedAndSaveFactVector(db, id, newTitle, strings.Join(ParsePgTextArray(newTagsRaw), " "))
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodGet {
			var projectID, summary, content string
			var confidence, sourceType, note string
			var title, keywords, tagsRaw, factType string
			var sourceThreadID sql.NullString
			var sortOrder int
			var mark bool
			var createdAt, updatedAt time.Time
			err := db.QueryRow(`SELECT id, project_id,
				summary, COALESCE(content,''), confidence,
				source_thread_id, source_type,
				sort_order, mark, COALESCE(note,''),
				COALESCE(title,''), COALESCE(keywords,''),
				COALESCE(tags,'{}')::text, COALESCE(fact_type,'business_rule'),
				created_at, updated_at
				FROM ont_learned_fact WHERE id = $1`, id).Scan(
				&id, &projectID,
				&summary, &content, &confidence,
				&sourceThreadID, &sourceType,
				&sortOrder, &mark, &note,
				&title, &keywords, &tagsRaw, &factType,
				&createdAt, &updatedAt)
			if err != nil {
				w.WriteHeader(404)
				JsonResp(w, M{"error": "not found"})
				return
			}

			// Fetch definitions
			var defs []M
			defRows, _ := db.Query(`SELECT id, def_type, COALESCE(content,''), sort_order, mark, COALESCE(note,''), created_at, updated_at
				FROM ont_fact_definition WHERE fact_id = $1 ORDER BY sort_order, created_at`, id)
			if defRows != nil {
				for defRows.Next() {
					var did, dt, dc, dn string
					var ds int
					var dm bool
					var dca, dua time.Time
					defRows.Scan(&did, &dt, &dc, &ds, &dm, &dn, &dca, &dua)
					defs = append(defs, M{"id": did, "factId": id, "defType": dt, "content": dc, "sortOrder": ds, "mark": dm, "note": dn, "createdAt": dca.Format(time.RFC3339), "updatedAt": dua.Format(time.RFC3339)})
				}
				defRows.Close()
			}
			if defs == nil {
				defs = []M{}
			}

			// Fetch links
			var links []M
			linkRows, _ := db.Query(`SELECT id, target_type, target_id, role, COALESCE(note,''), created_at
				FROM ont_fact_link WHERE fact_id = $1 ORDER BY created_at`, id)
			if linkRows != nil {
				for linkRows.Next() {
					var lid, targetType, targetID, role, lnote string
					var lca time.Time
					linkRows.Scan(&lid, &targetType, &targetID, &role, &lnote, &lca)
					targetName := resolveFactLinkTargetName(db, targetType, targetID)
					links = append(links, M{"id": lid, "factId": id, "targetType": targetType, "targetId": targetID, "targetName": targetName, "role": role, "note": lnote, "createdAt": lca.Format(time.RFC3339)})
				}
				linkRows.Close()
			}
			if links == nil {
				links = []M{}
			}

			tags := ParsePgTextArray(tagsRaw)
			JsonResp(w, M{
				"id": id, "projectId": projectID,
				"summary": summary, "content": content, "confidence": confidence,
				"sourceThreadId": NullStr(sourceThreadID), "sourceType": sourceType,
				"sortOrder": sortOrder, "mark": mark, "note": note,
				"title": title, "keywords": keywords, "tags": tags, "factType": factType,
				"definitions": defs, "links": links,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_learned_fact WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}

// ─── Fact Definitions ─────────────────────────────────────────

func handleFactDefinitions(db *sql.DB) http.HandlerFunc {
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
			err := db.QueryRow(`INSERT INTO ont_fact_definition
				(project_id, fact_id, def_type, content, sort_order, mark, note)
				VALUES ($1, $2, $3, $4, $5, false, $6) RETURNING id`,
				pid, StrVal(body, "factId"),
				StrVal(body, "defType"), StrVal(body, "content"),
				numVal(body, "sortOrder"), StrVal(body, "note")).Scan(&id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id})
			return
		}

		factID := r.URL.Query().Get("factId")
		if !IsValidUUID(factID) {
			ListResp(w, []M{}, 0)
			return
		}

		rows, err := db.Query(`SELECT id, fact_id, def_type,
			COALESCE(content,''), sort_order, mark, COALESCE(note,''),
			created_at, updated_at
			FROM ont_fact_definition
			WHERE fact_id = $1
			ORDER BY sort_order, created_at`, factID)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, fid, defType, content, note string
			var sortOrder int
			var mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &fid, &defType, &content, &sortOrder, &mark, &note, &createdAt, &updatedAt)
			list = append(list, M{
				"id": id, "factId": fid, "defType": defType,
				"content": content, "sortOrder": sortOrder,
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

func handleFactDefinitionByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/fact-definitions")
		// Cross-project IDOR guard: verify project access before touching this fact definition.
		if !authmw.EnforceEntityProject(w, r, db, "ont_fact_definition", "id", id) {
			return
		}

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_fact_definition SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE ont_fact_definition SET
				def_type = $2, content = $3, sort_order = $4, note = $5, updated_at = now()
				WHERE id = $1`,
				id, StrVal(body, "defType"), StrVal(body, "content"),
				numVal(body, "sortOrder"), StrVal(body, "note"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_fact_definition WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}

// ─── Fact Links ───────────────────────────────────────────────

func handleFactLinks(db *sql.DB) http.HandlerFunc {
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
			err := db.QueryRow(`INSERT INTO ont_fact_link
				(project_id, fact_id, target_type, target_id, role, note)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
				pid, StrVal(body, "factId"),
				StrVal(body, "targetType"), StrVal(body, "targetId"),
				StrVal(body, "role"), StrVal(body, "note")).Scan(&id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id})
			return
		}

		// GET: multiple query modes
		factID := r.URL.Query().Get("factId")
		targetType := r.URL.Query().Get("targetType")
		targetID := r.URL.Query().Get("targetId")

		var q string
		var args []interface{}

		if IsValidUUID(factID) {
			// By fact
			q = `SELECT l.id, l.fact_id, l.target_type, l.target_id::text, l.role, COALESCE(l.note,''), l.created_at
				FROM ont_fact_link l WHERE l.fact_id = $1 ORDER BY l.created_at`
			args = []interface{}{factID}
		} else if targetType != "" && IsValidUUID(targetID) {
			// By target (for graph queries)
			q = `SELECT l.id, l.fact_id, l.target_type, l.target_id::text, l.role, COALESCE(l.note,''), l.created_at
				FROM ont_fact_link l WHERE l.target_type = $1 AND l.target_id = $2 ORDER BY l.created_at`
			args = []interface{}{targetType, targetID}
		} else if IsValidUUID(pid) {
			// All links for project (via JOIN)
			q = `SELECT l.id, l.fact_id, l.target_type, l.target_id::text, l.role, COALESCE(l.note,''), l.created_at
				FROM ont_fact_link l
				JOIN ont_learned_fact f ON l.fact_id = f.id
				WHERE f.project_id = $1
				ORDER BY l.created_at`
			args = []interface{}{pid}
		} else {
			ListResp(w, []M{}, 0)
			return
		}

		rows, err := db.Query(q, args...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, fid, tType, tID, role, note string
			var createdAt time.Time
			rows.Scan(&id, &fid, &tType, &tID, &role, &note, &createdAt)
			targetName := resolveFactLinkTargetName(db, tType, tID)
			list = append(list, M{
				"id": id, "factId": fid,
				"targetType": tType, "targetId": tID, "targetName": targetName,
				"role": role, "note": note,
				"createdAt": createdAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleFactLinkByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/ontology/fact-links")
		// Cross-project IDOR guard: verify project access before touching this fact link.
		if !authmw.EnforceEntityProject(w, r, db, "ont_fact_link", "id", id) {
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_fact_link WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}

// ─── Helper ───────────────────────────────────────────────────

// resolveFactLinkTargetName returns the display name for a fact link target.
func resolveFactLinkTargetName(db *sql.DB, targetType, targetID string) string {
	var name string
	switch targetType {
	case "object":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_object_type WHERE id = $1`, targetID).Scan(&name)
	case "metric":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_metric WHERE id = $1`, targetID).Scan(&name)
	case "property":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_property WHERE id = $1`, targetID).Scan(&name)
	case "link":
		db.QueryRow(`SELECT COALESCE(link_name,'') FROM ont_link_type WHERE id = $1`, targetID).Scan(&name)
	case "knowledge":
		db.QueryRow(`SELECT COALESCE(title,'') FROM ont_knowledge WHERE id = $1`, targetID).Scan(&name)
	case "fact":
		db.QueryRow(`SELECT COALESCE(summary,'') FROM ont_learned_fact WHERE id = $1`, targetID).Scan(&name)
	}
	return name
}

// handleLearnedFactsRecomputeVectors batch-computes content_vector for facts
// that have title/keywords but no vector yet.
func handleLearnedFactsRecomputeVectors(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		rows, err := db.Query(`SELECT id::text, COALESCE(title,''), COALESCE(keywords,'')
			FROM ont_learned_fact
			WHERE content_vector IS NULL AND (title != '' OR keywords != '')`)
		if err != nil {
			JsonResp(w, M{"error": err.Error()})
			return
		}
		defer rows.Close()

		var ids []string
		type job struct{ id, title, keywords string }
		var jobs []job
		for rows.Next() {
			var id, title, kw string
			rows.Scan(&id, &title, &kw)
			jobs = append(jobs, job{id, title, kw})
			ids = append(ids, id)
		}

		go func() {
			for _, j := range jobs {
				embedAndSaveFactVector(db, j.id, j.title, j.keywords)
			}
		}()

		JsonResp(w, M{"queued": len(jobs), "ids": ids})
	}
}

// itoa + numVal live in util.go (shared across the handler package).
