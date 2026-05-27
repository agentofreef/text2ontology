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

// NOTE: the LLM-facing description for composition lives in
// smartqueryToolDescription (Mode B) — compose is now the no-intent branch of
// the unified `smartquery` tool, not a separate tool. runComposeQueryTool below
// remains the implementation that path delegates to.

// metricAliasRE matches a trailing `as <alias>` clause on a canonical_metric
// expression (口径 editor preserves the author's measure alias on the stored
// SQL, e.g. `sum("ORDER_QUANTITY") as "total"`). Stripped before metricExprRE
// runs so the anchored `^func(arg)$` regex doesn't get derailed by the alias.
var metricAliasRE = regexp.MustCompile(`(?i)\s+as\s+("?[a-zA-Z_][a-zA-Z0-9_]*"?)\s*$`)

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

// pureMetricEnabled gates the Path A "纯口径" measure check (col-family): a
// Mode B compose may only aggregate a column already used by an authorized
// 口径 on the OD. Default on; set USE_PURE_METRIC=0 to disable (A/B / debug).
var pureMetricEnabled = envFlagDefaultOn("USE_PURE_METRIC")

// propertyOnOtherODs returns the names of every OTHER OD (other than
// `excludeOD`, project-scoped) that carries a property whose name matches
// `propName` byte-for-byte. Empty result on lookup error or no match.
//
// Purpose: when validation fails because a referenced property name doesn't
// live on the primary OD the LLM chose, this lookup tells us where the name
// DOES live, so the error hint can name a concrete retry path instead of
// just listing the primary OD's columns. Universal — does not hardcode any
// OD name, works for any property/OD combination in any project.
func propertyOnOtherODs(ctx context.Context, db *sql.DB, projectID, propName, excludeOD string) []string {
	if db == nil || strings.TrimSpace(propName) == "" {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT o.name
		FROM ont_property p
		JOIN ont_object_type o ON o.id = p.object_type_id
		WHERE p.project_id = $1 AND p.name = $2 AND o.name <> $3
		  AND COALESCE(o.mark, true) = true
		ORDER BY o.name`,
		projectID, propName, excludeOD,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out = append(out, n)
		}
	}
	return out
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

// metricColumnAuthorized reports whether the aggregated column `col` is covered
// by at least one authorized (mark=true) 口径 on the OD — i.e. some
// lakehouse_metric's canonical_metric aggregates the same column. The aggregation
// FUNCTION may differ (col-family gate). Returns (false, authorizedCols) when no
// 口径 backs the column; authorizedCols lists the columns that ARE authorized,
// for a corrective hint. Fail-open (true) on nil DB / lookup error so infra
// failure never blocks a query.
func metricColumnAuthorized(ctx context.Context, db *sql.DB, projectID, odID, col string) (bool, []string) {
	if db == nil || strings.TrimSpace(col) == "" {
		return true, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT canonical_metric FROM lakehouse_metric
		WHERE project_id=$1 AND object_id=$2 AND mark=true
		  AND deleted_at IS NULL
		  AND COALESCE(canonical_metric,'') <> ''`,
		projectID, odID)
	if err != nil {
		return true, nil // fail-open
	}
	defer rows.Close()
	target := normMetricColName(col)
	seen := map[string]bool{}
	var authed []string
	for rows.Next() {
		var cm string
		if rows.Scan(&cm) != nil {
			continue
		}
		c := metricColFromExpr(cm)
		if c == "" {
			continue
		}
		nc := normMetricColName(c)
		if !seen[nc] {
			seen[nc] = true
			authed = append(authed, c)
		}
		if nc == target {
			return true, nil
		}
	}
	if rows.Err() != nil {
		return true, nil // fail-open on iteration error
	}
	return false, authed
}

