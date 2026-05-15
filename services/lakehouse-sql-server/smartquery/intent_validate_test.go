package smartquery

import (
	"strings"
	"testing"
)

func TestValidateIntentDryRun_MinimalValid(t *testing.T) {
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:            "Sales.ByArtist",
		ObjectName:      "SaleLine",
		CanonicalMetric: "sum(LineTotal)",
		AutoGroupBy:     []string{"ArtistName"},
	})
	if !r.Ok {
		t.Fatalf("expected ok, got %+v", r)
	}
	if r.BoundSpec == nil || r.BoundSpec.Metric != "sum(LineTotal)" {
		t.Errorf("expected bound metric, got %+v", r.BoundSpec)
	}
}

func TestValidateIntentDryRun_MissingObject(t *testing.T) {
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:            "X",
		CanonicalMetric: "sum(x)",
	})
	if r.Ok {
		t.Fatal("expected fail without object")
	}
	if r.Code != "MISSING_OBJECT_NAME" {
		t.Errorf("expected MISSING_OBJECT_NAME, got %q", r.Code)
	}
}

func TestValidateIntentDryRun_BareIntentRejected(t *testing.T) {
	// Intent with no metric AND no groupBy → bind produces empty spec →
	// PassGateMetricOrGroupBy fails with MISSING_METRIC_OR_GROUPBY.
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:       "Bare",
		ObjectName: "X",
	})
	if r.Ok {
		t.Fatal("expected bare intent rejected")
	}
	if r.Code != "MISSING_METRIC_OR_GROUPBY" {
		t.Errorf("expected MISSING_METRIC_OR_GROUPBY, got %q errors=%v", r.Code, r.Errors)
	}
}

func TestValidateIntentDryRun_BadDefaultOrderDirNormalised(t *testing.T) {
	// applyIntentHint normalises any non-{ASC,DESC} dir to "DESC" before
	// PassValidateSpec sees it — so dry-run accepts this. The DB CHECK
	// constraint is the actual gate against bad dirs at save time
	// (lakehouse_metric_intent_default_order_by_dir_chk in migration
	// 20260502_intent_default_shape_and_parameters.sql).
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:                "X",
		ObjectName:          "Sale",
		CanonicalMetric:     "sum(x)",
		DefaultOrderByLabel: "Total_x",
		DefaultOrderByDir:   "WHATEVER",
	})
	if !r.Ok {
		t.Errorf("expected dry-run to accept (apply normalises dir), got %+v", r)
	}
	if len(r.BoundSpec.OrderBy) != 1 || r.BoundSpec.OrderBy[0].Dir != "DESC" {
		t.Errorf("expected dir normalised to DESC, got %+v", r.BoundSpec.OrderBy)
	}
}

func TestValidateIntentDryRun_PropertyFilterMissingProperty(t *testing.T) {
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:            "X",
		ObjectName:      "Sale",
		CanonicalMetric: "sum(x)",
		Parameters: []IntentParameter{
			{Name: "g", Type: "property_filter" /* no Property */},
		},
	})
	if r.Ok {
		t.Fatal("expected fail")
	}
	if r.Code != "PARAM_SCHEMA_INVALID" {
		t.Errorf("expected PARAM_SCHEMA_INVALID, got %q", r.Code)
	}
}

func TestValidateIntentDryRun_UnknownParamType(t *testing.T) {
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:            "X",
		ObjectName:      "Sale",
		CanonicalMetric: "sum(x)",
		Parameters: []IntentParameter{
			{Name: "z", Type: "unknown_kind"},
		},
	})
	if r.Ok {
		t.Fatal("expected fail")
	}
	if r.Code != "PARAM_SCHEMA_INVALID" {
		t.Errorf("expected PARAM_SCHEMA_INVALID, got %q", r.Code)
	}
}

func TestValidateIntentDryRun_SalesByArtistFullShape(t *testing.T) {
	// Real Chinook seed. Should pass cleanly.
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:                "Sales.ByArtist",
		ObjectName:          "SaleLine",
		CanonicalMetric:     "sum(LineTotal)",
		AutoGroupBy:         []string{"ArtistName"},
		DefaultOrderByLabel: "Total_LineTotal",
		DefaultOrderByDir:   "DESC",
		DefaultLimit:        10,
		Parameters: []IntentParameter{
			{Name: "n", Type: "int", Default: float64(10)},
			{Name: "genre", Type: "property_filter", Property: "GenreName", Optional: true},
			{Name: "country", Type: "property_filter", Property: "BillingCountry", Optional: true},
		},
	})
	if !r.Ok {
		t.Fatalf("expected ok, got %+v", r)
	}
	if r.BoundSpec.Limit != 10 {
		t.Errorf("expected default limit applied, got %d", r.BoundSpec.Limit)
	}
	if len(r.BoundSpec.GroupBy) != 1 || r.BoundSpec.GroupBy[0] != "ArtistName" {
		t.Errorf("expected groupBy=[ArtistName], got %v", r.BoundSpec.GroupBy)
	}
	// Both genre and country params get dummy "x" → 2 filters appended.
	if len(r.BoundSpec.Filters) != 2 {
		t.Errorf("expected 2 dummy property filters, got %v", r.BoundSpec.Filters)
	}
}

func TestValidateIntentDryRun_RequiredParamMissing(t *testing.T) {
	// Required param with no Default — DummyParamValue still provides one,
	// so dry-run should NOT fail on required-missing. (Real LLM call would
	// fail at runtime if user omits the param; dry-run is structural only.)
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:            "X",
		ObjectName:      "Sale",
		CanonicalMetric: "sum(x)",
		Parameters: []IntentParameter{
			{Name: "g", Type: "property_filter", Property: "GenreName" /* required */},
		},
	})
	if !r.Ok {
		t.Errorf("dry-run should pass — DummyParamValue provides values for required params, got %+v", r)
	}
}

func TestExtractProblemList_BatchedErrors(t *testing.T) {
	re := &ResolveError{
		Code:    "SPEC_VALIDATION_FAILED",
		Message: "spec 校验失败：a；b",
		Detail:  map[string]any{"errors": []string{"a", "b"}},
	}
	probs := extractProblemList(re, "")
	if len(probs) != 2 || probs[0] != "a" || probs[1] != "b" {
		t.Errorf("expected [a b], got %v", probs)
	}
}

func TestAsResolveError_UnwrapsWrap(t *testing.T) {
	// DefaultPipeline wraps with fmt.Errorf("pipeline pass #N: %w", ...).
	// asResolveError must dig through that.
	r := ValidateIntentDryRun(IntentValidationInput{
		Name:       "X",
		ObjectName: "Sale", // bare → MISSING_METRIC_OR_GROUPBY
	})
	if r.Ok {
		t.Fatal("expected bare to fail")
	}
	if !strings.Contains(r.Code, "MISSING_METRIC_OR_GROUPBY") {
		t.Errorf("expected unwrapped code, got %q (errors=%v)", r.Code, r.Errors)
	}
}
