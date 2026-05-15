package handler

// Builder Agent activation endpoints (US-005, US-006).
//
// These endpoints flip pending builder-proposed ontology rows
// (origin='builder', mark=false) into activated rows (mark=true) and
// perform the post-activation side-effects (canonical_query SOLIDIFY,
// trial-run, auto-Ok knowledge, trigger keywords, join_key causality).
// All work happens in a single transaction per endpoint so a failure
// leaves no partially-activated state behind.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
	"github.com/lib/pq"
)

// schemaNameRe validates lakehouse_schema before string-interpolating it
// into a SET LOCAL search_path. Matches Postgres unquoted identifier rules.
var schemaNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// activateRespondError writes a 4xx/5xx JSON error body in the
// {success:false,error:...} shape used by all builder endpoints.
func activateRespondError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	JsonResp(w, M{"success": false, "error": msg})
}

// pgQuoteIdent escapes a Postgres identifier (doubles internal quotes).
func pgQuoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// ─── POST /api/ontology/builder/activate-od ─────────────────────────────────
//
// Single-tx flow: ownership → apply edits → mark=true on object + props →
// inlined SOLIDIFY (canonical_query) → SET LOCAL search_path → LIMIT-10
// trial-run → auto-create Ok per property → COMMIT.
//
// Body shape:
//
//	{
//	  "objectId": "<uuid>",
//	  "projectId": "<uuid>",
//	  "edits": {
//	    "name"?: string, "kind"?: string, "description"?: string,
//	    "semanticSql"?: string,
//	    "properties"?: [{
//	      "name": string, "dataType"?: string, "sourceColumn"?: string,
//	      "isFilterable"?: bool, "isGroupable"?: bool, "isMachineCode"?: bool
//	    }]
//	  }
//	}
//
// On success returns 200 {success, objectId, canonicalQuery, rowCount,
// sampleRows[≤3], columns[]}. On any failure ROLLBACK + 400/404.
func handleBuilderActivateOd(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ObjectId  string                 `json:"objectId"`
			ProjectId string                 `json:"projectId"`
			Edits     map[string]interface{} `json:"edits"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			activateRespondError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if !IsValidUUID(req.ObjectId) || !IsValidUUID(req.ProjectId) {
			activateRespondError(w, http.StatusBadRequest, "objectId and projectId must be valid UUIDs")
			return
		}

		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "begin tx: "+err.Error())
			return
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		// 1. Ownership check.
		var ownerCheck string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM ont_object_type
			 WHERE id=$1 AND project_id=$2 AND origin='builder' AND mark=false`,
			req.ObjectId, req.ProjectId).Scan(&ownerCheck)
		if err == sql.ErrNoRows {
			activateRespondError(w, http.StatusNotFound, "pending builder OD not found or not owned by this project")
			return
		}
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "ownership check: "+err.Error())
			return
		}

		// 2. Apply edits to ont_object_type (partial — skip absent fields).
		odSetCols := []string{}
		odArgs := []interface{}{}
		appendOd := func(col string, val interface{}) {
			odSetCols = append(odSetCols, fmt.Sprintf("%s=$%d", col, len(odArgs)+1))
			odArgs = append(odArgs, val)
		}
		if req.Edits != nil {
			if v, ok := req.Edits["name"].(string); ok && v != "" {
				appendOd("name", v)
			}
			if v, ok := req.Edits["kind"].(string); ok && v != "" {
				appendOd("kind", v)
			}
			if v, ok := req.Edits["description"].(string); ok {
				appendOd("description", v)
			}
			if v, ok := req.Edits["semanticSql"].(string); ok && v != "" {
				appendOd("semantic_sql", v)
			}
		}
		if len(odSetCols) > 0 {
			odArgs = append(odArgs, req.ObjectId)
			q := fmt.Sprintf(`UPDATE ont_object_type SET %s, updated_at=now() WHERE id=$%d`,
				strings.Join(odSetCols, ", "), len(odArgs))
			if _, err := tx.ExecContext(ctx, q, odArgs...); err != nil {
				activateRespondError(w, http.StatusBadRequest, "apply OD edits: "+err.Error())
				return
			}
		}

		// 3. Apply property edits (match by name — stable identifier within OD).
		if req.Edits != nil {
			if propsRaw, ok := req.Edits["properties"].([]interface{}); ok {
				for _, p := range propsRaw {
					pm, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					pname, _ := pm["name"].(string)
					if pname == "" {
						continue
					}
					setCols := []string{}
					args := []interface{}{}
					appendP := func(col string, val interface{}) {
						setCols = append(setCols, fmt.Sprintf("%s=$%d", col, len(args)+1))
						args = append(args, val)
					}
					if v, ok := pm["dataType"].(string); ok && v != "" {
						appendP("data_type", v)
					}
					if v, ok := pm["sourceColumn"].(string); ok {
						appendP("source_column", v)
					}
					if v, ok := pm["isFilterable"].(bool); ok {
						appendP("is_filterable", v)
					}
					if v, ok := pm["isGroupable"].(bool); ok {
						appendP("is_groupable", v)
					}
					if v, ok := pm["isMachineCode"].(bool); ok {
						appendP("is_machine_code", v)
					}
					if len(setCols) == 0 {
						continue
					}
					args = append(args, req.ObjectId, pname)
					q := fmt.Sprintf(`UPDATE ont_property SET %s, updated_at=now() WHERE object_type_id=$%d AND name=$%d`,
						strings.Join(setCols, ", "), len(args)-1, len(args))
					if _, err := tx.ExecContext(ctx, q, args...); err != nil {
						activateRespondError(w, http.StatusBadRequest, "apply property edits: "+err.Error())
						return
					}
				}
			}
		}

		// 4. Activate OD + properties (mark=true).
		if _, err := tx.ExecContext(ctx,
			`UPDATE ont_object_type SET mark=true, updated_at=now() WHERE id=$1 AND project_id=$2`,
			req.ObjectId, req.ProjectId); err != nil {
			activateRespondError(w, http.StatusBadRequest, "activate OD: "+err.Error())
			return
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE ont_property SET mark=true, updated_at=now() WHERE object_type_id=$1`,
			req.ObjectId); err != nil {
			activateRespondError(w, http.StatusBadRequest, "activate properties: "+err.Error())
			return
		}

		// 5. Inlined SOLIDIFY logic.
		// NOTE: SOLIDIFY logic inlined from collector-server/ingest/pbit/routes.go:989. Keep in sync.
		var semanticSQL string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(semantic_sql,'') FROM ont_object_type WHERE id=$1`,
			req.ObjectId).Scan(&semanticSQL); err != nil {
			activateRespondError(w, http.StatusBadRequest, "read semantic_sql: "+err.Error())
			return
		}
		if strings.TrimSpace(semanticSQL) == "" {
			activateRespondError(w, http.StatusBadRequest, "semantic_sql is empty — cannot solidify")
			return
		}

		propRows, err := tx.QueryContext(ctx,
			`SELECT name, COALESCE(source_column,'') FROM ont_property WHERE object_type_id=$1 ORDER BY name`,
			req.ObjectId)
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "query properties: "+err.Error())
			return
		}
		type propMapping struct{ name, sourceCol string }
		var props []propMapping
		for propRows.Next() {
			var pm propMapping
			if err := propRows.Scan(&pm.name, &pm.sourceCol); err != nil {
				propRows.Close()
				activateRespondError(w, http.StatusInternalServerError, "scan property: "+err.Error())
				return
			}
			props = append(props, pm)
		}
		propRows.Close()

		var mapped []propMapping
		for _, p := range props {
			if p.sourceCol != "" {
				mapped = append(mapped, p)
			}
		}
		var selectCols string
		if len(mapped) == 0 {
			// Preserve routes.go:1057-1058 behavior — pass through all columns.
			selectCols = "od.*"
		} else {
			parts := make([]string, len(mapped))
			for i, p := range mapped {
				if p.sourceCol == p.name {
					parts[i] = "od." + pgQuoteIdent(p.sourceCol)
				} else {
					parts[i] = "od." + pgQuoteIdent(p.sourceCol) + " AS " + pgQuoteIdent(p.name)
				}
			}
			selectCols = strings.Join(parts, ", ")
		}
		canonicalQuery := fmt.Sprintf(`SELECT %s FROM (%s) AS od`, selectCols, semanticSQL)

		if _, err := tx.ExecContext(ctx,
			`UPDATE ont_object_type SET canonical_query=$1, validated_at=now(),
			 user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(user_edited_fields || ARRAY['canonical_query']::text[]))),
			 updated_at=now()
			 WHERE id=$2`,
			canonicalQuery, req.ObjectId); err != nil {
			activateRespondError(w, http.StatusInternalServerError, "store canonical_query: "+err.Error())
			return
		}

		// 6. Trial-run.
		var lakehouseSchema string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id=$1`,
			req.ProjectId).Scan(&lakehouseSchema); err != nil {
			activateRespondError(w, http.StatusBadRequest, "read project.lakehouse_schema: "+err.Error())
			return
		}
		if lakehouseSchema == "" {
			activateRespondError(w, http.StatusBadRequest, "project lakehouse_schema not configured")
			return
		}
		if !schemaNameRe.MatchString(lakehouseSchema) {
			activateRespondError(w, http.StatusBadRequest, "project lakehouse_schema is not a safe identifier")
			return
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`SET LOCAL search_path TO "%s", public`, lakehouseSchema)); err != nil {
			activateRespondError(w, http.StatusBadRequest, "set search_path: "+err.Error())
			return
		}

		trialQuery := fmt.Sprintf(`SELECT * FROM (%s) AS cq LIMIT 10`, canonicalQuery)
		rows, err := tx.QueryContext(ctx, trialQuery)
		if err != nil {
			activateRespondError(w, http.StatusBadRequest, "trial-run failed: "+err.Error())
			return
		}
		cols, _ := rows.Columns()
		var sampleRows []map[string]interface{}
		rowCount := 0
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				activateRespondError(w, http.StatusInternalServerError, "scan trial row: "+err.Error())
				return
			}
			rowCount++
			if len(sampleRows) < 3 {
				row := map[string]interface{}{}
				for i, c := range cols {
					if b, ok := vals[i].([]byte); ok {
						row[c] = string(b)
					} else {
						row[c] = vals[i]
					}
				}
				sampleRows = append(sampleRows, row)
			}
		}
		rows.Close()
		if sampleRows == nil {
			sampleRows = []map[string]interface{}{}
		}
		if cols == nil {
			cols = []string{}
		}

		// 7. Auto-create Ok knowledge per property (in-tx version of
		// autoCreatePropertyKnowledge from handler_object.go:14).
		var objName string
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM ont_object_type WHERE id=$1`, req.ObjectId).Scan(&objName)

		propRows2, err := tx.QueryContext(ctx,
			`SELECT id, name, COALESCE(display_name,''), COALESCE(description,''),
			        COALESCE(source_column,''), COALESCE(data_type,'')
			 FROM ont_property WHERE object_type_id=$1`, req.ObjectId)
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "list properties for knowledge: "+err.Error())
			return
		}
		type propRow struct{ id, name, display, desc, scol, dtype string }
		var allProps []propRow
		for propRows2.Next() {
			var p propRow
			if err := propRows2.Scan(&p.id, &p.name, &p.display, &p.desc, &p.scol, &p.dtype); err != nil {
				propRows2.Close()
				activateRespondError(w, http.StatusInternalServerError, "scan property for knowledge: "+err.Error())
				return
			}
			allProps = append(allProps, p)
		}
		propRows2.Close()

		for _, p := range allProps {
			title := p.name
			if p.display != "" {
				title = p.display
			}
			summary := p.desc
			if summary == "" {
				summary = title
			}
			content := "## " + title + "\n\n- 所属对象: " + objName + "\n"
			if p.scol != "" {
				content += "- 来源列: " + p.scol + "\n"
			}
			if p.dtype != "" {
				content += "- 数据类型: " + p.dtype + "\n"
			}
			if p.desc != "" {
				content += "\n" + p.desc
			}

			var kid string
			_ = tx.QueryRowContext(ctx,
				`SELECT id FROM ont_knowledge WHERE anchor_type='property' AND anchor_id=$1`,
				p.id).Scan(&kid)
			if kid == "" {
				if err := tx.QueryRowContext(ctx,
					`INSERT INTO ont_knowledge
					   (project_id, title, summary, content, entry_type, anchor_type, anchor_id, sort_order, mark, note)
					 VALUES ($1, $2, $3, $4, 'concept', 'property', $5, 0, true, '')
					 RETURNING id`,
					req.ProjectId, title, summary, content, p.id).Scan(&kid); err != nil {
					activateRespondError(w, http.StatusInternalServerError, "insert ont_knowledge: "+err.Error())
					return
				}
				if kid != "" {
					defContent := p.desc
					if defContent == "" && p.scol != "" {
						defContent = "来源列: " + p.scol
						if p.dtype != "" {
							defContent += "，数据类型: " + p.dtype
						}
					}
					if defContent != "" {
						// project_id is NOT NULL on ont_knowledge_definition (FK to project).
						if _, err := tx.ExecContext(ctx,
							`INSERT INTO ont_knowledge_definition
							   (knowledge_id, project_id, def_type, content, sort_order, mark)
							 VALUES ($1, $2, 'positive', $3, 0, true)`,
							kid, req.ProjectId, defContent); err != nil {
							activateRespondError(w, http.StatusInternalServerError, "insert ont_knowledge_definition: "+err.Error())
							return
						}
					}
				}
			} else {
				if _, err := tx.ExecContext(ctx,
					`UPDATE ont_knowledge SET title=$1, summary=$2, content=$3, updated_at=now() WHERE id=$4`,
					title, summary, content, kid); err != nil {
					activateRespondError(w, http.StatusInternalServerError, "update ont_knowledge: "+err.Error())
					return
				}
			}
		}

		// 8. Commit.
		if err := tx.Commit(); err != nil {
			activateRespondError(w, http.StatusInternalServerError, "commit: "+err.Error())
			return
		}
		committed = true

		JsonResp(w, M{
			"success":        true,
			"objectId":       req.ObjectId,
			"canonicalQuery": canonicalQuery,
			"rowCount":       rowCount,
			"sampleRows":     sampleRows,
			"columns":        cols,
		})
	}
}