// metricColFromExpr extracts the aggregated column from a canonical_metric
// expression like `sum(ORDER_QUANTITY)`, `sum("ORDER_QUANTITY") as "total"`, or
// `count(ORDER.ORDER_ID)` — returning the bare column (alias / OD prefix /
// granularity suffix / surrounding double-quotes stripped), or "" if it can't
// be parsed. Without the alias + quote handling, every 口径 authored with a
// custom measure alias falsely fails the NO_AUTHORIZED_METRIC gate.
func metricColFromExpr(expr string) string {
	if loc := metricAliasRE.FindStringIndex(expr); loc != nil {
		expr = expr[:loc[0]]
	}
	m := metricExprRE.FindStringSubmatch(expr)
	if m == nil {
		return ""
	}
	arg := stripGranularitySuffix(strings.TrimSpace(m[2]))
	if i := strings.LastIndex(arg, "."); i >= 0 {
		arg = arg[i+1:]
	}
	arg = strings.Trim(strings.TrimSpace(arg), `"`)
	return arg
}

// normMetricColName normalizes a column name for case-insensitive comparison.
func normMetricColName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// compositionResolver holds the per-call cross-OD property-resolution state
// shared by the pure composer (runComposeQueryTool) and the metric-name path
// (Mode A). It validates "OD.Prop" / bare "Prop" references against the
// project's ontology, lazy-loading dim-OD property sets, and records which dim
// ODs were referenced so the caller can append them to spec.Objects (the engine
// needs the full set to resolve JOIN edges via ont_causality).
type compositionResolver struct {
	ctx       context.Context
	db        *sql.DB
	projectID string
	odName    string // primary OD name
	// odProps caches property-name sets per OD. Primary is loaded eagerly;
	// dim ODs are loaded lazily the first time an "OD.Prop" reference names them.
	odProps map[string]map[string]bool
	primary map[string]bool // == odProps[odName], for hot-path lookups
	// odIDByName caches OD id by name (primary + lazily-loaded dim ODs).
	odIDByName map[string]string
	// referencedDimODs records dim ODs touched by a qualified reference so the
	// caller can append them (sorted) to spec.Objects.
	referencedDimODs map[string]bool
}

// newCompositionResolver validates odName, loads its property set, and returns
// a resolver ready to parse runtime groupBy/filters/orderBy references. Returns
// a composeError-style M (non-nil ⇒ error, caller returns it directly).
func newCompositionResolver(ctx context.Context, db *sql.DB, projectID, odName string) (*compositionResolver, M) {
	var odID string
	err := db.QueryRowContext(ctx, `
		SELECT id FROM ont_object_type
		WHERE project_id=$1 AND name=$2 AND mark=true`,
		projectID, odName,
	).Scan(&odID)
	if err == sql.ErrNoRows {
		return nil, composeError(fmt.Sprintf("OD %q not found or not active", odName),
			"先 list(type=ods) 看可用 OD 名")
	}
	if err != nil {
		return nil, composeError("OD lookup DB error: "+err.Error(), "")
	}
	primary, err := loadODPropertyNames(ctx, db, odID)
	if err != nil {
		return nil, composeError("property lookup DB error: "+err.Error(), "")
	}
	if len(primary) == 0 {
		return nil, composeError(fmt.Sprintf("OD %q has zero properties", odName),
			"先 inspect(target=<OD>, mode=schema) 检查")
	}
	return &compositionResolver{
		ctx:              ctx,
		db:               db,
		projectID:        projectID,
		odName:           odName,
		odProps:          map[string]map[string]bool{odName: primary},
		primary:          primary,
		odIDByName:       map[string]string{odName: odID},
		referencedDimODs: map[string]bool{},
	}, nil
}

// odID returns the cached primary OD id.
func (r *compositionResolver) odID() string { return r.odIDByName[r.odName] }

