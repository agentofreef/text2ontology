package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lib/pq"

	. "github.com/lakehouse2ontology/httputil"
)

// Ontology export/import: project-scoped, full bundle
//
// Exports every project-owned ontology entity to a portable JSON bundle keyed
// by human-readable names (not UUIDs), then imports the bundle back into the
// same or a different project. Covers:
//   - ont_object_type + ont_property (objects and their column mappings)
//   - ont_link_type                  (object → object links)
//   - ont_metric                     (metric definitions incl. sql_expression)
//   - ont_alias                      (synonyms with target resolved by name)
//   - ont_resolution_rule            (disambiguation rules)
//   - ont_method                     (reusable method patterns)
//   - lakehouse_metric_intent        (canonical query templates + pivot)
//   - lakehouse_keyword              (token → property/metric_intent mappings)
//
// Vector columns (prop_vector / metric_vector / alias_vector / keyword_vector /
// alias_vector rows in lakehouse_keyword_alias_vector) are intentionally
// omitted — they can be recomputed from the source text after import. Runtime
// tables (ont_query_log, execution_history, agent threads, test suites) are
// also excluded since they are observational data, not ontology definition.

// ---- Export -----------------------------------------------------------------

// handleOntologyExport — GET /api/ontology/export?projectId=...
//
// Returns a JSON file download with all ontology data for the project.
// Content-Disposition carries a filename derived from the project's name
// plus export timestamp.
func handleOntologyExport(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		CorsHeaders(w)

		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}

		bundle, err := buildExportBundle(db, pid)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}

		stamp := time.Now().Format("20060102-150405")
		asciiName := fmt.Sprintf("ontology-%s.json", stamp)
		fullName := asciiName

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(
			`attachment; filename="%s"; filename*=UTF-8''%s`,
			asciiName, url.PathEscape(fullName),
		))
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bundle)
	}
}

