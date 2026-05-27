package sqlrewrite

// parse_metric.go — bare-metric SQL decomposition.
//
// The new "simple" metric model stores ONLY the bare measure SQL — what the
// metric is, not what a query will eventually look like:
//
//   select "ORDER_TYPE", sum("ORDER_QUANTITY") as qty
//   from "EARLY_ORDER"
//   group by "ORDER_TYPE"
//
// ParseBareMetricSQL splits that into the three pieces the runtime needs:
//
//   primaryOD = "EARLY_ORDER"
//   measure   = `sum("ORDER_QUANTITY")`        (first aggregate; alias stripped)
//   baseDims  = ["ORDER_TYPE"]                   (non-aggregate select items)
//
// The metric editor parses + persists these on save; at query time the engine
// consumes the structured fields directly (no string surgery on user SQL). All
// JSON-driven runtime behavior — extra dim columns, cross-OD JOIN, filters —
// is layered on top via the existing structured assembly (promoteFilterProps
// ToGroupBy + ensureObjectsCoverReferencedProps + ResolveJoinPath).
//
// Accepted shape:
//   select <col-or-agg>, ..., from "<OD>" [where ...] [group by <col>, ...]
//     [order by ...] [limit ...]
//
// Rejected shapes (clear errors; author falls back to legacy `{sys.x}` /
// level='sql' for these exotic cases):
//   - JOIN / subquery / CTE in FROM
//   - multi-OD reference
//   - no FROM, no SELECT
//   - zero aggregates (a "metric" without a measure is not a metric)
//   - non-aggregate select item missing from GROUP BY (would be invalid SQL)
//
// Pure module — no DB, no service imports.

import (
	"fmt"
	"regexp"
	"strings"
)

// aggFuncs lists the aggregate functions recognized by the parser. The set
// mirrors what tool_compose_query.go's allowedAggregators accepts so the
// authored measure won't surprise the runtime.
var aggFuncs = map[string]bool{
	"sum":            true,
	"avg":            true,
	"min":            true,
	"max":            true,
	"count":          true,
	"distinct_count": true,
	"distinctcount":  true,
}

// Top-level clause keywords, in the order a well-formed SELECT can present
// them. Multi-word keywords ("group by" / "order by") are matched as a single
// unit (with arbitrary whitespace between the two words handled by the
// pre-normalization in splitTopLevelClauses).
var clauseKws = []string{"select", "from", "where", "group by", "having", "order by", "limit"}

// ParseBareMetricSQL decomposes a single-layer aggregation SELECT into its
// structural pieces. See the file header for the accepted/rejected shape.
//
// The returned `measure` keeps the original quoted-identifier form so the
// runtime can pass it through the resolver unchanged. `baseDims` are stripped
// of surrounding quotes; the runtime quotes them as identifiers itself.
func ParseBareMetricSQL(rawSQL string) (primaryOD, measure string, baseDims []string, err error) {
	sqlText := stripSQLComments(rawSQL)
	// Normalize whitespace so multi-word keywords ("group by") match cleanly
	// regardless of source formatting.
	sqlText = regexp.MustCompile(`\s+`).ReplaceAllString(sqlText, " ")
	sqlText = strings.TrimSpace(strings.TrimRight(sqlText, ";"))
	if sqlText == "" {
		return "", "", nil, fmt.Errorf("空 SQL")
	}

	clauses, err := splitTopLevelClauses(sqlText)
	if err != nil {
		return "", "", nil, err
	}
	selectText, ok := clauses["select"]
	if !ok {
		return "", "", nil, fmt.Errorf("缺少 SELECT 子句")
	}
	fromText, ok := clauses["from"]
	if !ok {
		return "", "", nil, fmt.Errorf("缺少 FROM 子句")
	}

	// FROM must be a single OD: reject JOIN and subqueries in FROM. The
	// runtime adds cross-OD JOINs from ont_causality at query time.
	if regexp.MustCompile(`(?i)\bjoin\b`).MatchString(fromText) {
		return "", "", nil, fmt.Errorf("口径 SQL 不允许 JOIN — 跨 OD 关联由运行时基于本体关系自动生成")
	}
	if strings.Contains(fromText, "(") {
		return "", "", nil, fmt.Errorf("口径 SQL 不支持 FROM 里的子查询/嵌套")
	}
	refs := ExtractReferencedNames(sqlText)
	if len(refs) == 0 {
		return "", "", nil, fmt.Errorf("无法解析 FROM 中的 OD 名")
	}
	if len(refs) > 1 {
		return "", "", nil, fmt.Errorf("口径 SQL 必须基于单个 OD,当前引用了: %v", refs)
	}
	primaryOD = refs[0]

	// Split select items at top-level commas (paren-aware so `sum(a, b)` stays
	// intact). Each item is classified as aggregate (→ measure) or non-aggregate
	// (→ dimension). The FIRST aggregate becomes canonical_metric; additional
	// aggregates are rejected for the simple form (one measure per metric).
	items := splitTopLevelCommas(selectText)
	if len(items) == 0 {
		return "", "", nil, fmt.Errorf("SELECT 列表为空")
	}
	var aggregates []string
	var dims []string
	for _, raw := range items {
		itemTrim := strings.TrimSpace(raw)
		body, _ := stripSelectAlias(itemTrim)
		if isAggregateExpr(body) {
			// Preserve the ORIGINAL form (including any `AS <alias>` suffix) so the
			// runtime resolver can lift the author's output label out of the stored
			// canonical_metric. The classification check above used the alias-
			// stripped body; the stored expression keeps the full text.
			aggregates = append(aggregates, itemTrim)
		} else {
			d := stripQuotedIdent(body)
			if d == "" {
				return "", "", nil, fmt.Errorf("无法解析 SELECT 中的列: %q", raw)
			}
			dims = append(dims, d)
		}
	}
	if len(aggregates) == 0 {
		return "", "", nil, fmt.Errorf("口径 SQL 必须至少包含一个聚合(sum/avg/count/min/max/distinct_count)")
	}
	if len(aggregates) > 1 {
		return "", "", nil, fmt.Errorf("简单口径只允许一个聚合度量,检测到 %d 个: %v", len(aggregates), aggregates)
	}
	measure = aggregates[0]
	baseDims = dims

	// baseDims must align with GROUP BY (Postgres would reject otherwise; we
	// surface a clear error before persisting).
	gbText, hasGB := clauses["group by"]
	if len(dims) > 0 && !hasGB {
		return "", "", nil, fmt.Errorf("有非聚合列 %v 但缺少 GROUP BY", dims)
	}
	if hasGB {
		gbSet := map[string]bool{}
		for _, g := range splitTopLevelCommas(gbText) {
			gbSet[strings.ToLower(stripQuotedIdent(strings.TrimSpace(g)))] = true
		}
		for _, d := range dims {
			if !gbSet[strings.ToLower(d)] {
				return "", "", nil, fmt.Errorf("非聚合列 %q 未出现在 GROUP BY 中", d)
			}
		}
	}
	return primaryOD, measure, baseDims, nil
}

