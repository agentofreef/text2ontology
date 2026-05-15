package smartquery

import (
	"errors"
	"reflect"
	"testing"
)

func TestApplyIntentHint_Nil(t *testing.T) {
	spec := &QuerySpec{Metric: "x", GroupBy: []string{"a"}}
	if changes := applyIntentHint(spec, nil); changes != nil {
		t.Errorf("expected no changes when hint is nil, got %v", changes)
	}
	if spec.Metric != "x" || !reflect.DeepEqual(spec.GroupBy, []string{"a"}) {
		t.Errorf("nil hint must not mutate spec, got metric=%q groupBy=%v", spec.Metric, spec.GroupBy)
	}
}

func TestApplyIntentHint_MetricOverride(t *testing.T) {
	spec := &QuerySpec{Metric: "count(LineTotal)"}
	hint := &IntentHint{Name: "Sales.Total", CanonicalMetric: "sum(LineTotal)"}
	changes := applyIntentHint(spec, hint)
	if spec.Metric != "sum(LineTotal)" {
		t.Errorf("expected metric override, got %q", spec.Metric)
	}
	if len(changes) == 0 {
		t.Errorf("expected change record")
	}
}

func TestApplyIntentHint_MetricSameNoChange(t *testing.T) {
	spec := &QuerySpec{Metric: "  SUM(LineTotal)  "}
	hint := &IntentHint{Name: "Sales.Total", CanonicalMetric: "sum(LineTotal)"}
	applyIntentHint(spec, hint)
	if spec.Metric != "  SUM(LineTotal)  " {
		t.Errorf("structurally identical metric must not be replaced, got %q", spec.Metric)
	}
}

func TestApplyIntentHint_ReplaceGroupByWipesAndStripsEqFilters(t *testing.T) {
	spec := &QuerySpec{
		GroupBy: []string{"OldDim"},
		Filters: []FilterItem{
			{Prop: "Order_Type", Op: "=", Value: "real"},
			{Prop: "Other", Op: ">", Value: "10"},
		},
	}
	hint := &IntentHint{
		Name:           "Quantity.Distribution",
		AutoGroupBy:    []string{"Geo", "Order_Type"},
		ReplaceGroupBy: true,
	}
	applyIntentHint(spec, hint)
	if !reflect.DeepEqual(spec.GroupBy, []string{"Geo", "Order_Type"}) {
		t.Errorf("expected groupBy replaced, got %v", spec.GroupBy)
	}
	if len(spec.Filters) != 1 || spec.Filters[0].Prop != "Other" {
		t.Errorf("expected eq filter on Order_Type stripped, kept >, got %v", spec.Filters)
	}
}

func TestApplyIntentHint_MoveAndInject(t *testing.T) {
	spec := &QuerySpec{
		GroupBy: []string{"Geo"},
		Filters: []FilterItem{
			{Prop: "Order_Type", Op: "=", Value: "real"}, // → MOVE
		},
	}
	hint := &IntentHint{
		Name:        "Order.Quantity",
		AutoGroupBy: []string{"Order_Type", "Channel"}, // Order_Type=move, Channel=fresh
	}
	applyIntentHint(spec, hint)
	// move + fresh prepended ahead of pre-existing
	want := []string{"Order_Type", "Channel", "Geo"}
	if !reflect.DeepEqual(spec.GroupBy, want) {
		t.Errorf("expected groupBy %v, got %v", want, spec.GroupBy)
	}
	if len(spec.Filters) != 0 {
		t.Errorf("expected eq filter on Order_Type stripped (MOVE), got %v", spec.Filters)
	}
}

func TestApplyIntentHint_InjectSkippedUnderShareColumn(t *testing.T) {
	spec := &QuerySpec{
		GroupBy:        []string{"Geo"},
		AddShareColumn: true,
	}
	hint := &IntentHint{
		Name:        "Order.Quantity",
		AutoGroupBy: []string{"Order_Type"}, // pure inject — would widen share
	}
	applyIntentHint(spec, hint)
	if !reflect.DeepEqual(spec.GroupBy, []string{"Geo"}) {
		t.Errorf("expected fresh inject skipped under AddShareColumn, got %v", spec.GroupBy)
	}
}

func TestApplyIntentHint_AddShareColumnSafeAllowsInject(t *testing.T) {
	spec := &QuerySpec{
		GroupBy:        []string{"Geo"},
		AddShareColumn: true,
	}
	hint := &IntentHint{
		Name:               "Order.Quantity.Distribution",
		AutoGroupBy:        []string{"Order_Type"},
		AddShareColumnSafe: true,
	}
	applyIntentHint(spec, hint)
	if !reflect.DeepEqual(spec.GroupBy, []string{"Order_Type", "Geo"}) {
		t.Errorf("expected inject allowed when AddShareColumnSafe=true, got %v", spec.GroupBy)
	}
}

