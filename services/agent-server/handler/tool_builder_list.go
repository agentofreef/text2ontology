// Builder list_* tools — read-only queries used by the OD Builder Agent to
// inspect existing ontology entities (OD / Intent / Link) during interview and
// propose phases. Complements the CREATE tools in tool_builder_propose.go.
//
// Three exported functions:
//   builderToolListOds     — replaces list_existing_ods with filter support
//   builderToolListIntents — new; queries lakehouse_metric_intent + keywords
//   builderToolListLinks   — new; queries ont_link_type with object names
//
// All functions are pure SELECT-side and never write the database.
package handler

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/lib/pq"

	. "github.com/lakehouse2ontology/httputil"
)

// builderToolListOds returns ODs for the project with optional mark/kind/name
// filters. For each OD its properties are fetched in a second query.
//
// Args (all optional):
//   markFilter  "active" (default) | "pending" | "all"
//   kindFilter  "entity" | "event" | "attribute"
//   searchName  substring match (ILIKE) on OD name
func builderToolListOds(db *sql.DB, projectID string, args map[string]interface{}) M {
	if !IsValidUUID(projectID) {
		return M{"error": "invalid projectID"}
	}

	markFilter, _ := args["markFilter"].(string)
	if markFilter == "" {
		markFilter = "active"
	}
	kindFilter, _ := args["kindFilter"].(string)
	searchName, _ := args["searchName"].(string)

	// Build WHERE clauses and args slice.
	params := []interface{}{projectID}
	clauses := []string{"project_id = $1"}

	switch markFilter {
	case "active":
		clauses = append(clauses, "mark = true")
	case "pending":
		clauses = append(clauses, "mark = false AND COALESCE(origin,'') = 'builder'")
	case "all":
		// no additional clause
	}

	if kindFilter != "" {
		params = append(params, kindFilter)
		clauses = append(clauses, buildPlaceholder("kind = $", len(params)))
	}
	if searchName != "" {
		params = append(params, "%"+strings.ToLower(searchName)+"%")
		clauses = append(clauses, buildPlaceholder("LOWER(name) LIKE $", len(params)))
	}

	where := strings.Join(clauses, " AND ")

	// ont_object_type doesn't have an origin column in the current schema —
	// origin is stored in source_config JSONB (plan Gap-5). We read source_config
	// as text and expose it, but filter by COALESCE(origin,'') reads from a
	// computed expression. Adjust: the plan stores provenance in source_config,
	// not a dedicated origin column. We keep the filter safe by using
	// source_config->>'origin' for the pending clause.
	pendingOriginWhere := strings.Replace(where,
		"COALESCE(origin,'') = 'builder'",
		"source_config->>'origin' = 'builder'", 1)

	rows, err := db.Query(`
		SELECT id, name, COALESCE(display_name,''), kind, COALESCE(description,''),
		       COALESCE(source_table,''), COALESCE(semantic_sql,''),
		       COALESCE(canonical_query,''),
		       COALESCE(validated_at::text,''), mark,
		       COALESCE(source_config->>'origin','')
		FROM ont_object_type
		WHERE `+pendingOriginWhere+`
		ORDER BY mark DESC, name`, params...)
	if err != nil {
		return M{"error": "list ods failed: " + err.Error()}
	}
	defer rows.Close()

	var ods []M
	for rows.Next() {
		var id, name, displayName, kind, desc, sourceTable, semSQL string
		var canonicalQuery, validatedAt, origin string
		var mark bool
		if err := rows.Scan(&id, &name, &displayName, &kind, &desc, &sourceTable,
			&semSQL, &canonicalQuery, &validatedAt, &mark, &origin); err != nil {
			continue
		}

		preview := semSQL
		if len(preview) > 200 {
			preview = preview[:200]
		}

		props := fetchOdProperties(db, id)

		ods = append(ods, M{
			"id":                  id,
			"name":                name,
			"displayName":         displayName,
			"kind":                kind,
			"description":         desc,
			"sourceTable":         sourceTable,
			"semanticSqlPreview":  preview,
			"semanticSqlFull":     semSQL,
			"canonicalQuery":      canonicalQuery,
			"validatedAt":         validatedAt,
			"mark":                mark,
			"origin":              origin,
			"properties":          props,
		})
	}
	if ods == nil {
		ods = []M{}
	}

	appliedFilters := M{
		"markFilter": markFilter,
	}
	if kindFilter != "" {
		appliedFilters["kindFilter"] = kindFilter
	}
	if searchName != "" {
		appliedFilters["searchName"] = searchName
	}

	return M{
		"ods":            ods,
		"totalCount":     len(ods),
		"appliedFilters": appliedFilters,
	}
}