// ─── POST /api/ontology/builder/activate-intent ─────────────────────────────
//
// Body: {intentId, projectId, edits, triggerKeywords[]}.
// Single tx: ownership → apply edits → mark=true → INSERT keywords (ON
// CONFLICT DO NOTHING) using object_type_id read from intent.object_id.
func handleBuilderActivateIntent(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			IntentId        string                 `json:"intentId"`
			ProjectId       string                 `json:"projectId"`
			Edits           map[string]interface{} `json:"edits"`
			TriggerKeywords []string               `json:"triggerKeywords"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			activateRespondError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if !IsValidUUID(req.IntentId) || !IsValidUUID(req.ProjectId) {
			activateRespondError(w, http.StatusBadRequest, "intentId and projectId must be valid UUIDs")
			return
		}

		// Orphan guard — activating an intent without trigger keywords would
		// create a fresh orphan (the very bug we're trying to eliminate). The
		// helper enforces the same invariant on REST POST; mirror it here so
		// every write path leads to the same outcome.
		nonBlank := 0
		for _, t := range req.TriggerKeywords {
			if strings.TrimSpace(t) != "" {
				nonBlank++
			}
		}
		if nonBlank == 0 {
			activateRespondError(w, http.StatusBadRequest,
				"triggerKeywords must contain ≥1 non-blank entry — recall cannot match an intent without trigger words")
			return
		}

		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "begin tx: "+err.Error())
			return
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		// 1. Ownership.
		var idCheck, objectID string
		err = tx.QueryRowContext(ctx,
			`SELECT id, object_id FROM lakehouse_metric_intent
			 WHERE id=$1 AND project_id=$2 AND mark=false`,
			req.IntentId, req.ProjectId).Scan(&idCheck, &objectID)
		if err == sql.ErrNoRows {
			activateRespondError(w, http.StatusNotFound, "pending builder Intent not found or already activated")
			return
		}
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "ownership check: "+err.Error())
			return
		}

		// 2. Apply edits (partial).
		setCols := []string{}
		args := []interface{}{}
		appendCol := func(col string, val interface{}) {
			setCols = append(setCols, fmt.Sprintf("%s=$%d", col, len(args)+1))
			args = append(args, val)
		}
		if req.Edits != nil {
			if v, ok := req.Edits["name"].(string); ok && v != "" {
				appendCol("name", v)
			}
			if v, ok := req.Edits["canonicalMetric"].(string); ok && v != "" {
				appendCol("canonical_metric", v)
			}
			if v, ok := req.Edits["canonicalFilters"]; ok {
				// JSONB — re-marshal to JSON bytes.
				b, mErr := json.Marshal(v)
				if mErr != nil {
					activateRespondError(w, http.StatusBadRequest, "marshal canonicalFilters: "+mErr.Error())
					return
				}
				appendCol("canonical_filters", string(b))
			}
			if v, ok := req.Edits["autoGroupBy"].([]interface{}); ok {
				strs := make([]string, 0, len(v))
				for _, x := range v {
					if s, ok := x.(string); ok {
						strs = append(strs, s)
					}
				}
				appendCol("auto_group_by", pq.Array(strs))
			}
			if v, ok := req.Edits["pivotOn"].(string); ok {
				appendCol("pivot_on", v)
			}
			if v, ok := req.Edits["pivotValues"].([]interface{}); ok {
				strs := make([]string, 0, len(v))
				for _, x := range v {
					if s, ok := x.(string); ok {
						strs = append(strs, s)
					}
				}
				appendCol("pivot_values", pq.Array(strs))
			}
			if v, ok := req.Edits["pivotColumnLabels"].([]interface{}); ok {
				strs := make([]string, 0, len(v))
				for _, x := range v {
					if s, ok := x.(string); ok {
						strs = append(strs, s)
					}
				}
				appendCol("pivot_column_labels", pq.Array(strs))
			}
			if v, ok := req.Edits["pivotTotalLabel"].(string); ok {
				appendCol("pivot_total_label", v)
			}
			if v, ok := req.Edits["pivotWithPercent"].(bool); ok {
				appendCol("pivot_with_percent", v)
			}
			if v, ok := req.Edits["pivotAppendGrandTotal"].(bool); ok {
				appendCol("pivot_append_grand_total", v)
			}
		}
		if len(setCols) > 0 {
			args = append(args, req.IntentId)
			q := fmt.Sprintf(`UPDATE lakehouse_metric_intent SET %s, updated_at=now() WHERE id=$%d`,
				strings.Join(setCols, ", "), len(args))
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				activateRespondError(w, http.StatusBadRequest, "apply Intent edits: "+err.Error())
				return
			}
		}

		// 3. Activate Intent.
		if _, err := tx.ExecContext(ctx,
			`UPDATE lakehouse_metric_intent SET mark=true, updated_at=now() WHERE id=$1`,
			req.IntentId); err != nil {
			activateRespondError(w, http.StatusBadRequest, "activate Intent: "+err.Error())
			return
		}

		// 4. Insert trigger keywords.
		keywordCount := 0
		for _, kw := range req.TriggerKeywords {
			kw = strings.TrimSpace(kw)
			if kw == "" {
				continue
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO lakehouse_keyword (project_id, object_type_id, metric_intent_id, keyword, is_machine_code)
				 VALUES ($1, $2, $3, $4, false)
				 ON CONFLICT DO NOTHING`,
				req.ProjectId, objectID, req.IntentId, kw)
			if err != nil {
				activateRespondError(w, http.StatusBadRequest, "insert keyword "+kw+": "+err.Error())
				return
			}
			n, _ := res.RowsAffected()
			keywordCount += int(n)
		}

		if err := tx.Commit(); err != nil {
			activateRespondError(w, http.StatusInternalServerError, "commit: "+err.Error())
			return
		}
		committed = true

		JsonResp(w, M{
			"success":      true,
			"intentId":     req.IntentId,
			"keywordCount": keywordCount,
		})
	}
}

