package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// a 2-feature analysis_pattern OK card for tool-level tests.
func patternOkEntry() recall.OkEntry {
	return recall.OkEntry{
		ID:         "pat-1",
		Title:      "供应中断影响测算",
		EntryType:  "analysis",
		AnchorType: "analysis_pattern",
		SkillConfig: json.RawMessage(`{
			"trigger": {"keywords":["断供"]},
			"features": [
				{"id":"revenue","behavior":"受冲击毛营收","verification":"rows>0",
				 "tool_hints":[{"tool":"smartquery","intent":"supply_chain_impact"}]},
				{"id":"stores","behavior":"受影响门店数","verification":"has count"}
			],
			"synthesis": {
				"template":"毛营收 {{ .features.revenue.value }} 元。",
				"caveats":["毛营收不等于净损失"]
			}
		}`),
	}
}

func TestRunStartAnalysisPlan_HappyPath(t *testing.T) {
	oks := []recall.OkEntry{patternOkEntry()}
	st, res := runStartAnalysisPlan(oks, map[string]interface{}{"patternId": "pat-1"})
	if st == nil {
		t.Fatalf("expected state, got nil; res=%v", res)
	}
	if res["error"] != nil {
		t.Fatalf("unexpected error: %v", res["error"])
	}
	if res["started"] != true {
		t.Errorf("started flag missing: %v", res)
	}
	// First feature should be activated.
	af, ok := res["activeFeature"].(featureDescriptor)
	if !ok {
		t.Fatalf("activeFeature missing or wrong type: %T", res["activeFeature"])
	}
	if af.ID != "revenue" || af.State != "active" {
		t.Errorf("first active feature = %+v, want revenue/active", af)
	}
}

func TestRunStartAnalysisPlan_Errors(t *testing.T) {
	oks := []recall.OkEntry{patternOkEntry()}
	// missing patternId
	if _, res := runStartAnalysisPlan(oks, map[string]interface{}{}); res["error"] == nil {
		t.Error("expected error for missing patternId")
	}
	// unknown patternId
	if _, res := runStartAnalysisPlan(oks, map[string]interface{}{"patternId": "ghost"}); res["error"] == nil {
		t.Error("expected error for unknown patternId")
	}
	// not an analysis pattern (plain concept card)
	plain := []recall.OkEntry{{ID: "c1", Title: "concept", EntryType: "concept"}}
	if _, res := runStartAnalysisPlan(plain, map[string]interface{}{"patternId": "c1"}); res["error"] == nil {
		t.Error("expected error for non-analysis-pattern card")
	}
}

// Full flow: start → verify pass → verify pass → complete.
func TestAnalysisPlanFlow_AllPassing(t *testing.T) {
	oks := []recall.OkEntry{patternOkEntry()}
	st, _ := runStartAnalysisPlan(oks, map[string]interface{}{"patternId": "pat-1"})
	if st == nil {
		t.Fatal("start failed")
	}

	// verify feature 1
	res := runVerifyFeature(st, map[string]interface{}{
		"featureId": "revenue", "verdict": "pass",
		"tool": "smartquery", "value": "8,380,820", "summary": "5 城市",
	})
	if res["error"] != nil {
		t.Fatalf("verify revenue: %v", res["error"])
	}
	af, _ := res["activeFeature"].(featureDescriptor)
	if af.ID != "stores" {
		t.Errorf("after passing revenue, next active = %+v, want stores", af)
	}

	// verify feature 2
	res = runVerifyFeature(st, map[string]interface{}{
		"featureId": "stores", "verdict": "pass", "summary": "42 门店",
	})
	if res["error"] != nil {
		t.Fatalf("verify stores: %v", res["error"])
	}
	if res["allSettled"] != true {
		t.Errorf("after both pass, allSettled should be true: %v", res)
	}

	// complete
	answer, cres := runCompleteAnalysis(st)
	if cres["error"] != nil {
		t.Fatalf("complete: %v", cres["error"])
	}
	if !strings.Contains(answer, "8,380,820") {
		t.Errorf("final answer missing rendered value:\n%s", answer)
	}
	if !strings.Contains(answer, "毛营收不等于净损失") {
		t.Errorf("final answer missing caveat:\n%s", answer)
	}
}

// fail verdict three times → feature auto-escalates to blocked, still synthesised.
func TestAnalysisPlanFlow_RetryThenBlocked(t *testing.T) {
	oks := []recall.OkEntry{patternOkEntry()}
	st, _ := runStartAnalysisPlan(oks, map[string]interface{}{"patternId": "pat-1"})

	// fail revenue 3 times — retry budget is 2, 3rd fail escalates to blocked.
	for i := 0; i < 2; i++ {
		res := runVerifyFeature(st, map[string]interface{}{
			"featureId": "revenue", "verdict": "fail", "reasoning": "wrong tool",
		})
		if res["outcome"] != "retry" {
			t.Fatalf("fail #%d outcome = %v, want retry", i+1, res["outcome"])
		}
		// after retry, the same feature should be re-activated by activeFeatureBlock
		af, _ := res["activeFeature"].(featureDescriptor)
		if af.ID != "revenue" {
			t.Fatalf("after retry, active = %+v, want revenue", af)
		}
	}
	res := runVerifyFeature(st, map[string]interface{}{
		"featureId": "revenue", "verdict": "fail", "reasoning": "still no",
	})
	if res["outcome"] != "blocked" {
		t.Fatalf("3rd fail outcome = %v, want blocked", res["outcome"])
	}

	// stores still gets processed
	res = runVerifyFeature(st, map[string]interface{}{
		"featureId": "stores", "verdict": "pass", "summary": "42 门店",
	})
	if res["allSettled"] != true {
		t.Errorf("allSettled should be true after revenue blocked + stores passed: %v", res)
	}

	answer, _ := runCompleteAnalysis(st)
	if !strings.Contains(answer, "以下维度本次未取到") {
		t.Errorf("blocked feature not declared in synthesis:\n%s", answer)
	}
	if !strings.Contains(answer, "受冲击毛营收") {
		t.Errorf("blocked feature behavior missing:\n%s", answer)
	}
}

// verdict=blocked: LLM directly declares a feature unobtainable.
func TestRunVerifyFeature_DirectBlocked(t *testing.T) {
	oks := []recall.OkEntry{patternOkEntry()}
	st, _ := runStartAnalysisPlan(oks, map[string]interface{}{"patternId": "pat-1"})
	res := runVerifyFeature(st, map[string]interface{}{
		"featureId": "revenue", "verdict": "blocked",
		"reasoning": "engine COUNT(DISTINCT) bug",
	})
	if res["outcome"] != "blocked" {
		t.Errorf("verdict=blocked outcome = %v, want blocked", res["outcome"])
	}
}

func TestRunVerifyFeature_Guards(t *testing.T) {
	// nil state
	if res := runVerifyFeature(nil, map[string]interface{}{"featureId": "x", "verdict": "pass"}); res["error"] == nil {
		t.Error("expected error for nil state")
	}
	oks := []recall.OkEntry{patternOkEntry()}
	st, _ := runStartAnalysisPlan(oks, map[string]interface{}{"patternId": "pat-1"})
	// bad verdict
	if res := runVerifyFeature(st, map[string]interface{}{"featureId": "revenue", "verdict": "maybe"}); res["error"] == nil {
		t.Error("expected error for bad verdict")
	}
}

func TestRunCompleteAnalysis_GuardNilState(t *testing.T) {
	if _, res := runCompleteAnalysis(nil); res["error"] == nil {
		t.Error("expected error for nil state")
	}
}
