package smartquery

import (
	"errors"
	"strings"
	"testing"
)

func TestPassValidateSpec_Empty(t *testing.T) {
	// Empty spec is structurally valid here (PassGateMetricOrGroupBy
	// catches the "no metric AND no groupBy" case earlier in DefaultPipeline).
	if err := PassValidateSpec(&QuerySpec{}); err != nil {
		t.Errorf("empty spec must pass validation, got %v", err)
	}
}

func TestPassValidateSpec_NegativeLimit(t *testing.T) {
	err := PassValidateSpec(&QuerySpec{Limit: -1})
	if err == nil {
		t.Fatal("expected negative limit rejected")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "SPEC_VALIDATION_FAILED" {
		t.Errorf("expected SPEC_VALIDATION_FAILED, got %v", err)
	}
}

func TestPassValidateSpec_LimitZeroAllowed(t *testing.T) {
	// Limit=0 means "no LIMIT clause" — pivot path uses this.
	if err := PassValidateSpec(&QuerySpec{Limit: 0, Metric: "sum(x)"}); err != nil {
		t.Errorf("Limit=0 must be allowed (pivot path), got %v", err)
	}
}

func TestPassValidateSpec_EmptyGroupByEntry(t *testing.T) {
	err := PassValidateSpec(&QuerySpec{GroupBy: []string{"  ", "ArtistName"}})
	if err == nil {
		t.Fatal("expected empty groupBy entry rejected")
	}
}

func TestPassValidateSpec_OrderByMissingProp(t *testing.T) {
	// Real failure mode: LLM splits {prop:"x",dir:"DESC"} into two array items.
	err := PassValidateSpec(&QuerySpec{
		OrderBy: []OrderByItem{
			{Prop: "Total_LineTotal"}, // no dir
			{Dir: "DESC"},              // no prop ← this trips
		},
	})
	if err == nil {
		t.Fatal("expected orderBy without prop rejected")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Code != "SPEC_VALIDATION_FAILED" {
		t.Errorf("expected SPEC_VALIDATION_FAILED, got %v", err)
	}
}

func TestPassValidateSpec_OrderByInvalidDir(t *testing.T) {
	err := PassValidateSpec(&QuerySpec{
		OrderBy: []OrderByItem{{Prop: "x", Dir: "WHATEVER"}},
	})
	if err == nil {
		t.Fatal("expected invalid dir rejected")
	}
}

func TestPassValidateSpec_OrderByValid(t *testing.T) {
	if err := PassValidateSpec(&QuerySpec{
		OrderBy: []OrderByItem{{Prop: "x", Dir: "DESC"}, {Prop: "y", Dir: "asc"}},
	}); err != nil {
		t.Errorf("valid orderBy must pass, got %v", err)
	}
}

func TestPassValidateSpec_FilterMissingProp(t *testing.T) {
	err := PassValidateSpec(&QuerySpec{
		Filters: []FilterItem{{Op: "=", Value: "Rock"}},
	})
	if err == nil {
		t.Fatal("expected filter without prop rejected")
	}
}

func TestPassValidateSpec_FilterMissingOp(t *testing.T) {
	err := PassValidateSpec(&QuerySpec{
		Filters: []FilterItem{{Prop: "GenreName", Value: "Rock"}},
	})
	if err == nil {
		t.Fatal("expected filter without op rejected")
	}
}

func TestPassValidateSpec_FilterInvalidOp(t *testing.T) {
	err := PassValidateSpec(&QuerySpec{
		Filters: []FilterItem{{Prop: "x", Op: "weird_op", Value: "v"}},
	})
	if err == nil {
		t.Fatal("expected invalid op rejected")
	}
}

func TestPassValidateSpec_FilterCanonicalOpsAllPass(t *testing.T) {
	for _, op := range []string{"=", "<>", ">", ">=", "<", "<=",
		"contains", "not contains", "starts with", "ends with",
		"like", "in", "not in", "is blank", "is not blank",
		"between", "regex"} {
		err := PassValidateSpec(&QuerySpec{
			Filters: []FilterItem{{Prop: "x", Op: op, Value: "v"}},
		})
		if err != nil {
			t.Errorf("op %q must pass, got %v", op, err)
		}
	}
}

func TestPassValidateSpec_BatchedErrors(t *testing.T) {
	// All offences reported in a single ResolveError so caller doesn't have
	// to rerun multiple times to see them.
	err := PassValidateSpec(&QuerySpec{
		Limit:   -5,
		GroupBy: []string{""},
		OrderBy: []OrderByItem{{Dir: "BAD"}},
		Filters: []FilterItem{{Op: "??"}},
	})
	if err == nil {
		t.Fatal("expected multi-error rejection")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatal("expected ResolveError")
	}
	probs, ok := re.Detail["errors"].([]string)
	if !ok || len(probs) < 4 {
		t.Errorf("expected 4+ batched errors, got %d: %v", len(probs), probs)
	}
}

func TestPassValidateSpec_ErrorContainsLLMHint(t *testing.T) {
	// LLM-friendly hint for the orderBy-split case.
	err := PassValidateSpec(&QuerySpec{
		OrderBy: []OrderByItem{{Prop: "x"}, {Dir: "DESC"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "拆成两个 item") {
		t.Errorf("expected LLM hint about orderBy split, got %v", err)
	}
}

func TestDefaultPipeline_IncludesValidate(t *testing.T) {
	// Regression: ensure DefaultPipeline now has PassValidateSpec.
	// A spec that passes the gate but has bad orderBy must be rejected.
	spec := QuerySpec{
		Metric:  "sum(x)",
		OrderBy: []OrderByItem{{Dir: "DESC"}}, // missing prop
	}
	err := DefaultPipeline.Run(&spec)
	if err == nil {
		t.Fatal("expected DefaultPipeline to reject bad orderBy via PassValidateSpec")
	}
	if !strings.Contains(err.Error(), "spec 校验失败") {
		t.Errorf("expected validation error, got %v", err)
	}
}
