package handler

import (
	"strings"
	"testing"

	. "github.com/lakehouse2ontology/httputil"
)

// DT02 — a tagged data result renders its step id + addressing hint so the
// LLM can write 「sum(t1.col)」 references instead of transcribing numbers.
func TestToolResultToMarkdown_StepIDHeader(t *testing.T) {
	result := M{
		"execution_status": "success",
		"execution_result": `[{"city":"上海","amount":2735766},{"city":"北京","amount":1973348}]`,
		"step_id":          "t1",
	}
	out := toolResultToMarkdown("smartquery", nil, result)

	if !strings.Contains(out, "id=t1") {
		t.Errorf("step id header missing:\n%s", out)
	}
	if !strings.Contains(out, "「sum(t1.列名)」") {
		t.Errorf("reference syntax hint missing:\n%s", out)
	}
	// columns must still be present in the TOON header
	if !strings.Contains(out, "amount") || !strings.Contains(out, "city") {
		t.Errorf("column header missing:\n%s", out)
	}
}

// A result with no step_id (e.g. an untagged tool) renders unchanged — the
// data-template header is absent, preserving backward behaviour.
func TestToolResultToMarkdown_NoStepID(t *testing.T) {
	result := M{
		"execution_status": "success",
		"execution_result": `[{"city":"上海","amount":2735766}]`,
	}
	out := toolResultToMarkdown("smartquery", nil, result)
	if strings.Contains(out, "id=t") {
		t.Errorf("unexpected step id header on untagged result:\n%s", out)
	}
	if !strings.Contains(out, "status: success") {
		t.Errorf("result table not rendered:\n%s", out)
	}
}
