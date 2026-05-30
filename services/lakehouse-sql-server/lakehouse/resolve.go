package lakehouse

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// aliasTailRe extracts a trailing `AS <ident>` from a metric expression so the
// author's chosen output column name wins over the engine's default
// `Total_<col>` convention. Quoted identifiers ("X") and bare identifiers
// (NAME) are both accepted; the captured alias is returned WITHOUT the quotes.
// Two patterns are needed because PostgreSQL allows the `AS` keyword to be glued
// directly onto a `)` or `"` boundary — e.g. `sum("X")as "alias"` with no space.
// Go's regexp (RE2) lacks lookbehind, so we can't express "require whitespace
// before `as` UNLESS preceded by ) or \"" in one pass. Instead:
//
//   - aliasTailReTight matches when the body ends in `)` or `"` (space optional).
//   - aliasTailReLoose matches when the body ends in an identifier char (space
//     REQUIRED to avoid eating `column_as` → `column_`).
//
// stripMetricAlias tries tight first because it's the narrower (paren/quote)
// shape; if it doesn't match we fall back to the loose form.
var (
	aliasTailReTight = regexp.MustCompile(`(?is)^(.+?[)"])\s*as\s+("?[A-Za-z_][A-Za-z0-9_]*"?)\s*$`)
	aliasTailReLoose = regexp.MustCompile(`(?is)^(.+?)\s+as\s+("?[A-Za-z_][A-Za-z0-9_]*"?)\s*$`)
)

// stripMetricAlias peels off a trailing `AS <ident>` from a metric expression.
// Returns (body, alias). When no alias is present, returns (s, "").
//   "sum(\"X\") AS qty"   → ("sum(\"X\")", "qty")
//   "sum(\"X\")as \"qty\"" → ("sum(\"X\")", "qty")   ← Postgres allows the gap
//   "sum(\"X\")"           → ("sum(\"X\")", "")
func stripMetricAlias(s string) (body, alias string) {
	s = strings.TrimSpace(s)
	if m := aliasTailReTight.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), strings.Trim(m[2], `"`)
	}
	if m := aliasTailReLoose.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), strings.Trim(m[2], `"`)
	}
	return s, ""
}

// metricLabel honors an author-supplied alias when present; otherwise falls
// back to the engine's default `Total_<col>` convention.
func metricLabel(authorAlias, propName string) string {
	if a := strings.TrimSpace(authorAlias); a != "" {
		return a
	}
	return "Total_" + propName
}

// countRowsKeywords maps metric names that should produce COUNT(*).
// Bug #2 fix: "sum" and "total" REMOVED — they are aggregation function names, not COUNT synonyms.
var countRowsKeywords = map[string]bool{
	"count":     true,
	"countrows": true,
	"cnt":       true,
	"数量":        true,
	"订单数":       true,
}

// stripAggWrapperWithFunc returns the inner name and the detected aggregation function.
// "sum(Order_Quantity)" → ("Order_Quantity", "SUM")
// "Order_Quantity"      → ("Order_Quantity", "")
// "distinctcount(Customer)" → ("Customer", "DISTINCTCOUNT")
func stripAggWrapperWithFunc(s string) (inner string, fn string) {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, entry := range []struct {
		prefix string
		fn     string
	}{
		{"sum(", "SUM"},
		{"count(", "COUNT"},
		{"avg(", "AVG"},
		{"average(", "AVG"},
		{"max(", "MAX"},
		{"min(", "MIN"},
		{"distinctcount(", "DISTINCTCOUNT"},
	} {
		if strings.HasPrefix(lower, entry.prefix) && strings.HasSuffix(s, ")") {
			return strings.TrimSpace(s[len(entry.prefix) : len(s)-1]), entry.fn
		}
	}
	return s, ""
}