// stripSQLComments removes `--` line and `/* */` block comments. Shared with
// the rest of sqlrewrite via a local copy (the canonical implementation in
// RejectDDL operates on the post-substitution text; this one runs PRE-parse).
func stripSQLComments(s string) string {
	s = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`/\*[\s\S]*?\*/`).ReplaceAllString(s, " ")
	return s
}

// splitTopLevelClauses tokenizes a normalized single-SELECT into its clause
// bodies. Keyed by clause keyword (lowercase, e.g. "group by"); body is the
// trimmed text between this keyword and the next. Paren depth is tracked so
// keywords inside function calls / scalar subqueries are NOT split on.
func splitTopLevelClauses(sql string) (map[string]string, error) {
	type mark struct {
		kw         string
		start, end int
	}
	lower := strings.ToLower(sql)
	var marks []mark
	depth := 0
	i := 0
	for i < len(lower) {
		c := lower[i]
		if c == '(' {
			depth++
			i++
			continue
		}
		if c == ')' {
			depth--
			i++
			continue
		}
		matched := false
		if depth == 0 {
			for _, kw := range clauseKws {
				end := i + len(kw)
				if end > len(lower) {
					continue
				}
				if lower[i:end] != kw {
					continue
				}
				leftOK := i == 0 || !isIdentByte(lower[i-1])
				rightOK := end == len(lower) || !isIdentByte(lower[end])
				if leftOK && rightOK {
					marks = append(marks, mark{kw: kw, start: i, end: end})
					i = end
					matched = true
					break
				}
			}
		}
		if !matched {
			i++
		}
	}
	if len(marks) == 0 || marks[0].kw != "select" {
		return nil, fmt.Errorf("SQL 必须以 SELECT 开头")
	}
	out := map[string]string{}
	for idx, m := range marks {
		var body string
		if idx+1 < len(marks) {
			body = sql[m.end:marks[idx+1].start]
		} else {
			body = sql[m.end:]
		}
		out[m.kw] = strings.TrimSpace(body)
	}
	return out, nil
}

// isIdentByte returns true for [A-Za-z0-9_], used to enforce word boundaries
// when matching clause keywords (so "fromage" doesn't match "from").
func isIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}

// splitTopLevelCommas splits a string by `,` at paren depth 0 so commas inside
// function arguments (e.g. `coalesce(a, b)`) don't fragment the item.
func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// isAggregateExpr reports whether expr starts with a recognized aggregate
// function call (case-insensitive). Window-style aggregates (with OVER) are
// NOT treated as plain aggregates here — the runtime structured engine doesn't
// model them; an authoring shape that needs OVER goes to the legacy escape
// hatch (level='sql').
func isAggregateExpr(expr string) bool {
	m := regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_]*)\s*\(`).FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return false
	}
	if !aggFuncs[strings.ToLower(m[1])] {
		return false
	}
	if regexp.MustCompile(`(?i)\bover\s*\(`).MatchString(expr) {
		return false
	}
	return true
}

// stripSelectAlias splits a select item into (body, alias) by recognizing an
// explicit `AS alias` suffix. A BARE trailing identifier (no AS) is left in
// place — too risky to strip without confusing `from_date` with `as date`.
//
// Two patterns mirror the runtime resolver (lakehouse-sql-server/lakehouse
// stripMetricAlias): PostgreSQL allows `as` glued directly to `)` / `"`, so we
// accept a zero-space gap only when the body ends with one of those chars.
// Otherwise we still require whitespace before `as` to avoid mis-splitting
// names like `column_as`.
var (
	selectAliasTightRe = regexp.MustCompile(`(?is)^(.+?[)"])\s*as\s+("?[A-Za-z_][A-Za-z0-9_]*"?)\s*$`)
	selectAliasLooseRe = regexp.MustCompile(`(?is)^(.+?)\s+as\s+("?[A-Za-z_][A-Za-z0-9_]*"?)\s*$`)
)

func stripSelectAlias(item string) (string, string) {
	s := strings.TrimSpace(item)
	if m := selectAliasTightRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), strings.Trim(m[2], `"`)
	}
	if m := selectAliasLooseRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), strings.Trim(m[2], `"`)
	}
	return s, ""
}

// stripQuotedIdent removes a surrounding `"..."` pair, doubling-back any
// escaped `""`. Bare identifiers are returned unchanged.
func stripQuotedIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}
