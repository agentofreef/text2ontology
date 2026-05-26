package handler

// handler_lakehouse_metric.go — CRUD for the unified-metric table lakehouse_metric.
// Mirrors handler_intent.go but writes the FULL field set (level / parameters /
// replace_group_by / default_order_by_* / default_limit / plan) and links trigger
// keywords via metric_id. Reuses the SAME dry-run validator (validateIntentRemote /
// buildIntentValidateInput) — it validates the spec SHAPE and is table-agnostic.
// Coexists with the metric-intents handlers; does NOT touch lakehouse_metric_intent.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/ontology"
	"github.com/lakehouse2ontology/sqlrewrite"
)

// deriveObjectIDFromSQL resolves the metric's owning Od (object_id) from a
// SQL-mode metric's query_sql: the SQL references Od names in FROM/JOIN, so the
// first referenced Od that exists in the project IS the owning Od. This removes
// the redundant standalone "归属 OD" field — the Od is whatever the SQL targets.
// Reuses sqlrewrite.ExtractReferencedNames (the same scanner execute-sql validates
// with) so authoring + runtime agree. Returns "" if no known Od is referenced.
func deriveObjectIDFromSQL(db *sql.DB, projectID, querySQL string) string {
	// Strip inline {sys.req/opt.NAME} tokens before scanning FROM/JOIN so a token
	// adjacent to a clause keyword can't confuse ExtractReferencedNames. Passing
	// empty values renders all tokens away (values dropped, dimensions removed).
	rewritten, _, _ := sqlrewrite.RenderSysParams(querySQL, nil)
	for _, ref := range sqlrewrite.ExtractReferencedNames(rewritten) {
		var id string
		if err := db.QueryRow(
			`SELECT id::text FROM ont_object_type
			   WHERE project_id=$1 AND LOWER(name)=LOWER($2) AND COALESCE(mark,true)=true
			   LIMIT 1`, projectID, ref,
		).Scan(&id); err == nil && id != "" {
			return id
		}
	}
	return ""
}

// parseSimpleMetricFromSQL decomposes a "simple" metric's BARE authoring SQL
// — `select <dim>, ..., agg(...) from "<OD>" group by <dim>, ...` — into the
// three structural fields the runtime structured engine consumes:
//
//	objectID         — the primary OD's UUID (resolved from FROM)
//	canonicalMetric  — the first aggregate, e.g. `sum("ORDER_QUANTITY")`
//	autoGroupBy      — the non-aggregate select columns (always-on dims)
//
// The new "simple" editor sends ONLY the bare SQL; this helper parses it once
// at save-time so storage is structured (zero runtime parsing). Authors with
// exotic shapes (JOIN/subquery/multi-aggregate/window) get a clear parse error
// and fall back to the legacy `{sys.x}` / level='sql' path.
func parseSimpleMetricFromSQL(db *sql.DB, projectID, querySQL string) (objectID, canonicalMetric string, autoGroupBy []string, err error) {
	primaryOD, measure, baseDims, perr := sqlrewrite.ParseBareMetricSQL(querySQL)
	if perr != nil {
		return "", "", nil, perr
	}
	if qErr := db.QueryRow(
		`SELECT id::text FROM ont_object_type
		   WHERE project_id=$1 AND LOWER(name)=LOWER($2) AND COALESCE(mark,true)=true
		   LIMIT 1`, projectID, primaryOD,
	).Scan(&objectID); qErr != nil {
		return "", "", nil, fmt.Errorf("FROM 中的 OD %q 在本项目中未找到(或未启用 mark)", primaryOD)
	}
	return objectID, measure, baseDims, nil
}

