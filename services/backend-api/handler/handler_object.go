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

// autoCreatePropertyKnowledge creates (or updates) the Ok knowledge entry for a property.
func autoCreatePropertyKnowledge(db *sql.DB, propID, objTypeID, projectID string, name, displayName, desc, scol, dtype string) {
	var objName string
	if err := db.QueryRow(`SELECT COALESCE(name,'') FROM ont_object_type WHERE id = $1`, objTypeID).Scan(&objName); err != nil {
		return
	}
	title := name
	if displayName != "" {
		title = displayName
	}
	summary := desc
	if summary == "" {
		summary = title
	}
	content := "## " + title + "\n\n- 所属对象: " + objName + "\n"
	if scol != "" {
		content += "- 来源列: " + scol + "\n"
	}
	if dtype != "" {
		content += "- 数据类型: " + dtype + "\n"
	}
	if desc != "" {
		content += "\n" + desc
	}

	// Check if an Ok entry already exists for this property
	var kid string
	db.QueryRow(`SELECT id FROM ont_knowledge WHERE anchor_type = 'property' AND anchor_id = $1`, propID).Scan(&kid)
	if kid == "" {
		db.QueryRow(`INSERT INTO ont_knowledge
			(project_id, title, summary, content, entry_type, anchor_type, anchor_id, sort_order, mark, note)
			VALUES ($1, $2, $3, $4, 'concept', 'property', $5, 0, true, '') RETURNING id`,
			projectID, title, summary, content, propID).Scan(&kid)
		// Create initial POSITIVE definition
		if kid != "" {
			defContent := desc
			if defContent == "" && scol != "" {
				defContent = fmt.Sprintf("来源列: %s", scol)
				if dtype != "" {
					defContent += fmt.Sprintf("，数据类型: %s", dtype)
				}
			}
			if defContent != "" {
				// project_id is NOT NULL on ont_knowledge_definition (FK to project).
				db.Exec(`INSERT INTO ont_knowledge_definition (knowledge_id, project_id, def_type, content, sort_order, mark)
					VALUES ($1, $2, 'positive', $3, 0, true)`, kid, projectID, defContent)
			}
		}
	} else {
		// Sync title/summary/content when property is updated
		db.Exec(`UPDATE ont_knowledge SET title = $1, summary = $2, content = $3, updated_at = now()
			WHERE id = $4`, title, summary, content, kid)
	}
}

