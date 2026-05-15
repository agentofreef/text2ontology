package lakehouse

import (
	"fmt"
	"strings"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// NOTE — this file is the home of the SQL-string helpers shared across the
// goqu builder (sql_builder_v2.go) and the dense builder (dense_sql.go).
// Layer 1 (Ontology preview) is no longer a parallel implementation: P4
// folded it into BuildOntologySQLV2 which routes through the same goqu
// pipeline as the physical builder, only swapping the FROM source.
//
// The retained helpers are:
//   - groupFilters / buildFilterGroupExpr / buildFilterCondition (dense)
//   - buildHavingClause (dense)
//   - aggExpr / aggExprMulti / aggExprRaw / sqlAggFunc (dense)
//   - wrapDivisionSafety / quoteExprLabel(s) (derived metric outer wrap)
//   - dateGranularityExpr (dense)
//   - colRefSingle / colRefMulti (dense)
//   - quoteIdent / quoteColRef / sanitizeAlias / escapeSQLString (dense)
//   - orderByRefDerived (derived metric outer wrap)
// A future cleanup can rewrite dense_sql.go on top of goqu native, at
// which point most of these helpers can finally retire.

// ── Filter grouping helpers (shared by v2 + dense) ──

// filterGroup groups filters by the same property for OR/IN merging.
type filterGroup struct {
	prop    smartquery.PropertyInfo
	filters []smartquery.ResolvedFilter
}

func groupFilters(filters []smartquery.ResolvedFilter) []filterGroup {
	seen := map[string]int{} // propKey → index in groups
	var groups []filterGroup
	for _, f := range filters {
		key := f.Prop.ObjectName + "." + f.Prop.Name
		if idx, ok := seen[key]; ok {
			groups[idx].filters = append(groups[idx].filters, f)
		} else {
			seen[key] = len(groups)
			groups = append(groups, filterGroup{prop: f.Prop, filters: []smartquery.ResolvedFilter{f}})
		}
	}
	return groups
}

func buildFilterGroupExpr(colRef string, filters []smartquery.ResolvedFilter) string {
	if len(filters) == 1 {
		return buildFilterCondition(colRef, filters[0])
	}

	// Check if all are equality filters → merge into IN(...)
	allEquality := true
	hasRange := false
	for _, f := range filters {
		if f.Op != "=" {
			allEquality = false
		}
		if f.Op == ">" || f.Op == ">=" || f.Op == "<" || f.Op == "<=" {
			hasRange = true
		}
	}

	if allEquality && !filters[0].FuzzyMatch {
		// Bug #9 fix: merge into IN(...)
		// For non-numeric columns, use LOWER()-based case-insensitive IN.
		if len(filters) > 0 && !isNumericType(filters[0].Prop.DataType) {
			var vals []string
			for _, f := range filters {
				vals = append(vals, "LOWER('"+escapeSQLString(f.Value)+"')")
			}
			return fmt.Sprintf("LOWER(CAST(%s AS TEXT)) IN (%s)", colRef, strings.Join(vals, ", "))
		}
		var vals []string
		for _, f := range filters {
			vals = append(vals, "'"+escapeSQLString(f.Value)+"'")
		}
		return fmt.Sprintf("%s IN (%s)", colRef, strings.Join(vals, ", "))
	}

	// Range or mixed: combine with AND
	var parts []string
	for _, f := range filters {
		c := buildFilterCondition(colRef, f)
		if c != "" {
			parts = append(parts, c)
		}
	}
	if hasRange || len(parts) > 1 {
		return "(" + strings.Join(parts, " AND ") + ")"
	}
	return strings.Join(parts, " AND ")
}

func buildFilterCondition(colRef string, f smartquery.ResolvedFilter) string {
	val := escapeSQLString(f.Value)
	switch f.Op {
	case "=":
		if f.FuzzyMatch {
			return fmt.Sprintf("CAST(%s AS TEXT) ILIKE '%%%s%%'", colRef, val)
		}
		// Case-insensitive equals for text columns; strict = for numeric.
		if !isNumericType(f.Prop.DataType) {
			return fmt.Sprintf("CAST(%s AS TEXT) ILIKE '%s'", colRef, val)
		}
		return fmt.Sprintf("%s = '%s'", colRef, val)
	case "<>", "!=":
		if !isNumericType(f.Prop.DataType) {
			return fmt.Sprintf("CAST(%s AS TEXT) NOT ILIKE '%s'", colRef, val)
		}
		return fmt.Sprintf("%s <> '%s'", colRef, val)
	case ">", ">=", "<", "<=":
		if isNumericType(f.Prop.DataType) {
			return fmt.Sprintf("%s %s %s", colRef, f.Op, val)
		}
		return fmt.Sprintf("%s %s '%s'", colRef, f.Op, val)
	case "contains":
		return fmt.Sprintf("CAST(%s AS TEXT) ILIKE '%%%s%%'", colRef, val)
	case "not contains":
		return fmt.Sprintf("CAST(%s AS TEXT) NOT ILIKE '%%%s%%'", colRef, val)
	case "starts with":
		return fmt.Sprintf("CAST(%s AS TEXT) ILIKE '%s%%'", colRef, val)
	case "ends with":
		return fmt.Sprintf("CAST(%s AS TEXT) ILIKE '%%%s'", colRef, val)
	case "is blank":
		return fmt.Sprintf("(%s IS NULL OR CAST(%s AS TEXT) = '')", colRef, colRef)
	case "is not blank":
		return fmt.Sprintf("(%s IS NOT NULL AND CAST(%s AS TEXT) <> '')", colRef, colRef)
	case "between":
		parts := strings.SplitN(f.Value, ",", 2)
		if len(parts) == 2 {
			lo := escapeSQLString(strings.TrimSpace(parts[0]))
			hi := escapeSQLString(strings.TrimSpace(parts[1]))
			if isNumericType(f.Prop.DataType) {
				return fmt.Sprintf("%s BETWEEN %s AND %s", colRef, lo, hi)
			}
			return fmt.Sprintf("%s BETWEEN '%s' AND '%s'", colRef, lo, hi)
		}
		return ""
	case "in":
		items := strings.Split(f.Value, ",")
		var quoted []string
		for _, item := range items {
			quoted = append(quoted, "'"+escapeSQLString(strings.TrimSpace(item))+"'")
		}
		return fmt.Sprintf("%s IN (%s)", colRef, strings.Join(quoted, ", "))
	case "not in":
		items := strings.Split(f.Value, ",")
		var quoted []string
		for _, item := range items {
			quoted = append(quoted, "'"+escapeSQLString(strings.TrimSpace(item))+"'")
		}
		return fmt.Sprintf("%s NOT IN (%s)", colRef, strings.Join(quoted, ", "))
	default:
		return fmt.Sprintf("%s = '%s'", colRef, val)
	}
}

// ── HAVING helper ──

func buildHavingClause(rq *ResolvedLakehouseQuery) string {
	if rq.MetricFilter == nil || rq.MetricFilter.Op == "" || rq.MetricFilter.Value == "" || len(rq.Aggregates) == 0 {
		return ""
	}
	agg := rq.Aggregates[0]
	expr := aggExprRaw(agg, "")
	return fmt.Sprintf("HAVING %s %s %s", expr, rq.MetricFilter.Op, rq.MetricFilter.Value)
}

// orderByRefDerived handles ORDER BY in the outer derived-metric query.
// Can reference both aggregate labels and derived metric names.
func orderByRefDerived(ob smartquery.ResolvedOrderBy, rq *ResolvedLakehouseQuery) string {
	switch ob.Kind {
	case smartquery.OrderByCustomLabel:
		return quoteIdent(ob.Label)
	case smartquery.OrderByAggregate:
		if ob.AggIndex >= 0 && ob.AggIndex < len(rq.Aggregates) {
			return quoteIdent(rq.Aggregates[ob.AggIndex].Label)
		}
	case smartquery.OrderByProp:
		if ob.Prop != nil {
			return quoteIdent(ob.Prop.Name)
		}
	}
	// Check derived metric names.
	for _, d := range rq.Derived {
		if strings.EqualFold(ob.Label, d.Name) {
			return quoteIdent(d.Name)
		}
	}
	return quoteIdent(ob.Label)
}

// ── Aggregate expression helpers ──

// aggExpr generates a single-Od aggregate expression with alias.
func aggExpr(agg smartquery.ResolvedAggregate, alias string) string {
	switch agg.Kind {
	case smartquery.AggCountRows:
		return fmt.Sprintf("COUNT(*) AS %s", quoteIdent(agg.Label))
	case AggCustomSQL:
		return fmt.Sprintf("(%s) AS %s", agg.Expr, quoteIdent(agg.Label))
	case smartquery.AggStandard:
		if agg.Prop == nil {
			return fmt.Sprintf("COUNT(*) AS %s", quoteIdent("Count"))
		}
		col := colRefSingle(*agg.Prop, alias)
		fn := strings.ToUpper(agg.Func)
		// Bug #3 fix: DISTINCTCOUNT → COUNT(DISTINCT col)
		if fn == "DISTINCTCOUNT" {
			return fmt.Sprintf("COUNT(DISTINCT %s) AS %s", col, quoteIdent(agg.Label))
		}
		return fmt.Sprintf("%s(%s) AS %s", sqlAggFunc(fn), col, quoteIdent(agg.Label))
	default:
		return fmt.Sprintf("COUNT(*) AS %s", quoteIdent("Count"))
	}
}

// aggExprMulti generates a multi-Od aggregate expression.
func aggExprMulti(agg smartquery.ResolvedAggregate) string {
	switch agg.Kind {
	case smartquery.AggCountRows:
		return fmt.Sprintf("COUNT(*) AS %s", quoteIdent(agg.Label))
	case AggCustomSQL:
		return fmt.Sprintf("(%s) AS %s", agg.Expr, quoteIdent(agg.Label))
	case smartquery.AggStandard:
		if agg.Prop == nil {
			return fmt.Sprintf("COUNT(*) AS %s", quoteIdent("Count"))
		}
		col := colRefMulti(*agg.Prop)
		fn := strings.ToUpper(agg.Func)
		if fn == "DISTINCTCOUNT" {
			return fmt.Sprintf("COUNT(DISTINCT %s) AS %s", col, quoteIdent(agg.Label))
		}
		return fmt.Sprintf("%s(%s) AS %s", sqlAggFunc(fn), col, quoteIdent(agg.Label))
	default:
		return fmt.Sprintf("COUNT(*) AS %s", quoteIdent("Count"))
	}
}

// aggExprRaw returns the aggregate expression without alias (for HAVING).
func aggExprRaw(agg smartquery.ResolvedAggregate, alias string) string {
	switch agg.Kind {
	case smartquery.AggCountRows:
		return "COUNT(*)"
	case AggCustomSQL:
		return "(" + agg.Expr + ")"
	case smartquery.AggStandard:
		if agg.Prop == nil {
			return "COUNT(*)"
		}
		col := quoteIdent(agg.Prop.ColumnName)
		if alias != "" {
			col = quoteIdent(alias) + "." + col
		}
		fn := strings.ToUpper(agg.Func)
		if fn == "DISTINCTCOUNT" {
			return fmt.Sprintf("COUNT(DISTINCT %s)", col)
		}
		return fmt.Sprintf("%s(%s)", sqlAggFunc(fn), col)
	default:
		return "COUNT(*)"
	}
}

// sqlAggFunc maps aggregate function names to SQL.
func sqlAggFunc(fn string) string {
	switch strings.ToUpper(fn) {
	case "SUM":
		return "SUM"
	case "AVG", "AVERAGE":
		return "AVG"
	case "MIN":
		return "MIN"
	case "MAX":
		return "MAX"
	case "COUNT":
		return "COUNT"
	default:
		return "SUM"
	}
}

// ── Derived metric helpers ──

// wrapDivisionSafety takes a derived expression like "Total_Revenue / Count"
// and replaces known aggregate labels with quoted identifiers, wrapping divisors in NULLIF.
func wrapDivisionSafety(expr string, aggs []smartquery.ResolvedAggregate) string {
	// Build label set for matching.
	labelSet := map[string]string{} // lower → original label
	for _, agg := range aggs {
		labelSet[strings.ToLower(agg.Label)] = agg.Label
	}

	// Split on "/" to find division.
	parts := strings.SplitN(expr, "/", 2)
	if len(parts) == 2 {
		numerator := strings.TrimSpace(parts[0])
		denominator := strings.TrimSpace(parts[1])
		numerator = quoteExprLabel(numerator, labelSet)
		denominator = quoteExprLabel(denominator, labelSet)
		return fmt.Sprintf("%s / NULLIF(%s, 0)", numerator, denominator)
	}

	// No division: just quote labels.
	return quoteExprLabels(expr, labelSet)
}

// quoteExprLabel replaces a single token with its quoted identifier if it matches an aggregate label.
func quoteExprLabel(token string, labelSet map[string]string) string {
	token = strings.TrimSpace(token)
	if label, ok := labelSet[strings.ToLower(token)]; ok {
		return quoteIdent(label)
	}
	// Try stripping parens: (expr)
	if strings.HasPrefix(token, "(") && strings.HasSuffix(token, ")") {
		inner := token[1 : len(token)-1]
		if label, ok := labelSet[strings.ToLower(strings.TrimSpace(inner))]; ok {
			return quoteIdent(label)
		}
	}
	return quoteIdent(token)
}

// quoteExprLabels replaces all known labels in an expression with quoted identifiers.
func quoteExprLabels(expr string, labelSet map[string]string) string {
	result := expr
	for lower, label := range labelSet {
		// Case-insensitive replace of whole words.
		idx := strings.Index(strings.ToLower(result), lower)
		if idx >= 0 {
			result = result[:idx] + quoteIdent(label) + result[idx+len(lower):]
		}
	}
	return result
}

// ── Date granularity ──

func dateGranularityExpr(colRef, granularity string) string {
	switch granularity {
	case "YYYY-MM":
		return fmt.Sprintf("TO_CHAR(%s::timestamp, 'YYYY-MM')", colRef)
	case "YYYY":
		return fmt.Sprintf("TO_CHAR(%s::timestamp, 'YYYY')", colRef)
	case "YYYY-MM-DD":
		return fmt.Sprintf("TO_CHAR(%s::timestamp, 'YYYY-MM-DD')", colRef)
	case "YYYY-Q":
		return fmt.Sprintf("TO_CHAR(%s::timestamp, 'YYYY') || '-Q' || EXTRACT(QUARTER FROM %s::timestamp)::text", colRef, colRef)
	case "YYYY-WW":
		return fmt.Sprintf("TO_CHAR(%s::timestamp, 'IYYY-\"W\"IW')", colRef)
	case "YYYY-MM-DD HH":
		return fmt.Sprintf("TO_CHAR(%s::timestamp, 'YYYY-MM-DD HH24')", colRef)
	default:
		return colRef
	}
}

// ── Column reference helpers (Bug #7: use ColumnName for SQL, Name for labels) ──

// colRefSingle returns a qualified column reference for single-Od queries.
// Uses prop.ColumnName (physical column) for SQL generation.
func colRefSingle(prop smartquery.PropertyInfo, alias string) string {
	// Use quoteIdent for alias (table name) to preserve Od name casing
	// Use quoteColRef for column name to avoid case-sensitivity issues
	return quoteIdent(alias) + "." + quoteColRef(prop.ColumnName)
}

// colRefMulti returns a qualified column reference for multi-Od queries.
func colRefMulti(prop smartquery.PropertyInfo) string {
	a := sanitizeAlias(prop.ObjectName)
	return quoteIdent(a) + "." + quoteColRef(prop.ColumnName)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteColRef quotes a column reference for use in SQL.
// Uses quoteIdent (preserving original case with double-quotes) so the reference
// exactly matches the canonical_query's quoted column aliases. ColumnName is
// always set to property.Name during resolution, and canonical_query aliases
// columns to the same property names — so exact-case quoted matching is safe.
func quoteColRef(s string) string {
	return quoteIdent(s)
}

func sanitizeAlias(name string) string {
	return name
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
