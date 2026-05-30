package lakehouse

// sql_builder_v2.go — goqu-based SQL builder for the lakehouse query engine.
//
// Both the query skeleton (SELECT/FROM/WHERE/GROUP BY/HAVING/ORDER BY/LIMIT)
// AND the inner expressions (column refs, filter predicates, aggregates,
// JOIN ON clauses) are built with goqu native expressions:
//
//   - Column references go through goqu.I("Od.Col") so dialect-correct
//     identifier quoting is automatic. No more manual quoteIdent calls
//     for SELECT / WHERE / GROUP BY / ORDER BY column refs.
//   - Filter values are escaped by goqu's literal layer (PostgreSQL doubles
//     single quotes), eliminating the manual escapeSQLString helper
//     everywhere except in the date-granularity / share-column wrappers.
//   - Aggregates use goqu.SUM / goqu.COUNT / goqu.MAX / goqu.MIN / goqu.AVG
//     and goqu.L only for ont_metric.sql_expression custom SQL — that path
//     by definition opts out of structural validation.
//
// Two raw escape hatches remain, both intentional:
//   1. canonical_query — user-defined SQL fragment; embedded as
//      `(<canonical>) AS "OdName"` via goqu.L("(...) AS Od").
//   2. ont_metric custom SQL aggregate — opaque expression by contract.
//
// Dense GROUP BY mode (CROSS JOIN dim universe + LEFT JOIN fact, with
// filter routing splitting) lives in dense_sql.go. When isDenseApplicable
// returns true, BuildSQLV2 delegates to buildDenseSQL.