func handleObjects(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			bodyPid := StrVal(body, "projectId")
			if bodyPid != "" {
				pid = bodyPid
			}
			if !IsValidUUID(pid) {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "projectId is required"})
				return
			}
			// A body projectId can override the (middleware-gated) query value,
			// so re-verify access against the effective pid before creating.
			if !authmw.EnforceProjectAccess(w, r, db, pid) {
				return
			}
			sourceType := StrVal(body, "sourceType")
			origin := StrVal(body, "origin")
			var id string
			err := db.QueryRow(`INSERT INTO ont_object_type (project_id, name, display_name, kind, description, source_table, source_type, origin, mark, note, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, false, $9, $10) RETURNING id`,
				pid, StrVal(body, "name"), StrVal(body, "displayName"),
				StrVal(body, "kind"), StrVal(body, "description"), StrVal(body, "sourceTable"),
				NilIfEmpty(sourceType), NilIfEmpty(origin),
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
		// Builder propose_* now writes mark=true directly (the old draft/approval
		// two-step UX was removed per UX simplification — propose = create).
		// Lists every Od regardless of origin / mark; the page's segmented filter
		// (active / unmarked / all) is the user-facing toggle when needed.
		rows, err := db.Query(`SELECT id, project_id, name, COALESCE(display_name,''), kind,
			COALESCE(description,''), COALESCE(source_table,''), bridged_from, mark, COALESCE(note,''),
			COALESCE(semantic_sql,''), COALESCE(canonical_query,''), validated_at,
			data_source_id::text,
			created_at, updated_at
			FROM ont_object_type
			WHERE project_id = $1
			ORDER BY kind DESC, name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, name, displayName, kind, desc, sourceTable, note string
			var semanticSQL, canonicalQuery string
			var validatedAt sql.NullTime
			var bridgedFrom, dataSourceID sql.NullString
			var mark bool
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &name, &displayName, &kind, &desc, &sourceTable,
				&bridgedFrom, &mark, &note,
				&semanticSQL, &canonicalQuery, &validatedAt,
				&dataSourceID,
				&createdAt, &updatedAt)

			// Fetch properties for this object. The two enrichment columns
			// (machine-code override, sample-value preview) used to come from
			// LEFT JOIN semantic_table / column_explanation — those tables only
			// exist in the enterprise schema, so the join silently errored in
			// community deployments and properties came back empty. Fall back
			// to the property's own is_machine_code; leave sample_values blank.
			propRows, _ := db.Query(`SELECT p.id, p.project_id, p.object_type_id, p.name, COALESCE(p.display_name,''),
				COALESCE(p.data_type,''), COALESCE(p.source_column,''), p.is_filterable, p.is_groupable,
				COALESCE(p.description,''), COALESCE(p.short_description,''), p.bridged_from, p.mark, COALESCE(p.note,''), p.created_at, p.updated_at,
				COALESCE(p.is_machine_code, false),
				''::text,
				p.keywords_synced_at
				FROM ont_property p
				WHERE p.object_type_id = $1 ORDER BY p.name`, id)
			var props []M
			if propRows != nil {
				for propRows.Next() {
					var propID, propProjID, oid, pname, pdisplay, dtype, scol, pdesc, pshortDesc, pnote string
					var pBridged sql.NullString
					var pFilterable, pGroupable, pmark bool
					var pIsMC bool
					var pSampleValues string
					var pcat, puat time.Time
					var pKeywordsSyncedAt sql.NullTime
					propRows.Scan(&propID, &propProjID, &oid, &pname, &pdisplay, &dtype, &scol,
						&pFilterable, &pGroupable, &pdesc, &pshortDesc, &pBridged, &pmark, &pnote, &pcat, &puat,
						&pIsMC, &pSampleValues, &pKeywordsSyncedAt)
					props = append(props, M{
						"id": propID, "objectTypeId": oid, "name": pname, "displayName": pdisplay,
						"dataType": dtype, "sourceColumn": scol, "isFilterable": pFilterable,
						"isGroupable": pGroupable, "description": pdesc, "shortDescription": pshortDesc,
						"bridgedFrom": NullStr(pBridged), "mark": pmark, "note": pnote,
						"isMachineCode": pIsMC, "sampleValues": pSampleValues,
						"keywordsSyncedAt": NullTimeStr(pKeywordsSyncedAt),
						"createdAt":        pcat.Format(time.RFC3339), "updatedAt": puat.Format(time.RFC3339),
					})
				}
				propRows.Close()
			}
			if props == nil {
				props = []M{}
			}

			list = append(list, M{
				"id": id, "projectId": projectID,
				"name": name, "displayName": displayName, "kind": kind,
				"description": desc, "sourceTable": sourceTable,
				"bridgedFrom": NullStr(bridgedFrom), "mark": mark, "note": note,
				"semanticSql": semanticSQL, "canonicalQuery": canonicalQuery,
				"validatedAt":  NullTimeStr(validatedAt),
				"dataSourceId": NullStr(dataSourceID),
				"properties":   props,
				"createdAt":    createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleObjectByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/objects")
		// Cross-project IDOR guard: confirm the caller can access the object's
		// project before any operation on this id (covers the whole /{id}/* subtree).
		if !authmw.EnforceEntityProject(w, r, db, "ont_object_type", "id", id) {
			return
		}

		// POST /api/ontology/objects/{id}/properties — create property
		if strings.HasSuffix(path, "/properties") && r.Method == http.MethodPost {
			body := ReadBody(r)
			pid := GetProjectID(r)
			var newID string
			err := db.QueryRow(`INSERT INTO ont_property (project_id, object_type_id, name, display_name, data_type, source_column,
				is_filterable, is_groupable, enum_values, description, short_description, mark, note, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false, $12, $13) RETURNING id`,
				pid, id, StrVal(body, "name"), StrVal(body, "displayName"), StrVal(body, "dataType"),
				StrVal(body, "sourceColumn"), BoolVal(body, "isFilterable"), BoolVal(body, "isGroupable"),
				StringsToPgArray(body, "enumValues"), StrVal(body, "description"), StrVal(body, "shortDescription"),
				StrVal(body, "note"), NilIfEmpty(StrVal(body, "createdBy"))).Scan(&newID)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			// Auto-create Ok knowledge entry for this property
			go autoCreatePropertyKnowledge(db, newID, id, pid,
				StrVal(body, "name"), StrVal(body, "displayName"),
				StrVal(body, "description"), StrVal(body, "sourceColumn"), StrVal(body, "dataType"))
			JsonResp(w, M{"id": newID})
			return
		}

		// GET /api/ontology/objects/{id}/properties — list properties
		if strings.HasSuffix(path, "/properties") && r.Method == http.MethodGet {
			propRows, err := db.Query(`SELECT id, project_id, object_type_id, name, COALESCE(display_name,''),
				COALESCE(data_type,''), COALESCE(source_column,''), is_filterable, is_groupable,
				COALESCE(description,''), COALESCE(short_description,''), bridged_from, mark, COALESCE(note,''), created_at, updated_at
				FROM ont_property WHERE object_type_id = $1 ORDER BY name`, id)
			if err != nil {
				ListResp(w, []M{}, 0)
				return
			}
			defer propRows.Close()
			var props []M
			for propRows.Next() {
				var propID, propPid, oid, pname, pdisplay, dtype, scol, pdesc, pshortDesc, pnote string
				var pBridged sql.NullString
				var pFilterable, pGroupable, pmark bool
				var pcat, puat time.Time
				propRows.Scan(&propID, &propPid, &oid, &pname, &pdisplay, &dtype, &scol,
					&pFilterable, &pGroupable, &pdesc, &pshortDesc, &pBridged, &pmark, &pnote, &pcat, &puat)
				props = append(props, M{
					"id": propID, "objectTypeId": oid, "name": pname, "displayName": pdisplay,
					"dataType": dtype, "sourceColumn": scol, "isFilterable": pFilterable,
					"isGroupable": pGroupable, "description": pdesc, "shortDescription": pshortDesc,
					"bridgedFrom": NullStr(pBridged), "mark": pmark, "note": pnote,
					"createdAt": pcat.Format(time.RFC3339), "updatedAt": puat.Format(time.RFC3339),
				})
			}
			if props == nil {
				props = []M{}
			}
			ListResp(w, props, len(props))
			return
		}

		// PUT /api/ontology/objects/{id}/mark
		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_object_type SET mark = $1,
				user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['mark']::text[]))),
				updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		// GET /api/ontology/objects/{id} — single Od with its properties.
		// Mirrors the row shape emitted by handleObjects (the list endpoint) so
		// the frontend detail page can use the same OntObjectType type.
		// Was missing — frontend's `/lakehouse-objects/detail` was 404'ing on
		// every direct/refresh navigation that hit this route by id.
		if r.Method == http.MethodGet {
			if !IsValidUUID(id) {
				w.WriteHeader(http.StatusBadRequest)
				JsonResp(w, M{"error": "invalid id"})
				return
			}
			var (
				projectID, name, displayName, kind, desc, sourceTable, note string
				semanticSQL, canonicalQuery                                  string
				validatedAt                                                  sql.NullTime
				bridgedFrom, dataSourceID                                    sql.NullString
				mark                                                         bool
				createdAt, updatedAt                                         time.Time
			)
			err := db.QueryRow(`SELECT project_id, name, COALESCE(display_name,''), kind,
				COALESCE(description,''), COALESCE(source_table,''), bridged_from, mark, COALESCE(note,''),
				COALESCE(semantic_sql,''), COALESCE(canonical_query,''), validated_at,
				data_source_id::text,
				created_at, updated_at
				FROM ont_object_type WHERE id = $1`, id).Scan(
				&projectID, &name, &displayName, &kind, &desc, &sourceTable,
				&bridgedFrom, &mark, &note, &semanticSQL, &canonicalQuery, &validatedAt,
				&dataSourceID,
				&createdAt, &updatedAt)
			if err == sql.ErrNoRows {
				w.WriteHeader(http.StatusNotFound)
				JsonResp(w, M{"error": "object not found"})
				return
			}
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			// Properties — community schema has no semantic_table /
			// column_explanation; drop the LEFT JOIN that silently errored
			// and starved the response of properties. Fall back to the
			// property's own is_machine_code; sample_values stays blank.
			propRows, _ := db.Query(`SELECT p.id, p.project_id, p.object_type_id, p.name, COALESCE(p.display_name,''),
				COALESCE(p.data_type,''), COALESCE(p.source_column,''), p.is_filterable, p.is_groupable,
				COALESCE(p.description,''), COALESCE(p.short_description,''), p.bridged_from, p.mark, COALESCE(p.note,''), p.created_at, p.updated_at,
				COALESCE(p.is_machine_code, false),
				''::text,
				p.keywords_synced_at
				FROM ont_property p
				WHERE p.object_type_id = $1 ORDER BY p.name`, id)
			var props []M
			if propRows != nil {
				for propRows.Next() {
					var propID, propProjID, oid, pname, pdisplay, dtype, scol, pdesc, pshortDesc, pnote string
					var pBridged sql.NullString
					var pFilterable, pGroupable, pmark, pIsMC bool
					var pSampleValues string
					var pcat, puat time.Time
					var pKeywordsSyncedAt sql.NullTime
					propRows.Scan(&propID, &propProjID, &oid, &pname, &pdisplay, &dtype, &scol,
						&pFilterable, &pGroupable, &pdesc, &pshortDesc, &pBridged, &pmark, &pnote, &pcat, &puat,
						&pIsMC, &pSampleValues, &pKeywordsSyncedAt)
					props = append(props, M{
						"id": propID, "objectTypeId": oid, "name": pname, "displayName": pdisplay,
						"dataType": dtype, "sourceColumn": scol, "isFilterable": pFilterable,
						"isGroupable": pGroupable, "description": pdesc, "shortDescription": pshortDesc,
						"bridgedFrom": NullStr(pBridged), "mark": pmark, "note": pnote,
						"isMachineCode": pIsMC, "sampleValues": pSampleValues,
						"keywordsSyncedAt": NullTimeStr(pKeywordsSyncedAt),
						"createdAt":        pcat.Format(time.RFC3339), "updatedAt": puat.Format(time.RFC3339),
					})
				}
				propRows.Close()
			}
			if props == nil {
				props = []M{}
			}
			JsonResp(w, M{
				"id": id, "projectId": projectID,
				"name": name, "displayName": displayName, "kind": kind,
				"description": desc, "sourceTable": sourceTable,
				"bridgedFrom": NullStr(bridgedFrom), "mark": mark, "note": note,
				"semanticSql": semanticSQL, "canonicalQuery": canonicalQuery,
				"validatedAt":  NullTimeStr(validatedAt),
				"dataSourceId": NullStr(dataSourceID),
				"properties":   props,
				"createdAt":    createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE ont_object_type SET name = $2, display_name = $3, kind = $4,
				description = $5, source_table = $6, note = $7, semantic_sql = $8,
				user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['display_name','description','semantic_sql']::text[]))),
				updated_at = now() WHERE id = $1`,
				id, StrVal(body, "name"), StrVal(body, "displayName"), StrVal(body, "kind"),
				StrVal(body, "description"), StrVal(body, "sourceTable"), StrVal(body, "note"),
				StrVal(body, "semanticSql"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_object_type WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}

// handlePropertyNodes returns all Od properties for a version, formatted as knowledge-like nodes.
// Uses a complex SELECT: LEFT JOIN ont_knowledge to surface existing Ok entries and def counts.
func handlePropertyNodes(db *sql.DB) http.HandlerFunc {
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

		baseQ := `
			SELECT
				p.id AS property_id,
				p.name,
				COALESCE(p.display_name, '') AS display_name,
				COALESCE(p.description, '') AS description,
				COALESCE(p.source_column, '') AS source_column,
				COALESCE(p.data_type, '') AS data_type,
				o.id AS object_type_id,
				o.name AS object_name,
				COALESCE(o.kind, '') AS object_kind,
				COALESCE(k.id::text, '') AS knowledge_id,
				COALESCE(k.mark, false) AS knowledge_mark,
				COALESCE(def_agg.def_count, 0) AS def_count,
				p.created_at,
				p.updated_at
			FROM ont_property p
			JOIN ont_object_type o ON p.object_type_id = o.id
			LEFT JOIN ont_knowledge k ON k.anchor_type = 'property' AND k.anchor_id = p.id
			LEFT JOIN (
				SELECT knowledge_id, COUNT(*) AS def_count
				FROM ont_knowledge_definition
				GROUP BY knowledge_id
			) def_agg ON def_agg.knowledge_id = k.id
			WHERE o.project_id = $1`

		rows, err := db.Query(baseQ+` ORDER BY o.name, p.name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()
		var list []M
		for rows.Next() {
			var propID, name, displayName, desc, scol, dtype string
			var objTypeID, objName, objKind string
			var knowledgeID string
			var knowledgeMark bool
			var defCount int
			var createdAt, updatedAt time.Time
			rows.Scan(&propID, &name, &displayName, &desc, &scol, &dtype,
				&objTypeID, &objName, &objKind,
				&knowledgeID, &knowledgeMark,
				&defCount, &createdAt, &updatedAt)
			title := name
			if displayName != "" {
				title = displayName
			}
			list = append(list, M{
				"id": propID, "title": title, "name": name, "displayName": displayName,
				"description": desc, "sourceColumn": scol, "dataType": dtype,
				"objectTypeId": objTypeID, "objectName": objName, "objectKind": objKind,
				"knowledgeId":     knowledgeID,
				"knowledgeMark":   knowledgeMark,
				"definitionCount": defCount,
				"createdAt":       createdAt.Format(time.RFC3339),
				"updatedAt":       updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleProperties(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		objectTypeID := r.URL.Query().Get("objectTypeId")
		if !IsValidUUID(objectTypeID) {
			ListResp(w, []M{}, 0)
			return
		}
		rows, err := db.Query(`SELECT id, name, COALESCE(data_type,''), COALESCE(source_column,''), COALESCE(description,'')
			FROM ont_property WHERE object_type_id = $1 ORDER BY name`, objectTypeID)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()
		var list []M
		for rows.Next() {
			var id, name, dt, sc, desc string
			rows.Scan(&id, &name, &dt, &sc, &desc)
			list = append(list, M{"id": id, "name": name, "dataType": dt, "sourceColumn": sc, "description": desc})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handlePropertyByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/properties")
		// Cross-project IDOR guard: verify project access before touching this property.
		if !authmw.EnforceEntityProject(w, r, db, "ont_property", "id", id) {
			return
		}

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_property SET mark = $1,
				user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['mark']::text[]))),
				updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		// POST /api/ontology/properties/{id}/extract-values removed in lakehouse2ontology
		// refactor (Stage 1): only worked via DAX EVALUATE against PBI live datasets,
		// which is no longer supported. A Postgres-native equivalent (SELECT DISTINCT
		// from staged tables) can be added in a follow-up if needed.

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE ont_property SET name = $2, display_name = $3, data_type = $4,
				source_column = $5, is_filterable = $6, is_groupable = $7, enum_values = $8,
				description = $9, note = $10, is_machine_code = $11, short_description = $12,
				user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['display_name','description','short_description','data_type','is_filterable','is_groupable','enum_values']::text[]))),
				updated_at = now() WHERE id = $1`,
				id, StrVal(body, "name"), StrVal(body, "displayName"), StrVal(body, "dataType"),
				StrVal(body, "sourceColumn"), BoolVal(body, "isFilterable"), BoolVal(body, "isGroupable"),
				StringsToPgArray(body, "enumValues"), StrVal(body, "description"), StrVal(body, "note"),
				BoolVal(body, "isMachineCode"), StrVal(body, "shortDescription"))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			// Sync the Ok knowledge entry (find object_type_id via property)
			var objTypeID, projID string
			if db.QueryRow(`SELECT object_type_id, project_id FROM ont_property WHERE id = $1`, id).Scan(&objTypeID, &projID) == nil {
				go autoCreatePropertyKnowledge(db, id, objTypeID, projID,
					StrVal(body, "name"), StrVal(body, "displayName"),
					StrVal(body, "description"), StrVal(body, "sourceColumn"), StrVal(body, "dataType"))
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			// Cascade delete the linked Ok knowledge entry first
			db.Exec(`DELETE FROM ont_knowledge WHERE anchor_type = 'property' AND anchor_id = $1`, id)
			db.Exec(`DELETE FROM ont_property WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