func TestApplyIntentHint_AlreadyPresentSkipped(t *testing.T) {
	spec := &QuerySpec{GroupBy: []string{"Order_Type", "Geo"}}
	hint := &IntentHint{
		Name:        "Order.Quantity",
		AutoGroupBy: []string{"Order_Type"},
	}
	applyIntentHint(spec, hint)
	if !reflect.DeepEqual(spec.GroupBy, []string{"Order_Type", "Geo"}) {
		t.Errorf("expected no-op when AutoGroupBy already in spec.GroupBy, got %v", spec.GroupBy)
	}
}

func TestApplyIntentHint_Idempotent(t *testing.T) {
	spec := &QuerySpec{GroupBy: []string{"Geo"}}
	hint := &IntentHint{Name: "X", AutoGroupBy: []string{"Order_Type"}}
	applyIntentHint(spec, hint)
	first := append([]string(nil), spec.GroupBy...)
	applyIntentHint(spec, hint)
	if !reflect.DeepEqual(spec.GroupBy, first) {
		t.Errorf("expected idempotency, first=%v second=%v", first, spec.GroupBy)
	}
}

func TestApplyIntentHint_DefaultOrderBy(t *testing.T) {
	spec := &QuerySpec{
		Metric:  "sum(LineTotal)",
		GroupBy: []string{"ArtistName"},
	}
	hint := &IntentHint{
		Name:                "Sales.ByArtist",
		DefaultOrderByLabel: "Total_LineTotal",
		DefaultOrderByDir:   "DESC",
	}
	applyIntentHint(spec, hint)
	if len(spec.OrderBy) != 1 ||
		spec.OrderBy[0].Prop != "Total_LineTotal" ||
		spec.OrderBy[0].Dir != "DESC" {
		t.Errorf("expected default ORDER BY injected, got %+v", spec.OrderBy)
	}
}

func TestApplyIntentHint_DefaultOrderByNotInjectedWhenLLMSet(t *testing.T) {
	llmOrder := []OrderByItem{{Prop: "ArtistName", Dir: "ASC"}}
	spec := &QuerySpec{OrderBy: llmOrder}
	hint := &IntentHint{
		Name:                "Sales.ByArtist",
		DefaultOrderByLabel: "Total_LineTotal",
		DefaultOrderByDir:   "DESC",
	}
	applyIntentHint(spec, hint)
	if !reflect.DeepEqual(spec.OrderBy, llmOrder) {
		t.Errorf("expected LLM orderBy to win, got %+v", spec.OrderBy)
	}
}

func TestApplyIntentHint_DefaultOrderByDefaultsToDESC(t *testing.T) {
	spec := &QuerySpec{}
	hint := &IntentHint{Name: "X", DefaultOrderByLabel: "Total"}
	applyIntentHint(spec, hint)
	if len(spec.OrderBy) != 1 || spec.OrderBy[0].Dir != "DESC" {
		t.Errorf("expected default direction DESC, got %+v", spec.OrderBy)
	}
}

func TestApplyIntentHint_DefaultLimit(t *testing.T) {
	spec := &QuerySpec{Limit: 0}
	hint := &IntentHint{Name: "X", DefaultLimit: 10}
	applyIntentHint(spec, hint)
	if spec.Limit != 10 {
		t.Errorf("expected default limit 10, got %d", spec.Limit)
	}
}

func TestApplyIntentHint_DefaultLimitOverridesNormalizeCeiling(t *testing.T) {
	// NormalizeQuerySpec sets Limit=1000 as a sanity ceiling; default_limit
	// from intent (e.g. 10 for ranking) should override that ceiling.
	spec := &QuerySpec{Limit: 1000}
	hint := &IntentHint{Name: "X", DefaultLimit: 10}
	applyIntentHint(spec, hint)
	if spec.Limit != 10 {
		t.Errorf("expected default limit to override 1000 ceiling, got %d", spec.Limit)
	}
}

func TestApplyIntentHint_LLMLimitWinsOverDefault(t *testing.T) {
	// User said "Top 5" → LLM-supplied limit=5 < ceiling → preserved.
	spec := &QuerySpec{Limit: 5}
	hint := &IntentHint{Name: "X", DefaultLimit: 10}
	applyIntentHint(spec, hint)
	if spec.Limit != 5 {
		t.Errorf("expected LLM-supplied limit 5 to win, got %d", spec.Limit)
	}
}