// buildExportBundle assembles the full ontology export payload for one
// project. See file header for what's included / excluded.
func buildExportBundle(db *sql.DB, pid string) (M, error) {
	// --- objects + properties ---
	// Build objectID→name map so downstream tables (links/metrics/intents/keywords)
	// can dereference FKs to names.
	objIDToName := map[string]string{}
	objects := []M{}
	objRows, err := db.Query(`SELECT id, name, COALESCE(display_name,''), kind, COALESCE(description,''),
		COALESCE(source_table,''), COALESCE(source_type,''), COALESCE(origin,''),
		COALESCE(semantic_sql,''), COALESCE(canonical_query,''), validated_at,
		mark, COALESCE(note,''), COALESCE(user_edited_fields, '{}'::text[])
		FROM ont_object_type WHERE project_id = $1 AND deleted_at IS NULL
		ORDER BY kind DESC, name`, pid)
	if err != nil {
		return nil, err
	}
	defer objRows.Close()
	for objRows.Next() {
		var id, name, display, kind, desc, srcTable, srcType, origin string
		var semanticSQL, canonical, note string
		var validatedAt sql.NullTime
		var mark bool
		var uef pq.StringArray
		if err := objRows.Scan(&id, &name, &display, &kind, &desc, &srcTable, &srcType, &origin,
			&semanticSQL, &canonical, &validatedAt,
			&mark, &note, &uef); err != nil {
			continue
		}
		objIDToName[id] = name

		// properties for this object
		props := []M{}
		pRows, _ := db.Query(`SELECT name, COALESCE(display_name,''), COALESCE(data_type,''),
			COALESCE(source_column,''), is_filterable, is_groupable,
			COALESCE(enum_values, '{}'::text[]), COALESCE(description,''),
			COALESCE(is_machine_code, false), mark, COALESCE(note,''),
			COALESCE(user_edited_fields, '{}'::text[])
			FROM ont_property WHERE object_type_id = $1 AND deleted_at IS NULL ORDER BY name`, id)
		if pRows != nil {
			for pRows.Next() {
				var pname, pdisplay, ptype, pscol, pdesc, pnote string
				var pfilt, pgrp, pmc, pmark bool
				var penum pq.StringArray
				var puef pq.StringArray
				if err := pRows.Scan(&pname, &pdisplay, &ptype, &pscol, &pfilt, &pgrp,
					&penum, &pdesc, &pmc, &pmark, &pnote, &puef); err != nil {
					continue
				}
				props = append(props, M{
					"name":             pname,
					"displayName":      pdisplay,
					"dataType":         ptype,
					"sourceColumn":     pscol,
					"isFilterable":     pfilt,
					"isGroupable":      pgrp,
					"enumValues":       []string(penum),
					"description":      pdesc,
					"isMachineCode":    pmc,
					"mark":             pmark,
					"note":             pnote,
					"userEditedFields": []string(puef),
				})
			}
			pRows.Close()
		}

		objects = append(objects, M{
			"name":             name,
			"displayName":      display,
			"kind":             kind,
			"description":      desc,
			"sourceTable":      srcTable,
			"sourceType":       srcType,
			"origin":           origin,
			"semanticSql":      semanticSQL,
			"canonicalQuery":   canonical,
			"validatedAt":      nullTimeValue(validatedAt),
			"mark":             mark,
			"note":             note,
			"userEditedFields": []string(uef),
			"properties":       props,
		})
	}

	// --- links ---
	links := []M{}
	lRows, _ := db.Query(`SELECT from_object_id, to_object_id, COALESCE(link_name,''),
		COALESCE(fk_column,''), cardinality, COALESCE(reject_reason,''),
		COALESCE(description,''), mark, COALESCE(note,''),
		COALESCE(user_edited_fields, '{}'::text[])
		FROM ont_link_type WHERE project_id = $1 AND deleted_at IS NULL`, pid)
	if lRows != nil {
		for lRows.Next() {
			var fromID, toID, linkName, fkCol, card, reject, desc, note string
			var mark bool
			var uef pq.StringArray
			if err := lRows.Scan(&fromID, &toID, &linkName, &fkCol, &card, &reject, &desc, &mark, &note, &uef); err != nil {
				continue
			}
			links = append(links, M{
				"fromObjectName":   objIDToName[fromID],
				"toObjectName":     objIDToName[toID],
				"linkName":         linkName,
				"fkColumn":         fkCol,
				"cardinality":      card,
				"rejectReason":     reject,
				"description":      desc,
				"mark":             mark,
				"note":             note,
				"userEditedFields": []string(uef),
			})
		}
		lRows.Close()
	}

	// --- metrics ---
	metrics := []M{}
	mRows, _ := db.Query(`SELECT name, COALESCE(display_name,''), metric_type, COALESCE(aggregation,''),
		target_object_id, COALESCE(target_property,''), COALESCE(formula,''),
		COALESCE(depends_on, '{}'::text[]),
		COALESCE(sql_expression,''), COALESCE(format_string,''),
		COALESCE(description,''), mark, COALESCE(note,''),
		COALESCE(user_edited_fields, '{}'::text[])
		FROM ont_metric WHERE project_id = $1 AND deleted_at IS NULL ORDER BY metric_type, name`, pid)
	if mRows != nil {
		for mRows.Next() {
			var name, display, mtype, agg, tProp, formula, sqlExpr, fmtStr, desc, note string
			var targetObjID sql.NullString
			var dep pq.StringArray
			var uef pq.StringArray
			var mark bool
			if err := mRows.Scan(&name, &display, &mtype, &agg, &targetObjID, &tProp, &formula,
				&dep, &sqlExpr, &fmtStr, &desc, &mark, &note, &uef); err != nil {
				continue
			}
			metrics = append(metrics, M{
				"name":             name,
				"displayName":      display,
				"metricType":       mtype,
				"aggregation":      agg,
				"targetObjectName": objIDToName[NullStr(targetObjID)],
				"targetProperty":   tProp,
				"formula":          formula,
				"dependsOn":        []string(dep),
				"sqlExpression":    sqlExpr,
				"formatString":     fmtStr,
				"description":      desc,
				"mark":             mark,
				"note":             note,
				"userEditedFields": []string(uef),
			})
		}
		mRows.Close()
	}

	// Build metric name → id map so aliases.target_id(kind=metric) can resolve.
	metricIDToName := map[string]string{}
	mnRows, _ := db.Query(`SELECT id, name FROM ont_metric WHERE project_id = $1`, pid)
	if mnRows != nil {
		for mnRows.Next() {
			var id, name string
			mnRows.Scan(&id, &name)
			metricIDToName[id] = name
		}
		mnRows.Close()
	}
	// Property id → "ObjectName.PropertyName" so aliases.target_id(kind=property) can resolve.
	propIDToQN := map[string]string{}
	pqRows, _ := db.Query(`SELECT p.id, p.name, o.name
		FROM ont_property p JOIN ont_object_type o ON p.object_type_id = o.id
		WHERE o.project_id = $1`, pid)
	if pqRows != nil {
		for pqRows.Next() {
			var pid2, pname, oname string
			pqRows.Scan(&pid2, &pname, &oname)
			propIDToQN[pid2] = oname + "." + pname
		}
		pqRows.Close()
	}

	// --- aliases ---
	aliases := []M{}
	aRows, _ := db.Query(`SELECT alias_text, alias_type, target_id, COALESCE(target_kind,''),
		COALESCE(canonical_value,''), COALESCE(ambiguity_config::text,'null'),
		is_exact_match, priority, COALESCE(synonyms, '{}'::text[]),
		mark, COALESCE(note,'')
		FROM ont_alias WHERE project_id = $1`, pid)
	if aRows != nil {
		for aRows.Next() {
			var aText, aType, tKind, canon, ambRaw, note string
			var tid sql.NullString
			var exact, mark bool
			var prio int
			var syn pq.StringArray
			if err := aRows.Scan(&aText, &aType, &tid, &tKind, &canon, &ambRaw, &exact, &prio, &syn, &mark, &note); err != nil {
				continue
			}
			var amb interface{}
			if ambRaw != "" && ambRaw != "null" {
				_ = json.Unmarshal([]byte(ambRaw), &amb)
			}
			// Resolve target_id → human name based on target_kind
			targetRef := ""
			if tid.Valid {
				switch tKind {
				case "object", "object_type":
					targetRef = objIDToName[tid.String]
				case "metric":
					targetRef = metricIDToName[tid.String]
				case "property":
					targetRef = propIDToQN[tid.String]
				}
			}
			aliases = append(aliases, M{
				"aliasText":       aText,
				"aliasType":       aType,
				"targetKind":      tKind,
				"targetRef":       targetRef,
				"canonicalValue":  canon,
				"ambiguityConfig": amb,
				"isExactMatch":    exact,
				"priority":        prio,
				"synonyms":        []string(syn),
				"mark":            mark,
				"note":            note,
			})
		}
		aRows.Close()
	}

	// --- rules ---
	rules := []M{}
	rRows, _ := db.Query(`SELECT rule_type, trigger_key, COALESCE(rule_config::text,'{}'),
		priority, mark, COALESCE(note,'')
		FROM ont_resolution_rule WHERE project_id = $1`, pid)
	if rRows != nil {
		for rRows.Next() {
			var rType, tKey, cfgRaw, note string
			var prio int
			var mark bool
			if err := rRows.Scan(&rType, &tKey, &cfgRaw, &prio, &mark, &note); err != nil {
				continue
			}
			var cfg interface{}
			_ = json.Unmarshal([]byte(cfgRaw), &cfg)
			rules = append(rules, M{
				"ruleType":   rType,
				"triggerKey": tKey,
				"ruleConfig": cfg,
				"priority":   prio,
				"mark":       mark,
				"note":       note,
			})
		}
		rRows.Close()
	}

	// --- methods ---
	methods := []M{}
	mtRows, _ := db.Query(`SELECT method_name, COALESCE(display_name,''), COALESCE(description,''),
		COALESCE(trigger_words::text,'null'), COALESCE(parameters::text,'{}'),
		COALESCE(execution_config::text,'{}'), is_enabled, mark, COALESCE(note,'')
		FROM ont_method WHERE project_id = $1`, pid)
	if mtRows != nil {
		for mtRows.Next() {
			var mname, display, desc, twRaw, paramsRaw, execRaw, note string
			var enabled, mark bool
			if err := mtRows.Scan(&mname, &display, &desc, &twRaw, &paramsRaw, &execRaw, &enabled, &mark, &note); err != nil {
				continue
			}
			var tw, params, exec interface{}
			_ = json.Unmarshal([]byte(twRaw), &tw)
			_ = json.Unmarshal([]byte(paramsRaw), &params)
			_ = json.Unmarshal([]byte(execRaw), &exec)
			methods = append(methods, M{
				"methodName":      mname,
				"displayName":     display,
				"description":     desc,
				"triggerWords":    tw,
				"parameters":      params,
				"executionConfig": exec,
				"isEnabled":       enabled,
				"mark":            mark,
				"note":            note,
			})
		}
		mtRows.Close()
	}

	// --- metric intents ---
	intents := []M{}
	intentIDToName := map[string]string{}
	iRows, _ := db.Query(`SELECT id, object_id, name, COALESCE(display_name,''),
		COALESCE(canonical_metric,''), COALESCE(canonical_filters::text,'[]'),
		COALESCE(auto_group_by, '{}'::text[]),
		COALESCE(pivot_on,''), COALESCE(pivot_values, '{}'::text[]),
		COALESCE(pivot_column_labels, '{}'::text[]),
		COALESCE(pivot_total_label,'Total'),
		COALESCE(pivot_with_percent, false), COALESCE(pivot_append_grand_total, false),
		COALESCE(pivot_percent_axis,'row'),
		COALESCE(response_template,''), COALESCE(description,''), priority, mark
		FROM lakehouse_metric_intent WHERE project_id = $1 ORDER BY priority DESC, name`, pid)
	if iRows != nil {
		for iRows.Next() {
			var id, objID, name, display, metric, filtersRaw, pOn, pTotal, pAxis, respTpl, desc string
			var autoGB, pVals, pLabels pq.StringArray
			var pPct, pGrand, mark bool
			var prio int
			if err := iRows.Scan(&id, &objID, &name, &display, &metric, &filtersRaw, &autoGB,
				&pOn, &pVals, &pLabels, &pTotal, &pPct, &pGrand, &pAxis,
				&respTpl, &desc, &prio, &mark); err != nil {
				continue
			}
			intentIDToName[id] = name
			var filters interface{}
			_ = json.Unmarshal([]byte(filtersRaw), &filters)
			if filters == nil {
				filters = []interface{}{}
			}
			intents = append(intents, M{
				"objectName":            objIDToName[objID],
				"name":                  name,
				"displayName":           display,
				"canonicalMetric":       metric,
				"canonicalFilters":      filters,
				"autoGroupBy":           []string(autoGB),
				"pivotOn":               pOn,
				"pivotValues":           []string(pVals),
				"pivotColumnLabels":     []string(pLabels),
				"pivotTotalLabel":       pTotal,
				"pivotWithPercent":      pPct,
				"pivotAppendGrandTotal": pGrand,
				"pivotPercentAxis":      pAxis,
				"responseTemplate":      respTpl,
				"description":           desc,
				"priority":              prio,
				"mark":                  mark,
			})
		}
		iRows.Close()
	}

	// --- lakehouse keywords ---
	// Resolve FKs: object_type_id → objectName, property_id → propertyName,
	// metric_intent_id → intentName. Keep both hooks so import can re-anchor.
	keywords := []M{}
	propIDToName := map[string]string{}
	for id, qn := range propIDToQN {
		parts := strings.SplitN(qn, ".", 2)
		if len(parts) == 2 {
			propIDToName[id] = parts[1]
		}
	}
	kRows, _ := db.Query(`SELECT object_type_id, property_id, metric_intent_id,
		keyword, is_machine_code,
		COALESCE(is_column_name, false), COALESCE(aliases, '{}'::text[]),
		orphan_at
		FROM lakehouse_keyword WHERE project_id = $1`, pid)
	if kRows != nil {
		for kRows.Next() {
			var objID, keyword string
			var propID, intentID sql.NullString
			var isMC, isCol bool
			var aliasesArr pq.StringArray
			var orphanAt sql.NullTime
			if err := kRows.Scan(&objID, &propID, &intentID, &keyword, &isMC, &isCol, &aliasesArr, &orphanAt); err != nil {
				continue
			}
			// Only emit keywords whose object actually lives in this version.
			objName, ok := objIDToName[objID]
			if !ok {
				continue
			}
			rec := M{
				"objectName":    objName,
				"keyword":       keyword,
				"isMachineCode": isMC,
				"isColumnName":  isCol,
				"aliases":       []string(aliasesArr),
				"orphanAt":      nullTimeValue(orphanAt),
				"propertyName":  "",
				"intentName":    "",
			}
			if propID.Valid {
				rec["propertyName"] = propIDToName[propID.String]
			}
			if intentID.Valid {
				rec["intentName"] = intentIDToName[intentID.String]
			}
			keywords = append(keywords, rec)
		}
		kRows.Close()
	}

	bundle := M{
		"meta": M{
			"schemaVersion": 1,
			"exportedAt":    time.Now().Format(time.RFC3339),
			"projectId":     pid,
			"counts": M{
				"objects":       len(objects),
				"links":         len(links),
				"metrics":       len(metrics),
				"aliases":       len(aliases),
				"rules":         len(rules),
				"methods":       len(methods),
				"metricIntents": len(intents),
				"keywords":      len(keywords),
			},
		},
		"objects":       objects,
		"links":         links,
		"metrics":       metrics,
		"aliases":       aliases,
		"rules":         rules,
		"methods":       methods,
		"metricIntents": intents,
		"keywords":      keywords,
	}
	return bundle, nil
}

