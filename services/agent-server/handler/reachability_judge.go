package handler

// reachability_judge.go — MissionAct 任务可达器 (reachability judge).
//
// Runs BEFORE the ReAct loop on every lakehouse question when
// USE_MISSION_ACT is on. Two steps:
//
//  1. Decompose: one lean LLM call breaks the question into required
//     elements (metrics, dimensions, filters). Fail-open: any LLM or
//     parse error is logged and we fall through to the normal loop.
//
//  2. Judge: pure deterministic call to mission.Judge — no LLM. A
//     single uncovered dimension makes the whole question infeasible.
//
// Gate:
//   - Feasible        → return nil (caller continues into ReAct loop).
//   - Infeasible      → return non-empty finalAnswer string (caller
//     streams it and returns; no ReAct loop runs).
//   - Judge skipped   → return nil (fail-open).
//
// All side-effects (recordReachability, mission status) are
// best-effort: they never fail the turn.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/mission"
	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// runReachabilityJudge is the single hook call made by
// handleAgentStreamLakehouse. It returns the machine-templated infeasibility
// answer (non-empty) when the gate fires, or "" to proceed normally.
//
// db and intents are passed in rather than derived here so the function does
// not depend on any handler-level state beyond what the call site already has.
func runReachabilityJudge(
	ctx context.Context,
	db *sql.DB,
	sm *shadowMission,
	question string,
	intents []recall.MetricIntent,
) string {
	if !missionActEnabled {
		return ""
	}

	// ── Step 1: Decompose (LLM call, fail-open) ──────────────────────────────
	decomp, err := decomposeQuestion(ctx, db, question, intents)
	if err != nil {
		log.Printf("MISSION-ACT: reachability decompose skipped (fail-open): %v", err)
		return ""
	}
	if len(decomp) == 0 {
		// Empty decomposition — nothing to gate on; proceed normally.
		return ""
	}

	// ── Step 2: Judge (pure deterministic) ───────────────────────────────────
	specs := buildIntentSpecs(intents)
	verdict := mission.Judge(decomp, specs)

	// ── Persist reachability verdict (best-effort) ────────────────────────────
	sm.recordReachability(ctx, verdict)

	// ── Gate ─────────────────────────────────────────────────────────────────
	if verdict.Feasible {
		return ""
	}

	// Infeasible — build machine-templated answer (NOT LLM-generated).
	return buildInfeasibilityAnswer(verdict)
}

// decomposeQuestion calls the agent LLM with a lean prompt to break the
// question into required elements. Returns nil,err on any failure (fail-open).
//
// Prompt contract: returns ONLY a JSON array:
//
//	[{"kind":"metric|dimension|filter","name":"..."}]
//
// For dimension/filter items, name MUST use the candidate Intent parameter
// property names — that is what the deterministic coverage match keys on.
func decomposeQuestion(
	ctx context.Context,
	db *sql.DB,
	question string,
	intents []recall.MetricIntent,
) ([]mission.DecompItem, error) {
	baseURL, apiKey, modelName, _, _, vendor := llmclient.GetConfigForRole(db, "agent")
	if baseURL == "" {
		return nil, fmt.Errorf("no agent LLM config available")
	}

	systemPrompt := `你是一个分析助手。将用户问题拆解为它所需的数据要素。

只输出一个 JSON 数组，不要包裹 markdown 代码块，不要输出任何其他文字：
[{"kind":"metric|dimension|filter","name":"...","shape":"...","why":"..."}]

规则：
- kind="metric" 表示用户想查询的指标（如销售额、数量）。
- kind="dimension" 表示用户想按其分组的维度（如按地区、按月份）。
- kind="filter" 表示用户想按其筛选的条件（如某员工、某城市）。
- 对于 dimension 和 filter，name 必须使用下方"可用参数"列表中已有的参数 property 名称；如果不存在匹配的参数，使用问题中的原始词。
- shape 用一两个词描述该要素的形态：metric 用"标量/求和/计数"等；dimension 用"分组"；filter 用"等值/范围/区间"等。
- why 用一句话说明这个问题为什么需要这个要素。
- 如果问题只是通用查询（无特定筛选），只返回 metric 项。
- 数组元素不超过 8 个。`

	userPrompt := buildDecomposeUserPrompt(question, intents)

	llmMessages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	content, _, err := llmclient.DoChatWithUsage(baseURL, apiKey, map[string]interface{}{
		"model":       modelName,
		"messages":    llmMessages,
		"max_tokens":  400,
		"temperature": 0.1,
		"_vendor":     vendor,
	})
	if err != nil {
		return nil, fmt.Errorf("decompose LLM call failed: %w", err)
	}

	return parseDecompJSON(llmclient.StripThinkTags(content))
}