// fetchOdProperties fetches all properties for a given object_type_id, ordered
// by name. Returns empty slice on error.
func fetchOdProperties(db *sql.DB, odID string) []M {
	propRows, err := db.Query(`
		SELECT id, name, COALESCE(data_type,''), COALESCE(source_column,''),
		       is_filterable, is_groupable, is_machine_code, mark
		FROM ont_property
		WHERE object_type_id = $1
		ORDER BY name`, odID)
	if err != nil {
		return []M{}
	}
	defer propRows.Close()

	var props []M
	for propRows.Next() {
		var pid, pname, dtype, scol string
		var isFilterable, isGroupable, isMachineCode, pmark bool
		if err := propRows.Scan(&pid, &pname, &dtype, &scol,
			&isFilterable, &isGroupable, &isMachineCode, &pmark); err != nil {
			continue
		}
		props = append(props, M{
			"id":            pid,
			"name":          pname,
			"dataType":      dtype,
			"sourceColumn":  scol,
			"isFilterable":  isFilterable,
			"isGroupable":   isGroupable,
			"isMachineCode": isMachineCode,
			"mark":          pmark,
		})
	}
	if props == nil {
		props = []M{}
	}
	return props
}

// builderToolListIntents returns Metric Intents for the project with optional
// mark/objectId/name filters. Trigger keywords are fetched for active intents.
//
// Args (all optional):
//   markFilter  "active" (default) | "pending" | "all"
//   objectId    UUID — filter to intents bound to this OD
//   searchName  substring match (ILIKE) on intent name
func builderToolListIntents(db *sql.DB, projectID string, args map[string]interface{}) M {
	if !IsValidUUID(projectID) {
		return M{"error": "invalid projectID"}
	}

	markFilter, _ := args["markFilter"].(string)
	if markFilter == "" {
		markFilter = "active"
	}
	objectID, _ := args["objectId"].(string)
	searchName, _ := args["searchName"].(string)

	params := []interface{}{projectID}
	clauses := []string{"i.project_id = $1"}

	switch markFilter {
	case "active":
		clauses = append(clauses, "i.mark = true")
	case "pending":
		clauses = append(clauses, "i.mark = false")
	case "all":
		// no additional clause
	}

	if objectID != "" {
		if !IsValidUUID(objectID) {
			return M{"error": "invalid objectId"}
		}
		params = append(params, objectID)
		clauses = append(clauses, buildPlaceholder("i.object_id = $", len(params)))
	}
	if searchName != "" {
		params = append(params, "%"+strings.ToLower(searchName)+"%")
		clauses = append(clauses, buildPlaceholder("LOWER(i.name) LIKE $", len(params)))
	}

	where := strings.Join(clauses, " AND ")

	rows, err := db.Query(`
		SELECT i.id, i.name, i.object_id, COALESCE(o.name,'') AS object_name,
		       i.canonical_metric,
		       COALESCE(i.canonical_filters::text,'[]'),
		       COALESCE(i.auto_group_by, '{}'),
		       COALESCE(i.pivot_on,''), COALESCE(i.pivot_values, '{}'),
		       COALESCE(i.pivot_column_labels, '{}'),
		       COALESCE(i.pivot_total_label,'Total'),
		       COALESCE(i.pivot_with_percent, false),
		       COALESCE(i.pivot_append_grand_total, false),
		       COALESCE(i.priority, 0), i.mark
		FROM lakehouse_metric_intent i
		LEFT JOIN ont_object_type o ON o.id = i.object_id
		WHERE `+where+`
		ORDER BY i.mark DESC, i.priority DESC, i.name`, params...)
	if err != nil {
		return M{"error": "list intents failed: " + err.Error()}
	}
	defer rows.Close()

	var intents []M
	for rows.Next() {
		var id, name, objectId, objectName, canonicalMetric, canonicalFiltersRaw string
		var pivotOn, pivotTotalLabel string
		var autoGroupBy, pivotValues, pivotColumnLabels []string
		var pivotWithPercent, pivotAppendGrandTotal, mark bool
		var priority int

		if err := rows.Scan(
			&id, &name, &objectId, &objectName, &canonicalMetric,
			&canonicalFiltersRaw, pq.Array(&autoGroupBy),
			&pivotOn, pq.Array(&pivotValues), pq.Array(&pivotColumnLabels),
			&pivotTotalLabel, &pivotWithPercent, &pivotAppendGrandTotal,
			&priority, &mark,
		); err != nil {
			continue
		}

		// Parse canonical_filters JSONB text into interface{} for clean JSON output.
		var canonicalFilters interface{}
		if err := json.Unmarshal([]byte(canonicalFiltersRaw), &canonicalFilters); err != nil {
			canonicalFilters = []interface{}{}
		}

		if autoGroupBy == nil {
			autoGroupBy = []string{}
		}
		if pivotValues == nil {
			pivotValues = []string{}
		}
		if pivotColumnLabels == nil {
			pivotColumnLabels = []string{}
		}

		// Fetch trigger keywords only for active (mark=true) intents.
		// Pending intents have no keyword rows yet (lakehouse_keyword has no
		// mark column; rows would leak into recall before activation).
		var triggerKeywords []string
		if mark {
			triggerKeywords = fetchIntentKeywords(db, id, projectID)
		} else {
			triggerKeywords = []string{}
		}

		// `orphan` flags an active intent with 0 trigger keywords — recall
		// can never match it. The analyst agent uses this to surface "fix
		// these intents" recommendations to the user.
		isOrphan := mark && len(triggerKeywords) == 0

		intents = append(intents, M{
			"id":                   id,
			"name":                 name,
			"objectId":             objectId,
			"objectName":           objectName,
			"canonicalMetric":      canonicalMetric,
			"canonicalFilters":     canonicalFilters,
			"autoGroupBy":          autoGroupBy,
			"pivotOn":              pivotOn,
			"pivotValues":          pivotValues,
			"pivotColumnLabels":    pivotColumnLabels,
			"pivotTotalLabel":      pivotTotalLabel,
			"pivotWithPercent":     pivotWithPercent,
			"pivotAppendGrandTotal": pivotAppendGrandTotal,
			"priority":             priority,
			"mark":                 mark,
			"triggerKeywords":      triggerKeywords,
			"triggerCount":         len(triggerKeywords),
			"orphan":               isOrphan,
		})
	}
	if intents == nil {
		intents = []M{}
	}

	// Aggregate orphan count — surfaced at top level so the agent can spot
	// the issue at a glance without iterating every row. An orphan is an
	// active intent with no trigger keywords; recall cannot see it.
	orphanCount := 0
	for _, in := range intents {
		if v, ok := in["orphan"].(bool); ok && v {
			orphanCount++
		}
	}

	return M{
		"intents":     intents,
		"totalCount":  len(intents),
		"orphanCount": orphanCount,
	}
}