// ─── POST /api/ontology/builder/activate-link ───────────────────────────────
//
// Body: {linkId, projectId, fromPropertyId, toPropertyId, edits}.
// Single tx: ownership → apply edits → mark=true → ensure Ok exists for
// from/to property → INSERT ont_causality(relation_type='join_key').
func handleBuilderActivateLink(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			LinkId         string                 `json:"linkId"`
			ProjectId      string                 `json:"projectId"`
			FromPropertyId string                 `json:"fromPropertyId"`
			ToPropertyId   string                 `json:"toPropertyId"`
			Edits          map[string]interface{} `json:"edits"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			activateRespondError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if !IsValidUUID(req.LinkId) || !IsValidUUID(req.ProjectId) ||
			!IsValidUUID(req.FromPropertyId) || !IsValidUUID(req.ToPropertyId) {
			activateRespondError(w, http.StatusBadRequest, "linkId/projectId/fromPropertyId/toPropertyId must be valid UUIDs")
			return
		}

		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "begin tx: "+err.Error())
			return
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		// 1. Ownership.
		var idCheck string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM ont_link_type
			 WHERE id=$1 AND project_id=$2 AND mark=false`,
			req.LinkId, req.ProjectId).Scan(&idCheck)
		if err == sql.ErrNoRows {
			activateRespondError(w, http.StatusNotFound, "pending builder Link not found or already activated")
			return
		}
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "ownership check: "+err.Error())
			return
		}

		// 2. Apply edits (partial).
		setCols := []string{}
		args := []interface{}{}
		appendCol := func(col string, val interface{}) {
			setCols = append(setCols, fmt.Sprintf("%s=$%d", col, len(args)+1))
			args = append(args, val)
		}
		if req.Edits != nil {
			// link_name (frontend sends "name" or "linkName")
			if v, ok := req.Edits["name"].(string); ok && v != "" {
				appendCol("link_name", v)
			} else if v, ok := req.Edits["linkName"].(string); ok && v != "" {
				appendCol("link_name", v)
			}
			if v, ok := req.Edits["description"].(string); ok {
				appendCol("description", v)
			}
			if v, ok := req.Edits["fkColumn"].(string); ok {
				appendCol("fk_column", v)
			}
			if v, ok := req.Edits["cardinality"].(string); ok && v != "" {
				appendCol("cardinality", v)
			}
		}
		if len(setCols) > 0 {
			args = append(args, req.LinkId)
			q := fmt.Sprintf(`UPDATE ont_link_type SET %s, updated_at=now() WHERE id=$%d`,
				strings.Join(setCols, ", "), len(args))
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				activateRespondError(w, http.StatusBadRequest, "apply Link edits: "+err.Error())
				return
			}
		}

		// 3. Activate Link.
		if _, err := tx.ExecContext(ctx,
			`UPDATE ont_link_type SET mark=true, updated_at=now() WHERE id=$1`,
			req.LinkId); err != nil {
			activateRespondError(w, http.StatusBadRequest, "activate Link: "+err.Error())
			return
		}

		// 4. Lookup-or-create Ok knowledge for both properties.
		ensureKnowledge := func(propID string) (string, error) {
			var kid string
			_ = tx.QueryRowContext(ctx,
				`SELECT id FROM ont_knowledge WHERE anchor_type='property' AND anchor_id=$1`,
				propID).Scan(&kid)
			if kid != "" {
				return kid, nil
			}
			var pname string
			if err := tx.QueryRowContext(ctx,
				`SELECT COALESCE(display_name, name, '') FROM ont_property WHERE id=$1`,
				propID).Scan(&pname); err != nil {
				return "", fmt.Errorf("read property %s: %w", propID, err)
			}
			if pname == "" {
				pname = "property"
			}
			content := pname + " (auto-generated for join_key link)."
			err := tx.QueryRowContext(ctx,
				`INSERT INTO ont_knowledge
				   (project_id, anchor_type, anchor_id, entry_type, title, summary, content, sort_order, mark, note)
				 VALUES ($1, 'property', $2, 'concept', $3, $4, $5, 0, true, '')
				 RETURNING id`,
				req.ProjectId, propID, pname, pname, content).Scan(&kid)
			return kid, err
		}

		fromKid, err := ensureKnowledge(req.FromPropertyId)
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "ensure from-property knowledge: "+err.Error())
			return
		}
		toKid, err := ensureKnowledge(req.ToPropertyId)
		if err != nil {
			activateRespondError(w, http.StatusInternalServerError, "ensure to-property knowledge: "+err.Error())
			return
		}

		// 5. Insert causality edge.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ont_causality
			   (project_id, from_knowledge_id, to_knowledge_id, relation_type, direction, description, mark)
			 VALUES ($1, $2, $3, 'join_key', 'positive', 'Auto-created from builder propose_link.', true)`,
			req.ProjectId, fromKid, toKid); err != nil {
			activateRespondError(w, http.StatusBadRequest, "insert causality: "+err.Error())
			return
		}

		if err := tx.Commit(); err != nil {
			activateRespondError(w, http.StatusInternalServerError, "commit: "+err.Error())
			return
		}
		committed = true

		JsonResp(w, M{
			"success": true,
			"linkId":  req.LinkId,
		})
	}
}

