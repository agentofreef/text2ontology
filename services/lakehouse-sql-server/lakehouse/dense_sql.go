package lakehouse

import (
	"fmt"
	"strings"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// ── Dense GROUP BY mode ──
//
// Problem: standard SQL GROUP BY emits only groups with matching rows. If
// user asks "sum qty by (Product, Order_Type)" and Product P has no 'real'
// orders, the (P, 'real') group silently disappears instead of showing 0.
//
// Fix: construct a "dims" subquery — the cartesian product of each groupBy
// column's value universe — then LEFT JOIN the fact source. Fact-side filters
// are pushed into the ON clause (putting them in WHERE would degrade LEFT JOIN
// to INNER JOIN and defeat the fix). Aggregates that can be 0-defaulted
// (SUM/COUNT/DISTINCTCOUNT) are wrapped with COALESCE(..., 0); MIN/MAX/AVG
// remain NULL on empty groups (correct semantics — no data).
//
// Scope (MVP):
//   • one fact Od (owner of first AggStandard prop; or Objects[0] for pure COUNT)
//   • 0..N pure-dim Ods, each directly joinable to fact Od via one JoinEdge
//   • fact Od may itself contribute groupBy columns (handled via its own dim sub)
//   • aggregates: AggStandard + AggCountRows; custom SQL → fallback
//   • no derived metrics (BuildWithDerived wraps dense SQL as-is)
//
// Unhandled (→ legacy fallback): custom SQL aggregates, bridge Ods required
// to reach a dim Od from fact Od, or queries without groupBy.

// isDenseApplicable reports whether this query can go through dense mode.
func isDenseApplicable(rq *ResolvedLakehouseQuery) bool {
	if rq.DenseGroups != nil && !*rq.DenseGroups {
		return false
	}
	if len(rq.GroupByCols) == 0 || len(rq.Aggregates) == 0 {
		return false
	}
	for _, agg := range rq.Aggregates {
		if agg.Kind != smartquery.AggStandard && agg.Kind != smartquery.AggCountRows {
			return false
		}
	}
	if len(rq.Derived) > 0 {
		return false
	}
	return factOdName(rq) != ""
}

// factOdName identifies the Od that owns the aggregated measure.
func factOdName(rq *ResolvedLakehouseQuery) string {
	for _, agg := range rq.Aggregates {
		if agg.Kind == smartquery.AggStandard && agg.Prop != nil && agg.Prop.ObjectName != "" {
			return agg.Prop.ObjectName
		}
	}
	if len(rq.Objects) > 0 {
		return rq.Objects[0].Name
	}
	return ""
}

// partitionGroupBy groups groupBy cols by Od name, preserving first-seen order.
func partitionGroupBy(rq *ResolvedLakehouseQuery) ([]string, map[string][]smartquery.ResolvedGroupBy) {
	var ods []string
	by := map[string][]smartquery.ResolvedGroupBy{}
	for _, gb := range rq.GroupByCols {
		od := gb.Prop.ObjectName
		if _, ok := by[od]; !ok {
			ods = append(ods, od)
		}
		by[od] = append(by[od], gb)
	}
	return ods, by
}

// splitDenseFilters routes each filter to either a dim-universe WHERE (by Od)
// or the fact-side LEFT JOIN ON clause.
//
// Routing rules:
//   - pure-dim Od (has groupBy, != factOd): ALL filters on it → its dim WHERE
//     (no fact-side copy needed: the filtered column doesn't exist on fact Od)
//   - fact Od, column IS a groupBy col: → dim WHERE only
//     (no fact-side copy needed: LEFT JOIN ON already has `fact.col = dims.col`
//     equality, so narrowing dims implicitly narrows the matched fact rows)
//   - fact Od, column is NOT a groupBy col (e.g. fact-self-dim + filter on a
//     non-grouped column like MTM_Number): → dim WHERE AND fact ON
//     · dim WHERE: narrows the dim universe so we don't emit COALESCE→0 rows
//     for (gb) combos that don't actually exist under the filter
//     · fact ON: required because the filtered column is NOT in dims' output,
//     so ON has no `fact.col = dims.col` equality to carry the restriction —
//     without this copy, fact rows with a different value would still join
//     and wrongly contribute to the aggregate
//   - any other Od (bridge / non-dim / non-fact): → fact ON (handled as extra
//     INNER JOIN in buildDenseSQL)
func splitDenseFilters(rq *ResolvedLakehouseQuery, factOd string) (map[string][]smartquery.ResolvedFilter, []smartquery.ResolvedFilter) {
	dimFilters := map[string][]smartquery.ResolvedFilter{}
	var factFilters []smartquery.ResolvedFilter

	_, gbByOd := partitionGroupBy(rq)
	factGBCols := map[string]bool{}
	for _, gb := range gbByOd[factOd] {
		factGBCols[strings.ToLower(gb.Prop.Name)] = true
	}

	for _, f := range rq.FilterItems {
		od := f.Prop.ObjectName
		_, hasGB := gbByOd[od]
		isFact := strings.EqualFold(od, factOd)

		switch {
		case hasGB && !isFact:
			// Pure-dim Od: filter goes to its own dim sub WHERE.
			dimFilters[od] = append(dimFilters[od], f)
		case isFact && factGBCols[strings.ToLower(f.Prop.Name)]:
			// Fact Od, GB col: dim WHERE alone is enough; ON already equi-joins
			// this column so narrowing dims narrows the fact rows too.
			dimFilters[od] = append(dimFilters[od], f)
		case isFact && len(gbByOd[factOd]) > 0:
			// Fact-self-dim, NON-GB col (e.g. MTM_Number filter while grouping
			// by Offering/Order_Type): duplicate in BOTH dim sub (to narrow the
			// universe) AND fact ON (because ON has no equi-join on this col,
			// so fact rows with a different value would otherwise still match).
			dimFilters[od] = append(dimFilters[od], f)
			factFilters = append(factFilters, f)
		default:
			factFilters = append(factFilters, f)
		}
	}
	return dimFilters, factFilters
}

// findJoinEdgeBetween returns the JoinEdge between od1 and od2 in either
// direction, reoriented so FromOd==od1 and ToOd==od2.
func findJoinEdgeBetween(od1, od2 string, joinPath []JoinEdge) (JoinEdge, bool) {
	for _, e := range joinPath {
		if strings.EqualFold(e.FromOd, od1) && strings.EqualFold(e.ToOd, od2) {
			return e, true
		}
		if strings.EqualFold(e.FromOd, od2) && strings.EqualFold(e.ToOd, od1) {
			return JoinEdge{
				FromOd: e.ToOd, FromProp: e.ToProp,
				ToOd: e.FromOd, ToProp: e.FromProp,
				Cardinality: e.Cardinality,
			}, true
		}
	}
	return JoinEdge{}, false
}

// findJoinEdgeToFact returns the JoinEdge connecting dimOd directly to factOd,
// reoriented so FromOd==dimOd and ToOd==factOd. Second return is false if none.
func findJoinEdgeToFact(dimOd, factOd string, joinPath []JoinEdge) (JoinEdge, bool) {
	for _, e := range joinPath {
		if strings.EqualFold(e.FromOd, dimOd) && strings.EqualFold(e.ToOd, factOd) {
			return e, true
		}
		if strings.EqualFold(e.FromOd, factOd) && strings.EqualFold(e.ToOd, dimOd) {
			return JoinEdge{
				FromOd: e.ToOd, FromProp: e.ToProp,
				ToOd: e.FromOd, ToProp: e.FromProp,
				Cardinality: e.Cardinality,
			}, true
		}
	}
	return JoinEdge{}, false
}

// gbOutputLabel returns the stable output alias for a groupBy column.
func gbOutputLabel(gb smartquery.ResolvedGroupBy) string {
	if gb.OutputLabel != "" {
		return gb.OutputLabel
	}
	return gb.Prop.Name
}

// dimJoinKeyAlias returns the alias for a hidden join key carried in dims.
func dimJoinKeyAlias(dimOd, propName string) string {
	return "__" + dimOd + "_" + propName + "_jk__"
}

// dimJoinRefInDims returns the dims-side column name used in the LEFT JOIN ON
// clause. If the join key is itself a non-granularity groupBy column, reuse
// its output label; otherwise use the hidden __..._jk__ alias.
func dimJoinRefInDims(dimOd string, edge JoinEdge, gbByOd map[string][]smartquery.ResolvedGroupBy) string {
	for _, gb := range gbByOd[dimOd] {
		if strings.EqualFold(gb.Prop.Name, edge.FromProp) && gb.Granularity == "" {
			return gbOutputLabel(gb)
		}
	}
	return dimJoinKeyAlias(dimOd, edge.FromProp)
}

// renderOdSource returns the FROM-clause source for an Od.
//   - ontology=true  (Layer 1): bare "OdName"
//   - ontology=false (Layer 2): (canonical_query) AS "OdName"
func renderOdSource(obj *ObjectInfo, ontology bool) string {
	if ontology {
		return quoteIdent(obj.Name)
	}
	return "(" + obj.CanonicalQuery + ") AS " + quoteIdent(obj.Name)
}

// denseAggExpr builds the outer aggregate expression.
// SUM/COUNT/DISTINCTCOUNT wrapped with COALESCE(..., 0); others left raw.
func denseAggExpr(agg smartquery.ResolvedAggregate, factObj *ObjectInfo) string {
	factRef := quoteIdent(factObj.Name)
	switch agg.Kind {
	case smartquery.AggCountRows:
		label := agg.Label
		if label == "" {
			label = "Count"
		}
		// Use fact-side witness column so unmatched LEFT JOIN rows aren't counted.
		witness := ""
		if len(factObj.Props) > 0 {
			witness = factObj.Props[0].Name
		}
		if witness == "" {
			return fmt.Sprintf("COUNT(*) AS %s", quoteIdent(label))
		}
		return fmt.Sprintf("COALESCE(COUNT(%s.%s), 0) AS %s",
			factRef, quoteColRef(witness), quoteIdent(label))
	case smartquery.AggStandard:
		if agg.Prop == nil {
			return fmt.Sprintf("COALESCE(COUNT(*), 0) AS %s", quoteIdent("Count"))
		}
		col := factRef + "." + quoteColRef(agg.Prop.Name)
		fn := strings.ToUpper(agg.Func)
		switch fn {
		case "SUM":
			return fmt.Sprintf("COALESCE(SUM(%s), 0) AS %s", col, quoteIdent(agg.Label))
		case "COUNT":
			return fmt.Sprintf("COALESCE(COUNT(%s), 0) AS %s", col, quoteIdent(agg.Label))
		case "DISTINCTCOUNT":
			return fmt.Sprintf("COALESCE(COUNT(DISTINCT %s), 0) AS %s", col, quoteIdent(agg.Label))
		default:
			// MIN/MAX/AVG — NULL on empty groups is semantically correct.
			return fmt.Sprintf("%s(%s) AS %s", sqlAggFunc(fn), col, quoteIdent(agg.Label))
		}
	}
	return fmt.Sprintf("COALESCE(COUNT(*), 0) AS %s", quoteIdent("Count"))
}

// denseOrderByRef returns the ORDER BY reference in the outer dense query.
func denseOrderByRef(ob smartquery.ResolvedOrderBy, rq *ResolvedLakehouseQuery) string {
	switch ob.Kind {
	case smartquery.OrderByProp:
		if ob.Prop != nil {
			for _, gb := range rq.GroupByCols {
				if strings.EqualFold(gb.Prop.Name, ob.Prop.Name) {
					return "dims." + quoteIdent(gbOutputLabel(gb))
				}
			}
			return quoteColRef(ob.Prop.Name)
		}
		return quoteIdent(ob.Label)
	case smartquery.OrderByAggregate:
		if ob.AggIndex >= 0 && ob.AggIndex < len(rq.Aggregates) {
			return quoteIdent(rq.Aggregates[ob.AggIndex].Label)
		}
		return quoteIdent("Count")
	case smartquery.OrderByCustomLabel:
		return quoteIdent(ob.Label)
	}
	return quoteIdent("Count")
}

// ── Build dim universe (per-Od SELECT DISTINCT + CROSS JOIN) ──

// buildSingleDimOdSubquery returns the SELECT DISTINCT clause for one Od.
// Includes the join key to fact Od as a hidden column when applicable.
func buildSingleDimOdSubquery(
	od string,
	gbCols []smartquery.ResolvedGroupBy,
	filters []smartquery.ResolvedFilter,
	factOd string,
	joinPath []JoinEdge,
	obj *ObjectInfo,
	ontology bool,
) string {
	odRef := quoteIdent(obj.Name)

	var selCols []string
	// Visible groupBy columns.
	for _, gb := range gbCols {
		label := gbOutputLabel(gb)
		base := odRef + "." + quoteColRef(gb.Prop.Name)
		if gb.Granularity != "" {
			base = dateGranularityExpr(base, gb.Granularity)
		}
		selCols = append(selCols, fmt.Sprintf("%s AS %s", base, quoteIdent(label)))
	}

	// Hidden join key (only for pure-dim Ods).
	if !strings.EqualFold(od, factOd) {
		if edge, ok := findJoinEdgeToFact(od, factOd, joinPath); ok {
			keyName := edge.FromProp
			alreadyVisible := false
			for _, gb := range gbCols {
				if strings.EqualFold(gb.Prop.Name, keyName) && gb.Granularity == "" {
					alreadyVisible = true
					break
				}
			}
			if !alreadyVisible {
				selCols = append(selCols, fmt.Sprintf("%s.%s AS %s",
					odRef, quoteColRef(keyName), quoteIdent(dimJoinKeyAlias(od, keyName))))
			}
		}
	}

	// WHERE from filters on this Od. Group multi-eq on the same prop into
	// IN(...) — three "MONTH = 'X'" filters from the LLM otherwise become
	// `MONTH ILIKE 'X' AND MONTH ILIKE 'Y' AND MONTH ILIKE 'Z'` (impossible).
	whereConds := buildGroupedFilterConds(filters, od)

	var sb strings.Builder
	sb.WriteString("SELECT DISTINCT ")
	sb.WriteString(strings.Join(selCols, ", "))
	sb.WriteString(" FROM ")
	sb.WriteString(renderOdSource(obj, ontology))
	if len(whereConds) > 0 {
		sb.WriteString(" WHERE " + strings.Join(whereConds, " AND "))
	}
	return sb.String()
}

// buildDimUniverse composes the dim-universe subquery from per-Od DISTINCT subs.
// Single dim Od → that sub directly; multiple → CROSS JOIN them.
func buildDimUniverse(
	dimOds []string,
	gbByOd map[string][]smartquery.ResolvedGroupBy,
	dimFilters map[string][]smartquery.ResolvedFilter,
	factOd string,
	joinPath []JoinEdge,
	odByName map[string]*ObjectInfo,
	ontology bool,
) (string, error) {
	if len(dimOds) == 0 {
		return "", &smartquery.ResolveError{Code: "DENSE_NO_DIMS", Message: "dense mode: 没有 groupBy 维度"}
	}

	var perOdSubs []string
	for _, od := range dimOds {
		obj, ok := odByName[od]
		if !ok || obj == nil {
			return "", &smartquery.ResolveError{
				Code:    "OBJECT_NOT_FOUND",
				Message: fmt.Sprintf("dense mode: 维度对象 %q 未加载", od),
				Detail:  map[string]any{"od": od},
			}
		}
		sub := buildSingleDimOdSubquery(
			od, gbByOd[od], dimFilters[od], factOd, joinPath, obj, ontology,
		)
		perOdSubs = append(perOdSubs, sub)
	}

	if len(perOdSubs) == 1 {
		return perOdSubs[0], nil
	}
	var wrapped []string
	for i, s := range perOdSubs {
		wrapped = append(wrapped, fmt.Sprintf("(%s) d%d", s, i))
	}
	return "SELECT * FROM " + strings.Join(wrapped, " CROSS JOIN "), nil
}

// ── Build LEFT JOIN ON clauses ──

func buildDenseOn(
	rq *ResolvedLakehouseQuery,
	factOd string,
	joinPath []JoinEdge,
	factFilters []smartquery.ResolvedFilter,
) ([]string, error) {
	factRef := quoteIdent(factOd)
	dimRef := "dims"
	var conds []string

	_, gbByOd := partitionGroupBy(rq)

	// Fact-Od groupBy columns: fact.col = dims.label (apply granularity on fact side).
	for _, gb := range gbByOd[factOd] {
		label := gbOutputLabel(gb)
		left := factRef + "." + quoteColRef(gb.Prop.Name)
		if gb.Granularity != "" {
			left = dateGranularityExpr(left, gb.Granularity)
		}
		conds = append(conds, fmt.Sprintf("%s = %s.%s", left, dimRef, quoteIdent(label)))
	}

	// Pure-dim Ods: join via direct JoinEdge.
	for _, od := range sortedKeys(gbByOd) {
		if strings.EqualFold(od, factOd) {
			continue
		}
		edge, ok := findJoinEdgeToFact(od, factOd, joinPath)
		if !ok {
			return nil, &smartquery.ResolveError{
				Code:    "DENSE_NO_DIRECT_JOIN",
				Message: fmt.Sprintf("dense mode: 维度对象 %q 到 fact Od %q 没有直接 join_key 边", od, factOd),
				Detail:  map[string]any{"dimOd": od, "factOd": factOd},
			}
		}
		conds = append(conds, fmt.Sprintf("%s.%s = %s.%s",
			factRef, quoteColRef(edge.ToProp),
			dimRef, quoteIdent(dimJoinRefInDims(od, edge, gbByOd)),
		))
	}

	// Fact-side filters (must go in ON, not WHERE — else LEFT JOIN degrades).
	// Only filters whose Od is factOd should reach here; use factOd as table
	// prefix. Multi-eq on same prop → IN(...) (avoids impossible-AND bug).
	conds = append(conds, buildGroupedFilterConds(factFilters, factOd)...)

	return conds, nil
}

// buildGroupedFilterConds groups filters on the same property and renders
// each group via buildFilterGroupExpr — multiple "=" filters on one prop
// merge into IN(...) instead of being AND'd as ILIKE (which is the
// impossible-condition bug: `MONTH ILIKE '2026-01' AND MONTH ILIKE '2026-02'`
// can never be true for a single row).
//
// qualifyOd is the table prefix to use in the column reference (typically the
// Od name; dense_sql's dim subqueries / extra JOINs / fact ON clauses all
// reference tables by Od name). Empty qualifyOd falls back to each filter's
// own Prop.ObjectName.
//
// Returns per-group condition strings, ready to be joined with " AND ".
func buildGroupedFilterConds(filters []smartquery.ResolvedFilter, qualifyOd string) []string {
	if len(filters) == 0 {
		return nil
	}
	groups := groupFilters(filters)
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		var colRef string
		if qualifyOd != "" {
			colRef = quoteIdent(qualifyOd) + "." + quoteColRef(g.prop.Name)
		} else {
			colRef = quoteIdent(g.prop.ObjectName) + "." + quoteColRef(g.prop.Name)
		}
		if c := buildFilterGroupExpr(colRef, g.filters); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// sortedKeys returns map keys in a deterministic order.
func sortedKeys(m map[string][]smartquery.ResolvedGroupBy) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// simple insertion sort — small N
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// sortedStringKeys returns keys of a map[string][]smartquery.ResolvedFilter in deterministic order.
func sortedStringKeys(m map[string][]smartquery.ResolvedFilter) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// ── Main dense build ──

// buildDenseSQL generates the dense-mode SQL for both Layer 1 (ontology=true)
// and Layer 2 (ontology=false). Returns the SQL string.
func buildDenseSQL(rq *ResolvedLakehouseQuery, joinPath []JoinEdge, ontology bool) (string, error) {
	factOd := factOdName(rq)
	if factOd == "" {
		return "", &smartquery.ResolveError{Code: "OBJECT_NOT_FOUND", Message: "dense mode: 无法识别 fact Od"}
	}

	odByName := map[string]*ObjectInfo{}
	for i := range rq.Objects {
		odByName[rq.Objects[i].Name] = &rq.Objects[i]
	}
	factObj, ok := odByName[factOd]
	if !ok {
		return "", &smartquery.ResolveError{
			Code:    "OBJECT_NOT_FOUND",
			Message: fmt.Sprintf("dense mode: fact Od %q 未加载", factOd),
			Detail:  map[string]any{"factOd": factOd},
		}
	}

	dimFilters, factFilters := splitDenseFilters(rq, factOd)
	dimOds, gbByOd := partitionGroupBy(rq)

	// Split factFilters: filters on factOd stay in LEFT JOIN ON (safe);
	// filters on other Ods need an extra INNER JOIN + WHERE (they reference a
	// table not in the FROM chain, causing "missing FROM-clause entry").
	var trueFactFilters []smartquery.ResolvedFilter
	extraOdFilters := map[string][]smartquery.ResolvedFilter{}
	for _, f := range factFilters {
		if strings.EqualFold(f.Prop.ObjectName, factOd) {
			trueFactFilters = append(trueFactFilters, f)
		} else {
			extraOdFilters[f.Prop.ObjectName] = append(extraOdFilters[f.Prop.ObjectName], f)
		}
	}

	// Dim universe.
	dimSQL, err := buildDimUniverse(dimOds, gbByOd, dimFilters, factOd, joinPath, odByName, ontology)
	if err != nil {
		return "", err
	}

	// LEFT JOIN ON clauses (only true fact-Od filters).
	onConds, err := buildDenseOn(rq, factOd, joinPath, trueFactFilters)
	if err != nil {
		return "", err
	}
	if len(onConds) == 0 {
		onConds = []string{"TRUE"}
	}

	// Extra JOINs for Ods referenced in filters but not in the dim/fact chain.
	// Each such Od gets an INNER JOIN via its direct edge to factOd; filters
	// go into WHERE (not ON) because LEFT JOIN semantics are already handled).
	var extraJoins []string
	var extraWhere []string
	for _, od := range sortedStringKeys(extraOdFilters) {
		extraObj, ok := odByName[od]
		if !ok || extraObj == nil {
			return "", &smartquery.ResolveError{
				Code:    "OBJECT_NOT_FOUND",
				Message: fmt.Sprintf("dense mode: 过滤引用了未加载的对象 %q", od),
				Detail:  map[string]any{"od": od},
			}
		}
		// Oriented: factOd → extraOd (FromOd=factOd, ToOd=extraOd).
		edge, ok := findJoinEdgeBetween(factOd, od, joinPath)
		if !ok {
			return "", &smartquery.ResolveError{
				Code:    "DENSE_NO_DIRECT_JOIN",
				Message: fmt.Sprintf("dense mode: fact Od %q 到过滤对象 %q 没有直接 join 边", factOd, od),
				Detail:  map[string]any{"factOd": factOd, "extraOd": od},
			}
		}
		extraJoins = append(extraJoins, fmt.Sprintf("JOIN %s ON %s.%s = %s.%s",
			renderOdSource(extraObj, ontology),
			quoteIdent(od), quoteColRef(edge.ToProp),
			quoteIdent(factOd), quoteColRef(edge.FromProp),
		))
		// Multi-eq on same prop → IN(...).
		extraWhere = append(extraWhere, buildGroupedFilterConds(extraOdFilters[od], od)...)
	}

	// Outer SELECT.
	var selParts []string
	for _, gb := range rq.GroupByCols {
		selParts = append(selParts, "  dims."+quoteIdent(gbOutputLabel(gb)))
	}
	for _, agg := range rq.Aggregates {
		selParts = append(selParts, "  "+denseAggExpr(agg, factObj))
	}

	var sb strings.Builder
	sb.WriteString("SELECT\n")
	sb.WriteString(strings.Join(selParts, ",\n"))
	sb.WriteString(fmt.Sprintf("\nFROM (%s) dims", dimSQL))
	sb.WriteString(fmt.Sprintf("\nLEFT JOIN %s ON %s",
		renderOdSource(factObj, ontology),
		strings.Join(onConds, "\n  AND ")))
	for _, j := range extraJoins {
		sb.WriteString("\n" + j)
	}
	if len(extraWhere) > 0 {
		sb.WriteString("\nWHERE " + strings.Join(extraWhere, " AND "))
	}

	// GROUP BY (positional).
	var gbParts []string
	for i := range rq.GroupByCols {
		gbParts = append(gbParts, fmt.Sprintf("%d", i+1))
	}
	sb.WriteString("\nGROUP BY " + strings.Join(gbParts, ", "))

	// HAVING reuses existing helper (references aggregate labels).
	having := buildHavingClause(rq)
	if having != "" {
		sb.WriteString("\n" + having)
	}

	// ── Universal share column: when AddShareColumn=true and we have at
	// least one aggregate, wrap the query with an outer SELECT that adds
	// `metric / SUM(metric) OVER ()` * 100. ORDER BY + LIMIT relocate to
	// the outer query so Top-N is computed AFTER share — never before.
	if rq.AddShareColumn && len(rq.Aggregates) > 0 {
		shareLabel := rq.ShareLabel
		if shareLabel == "" {
			shareLabel = "占比"
		}
		// Use the first aggregate as the share numerator. Multi-metric
		// queries get share on the primary metric only — keeps semantics
		// unambiguous.
		metricLabel := rq.Aggregates[0].Label

		var outer strings.Builder
		outer.WriteString("SELECT _share_inner.*,\n")
		outer.WriteString(fmt.Sprintf(
			"  ROUND(_share_inner.%s * 100.0 / NULLIF(SUM(_share_inner.%s) OVER (), 0), 2) AS %s\n",
			quoteIdent(metricLabel), quoteIdent(metricLabel), quoteIdent(shareLabel)))
		outer.WriteString(fmt.Sprintf("FROM (\n%s\n) _share_inner", sb.String()))

		// Outer ORDER BY: if user requested ORDER BY by metric, swap it
		// to ORDER BY share (same direction). Otherwise default to share DESC.
		if len(rq.OrderByCols) > 0 {
			var obParts []string
			for _, ob := range rq.OrderByCols {
				ref := denseOrderByRef(ob, rq)
				// If sorting by the primary metric label, transparently
				// switch to the share column — semantically what user
				// meant when asking "占比最大的 Top N".
				if strings.Trim(ref, `"`) == metricLabel {
					ref = quoteIdent(shareLabel)
				}
				obParts = append(obParts, ref+" "+ob.Dir)
			}
			outer.WriteString("\nORDER BY " + strings.Join(obParts, ", "))
		} else {
			outer.WriteString(fmt.Sprintf("\nORDER BY %s DESC", quoteIdent(shareLabel)))
		}
		if rq.Limit > 0 {
			outer.WriteString(fmt.Sprintf("\nLIMIT %d", rq.Limit))
		}
		return outer.String(), nil
	}

	if len(rq.OrderByCols) > 0 {
		var obParts []string
		for _, ob := range rq.OrderByCols {
			obParts = append(obParts, denseOrderByRef(ob, rq)+" "+ob.Dir)
		}
		sb.WriteString("\nORDER BY " + strings.Join(obParts, ", "))
	}

	if rq.Limit > 0 {
		sb.WriteString(fmt.Sprintf("\nLIMIT %d", rq.Limit))
	}

	return sb.String(), nil
}