// fetchIntentKeywords returns the trigger keywords for a given intent.
func fetchIntentKeywords(db *sql.DB, intentID, projectID string) []string {
	rows, err := db.Query(`
		SELECT keyword FROM lakehouse_keyword
		WHERE metric_intent_id = $1 AND project_id = $2
		ORDER BY keyword`, intentID, projectID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	var keywords []string
	for rows.Next() {
		var kw string
		if err := rows.Scan(&kw); err != nil {
			continue
		}
		keywords = append(keywords, kw)
	}
	if keywords == nil {
		keywords = []string{}
	}
	return keywords
}

// builderToolListLinks returns Link types for the project with optional
// mark/objectId filters. Object names are resolved via LEFT JOIN.
//
// Property anchor resolution (from_property_id / to_property_id) is best-effort
// via ont_causality with relation_type='join_key'. If the causality rows don't
// exist the fields are left as empty strings.
//
// Args (all optional):
//   markFilter  "active" (default) | "pending" | "all"
//   objectId    UUID — filter links where from_object_id OR to_object_id matches
func builderToolListLinks(db *sql.DB, projectID string, args map[string]interface{}) M {
	if !IsValidUUID(projectID) {
		return M{"error": "invalid projectID"}
	}

	markFilter, _ := args["markFilter"].(string)
	if markFilter == "" {
		markFilter = "active"
	}
	objectID, _ := args["objectId"].(string)

	params := []interface{}{projectID}
	clauses := []string{"l.project_id = $1"}

	switch markFilter {
	case "active":
		clauses = append(clauses, "l.mark = true")
	case "pending":
		clauses = append(clauses, "l.mark = false")
	case "all":
		// no additional clause
	}

	if objectID != "" {
		if !IsValidUUID(objectID) {
			return M{"error": "invalid objectId"}
		}
		params = append(params, objectID)
		n := len(params)
		clauses = append(clauses, "(l.from_object_id = $"+itoa(n)+" OR l.to_object_id = $"+itoa(n)+")")
	}

	where := strings.Join(clauses, " AND ")

	rows, err := db.Query(`
		SELECT l.id, COALESCE(l.link_name,''), COALESCE(l.description,''),
		       l.from_object_id, l.to_object_id,
		       COALESCE(fo.name,'') AS from_name,
		       COALESCE(to_obj.name,'') AS to_name,
		       COALESCE(l.fk_column,''), COALESCE(l.cardinality,''), l.mark
		FROM ont_link_type l
		LEFT JOIN ont_object_type fo     ON fo.id     = l.from_object_id
		LEFT JOIN ont_object_type to_obj ON to_obj.id = l.to_object_id
		WHERE `+where+`
		ORDER BY l.mark DESC, fo.name, to_obj.name`, params...)
	if err != nil {
		return M{"error": "list links failed: " + err.Error()}
	}
	defer rows.Close()

	var links []M
	for rows.Next() {
		var id, name, desc, fromObjID, toObjID, fromName, toName string
		var fkCol, cardinality string
		var mark bool
		if err := rows.Scan(&id, &name, &desc, &fromObjID, &toObjID,
			&fromName, &toName, &fkCol, &cardinality, &mark); err != nil {
			continue
		}

		fromPropID, toPropID := resolveJoinKeyProperties(db, projectID, fromObjID, toObjID)

		links = append(links, M{
			"id":             id,
			"name":           name,
			"description":    desc,
			"fromObjectId":   fromObjID,
			"fromObjectName": fromName,
			"toObjectId":     toObjID,
			"toObjectName":   toName,
			"fromPropertyId": fromPropID,
			"toPropertyId":   toPropID,
			"fkColumn":       fkCol,
			"cardinality":    cardinality,
			"mark":           mark,
		})
	}
	if links == nil {
		links = []M{}
	}

	return M{
		"links":      links,
		"totalCount": len(links),
	}
}

// resolveJoinKeyProperties attempts to find property-anchored ont_causality rows
// with relation_type='join_key' that connect properties belonging to fromObjID
// and toObjID. Returns empty strings if no such causality row exists.
//
// This is best-effort: causality rows may not have been created for all links.
func resolveJoinKeyProperties(db *sql.DB, projectID, fromObjID, toObjID string) (fromPropID, toPropID string) {
	// Find a causality row whose from_knowledge references a property on
	// fromObjID and whose to_knowledge references a property on toObjID.
	err := db.QueryRow(`
		SELECT k1.anchor_id, k2.anchor_id
		FROM ont_causality c
		JOIN ont_knowledge k1 ON k1.id = c.from_knowledge_id
		JOIN ont_knowledge k2 ON k2.id = c.to_knowledge_id
		JOIN ont_property  p1 ON p1.id = k1.anchor_id AND p1.object_type_id = $2
		JOIN ont_property  p2 ON p2.id = k2.anchor_id AND p2.object_type_id = $3
		WHERE c.project_id = $1
		  AND c.relation_type = 'join_key'
		  AND k1.anchor_type = 'property'
		  AND k2.anchor_type = 'property'
		LIMIT 1`, projectID, fromObjID, toObjID).Scan(&fromPropID, &toPropID)
	if err != nil {
		// No row or query error — leave empty strings (documented best-effort).
		return "", ""
	}
	return fromPropID, toPropID
}

// buildPlaceholder appends the placeholder index to the prefix.
// E.g. buildPlaceholder("kind = $", 3) → "kind = $3"
func buildPlaceholder(prefix string, idx int) string {
	return prefix + itoa(idx)
}

// itoa converts an int to its decimal string representation without importing
// strconv (which is already available in the Go standard library but avoids
// adding a new import just for this helper).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n >= 10 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	pos--
	buf[pos] = byte('0' + n)
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

