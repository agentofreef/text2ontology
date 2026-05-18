// compose_query — Tier 1 catalog-bound composition.
//
// Why this tool exists:
//   strict-mode smartquery only accepts {intent, params}. When the user
//   asks a question that no pre-built intent covers (e.g. "Beverages 类别下
//   每员工卖了多少" — needs filter on CategoryName + groupBy on EmployeeID,
//   a combination none of our 7 intents has), the LLM either picked a
//   close-but-wrong intent or refused to call smartquery at all. reflect
//   catches the first case; for the second case the LLM never called
//   smartquery so reflect couldn't fire.
//
//   compose_query lets the LLM build a QuerySpec directly from catalog
//   tokens (OD names, property names, aggregator funcs) — without writing
//   raw SQL. Every token is validated server-side against the project's
//   ontology before SQL generation. Failures return COMPOSE_FAILED with
//   a typed code so the LLM can self-correct or fall back to "I have the
//   closest intent's data + a gap explanation" style answers.
//
// Safety boundaries (vs Tier 2 free-SQL):
//   - LLM cannot write SQL — only emit {odName, metric, filters, groupBy}
//   - Every property name must resolve via the existing engine.Resolve
//     pipeline (same path strict-mode smartquery uses)
//   - Aggregator function whitelist: sum/avg/min/max/count/distinct_count
//   - Operator whitelist: =, !=, in, not_in, >, <, >=, <=, like, between
//   - Filter values are bound as parameters (the engine emits parameterised
//     SQL); LLM cannot inject SQL via value strings
//
// MVP scope (v1):
//   - Single primary OD (no cross-OD JOIN)
//   - All filter / groupBy properties must live on the primary OD's
//     denormalised projection. Cross-OD attribute pulls (Type B in the
//     design discussion) are deferred to v2 once we wire ont_link_type
//     traversal into the engine.
//
// The result shape mirrors lakehouseToolSmartQuery so reflect / synthesize
// can post-process it identically.

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/lakehouse2ontology/services/agent-server/smartquery"

	. "github.com/lakehouse2ontology/httputil"
)

const composeQueryToolDescription = `自由组合一次查询（catalog 受限版本）。
当 strict 模式 smartquery 没有完美匹配的 intent，且 reflect 已经判定 verdict=mismatch 时使用。
你不能写 SQL — 只能从已有 OD 的 property / 聚合函数白名单里挑组合。

入参：
  odName   string   主 OD 名（必填，**单个**）
  metric   string   聚合表达式，例 "sum(NetAmount)" / "count(id)" / "avg(UnitPrice)"
                    支持函数：sum / avg / min / max / count / distinct_count
                    ⚠ count 必须带 property 名（例 count(id)）；count(*) 不被引擎接受
                    （会造成 JOIN 双重计数，引擎选择强制显式 count(<id 列>) 而非 *）
  filters  array    过滤项 [{property, op, value}]，每个 property 必须是 odName 的一个 property
                    op 白名单：=, !=, >, <, >=, <=, in, not_in, like, between
  groupBy  array    分组列 ["property1","property2"]，必须是 odName 的 property
  orderBy  array    [{label,dir}] 可选；label 是结果列名（如 "Total_NetAmount"），dir = "ASC"|"DESC"
  limit    int      可选，>=1

返回：和 smartquery 一致的 result（execution_status / generated_sql / total_rows / execution_result / 等）。
失败时 error.code = COMPOSE_FAILED + 详细原因（哪个 token 不存在 / 哪个 op 不允许）。

MVP 限制：暂不支持跨 OD JOIN（v2 才加 1 跳）。所有 filter/groupBy 必须是主 OD 已有的 property。`

