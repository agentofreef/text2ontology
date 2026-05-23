package handler

// tool_declare_gap.go — MissionAct M2: declare_capability_gap tool.
//
// When the lakehouse agent finds that a question requires a filter/dimension
// that NO recalled Intent can serve, it calls this tool instead of grinding
// or emitting "技术限制". The tool runs the VerifyNoParamGap gate, writes
// persistence records best-effort, and assembles a machine-templated final
// answer that cannot be softened by an LLM.
//
// Gated behind USE_MISSION_ACT: when the flag is off the tool is absent from
// v2Tools and absent from lakehouseToolNames, so it can never be dispatched.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lakehouse2ontology/mission"
	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// capGapResult is the structured return from runDeclareCapabilityGap.
// terminal==true signals the caller (dispatchTool) that this turn should
// end with finalAnswer as the streamed response, not continue the LLM loop.
type capGapResult struct {
	terminal    bool
	finalAnswer string
	toolResult  map[string]any // returned to the LLM on gate rejection (non-terminal)
}

// runDeclareCapabilityGap implements the declare_capability_gap tool.
// It is only reachable when missionActEnabled == true (caller guarantees this).
func runDeclareCapabilityGap(
	ctx context.Context,
	db *sql.DB,
	sm *shadowMission,
	recalledIntents []recall.MetricIntent,
	args map[string]any,
) capGapResult {
	// ── Parse arguments ──────────────────────────────────────────────────────
	missingDim, _ := args["missing_dimension"].(string)
	gapKind, _ := args["gap_kind"].(string)
	suggestedFix, _ := args["suggested_fix"].(string)
	closestReachable, _ := args["closest_reachable"].(string)

	if missingDim == "" {
		return capGapResult{toolResult: map[string]any{
			"error": "declare_capability_gap: missing_dimension is required",
		}}
	}
	if gapKind == "" {
		gapKind = "no_param"
	}

	// Parse candidates_checked array from args.
	var candidates []mission.CandidateCheck
	if raw, ok := args["candidates_checked"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				cc := mission.CandidateCheck{
					IntentName:      strVal(m, "intent_name"),
					ParamsSummary:   strVal(m, "params_summary"),
					WhyInsufficient: strVal(m, "why_insufficient"),
				}
				candidates = append(candidates, cc)
			}
		}
	}

	// ── Verification gate (no_param only — spec §3.5) ────────────────────────
	if gapKind == "no_param" {
		specs := buildIntentSpecs(recalledIntents)
		if err := mission.VerifyNoParamGap(missingDim, specs); err != nil {
			// Gate rejected: return an error tool result so the agent retries.
			return capGapResult{toolResult: map[string]any{
				"error":            fmt.Sprintf("capability gap claim rejected: %v", err),
				"gate_rejected":    true,
				"missing_dimension": missingDim,
				"hint": "该维度已有参数覆盖，请选择真正覆盖该维度的指标并直接查询，不要重新声明能力缺口。",
			}}
		}
	}

	// ── Gap accepted — build BlockedReason ───────────────────────────────────
	kind := mission.GapNoParam
	switch gapKind {
	case "shape_unsupported":
		kind = mission.GapShapeUnsupported
	case "no_data":
		kind = mission.GapNoData
	}
	reason := mission.BlockedReason{
		Kind:              kind,
		MissingDimension:  missingDim,
		CandidatesChecked: candidates,
		SuggestedFix:      suggestedFix,
	}

	// ── Persist shadow mission + gap log (best-effort) ───────────────────────
	if sm != nil && sm.m != nil {
		sm.blockRoot(ctx, reason)
		writeCapabilityGapLog(ctx, db, sm.m.MissionID, sm.m.ProjectID, reason)
	}

	// ── Assemble machine-templated final answer ───────────────────────────────
	answer := buildGapAnswer(missingDim, candidates, suggestedFix, closestReachable)

	return capGapResult{terminal: true, finalAnswer: answer}
}

// buildGapAnswer assembles the machine-templated response for a located
// capability gap. This is NOT LLM-generated — the text is built in Go so it
// cannot be softened or omitted.
func buildGapAnswer(missingDim string, candidates []mission.CandidateCheck, suggestedFix, closestReachable string) string {
	var sb strings.Builder
	sb.WriteString("〔无法精确回答 —— 能力缺口〕\n\n")
	sb.WriteString(fmt.Sprintf("你的问题需要按【%s】筛选，但没有任何已授权的指标提供这个维度。\n", missingDim))

	if len(candidates) > 0 {
		sb.WriteString(fmt.Sprintf("\n已检查 %d 个相关指标：\n", len(candidates)))
		for _, c := range candidates {
			line := fmt.Sprintf("- %s", c.IntentName)
			if c.ParamsSummary != "" {
				line += fmt.Sprintf("：%s", c.ParamsSummary)
			}
			if c.WhyInsufficient != "" {
				line += fmt.Sprintf(" —— %s", c.WhyInsufficient)
			}
			sb.WriteString(line + "\n")
		}
	}

	if suggestedFix != "" {
		sb.WriteString(fmt.Sprintf("\n这不是临时故障。修复方向：%s", suggestedFix))
	}
	if closestReachable != "" {
		sb.WriteString(fmt.Sprintf("\n当前能给你的最接近结果：%s", closestReachable))
	}
	return sb.String()
}

