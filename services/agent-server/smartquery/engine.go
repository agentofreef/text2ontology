package smartquery

import (
	"fmt"
	"strings"
)

// NormalizeQuerySpec coerces operator aliases, strips agg wrappers from metric/orderBy,
// strips object prefixes, and applies v2 defaults. This is the bridge from the LLM
// tool-call shape to a clean QuerySpec.
func NormalizeQuerySpec(raw map[string]interface{}) QuerySpec {
	spec := QuerySpec{
		DisplayMode: "table",
		Limit:       1000,
	}

	// Objects.
	if objs, ok := raw["objects"].([]interface{}); ok {
		for _, o := range objs {
			spec.Objects = append(spec.Objects, fmt.Sprintf("%v", o))
		}
	}

	// Metric — strip agg wrapper.
	if m, ok := raw["metric"].(string); ok {
		spec.Metric = stripAggWrapper(m)
	}

	// GroupBy.
	if gbs, ok := raw["groupBy"].([]interface{}); ok {
		for _, g := range gbs {
			// Strip object prefix.
			propName := fmt.Sprintf("%v", g)
			if parts := strings.SplitN(propName, ".", 2); len(parts) == 2 {
				propName = parts[1]
			}
			spec.GroupBy = append(spec.GroupBy, propName)
		}
	}

	// Filters.
	if fs, ok := raw["filters"].([]interface{}); ok {
		for _, f := range fs {
			if fm, ok := f.(map[string]interface{}); ok {
				fuzzy, _ := fm["fuzzyMatch"].(bool)
				prop := ""
				for _, key := range []string{"prop", "property", "field", "column"} {
					if v, ok := fm[key].(string); ok && v != "" {
						prop = v
						break
					}
				}
				op := ""
				for _, key := range []string{"op", "operator", "condition"} {
					if v, ok := fm[key].(string); ok && v != "" {
						op = v
						break
					}
				}
				op = normalizeOperator(op)
				// LLM frequently emits filters without `op` (e.g. {prop,value}
				// only) — default to "=" instead of silently dropping the
				// filter, which used to produce queries with no WHERE clause
				// and confusingly "all-data" results.
				if op == "" || op == "<nil>" {
					op = "="
				}
				val := fmt.Sprintf("%v", fm["value"])
				if prop != "" {
					spec.Filters = append(spec.Filters, FilterItem{
						Prop: prop, Op: op, Value: val, FuzzyMatch: fuzzy,
					})
				}
			} else if fs, ok := f.(string); ok && fs != "" {
				if fi, ok := parseStringFilterItemNorm(fs); ok {
					spec.Filters = append(spec.Filters, fi)
				}
			}
		}
	}

	// OrderBy.
	if obs, ok := raw["orderBy"].([]interface{}); ok {
		for _, o := range obs {
			if om, ok := o.(map[string]interface{}); ok {
				obProp := ""
				for _, key := range []string{"prop", "property", "field", "column"} {
					if v, ok := om[key].(string); ok && v != "" {
						obProp = v
						break
					}
				}
				dir := ""
				for _, key := range []string{"dir", "order", "direction"} {
					if v, ok := om[key].(string); ok && v != "" {
						dir = strings.ToUpper(v)
						break
					}
				}
				if dir != "ASC" && dir != "DESC" {
					dir = "DESC" // default DESC for top-N queries
				}
				obProp = stripAggWrapper(obProp)
				if parts := strings.SplitN(obProp, ".", 2); len(parts) == 2 {
					obProp = parts[1]
				}
				if obProp != "" {
					spec.OrderBy = append(spec.OrderBy, OrderByItem{Prop: obProp, Dir: dir})
				}
			}
		}
	}

	// Limit.
	if lv, ok := raw["limit"].(float64); ok && lv > 0 {
		spec.Limit = int(lv)
	}

	// DisplayMode.
	if dm, ok := raw["displayMode"].(string); ok && dm != "" {
		spec.DisplayMode = dm
	}

	// DerivedMetric.
	if dm, ok := raw["derivedMetric"].(map[string]interface{}); ok {
		name, _ := dm["name"].(string)
		expr, _ := dm["expression"].(string)
		baseTable, _ := dm["baseTable"].(string)
		if name != "" && expr != "" {
			spec.Derived = append(spec.Derived, DerivedMetricDef{Name: name, Expression: expr, BaseTable: baseTable})
		}
	}

	// DenseGroups — explicit opt-out of empty-group inclusion.
	// Default behavior (nil) is engine-specific. Lakehouse engine treats nil as true.
	if dg, ok := raw["denseGroups"].(bool); ok {
		spec.DenseGroups = &dg
	}

	// AddShareColumn — universal "占比" trigger from LLM. Engine wraps the
	// query with `metric / SUM(metric) OVER ()` * 100 as a share column.
	if asc, ok := raw["addShareColumn"].(bool); ok {
		spec.AddShareColumn = asc
	}
	if sl, ok := raw["shareLabel"].(string); ok && strings.TrimSpace(sl) != "" {
		spec.ShareLabel = strings.TrimSpace(sl)
	}

	// MetricFilter.
	if mf, ok := raw["metricFilter"].(map[string]interface{}); ok {
		mfOp, _ := mf["op"].(string)
		mfVal, _ := mf["value"].(string)
		if mfVal == "" {
			if v, ok := mf["value"].(float64); ok {
				mfVal = fmt.Sprintf("%g", v)
			}
		}
		if mfOp != "" && mfVal != "" {
			spec.MetricFilter = &MetricFilter{Op: mfOp, Value: mfVal}
		}
	}

	return spec
}