// metricExprRE matches "func(arg)" where arg is a property name.
// Examples: sum(NetAmount), count(id), avg(UnitPrice), distinct_count(CustomerID).
// NOTE: count(*) is rejected at validation time — see the metricArg check
// below — because the lakehouse-sql-server resolver intentionally refuses it
// to avoid silent JOIN double-counting (resolve.go: "no silent COUNT(*) fallback").
var metricExprRE = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\(\s*([^)]+?)\s*\)\s*$`)

// allowedOps mirrors what the smartquery resolver accepts. Anything not in
// here gets a clean COMPOSE_FAILED instead of an opaque resolver error.
var allowedOps = map[string]bool{
	"=": true, "!=": true, "<>": true,
	">": true, "<": true, ">=": true, "<=": true,
	"in": true, "not_in": true,
	"like": true, "ilike": true,
	"between": true,
	"is_null": true, "is_not_null": true,
}

// allowedAggregators is the function whitelist for compose_query metrics.
// Standard SQL aggregators only — no window functions, no custom UDFs.
var allowedAggregators = map[string]bool{
	"sum":            true,
	"avg":            true,
	"min":            true,
	"max":            true,
	"count":          true,
	"distinct_count": true,
	"distinctcount":  true,
}

// composeError builds the common error payload shape so the LLM can
// pattern-match on err.code. Mirrors strict-mode smartquery's error format.
func composeError(detail string, hint string) M {
	out := M{
		"error": fmt.Sprintf("COMPOSE_FAILED: %s", detail),
		"code":  "COMPOSE_FAILED",
	}
	if hint != "" {
		out["hint"] = hint
	}
	return out
}

// runComposeQueryTool is the dispatchTool handler for compose_query.
// Validates input, builds smartquery.QuerySpec without an IntentHint, and
// dispatches to the same SQL engine strict-mode uses.
func runComposeQueryTool(ctx context.Context, db *sql.DB, projectID, userQuestion string, args map[string]interface{}) M {
	odName := strings.TrimSpace(StrVal(args, "odName"))
	if odName == "" {
		return composeError("odName is required", "请填写主 OD 名（如 \"SALE\"）")
	}

	metric := strings.TrimSpace(StrVal(args, "metric"))
	if metric == "" {
		return composeError("metric is required", "形如 \"sum(NetAmount)\" 或 \"count(id)\"（count 需显式给 property，不接受 count(*)）")
	}

	// Parse and validate metric expression.
	m := metricExprRE.FindStringSubmatch(metric)
	if m == nil {
		return composeError(fmt.Sprintf("metric %q malformed; expected func(arg)", metric),
			"例：sum(NetAmount) / count(id) / avg(UnitPrice)")
	}
	fn := strings.ToLower(m[1])
	if !allowedAggregators[fn] {
		allowed := []string{"sum", "avg", "min", "max", "count", "distinct_count"}
		return composeError(fmt.Sprintf("aggregator %q not allowed", fn),
			"白名单：" + strings.Join(allowed, " / "))
	}

	// Validate odName exists + is active.
	var odID string
	err := db.QueryRowContext(ctx, `
		SELECT id FROM ont_object_type
		WHERE project_id=$1 AND name=$2 AND mark=true`,
		projectID, odName,
	).Scan(&odID)
	if err == sql.ErrNoRows {
		return composeError(fmt.Sprintf("OD %q not found or not active", odName),
			"先 list(type=ods) 看可用 OD 名")
	}
	if err != nil {
		return composeError("OD lookup DB error: "+err.Error(), "")
	}

	// Property cache per OD — primary loaded eagerly. Dim ODs are loaded
	// lazily as filter/groupBy references the first time we see "OD.Prop".
	odProps := map[string]map[string]bool{}
	odIDByName := map[string]string{odName: odID}
	primary, err := loadODPropertyNames(ctx, db, odID)
	if err != nil {
		return composeError("property lookup DB error: "+err.Error(), "")
	}
	if len(primary) == 0 {
		return composeError(fmt.Sprintf("OD %q has zero properties", odName),
			"先 inspect(target=<OD>, mode=schema) 检查")
	}
	odProps[odName] = primary

	// Track which dim ODs the LLM referenced — they all need to land in
	// spec.Objects so the engine's ResolveJoinPath finds JOIN edges via
	// ont_causality(relation_type='join_key').
	referencedDimODs := map[string]bool{}

	// resolveQualifiedProperty parses "OD.Prop" or bare "Prop" form.
	// Returns (resolvedRef, dimODName, errorMap). dimODName is "" when the
	// reference resolves on the primary OD. The resolvedRef preserves the
	// original token so the engine's `stripObjectPrefix` matches the right
	// OD via spec.Objects membership.
	resolveQualifiedProperty := func(rawRef, ctxLabel string) (string, string, M) {
		ref := strings.TrimSpace(rawRef)
		if ref == "" {
			return "", "", composeError(fmt.Sprintf("%s: property is required", ctxLabel), "")
		}
		// Strip date-granularity suffix early — engine handles that — but
		// preserve it on the returned token so spec.GroupBy keeps "(月)".
		bare := stripGranularitySuffix(ref)
		dotIdx := strings.Index(bare, ".")
		if dotIdx <= 0 {
			// Bare prop → must be on primary.
			if !primary[bare] {
				return "", "", composeError(
					fmt.Sprintf("%s: %q is not a property of primary OD %q", ctxLabel, bare, odName),
					"available on primary: "+strings.Join(sortedKeys(primary), ", ")+
						"  // 跨 OD 引用请用 'OD.Property' 形式（如 CUSTOMER.Country）")
			}
			return ref, "", nil
		}
		// Qualified OD.Prop form.
		dimOD := strings.TrimSpace(bare[:dotIdx])
		dimProp := strings.TrimSpace(bare[dotIdx+1:])
		if dimOD == "" || dimProp == "" {
			return "", "", composeError(fmt.Sprintf("%s: malformed OD.Property reference %q", ctxLabel, ref),
				"形如 CUSTOMER.Country")
		}
		// If the qualifier IS the primary OD, treat as primary ref.
		if dimOD == odName {
			if !primary[dimProp] {
				return "", "", composeError(
					fmt.Sprintf("%s: %q is not a property of OD %q", ctxLabel, dimProp, odName),
					"available: "+strings.Join(sortedKeys(primary), ", "))
			}
			return ref, "", nil
		}
		// Cross-OD ref. Lazy-load dim OD properties + validate.
		if odProps[dimOD] == nil {
			var dimID string
			if e := db.QueryRowContext(ctx, `
				SELECT id FROM ont_object_type
				WHERE project_id=$1 AND name=$2 AND mark=true`,
				projectID, dimOD,
			).Scan(&dimID); e != nil {
				if e == sql.ErrNoRows {
					return "", "", composeError(
						fmt.Sprintf("%s: dim OD %q not found or not active", ctxLabel, dimOD),
						"看 catalog 列出来的 OD 名")
				}
				return "", "", composeError("dim OD lookup DB error: "+e.Error(), "")
			}
			props, perr := loadODPropertyNames(ctx, db, dimID)
			if perr != nil {
				return "", "", composeError("dim property lookup DB error: "+perr.Error(), "")
			}
			odProps[dimOD] = props
			odIDByName[dimOD] = dimID
		}
		if !odProps[dimOD][dimProp] {
			return "", "", composeError(
				fmt.Sprintf("%s: %q is not a property of OD %q", ctxLabel, dimProp, dimOD),
				"available on "+dimOD+": "+strings.Join(sortedKeys(odProps[dimOD]), ", "))
		}
		referencedDimODs[dimOD] = true
		return ref, dimOD, nil
	}

	// Validate metric arg (the property the aggregator wraps). Metric arg
	// is constrained to the primary OD only — cross-OD aggregation needs a
	// derived metric definition, which is out of MVP scope.
	metricArg := strings.TrimSpace(m[2])
	if metricArg == "*" {
		// Reject count(*) (and any *-arg) explicitly. The lakehouse-sql-server
		// resolver refuses count(*) by design (see resolve.go "no silent
		// COUNT(*) fallback") to avoid JOIN double-counting. Catching it here
		// gives the LLM an immediate, specific error instead of a generic
		// METRIC_NOT_FOUND from the engine with a long property list.
		hint := "请显式给 property，例 count(id)"
		if keys := sortedKeys(primary); len(keys) > 0 {
			hint += "、count(" + keys[0] + ")"
		}
		return composeError(
			fmt.Sprintf("metric arg %q not accepted — count(*) 会在 JOIN 下双重计数", metricArg),
			hint)
	} else if strings.Contains(metricArg, ".") {
		return composeError(
			fmt.Sprintf("metric arg %q crosses OD (qualified form)", metricArg),
			"metric arg 必须是主 OD 上的 property，跨 OD 聚合请联系运营加 derived metric")
	} else if !primary[metricArg] {
		return composeError(
			fmt.Sprintf("metric arg %q is not a property of primary OD %q", metricArg, odName),
			"available: "+strings.Join(sortedKeys(primary), ", "))
	}

	// Build QuerySpec. IntentHint stays nil — that's how the engine knows
	// this is composed (no auto group_by / canonical_filters merging).
	// spec.Objects starts with primary; cross-OD references will be appended
	// after validation so the engine's ResolveJoinPath sees the full set.
	spec := smartquery.QuerySpec{
		ProjectID:   projectID,
		Objects:     []string{odName},
		Metric:      metric,
		DisplayMode: "table",
	}

	// Validate + assemble groupBy. Each entry can be bare ("EmployeeID") or
	// qualified ("CUSTOMER.Country"); the latter pulls CUSTOMER into
	// spec.Objects automatically.
	if rawGB, ok := args["groupBy"].([]interface{}); ok {
		for i, raw := range rawGB {
			s, ok := raw.(string)
			if !ok {
				return composeError(fmt.Sprintf("groupBy[%d] must be a string", i), "")
			}
			ref, _, errM := resolveQualifiedProperty(s, fmt.Sprintf("groupBy[%d]", i))
			if errM != nil {
				return errM
			}
			spec.GroupBy = append(spec.GroupBy, ref)
		}
	}

	// Validate + assemble filters. Same OD.Prop semantics as groupBy.
	if rawFilters, ok := args["filters"].([]interface{}); ok {
		for i, raw := range rawFilters {
			fm, ok := raw.(map[string]interface{})
			if !ok {
				return composeError(fmt.Sprintf("filters[%d] must be an object", i), "")
			}
			ref, _, errM := resolveQualifiedProperty(StrVal(fm, "property"), fmt.Sprintf("filters[%d]", i))
			if errM != nil {
				return errM
			}
			op := strings.ToLower(strings.TrimSpace(StrVal(fm, "op")))
			value := StrVal(fm, "value")
			if op == "" {
				op = "="
			}
			if !allowedOps[op] {
				return composeError(
					fmt.Sprintf("filters[%d].op=%q not allowed", i, op),
					"whitelist: =, !=, >, <, >=, <=, in, not_in, like, between")
			}
			spec.Filters = append(spec.Filters, smartquery.FilterItem{
				Prop:  ref,
				Op:    op,
				Value: value,
			})
		}
	}

	// Append referenced dim ODs to spec.Objects (deterministic order so SQL
	// generation is stable across runs). Engine's ResolveJoinPath will
	// traverse ont_causality(join_key) to find the JOIN path.
	for _, dim := range sortedKeys(referencedDimODs) {
		spec.Objects = append(spec.Objects, dim)
	}

	// Validate + assemble orderBy.
	if rawOrder, ok := args["orderBy"].([]interface{}); ok {
		for i, raw := range rawOrder {
			om, ok := raw.(map[string]interface{})
			if !ok {
				return composeError(fmt.Sprintf("orderBy[%d] must be an object", i), "")
			}
			label := strings.TrimSpace(StrVal(om, "label"))
			if label == "" {
				// label is required so we don't dictate which column is the metric
				return composeError(fmt.Sprintf("orderBy[%d].label required", i),
					"label = result column name (例 \"Total_NetAmount\" 或 EmployeeID)")
			}
			dir := strings.ToUpper(strings.TrimSpace(StrVal(om, "dir")))
			if dir != "ASC" && dir != "DESC" {
				dir = "DESC"
			}
			spec.OrderBy = append(spec.OrderBy, smartquery.OrderByItem{
				Prop: label,
				Dir:  dir,
			})
		}
	}

	// Validate limit.
	if v, ok := args["limit"]; ok {
		switch n := v.(type) {
		case float64:
			if n >= 1 {
				spec.Limit = int(n)
			}
		case int:
			if n >= 1 {
				spec.Limit = n
			}
		}
	}

	// Dispatch to the same engine strict-mode uses — IntentHint is nil so
	// the engine treats this as a fully-specified spec without any auto
	// group_by / canonical_filters injection.
	result := smartqueryExec(db).Execute(ctx, spec)

	if result.ErrorMessage != "" && result.SQL == "" {
		return M{
			"error":         "COMPOSE_FAILED: " + result.ErrorMessage,
			"code":          "COMPOSE_FAILED",
			"engine_error":  result.ErrorMessage,
			"debug":         result.DebugInfo,
		}
	}

	// Trim execution_result preview if huge — same heuristic as smartquery
	// but without pivot post-processing (composed queries don't have intent
	// pivot config).
	totalRows := 0
	if result.ResultJSON != "" {
		var rows []map[string]interface{}
		if err := json.Unmarshal([]byte(result.ResultJSON), &rows); err == nil {
			totalRows = len(rows)
		}
	}

	execStatus := "error"
	if result.ExecutionOK {
		execStatus = "success"
	}

	return M{
		"composed":          true,
		"odName":            odName,
		"metric":            metric,
		"filters":           spec.Filters,
		"groupBy":           spec.GroupBy,
		"orderBy":           spec.OrderBy,
		"limit":             spec.Limit,
		"generated_sql":     result.SQL,
		"execution_status":  execStatus,
		"execution_error":   result.ErrorMessage,
		"execution_result":  result.ResultJSON,
		"total_rows":        totalRows,
		// matched_intent / bound_spec stay nil — composed queries are not
		// intent-bound. Reflect's downstream code tolerates these absent.
	}
}

// loadODPropertyNames returns a set of property names that exist on the
// supplied OD (by ID). Used for fast O(1) catalog lookup during validation.
func loadODPropertyNames(ctx context.Context, db *sql.DB, odID string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name FROM ont_property WHERE object_type_id = $1`, odID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// stripGranularitySuffix removes a trailing "(月)" / "(年)" / "(week)" etc.
// from a date-property reference. The bare prop must still be a real
// property; the engine handles the granularity tag separately.
func stripGranularitySuffix(s string) string {
	if i := strings.Index(s, "("); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// sortedKeys returns the keys of a string-set sorted alphabetically. Used
// for stable error messages.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// minimal-dependency sort
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