// buildIntentSpecs converts the recalled MetricIntents (recall.MetricIntent)
// to the mission.IntentSpec slice that VerifyNoParamGap expects.
func buildIntentSpecs(intents []recall.MetricIntent) []mission.IntentSpec {
	specs := make([]mission.IntentSpec, 0, len(intents))
	for _, mi := range intents {
		spec := mission.IntentSpec{Name: mi.Name}
		for _, p := range mi.Parameters {
			spec.Params = append(spec.Params, mission.IntentParam{
				Name:        p.Name,
				Type:        p.Type,
				Property:    p.Property,
				Op:          p.Op,
				Description: p.Description,
			})
		}
		specs = append(specs, spec)
	}
	return specs
}

// strVal is a nil-safe string extractor from a map[string]any.
func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// declareCapabilityGapToolDescription is the LLM-facing description shown
// in the tool list. Only injected into v2Tools when missionActEnabled.
const declareCapabilityGapToolDescription = `声明能力缺口（locate capability gap）。
当你确认问题所需的筛选维度没有任何已授权 Intent 能覆盖时，调用此工具——
不要给出"技术限制 / 系统暂时无法处理"等模糊回答，也不要无限制地重试。
收到 gate_rejected 错误时，说明该维度其实有 Intent 覆盖，请直接用那个 Intent 查询，不要重新声明缺口。`

// capabilityGapPromptSection returns the system-prompt section that instructs
// the LLM how and when to use declare_capability_gap. Only spliced in when
// USE_MISSION_ACT is on; returns "" otherwise so the prompt is byte-identical.
func capabilityGapPromptSection() string {
	if !missionActEnabled {
		return ""
	}
	return `
## 能力缺口声明（USE_MISSION_ACT 已启用）

当你发现问题需要的筛选维度（如 employee_id、region、channel）**任何** 已召回的指标都没有对应参数时：
1. 调用 ` + "`declare_capability_gap`" + `，列出你检查过的每个指标和它为何不足。
2. **绝对不要**回答"技术限制 / 系统暂时无法处理 / 平台功能有限"等模糊措辞。
3. 若收到 ` + "`gate_rejected`" + ` 错误，说明该维度确实有指标能覆盖——改用那个指标直接查询，不要重新声明缺口。
4. 只有 ` + "`🎯 查询指标`" + ` 候选集**完全为空**时才允许告知用户"超出已配置范围"；非空则必须先尽力查询或声明定位缺口。`
}

// declareCapabilityGapToolDef returns the llmclient.ToolDef for this tool.
// Imported inline so the v2Tools block stays readable.
func declareCapabilityGapToolDef() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"missing_dimension", "gap_kind", "candidates_checked", "suggested_fix"},
		"properties": map[string]any{
			"missing_dimension": map[string]any{
				"type":        "string",
				"description": "问题需要但所有指标都没有覆盖的维度/筛选条件，例如 \"employee_id\" 或 \"region\"",
			},
			"gap_kind": map[string]any{
				"type":        "string",
				"enum":        []string{"no_param", "shape_unsupported", "no_data"},
				"description": "no_param=任何指标都没有该维度的参数；shape_unsupported=参数存在但形状不符（如只支持单值不支持范围）；no_data=参数存在但底层数据缺失",
			},
			"candidates_checked": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"intent_name", "why_insufficient"},
					"properties": map[string]any{
						"intent_name":      map[string]any{"type": "string", "description": "指标名称"},
						"params_summary":   map[string]any{"type": "string", "description": "该指标的参数列表摘要（一行）"},
						"why_insufficient": map[string]any{"type": "string", "description": "为什么这个指标不能覆盖该维度"},
					},
				},
				"description": "已检查过的所有相关指标，需逐一说明为何不覆盖",
			},
			"suggested_fix": map[string]any{
				"type":        "string",
				"description": "修复方向，例如：需要在本体授权层为 store_revenue 指标添加 employee_id 参数",
			},
			"closest_reachable": map[string]any{
				"type":        "string",
				"description": "（可选）当前能给出的最接近结果，如果有的话",
			},
		},
	}
}
