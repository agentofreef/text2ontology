package smartquery

import (
	"fmt"
	"strings"
)

// IntentValidationInput is the dry-run input sent by backend-api when
// saving / updating a lakehouse_metric_intent record. It carries every
// field that influences spec construction so the lakehouse server can
// run BindIntentParams + DefaultPipeline without needing the Intent ID
// or any DB lookup.
type IntentValidationInput struct {
	Name                string           `json:"name"`
	ObjectName          string           `json:"objectName"`
	CanonicalMetric     string           `json:"canonicalMetric,omitempty"`
	CanonicalFilters    []FilterItem     `json:"canonicalFilters,omitempty"`
	AutoGroupBy         []string         `json:"autoGroupBy,omitempty"`
	ReplaceGroupBy      bool             `json:"replaceGroupBy,omitempty"`
	DefaultOrderByLabel string           `json:"defaultOrderByLabel,omitempty"`
	DefaultOrderByDir   string           `json:"defaultOrderByDir,omitempty"`
	DefaultLimit        int              `json:"defaultLimit,omitempty"`
	Parameters          []IntentParameter `json:"parameters,omitempty"`
}

// IntentValidationResult is the dry-run output. Ok=true means the spec
// produced from this Intent + dummy parameters passes the full pipeline
// (PassApplyIntentHint → PassGateMetricOrGroupBy → PassValidateSpec).
// On Ok=false, Errors lists every detected problem.
type IntentValidationResult struct {
	Ok        bool         `json:"ok"`
	Errors    []string     `json:"errors,omitempty"`
	Code      string       `json:"code,omitempty"`
	BoundSpec *BoundSpecOut `json:"boundSpec,omitempty"`
}

// BoundSpecOut is a JSON-friendly view of the post-bind QuerySpec.
type BoundSpecOut struct {
	Objects []string      `json:"objects"`
	Metric  string        `json:"metric,omitempty"`
	GroupBy []string      `json:"groupBy,omitempty"`
	Filters []FilterItem  `json:"filters,omitempty"`
	OrderBy []OrderByItem `json:"orderBy,omitempty"`
	Limit   int           `json:"limit"`
}

// ValidateIntentDryRun runs the structural Intent validation that backend-api
// invokes on POST/PUT /api/ontology/metric-intents to prevent broken Intent
// records from being saved. Specifically:
//
//  1. Build a base IntentHint from the input fields
//  2. Build a base QuerySpec carrying that hint as spec.IntentHint
//  3. Generate dummy values for each parameter (DummyParamValue) and run
//     BindIntentParams on the spec. This exercises every parameter slot
//     and catches schema-level issues (property_filter without property,
//     unknown type, etc.).
//  4. Run DefaultPipeline (PassApplyIntentHint → PassGateMetricOrGroupBy →
//     PassValidateSpec) over the bound spec. This catches:
//       - Intent declares neither canonical_metric nor auto_group_by
//         (would produce MISSING_METRIC_OR_GROUPBY at runtime)
//       - default_order_by_dir not in {ASC, DESC}
//       - canonical_filters with bad op
//       - default_limit producing negative limit
//
// What it intentionally does NOT check (would require catalog access /
// real project context, which dry-run by design lacks):
//
//   - Property references in canonical_metric / auto_group_by /
//     canonical_filters resolve to real columns
//   - JOIN reachability between objects
//
// Those resolve at first runtime query. Dry-run is the structural gate.
func ValidateIntentDryRun(in IntentValidationInput) IntentValidationResult {
	if strings.TrimSpace(in.ObjectName) == "" {
		return IntentValidationResult{
			Ok:     false,
			Errors: []string{"objectName 必填（Intent 必须挂在一个 Od 上）"},
			Code:   "MISSING_OBJECT_NAME",
		}
	}
	hint := &IntentHint{
		Name:                in.Name,
		CanonicalMetric:     in.CanonicalMetric,
		CanonicalFilters:    append([]FilterItem(nil), in.CanonicalFilters...),
		AutoGroupBy:         append([]string(nil), in.AutoGroupBy...),
		ReplaceGroupBy:      in.ReplaceGroupBy,
		DefaultOrderByLabel: in.DefaultOrderByLabel,
		DefaultOrderByDir:   in.DefaultOrderByDir,
		DefaultLimit:        in.DefaultLimit,
	}
	spec := BuildBaseSpecFromHint(hint, in.ObjectName)

	// Build dummy params map exercising every parameter slot so the binder
	// surfaces type / property issues. Required params get dummies; optional
	// params get dummies too (extra coverage, no harm).
	dummy := make(map[string]interface{}, len(in.Parameters))
	for _, p := range in.Parameters {
		dummy[p.Name] = DummyParamValue(p)
	}
	if err := BindIntentParams(&spec, dummy, in.Parameters); err != nil {
		var re *ResolveError
		code := "PARAM_BIND_ERROR"
		msg := err.Error()
		if asResolveError(err, &re) {
			code = re.Code
			msg = re.Message
		}
		return IntentValidationResult{
			Ok:     false,
			Errors: []string{msg},
			Code:   code,
		}
	}

	if err := DefaultPipeline.Run(&spec); err != nil {
		var re *ResolveError
		code := "PIPELINE_FAILED"
		msg := err.Error()
		if asResolveError(err, &re) {
			code = re.Code
			msg = re.Message
		}
		return IntentValidationResult{
			Ok:     false,
			Errors: extractProblemList(re, msg),
			Code:   code,
		}
	}

	return IntentValidationResult{
		Ok: true,
		BoundSpec: &BoundSpecOut{
			Objects: spec.Objects,
			Metric:  spec.Metric,
			GroupBy: spec.GroupBy,
			Filters: spec.Filters,
			OrderBy: spec.OrderBy,
			Limit:   spec.Limit,
		},
	}
}

// asResolveError unwraps fmt.Errorf("pipeline pass #N: %w", ResolveError) so
// callers see the typed error not the wrapping noise.
func asResolveError(err error, target **ResolveError) bool {
	if err == nil {
		return false
	}
	type unwrapper interface{ Unwrap() error }
	cur := err
	for cur != nil {
		if re, ok := cur.(*ResolveError); ok {
			*target = re
			return true
		}
		uw, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = uw.Unwrap()
	}
	return false
}

// extractProblemList returns the batched errors list from a ResolveError's
// Detail when present; falls back to a single-element list of msg otherwise.
func extractProblemList(re *ResolveError, fallbackMsg string) []string {
	if re != nil && re.Detail != nil {
		if probs, ok := re.Detail["errors"].([]string); ok && len(probs) > 0 {
			return probs
		}
	}
	if fallbackMsg == "" {
		return nil
	}
	return []string{fallbackMsg}
}

// FormatProblems is a convenience for log lines.
func FormatProblems(probs []string) string {
	if len(probs) == 0 {
		return ""
	}
	return fmt.Sprintf("[%d problems] %s", len(probs), strings.Join(probs, "; "))
}
