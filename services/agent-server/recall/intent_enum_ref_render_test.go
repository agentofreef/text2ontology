package recall

// T6 · prompt rendering (bounded value-ref contract,
// .omc/specs/bounded-value-ref-contract.md §4 T6 / §3.3).
//
// formatMetricIntent must render the candidate set for enum_ref parameters
// so the LLM sees "from this finite set, pick one" instead of "fill in a
// string and pray". Without this, the binder's PARAM_VALUE_UNKNOWN fires
// on every miss and the agent loop has to brute-force guess.

import (
	"strings"
	"testing"
)

// T6a · enum_ref with populated AllowedValues renders a "必须从以下选一个" line
// listing every candidate.
func TestFormatMetricIntent_EnumRefRendersCandidates(t *testing.T) {
	mi := MetricIntent{
		Name:            "store_revenue",
		ObjectName:      "STORE",
		CanonicalMetric: "sum(paid_amount_cny)",
		Parameters: []MetricIntentParameter{
			{
				Name: "city", Type: "enum_ref", Property: "city", Optional: true,
				Description:   "城市",
				AllowedValues: []string{"上海", "北京"},
			},
		},
	}

	var sb strings.Builder
	formatMetricIntent(&sb, mi, nil)
	out := sb.String()

	if !strings.Contains(out, "enum_ref") {
		t.Errorf("rendered help must mention enum_ref type, got:\n%s", out)
	}
	if !strings.Contains(out, "必须从以下选一个") {
		t.Errorf("rendered help must say %q for enum_ref, got:\n%s",
			"必须从以下选一个", out)
	}
	if !strings.Contains(out, "上海") || !strings.Contains(out, "北京") {
		t.Errorf("rendered help must list every candidate, got:\n%s", out)
	}
}

// T6b · enum_ref WITHOUT AllowedValues (recall side couldn't resolve, or
// list exceeded cap) falls back to the same single-line format as a regular
// string param — no half-rendered "必须从以下选一个: " with empty bracket.
func TestFormatMetricIntent_EnumRefWithoutCandidatesNoHint(t *testing.T) {
	mi := MetricIntent{
		Name:       "store_revenue",
		ObjectName: "STORE",
		Parameters: []MetricIntentParameter{
			{Name: "city", Type: "enum_ref", Property: "city", Optional: true, Description: "城市"},
		},
	}

	var sb strings.Builder
	formatMetricIntent(&sb, mi, nil)
	out := sb.String()

	if strings.Contains(out, "必须从以下选一个") {
		t.Errorf("must NOT render candidate hint when AllowedValues is empty, got:\n%s", out)
	}
}