import (
	"fmt"
	"strings"

	"github.com/doug-martin/goqu/v9"
	_ "github.com/doug-martin/goqu/v9/dialect/postgres"
	"github.com/doug-martin/goqu/v9/exp"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// pgDialect is the goqu dialect handle for PostgreSQL.
var pgDialect = goqu.Dialect("postgres")

// BuildMode selects the SQL layer the builder emits.
type BuildMode int

const (
	// BuildPhysical produces full executable PostgreSQL: each Od is wrapped
	// as `(canonical_query) AS "Od"` so that `Od.Property` references
	// resolve against the columns the canonical query exposes.
	BuildPhysical BuildMode = iota
	// BuildOntology produces human-readable preview SQL: each Od is
	// referenced by its bare ontology name (`FROM "Od"`). The output is
	// not directly executable — it's a UI affordance that lets users see
	// the query in domain terms before the engine substitutes the
	// canonical_query at execution.
	BuildOntology
)

// BuildSQLV2 generates the physical (executable) SQL — the canonical
// entrypoint used by Engine.Execute. Wraps BuildLayerV2(BuildPhysical).
func BuildSQLV2(rq *ResolvedLakehouseQuery, joinPath []JoinEdge) (string, error) {
	return BuildLayerV2(rq, joinPath, BuildPhysical)
}

// BuildOntologySQLV2 generates the ontology (preview) SQL — the same
// query expressed in Od names. Replaces the legacy hand-rolled
// BuildOntologySQL function in P4.
func BuildOntologySQLV2(rq *ResolvedLakehouseQuery, joinPath []JoinEdge) (string, error) {
	return BuildLayerV2(rq, joinPath, BuildOntology)
}

// BuildLayerV2 is the unified builder. Both Physical and Ontology layers
// share every clause except the FROM source: dense / single-Od / multi-Od
// dispatch is identical, only the FROM strategy differs.
func BuildLayerV2(rq *ResolvedLakehouseQuery, joinPath []JoinEdge, mode BuildMode) (string, error) {
	if len(rq.Objects) == 0 {
		return "", &smartquery.ResolveError{Code: "OBJECT_NOT_FOUND", Message: "没有可用的对象，无法生成 SQL"}
	}
	if isDenseApplicable(rq) {
		if sql, err := buildDenseSQL(rq, joinPath, mode == BuildOntology); err == nil {
			return sql, nil
		} else if mode == BuildPhysical {
			// Only record the warning on the physical path; the ontology
			// layer is preview-only and silently falls back so the UI
			// never sees a "dense disabled" notice for the same query
			// that ran successfully.
			rq.Warnings = append(rq.Warnings,
				fmt.Sprintf("dense GROUP BY mode 未启用（回落到常规 SQL）: %v", err))
		}
	}
	if len(rq.Objects) == 1 {
		return buildSingleOdV2(rq, mode)
	}
	return buildMultiOdV2(rq, joinPath, mode)
}

// BuildWithDerivedV2 wraps the v2 builder output with derived-metric outer
// SELECT. Mirrors v1's BuildWithDerived control flow.
func BuildWithDerivedV2(rq *ResolvedLakehouseQuery, joinPath []JoinEdge) (string, error) {
	if len(rq.Derived) == 0 {
		return BuildSQLV2(rq, joinPath)
	}

	// Build inner WITHOUT LIMIT and ORDER BY — they relocate to the outer.
	savedLimit := rq.Limit
	savedOrder := rq.OrderByCols
	rq.Limit = 0
	rq.OrderByCols = nil

	innerSQL, err := BuildSQLV2(rq, joinPath)

	rq.Limit = savedLimit
	rq.OrderByCols = savedOrder

	if err != nil {
		return "", err
	}
	return wrapDerivedV2(innerSQL, rq), nil
}

// wrapDerivedV2 builds the outer SELECT for derived metrics around innerSQL.
// Kept as direct string assembly (not goqu) — the "sub.* + N raw expression
// aliases" shape contains opaque user SQL (the derived expression) so a
// typed AST buys nothing here.
func wrapDerivedV2(innerSQL string, rq *ResolvedLakehouseQuery) string {
	var sb strings.Builder
	sb.WriteString("SELECT sub.*")
	for _, d := range rq.Derived {
		expr := wrapDivisionSafety(d.Expression, rq.Aggregates)
		sb.WriteString(fmt.Sprintf(",\n  %s AS %s", expr, quoteIdent(d.Name)))
	}
	sb.WriteString(fmt.Sprintf("\nFROM (\n%s\n) sub", innerSQL))
	if len(rq.OrderByCols) > 0 {
		var parts []string
		for _, ob := range rq.OrderByCols {
			ref := orderByRefDerived(ob, rq)
			parts = append(parts, ref+" "+ob.Dir)
		}
		sb.WriteString("\nORDER BY " + strings.Join(parts, ", "))
	}
	if rq.Limit > 0 {
		sb.WriteString(fmt.Sprintf("\nLIMIT %d", rq.Limit))
	}
	return sb.String()
}

// ── Single-Od ──

// fromSource returns the goqu FROM-clause source for an Od, picking between
// the physical canonical_query subselect (executable) and the bare ontology
// name (preview).
func fromSource(obj ObjectInfo, mode BuildMode) exp.Expression {
	if mode == BuildOntology {
		return goqu.T(obj.Name)
	}
	return goqu.L("(" + obj.CanonicalQuery + ") AS " + quoteIdent(obj.Name))
}

func buildSingleOdV2(rq *ResolvedLakehouseQuery, mode BuildMode) (string, error) {
	obj := rq.Objects[0]
	alias := obj.Name

	ds := pgDialect.From(fromSource(obj, mode))

	if sel := selectExprsV2(rq, alias, false); len(sel) > 0 {
		ds = ds.Select(sel...)
	} else {
		ds = ds.Select(goqu.L("*"))
	}

	if w := whereExprV2(rq.FilterItems, alias, false); w != nil {
		ds = ds.Where(w)
	}

	if gb := groupByExprsV2(rq, alias, false); len(gb) > 0 {
		ds = ds.GroupBy(gb...)
	}

	if h := havingExprV2(rq); h != nil {
		ds = ds.Having(h)
	}

	if ob := orderByExprsV2(rq, alias, false); len(ob) > 0 {
		ds = ds.Order(ob...)
	}

	if rq.Limit > 0 {
		ds = ds.Limit(uint(rq.Limit))
	}

	sql, _, err := ds.ToSQL()
	if err != nil {
		return "", fmt.Errorf("goqu ToSQL failed: %w", err)
	}

	if rq.AddShareColumn && len(rq.Aggregates) > 0 {
		return wrapShareColumnV2(sql, rq), nil
	}
	return sql, nil
}

// ── Multi-Od ──

func buildMultiOdV2(rq *ResolvedLakehouseQuery, joinPath []JoinEdge, mode BuildMode) (string, error) {
	objMap := map[string]ObjectInfo{}
	for _, o := range rq.Objects {
		objMap[o.Name] = o
	}

	firstObj := rq.Objects[0]
	ds := pgDialect.From(fromSource(firstObj, mode))

	joinedOds := map[string]bool{firstObj.Name: true}
	for _, edge := range joinPath {
		var newOd, newProp, existingOd, existingProp string
		switch {
		case joinedOds[edge.FromOd] && !joinedOds[edge.ToOd]:
			existingOd, existingProp, newOd, newProp = edge.FromOd, edge.FromProp, edge.ToOd, edge.ToProp
		case joinedOds[edge.ToOd] && !joinedOds[edge.FromOd]:
			existingOd, existingProp, newOd, newProp = edge.ToOd, edge.ToProp, edge.FromOd, edge.FromProp
		case joinedOds[edge.FromOd] && joinedOds[edge.ToOd]:
			continue
		default:
			continue
		}

		newObj, ok := objMap[newOd]
		if !ok {
			return "", &smartquery.ResolveError{
				Code:    "INTERMEDIATE_OD_MISSING",
				Message: fmt.Sprintf("JOIN 路径中的中间对象 %q 未加载 canonical_query。请确认该对象已配置 SQL 映射。", newOd),
				Detail:  map[string]any{"missingOd": newOd},
			}
		}

		// JOIN ON via goqu native column refs — no string concat.
		onCond := odCol(existingOd, existingProp).Eq(odCol(newOd, newProp))
		ds = ds.Join(fromSource(newObj, mode), goqu.On(onCond))
		joinedOds[newOd] = true
	}

	if sel := selectExprsV2(rq, "", true); len(sel) > 0 {
		ds = ds.Select(sel...)
	} else {
		ds = ds.Select(goqu.L("*"))
	}

	if w := whereExprV2(rq.FilterItems, "", true); w != nil {
		ds = ds.Where(w)
	}

	if gb := groupByExprsV2(rq, "", true); len(gb) > 0 {
		ds = ds.GroupBy(gb...)
	}

	if h := havingExprV2(rq); h != nil {
		ds = ds.Having(h)
	}

	if ob := orderByExprsV2(rq, "", true); len(ob) > 0 {
		ds = ds.Order(ob...)
	}

	if rq.Limit > 0 {
		ds = ds.Limit(uint(rq.Limit))
	}

	sql, _, err := ds.ToSQL()
	if err != nil {
		return "", fmt.Errorf("goqu ToSQL failed: %w", err)
	}

	if rq.AddShareColumn && len(rq.Aggregates) > 0 {
		return wrapShareColumnV2(sql, rq), nil
	}
	return sql, nil
}

// ── Goqu identifier helpers ──

// odCol returns a goqu identifier for "alias.col" with both parts quoted by
// the dialect. This is the single allowed path for column references inside
// the v2 builder — direct fmt.Sprintf("%s.%s", quoteIdent(...), ...) is
// banned here.
func odCol(alias, col string) exp.IdentifierExpression {
	return goqu.I(alias + "." + col)
}

// propCol resolves a property to a qualified goqu identifier, picking the
// table prefix per builder mode (single-Od uses caller alias, multi-Od uses
// prop.ObjectName).
func propCol(prop smartquery.PropertyInfo, alias string, multi bool) exp.IdentifierExpression {
	if multi {
		return odCol(prop.ObjectName, prop.ColumnName)
	}
	return odCol(alias, prop.ColumnName)
}

// dateGranExpr wraps a column expression in TO_CHAR(<expr>::timestamp, fmt).
// The format string is a constant from a closed allow-list (set in
// stripDateGranularity), so passing it as a goqu literal placeholder is safe.
func dateGranExpr(col exp.Expression, format string) exp.LiteralExpression {
	// goqu.L parameterises ?-placeholders with the trailing args. col is
	// rendered as its quoted identifier; format ends up as a literal string
	// (single-quote-escaped by the dialect).
	return goqu.L("TO_CHAR(? :: timestamp, ?)", col, format)
}

// ── SELECT ──

// selectExprsV2 returns goqu SELECT expressions for groupBy + aggregates.
// Column refs and aggregates are native goqu expressions — no goqu.L
// raw-SQL escape hatch except for date granularity (which uses parameter
// placeholders, not string concat).
func selectExprsV2(rq *ResolvedLakehouseQuery, alias string, multi bool) []interface{} {
	// Detect output-label collisions among groupBy dims. When two joined ODs
	// expose a same-named column (e.g. INGREDIENT.name AND MENUITEM.name both
	// → "name"), projecting both as the bare name silently clobbers one in the
	// result rows. Colliding dims get an OD-qualified label ("INGREDIENT.name")
	// so each survives; non-colliding dims keep the bare name (no change to the
	// common single-OD / no-collision case → response templates + pivots that
	// reference the bare label are unaffected).
	labelCount := map[string]int{}
	for _, gb := range rq.GroupByCols {
		if gb.Granularity == "" {
			labelCount[gb.Prop.Name]++
		}
	}
	var out []interface{}
	for _, gb := range rq.GroupByCols {
		col := propCol(gb.Prop, alias, multi)
		if gb.Granularity != "" {
			out = append(out, dateGranExpr(col, gb.Granularity).As(goqu.I(gb.OutputLabel)))
			continue
		}
		label := gb.Prop.Name
		if labelCount[label] > 1 && gb.Prop.ObjectName != "" {
			label = gb.Prop.ObjectName + "." + gb.Prop.Name
		}
		out = append(out, col.As(goqu.I(label)))
	}
	for _, agg := range rq.Aggregates {
		out = append(out, aggExprV2(agg, alias, multi))
	}
	return out
}

// aggExprV2 renders a ResolvedAggregate as a goqu expression with alias.
// Replaces v1's string-returning aggExpr / aggExprMulti.
func aggExprV2(agg smartquery.ResolvedAggregate, alias string, multi bool) exp.Expression {
	switch agg.Kind {
	case smartquery.AggCountRows:
		label := agg.Label
		if label == "" {
			label = "Count"
		}
		return goqu.COUNT(goqu.L("*")).As(goqu.I(label))
	case AggCustomSQL:
		// Custom SQL aggregate from ont_metric.sql_expression — opaque by
		// contract (operator owns the SQL safety).
		return goqu.L("(" + agg.Expr + ")").As(goqu.I(agg.Label))
	case smartquery.AggStandard:
		if agg.Prop == nil {
			return goqu.COUNT(goqu.L("*")).As(goqu.I("Count"))
		}
		col := propCol(*agg.Prop, alias, multi)
		fn := strings.ToUpper(agg.Func)
		label := agg.Label
		if label == "" {
			label = "Total_" + agg.Prop.Name
		}
		// Defensive numeric coercion — mirrors denseAggExpr in dense_sql.go.
		// ont_property.data_type may say "int8" while the physical column landed
		// as `text` (collector imports PBIX string-quoted numbers verbatim;
		// ontology learning re-types via sample values). Without a cast,
		// `sum(text)` errors out even when the values parse cleanly. We only
		// cast when the FUNCTION needs numeric AND the declared property type
		// is numeric — non-numeric properties hitting SUM/AVG were already
		// rejected by the resolver, and COUNT/DISTINCTCOUNT are type-agnostic
		// so we skip the cast there.
		aggCol := interface{}(col)
		if (fn == "SUM" || fn == "AVG" || fn == "AVERAGE" || fn == "MIN" || fn == "MAX") &&
			isNumericType(agg.Prop.DataType) {
			aggCol = goqu.Cast(col, "numeric")
		}
		switch fn {
		case "SUM":
			return goqu.SUM(aggCol).As(goqu.I(label))
		case "AVG", "AVERAGE":
			return goqu.AVG(aggCol).As(goqu.I(label))
		case "MIN":
			return goqu.MIN(aggCol).As(goqu.I(label))
		case "MAX":
			return goqu.MAX(aggCol).As(goqu.I(label))
		case "COUNT":
			return goqu.COUNT(col).As(goqu.I(label))
		case "DISTINCTCOUNT":
			return goqu.COUNT(goqu.L("DISTINCT ?", col)).As(goqu.I(label))
		default:
			return goqu.SUM(aggCol).As(goqu.I(label))
		}
	}
	return goqu.COUNT(goqu.L("*")).As(goqu.I("Count"))
}

// ── GROUP BY ──

// groupByExprsV2 returns the GROUP BY expressions matching SELECT's groupBy
// outputs. Granularity columns repeat the TO_CHAR expression on the column
// (Postgres collapses identical expressions across SELECT and GROUP BY).
func groupByExprsV2(rq *ResolvedLakehouseQuery, alias string, multi bool) []interface{} {
	if len(rq.GroupByCols) == 0 {
		return nil
	}
	var out []interface{}
	for _, gb := range rq.GroupByCols {
		col := propCol(gb.Prop, alias, multi)
		if gb.Granularity != "" {
			out = append(out, dateGranExpr(col, gb.Granularity))
		} else {
			out = append(out, col)
		}
	}
	return out
}

// ── HAVING ──

// havingExprV2 builds HAVING for MetricFilter (post-aggregation predicate).
// The aggregate is recomputed via aggExprV2 (without alias) and goqu's
// numeric-comparison helpers handle the operator.
func havingExprV2(rq *ResolvedLakehouseQuery) goqu.Expression {
	if rq.MetricFilter == nil || rq.MetricFilter.Op == "" || rq.MetricFilter.Value == "" || len(rq.Aggregates) == 0 {
		return nil
	}
	aggExpr := aggExprNoAlias(rq.Aggregates[0])
	if aggExpr == nil {
		return nil
	}
	return compareExpr(aggExpr, rq.MetricFilter.Op, rq.MetricFilter.Value, true)
}

// aggExprNoAlias returns the bare aggregate expression (no AS alias) for
// HAVING, where the aggregate is referenced by recomputing — Postgres can
// match it against the SELECT clause's identical expression.
func aggExprNoAlias(agg smartquery.ResolvedAggregate) exp.Expression {
	switch agg.Kind {
	case smartquery.AggCountRows:
		return goqu.COUNT(goqu.L("*"))
	case AggCustomSQL:
		return goqu.L("(" + agg.Expr + ")")
	case smartquery.AggStandard:
		if agg.Prop == nil {
			return goqu.COUNT(goqu.L("*"))
		}
		// Use the prop's owning Od as alias; HAVING never enters the
		// single-Od / multi-Od split because Postgres scopes HAVING to the
		// ungrouped FROM context, which already has every Od joined.
		col := odCol(agg.Prop.ObjectName, agg.Prop.ColumnName)
		fn := strings.ToUpper(agg.Func)
		switch fn {
		case "SUM":
			return goqu.SUM(col)
		case "AVG", "AVERAGE":
			return goqu.AVG(col)
		case "MIN":
			return goqu.MIN(col)
		case "MAX":
			return goqu.MAX(col)
		case "COUNT":
			return goqu.COUNT(col)
		case "DISTINCTCOUNT":
			return goqu.COUNT(goqu.L("DISTINCT ?", col))
		default:
			return goqu.SUM(col)
		}
	}
	return nil
}

// ── ORDER BY ──

// orderByExprsV2 builds ORDER BY expressions using goqu native identifiers.
func orderByExprsV2(rq *ResolvedLakehouseQuery, alias string, multi bool) []exp.OrderedExpression {
	if len(rq.OrderByCols) == 0 {
		return nil
	}
	var out []exp.OrderedExpression
	for _, ob := range rq.OrderByCols {
		ref := orderByRefV2(ob, rq, alias, multi)
		if strings.EqualFold(ob.Dir, "DESC") {
			out = append(out, ref.Desc())
		} else {
			out = append(out, ref.Asc())
		}
	}
	return out
}

// orderByRefV2 returns the goqu identifier the ORDER BY clause should sort
// on. Aggregate-by-index and custom labels both resolve to bare identifier
// references (which Postgres matches against the SELECT alias).
func orderByRefV2(ob smartquery.ResolvedOrderBy, rq *ResolvedLakehouseQuery, alias string, multi bool) exp.IdentifierExpression {
	switch ob.Kind {
	case smartquery.OrderByProp:
		if ob.Prop != nil {
			return propCol(*ob.Prop, alias, multi)
		}
		return goqu.I(ob.Label)
	case smartquery.OrderByAggregate:
		if ob.AggIndex >= 0 && ob.AggIndex < len(rq.Aggregates) {
			return goqu.I(rq.Aggregates[ob.AggIndex].Label)
		}
		return goqu.I("Count")
	case smartquery.OrderByCustomLabel:
		return goqu.I(ob.Label)
	}
	return goqu.I("Count")
}

// ── WHERE ──

// whereExprV2 builds the WHERE clause expression, grouping multi-eq filters
// on the same prop into IN(...) and AND-ing across props.
func whereExprV2(filters []smartquery.ResolvedFilter, alias string, multi bool) goqu.Expression {
	if len(filters) == 0 {
		return nil
	}
	groups := groupFilters(filters)
	var conds []goqu.Expression
	for _, g := range groups {
		col := propCol(g.prop, alias, multi)
		if e := filterGroupExprV2(col, g.filters); e != nil {
			conds = append(conds, e)
		}
	}
	if len(conds) == 0 {
		return nil
	}
	if len(conds) == 1 {
		return conds[0]
	}
	return goqu.And(conds...)
}

// filterGroupExprV2 builds a predicate covering one or more filters on the
// same column. Multiple equality filters → IN(...). Range / mixed → AND.
func filterGroupExprV2(col exp.IdentifierExpression, filters []smartquery.ResolvedFilter) goqu.Expression {
	if len(filters) == 0 {
		return nil
	}
	if len(filters) == 1 {
		return filterCondExprV2(col, filters[0])
	}

	allEq := true
	hasRange := false
	for _, f := range filters {
		if f.Op != "=" {
			allEq = false
		}
		if f.Op == ">" || f.Op == ">=" || f.Op == "<" || f.Op == "<=" {
			hasRange = true
		}
	}

	if allEq && !filters[0].FuzzyMatch {
		// Bug #9 fix: merge into IN(...). Non-numeric uses LOWER() on both
		// sides for case-insensitive semantics matching the singleton
		// ILIKE path.
		if !isNumericType(filters[0].Prop.DataType) {
			loweredVals := make([]interface{}, 0, len(filters))
			for _, f := range filters {
				loweredVals = append(loweredVals, goqu.Func("LOWER", f.Value))
			}
			return goqu.Func("LOWER", goqu.Cast(col, "TEXT")).In(loweredVals...)
		}
		vals := make([]interface{}, 0, len(filters))
		for _, f := range filters {
			vals = append(vals, f.Value)
		}
		return col.In(vals...)
	}

	var parts []goqu.Expression
	for _, f := range filters {
		if e := filterCondExprV2(col, f); e != nil {
			parts = append(parts, e)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return parts[0]
	}
	if hasRange || len(parts) > 1 {
		return goqu.And(parts...)
	}
	return parts[0]
}

// filterCondExprV2 renders a single filter as a goqu predicate, preserving
// all v1 operator semantics (fuzzy match, numeric-vs-text dispatch, between
// pair, IN list).
func filterCondExprV2(col exp.IdentifierExpression, f smartquery.ResolvedFilter) goqu.Expression {
	op := strings.ToLower(strings.TrimSpace(f.Op))
	if op == "" {
		op = "="
	}
	textCol := goqu.Cast(col, "TEXT")
	switch op {
	case "=":
		if f.FuzzyMatch {
			return textCol.ILike("%" + f.Value + "%")
		}
		if !isNumericType(f.Prop.DataType) {
			return textCol.ILike(f.Value)
		}
		// Numeric eq with the value rendered as a string literal — matches
		// v1's behaviour of `col = 'val'` (PG coerces if compatible).
		return col.Eq(f.Value)
	case "<>", "!=":
		if !isNumericType(f.Prop.DataType) {
			return textCol.NotILike(f.Value)
		}
		return col.Neq(f.Value)
	case ">", ">=", "<", "<=":
		return compareExpr(col, op, f.Value, isNumericType(f.Prop.DataType))
	case "contains":
		return textCol.ILike("%" + f.Value + "%")
	case "not contains":
		return textCol.NotILike("%" + f.Value + "%")
	case "starts with":
		return textCol.ILike(f.Value + "%")
	case "ends with":
		return textCol.ILike("%" + f.Value)
	case "like":
		// Caller supplies the wildcards (e.g. "%-12-%"); use the pattern
		// as-is. CAST AS TEXT so it works on date/numeric columns too.
		return textCol.ILike(f.Value)
	case "not like":
		return textCol.NotILike(f.Value)
	case "is blank":
		return goqu.Or(col.IsNull(), textCol.Eq(""))
	case "is not blank":
		return goqu.And(col.IsNotNull(), textCol.Neq(""))
	case "between":
		parts := strings.SplitN(f.Value, ",", 2)
		if len(parts) != 2 {
			return nil
		}
		lo := strings.TrimSpace(parts[0])
		hi := strings.TrimSpace(parts[1])
		if isNumericType(f.Prop.DataType) {
			return col.Between(goqu.Range(goqu.L(lo), goqu.L(hi)))
		}
		return col.Between(goqu.Range(lo, hi))
	case "in":
		items := strings.Split(f.Value, ",")
		vals := make([]interface{}, 0, len(items))
		for _, item := range items {
			vals = append(vals, strings.TrimSpace(item))
		}
		return col.In(vals...)
	case "not in":
		items := strings.Split(f.Value, ",")
		vals := make([]interface{}, 0, len(items))
		for _, item := range items {
			vals = append(vals, strings.TrimSpace(item))
		}
		return col.NotIn(vals...)
	default:
		// Unreachable for valid input: ResolveQuery gates every op through
		// isValidFilterOp before reaching here. If this branch is ever hit,
		// isValidFilterOp() and this switch have drifted out of sync — fail
		// visibly (empty result + a screaming SQL comment) instead of the
		// historical silent degradation to "=", which produced empty
		// results with no diagnosable cause.
		return goqu.L("FALSE /* SMARTQUERY-BUG: unhandled filter operator reached filterCondExprV2 default — sync isValidFilterOp() with this switch */")
	}
}

// compareExpr handles the four range operators (>/>=/</<=) for both numeric
// and text columns. The numeric flag controls whether the value is wrapped
// as a goqu literal (for raw numeric output) vs. a quoted string.
func compareExpr(left exp.Expression, op, value string, numeric bool) goqu.Expression {
	var right interface{}
	if numeric {
		right = goqu.L(value) // raw numeric — no quoting
	} else {
		right = value // string — goqu escapes
	}
	type cmp interface {
		Gt(interface{}) exp.BooleanExpression
		Gte(interface{}) exp.BooleanExpression
		Lt(interface{}) exp.BooleanExpression
		Lte(interface{}) exp.BooleanExpression
		Eq(interface{}) exp.BooleanExpression
		Neq(interface{}) exp.BooleanExpression
	}
	c, ok := left.(cmp)
	if !ok {
		// Fallback: literal-formatted comparison. Should never happen for
		// goqu identifier / function expressions which all implement cmp.
		if numeric {
			return goqu.L("? "+op+" "+value, left)
		}
		return goqu.L("? "+op+" ?", left, value)
	}
	switch op {
	case ">":
		return c.Gt(right)
	case ">=":
		return c.Gte(right)
	case "<":
		return c.Lt(right)
	case "<=":
		return c.Lte(right)
	case "=", "==":
		return c.Eq(right)
	case "<>", "!=":
		return c.Neq(right)
	}
	return c.Eq(right)
}

// ── Share-column outer wrapper ──

// wrapShareColumnV2 wraps inner SQL with an outer SELECT that adds
//
//	ROUND(metric * 100.0 / NULLIF(SUM(metric) OVER (), 0), 2) AS shareLabel.
//
// Mirrors dense_sql.go's share logic for non-dense paths so AddShareColumn
// works regardless of which build path produced the inner query. Stays as
// string assembly (not goqu) — `inner.* + ROUND(...)` over a sub-SELECT is
// simpler with templated text than threading an extra dataset through goqu.
func wrapShareColumnV2(innerSQL string, rq *ResolvedLakehouseQuery) string {
	shareLabel := rq.ShareLabel
	if shareLabel == "" {
		shareLabel = "占比"
	}
	metricLabel := rq.Aggregates[0].Label

	var sb strings.Builder
	sb.WriteString("SELECT _share_inner.*,\n")
	sb.WriteString(fmt.Sprintf(
		"  ROUND(_share_inner.%s * 100.0 / NULLIF(SUM(_share_inner.%s) OVER (), 0), 2) AS %s\n",
		quoteIdent(metricLabel), quoteIdent(metricLabel), quoteIdent(shareLabel)))
	sb.WriteString(fmt.Sprintf("FROM (\n%s\n) _share_inner", innerSQL))
	return sb.String()
}