func TestApplyIntentHint_CanonicalFiltersMerged(t *testing.T) {
	spec := &QuerySpec{
		Filters: []FilterItem{{Prop: "Geo", Op: "=", Value: "US"}},
	}
	hint := &IntentHint{
		Name: "ConfirmedSales",
		CanonicalFilters: []FilterItem{
			{Prop: "Status", Op: "=", Value: "Confirmed"},
		},
	}
	applyIntentHint(spec, hint)
	if len(spec.Filters) != 2 {
		t.Fatalf("expected 2 filters (Geo + Status), got %v", spec.Filters)
	}
	hasStatus := false
	for _, f := range spec.Filters {
		if f.Prop == "Status" && f.Value == "Confirmed" {
			hasStatus = true
		}
	}
	if !hasStatus {
		t.Errorf("expected canonical filter Status=Confirmed merged, got %v", spec.Filters)
	}
}

func TestApplyIntentHint_CanonicalFiltersUserOverrideWins(t *testing.T) {
	// User explicitly filtered Status=Pending; intent's canonical
	// Status=Confirmed should NOT clobber the user override.
	spec := &QuerySpec{
		Filters: []FilterItem{{Prop: "Status", Op: "=", Value: "Pending"}},
	}
	hint := &IntentHint{
		Name: "ConfirmedSales",
		CanonicalFilters: []FilterItem{
			{Prop: "Status", Op: "=", Value: "Confirmed"},
		},
	}
	applyIntentHint(spec, hint)
	if len(spec.Filters) != 1 || spec.Filters[0].Value != "Pending" {
		t.Errorf("expected user override to win, got %+v", spec.Filters)
	}
}

func TestDefaultPipeline_BareSpecWithIntentDefaultsPasses(t *testing.T) {
	// Bare spec (no metric, no groupBy) but with intent that injects
	// metric+groupBy via auto_group_by + canonical_metric → gate passes.
	spec := QuerySpec{
		Objects: []string{"SaleLine"},
		IntentHint: &IntentHint{
			Name:                "Sales.ByArtist",
			CanonicalMetric:     "sum(LineTotal)",
			AutoGroupBy:         []string{"ArtistName"},
			DefaultOrderByLabel: "Total_LineTotal",
			DefaultOrderByDir:   "DESC",
			DefaultLimit:        10,
		},
	}
	if err := DefaultPipeline.Run(&spec); err != nil {
		t.Fatalf("expected pipeline to pass with full intent hint, got %v", err)
	}
	if spec.Metric != "sum(LineTotal)" {
		t.Errorf("expected metric injected, got %q", spec.Metric)
	}
	if len(spec.GroupBy) != 1 || spec.GroupBy[0] != "ArtistName" {
		t.Errorf("expected groupBy injected, got %v", spec.GroupBy)
	}
	if len(spec.OrderBy) != 1 || spec.OrderBy[0].Dir != "DESC" {
		t.Errorf("expected default order injected, got %v", spec.OrderBy)
	}
	if spec.Limit != 10 {
		t.Errorf("expected default limit injected, got %d", spec.Limit)
	}
}

func TestPassGateMetricOrGroupBy_Bare(t *testing.T) {
	spec := &QuerySpec{}
	err := PassGateMetricOrGroupBy(spec)
	if err == nil {
		t.Fatal("expected error for bare spec")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "MISSING_METRIC_OR_GROUPBY" {
		t.Errorf("expected MISSING_METRIC_OR_GROUPBY ResolveError, got %v", err)
	}
}

func TestPassGateMetricOrGroupBy_AcceptsMetricOnly(t *testing.T) {
	spec := &QuerySpec{Metric: "sum(X)"}
	if err := PassGateMetricOrGroupBy(spec); err != nil {
		t.Errorf("metric-only spec must pass, got %v", err)
	}
}

func TestPassGateMetricOrGroupBy_AcceptsGroupByOnly(t *testing.T) {
	spec := &QuerySpec{GroupBy: []string{"Geo"}}
	if err := PassGateMetricOrGroupBy(spec); err != nil {
		t.Errorf("groupBy-only spec must pass, got %v", err)
	}
}

func TestDefaultPipeline_HintInjectsMetricThenGatePasses(t *testing.T) {
	spec := QuerySpec{
		Objects:    []string{"Sale"},
		IntentHint: &IntentHint{CanonicalMetric: "sum(LineTotal)"},
	}
	if err := DefaultPipeline.Run(&spec); err != nil {
		t.Fatalf("expected pipeline to pass, got %v", err)
	}
	if spec.Metric != "sum(LineTotal)" {
		t.Errorf("expected hint to inject metric, got %q", spec.Metric)
	}
}

func TestDefaultPipeline_BareSpecFailsAtGate(t *testing.T) {
	spec := QuerySpec{Objects: []string{"Sale"}}
	if err := DefaultPipeline.Run(&spec); err == nil {
		t.Fatal("expected gate to reject bare spec")
	}
}