// nullTimeValue returns a time string or nil, for cleaner JSON output.
func nullTimeValue(t sql.NullTime) interface{} {
	if !t.Valid {
		return nil
	}
	return t.Time.Format(time.RFC3339)
}

// ---- Import -----------------------------------------------------------------

// handleOntologyImport — POST /api/ontology/import
//
// Body:
//
//	{
//	  "projectId": "<uuid>",
//	  "mode":      "merge" | "replace" | "skip",
//	  "payload":   { ... same shape as export bundle ... }
//	}
//
// Modes:
//
//	merge   — upsert by natural key (name / alias_text / rule_type+trigger_key / ...)
//	skip    — insert only if no row with that natural key exists
//	replace — delete all rows for the project first, then insert everything
//
// Returns a summary of counts { inserted, updated, skipped, errors }.
func handleOntologyImport(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		CorsHeaders(w)

		body := ReadBody(r)
		pid := StrVal(body, "projectId")
		if !IsValidUUID(pid) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}
		mode := strings.ToLower(StrVal(body, "mode"))
		if mode != "merge" && mode != "replace" && mode != "skip" {
			mode = "merge"
		}

		payloadAny, ok := body["payload"]
		if !ok {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "payload required"})
			return
		}
		payload, ok := payloadAny.(map[string]interface{})
		if !ok {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "payload must be an object"})
			return
		}

		summary := runImport(db, pid, mode, payload)
		JsonResp(w, M{
			"success": true,
			"mode":    mode,
			"summary": summary,
		})
	}
}

