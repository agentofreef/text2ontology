package smartquery

import (
	"fmt"
	"regexp"
	"strings"
)

// Pass is a single QuerySpec → QuerySpec transformation. Passes mutate
// in place and return an error to abort the pipeline. The contract:
//
//   - Idempotent: running the same pass twice on the same spec must be
//     equivalent to running it once.
//   - Commutative-when-possible: passes that don't logically depend on
//     each other should produce the same final spec regardless of order.
//   - Pure: no DB, no I/O, no clocks. The caller assembles all data
//     dependencies (e.g. spec.IntentHint) before invoking the pipeline.
type Pass func(*QuerySpec) error

// Pipeline is an ordered list of Passes. Run executes them sequentially,
// stopping at the first error.
type Pipeline []Pass

// Run executes each pass in order on spec. Returns the first non-nil
// error encountered.
func (p Pipeline) Run(spec *QuerySpec) error {
	for i, pass := range p {
		if err := pass(spec); err != nil {
			return fmt.Errorf("pipeline pass #%d: %w", i, err)
		}
	}
	return nil
}

// PassApplyIntentHint is the canonical pass that applies spec.IntentHint
// (metric override + auto_group_by injection) to spec. No-op when hint
// is nil.
func PassApplyIntentHint(spec *QuerySpec) error {
	applyIntentHint(spec, spec.IntentHint)
	return nil
}

// PassGateMetricOrGroupBy fails the pipeline when spec has neither metric
// nor groupBy — i.e. an unaggregated, ungrouped detail query that would
// otherwise pull every row in the source table.
//
// Runs AFTER PassApplyIntentHint so intent-injected metric/groupBy count.
func PassGateMetricOrGroupBy(spec *QuerySpec) error {
	if spec.Metric != "" || len(spec.GroupBy) > 0 {
		return nil
	}
	return &ResolveError{
		Code:    "MISSING_METRIC_OR_GROUPBY",
		Message: "未聚合的明细查询缺少分组维度，会返回过多数据。请指定 metric（如 sum(...)）或 groupBy 后重试。",
	}
}

// validOpSet enumerates the operators supported downstream by SQL generation.
// Anything outside this set produces SPEC_VALIDATION_FAILED before resolve.
//
// Mirrors engine.normalizeOperator output range — that function maps aliases
// (e.g. "eq" → "=", "neq" → "<>") to canonical forms; PassValidateSpec runs
// AFTER NormalizeQuerySpec so we only see canonicals here.
var validOpSet = map[string]bool{
	"=":             true,
	"<>":            true,
	">":             true,
	">=":            true,
	"<":             true,
	"<=":            true,
	"contains":      true,
	"not contains":  true,
	"starts with":   true,
	"ends with":     true,
	"like":          true,
	"in":            true,
	"not in":        true,
	"is blank":      true,
	"is not blank":  true,
	"between":       true,
	"regex":         true,
}

// metricFilterOpSet is the comparison-operator whitelist for MetricFilter.
// MetricFilter.Op and MetricFilter.Value are interpolated UNESCAPED into the
// generated HAVING clause (see sql_builder.go havingExpr /
// sql_builder_v2.go havingExprV2), so this is the sole injection guard on the
// LLM-supplied post-aggregation predicate. Only canonical comparison ops are
// allowed — no aliases, no whitespace, no SQL keywords.
var metricFilterOpSet = map[string]bool{
	">":  true,
	">=": true,
	"<":  true,
	"<=": true,
	"=":  true,
	"<>": true,
}