// splitCommaMetric splits a comma-separated metric string into trimmed, non-empty parts.
func splitCommaMetric(metric string) []string {
	parts := strings.Split(metric, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// augmentObjectsFromRefs returns spec.Objects extended with any Od referenced
// by a filter / orderBy / groupBy prop prefix ("Od.prop") that the caller did
// not explicitly declare. Referencing a linked Od's property inherently means
// that Od must join into the query — the caller should not have to list it
// twice. Unknown prefixes are harmless: loadLakehouseObjects silently skips
// Ods it cannot resolve.
func augmentObjectsFromRefs(objects []string, filters []smartquery.FilterItem, orderBy []smartquery.OrderByItem, groupBy []string) []string {
	seen := make(map[string]bool, len(objects))
	for _, o := range objects {
		seen[strings.ToLower(strings.TrimSpace(o))] = true
	}
	consider := func(prop string) {
		od, _ := smartquery.StripObjectPrefix(prop)
		od = strings.TrimSpace(od)
		if od == "" {
			return
		}
		key := strings.ToLower(od)
		if !seen[key] {
			seen[key] = true
			objects = append(objects, od)
		}
	}
	for _, f := range filters {
		consider(f.Prop)
	}
	for _, ob := range orderBy {
		consider(ob.Prop)
	}
	for _, g := range groupBy {
		consider(g)
	}
	return objects
}

// ResolveQuery resolves a QuerySpec into a fully bound ResolvedLakehouseQuery.
// Loads properties from ont_property + ont_object_type, validates canonical_query exists.
func ResolveQuery(db *sql.DB, spec smartquery.QuerySpec, corrector *LakehouseCorrector) (*ResolvedLakehouseQuery, error) {
	rq := &ResolvedLakehouseQuery{
		ProjectID:      spec.ProjectID,
		Limit:          spec.Limit,
		Derived:        spec.Derived,
		MetricFilter:   spec.MetricFilter,
		DenseGroups:    spec.DenseGroups,
		AddShareColumn: spec.AddShareColumn,
		ShareLabel:     spec.ShareLabel,
	}
	if rq.Limit <= 0 {
		rq.Limit = 1000
	}

	// 0. Auto-include Ods referenced by filter / orderBy / groupBy prop
	//    prefixes but missing from spec.Objects. Without this, a filter on a
	//    linked Od (e.g. "ORDER_ENT.OrderDate" while objects=["ORDER_DETAIL"])
	//    takes the single-Od path, mis-qualifies the column onto the primary
	//    Od, and fails with "column does not exist". The OD layer must not
	//    require the caller to manually list every referenced Od.
	spec.Objects = augmentObjectsFromRefs(spec.Objects, spec.Filters, spec.OrderBy, spec.GroupBy)

	// 1. Load objects + properties.
	objects, allProps, err := loadLakehouseObjects(db, spec.ProjectID, spec.Objects)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, &smartquery.ResolveError{
			Code:    "OBJECT_NOT_FOUND",
			Message: fmt.Sprintf("未找到对象 %v 的属性。请确认对象名称拼写正确。", spec.Objects),
			Detail:  map[string]any{"objects": spec.Objects},
		}
	}

	// Validate all objects have canonical_query.
	for _, obj := range objects {
		if obj.CanonicalQuery == "" {
			return nil, &smartquery.ResolveError{
				Code:    "NO_CANONICAL_QUERY",
				Message: fmt.Sprintf("对象 %q 尚未配置 SQL 查询映射 (canonical_query)。请先在 Lakehouse Objects 页面完成 SQL 验证和固化。", obj.Name),
				Detail:  map[string]any{"object": obj.Name},
			}
		}
	}

	rq.Objects = objects
	rq.AllProps = allProps

	// 2. Resolve metric — multi-metric support via splitCommaMetric.
	if spec.Metric != "" {
		metricParts := splitCommaMetric(spec.Metric)
		for _, part := range metricParts {
			aggs, err := resolveMetricLakehouse(db, spec.ProjectID, part, allProps, spec.Objects)
			if err != nil {
				// Metric-as-groupBy fallback: non-numeric, no explicit agg function.
				if re, ok := err.(*smartquery.ResolveError); ok && re.Code == "METRIC_AS_GROUPBY" {
					pi := re.Detail["prop"].(smartquery.PropertyInfo)
					propName := re.Detail["propName"].(string)
					_, granularity, outputLabel := smartquery.StripDateGranularity(propName)
					dup := false
					for _, g := range spec.GroupBy {
						_, gName := smartquery.StripObjectPrefix(g)
						gClean, _, _ := smartquery.StripDateGranularity(gName)
						if strings.EqualFold(gClean, pi.Name) {
							dup = true
							break
						}
					}
					if !dup {
						rq.GroupByCols = append(rq.GroupByCols, smartquery.ResolvedGroupBy{
							Prop: pi, Granularity: granularity, OutputLabel: outputLabel, OriginalToken: propName,
						})
					}
					continue
				}
				return nil, err
			}
			rq.Aggregates = append(rq.Aggregates, aggs...)
		}
	}

	// 3. Resolve groupBy.
	// Try the FULL property name first. Only fall back to StripDateGranularity if
	// the full name does not resolve. This protects real columns ending in
	// "_QUARTER" / "_YEAR" / "_MONTH" / "_DAY" / "_WEEK" (e.g. ORDER.FISCAL_QUARTER)
	// from being clobbered by the underscore-suffix granularity heuristic, which
	// would otherwise strip the suffix and route the resolver to an unrelated
	// property in another Od via Tier-3 fuzzy global matching.
	for _, g := range spec.GroupBy {
		odName, propName := smartquery.StripObjectPrefix(g)
		if propName == "" {
			propName = g
		}
		// OD-qualified ("INGREDIENT.name") → bind to THAT OD, not whatever OD
		// the global resolver happens to pick for a shared column name.
		if odName != "" {
			if pi, err := resolvePropertyInOD(db, spec.ProjectID, odName, propName, allProps); err == nil {
				rq.GroupByCols = append(rq.GroupByCols, smartquery.ResolvedGroupBy{
					Prop: pi, Granularity: "", OutputLabel: pi.Name, OriginalToken: g,
				})
				continue
			}
		}
		if pi, err := resolvePropertyLakehouse(db, spec.ProjectID, propName, allProps); err == nil {
			rq.GroupByCols = append(rq.GroupByCols, smartquery.ResolvedGroupBy{
				Prop: pi, Granularity: "", OutputLabel: pi.Name, OriginalToken: g,
			})
			continue
		}
		cleanName, granularity, outputLabel := smartquery.StripDateGranularity(propName)
		pi, err := resolvePropertyLakehouse(db, spec.ProjectID, cleanName, allProps)
		if err != nil {
			return nil, err
		}
		rq.GroupByCols = append(rq.GroupByCols, smartquery.ResolvedGroupBy{
			Prop: pi, Granularity: granularity, OutputLabel: outputLabel, OriginalToken: g,
		})
	}

	// 4. Resolve filters (with keyword correction + structured tracking).
	for _, f := range spec.Filters {
		odName, propName := smartquery.StripObjectPrefix(f.Prop)
		if propName == "" {
			propName = f.Prop
		}
		var pi smartquery.PropertyInfo
		var err error
		// OD-qualified filter ("MENUITEM.name") → bind to THAT OD first.
		if odName != "" {
			pi, err = resolvePropertyInOD(db, spec.ProjectID, odName, propName, allProps)
		}
		if odName == "" || err != nil {
			pi, err = resolvePropertyLakehouse(db, spec.ProjectID, propName, allProps)
		}
		if err != nil {
			return nil, err
		}

		// Validate the filter operator. An unknown op (e.g. "like" when the
		// LLM reaches for SQL-native syntax) must be rejected here with a
		// clear, addressable error — otherwise it silently degrades to "="
		// in the v2 SQL builder's default branch and returns an empty result
		// with no diagnosable cause. Empty op is allowed (treated as "=").
		if f.Op != "" && !isValidFilterOp(f.Op) {
			return nil, &smartquery.ResolveError{
				Code:    "INVALID_FILTER_OP",
				Message: fmt.Sprintf("不支持的过滤操作符 %q。可用: =, <>, >, >=, <, <=, contains, not contains, starts with, ends with, like, not like, between, in, not in, is blank, is not blank", f.Op),
				Detail:  map[string]any{"prop": f.Prop, "op": f.Op},
			}
		}

		// Validate between format (ported from smartquery).
		if strings.EqualFold(strings.TrimSpace(f.Op), "between") {
			parts := strings.SplitN(f.Value, ",", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				return nil, &smartquery.ResolveError{
					Code:    "MALFORMED_FILTER_VALUE",
					Message: fmt.Sprintf("between 运算符的值必须为 'low,high' 格式，但收到: %q", f.Value),
					Detail:  map[string]any{"prop": f.Prop, "value": f.Value},
				}
			}
		}

		// Apply keyword correction for string equality filters.
		corrected := f.Value
		origValue := f.Value
		fuzzy := f.FuzzyMatch
		if f.Op == "=" && !fuzzy && !isNumericType(pi.DataType) && !isDateType(pi.DataType) && corrector != nil {
			correctedVal, status := corrector.Correct(spec.ProjectID, pi, f.Value)
			kc := smartquery.KeywordCorrection{
				Prop:      f.Prop,
				UserValue: f.Value,
				DBValue:   correctedVal,
				Status:    status,
			}
			if status == "machineCode_passthrough" || status == "machineCode_fuzzy" {
				fuzzy = true
				kc.DBValue = f.Value
			} else if correctedVal != "" && !strings.EqualFold(correctedVal, f.Value) {
				corrected = correctedVal
			}
			// Always track corrections for traceability (Bug #8 fix).
			rq.KeywordCorrections = append(rq.KeywordCorrections, kc)
			if status != "matched" && status != "no_match" {
				rq.Warnings = append(rq.Warnings, fmt.Sprintf("keyword correction: %s %q → %q (%s)", f.Prop, f.Value, kc.DBValue, status))
			}
		}

		// Deterministic period expansion: when a date/timestamp property is
		// filtered with a human period expression ("2025年12月", "本月",
		// "2025-12", "2025 Q4"), expand it into an explicit half-open range
		// [start, end). Without this the raw value becomes a text ILIKE
		// against a timestamp column and silently matches zero rows. See
		// period.go. Catches every path — Intent params, compose_query,
		// direct filters — because it lives at resolution, not param-bind.
		opLower := strings.ToLower(strings.TrimSpace(f.Op))
		if isDateType(pi.DataType) && (opLower == "" || opLower == "=" || opLower == "starts with") {
			if start, end, okP := expandPeriod(f.Value); okP {
				rq.FilterItems = append(rq.FilterItems,
					smartquery.ResolvedFilter{Prop: pi, Op: ">=", Value: start, OriginalValue: origValue},
					smartquery.ResolvedFilter{Prop: pi, Op: "<", Value: end, OriginalValue: origValue},
				)
				rq.Warnings = append(rq.Warnings, fmt.Sprintf(
					"period normalized: %s %q → [%s, %s)", f.Prop, f.Value, start, end))
				continue
			}
		}

		rq.FilterItems = append(rq.FilterItems, smartquery.ResolvedFilter{
			Prop: pi, Op: f.Op, Value: corrected, OriginalValue: origValue, FuzzyMatch: fuzzy,
		})
	}

	// 5. Resolve orderBy.
	for _, ob := range spec.OrderBy {
		propName := smartquery.StripAggWrapper(ob.Prop)
		_, propName = smartquery.StripObjectPrefix(propName)
		if propName == "" {
			propName = ob.Prop
		}
		dir := strings.ToUpper(ob.Dir)
		if dir != "ASC" && dir != "DESC" {
			dir = "DESC"
		}

		// Check aggregate label first (before any name mangling).
		aggIdx := matchAggregateLabel(propName, rq.Aggregates)

		// If no match, try stripping known Od name as underscore prefix.
		// e.g., "Order_Order_Quantity" → "Order_Quantity" when Od "Order" is in scope.
		if aggIdx < 0 {
			for _, obj := range rq.Objects {
				prefix := obj.Name + "_"
				if len(propName) > len(prefix) && strings.EqualFold(propName[:len(prefix)], prefix) {
					candidate := propName[len(prefix):]
					if idx := matchAggregateLabel(candidate, rq.Aggregates); idx >= 0 {
						aggIdx = idx
						propName = candidate
					}
					break
				}
			}
		}

		if aggIdx >= 0 {
			rq.OrderByCols = append(rq.OrderByCols, smartquery.ResolvedOrderBy{Kind: smartquery.OrderByAggregate, AggIndex: aggIdx, Dir: dir})
			continue
		}

		// Check if ordering by a groupBy column (including date granularity).
		// e.g., "Order_Receiving_Date:week" → matches groupBy with OutputLabel "Order_Receiving_Date_Week".
		cleanOB, _, _ := smartquery.StripDateGranularity(propName)
		gbMatched := false
		for _, gb := range rq.GroupByCols {
			if strings.EqualFold(cleanOB, gb.Prop.Name) || strings.EqualFold(propName, gb.OriginalToken) {
				label := gb.OutputLabel
				if label == "" {
					label = gb.Prop.Name
				}
				rq.OrderByCols = append(rq.OrderByCols, smartquery.ResolvedOrderBy{Kind: smartquery.OrderByCustomLabel, Label: label, Dir: dir})
				gbMatched = true
				break
			}
		}
		if gbMatched {
			continue
		}

		// Property match.
		pi, err := resolvePropertyLakehouse(db, spec.ProjectID, propName, allProps)
		if err != nil {
			// Fallback: if single aggregate, order by it.
			if len(rq.Aggregates) == 1 {
				rq.OrderByCols = append(rq.OrderByCols, smartquery.ResolvedOrderBy{Kind: smartquery.OrderByAggregate, AggIndex: 0, Dir: dir})
				continue
			}
			rq.Warnings = append(rq.Warnings, fmt.Sprintf("orderBy %q: %v, 已跳过", ob.Prop, err))
			continue
		}
		rq.OrderByCols = append(rq.OrderByCols, smartquery.ResolvedOrderBy{Kind: smartquery.OrderByProp, Prop: &pi, Dir: dir})
	}

	// Auto-add COUNT(*) if groupBy exists but no aggregates.
	if len(rq.Aggregates) == 0 && len(rq.GroupByCols) > 0 {
		rq.Aggregates = []smartquery.ResolvedAggregate{{Kind: smartquery.AggCountRows, Label: "Count"}}
	}

	// Final gate: fail fast if any *referenced* property has no matching real
	// column in its Od's canonical_query output (after case/whitespace/
	// underscore normalization). Unreferenced mismatches are tolerated —
	// they may simply reflect extra properties the user hasn't mapped yet.
	if err := validateAllPropsMapped(rq); err != nil {
		return nil, err
	}

	return rq, nil
}

// validateAllPropsMapped walks every property referenced by the resolved
// query and returns a ResolveError when one has no real column in its Od's
// canonical_query output. The error message lists the actual output columns
// so the user can reconcile property naming with the canonical_query.
//
// Called at the end of ResolveQuery so it sees the union of filters, groupBy
// columns, aggregates, and orderBy clauses. Aggregate kinds without a
// property target (COUNT(*), custom SQL) are skipped.
func validateAllPropsMapped(rq *ResolvedLakehouseQuery) error {
	unmatchedByOd := make(map[string]map[string]bool, len(rq.Objects))
	actualByOd := make(map[string][]string, len(rq.Objects))
	for _, o := range rq.Objects {
		if len(o.UnmatchedProps) > 0 {
			m := make(map[string]bool, len(o.UnmatchedProps))
			for _, p := range o.UnmatchedProps {
				m[p] = true
			}
			unmatchedByOd[o.Name] = m
		}
		if len(o.ActualColumns) > 0 {
			actualByOd[o.Name] = o.ActualColumns
		}
	}
	if len(unmatchedByOd) == 0 {
		return nil // fast path: everything aligned
	}

	check := func(pi smartquery.PropertyInfo) error {
		if pi.Name == "" || pi.ObjectName == "" {
			return nil
		}
		um, ok := unmatchedByOd[pi.ObjectName]
		if !ok || !um[pi.Name] {
			return nil
		}
		return &smartquery.ResolveError{
			Code: "COLUMN_NOT_IN_CANONICAL",
			Message: fmt.Sprintf(
				"对象 %q 的属性 %q 在 canonical_query 的真实输出列中找不到（已对大小写/空格/下划线做归一化后仍无匹配）。真实输出列: %v",
				pi.ObjectName, pi.Name, actualByOd[pi.ObjectName],
			),
			Detail: map[string]any{
				"object":        pi.ObjectName,
				"property":      pi.Name,
				"actualColumns": actualByOd[pi.ObjectName],
			},
		}
	}

	for _, f := range rq.FilterItems {
		if err := check(f.Prop); err != nil {
			return err
		}
	}
	for _, g := range rq.GroupByCols {
		if err := check(g.Prop); err != nil {
			return err
		}
	}
	for _, a := range rq.Aggregates {
		// Prop is nil for COUNT(*), custom SQL, and derived-ref aggregates —
		// those have no per-column lookup to validate.
		if a.Prop == nil {
			continue
		}
		if err := check(*a.Prop); err != nil {
			return err
		}
	}
	for _, ob := range rq.OrderByCols {
		if ob.Prop == nil {
			continue
		}
		if err := check(*ob.Prop); err != nil {
			return err
		}
	}
	return nil
}

// ── Object + Property Loading ──

// loadLakehouseObjects loads Od objects + properties from ont_object_type + ont_property.
func loadLakehouseObjects(db *sql.DB, projectID string, objectNames []string) ([]ObjectInfo, []smartquery.PropertyInfo, error) {
	if len(objectNames) == 0 {
		return nil, nil, nil
	}
	var objects []ObjectInfo
	var allProps []smartquery.PropertyInfo

	for _, objName := range objectNames {
		var objID, canonicalQuery string
		// Exact match first.
		db.QueryRow(`SELECT id, COALESCE(canonical_query,'') FROM ont_object_type
			WHERE project_id = $1 AND name = $2`, projectID, objName).Scan(&objID, &canonicalQuery)
		// Fuzzy fallback.
		if objID == "" {
			db.QueryRow(`SELECT id, COALESCE(canonical_query,'') FROM ont_object_type
				WHERE project_id = $1 AND (name ILIKE '%'||$2||'%' OR $2 ILIKE '%'||name||'%')
				ORDER BY LENGTH(name) ASC LIMIT 1`, projectID, objName).Scan(&objID, &canonicalQuery)
		}
		if objID == "" {
			continue
		}

		obj := ObjectInfo{ID: objID, Name: objName, CanonicalQuery: canonicalQuery}

		// Load properties.
		pRows, err := db.Query(`SELECT name, COALESCE(data_type,''), COALESCE(source_column,''), COALESCE(is_machine_code, false)
			FROM ont_property WHERE object_type_id = $1 ORDER BY name`, objID)
		if err != nil {
			return nil, nil, err
		}
		for pRows.Next() {
			var n, dt, sc string
			var mc bool
			pRows.Scan(&n, &dt, &sc, &mc)
			// In lakehouse, canonical_query already aliases physical columns → property names.
			// ColumnName = property Name (the alias), NOT source_column (the physical column).
			// source_column is stored for metadata/display only.
			pi := smartquery.PropertyInfo{
				Name:       n,
				DataType:   dt,
				TableName:  objName,
				ColumnName: n,
				ObjectName: objName,
				ObjectID:   objID,
			}
			obj.Props = append(obj.Props, pi)
			allProps = append(allProps, pi)
		}
		pRows.Close()

		// Probe canonical_query for its real output columns and rewrite it
		// with an outer SELECT that aliases each real column to the matching
		// property name (case/whitespace/underscore insensitive). This lets
		// SQL builder emit `"<od>"."<property_name>"` references without
		// caring about the raw physical column naming convention.
		// On probe failure, leave canonical_query untouched — the original
		// error will surface at execute time.
		applyCanonicalAlignment(db, &obj)

		objects = append(objects, obj)
	}
	return objects, allProps, nil
}

// applyCanonicalAlignment probes obj.CanonicalQuery, rewrites it to alias
// real columns to property names when necessary, and records the probe result
// on obj (ActualColumns/UnmatchedProps). It's a no-op when canonical_query is
// empty or the probe fails — in both cases obj is left unchanged.
func applyCanonicalAlignment(db *sql.DB, obj *ObjectInfo) {
	if obj.CanonicalQuery == "" || len(obj.Props) == 0 {
		return
	}
	actualCols, err := probeCanonicalColumns(db, obj.CanonicalQuery)
	if err != nil || len(actualCols) == 0 {
		return
	}
	propNames := make([]string, len(obj.Props))
	for i, p := range obj.Props {
		propNames[i] = p.Name
	}
	rewritten, unmatched := alignCanonicalQuery(obj.CanonicalQuery, propNames, actualCols)
	obj.CanonicalQuery = rewritten
	obj.ActualColumns = actualCols
	obj.UnmatchedProps = unmatched
}

// LoadSingleObject loads a single Od by name for intermediate JOIN path resolution.
func LoadSingleObject(db *sql.DB, projectID, objName string) (ObjectInfo, error) {
	var objID, canonicalQuery string
	db.QueryRow(`SELECT id, COALESCE(canonical_query,'') FROM ont_object_type
		WHERE project_id = $1 AND name = $2`, projectID, objName).Scan(&objID, &canonicalQuery)
	if objID == "" {
		return ObjectInfo{}, &smartquery.ResolveError{
			Code:    "OBJECT_NOT_FOUND",
			Message: fmt.Sprintf("JOIN 路径中的中间对象 %q 未找到", objName),
		}
	}
	obj := ObjectInfo{ID: objID, Name: objName, CanonicalQuery: canonicalQuery}

	pRows, err := db.Query(`SELECT name, COALESCE(data_type,''), COALESCE(source_column,''), COALESCE(is_machine_code, false)
		FROM ont_property WHERE object_type_id = $1 ORDER BY name`, objID)
	if err != nil {
		return ObjectInfo{}, err
	}
	for pRows.Next() {
		var n, dt, sc string
		var mc bool
		pRows.Scan(&n, &dt, &sc, &mc)
		obj.Props = append(obj.Props, smartquery.PropertyInfo{
			Name: n, DataType: dt, TableName: objName, ColumnName: n, ObjectName: objName, ObjectID: objID,
		})
	}
	pRows.Close()

	// Apply the same canonical_query alignment as loadLakehouseObjects so
	// JOIN-path intermediates honour the same column-name tolerance.
	applyCanonicalAlignment(db, &obj)

	return obj, nil
}

// ── Property Resolution (4-tier) ──

// resolvePropertyLakehouse resolves a property name using a 4-tier cascade:
// Tier 1: local exact (case-insensitive)
// Tier 2: global exact across all objects in version
// Tier 3: fuzzy substring with length guard (≤1.5x)
// Tier 4: hard error with available properties
// resolvePropertyInOD binds a property to a SPECIFIC OD, used when the caller's
// token was OD-qualified ("INGREDIENT.name"). Without this, a qualified column
// fell through to resolvePropertyLakehouse's global Tier-2 match, which does
// `ORDER BY LENGTH(ot.name) LIMIT 1` and silently bound to the shortest-named
// OD that happened to share the column name (e.g. INGREDIENT.name → MENUITEM.name).
// On a miss within the named OD it returns an error so the caller can fall back.
func resolvePropertyInOD(db *sql.DB, projectID, odName, propName string, localProps []smartquery.PropertyInfo) (smartquery.PropertyInfo, error) {
	// Tier 1: local exact (object + property), case-insensitive.
	for _, p := range localProps {
		if strings.EqualFold(p.ObjectName, odName) && strings.EqualFold(p.Name, propName) {
			return p, nil
		}
	}
	// Tier 2: DB exact, constrained to the named OD.
	var oID, oName, pName, pDT string
	row := db.QueryRow(`
		SELECT ot.id, ot.name, p.name, COALESCE(p.data_type,'')
		FROM ont_property p
		JOIN ont_object_type ot ON ot.id = p.object_type_id
		WHERE ot.project_id = $1 AND LOWER(ot.name) = LOWER($2) AND LOWER(p.name) = LOWER($3)
		LIMIT 1`, projectID, odName, propName)
	if err := row.Scan(&oID, &oName, &pName, &pDT); err == nil {
		return smartquery.PropertyInfo{Name: pName, DataType: pDT, TableName: oName, ColumnName: pName, ObjectName: oName, ObjectID: oID}, nil
	}
	return smartquery.PropertyInfo{}, fmt.Errorf("property %q not found in OD %q", propName, odName)
}

func resolvePropertyLakehouse(db *sql.DB, projectID, name string, localProps []smartquery.PropertyInfo) (smartquery.PropertyInfo, error) {
	// Tier 1: local exact match (case-insensitive).
	for _, p := range localProps {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}

	// Tier 2: global exact match across all objects in the project.
	var goID, goName, gpName, gpDT, gpSC string
	row := db.QueryRow(`
		SELECT ot.id, ot.name, p.name, COALESCE(p.data_type,''), COALESCE(p.source_column,'')
		FROM ont_property p
		JOIN ont_object_type ot ON ot.id = p.object_type_id
		WHERE ot.project_id = $1 AND LOWER(p.name) = LOWER($2)
		ORDER BY LENGTH(ot.name) ASC LIMIT 1`, projectID, name)
	if err := row.Scan(&goID, &goName, &gpName, &gpDT, &gpSC); err == nil {
		return smartquery.PropertyInfo{Name: gpName, DataType: gpDT, TableName: goName, ColumnName: gpName, ObjectName: goName, ObjectID: goID}, nil
	}

	// Tier 3: fuzzy substring match with length guard (≤1.5x input length).
	nameLen := len([]rune(name))
	nameLower := strings.ToLower(name)
	for _, p := range localProps {
		pLower := strings.ToLower(p.Name)
		pLen := len([]rune(p.Name))
		if (strings.Contains(nameLower, pLower) || strings.Contains(pLower, nameLower)) &&
			pLen <= int(float64(nameLen)*1.5)+1 && nameLen <= int(float64(pLen)*1.5)+1 {
			return p, nil
		}
	}
	// Fuzzy global via DB with length guard.
	var foID, foName, fpName, fpDT, fpSC string
	row2 := db.QueryRow(`
		SELECT ot.id, ot.name, p.name, COALESCE(p.data_type,''), COALESCE(p.source_column,'')
		FROM ont_property p
		JOIN ont_object_type ot ON ot.id = p.object_type_id
		WHERE ot.project_id = $1
		  AND (p.name ILIKE '%'||$2||'%' OR $2 ILIKE '%'||p.name||'%')
		  AND LENGTH(p.name) <= LENGTH($2) * 2
		  AND LENGTH($2) <= LENGTH(p.name) * 2
		ORDER BY LENGTH(p.name) ASC LIMIT 1`, projectID, name)
	if err := row2.Scan(&foID, &foName, &fpName, &fpDT, &fpSC); err == nil {
		return smartquery.PropertyInfo{Name: fpName, DataType: fpDT, TableName: foName, ColumnName: fpName, ObjectName: foName, ObjectID: foID}, nil
	}

	// Tier 4: hard error with available properties listed.
	var available []string
	for _, p := range localProps {
		available = append(available, p.ObjectName+"."+p.Name)
	}
	return smartquery.PropertyInfo{}, &smartquery.ResolveError{
		Code:    "PROPERTY_NOT_FOUND",
		Message: fmt.Sprintf("属性 %q 未找到。可用属性: %v", name, available),
		Detail:  map[string]any{"name": name, "available": available},
	}
}

// ── Metric Resolution ──

// resolveMetricLakehouse resolves a single metric part into aggregates.
// Resolution order: countRowsKeywords → ont_metric (sql_expression → agg+prop) → property → error.
// NEVER silently falls back to COUNT(*). (Bug #1 fix)
func resolveMetricLakehouse(db *sql.DB, projectID, metricPart string, allProps []smartquery.PropertyInfo, objectNames []string) ([]smartquery.ResolvedAggregate, error) {
	// Peel off an optional `AS <alias>` BEFORE the aggregate wrapper detection.
	// authorAlias (when non-empty) overrides the default `Total_<col>` label so
	// the runtime output column matches what the metric author wrote.
	metricPart, authorAlias := stripMetricAlias(metricPart)
	innerName, detectedFunc := stripAggWrapperWithFunc(metricPart)
	if innerName == "" {
		return nil, nil
	}
	objPrefix, metricName := smartquery.StripObjectPrefix(innerName)
	if metricName == "" {
		metricName = innerName
	}

	// Step 1: Explicit count keywords → COUNT(*).
	if countRowsKeywords[strings.ToLower(strings.TrimSpace(metricName))] {
		return []smartquery.ResolvedAggregate{{Kind: smartquery.AggCountRows, Label: "Count"}}, nil
	}

	// Step 2: Check ont_metric table (scoped to query objects).
	searchObjects := objectNames
	if objPrefix != "" {
		searchObjects = []string{objPrefix}
	}
	for _, objName := range searchObjects {
		var objID string
		db.QueryRow(`SELECT id FROM ont_object_type WHERE project_id=$1 AND name=$2`, projectID, objName).Scan(&objID)
		if objID == "" {
			continue
		}

		var mAgg, mTargetProp, mSQLExpr string
		db.QueryRow(`SELECT COALESCE(aggregation,'SUM'), COALESCE(target_property,''), COALESCE(sql_expression,'')
			FROM ont_metric WHERE target_object_id=$1 AND (name=$2 OR LOWER(name)=LOWER($2))`,
			objID, metricName).Scan(&mAgg, &mTargetProp, &mSQLExpr)

		// Priority 1: sql_expression → AggCustomSQL (reuse Expr field for SQL).
		if mSQLExpr != "" {
			return []smartquery.ResolvedAggregate{{
				Kind:  AggCustomSQL,
				Label: metricName,
				Expr:  mSQLExpr,
			}}, nil
		}

		// Priority 2: aggregation + target_property → AggStandard.
		if mTargetProp != "" {
			for _, p := range allProps {
				if strings.EqualFold(p.Name, mTargetProp) && isNumericType(p.DataType) {
					pp := p
					return []smartquery.ResolvedAggregate{{Kind: smartquery.AggStandard, Prop: &pp, Func: strings.ToUpper(mAgg), Label: metricLabel(authorAlias, p.Name)}}, nil
				}
			}
		}
	}

	// Step 3: Resolve as property name (exact match).
	for _, p := range allProps {
		if strings.EqualFold(p.Name, metricName) {
			// When LLM explicitly specifies an agg function (e.g., "sum(X)"),
			// trust the intent and generate the requested aggregation.
			// Let PostgreSQL validate at execution time — don't second-guess metadata.
			// This handles cases where data_type metadata is wrong or missing.
			if detectedFunc != "" {
				pp := p
				return []smartquery.ResolvedAggregate{{Kind: smartquery.AggStandard, Prop: &pp, Func: detectedFunc, Label: metricLabel(authorAlias, p.Name)}}, nil
			}
			// No explicit function: use data_type to decide.
			if isNumericType(p.DataType) {
				pp := p
				return []smartquery.ResolvedAggregate{{Kind: smartquery.AggStandard, Prop: &pp, Func: "SUM", Label: metricLabel(authorAlias, p.Name)}}, nil
			}
			// Non-numeric without explicit function → metric-as-groupBy fallback.
			pp := p
			return nil, &smartquery.ResolveError{
				Code:    "METRIC_AS_GROUPBY",
				Message: fmt.Sprintf("属性 %q 为非数值类型，将作为分组列处理", metricName),
				Detail:  map[string]any{"propName": metricName, "prop": pp},
			}
		}
	}

	// Step 4: Fuzzy property match with length guard.
	nameLower := strings.ToLower(metricName)
	nameLen := len([]rune(metricName))
	for _, p := range allProps {
		pLower := strings.ToLower(p.Name)
		pLen := len([]rune(p.Name))
		if (strings.Contains(nameLower, pLower) || strings.Contains(pLower, nameLower)) &&
			pLen <= int(float64(nameLen)*1.5)+1 && nameLen <= int(float64(pLen)*1.5)+1 {
			// Explicit function → trust LLM intent regardless of data_type.
			if detectedFunc != "" {
				pp := p
				return []smartquery.ResolvedAggregate{{Kind: smartquery.AggStandard, Prop: &pp, Func: detectedFunc, Label: metricLabel(authorAlias, p.Name)}}, nil
			}
			if isNumericType(p.DataType) {
				pp := p
				return []smartquery.ResolvedAggregate{{Kind: smartquery.AggStandard, Prop: &pp, Func: "SUM", Label: metricLabel(authorAlias, p.Name)}}, nil
			}
		}
	}

	// Step 5: HARD ERROR — no silent COUNT(*) fallback.
	var available []string
	for _, p := range allProps {
		tag := p.ObjectName + "." + p.Name
		if p.DataType != "" {
			tag += "(" + p.DataType + ")"
		}
		available = append(available, tag)
	}
	return nil, &smartquery.ResolveError{
		Code:    "METRIC_NOT_FOUND",
		Message: fmt.Sprintf("口径 %q 未找到对应的属性或预定义口径。可用属性: %v", metricPart, available),
		Detail:  map[string]any{"metric": metricPart, "available": available},
	}
}

// ── Utility ──

// matchAggregateLabel returns the aggregate index if propName matches any aggregate
// label or property name. Returns -1 if no match.
func matchAggregateLabel(propName string, aggregates []smartquery.ResolvedAggregate) int {
	// Normalize: treat space and underscore as equivalent, case-insensitive.
	// This handles LLM variants like "Total Order_Quantity" vs "Total_Order_Quantity".
	norm := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(s, " ", "_"))
	}
	target := norm(propName)
	for i, agg := range aggregates {
		if target == norm(agg.Label) {
			return i
		}
		if agg.Prop != nil && target == norm(agg.Prop.Name) {
			return i
		}
		if strings.EqualFold(propName, "count") && agg.Kind == smartquery.AggCountRows {
			return i
		}
	}
	return -1
}