// importSummary accumulates counts across the import operation.
type importSummary struct {
	Inserted map[string]int `json:"inserted"`
	Updated  map[string]int `json:"updated"`
	Skipped  map[string]int `json:"skipped"`
	Errors   []string       `json:"errors"`
}

func newImportSummary() *importSummary {
	return &importSummary{
		Inserted: map[string]int{},
		Updated:  map[string]int{},
		Skipped:  map[string]int{},
		Errors:   []string{},
	}
}

func (s *importSummary) toM() M {
	return M{
		"inserted": s.Inserted,
		"updated":  s.Updated,
		"skipped":  s.Skipped,
		"errors":   s.Errors,
	}
}

// runImport drives the actual insert sequence, honoring the selected mode.
// Order matters because of FK references:
//
//	objects → properties → links → metrics → aliases → rules → methods →
//	metricIntents → keywords
func runImport(db *sql.DB, pid, mode string, payload map[string]interface{}) M {
	s := newImportSummary()

	// Replace mode: clear existing rows for this version first.
	// Child rows cascade via FK ON DELETE CASCADE.
	if mode == "replace" {
		db.Exec(`DELETE FROM ont_object_type        WHERE project_id = $1`, pid)
		db.Exec(`DELETE FROM ont_link_type          WHERE project_id = $1`, pid)
		db.Exec(`DELETE FROM ont_metric             WHERE project_id = $1`, pid)
		db.Exec(`DELETE FROM ont_alias              WHERE project_id = $1`, pid)
		db.Exec(`DELETE FROM ont_resolution_rule    WHERE project_id = $1`, pid)
		db.Exec(`DELETE FROM ont_method             WHERE project_id = $1`, pid)
		db.Exec(`DELETE FROM lakehouse_metric_intent WHERE project_id = $1`, pid)
		db.Exec(`DELETE FROM lakehouse_keyword      WHERE project_id = $1`, pid)
	}

	// objectName → id (built as objects are inserted / resolved)
	objNameToID := map[string]string{}
	// "ObjectName.PropertyName" → propertyId
	propQNToID := map[string]string{}
	// intent name → id
	intentNameToID := map[string]string{}
	// metric name → id
	metricNameToID := map[string]string{}

	// Pre-populate maps with rows already present (important for merge/skip modes).
	preloadNameMaps(db, pid, objNameToID, propQNToID, metricNameToID, intentNameToID)

	// ---- objects ----
	if rawObjects, ok := payload["objects"].([]interface{}); ok {
		for _, o := range rawObjects {
			om, ok := o.(map[string]interface{})
			if !ok {
				continue
			}
			name := StrVal(om, "name")
			if name == "" {
				continue
			}
			existingID := objNameToID[name]
			if existingID != "" && mode == "skip" {
				s.Skipped["objects"]++
				continue
			}
			if existingID != "" {
				// merge — update in place
				_, err := db.Exec(`UPDATE ont_object_type SET
					display_name=$2, kind=$3, description=$4, source_table=$5,
					source_type=NULLIF($6,''), origin=$7,
					semantic_sql=$8, canonical_query=$9, validated_at=$10,
					mark=$11, note=$12, user_edited_fields=$13, updated_at=now()
					WHERE id=$1`,
					existingID, StrVal(om, "displayName"), StrValDefault(om, "kind", "entity"),
					StrVal(om, "description"), StrVal(om, "sourceTable"),
					StrVal(om, "sourceType"), StrVal(om, "origin"),
					StrVal(om, "semanticSql"), StrVal(om, "canonicalQuery"),
					nullableTimeParam(StrVal(om, "validatedAt")),
					BoolVal(om, "mark"), StrVal(om, "note"),
					stringSliceToPgArray(om["userEditedFields"]))
				if err != nil {
					s.Errors = append(s.Errors, "object "+name+": "+err.Error())
					continue
				}
				s.Updated["objects"]++
			} else {
				var id string
				err := db.QueryRow(`INSERT INTO ont_object_type
					(project_id, name, display_name, kind, description, source_table,
					 source_type, origin, semantic_sql, canonical_query,
					 validated_at, mark, note, user_edited_fields)
					VALUES ($1,$2,$3,$4,$5,$6, NULLIF($7,''),$8,$9,$10, $11,$12,$13,$14)
					RETURNING id`,
					pid, name, StrVal(om, "displayName"), StrValDefault(om, "kind", "entity"),
					StrVal(om, "description"), StrVal(om, "sourceTable"),
					StrVal(om, "sourceType"), StrVal(om, "origin"),
					StrVal(om, "semanticSql"), StrVal(om, "canonicalQuery"),
					nullableTimeParam(StrVal(om, "validatedAt")),
					BoolVal(om, "mark"), StrVal(om, "note"),
					stringSliceToPgArray(om["userEditedFields"])).Scan(&id)
				if err != nil {
					s.Errors = append(s.Errors, "object "+name+": "+err.Error())
					continue
				}
				existingID = id
				objNameToID[name] = id
				s.Inserted["objects"]++
			}

			// properties for this object
			if rawProps, ok := om["properties"].([]interface{}); ok {
				for _, p := range rawProps {
					pm, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					pname := StrVal(pm, "name")
					if pname == "" {
						continue
					}
					qn := name + "." + pname
					existingPID := propQNToID[qn]
					if existingPID != "" && mode == "skip" {
						s.Skipped["properties"]++
						continue
					}
					if existingPID != "" {
						_, err := db.Exec(`UPDATE ont_property SET
							display_name=$2, data_type=$3, source_column=$4,
							is_filterable=$5, is_groupable=$6, enum_values=$7,
							description=$8, is_machine_code=$9, mark=$10, note=$11,
							user_edited_fields=$12, updated_at=now()
							WHERE id=$1`,
							existingPID,
							StrVal(pm, "displayName"), StrVal(pm, "dataType"), StrVal(pm, "sourceColumn"),
							boolValOr(pm, "isFilterable", true), boolValOr(pm, "isGroupable", true),
							stringSliceToPgArray(pm["enumValues"]),
							StrVal(pm, "description"), BoolVal(pm, "isMachineCode"),
							BoolVal(pm, "mark"), StrVal(pm, "note"),
							stringSliceToPgArray(pm["userEditedFields"]))
						if err != nil {
							s.Errors = append(s.Errors, "property "+qn+": "+err.Error())
							continue
						}
						s.Updated["properties"]++
					} else {
						var newPID string
						err := db.QueryRow(`INSERT INTO ont_property
							(project_id, object_type_id, name, display_name, data_type, source_column,
							 is_filterable, is_groupable, enum_values, description,
							 is_machine_code, mark, note, user_edited_fields)
							VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`,
							pid, existingID, pname, StrVal(pm, "displayName"), StrVal(pm, "dataType"),
							StrVal(pm, "sourceColumn"),
							boolValOr(pm, "isFilterable", true), boolValOr(pm, "isGroupable", true),
							stringSliceToPgArray(pm["enumValues"]),
							StrVal(pm, "description"), BoolVal(pm, "isMachineCode"),
							BoolVal(pm, "mark"), StrVal(pm, "note"),
							stringSliceToPgArray(pm["userEditedFields"])).Scan(&newPID)
						if err != nil {
							s.Errors = append(s.Errors, "property "+qn+": "+err.Error())
							continue
						}
						propQNToID[qn] = newPID
						s.Inserted["properties"]++
					}
				}
			}
		}
	}

	// ---- metrics ---- (built before aliases so alias target_id can resolve to metric)
	if raw, ok := payload["metrics"].([]interface{}); ok {
		for _, m := range raw {
			mm, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			name := StrVal(mm, "name")
			if name == "" {
				continue
			}
			existingID := metricNameToID[name]
			if existingID != "" && mode == "skip" {
				s.Skipped["metrics"]++
				continue
			}
			var targetObjID interface{}
			if oname := StrVal(mm, "targetObjectName"); oname != "" {
				if id := objNameToID[oname]; id != "" {
					targetObjID = id
				}
			}
			if existingID != "" {
				_, err := db.Exec(`UPDATE ont_metric SET
					display_name=$2, metric_type=$3, aggregation=$4,
					target_object_id=$5, target_property=$6, formula=$7,
					depends_on=$8, sql_expression=$9,
					format_string=$10, description=$11, mark=$12, note=$13,
					user_edited_fields=$14, updated_at=now()
					WHERE id=$1`,
					existingID, StrVal(mm, "displayName"),
					StrValDefault(mm, "metricType", "simple"),
					StrVal(mm, "aggregation"), targetObjID, StrVal(mm, "targetProperty"),
					StrVal(mm, "formula"), stringSliceToPgArray(mm["dependsOn"]),
					StrVal(mm, "sqlExpression"),
					StrVal(mm, "formatString"), StrVal(mm, "description"),
					BoolVal(mm, "mark"), StrVal(mm, "note"),
					stringSliceToPgArray(mm["userEditedFields"]))
				if err != nil {
					s.Errors = append(s.Errors, "metric "+name+": "+err.Error())
					continue
				}
				s.Updated["metrics"]++
			} else {
				var id string
				err := db.QueryRow(`INSERT INTO ont_metric
					(project_id, name, display_name, metric_type, aggregation,
					 target_object_id, target_property, formula, depends_on,
					 sql_expression, format_string, description,
					 mark, note, user_edited_fields)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`,
					pid, name, StrVal(mm, "displayName"),
					StrValDefault(mm, "metricType", "simple"),
					StrVal(mm, "aggregation"), targetObjID, StrVal(mm, "targetProperty"),
					StrVal(mm, "formula"), stringSliceToPgArray(mm["dependsOn"]),
					StrVal(mm, "sqlExpression"),
					StrVal(mm, "formatString"), StrVal(mm, "description"),
					BoolVal(mm, "mark"), StrVal(mm, "note"),
					stringSliceToPgArray(mm["userEditedFields"])).Scan(&id)
				if err != nil {
					s.Errors = append(s.Errors, "metric "+name+": "+err.Error())
					continue
				}
				metricNameToID[name] = id
				s.Inserted["metrics"]++
			}
		}
	}

	// ---- links ----
	if raw, ok := payload["links"].([]interface{}); ok {
		for _, l := range raw {
			lm, ok := l.(map[string]interface{})
			if !ok {
				continue
			}
			fromName := StrVal(lm, "fromObjectName")
			toName := StrVal(lm, "toObjectName")
			fromID := objNameToID[fromName]
			toID := objNameToID[toName]
			if fromID == "" || toID == "" {
				s.Errors = append(s.Errors, fmt.Sprintf("link %s→%s: missing object", fromName, toName))
				continue
			}
			linkName := StrVal(lm, "linkName")
			card := StrValDefault(lm, "cardinality", "many-to-one")
			// Natural key: (fromID, toID, linkName, fkColumn)
			fkCol := StrVal(lm, "fkColumn")
			var existingID string
			db.QueryRow(`SELECT id FROM ont_link_type WHERE project_id=$1
				AND from_object_id=$2 AND to_object_id=$3 AND COALESCE(link_name,'')=$4 AND COALESCE(fk_column,'')=$5`,
				pid, fromID, toID, linkName, fkCol).Scan(&existingID)
			if existingID != "" && mode == "skip" {
				s.Skipped["links"]++
				continue
			}
			if existingID != "" {
				db.Exec(`UPDATE ont_link_type SET cardinality=$2, reject_reason=$3,
					description=$4, mark=$5, note=$6, user_edited_fields=$7, updated_at=now()
					WHERE id=$1`,
					existingID, card, StrVal(lm, "rejectReason"),
					StrVal(lm, "description"), BoolVal(lm, "mark"), StrVal(lm, "note"),
					stringSliceToPgArray(lm["userEditedFields"]))
				s.Updated["links"]++
			} else {
				_, err := db.Exec(`INSERT INTO ont_link_type
					(project_id, from_object_id, to_object_id, link_name, fk_column,
					 cardinality, reject_reason, description, mark, note, user_edited_fields)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
					pid, fromID, toID, linkName, fkCol, card,
					StrVal(lm, "rejectReason"), StrVal(lm, "description"),
					BoolVal(lm, "mark"), StrVal(lm, "note"),
					stringSliceToPgArray(lm["userEditedFields"]))
				if err != nil {
					s.Errors = append(s.Errors, fmt.Sprintf("link %s→%s: %s", fromName, toName, err.Error()))
					continue
				}
				s.Inserted["links"]++
			}
		}
	}

	// ---- aliases ----
	if raw, ok := payload["aliases"].([]interface{}); ok {
		for _, a := range raw {
			am, ok := a.(map[string]interface{})
			if !ok {
				continue
			}
			aText := StrVal(am, "aliasText")
			aType := StrVal(am, "aliasType")
			if aText == "" || aType == "" {
				continue
			}
			tKind := StrVal(am, "targetKind")
			tRef := StrVal(am, "targetRef")
			var targetID interface{}
			switch tKind {
			case "object", "object_type":
				if id := objNameToID[tRef]; id != "" {
					targetID = id
				}
			case "metric":
				if id := metricNameToID[tRef]; id != "" {
					targetID = id
				}
			case "property":
				if id := propQNToID[tRef]; id != "" {
					targetID = id
				}
			}
			ambJSON := jsonAnyBytes(am["ambiguityConfig"], "null")
			var existingID string
			db.QueryRow(`SELECT id FROM ont_alias WHERE project_id=$1
				AND alias_text=$2 AND alias_type=$3`, pid, aText, aType).Scan(&existingID)
			if existingID != "" && mode == "skip" {
				s.Skipped["aliases"]++
				continue
			}
			if existingID != "" {
				db.Exec(`UPDATE ont_alias SET target_id=$2, target_kind=$3, canonical_value=$4,
					ambiguity_config=$5::jsonb, is_exact_match=$6, priority=$7,
					synonyms=$8, mark=$9, note=$10, updated_at=now() WHERE id=$1`,
					existingID, targetID, tKind, StrVal(am, "canonicalValue"),
					string(ambJSON), BoolVal(am, "isExactMatch"),
					intValDefault(am, "priority", 0),
					stringSliceToPgArray(am["synonyms"]), BoolVal(am, "mark"), StrVal(am, "note"))
				s.Updated["aliases"]++
			} else {
				_, err := db.Exec(`INSERT INTO ont_alias
					(project_id, alias_text, alias_type, target_id, target_kind,
					 canonical_value, ambiguity_config, is_exact_match, priority, synonyms, mark, note)
					VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12)`,
					pid, aText, aType, targetID, tKind,
					StrVal(am, "canonicalValue"), string(ambJSON),
					BoolVal(am, "isExactMatch"), intValDefault(am, "priority", 0),
					stringSliceToPgArray(am["synonyms"]), BoolVal(am, "mark"), StrVal(am, "note"))
				if err != nil {
					s.Errors = append(s.Errors, "alias "+aText+": "+err.Error())
					continue
				}
				s.Inserted["aliases"]++
			}
		}
	}

	// ---- rules ----
	if raw, ok := payload["rules"].([]interface{}); ok {
		for _, r := range raw {
			rm, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			rType := StrVal(rm, "ruleType")
			tKey := StrVal(rm, "triggerKey")
			if rType == "" || tKey == "" {
				continue
			}
			cfgJSON := jsonAnyBytes(rm["ruleConfig"], "{}")
			var existingID string
			db.QueryRow(`SELECT id FROM ont_resolution_rule WHERE project_id=$1
				AND rule_type=$2 AND trigger_key=$3`, pid, rType, tKey).Scan(&existingID)
			if existingID != "" && mode == "skip" {
				s.Skipped["rules"]++
				continue
			}
			if existingID != "" {
				db.Exec(`UPDATE ont_resolution_rule SET rule_config=$2::jsonb, priority=$3,
					mark=$4, note=$5 WHERE id=$1`,
					existingID, string(cfgJSON), intValDefault(rm, "priority", 0),
					BoolVal(rm, "mark"), StrVal(rm, "note"))
				s.Updated["rules"]++
			} else {
				_, err := db.Exec(`INSERT INTO ont_resolution_rule
					(project_id, rule_type, trigger_key, rule_config, priority, mark, note)
					VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7)`,
					pid, rType, tKey, string(cfgJSON),
					intValDefault(rm, "priority", 0), BoolVal(rm, "mark"), StrVal(rm, "note"))
				if err != nil {
					s.Errors = append(s.Errors, "rule "+rType+"/"+tKey+": "+err.Error())
					continue
				}
				s.Inserted["rules"]++
			}
		}
	}

	// ---- methods ----
	if raw, ok := payload["methods"].([]interface{}); ok {
		for _, m := range raw {
			mm, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			mname := StrVal(mm, "methodName")
			if mname == "" {
				continue
			}
			twJSON := jsonAnyBytes(mm["triggerWords"], "null")
			paramsJSON := jsonAnyBytes(mm["parameters"], "{}")
			execJSON := jsonAnyBytes(mm["executionConfig"], "{}")
			var existingID string
			db.QueryRow(`SELECT id FROM ont_method WHERE project_id=$1 AND method_name=$2`,
				pid, mname).Scan(&existingID)
			if existingID != "" && mode == "skip" {
				s.Skipped["methods"]++
				continue
			}
			if existingID != "" {
				db.Exec(`UPDATE ont_method SET display_name=$2, description=$3,
					trigger_words=$4::jsonb, parameters=$5::jsonb, execution_config=$6::jsonb,
					is_enabled=$7, mark=$8, note=$9, updated_at=now() WHERE id=$1`,
					existingID, StrVal(mm, "displayName"), StrVal(mm, "description"),
					string(twJSON), string(paramsJSON), string(execJSON),
					boolValOr(mm, "isEnabled", true), BoolVal(mm, "mark"), StrVal(mm, "note"))
				s.Updated["methods"]++
			} else {
				_, err := db.Exec(`INSERT INTO ont_method
					(project_id, method_name, display_name, description,
					 trigger_words, parameters, execution_config, is_enabled, mark, note)
					VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb,$8,$9,$10)`,
					pid, mname, StrVal(mm, "displayName"), StrVal(mm, "description"),
					string(twJSON), string(paramsJSON), string(execJSON),
					boolValOr(mm, "isEnabled", true), BoolVal(mm, "mark"), StrVal(mm, "note"))
				if err != nil {
					s.Errors = append(s.Errors, "method "+mname+": "+err.Error())
					continue
				}
				s.Inserted["methods"]++
			}
		}
	}

	// ---- metric intents ----
	if raw, ok := payload["metricIntents"].([]interface{}); ok {
		for _, i := range raw {
			im, ok := i.(map[string]interface{})
			if !ok {
				continue
			}
			name := StrVal(im, "name")
			if name == "" {
				continue
			}
			objName := StrVal(im, "objectName")
			objID := objNameToID[objName]
			if objID == "" {
				s.Errors = append(s.Errors, "intent "+name+": object "+objName+" not found")
				continue
			}
			filtersJSON := jsonAnyBytes(im["canonicalFilters"], "[]")
			pctAxis := StrVal(im, "pivotPercentAxis")
			if pctAxis != "row" && pctAxis != "column" {
				pctAxis = "row"
			}
			var existingID string
			db.QueryRow(`SELECT id FROM lakehouse_metric_intent WHERE project_id=$1 AND name=$2`,
				pid, name).Scan(&existingID)
			if existingID != "" && mode == "skip" {
				s.Skipped["metricIntents"]++
				continue
			}
			if existingID != "" {
				_, err := db.Exec(`UPDATE lakehouse_metric_intent SET
					object_id=$2, display_name=$3, canonical_metric=$4,
					canonical_filters=$5::jsonb, auto_group_by=$6,
					pivot_on=NULLIF($7,''), pivot_values=$8, pivot_column_labels=$9,
					pivot_total_label=COALESCE(NULLIF($10,''),'Total'),
					pivot_with_percent=$11, pivot_append_grand_total=$12,
					pivot_percent_axis=$13,
					response_template=$14, description=$15, priority=$16,
					mark=$17, updated_at=now() WHERE id=$1`,
					existingID, objID, StrVal(im, "displayName"), StrVal(im, "canonicalMetric"),
					string(filtersJSON), stringSliceToPgArray(im["autoGroupBy"]),
					StrVal(im, "pivotOn"), stringSliceToPgArray(im["pivotValues"]),
					stringSliceToPgArray(im["pivotColumnLabels"]),
					StrVal(im, "pivotTotalLabel"),
					BoolVal(im, "pivotWithPercent"), BoolVal(im, "pivotAppendGrandTotal"),
					pctAxis,
					StrVal(im, "responseTemplate"), StrVal(im, "description"),
					intValDefault(im, "priority", 0), boolValOr(im, "mark", true))
				if err != nil {
					s.Errors = append(s.Errors, "intent "+name+": "+err.Error())
					continue
				}
				intentNameToID[name] = existingID
				s.Updated["metricIntents"]++
			} else {
				var newID string
				err := db.QueryRow(`INSERT INTO lakehouse_metric_intent
					(project_id, object_id, name, display_name,
					 canonical_metric, canonical_filters, auto_group_by,
					 pivot_on, pivot_values, pivot_column_labels, pivot_total_label,
					 pivot_with_percent, pivot_append_grand_total, pivot_percent_axis,
					 response_template, description, priority, mark)
					VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,
					        NULLIF($8,''),$9,$10,COALESCE(NULLIF($11,''),'Total'),
					        $12,$13,$14,$15,$16,$17,$18) RETURNING id`,
					pid, objID, name, StrVal(im, "displayName"),
					StrVal(im, "canonicalMetric"), string(filtersJSON),
					stringSliceToPgArray(im["autoGroupBy"]),
					StrVal(im, "pivotOn"), stringSliceToPgArray(im["pivotValues"]),
					stringSliceToPgArray(im["pivotColumnLabels"]),
					StrVal(im, "pivotTotalLabel"),
					BoolVal(im, "pivotWithPercent"), BoolVal(im, "pivotAppendGrandTotal"),
					pctAxis,
					StrVal(im, "responseTemplate"), StrVal(im, "description"),
					intValDefault(im, "priority", 0), boolValOr(im, "mark", true)).Scan(&newID)
				if err != nil {
					s.Errors = append(s.Errors, "intent "+name+": "+err.Error())
					continue
				}
				intentNameToID[name] = newID
				s.Inserted["metricIntents"]++
			}
		}
	}

	// ---- lakehouse keywords ----
	if raw, ok := payload["keywords"].([]interface{}); ok {
		for _, k := range raw {
			km, ok := k.(map[string]interface{})
			if !ok {
				continue
			}
			keyword := StrVal(km, "keyword")
			objName := StrVal(km, "objectName")
			if keyword == "" || objName == "" {
				continue
			}
			objID := objNameToID[objName]
			if objID == "" {
				s.Errors = append(s.Errors, "keyword "+keyword+": object "+objName+" not found")
				continue
			}
			var propID, intentID interface{}
			if pn := StrVal(km, "propertyName"); pn != "" {
				if id := propQNToID[objName+"."+pn]; id != "" {
					propID = id
				}
			}
			if in := StrVal(km, "intentName"); in != "" {
				if id := intentNameToID[in]; id != "" {
					intentID = id
				}
			}
			if propID == nil && intentID == nil {
				s.Errors = append(s.Errors, "keyword "+keyword+": neither property nor intent anchor")
				continue
			}

			// Natural key: (project, object, property|intent, keyword)
			var existingID string
			if propID != nil {
				db.QueryRow(`SELECT id FROM lakehouse_keyword WHERE project_id=$1 AND object_type_id=$2
					AND property_id=$3 AND keyword=$4`, pid, objID, propID, keyword).Scan(&existingID)
			} else {
				db.QueryRow(`SELECT id FROM lakehouse_keyword WHERE project_id=$1 AND object_type_id=$2
					AND metric_intent_id=$3 AND keyword=$4`, pid, objID, intentID, keyword).Scan(&existingID)
			}
			if existingID != "" && mode == "skip" {
				s.Skipped["keywords"]++
				continue
			}
			if existingID != "" {
				db.Exec(`UPDATE lakehouse_keyword SET is_machine_code=$2, is_column_name=$3,
					aliases=$4, synced_at=now() WHERE id=$1`,
					existingID, BoolVal(km, "isMachineCode"),
					BoolVal(km, "isColumnName"), stringSliceToPgArray(km["aliases"]))
				s.Updated["keywords"]++
			} else {
				_, err := db.Exec(`INSERT INTO lakehouse_keyword
					(project_id, object_type_id, property_id, metric_intent_id, keyword,
					 is_machine_code, is_column_name, aliases)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
					pid, objID, propID, intentID, keyword,
					BoolVal(km, "isMachineCode"),
					BoolVal(km, "isColumnName"), stringSliceToPgArray(km["aliases"]))
				if err != nil {
					s.Errors = append(s.Errors, "keyword "+keyword+": "+err.Error())
					continue
				}
				s.Inserted["keywords"]++
			}
		}
	}

	return s.toM()
}

