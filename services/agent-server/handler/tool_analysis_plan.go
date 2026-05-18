package handler

import (
	"errors"
	"fmt"

	"github.com/lakehouse2ontology/services/agent-server/analysis"
	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

// Plan-mode tools (.omc/specs/plan-from-ontology-knowledge.md §3.5).
//
// Three tools drive an analysis plan within a single agent turn:
//
//	start_analysis_plan({patternId, reason})  — build the FeatureLedger from a
//	    recalled analysis_pattern OK card, activate the first feature.
//	verify_feature({featureId, verdict, ...}) — record the LLM's verdict on the
//	    active feature, transition state, activate the next feature.
//	complete_analysis({})                     — render the final synthesis.
//
// The 3 run* functions below are pure: they take the turn state + args and
// return a tool-result M. The handler owns the *analysisPlanState lifetime
// (one per turn, nil until start_analysis_plan succeeds) and the loop-break
// when complete_analysis fires.

// LLM-facing tool descriptions for the three plan-mode tools.
const (
	startAnalysisPlanToolDescription = `进入「分析计划」模式。

当 context 顶部出现「📊 分析 Skill」块、且用户问的是"影响 / 为什么 / 如何 / 综合评估"这类需要多维度展开的问题时调用本工具。
若用户只想"快查一个数"，不要进入分析计划，直接用 smartquery / compose_query。

调用后服务端会基于该分析模式卡片建立一份「特征清单」(feature list)，并激活第一个特征。
随后你按 WIP=1 规则逐个特征推进：每个特征用一个数据工具拿到结果 → 调用 verify_feature 上报 → 服务端激活下一个。`

	verifyFeatureToolDescription = `上报当前 active 特征的验证结论。

在你用 smartquery / compose_query / lookup 等工具为某个特征拿到数据之后调用。
verdict：
  pass    — 数据满足该特征的验证条件
  fail    — 不满足；服务端会退回让你换工具/参数重试（预算 2 次，耗尽自动转 blocked）
  blocked — 该维度确实拿不到（如引擎 bug、数据缺失）；诚实标记，好过偷换问题
服务端返回下一个 active 特征，或提示所有特征已闭环、该调用 complete_analysis。`

	completeAnalysisToolDescription = `所有特征闭环后调用，生成最终分析答复。

服务端按分析模式卡片的 synthesis 模板渲染数字+维度，并逐字附加 caveat 注意事项。
这一步产出的就是最终答复，你不需要再自己复述或改写。`
)

// analysisPlanState is the per-turn plan-mode state. nil until a successful
// start_analysis_plan. Not persisted across turns in v1 (spec §2).
type analysisPlanState struct {
	ledger      *analysis.FeatureLedger
	patternID   string
	patternName string
}

// featureDescriptor is the LLM-facing shape of one feature in tool results.
type featureDescriptor struct {
	ID           string                    `json:"id"`
	Behavior     string                    `json:"behavior"`
	Verification string                    `json:"verification"`
	State        string                    `json:"state"`
	ToolHints    []recall.AnalysisToolHint  `json:"toolHints,omitempty"`
}

// describeLedger renders the full feature list for a tool result.
func describeLedger(l *analysis.FeatureLedger) []featureDescriptor {
	snap := l.Snapshot()
	out := make([]featureDescriptor, len(snap))
	for i, r := range snap {
		out[i] = featureDescriptor{
			ID:           r.ID,
			Behavior:     r.Behavior,
			Verification: r.Verification,
			State:        string(r.State),
			ToolHints:    r.ToolHints,
		}
	}
	return out
}

// activeFeatureBlock builds the "what to do next" payload: either the active
// feature or a flag that everything is settled.
func activeFeatureBlock(l *analysis.FeatureLedger) M {
	next, ok := l.PickNext()
	if !ok {
		// Nothing not_started — either all settled or one is active.
		for _, r := range l.Snapshot() {
			if r.State == analysis.StateActive {
				return M{
					"activeFeature": featureDescriptor{
						ID: r.ID, Behavior: r.Behavior,
						Verification: r.Verification, State: string(r.State),
						ToolHints: r.ToolHints,
					},
					"instruction": fmt.Sprintf(
						"特征 %q 仍在进行中。用一个工具（smartquery / compose_query / query_dag / lookup）拿到数据，再调用 verify_feature 上报结论。",
						r.ID),
				}
			}
		}
		return M{
			"allSettled": true,
			"instruction": "所有特征已闭环（passing 或 blocked）。立即调用 complete_analysis 生成最终答复。",
		}
	}
	// Activate the next not_started feature (WIP=1).
	if err := l.Activate(next.ID); err != nil {
		return M{"error": fmt.Sprintf("无法激活特征 %q: %v", next.ID, err)}
	}
	return M{
		"activeFeature": featureDescriptor{
			ID: next.ID, Behavior: next.Behavior,
			Verification: next.Verification, State: string(analysis.StateActive),
			ToolHints: next.ToolHints,
		},
		"instruction": fmt.Sprintf(
			"现在处理特征 %q：%s。用一个工具拿到数据满足验证条件「%s」，然后调用 verify_feature 上报。一次只做这一个特征。",
			next.ID, next.Behavior, next.Verification),
	}
}

// runStartAnalysisPlan builds a FeatureLedger from a recalled analysis_pattern
// OK card and activates the first feature.
//
// okEntries is the recall result's OkEntries slice (the patternId must match
// one whose IsAnalysisPattern() is true).
func runStartAnalysisPlan(okEntries []recall.OkEntry, args map[string]interface{}) (*analysisPlanState, M) {
	patternID, _ := args["patternId"].(string)
	if patternID == "" {
		return nil, M{"error": "start_analysis_plan: patternId 必填"}
	}

	var card *recall.OkEntry
	for i := range okEntries {
		if okEntries[i].ID == patternID {
			card = &okEntries[i]
			break
		}
	}
	if card == nil {
		return nil, M{
			"error": fmt.Sprintf("start_analysis_plan: 在本轮 recall 结果里找不到 patternId %q", patternID),
		}
	}
	if !card.IsAnalysisPattern() {
		return nil, M{
			"error": fmt.Sprintf("start_analysis_plan: OK 卡片 %q 不是分析模式卡片（entry_type/anchor_type/skill_config 不全）", patternID),
		}
	}

	pattern, err := recall.ParseAnalysisPattern(card.SkillConfig)
	if err != nil {
		return nil, M{
			"error": fmt.Sprintf("start_analysis_plan: 解析分析模式卡片失败: %v", err),
		}
	}

	st := &analysisPlanState{
		ledger:      analysis.NewFeatureLedger(pattern),
		patternID:   patternID,
		patternName: card.Title,
	}

	res := M{
		"started":     true,
		"patternId":   patternID,
		"patternName": card.Title,
		"features":    describeLedger(st.ledger),
	}
	for k, v := range activeFeatureBlock(st.ledger) {
		res[k] = v
	}
	return st, res
}

// runVerifyFeature records the LLM's verdict on the active feature and
// transitions the ledger.
//
// args:
//
//	featureId  string  — which feature (must be the active one)
//	verdict    string  — "pass" | "fail" | "blocked"
//	                       pass    → feature passes
//	                       fail    → retry (auto-escalates to blocked once the
//	                                 retry budget is exhausted)
//	                       blocked → LLM declares the feature unobtainable
//	                                 (e.g. engine bug) — terminal
//	tool       string  — tool the LLM used (evidence)
//	summary    string  — single-line result digest (evidence)
//	reasoning  string  — why this verdict (evidence)
//	value      string  — scalar result, if any (evidence)
//	rowCount   number  — row count, if any (evidence)
//	error      string  — failure/blocked reason (evidence)
func runVerifyFeature(st *analysisPlanState, args map[string]interface{}) M {
	if st == nil || st.ledger == nil {
		return M{"error": "verify_feature: 尚未进入分析计划，请先调用 start_analysis_plan"}
	}
	featureID, _ := args["featureId"].(string)
	if featureID == "" {
		return M{"error": "verify_feature: featureId 必填"}
	}
	verdict, _ := args["verdict"].(string)

	ev := analysis.Evidence{
		Tool:      strArg(args, "tool"),
		Summary:   strArg(args, "summary"),
		Reasoning: strArg(args, "reasoning"),
		Value:     strArg(args, "value"),
		Error:     strArg(args, "error"),
		RowCount:  intArg(args, "rowCount"),
	}

	var transitionErr error
	var outcome string
	switch verdict {
	case "pass":
		transitionErr = st.ledger.Pass(featureID, ev)
		outcome = "passing"
	case "fail":
		transitionErr = st.ledger.Retry(featureID, ev)
		if transitionErr != nil && errors.Is(transitionErr, analysis.ErrRetryExhausted) {
			// Budget exhausted — escalate to blocked automatically.
			if ev.Error == "" {
				ev.Error = "验证连续失败，重试预算耗尽"
			}
			transitionErr = st.ledger.Block(featureID, ev)
			outcome = "blocked"
		} else {
			outcome = "retry"
		}
	case "blocked":
		if ev.Error == "" {
			ev.Error = strArg(args, "reasoning")
		}
		transitionErr = st.ledger.Block(featureID, ev)
		outcome = "blocked"
	default:
		return M{"error": fmt.Sprintf("verify_feature: verdict 必须是 pass / fail / blocked，收到 %q", verdict)}
	}
	if transitionErr != nil {
		return M{"error": fmt.Sprintf("verify_feature: 状态转移失败: %v", transitionErr)}
	}

	passing, blocked, notStarted, active := st.ledger.CountByState()
	res := M{
		"featureId": featureID,
		"outcome":   outcome,
		"progress": M{
			"passing": passing, "blocked": blocked,
			"notStarted": notStarted, "active": active,
		},
		"features": describeLedger(st.ledger),
	}
	for k, v := range activeFeatureBlock(st.ledger) {
		res[k] = v
	}
	return res
}

// runCompleteAnalysis renders the final synthesis. Returns the final answer
// string (machine-stitched: template + verbatim caveats + blocked declarations)
// and the tool-result M. The handler emits finalAnswer directly as the turn's
// answer — the LLM does not get to rephrase it (spec §9.5).
func runCompleteAnalysis(st *analysisPlanState) (finalAnswer string, res M) {
	if st == nil || st.ledger == nil {
		return "", M{"error": "complete_analysis: 尚未进入分析计划，请先调用 start_analysis_plan"}
	}
	answer, err := analysis.RenderSynthesis(st.ledger)
	if err != nil {
		return "", M{"error": fmt.Sprintf("complete_analysis: 合成失败: %v", err)}
	}
	passing, blocked, _, _ := st.ledger.CountByState()
	return answer, M{
		"completed":    true,
		"patternName":  st.patternName,
		"finalAnswer":  answer,
		"passingCount": passing,
		"blockedCount": blocked,
	}
}

// ── small arg helpers ────────────────────────────────────────────────────

func strArg(args map[string]interface{}, k string) string {
	v, _ := args[k].(string)
	return v
}

func intArg(args map[string]interface{}, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