func isNumericType(dtype string) bool {
	d := strings.ToLower(strings.TrimSpace(dtype))
	switch d {
	// English / generic names
	case "integer", "int", "int64", "double", "decimal",
		"float", "number", "currency", "bigint", "numeric", "real",
		// Postgres internal alias names that show up in pg_type / data_type
		// columns when sourced from JDBC/ODBC/Postgres directly. Without these,
		// any property whose data_type ends up as int2/int4/int8/float4/float8
		// (the typical lakehouse case for `bigint` columns) gets misclassified
		// as non-numeric, which causes resolveMetricLakehouse to dump the prop
		// into GroupByCols and fall back to COUNT(*) — the "sum(X) became
		// COUNT(COUNTRY)" bug.
		"int2", "int4", "int8", "smallint", "float4", "float8",
		"double precision", "money", "serial", "bigserial":
		return true
	}
	// Postgres also reports parameterised types like "numeric(10,2)" or
	// "decimal(38,6)". Match the prefix when a precision is attached.
	for _, prefix := range []string{"numeric(", "decimal(", "float(", "int("} {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}

func isDateType(dtype string) bool {
	d := strings.ToLower(dtype)
	return d == "date" || d == "datetime" || d == "date/time" || d == "timestamp" || strings.Contains(d, "date")
}