// metricFilterValueRe constrains MetricFilter.Value to a strict numeric
// literal (optional leading '-', integer, optional decimal part). Anchored so
// no trailing SQL (e.g. "1; DELETE") can sneak past.
var metricFilterValueRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// PassValidateSpec is the runtime safety net guaranteeing "no malformed SQL"
// — the third defense line of strict-mode dispatch.
//
// Runs AFTER PassApplyIntentHint (so intent-injected defaults are visible)
// AFTER PassGateMetricOrGroupBy (so trivial bare-spec is already filtered),
// and BEFORE the engine's resolve / SQL-generation steps.
//
// What it catches:
//   - spec.Limit < 0 (negative LIMIT is invalid SQL)
//   - spec.GroupBy entries that are empty after trim (would produce SELECT
//     with empty alias)
//   - spec.OrderBy entries with empty Prop (would produce ORDER BY ,)
//   - spec.OrderBy.Dir outside {"ASC","DESC"} (case-insensitive accepted;
//     case-validation is engine's job — we just check it's set)
//   - spec.Filters entries with empty Prop or Op outside validOpSet
//
// What it explicitly does NOT catch (those are resolve.go's job):
//   - property name not bound to a real column (PROPERTY_NOT_FOUND)
//   - join path unreachable (RELATIONSHIP_UNREACHABLE)
//   - metric expression unparseable (MALFORMED_METRIC)
//
// Returns ResolveError(SPEC_VALIDATION_FAILED) with Detail.errors[] listing
// every offence so the LLM / save-time validator gets a single batched
// rejection rather than one-error-at-a-time fix loops.
func PassValidateSpec(spec *QuerySpec) error {
	var problems []string

	// LIMIT — Limit==0 is legitimate (means "no LIMIT clause"; pivot path
	// strips Limit to 0 expecting reapply-after-pivot). Negative is bug.
	if spec.Limit < 0 {
		problems = append(problems, fmt.Sprintf("limit=%d 必须 >= 0", spec.Limit))
	}

	// GROUP BY entries — must be non-empty after trim.
	for i, g := range spec.GroupBy {
		if strings.TrimSpace(g) == "" {
			problems = append(problems, fmt.Sprintf("groupBy[%d] 为空字符串", i))
		}
	}

	// ORDER BY entries — Prop and Dir both required.
	for i, o := range spec.OrderBy {
		if strings.TrimSpace(o.Prop) == "" {
			problems = append(problems, fmt.Sprintf("orderBy[%d] 缺少 prop（LLM 经常把 prop+dir 拆成两个 item，应合并为一个 {prop,dir}）", i))
			continue
		}
		dir := strings.ToUpper(strings.TrimSpace(o.Dir))
		if dir != "ASC" && dir != "DESC" {
			problems = append(problems, fmt.Sprintf("orderBy[%d] dir=%q 非法（仅接受 ASC / DESC）", i, o.Dir))
		}
	}

	// FILTERS — Prop required, Op in canonical whitelist.
	for i, f := range spec.Filters {
		if strings.TrimSpace(f.Prop) == "" {
			problems = append(problems, fmt.Sprintf("filters[%d] 缺少 prop", i))
			continue
		}
		op := strings.TrimSpace(f.Op)
		if op == "" {
			problems = append(problems, fmt.Sprintf("filters[%d] (prop=%q) 缺少 op", i, f.Prop))
			continue
		}
		if !validOpSet[op] {
			problems = append(problems, fmt.Sprintf("filters[%d] (prop=%q) op=%q 非法（合法 op 见 NormalizeQuerySpec.normalizeOperator）", i, f.Prop, f.Op))
		}
	}

	// METRIC FILTER — Op and Value are interpolated unescaped into the HAVING
	// clause downstream, so both must be tightly constrained: Op in the
	// comparison whitelist, Value a strict numeric literal. Anything else is a
	// SQL-injection vector (e.g. Op:"> 0; DROP TABLE", Value:"1; DELETE").
	if spec.MetricFilter != nil {
		mfOp := strings.TrimSpace(spec.MetricFilter.Op)
		if !metricFilterOpSet[mfOp] {
			problems = append(problems, fmt.Sprintf("metricFilter op=%q 非法（仅接受 > >= < <= = <>）", spec.MetricFilter.Op))
		}
		if !metricFilterValueRe.MatchString(strings.TrimSpace(spec.MetricFilter.Value)) {
			problems = append(problems, fmt.Sprintf("metricFilter value=%q 非法（必须为数字字面量，如 42 或 -3.14）", spec.MetricFilter.Value))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return &ResolveError{
		Code:    "SPEC_VALIDATION_FAILED",
		Message: fmt.Sprintf("spec 校验失败：%s", strings.Join(problems, "；")),
		Detail:  map[string]any{"errors": problems},
	}
}

// DefaultPipeline is the spec-level pipeline that runs before
// ResolveQuery in the lakehouse engine. Order matters:
//
//  1. PassApplyIntentHint — Intent-driven mutations (metric / groupBy /
//     default order / default limit / canonical filters)
//  2. PassGateMetricOrGroupBy — guard against bare detail queries
//  3. PassValidateSpec — runtime safety net; rejects malformed spec before
//     resolve / SQL-gen would silently produce wrong SQL
//
// Future passes (filter-prop promotion, ensure-objects-cover-refs,
// auto-COUNT(*)) can be appended here as they migrate from agent-server.
var DefaultPipeline = Pipeline{
	PassApplyIntentHint,
	PassGateMetricOrGroupBy,
	PassValidateSpec,
}
