package recall

import (
	"encoding/json"
	"strings"
	"testing"
)

// formatAnalysisSkills renders analysis_pattern cards; ordinary OKs are skipped.
func TestFormatAnalysisSkills(t *testing.T) {
	entries := []OkEntry{
		{ // an analysis pattern
			ID: "pat-9", Title: "供应中断影响测算", Summary: "断供→营收影响",
			EntryType: "analysis", AnchorType: "analysis_pattern",
			SkillConfig: json.RawMessage(`{
				"trigger": {"keywords":["断供"],"structural_hints":["如果X断供"]},
				"features": [
					{"id":"revenue","behavior":"受冲击毛营收","verification":"v"},
					{"id":"subs","behavior":"可替代SKU","verification":"v"}
				],
				"synthesis": {"template":"t"}
			}`),
		},
		{ID: "c1", Title: "普通概念", EntryType: "concept"}, // ordinary OK
	}

	var sb strings.Builder
	formatAnalysisSkills(&sb, entries)
	out := sb.String()

	for _, want := range []string{
		"📊 分析 Skill", "patternId=pat-9", "供应中断影响测算",
		"start_analysis_plan", "revenue", "受冲击毛营收", "如果X断供",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("skill block missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "普通概念") {
		t.Errorf("ordinary OK leaked into skill block:\n%s", out)
	}
}

// no analysis-pattern cards → writes nothing.
func TestFormatAnalysisSkills_Empty(t *testing.T) {
	var sb strings.Builder
	formatAnalysisSkills(&sb, []OkEntry{{ID: "c1", Title: "x", EntryType: "concept"}})
	if sb.Len() != 0 {
		t.Errorf("expected empty output, got: %s", sb.String())
	}
}

// T1 — happy path: a well-formed skill_config parses into N features.
func TestParseAnalysisPattern_HappyPath(t *testing.T) {
	raw := json.RawMessage(`{
		"trigger": {
			"keywords": ["断供","停产","下架"],
			"structural_hints": ["如果 X 断供"]
		},
		"features": [
			{
				"id": "gross_revenue_impact",
				"behavior": "受冲击毛营收（按城市分布）",
				"verification": "result.rows > 0 AND sum(metric) > 0",
				"tool_hints": [
					{"tool":"smartquery","intent":"supply_chain_impact"},
					{"tool":"compose_query"}
				]
			},
			{
				"id": "substitution_candidates",
				"behavior": "可替代 SKU 列表",
				"verification": "result.rows >= 0"
			}
		],
		"synthesis": {
			"template": "毛营收 {{ .features.gross_revenue_impact.value }}",
			"caveats": ["毛营收不等于净损失"]
		}
	}`)

	p, err := ParseAnalysisPattern(raw)
	if err != nil {
		t.Fatalf("expected ok, got error: %v", err)
	}
	if len(p.Features) != 2 {
		t.Fatalf("expected 2 features, got %d", len(p.Features))
	}
	if p.Features[0].ID != "gross_revenue_impact" {
		t.Errorf("feature[0].id = %q, want gross_revenue_impact", p.Features[0].ID)
	}
	if len(p.Features[0].ToolHints) != 2 {
		t.Errorf("feature[0] tool_hints len = %d, want 2", len(p.Features[0].ToolHints))
	}
	if p.Features[0].ToolHints[0].Intent != "supply_chain_impact" {
		t.Errorf("feature[0].tool_hints[0].intent = %q, want supply_chain_impact",
			p.Features[0].ToolHints[0].Intent)
	}
	if len(p.Trigger.Keywords) != 3 {
		t.Errorf("trigger.keywords len = %d, want 3", len(p.Trigger.Keywords))
	}
	if len(p.Synthesis.Caveats) != 1 {
		t.Errorf("synthesis.caveats len = %d, want 1", len(p.Synthesis.Caveats))
	}
}

// T1b — error cases: structural validation.
func TestParseAnalysisPattern_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // substring of expected error
	}{
		{"empty raw", ``, "empty skill_config"},
		{"malformed json", `{not json`, "decode skill_config"},
		{"no features",
			`{"features":[],"synthesis":{"template":"x"}}`,
			"features list is empty"},
		{"feature missing id",
			`{"features":[{"behavior":"b","verification":"v"}],"synthesis":{"template":"x"}}`,
			"missing id"},
		{"feature missing behavior",
			`{"features":[{"id":"f1","verification":"v"}],"synthesis":{"template":"x"}}`,
			"missing behavior"},
		{"feature missing verification",
			`{"features":[{"id":"f1","behavior":"b"}],"synthesis":{"template":"x"}}`,
			"missing verification"},
		{"duplicate feature id",
			`{"features":[
				{"id":"f1","behavior":"b","verification":"v"},
				{"id":"f1","behavior":"b","verification":"v"}
			 ],"synthesis":{"template":"x"}}`,
			"duplicate feature id"},
		{"empty template",
			`{"features":[{"id":"f1","behavior":"b","verification":"v"}],"synthesis":{}}`,
			"synthesis.template missing"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAnalysisPattern(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// T1c — IsAnalysisPattern gating: only fires when all three signals align.
func TestOkEntry_IsAnalysisPattern(t *testing.T) {
	cases := []struct {
		name  string
		entry OkEntry
		want  bool
	}{
		{"all three set", OkEntry{
			EntryType: "analysis", AnchorType: "analysis_pattern",
			SkillConfig: json.RawMessage(`{}`),
		}, true},
		{"wrong entry_type", OkEntry{
			EntryType: "concept", AnchorType: "analysis_pattern",
			SkillConfig: json.RawMessage(`{}`),
		}, false},
		{"wrong anchor_type", OkEntry{
			EntryType: "analysis", AnchorType: "version",
			SkillConfig: json.RawMessage(`{}`),
		}, false},
		{"empty skill_config", OkEntry{
			EntryType: "analysis", AnchorType: "analysis_pattern",
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.IsAnalysisPattern(); got != tc.want {
				t.Errorf("IsAnalysisPattern() = %v, want %v", got, tc.want)
			}
		})
	}
}