// resolveQualifiedProperty parses "OD.Prop" or bare "Prop" form. Returns
// (resolvedRef, dimODName, errorMap). dimODName is "" when the reference
// resolves on the primary OD. The resolvedRef preserves the original token so
// the engine's stripObjectPrefix matches the right OD via spec.Objects
// membership.
func (r *compositionResolver) resolveQualifiedProperty(rawRef, ctxLabel string) (string, string, M) {
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
		if !r.primary[bare] {
			return "", "", composeError(
				fmt.Sprintf("%s: %q is not a property of primary OD %q", ctxLabel, bare, r.odName),
				"available on primary: "+strings.Join(sortedKeys(r.primary), ", ")+
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
	if dimOD == r.odName {
		if !r.primary[dimProp] {
			return "", "", composeError(
				fmt.Sprintf("%s: %q is not a property of OD %q", ctxLabel, dimProp, r.odName),
				"available: "+strings.Join(sortedKeys(r.primary), ", "))
		}
		return ref, "", nil
	}
	// Cross-OD ref. Lazy-load dim OD properties + validate.
	if r.odProps[dimOD] == nil {
		var dimID string
		if e := r.db.QueryRowContext(r.ctx, `
			SELECT id FROM ont_object_type
			WHERE project_id=$1 AND name=$2 AND mark=true`,
			r.projectID, dimOD,
		).Scan(&dimID); e != nil {
			if e == sql.ErrNoRows {
				return "", "", composeError(
					fmt.Sprintf("%s: dim OD %q not found or not active", ctxLabel, dimOD),
					"看 catalog 列出来的 OD 名")
			}
			return "", "", composeError("dim OD lookup DB error: "+e.Error(), "")
		}
		props, perr := loadODPropertyNames(r.ctx, r.db, dimID)
		if perr != nil {
			return "", "", composeError("dim property lookup DB error: "+perr.Error(), "")
		}
		r.odProps[dimOD] = props
		r.odIDByName[dimOD] = dimID
	}
	if !r.odProps[dimOD][dimProp] {
		return "", "", composeError(
			fmt.Sprintf("%s: %q is not a property of OD %q", ctxLabel, dimProp, dimOD),
			"available on "+dimOD+": "+strings.Join(sortedKeys(r.odProps[dimOD]), ", "))
	}
	r.referencedDimODs[dimOD] = true
	return ref, dimOD, nil
}

// referencedDimODsSorted returns the dim ODs touched by resolveQualifiedProperty
// in deterministic (alphabetical) order, for stable spec.Objects assembly.
func (r *compositionResolver) referencedDimODsSorted() []string {
	return sortedKeys(r.referencedDimODs)
}

// applyRuntimeComposition resolves the function-call's groupBy / filters /
// orderBy (plus reachability-derived requiredDims) onto spec, doing cross-OD
// resolution, op whitelisting, and the low-cardinality value-domain guard. It
// is shared by the pure composer (empty metric → runComposeQueryTool) and the
// metric-name path (Mode A) so both honor identical runtime-composition
// semantics. Referenced dim ODs are appended to spec.Objects in deterministic
// order. Returns a composeError-style M (non-nil ⇒ error, caller returns it
// directly).
//
// Note: requiredDims are merged into spec.GroupBy here (best-effort, skip on
// resolve failure) — this is the completeness backstop for the composer path.
// Mode A passes requiredDims=nil because it merges them into the IntentHint's
// AutoGroupBy upstream (so they ride through the engine's intent enforcement
// with the right MOVE/INJECT semantics).
func applyRuntimeComposition(r *compositionResolver, args map[string]interface{}, requiredDims []string, spec *smartquery.QuerySpec) M {
	// Validate + assemble groupBy. Each entry can be bare ("EmployeeID") or
	// qualified ("CUSTOMER.Country"); the latter pulls CUSTOMER into
	// spec.Objects automatically.
	if rawGB, ok := args["groupBy"].([]interface{}); ok {
		for i, raw := range rawGB {
			s, ok := raw.(string)
			if !ok {
				return composeError(fmt.Sprintf("groupBy[%d] must be a string", i), "")
			}
			ref, _, errM := r.resolveQualifiedProperty(s, fmt.Sprintf("groupBy[%d]", i))
			if errM != nil {
				return errM
			}
			spec.GroupBy = append(spec.GroupBy, ref)
		}
	}

	// Completeness contract: force the executed query's groupBy to include the
	// question's required dimensions (resolved from recall by the reachability
	// judge). For each reqDim not already in groupBy, resolve it via the same
	// property pipeline; on resolve failure SKIP it (do not fail the whole query
	// — we just can't inject that one dim). resolveQualifiedProperty already
	// records the dim OD into referencedDimODs on success.
	for _, reqDim := range requiredDims {
		// Dedupe on the bare property name (case-insensitive) so a bare prop and
		// its OD-qualified form ("OD.Prop" vs "Prop") count as the SAME column.
		// This stops a duplicate groupBy (e.g. bare + qualified of one column)
		// that the dense-SQL resolver treats as two columns → 0 rows.
		nd := bareDimKey(reqDim)
		already := false
		for _, g := range spec.GroupBy {
			if bareDimKey(g) == nd {
				already = true
				break
			}
		}
		if already {
			continue
		}
		ref, _, errM := r.resolveQualifiedProperty(reqDim, "requiredDim")
		if errM != nil {
			continue // can't inject this dim; leave the query as-is
		}
		spec.GroupBy = append(spec.GroupBy, ref)
	}

	// Validate + assemble filters. Same OD.Prop semantics as groupBy.
	if rawFilters, ok := args["filters"].([]interface{}); ok {
		for i, raw := range rawFilters {
			fm, ok := raw.(map[string]interface{})
			if !ok {
				return composeError(fmt.Sprintf("filters[%d] must be an object", i), "")
			}
			ref, dimOD, errM := r.resolveQualifiedProperty(StrVal(fm, "property"), fmt.Sprintf("filters[%d]", i))
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
			// Stage A — value-domain guard (defense-in-depth). For equality-type
			// filters on a LOW-cardinality categorical property, reject a value
			// that isn't in the property's known domain (from lakehouse_keyword),
			// so the LLM can't silently run a fabricated value (e.g. "TBD" on a
			// {Ready, Not ready} field). Range/like ops and high-cardinality
			// props are skipped (domain unknown/too large → fail-open).
			if value != "" && (op == "=" || op == "!=" || op == "in" || op == "not_in") {
				owningOD := r.odName
				if dimOD != "" {
					owningOD = dimOD
				}
				if odID := r.odIDByName[owningOD]; odID != "" {
					if domain, known := lowCardValueDomain(r.ctx, r.db, odID, bareFilterPropName(StrVal(fm, "property"))); known {
						for _, v := range splitFilterValues(value, op) {
							if v != "" && !valueInDomain(v, domain) {
								return composeError(
									fmt.Sprintf("filters[%d]: 值 %q 不在属性 %q 的已知值域内", i, v, bareFilterPropName(StrVal(fm, "property"))),
									"该属性值域: "+strings.Join(domain, " | ")+
										" —— 若用户的说法不在其中，请勿臆造映射；向用户澄清，或换一个属性/值。")
							}
						}
					}
				}
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
	// traverse ont_causality(join_key) to find the JOIN path. Dedupe against
	// objects already present (Mode A seeds spec.Objects from the intent).
	existingObjs := map[string]bool{}
	for _, o := range spec.Objects {
		existingObjs[o] = true
	}
	for _, dim := range r.referencedDimODsSorted() {
		if !existingObjs[dim] {
			spec.Objects = append(spec.Objects, dim)
			existingObjs[dim] = true
		}
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

	return nil
}

// runComposeQueryTool is the dispatchTool handler for compose_query.
// Validates input, builds smartquery.QuerySpec without an IntentHint, and
// dispatches to the same SQL engine strict-mode uses.
func runComposeQueryTool(ctx context.Context, db *sql.DB, projectID, userQuestion string, args map[string]interface{}, requiredDims []string) M {
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

	// Validate odName exists + is active, and load its property set. The
	// resolver also owns the cross-OD "OD.Prop" reference resolution shared
	// with Mode A (applyRuntimeComposition below).
	r, errM := newCompositionResolver(ctx, db, projectID, odName)
	if errM != nil {
		return errM
	}
	primary := r.primary
	odID := r.odID()

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
		// Did-You-Mean: the most common cause of this error is the LLM
		// transcribing a property name from a sibling OD (e.g. `MTM_NUMBER`
		// lives on EARLY_ORDER, not MTM). Without a pointer the next retry
		// just bounces against the same primary OD. Universal lookup — no
		// hardcoded OD list — finds wherever the name actually lives and
		// nudges the LLM toward the right primary_od.
		hint := "available on " + odName + ": " + strings.Join(sortedKeys(primary), ", ")
		if otherODs := propertyOnOtherODs(ctx, db, projectID, metricArg, odName); len(otherODs) > 0 {
			hint += fmt.Sprintf(
				"; the property %q exists on OD %s — if that is what you meant, retry the call with odName=%q",
				metricArg, strings.Join(otherODs, " / "), otherODs[0])
		}
		return composeError(
			fmt.Sprintf("metric arg %q is not a property of primary OD %q", metricArg, odName),
			hint)
	}

	// Path A — pure-metric gate (col-family, USE_PURE_METRIC). The aggregated
	// column must be a measure that at least one authorized 口径 (metric_intent)
	// on this OD already uses in its canonical_metric. This forbids an ad-hoc
	// bare aggregate that no 口径 backs ("无口径裸测度"), so the agent never
	// invents an unauthorized computation — every number traces to a curated
	// 口径 (consistent with cite-only). The aggregation FUNCTION may differ
	// (sum vs avg); only the column must be authorized. Fail-open on nil DB /
	// lookup error so infra never blocks a query.
	if pureMetricEnabled {
		if ok, authed := metricColumnAuthorized(ctx, db, projectID, odID, metricArg); !ok {
			hint := "纯口径模式：度量列必须来自某个已授权口径。请改用 🎯 小节里已有的口径，" +
				"或调用 declare_capability_gap 声明缺口——不要拼一个没有口径背书的聚合。"
			if len(authed) > 0 {
				hint += " 本 OD 已授权口径覆盖的度量列：" + strings.Join(authed, " / ")
			}
			return M{
				"error": fmt.Sprintf("NO_AUTHORIZED_METRIC: 度量列「%s」没有任何已授权口径背书", metricArg),
				"code":  "NO_AUTHORIZED_METRIC",
				"hint":  hint,
			}
		}
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

	// Resolve runtime groupBy / requiredDims / filters / orderBy onto the spec
	// (cross-OD resolution, op whitelist, value-domain guard, dim-OD append).
	// Shared with Mode A — keeps the two paths in lockstep.
	if errM := applyRuntimeComposition(r, args, requiredDims, &spec); errM != nil {
		return errM
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

	resp := M{
		"composed":          true,
		"odName":            odName,
		"metric":            metric,
		"filters":           spec.Filters,
		"groupBy":           spec.GroupBy,
		"orderBy":           spec.OrderBy,
		"limit":             spec.Limit,
		"ontology_sql":      result.OntologySQL,
		"generated_sql":     result.SQL,
		"execution_status":  execStatus,
		"execution_error":   result.ErrorMessage,
		"execution_result":  result.ResultJSON,
		"total_rows":        totalRows,
		// matched_intent / bound_spec stay nil — composed queries are not
		// intent-bound. Reflect's downstream code tolerates these absent.
	}
	// Compose queries report their resolved groupBy as dim_columns inside
	// row_summary so the completeness check has a consistent surface to read.
	if len(spec.GroupBy) > 0 {
		resp["row_summary"] = M{"dim_columns": append([]string{}, spec.GroupBy...)}
	}
	annotateRequiredDims(resp, requiredDims)
	return resp
}

// annotateRequiredDims is the deterministic completeness backstop (P1d). When
// requiredDims is non-empty it always records them on the result, and when any
// is absent from the result's dim columns it adds the missing list plus a
// human-readable warning the answer layer must surface. It does NOT auto-rerun.
func annotateRequiredDims(resp M, requiredDims []string) {
	if len(requiredDims) == 0 {
		return
	}
	resp["required_dims"] = requiredDims
	if missing := missingRequiredDims(resp, requiredDims); len(missing) > 0 {
		resp["missing_required_dims"] = missing
		resp["dim_warning"] = "结果缺少必需维度: " + strings.Join(missing, ", ") +
			" —— 该结果不完整，回答时必须说明，或补上这些 groupBy 重查。"
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

// ── Stage A: value-domain guard helpers ──────────────────────────────────────

// valueDomainCap bounds how many distinct value-keywords a property may have to
// be treated as a LOW-cardinality categorical domain. Above this we cannot
// reliably enumerate the domain, so the guard skips validation (fail-open).
const valueDomainCap = 40

// bareFilterPropName extracts the property name from a filter reference,
// dropping a granularity suffix (e.g. "(月)") and an "OD." qualifier.
func bareFilterPropName(rawRef string) string {
	s := stripGranularitySuffix(strings.TrimSpace(rawRef))
	if idx := strings.LastIndex(s, "."); idx >= 0 {
		s = s[idx+1:]
	}
	return strings.TrimSpace(s)
}

// bareDimKey is the case-insensitive bare-property identity used to dedupe
// GROUP-BY dimensions: an OD-qualified ref ("OD.Prop") and the bare prop
// ("Prop") collapse to the same key, so a required dim already present in
// EITHER form is not injected twice (a bare+qualified pair of one column is a
// duplicate groupBy the engine treats as two columns → 0 rows). General rule —
// no specific property/OD names.
func bareDimKey(rawRef string) string {
	return strings.ToLower(bareFilterPropName(rawRef))
}

// splitFilterValues splits an in/not_in comma list into individual values;
// other ops yield the single trimmed value.
func splitFilterValues(value, op string) []string {
	if op == "in" || op == "not_in" {
		parts := strings.Split(value, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	return []string{strings.TrimSpace(value)}
}

// normForDomain lowercases and strips spaces/underscores so domain membership
// matches recall's surface-form normalization ("Not ready" ≈ "notready").
func normForDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	return s
}

// missingRequiredDims returns required dims not present among the result's dim
// columns. It reads resultM["row_summary"].dim_columns (the resolved groupBy
// column labels) and, for each reqDim, reports it missing when none of the dim
// columns matches under normForDomain containment (either direction). A reqDim
// may be an "OD.Prop" ref, so the bare prop after the last "." is also tried.
// Deterministic backstop — never auto-reruns.
func missingRequiredDims(resultM M, requiredDims []string) []string {
	if len(requiredDims) == 0 {
		return nil
	}
	// Extract dim_columns from row_summary (an M with a []string or []any).
	var dimCols []string
	if rs, ok := resultM["row_summary"].(M); ok {
		switch dc := rs["dim_columns"].(type) {
		case []string:
			dimCols = dc
		case []interface{}:
			for _, v := range dc {
				if s, ok := v.(string); ok {
					dimCols = append(dimCols, s)
				}
			}
		}
	}
	normCols := make([]string, 0, len(dimCols))
	for _, c := range dimCols {
		if n := normForDomain(c); n != "" {
			normCols = append(normCols, n)
		}
	}

	covered := func(reqDim string) bool {
		candidates := []string{reqDim}
		if i := strings.LastIndex(reqDim, "."); i >= 0 {
			candidates = append(candidates, reqDim[i+1:])
		}
		for _, cand := range candidates {
			n := normForDomain(cand)
			if n == "" {
				continue
			}
			for _, c := range normCols {
				if c == n || strings.Contains(c, n) || strings.Contains(n, c) {
					return true
				}
			}
		}
		return false
	}

	var missing []string
	for _, reqDim := range requiredDims {
		if !covered(reqDim) {
			missing = append(missing, reqDim)
		}
	}
	return missing
}

// valueInDomain reports whether v matches any domain member (normalised).
func valueInDomain(v string, domain []string) bool {
	nv := normForDomain(v)
	for _, d := range domain {
		if normForDomain(d) == nv {
			return true
		}
	}
	return false
}

// lowCardValueDomain returns the distinct value-keywords for a property when it
// is a low-cardinality categorical (≤ valueDomainCap distinct values). Returns
// (domain, true) for a known low-card domain; (nil, false) when the property is
// high-cardinality, has no value vocabulary, or on any error — fail-open, so the
// guard never blocks a filter it cannot confidently validate.
func lowCardValueDomain(ctx context.Context, db *sql.DB, odID, propName string) ([]string, bool) {
	if db == nil || odID == "" || propName == "" {
		return nil, false
	}
	var propID string
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM ont_property WHERE object_type_id=$1 AND name=$2`,
		odID, propName).Scan(&propID); err != nil {
		return nil, false
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT keyword FROM lakehouse_keyword
		WHERE property_id=$1
		  AND COALESCE(is_column_name, false) = false
		  AND COALESCE(is_stopword, false) = false
		  AND COALESCE(is_machine_code, false) = false
		LIMIT $2`, propID, valueDomainCap+1)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil && strings.TrimSpace(k) != "" {
			out = append(out, k)
		}
	}
	if rows.Err() != nil || len(out) == 0 || len(out) > valueDomainCap {
		return nil, false // error, no vocab, or high-cardinality → can't validate
	}
	return out, true
}