// preloadNameMaps populates the objectName/propertyQN/metricName/intentName →
// id maps with rows that already exist in the target version. This lets merge
// and skip modes find existing rows without a separate lookup per item.
func preloadNameMaps(db *sql.DB, pid string,
	objM, propQN, metricM, intentM map[string]string) {
	if rows, err := db.Query(`SELECT id, name FROM ont_object_type WHERE project_id=$1`, pid); err == nil {
		for rows.Next() {
			var id, name string
			rows.Scan(&id, &name)
			objM[name] = id
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT p.id, p.name, o.name
		FROM ont_property p JOIN ont_object_type o ON p.object_type_id = o.id
		WHERE o.project_id=$1`, pid); err == nil {
		for rows.Next() {
			var id, pname, oname string
			rows.Scan(&id, &pname, &oname)
			propQN[oname+"."+pname] = id
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT id, name FROM ont_metric WHERE project_id=$1`, pid); err == nil {
		for rows.Next() {
			var id, name string
			rows.Scan(&id, &name)
			metricM[name] = id
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT id, name FROM lakehouse_metric_intent WHERE project_id=$1`, pid); err == nil {
		for rows.Next() {
			var id, name string
			rows.Scan(&id, &name)
			intentM[name] = id
		}
		rows.Close()
	}
}

// ---- small helpers ----------------------------------------------------------

// StrValDefault returns the string at key, or def if missing/empty.
func StrValDefault(m map[string]interface{}, key, def string) string {
	v := StrVal(m, key)
	if v == "" {
		return def
	}
	return v
}

// boolValOr returns the bool at key, or def if absent. BoolVal from httputil
// returns false for absent keys, which is wrong when the default is true.
func boolValOr(m map[string]interface{}, key string, def bool) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// stringSliceToPgArray converts a JSON []interface{} / []string to a PG text
// array literal. Returns "{}" for nil/wrong-type.
func stringSliceToPgArray(v interface{}) string {
	switch t := v.(type) {
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			s := fmt.Sprintf("%v", x)
			s = strings.ReplaceAll(s, `\`, `\\`)
			s = strings.ReplaceAll(s, `"`, `\"`)
			parts = append(parts, `"`+s+`"`)
		}
		if len(parts) == 0 {
			return "{}"
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []string:
		if len(t) == 0 {
			return "{}"
		}
		return StringsSliceToPgArray(t)
	}
	return "{}"
}

// jsonAnyBytes marshals any JSON-compatible value; returns the provided
// fallback literal ("null"/"{}"/"[]") on nil or marshal failure.
func jsonAnyBytes(v interface{}, fallback string) []byte {
	if v == nil {
		return []byte(fallback)
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return []byte(fallback)
	}
	return b
}

// nullableTimeParam returns either the parsed time or nil (so NULL is written).
func nullableTimeParam(s string) interface{} {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return t
}