// buildDecomposeUserPrompt constructs the user-turn prompt for the decompose
// call. It lists every Intent with its parameter property names so the LLM can
// use the exact property tokens in dimension/filter names.
func buildDecomposeUserPrompt(question string, intents []recall.MetricIntent) string {
	var sb strings.Builder
	sb.WriteString("## 用户问题\n")
	sb.WriteString(question)
	sb.WriteString("\n\n## 可用参数（Intent 名称 → 参数 property 列表）\n")
	if len(intents) == 0 {
		sb.WriteString("（无已召回 Intent）\n")
	} else {
		for _, mi := range intents {
			sb.WriteString(fmt.Sprintf("- %s：", mi.Name))
			props := make([]string, 0, len(mi.Parameters))
			for _, p := range mi.Parameters {
				if p.Property != "" {
					props = append(props, p.Property)
				} else if p.Name != "" {
					props = append(props, p.Name)
				}
			}
			if len(props) == 0 {
				sb.WriteString("（无参数）")
			} else {
				sb.WriteString(strings.Join(props, "、"))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n请输出 JSON 数组。")
	return sb.String()
}

// parseDecompJSON parses the LLM's JSON array reply into []mission.DecompItem.
// Tolerates leading/trailing whitespace and code-block wrappers.
func parseDecompJSON(raw string) ([]mission.DecompItem, error) {
	cleaned := llmclient.ExtractJSON(raw)
	if cleaned == "" {
		// Try raw string directly in case ExtractJSON doesn't find braces.
		cleaned = strings.TrimSpace(raw)
	}
	// ExtractJSON may return an object {}; we need an array [...].
	// If it starts with '{', wrap attempt would fail — just try parsing as-is.
	var items []struct {
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		Shape string `json:"shape"`
		Why   string `json:"why"`
	}
	if err := json.Unmarshal([]byte(cleaned), &items); err != nil {
		return nil, fmt.Errorf("decompose JSON parse failed (%q): %w", truncateStr(cleaned, 120), err)
	}
	out := make([]mission.DecompItem, 0, len(items))
	for i, it := range items {
		kind := strings.ToLower(strings.TrimSpace(it.Kind))
		switch kind {
		case "metric", "dimension", "filter":
		default:
			kind = "metric" // safe default
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		out = append(out, mission.DecompItem{
			ID:          fmt.Sprintf("d%d", i+1),
			Kind:        kind,
			Name:        name,
			Shape:       strings.TrimSpace(it.Shape),
			WhyRequired: strings.TrimSpace(it.Why),
		})
	}
	return out, nil
}

// buildInfeasibilityAnswer assembles the machine-templated stop answer.
// NOT LLM-generated — text is built in Go so it cannot be softened.
func buildInfeasibilityAnswer(verdict mission.ReachabilityVerdict) string {
	var sb strings.Builder
	sb.WriteString("〔可达性判定：这个问题现在无法整体回答〕\n\n")
	sb.WriteString(verdict.Reason)

	if subset := verdict.AnswerableSubset(); len(subset) > 0 {
		sb.WriteString("\n\n我能覆盖的维度：")
		sb.WriteString(strings.Join(subset, "、"))
		sb.WriteString(" —— 但你的问题需要整体回答，目前做不到。")
	}
	return sb.String()
}

// truncateStr truncates s to max bytes for log messages.
func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
