package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

func handleKnowledgeEntries(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			skillJSON, _ := json.Marshal(body["skillConfig"])
			if string(skillJSON) == "null" {
				skillJSON = []byte("{}")
			}
			var id string
			err := db.QueryRow(`INSERT INTO ont_knowledge
				(project_id, topic_id, parent_id, title, summary, content,
				 entry_type, anchor_type, anchor_id, skill_config, sort_order, mark, note)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, false, $12)
				RETURNING id`,
				pid,
				NilIfEmpty(StrVal(body, "topicId")), NilIfEmpty(StrVal(body, "parentId")),
				StrVal(body, "title"), StrVal(body, "summary"), StrVal(body, "content"),
				StrVal(body, "entryType"), StrVal(body, "anchorType"),
				NilIfEmpty(StrVal(body, "anchorId")), string(skillJSON),
				numVal(body, "sortOrder"), StrVal(body, "note")).Scan(&id)
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

		topicID := r.URL.Query().Get("topicId")
		search := r.URL.Query().Get("search")
		anchorTypeFilter := r.URL.Query().Get("anchorType")

		q := `SELECT k.id, k.project_id, k.topic_id, k.parent_id,
			k.title, COALESCE(k.summary,''), COALESCE(k.content,''),
			k.entry_type, COALESCE(k.anchor_type,''), k.anchor_id,
			k.skill_config, k.sort_order, k.mark, COALESCE(k.note,''),
			k.created_at, k.updated_at,
			COALESCE(t.name,'') AS topic_name,
			(SELECT COUNT(*) FROM ont_knowledge_definition WHERE knowledge_id = k.id) AS def_count,
			(SELECT COUNT(*) FROM ont_knowledge_example WHERE knowledge_id = k.id) AS example_count
			FROM ont_knowledge k
			LEFT JOIN ont_topic t ON k.topic_id = t.id
			WHERE k.project_id = $1`
		args := []interface{}{pid}
		argIdx := 2

		if anchorTypeFilter != "" {
			q += ` AND k.anchor_type = $` + itoa(argIdx)
			args = append(args, anchorTypeFilter)
			argIdx++
		}
		if IsValidUUID(topicID) {
			q += ` AND k.topic_id = $` + itoa(argIdx)
			args = append(args, topicID)
			argIdx++
		}
		if search != "" {
			q += ` AND (k.title ILIKE $` + itoa(argIdx) + ` OR k.summary ILIKE $` + itoa(argIdx) + `)`
			args = append(args, "%"+search+"%")
			argIdx++
		}
		_ = argIdx
		q += ` ORDER BY t.sort_order, t.name, k.sort_order, k.title`

		rows, err := db.Query(q, args...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			var id, projectID, title, summary, content string
			var entryType, anchorType, note, topicName string
			var topicID, parentID, anchorID sql.NullString
			var skillConfigRaw string
			var sortOrder int
			var mark bool
			var defCount, exampleCount int
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &projectID, &topicID, &parentID,
				&title, &summary, &content,
				&entryType, &anchorType, &anchorID,
				&skillConfigRaw, &sortOrder, &mark, &note,
				&createdAt, &updatedAt, &topicName,
				&defCount, &exampleCount)

			var skillConfig interface{}
			json.Unmarshal([]byte(skillConfigRaw), &skillConfig)

			anchorName := ""
			if anchorID.Valid && anchorID.String != "" {
				anchorName = resolveAnchorName(db, anchorType, anchorID.String)
			}

			list = append(list, M{
				"id": id, "projectId": projectID,
				"topicId": NullStr(topicID), "topicName": topicName,
				"parentId": NullStr(parentID), "title": title,
				"summary": summary, "content": content,
				"entryType": entryType, "anchorType": anchorType,
				"anchorId": NullStr(anchorID), "anchorName": anchorName,
				"skillConfig": skillConfig, "sortOrder": sortOrder,
				"mark": mark, "note": note,
				"definitionCount": defCount, "exampleCount": exampleCount,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []M{}
		}
		ListResp(w, list, len(list))
	}
}

func handleKnowledgeEntryByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		path := r.URL.Path
		id := ExtractID(path, "/api/ontology/knowledge")

		if strings.HasSuffix(path, "/mark") {
			body := ReadBody(r)
			db.Exec(`UPDATE ont_knowledge SET mark = $1, updated_at = now() WHERE id = $2`, BoolVal(body, "mark"), id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			skillJSON, _ := json.Marshal(body["skillConfig"])
			if string(skillJSON) == "null" {
				skillJSON = []byte("{}")
			}
			_, err := db.Exec(`UPDATE ont_knowledge SET topic_id = $2, parent_id = $3,
				title = $4, summary = $5, content = $6, entry_type = $7,
				anchor_type = $8, anchor_id = $9, skill_config = $10::jsonb,
				sort_order = $11, note = $12, linked_property_id = $13, updated_at = now() WHERE id = $1`,
				id, NilIfEmpty(StrVal(body, "topicId")), NilIfEmpty(StrVal(body, "parentId")),
				StrVal(body, "title"), StrVal(body, "summary"), StrVal(body, "content"),
				StrVal(body, "entryType"), StrVal(body, "anchorType"),
				NilIfEmpty(StrVal(body, "anchorId")), string(skillJSON),
				numVal(body, "sortOrder"), StrVal(body, "note"),
				NilIfEmpty(StrVal(body, "linkedPropertyId")))
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodGet {
			var projectID, title, summary, content string
			var entryType, anchorType, note string
			var topicID, parentID, anchorID sql.NullString
			var skillConfigRaw string
			var sortOrder int
			var mark bool
			var createdAt, updatedAt time.Time
			var linkedPropID sql.NullString
			err := db.QueryRow(`SELECT id, project_id, topic_id, parent_id,
				title, COALESCE(summary,''), COALESCE(content,''),
				entry_type, COALESCE(anchor_type,''), anchor_id,
				skill_config, sort_order, mark, COALESCE(note,''),
				created_at, updated_at, linked_property_id
				FROM ont_knowledge WHERE id = $1`, id).Scan(
				&id, &projectID, &topicID, &parentID,
				&title, &summary, &content,
				&entryType, &anchorType, &anchorID,
				&skillConfigRaw, &sortOrder, &mark, &note,
				&createdAt, &updatedAt, &linkedPropID)
			if err != nil {
				w.WriteHeader(404)
				JsonResp(w, M{"error": "not found"})
				return
			}
			var skillConfig interface{}
			json.Unmarshal([]byte(skillConfigRaw), &skillConfig)
			anchorName := ""
			if anchorID.Valid && anchorID.String != "" {
				anchorName = resolveAnchorName(db, anchorType, anchorID.String)
			}
			// Resolve linked property name
			linkedPropName := ""
			if linkedPropID.Valid && linkedPropID.String != "" {
				db.QueryRow(`SELECT COALESCE(name,'') FROM ont_property WHERE id = $1`, linkedPropID.String).Scan(&linkedPropName)
			}

			// Fetch definitions
			var defs []M
			defRows, _ := db.Query(`SELECT id, def_type, COALESCE(content,''), sort_order, mark, COALESCE(note,''), created_at, updated_at
				FROM ont_knowledge_definition WHERE knowledge_id = $1 ORDER BY sort_order, created_at`, id)
			if defRows != nil {
				for defRows.Next() {
					var did, dt, dc, dn string
					var ds int
					var dm bool
					var dca, dua time.Time
					defRows.Scan(&did, &dt, &dc, &ds, &dm, &dn, &dca, &dua)
					defs = append(defs, M{"id": did, "knowledgeId": id, "defType": dt, "content": dc, "sortOrder": ds, "mark": dm, "note": dn, "createdAt": dca.Format(time.RFC3339), "updatedAt": dua.Format(time.RFC3339)})
				}
				defRows.Close()
			}
			if defs == nil {
				defs = []M{}
			}

			// Fetch examples
			var exs []M
			exRows, _ := db.Query(`SELECT id, example_type, COALESCE(content,''), sort_order, mark, COALESCE(note,''), created_at, updated_at
				FROM ont_knowledge_example WHERE knowledge_id = $1 ORDER BY sort_order, created_at`, id)
			if exRows != nil {
				for exRows.Next() {
					var eid, et, ec, en string
					var es int
					var em bool
					var eca, eua time.Time
					exRows.Scan(&eid, &et, &ec, &es, &em, &en, &eca, &eua)
					exs = append(exs, M{"id": eid, "knowledgeId": id, "exampleType": et, "content": ec, "sortOrder": es, "mark": em, "note": en, "createdAt": eca.Format(time.RFC3339), "updatedAt": eua.Format(time.RFC3339)})
				}
				exRows.Close()
			}
			if exs == nil {
				exs = []M{}
			}

			// Resolve topic name
			var topicName string
			if topicID.Valid && topicID.String != "" {
				db.QueryRow(`SELECT COALESCE(name,'') FROM ont_topic WHERE id = $1`, topicID.String).Scan(&topicName)
			}

			JsonResp(w, M{
				"id": id, "projectId": projectID,
				"topicId": NullStr(topicID), "topicName": topicName,
				"parentId": NullStr(parentID),
				"title": title, "summary": summary, "content": content,
				"entryType": entryType, "anchorType": anchorType,
				"anchorId": NullStr(anchorID), "anchorName": anchorName,
				"linkedPropertyId": NullStr(linkedPropID), "linkedPropertyName": linkedPropName,
				"skillConfig": skillConfig, "sortOrder": sortOrder,
				"mark": mark, "note": note,
				"definitions": defs, "examples": exs,
				"createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
			return
		}

		if r.Method == http.MethodDelete {
			// Property-anchored Ok entries can only be deleted via the Od property definition
			var anchorType string
			db.QueryRow(`SELECT COALESCE(anchor_type,'') FROM ont_knowledge WHERE id = $1`, id).Scan(&anchorType)
			if anchorType == "property" {
				w.WriteHeader(403)
				JsonResp(w, M{"error": "属性知识节点不能在此处删除，请在对象属性定义中删除该属性"})
				return
			}
			db.Exec(`DELETE FROM ont_knowledge WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}

// resolveAnchorName returns the display name for a grounding anchor.
func resolveAnchorName(db *sql.DB, anchorType, anchorID string) string {
	var name string
	switch anchorType {
	case "object":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_object_type WHERE id = $1`, anchorID).Scan(&name)
	case "metric":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_metric WHERE id = $1`, anchorID).Scan(&name)
	case "link":
		db.QueryRow(`SELECT COALESCE(link_name,'') FROM ont_link_type WHERE id = $1`, anchorID).Scan(&name)
	case "property":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_property WHERE id = $1`, anchorID).Scan(&name)
	}
	return name
}

// handleKnowledgeGenerate auto-creates concept skeletons from existing metrics.
func handleKnowledgeGenerate(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body := ReadBody(r)
		pid := GetProjectID(r)
		topicID := StrVal(body, "topicId")

		if !IsValidUUID(pid) || !IsValidUUID(topicID) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId and topicId are required"})
			return
		}

		// Load all metrics for this project
		rows, err := db.Query(`SELECT id, name, COALESCE(display_name,''), COALESCE(description,''),
			COALESCE(aggregation,'')
			FROM ont_metric WHERE project_id = $1`, pid)
		if err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		defer rows.Close()

		created := 0
		skipped := 0
		for rows.Next() {
			var mID, mName, mDisplay, mDesc, mAgg string
			rows.Scan(&mID, &mName, &mDisplay, &mDesc, &mAgg)

			// Check if a knowledge entry already grounds to this metric
			var existing int
			db.QueryRow(`SELECT COUNT(*) FROM ont_knowledge WHERE anchor_type = 'metric' AND anchor_id = $1`, mID).Scan(&existing)
			if existing > 0 {
				skipped++
				continue
			}

			title := mName
			if mDisplay != "" {
				title = mDisplay
			}
			summary := "指标: " + title
			if mDesc != "" {
				summary = mDesc
			}
			content := "## " + title + "\n\n"
			if mAgg != "" {
				content += "- 聚合方式: " + mAgg + "\n"
			}
			content += "\n请补充以下内容:\n- 业务含义:\n- 正常范围:\n- 易混淆项:\n"

			db.Exec(`INSERT INTO ont_knowledge
				(project_id, topic_id, title, summary, content,
				 entry_type, anchor_type, anchor_id, sort_order, mark)
				VALUES ($1, $2, $3, $4, $5, 'concept', 'metric', $6, 0, false)`,
				pid, topicID, title, summary, content, mID)
			created++
		}

		JsonResp(w, M{"created": created, "skipped": skipped})
	}
}