// applyParsedBareSQL is the shared POST/PUT entry shim: when the body carries
// a non-`sql` (or unset) level AND a bare `querySql`, parse it and POPULATE
// the body's structured fields (level=simple, canonicalMetric, autoGroupBy,
// objectId). On parse failure, writes a 400 and returns ok=false.
//
// Mutating the body keeps the downstream MetricSpec construction unchanged —
// the dry-run validator and the writer both read these fields by name.
func applyParsedBareSQL(w http.ResponseWriter, db *sql.DB, projectID string, body M) (ok bool) {
	level := StrVal(body, "level")
	if level == "sql" {
		return true // legacy {sys.x} path; nothing to parse.
	}
	querySQL := strings.TrimSpace(StrVal(body, "querySql"))
	if querySQL == "" {
		return true // structured-only authoring (no bare SQL); skip.
	}
	oid, measure, baseDims, perr := parseSimpleMetricFromSQL(db, projectID, querySQL)
	if perr != nil {
		w.WriteHeader(400)
		JsonResp(w, M{
			"error": fmt.Sprintf("指标 SQL 解析失败: %v", perr),
			"code":  "BARE_SQL_PARSE_FAILED",
			"hint":  "新模型只接单层聚合 SELECT (无 JOIN/子查询/多聚合)。复杂形态请使用 level='sql' 走 {sys.req/opt} 路径。",
		})
		return false
	}
	body["level"] = "simple"
	body["objectId"] = oid
	body["canonicalMetric"] = measure
	agbAny := make([]interface{}, len(baseDims))
	for i, d := range baseDims {
		agbAny[i] = d
	}
	body["autoGroupBy"] = agbAny
	return true
}