// normalizeOperator maps operator aliases to canonical forms.
func normalizeOperator(op string) string {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "equal", "equals", "eq", "==":
		return "="
	case "not equal", "neq", "ne", "!=", "<>":
		return "<>"
	case "gte", "greater than or equal":
		return ">="
	case "lte", "less than or equal":
		return "<="
	case "gt", "greater than":
		return ">"
	case "lt", "less than":
		return "<"
	case "contains", "includes", "contains string":
		return "contains"
	case "like":
		return "like"
	case "not contains", "not like", "doesnt contain", "doesn't contain":
		return "not contains"
	case "starts with", "startswith":
		return "starts with"
	case "ends with", "endswith":
		return "ends with"
	case "in":
		return "in"
	case "not in":
		return "not in"
	case "is blank", "is empty", "is null", "isnull":
		return "is blank"
	case "is not blank", "is not empty", "is not null", "isnotnull":
		return "is not blank"
	case "between":
		return "between"
	case "regex":
		return "regex"
	default:
		return op
	}
}

// AppendGroupBy adds a prop token to spec.GroupBy, deduplicating via
// CanonicalPropKey. It is the SINGLE allowed mutation path for groupBy from
// spec-level passes (promote, Intent enforce, future pipeline stages).
//
// Rationale: multiple independent passes push into GroupBy using raw strings
// with / without `Od.` prefix, different casing, or granularity suffixes.
// Comparing raw strings misses these equivalent-but-different tokens, causing
// duplicate groupBy cols → duplicate SELECT aliases → Postgres "ambiguous
// column" errors downstream. Centralising the dedup here makes every pass
// idempotent AND commutative — satisfying the anti-seesaw contract.
//
// Returns true if appended, false if the key already existed.
//
// Safe to call repeatedly with the same token — second call is a no-op. Safe
// to call from passes that run in any order relative to each other.
func (s *QuerySpec) AppendGroupBy(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	key := CanonicalPropKey(token)
	if key == "" {
		return false
	}
	for _, g := range s.GroupBy {
		if CanonicalPropKey(g) == key {
			return false
		}
	}
	s.GroupBy = append(s.GroupBy, token)
	return true
}

// HasGroupBy reports whether spec.GroupBy already contains an entry matching
// token under CanonicalPropKey equivalence. Use this when a pass needs to
// branch on presence without unconditionally appending.
func (s *QuerySpec) HasGroupBy(token string) bool {
	key := CanonicalPropKey(token)
	if key == "" {
		return false
	}
	for _, g := range s.GroupBy {
		if CanonicalPropKey(g) == key {
			return true
		}
	}
	return false
}

// parseStringFilterItemNorm parses a string like "Order.prop >= 'value'" into a FilterItem.
func parseStringFilterItemNorm(s string) (FilterItem, bool) {
	s = strings.TrimSpace(s)
	for _, op := range []string{">=", "<=", "!=", ">", "<", "="} {
		idx := strings.Index(s, op)
		if idx < 0 {
			continue
		}
		prop := strings.TrimSpace(s[:idx])
		val := strings.TrimSpace(s[idx+len(op):])
		if len(val) >= 2 && ((val[0] == '\'' && val[len(val)-1] == '\'') || (val[0] == '"' && val[len(val)-1] == '"')) {
			val = val[1 : len(val)-1]
		}
		if prop == "" || val == "" {
			continue
		}
		return FilterItem{Prop: prop, Op: op, Value: val}, true
	}
	return FilterItem{}, false
}
