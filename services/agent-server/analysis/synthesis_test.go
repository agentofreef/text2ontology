package analysis

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

func patternWith(t *testing.T, tpl string, caveats []string) *recall.AnalysisPattern {
	t.Helper()
	cv, _ := json.Marshal(caveats)
	raw := json.RawMessage(`{
		"trigger": {"keywords":["k"]},
		"features": [
			{"id":"gross_revenue","behavior":"受冲击毛营收","verification":"v"},
			{"id":"store_count","behavior":"受影响门店数","verification":"v"}
		],
		"synthesis": {"template": ` + mustJSON(tpl) + `, "caveats": ` + string(cv) + `}
	}`)
	p, err := recall.ParseAnalysisPattern(raw)
	if err != nil {
		t.Fatalf("ParseAnalysisPattern: %v", err)
	}
	return p
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// T5 — a blocked feature still appears in the final synthesis (honest section).
func TestRenderSynthesis_BlockedFeatureDeclared(t *testing.T) {
	p := patternWith(t,
		"毛营收 {{ .features.gross_revenue.value }} 元。",
		[]string{"毛营收不等于净损失"})
	l := NewFeatureLedger(p)

	// gross_revenue passes
	_ = l.Activate("gross_revenue")
	_ = l.Pass("gross_revenue", Evidence{Value: "8,380,820", Summary: "5 城市分布"})
	// store_count blocked
	_ = l.Activate("store_count")
	_ = l.Block("store_count", Evidence{Error: "engine 多聚合解析 bug"})

	out, err := RenderSynthesis(l)
	if err != nil {
		t.Fatalf("RenderSynthesis: %v", err)
	}
	if !strings.Contains(out, "8,380,820") {
		t.Errorf("template value not rendered:\n%s", out)
	}
	if !strings.Contains(out, "受影响门店数") {
		t.Errorf("blocked feature behavior missing from synthesis:\n%s", out)
	}
	if !strings.Contains(out, "engine 多聚合解析 bug") {
		t.Errorf("blocked feature error reason missing:\n%s", out)
	}
	if !strings.Contains(out, "以下维度本次未取到") {
		t.Errorf("honest-declaration header missing:\n%s", out)
	}
}

// T5b — a blocked feature's summary/value must NOT leak into the template
// body even when the LLM attached one to the verify_feature(blocked) call.
// Only the blocked-declaration section accounts for it.
func TestRenderSynthesis_BlockedSummaryDoesNotLeak(t *testing.T) {
	p := patternWith(t,
		"营收 {{ .features.gross_revenue.value }}。{{ if .features.store_count.summary }}门店：{{ .features.store_count.summary }}{{ end }}",
		nil)
	l := NewFeatureLedger(p)

	_ = l.Activate("gross_revenue")
	_ = l.Pass("gross_revenue", Evidence{Value: "838万"})
	// store_count blocked, but the LLM attached a (wrong-kind) summary + value.
	_ = l.Activate("store_count")
	_ = l.Block("store_count", Evidence{
		Summary: "受影响订单行数：256,607行",
		Value:   "256607",
		Error:   "engine cross-OD limit",
	})

	out, err := RenderSynthesis(l)
	if err != nil {
		t.Fatalf("RenderSynthesis: %v", err)
	}
	if strings.Contains(out, "256,607") || strings.Contains(out, "256607") {
		t.Errorf("blocked feature summary/value leaked into body:\n%s", out)
	}
	if strings.Contains(out, "门店：") {
		t.Errorf("blocked feature's {{ if .summary }} branch should be empty:\n%s", out)
	}
	// It must still be declared as not-obtained.
	if !strings.Contains(out, "engine cross-OD limit") {
		t.Errorf("blocked feature error missing from declaration:\n%s", out)
	}
}

// T6 — caveats are appended verbatim, every one, unmodified.
func TestRenderSynthesis_CaveatsVerbatim(t *testing.T) {
	caveats := []string{
		"毛营收不等于净损失：需扣除可替代部分",
		"时间口径是全量历史，不直接换算成年度",
		"替代率为定性估计，未做品类间需求弹性建模",
	}
	p := patternWith(t, "结论。", caveats)
	l := NewFeatureLedger(p)
	_ = l.Activate("gross_revenue")
	_ = l.Pass("gross_revenue", Evidence{Summary: "ok"})
	_ = l.Activate("store_count")
	_ = l.Pass("store_count", Evidence{Summary: "ok"})

	out, err := RenderSynthesis(l)
	if err != nil {
		t.Fatalf("RenderSynthesis: %v", err)
	}
	for _, c := range caveats {
		if !strings.Contains(out, c) {
			t.Errorf("caveat dropped or altered: %q\nfull output:\n%s", c, out)
		}
	}
	if !strings.Contains(out, "注意事项") {
		t.Errorf("caveat section header missing:\n%s", out)
	}
	// No blocked section when nothing is blocked.
	if strings.Contains(out, "以下维度本次未取到") {
		t.Errorf("blocked section should be absent when all passing:\n%s", out)
	}
}

// T6b — template field access uses lowercase keys per spec §9.4.
func TestRenderSynthesis_LowercaseVarSchema(t *testing.T) {
	p := patternWith(t,
		"rows={{ .features.gross_revenue.rows }} summary={{ .features.gross_revenue.summary }}",
		nil)
	l := NewFeatureLedger(p)
	_ = l.Activate("gross_revenue")
	_ = l.Pass("gross_revenue", Evidence{RowCount: 5, Summary: "五城"})
	_ = l.Activate("store_count")
	_ = l.Pass("store_count", Evidence{Summary: "ok"})

	out, err := RenderSynthesis(l)
	if err != nil {
		t.Fatalf("RenderSynthesis: %v", err)
	}
	if !strings.Contains(out, "rows=5") {
		t.Errorf("rows var not rendered:\n%s", out)
	}
	if !strings.Contains(out, "summary=五城") {
		t.Errorf("summary var not rendered:\n%s", out)
	}
}

// T6c — a malformed template returns an error, not a panic.
func TestRenderSynthesis_BadTemplate(t *testing.T) {
	p := patternWith(t, "{{ .features.x.", nil)
	l := NewFeatureLedger(p)
	if _, err := RenderSynthesis(l); err == nil {
		t.Fatal("expected error for malformed template, got nil")
	}
}