// handleKnowledgeSyncProperties bulk-creates Ok knowledge entries for Od properties
// that don't have one yet. Safe to call multiple times (idempotent).
func handleKnowledgeSyncProperties(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId is required"})
			return
		}

		// Single-property lazy creation: ?propertyId=UUID returns {"id": knowledgeId}
		if singlePropID := r.URL.Query().Get("propertyId"); IsValidUUID(singlePropID) {
			var kid string
			db.QueryRow(`SELECT id FROM ont_knowledge WHERE anchor_type='property' AND anchor_id=$1`, singlePropID).Scan(&kid)
			if kid != "" {
				JsonResp(w, M{"id": kid, "created": 0})
				return
			}
			var propName, displayName, desc, scol, dtype, objTypeID, objName string
			if err := db.QueryRow(`SELECT p.name, COALESCE(p.display_name,''), COALESCE(p.description,''),
				COALESCE(p.source_column,''), COALESCE(p.data_type,''), o.id, o.name
				FROM ont_property p JOIN ont_object_type o ON p.object_type_id=o.id WHERE p.id=$1`, singlePropID).
				Scan(&propName, &displayName, &desc, &scol, &dtype, &objTypeID, &objName); err != nil {
				w.WriteHeader(404)
				JsonResp(w, M{"error": "property not found"})
				return
			}
			autoCreatePropertyKnowledge(db, singlePropID, objTypeID, pid, propName, displayName, desc, scol, dtype)
			db.QueryRow(`SELECT id FROM ont_knowledge WHERE anchor_type='property' AND anchor_id=$1`, singlePropID).Scan(&kid)
			JsonResp(w, M{"id": kid, "created": 1})
			return
		}

		rows, err := db.Query(`
			SELECT p.id, p.name, COALESCE(p.display_name,''), COALESCE(p.description,''),
				COALESCE(p.source_column,''), COALESCE(p.data_type,''), o.name AS obj_name
			FROM ont_property p
			JOIN ont_object_type o ON p.object_type_id = o.id
			WHERE o.project_id = $1
			AND NOT EXISTS (
				SELECT 1 FROM ont_knowledge WHERE anchor_type = 'property' AND anchor_id = p.id
			)`, pid)
		if err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		defer rows.Close()

		created := 0
		for rows.Next() {
			var propID, name, displayName, desc, scol, dtype, objName string
			rows.Scan(&propID, &name, &displayName, &desc, &scol, &dtype, &objName)
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
			var kid string
			db.QueryRow(`INSERT INTO ont_knowledge
				(project_id, title, summary, content, entry_type, anchor_type, anchor_id, sort_order, mark, note)
				VALUES ($1, $2, $3, $4, 'concept', 'property', $5, 0, true, '') RETURNING id`,
				pid, title, summary, content, propID).Scan(&kid)
			if kid != "" {
				defContent := desc
				if defContent == "" {
					defContent = "来源列: " + scol
					if dtype != "" {
						defContent += "，数据类型: " + dtype
					}
				}
				if defContent != "" && defContent != "来源列: " {
					// project_id is NOT NULL on ont_knowledge_definition (FK to project).
					db.Exec(`INSERT INTO ont_knowledge_definition (knowledge_id, project_id, def_type, content, sort_order, mark)
						VALUES ($1, $2, 'positive', $3, 0, true)`, kid, pid, defContent)
				}
			}
			created++
		}
		JsonResp(w, M{"created": created})
	}
}


// numVal extracts a numeric value from a map (JSON numbers are float64 in Go).