// deriveSQLParamsJSON derives the metric's `parameters` JSONB array from a
// SQL-mode metric's query_sql, parsing the inline {sys.req/opt.NAME} tokens via
// sqlrewrite.ParseSysParams. Each param becomes an IntentParameter-shaped entry
// {name, type:"string", property:NAME, op:"=", optional:!Required, description:""}.
// This OVERRIDES any client-sent parameters — for SQL-mode metrics the params
// ARE the SQL, so deriving them here keeps the recall/agent layer's required-param
// knowledge in sync with the authored SQL. Returns "[]" when there are no params.
func deriveSQLParamsJSON(querySQL string) string {
	sys := sqlrewrite.ParseSysParams(querySQL)
	out := make([]map[string]interface{}, 0, len(sys))
	for _, p := range sys {
		out = append(out, map[string]interface{}{
			"name":        p.Name,
			"type":        "string",
			"property":    p.Name,
			"op":          "=",
			"optional":    !p.Required,
			"description": "",
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// HandleLakehouseMetrics handles /api/ontology/lakehouse-metrics:
//   - POST create (body: projectId, objectId, name, displayName, description,
//     level, canonicalMetric, canonicalFilters, autoGroupBy, replaceGroupBy,
//     defaultOrderByLabel, defaultOrderByDir, defaultLimit, pivot*, parameters,
//     plan, responseTemplate, priority, mark, triggerKeywords)
//   - GET  list   (query: projectId)
func HandleLakehouseMetrics(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			body := ReadBody(r)

			// New "simple" model entry: if the body carries a bare authoring
			// SQL (no {sys.x} tokens; level != 'sql'), parse it ONCE and
			// populate the structured fields the rest of this handler reads
			// (level, canonicalMetric, autoGroupBy, objectId). Parse error → 400.
			if !applyParsedBareSQL(w, db, pid, body) {
				return
			}

			// Dry-run gate (structured modes only): verify canonical_metric +
			// auto_group_by + parameters schema produce a valid spec before INSERT.
			// SQL mode (level='sql') authors raw SQL — the structural validator
			// doesn't apply; correctness is checked by Run/preview instead.
			if StrVal(body, "level") != "sql" {
				if vr := validateIntentRemote(r.Context(), buildIntentValidateInput(db, body)); !vr.Ok {
					w.WriteHeader(400)
					JsonResp(w, M{
						"error":  fmt.Sprintf("指标校验失败 (%s)", vr.Code),
						"errors": vr.Errors,
						"code":   vr.Code,
					})
					return
				}
			}

			objectID := StrVal(body, "objectId")
			// Multi-Od select: the metric can span several Ods; the FIRST selected
			// is the primary (object_id), the full set is persisted in extra.odIds.
			odIds := stringSliceFromBody(body, "odIds")
			if len(odIds) > 0 && IsValidUUID(odIds[0]) {
				objectID = odIds[0]
			}
			// SQL mode: if still unset, the owning Od is derived from the SQL's
			// FROM/JOIN refs — no standalone "归属 OD" field needed.
			if StrVal(body, "level") == "sql" && !IsValidUUID(objectID) {
				objectID = deriveObjectIDFromSQL(db, pid, StrVal(body, "querySql"))
			}
			if !IsValidUUID(objectID) {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "objectId is required（SQL 模式下，SQL 的 FROM/JOIN 需引用一个已存在的 OD）"})
				return
			}

			triggers := stringSliceFromBody(body, "triggerKeywords")

			// SQL-mode params are DERIVED from query_sql's inline {sys.req/opt.NAME}
			// tokens, OVERRIDING whatever parameters the client sent (the params ARE
			// the SQL now). Structured modes keep the client-supplied parameters.
			paramsJSON := string(jsonArrayBytes(body["parameters"]))
			if StrVal(body, "level") == "sql" {
				paramsJSON = deriveSQLParamsJSON(StrVal(body, "querySql"))
			}

			spec := ontology.MetricSpec{
				ProjectID:             pid,
				ObjectID:              objectID,
				Name:                  StrVal(body, "name"),
				DisplayName:           StrVal(body, "displayName"),
				Description:           StrVal(body, "description"),
				Level:                 StrVal(body, "level"),
				CanonicalMetric:       StrVal(body, "canonicalMetric"),
				CanonicalFilters:      string(jsonArrayBytes(body["canonicalFilters"])),
				AutoGroupBy:           stringSliceFromBody(body, "autoGroupBy"),
				ReplaceGroupBy:        derefBool(optBoolPtr(body, "replaceGroupBy")),
				DefaultOrderByLabel:   StrVal(body, "defaultOrderByLabel"),
				DefaultOrderByDir:     StrVal(body, "defaultOrderByDir"),
				DefaultLimit:          optIntPtr(body, "defaultLimit"),
				PivotOn:               StrVal(body, "pivotOn"),
				PivotValues:           stringSliceFromBody(body, "pivotValues"),
				PivotColumnLabels:     stringSliceFromBody(body, "pivotColumnLabels"),
				PivotTotalLabel:       StrVal(body, "pivotTotalLabel"),
				PivotWithPercent:      optBoolPtr(body, "pivotWithPercent"),
				PivotAppendGrandTotal: optBoolPtr(body, "pivotAppendGrandTotal"),
				PivotPercentAxis:      StrVal(body, "pivotPercentAxis"),
				PivotPercentScope:     StrVal(body, "pivotPercentScope"),
				PivotPercentSuffix:    StrVal(body, "pivotPercentSuffix"),
				Parameters:            paramsJSON,
				Plan:                  jsonObjOrEmpty(body["plan"]),
				QuerySQL:              StrVal(body, "querySql"),
				ResponseTemplate:      StrVal(body, "responseTemplate"),
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

			metricID, err := ontology.WriteMetricWithTriggers(r.Context(), tx, spec, triggers)
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
			// Persist the full multi-Od selection in extra.odIds (object_id holds
			// only the primary). Best-effort within the tx.
			if len(odIds) > 0 {
				if eb, mErr := json.Marshal(map[string][]string{"odIds": odIds}); mErr == nil {
					if _, uErr := tx.ExecContext(r.Context(),
						`UPDATE lakehouse_metric SET extra = $2::jsonb WHERE id=$1`, metricID, string(eb)); uErr != nil {
						w.WriteHeader(500)
						JsonResp(w, M{"error": "persist odIds: " + uErr.Error()})
						return
					}
				}
			}
			if err := tx.Commit(); err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": "commit: " + err.Error()})
				return
			}
			JsonResp(w, M{"id": metricID})
			return
		}

		// GET list
		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}
		rows, err := db.Query(lakehouseMetricListSQL+` WHERE m.deleted_at IS NULL AND m.project_id = $1
			ORDER BY m.priority DESC, m.name`, pid)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		for rows.Next() {
			item, scanErr := scanLakehouseMetric(rows)
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

// HandleLakehouseMetricByID: GET / PUT / DELETE on /api/ontology/lakehouse-metrics/{id}
func HandleLakehouseMetricByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/ontology/lakehouse-metrics")
		if !IsValidUUID(id) {
			http.NotFound(w, r)
			return
		}
		if !authmw.EnforceEntityProject(w, r, db, "lakehouse_metric", "id", id) {
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)

			// Resolve the metric's AUTHORITATIVE project before parsing the bare
			// SQL: the PUT URL may omit ?projectId=, and OD names (e.g.
			// EARLY_ORDER) can exist in several projects, so the OD lookup must
			// be scoped to THIS metric's project. Prefer the request's projectId,
			// fall back to the stored metric.project_id (EnforceEntityProject
			// above already verified the caller owns this entity's project).
			putProjID := GetProjectID(r)
			if !IsValidUUID(putProjID) {
				_ = db.QueryRowContext(r.Context(),
					`SELECT project_id::text FROM lakehouse_metric WHERE id=$1`, id).Scan(&putProjID)
			}

			// New "simple" model entry: parse bare authoring SQL → populate
			// structured fields (level, canonicalMetric, autoGroupBy, objectId)
			// BEFORE the dry-run gate, so the validator sees the populated form.
			if !applyParsedBareSQL(w, db, putProjID, body) {
				return
			}

			// Structured-only dry-run gate; SQL mode (level='sql') authors raw SQL.
			if StrVal(body, "level") != "sql" {
				if vr := validateIntentRemote(r.Context(), buildIntentValidateInput(db, body)); !vr.Ok {
					w.WriteHeader(400)
					JsonResp(w, M{
						"error":  fmt.Sprintf("指标校验失败 (%s)", vr.Code),
						"errors": vr.Errors,
						"code":   vr.Code,
					})
					return
				}
			}

			filtersJSON := jsonArrayBytes(body["canonicalFilters"])
			paramsJSON := jsonArrayBytes(body["parameters"])
			planArg := jsonObjOrNil(body["plan"])
			level := StrVal(body, "level")
			if level != "plan" && level != "sql" {
				level = "simple"
			}
			// SQL-mode params are DERIVED from query_sql (inline {sys.req/opt.NAME}),
			// OVERRIDING the client-sent parameters so they stay in sync with the SQL.
			if level == "sql" {
				paramsJSON = []byte(deriveSQLParamsJSON(StrVal(body, "querySql")))
			}
			canonicalMetric := StrVal(body, "canonicalMetric")
			if level == "sql" && canonicalMetric == "" {
				canonicalMetric = "(sql)" // sentinel; runtime routes on level
			}
			// Multi-Od select: primary = first selected; full set → extra.odIds.
			objIDForUpdate := StrVal(body, "objectId")
			odIds := stringSliceFromBody(body, "odIds")
			if len(odIds) > 0 && IsValidUUID(odIds[0]) {
				objIDForUpdate = odIds[0]
			}
			if level == "sql" && !IsValidUUID(objIDForUpdate) {
				objIDForUpdate = deriveObjectIDFromSQL(db, GetProjectID(r), StrVal(body, "querySql"))
			}
			// extra.odIds: marshal when provided; nil keeps the existing value.
			var extraArg interface{}
			if len(odIds) > 0 {
				if eb, mErr := json.Marshal(map[string][]string{"odIds": odIds}); mErr == nil {
					extraArg = string(eb)
				}
			}
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
			_, hasTriggers := body["triggerKeywords"]
			triggers := stringSliceFromBody(body, "triggerKeywords")

			tx, err := db.BeginTx(r.Context(), nil)
			if err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": "begin tx: " + err.Error()})
				return
			}
			defer tx.Rollback()

			if _, err := tx.ExecContext(r.Context(), `UPDATE lakehouse_metric SET
				name=$2, display_name=$3, description=$4, object_id=$5, level=$6,
				canonical_metric=$7, canonical_filters=$8::jsonb, auto_group_by=$9,
				replace_group_by=COALESCE($10, replace_group_by),
				default_order_by_label=NULLIF($11,''), default_order_by_dir=NULLIF($12,''),
				default_limit=$13,
				pivot_on=NULLIF($14,''), pivot_values=$15, pivot_column_labels=$16,
				pivot_total_label=COALESCE(NULLIF($17,''),'Total'),
				pivot_percent_axis=$18, pivot_percent_scope=$19, pivot_percent_suffix=$20,
				pivot_with_percent=COALESCE($21, pivot_with_percent),
				pivot_append_grand_total=COALESCE($22, pivot_append_grand_total),
				parameters=$23::jsonb, plan=$24::jsonb,
				response_template=$25, priority=$26, mark=COALESCE($27, mark),
				query_sql=$28, extra=COALESCE($29::jsonb, extra), updated_at=now()
				WHERE id=$1`,
				id, StrVal(body, "name"), StrVal(body, "displayName"), StrVal(body, "description"),
				NilIfEmpty(objIDForUpdate), level,
				canonicalMetric, string(filtersJSON), StringsToPgArray(body, "autoGroupBy"),
				optBoolPtr(body, "replaceGroupBy"),
				StrVal(body, "defaultOrderByLabel"), StrVal(body, "defaultOrderByDir"),
				optIntPtr(body, "defaultLimit"),
				NilIfEmpty(StrVal(body, "pivotOn")),
				StringsToPgArray(body, "pivotValues"), StringsToPgArray(body, "pivotColumnLabels"),
				StrVal(body, "pivotTotalLabel"),
				pivotPercentAxis, pivotPercentScope, percentSuffix,
				optBoolPtr(body, "pivotWithPercent"), optBoolPtr(body, "pivotAppendGrandTotal"),
				string(paramsJSON), planArg,
				StrVal(body, "responseTemplate"), intValDefault(body, "priority", 0), optBoolPtr(body, "mark"),
				NilIfEmpty(StrVal(body, "querySql")),
				extraArg,
			); err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}

			if hasTriggers {
				var objID, projID string
				if err := tx.QueryRowContext(r.Context(),
					`SELECT object_id, project_id FROM lakehouse_metric WHERE id=$1`, id,
				).Scan(&objID, &projID); err != nil {
					w.WriteHeader(500)
					JsonResp(w, M{"error": "fetch metric anchors: " + err.Error()})
					return
				}
				if err := ontology.UpdateMetricTriggers(r.Context(), tx, projID, id, objID, triggers); err != nil {
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
			db.Exec(`DELETE FROM lakehouse_metric WHERE id=$1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		// GET single
		row := db.QueryRow(lakehouseMetricListSQL+` WHERE m.id=$1`, id)
		item, err := scanLakehouseMetric(row)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		JsonResp(w, item)
	}
}

// lakehouseMetricListSQL is the shared SELECT for list + getByID.
const lakehouseMetricListSQL = `
	SELECT m.id, m.project_id, m.object_id,
	       m.name, COALESCE(m.display_name,''), COALESCE(m.description,''),
	       COALESCE(m.level,'simple'),
	       COALESCE(m.canonical_metric,''),
	       COALESCE(m.canonical_filters::text,'[]'),
	       COALESCE(m.auto_group_by, '{}'::text[]),
	       COALESCE(m.replace_group_by, false),
	       COALESCE(m.default_order_by_label,''),
	       COALESCE(m.default_order_by_dir,''),
	       m.default_limit,
	       COALESCE(m.pivot_on,''),
	       COALESCE(m.pivot_values, '{}'::text[]),
	       COALESCE(m.pivot_column_labels, '{}'::text[]),
	       COALESCE(m.pivot_total_label,'Total'),
	       COALESCE(m.pivot_percent_axis,'row'),
	       COALESCE(m.pivot_percent_scope,'filtered'),
	       COALESCE(m.pivot_percent_suffix,'占比'),
	       COALESCE(m.pivot_with_percent, false),
	       COALESCE(m.pivot_append_grand_total, false),
	       COALESCE(m.parameters::text,'[]'),
	       COALESCE(m.plan::text,''),
	       COALESCE(m.query_sql,''),
	       COALESCE(m.response_template,''),
	       COALESCE(m.priority, 0), m.mark, m.created_at, m.updated_at,
	       COALESCE(m.extra::text,'{}'),
	       COALESCE(o.name,'') AS object_name,
	       COALESCE((SELECT array_agg(lk.keyword ORDER BY lk.keyword)
	                 FROM lakehouse_keyword lk WHERE lk.metric_id = m.id), '{}'::text[]) AS trigger_keywords
	FROM lakehouse_metric m
	LEFT JOIN ont_object_type o ON m.object_id = o.id`

func scanLakehouseMetric(row rowScanner) (M, error) {
	var id, projectID, objectID, name, displayName, description, level string
	var metric, filtersJSON, defOrderLabel, defOrderDir string
	var pivotOn, pivotTotalLabel, pivotPercentAxis, pivotPercentScope, pivotPercentSuffix string
	var paramsJSON, planJSON, querySql, extraJSON, respTpl, objName string
	var autoGB, pivotVals, pivotLabels, triggerKw pq.StringArray
	var prio int
	var defLimit sql.NullInt64
	var mark, replaceGB, pivotWithPercent, pivotAppendGrandTotal bool
	var ca, ua time.Time
	if err := row.Scan(&id, &projectID, &objectID,
		&name, &displayName, &description, &level,
		&metric, &filtersJSON, &autoGB, &replaceGB,
		&defOrderLabel, &defOrderDir, &defLimit,
		&pivotOn, &pivotVals, &pivotLabels,
		&pivotTotalLabel, &pivotPercentAxis, &pivotPercentScope, &pivotPercentSuffix,
		&pivotWithPercent, &pivotAppendGrandTotal,
		&paramsJSON, &planJSON, &querySql,
		&respTpl, &prio, &mark, &ca, &ua, &extraJSON, &objName, &triggerKw); err != nil {
		return nil, err
	}
	var filters interface{}
	if filtersJSON != "" {
		_ = json.Unmarshal([]byte(filtersJSON), &filters)
	}
	if filters == nil {
		filters = []interface{}{}
	}
	var params interface{}
	if paramsJSON != "" {
		_ = json.Unmarshal([]byte(paramsJSON), &params)
	}
	if params == nil {
		params = []interface{}{}
	}
	var plan interface{}
	if planJSON != "" {
		_ = json.Unmarshal([]byte(planJSON), &plan)
	}
	odIds := []string{}
	if extraJSON != "" {
		var ex struct {
			OdIds []string `json:"odIds"`
		}
		if json.Unmarshal([]byte(extraJSON), &ex) == nil && ex.OdIds != nil {
			odIds = ex.OdIds
		}
	}
	out := M{
		"id": id, "projectId": projectID,
		"objectId": objectID, "objectName": objName, "odIds": odIds,
		"name": name, "displayName": displayName, "description": description,
		"level":                 level,
		"canonicalMetric":       metric,
		"canonicalFilters":      filters,
		"autoGroupBy":           sliceOrEmpty(autoGB),
		"replaceGroupBy":        replaceGB,
		"defaultOrderByLabel":   defOrderLabel,
		"defaultOrderByDir":     defOrderDir,
		"pivotOn":               pivotOn,
		"pivotValues":           sliceOrEmpty(pivotVals),
		"pivotColumnLabels":     sliceOrEmpty(pivotLabels),
		"pivotTotalLabel":       pivotTotalLabel,
		"pivotPercentAxis":      pivotPercentAxis,
		"pivotPercentScope":     pivotPercentScope,
		"pivotPercentSuffix":    pivotPercentSuffix,
		"pivotWithPercent":      pivotWithPercent,
		"pivotAppendGrandTotal": pivotAppendGrandTotal,
		"parameters":            params,
		"plan":                  plan,
		"querySql":              querySql,
		"responseTemplate":      respTpl,
		"priority":              prio,
		"mark":                  mark,
		"triggerKeywords":       sliceOrEmpty(triggerKw),
		"createdAt":             ca.Format(time.RFC3339),
		"updatedAt":             ua.Format(time.RFC3339),
	}
	if defLimit.Valid {
		out["defaultLimit"] = int(defLimit.Int64)
	}
	return out, nil
}

// ── small body helpers (names distinct from handler_intent.go) ──────────────

func sliceOrEmpty(a pq.StringArray) []string {
	s := []string(a)
	if s == nil {
		return []string{}
	}
	return s
}

func derefBool(p *bool) bool { return p != nil && *p }

func optIntPtr(body M, key string) *int {
	v, ok := body[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		x := int(n)
		return &x
	case int:
		return &n
	case int64:
		x := int(n)
		return &x
	}
	return nil
}

// jsonObjOrEmpty marshals a JSON object/array value to a string; nil/null → "".
func jsonObjOrEmpty(v interface{}) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return ""
	}
	return string(b)
}

// jsonObjOrNil returns the marshalled JSON string or nil (for $N::jsonb NULL).
func jsonObjOrNil(v interface{}) interface{} {
	s := jsonObjOrEmpty(v)
	if s == "" {
		return nil
	}
	return s
}
