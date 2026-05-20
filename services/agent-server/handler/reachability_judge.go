package handler

// reachability_judge.go — MissionAct 任务可达器 (reachability judge).
//
// Runs BEFORE the ReAct loop on every lakehouse question when
// USE_MISSION_ACT is on. ONE LLM call does both decomposition AND
// shape-aware coverage in one shot:
//
//   - Decompose the question into required elements (metric / dimension /
//     filter) plus the *shape* each one needs ("年范围", "单月前缀",
//     "等值", "枚举集合", …).
//   - For every dimension/filter, judge — based on the candidate Intent
//     parameter table (property + op + type + description) — whether some
//     Intent's parameter actually supports that shape, and name which.
//
// Pure name-match used to be enough but produced false positives: a
// "year range" filter happily matched a "starts-with YYYY-MM" parameter
// even though answering the question would need 13 separate calls. The
// LLM gets the full parameter schema so it can refuse those.
//
// Deterministic guard rails in buildVerdictFromLLMHints:
//   - covered_by names are sanitised against the real Intent set, so a
//     hallucinated name cannot prop up a feasible verdict.
//   - if the LLM says covered but cites no real intent, fall back to the
//     declarative name match; if even that fails, force uncovered.
//
// Gate:
//   - Feasible        → return "" (caller continues into ReAct loop).
//   - Infeasible      → return non-empty finalAnswer string (caller
//     streams it and returns; no ReAct loop runs).
//   - Judge skipped   → return "" (fail-open).
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
	"github.com/lib/pq"
)

// shapeCap is one row of the data-driven shape vocabulary (table
// lakehouse_shape_capability). It is loaded fresh on every gate
// invocation. The Name is treated as opaque by every Go file: nothing
// elsewhere in the codebase compares against a specific shape string.
// The gate only does Name-equality between what the LLM emits as the
// required shape and what an Intent parameter declares.
type shapeCap struct {
	Name        string
	Description string
	Examples    []string
	// Satisfies is the subsumption list: a parameter declaring this shape
	// can also serve a requirement the LLM classified as any name here
	// (plus this shape itself). Strictly broader→narrower; see
	// docs/schema/schema.sql > lakehouse_shape_capability.
	Satisfies []string
}

// loadShapeVocab reads the registered shape vocabulary from the database.
// Any error or an unpopulated table returns nil, in which case the gate
// degrades to its prior LLM-only judgement (no deterministic shape
// match). Safe default: a missing / empty vocab MUST NEVER make the gate
// stricter than the legacy path.
func loadShapeVocab(ctx context.Context, db *sql.DB) []shapeCap {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT name, COALESCE(description, ''), COALESCE(examples, '{}'::text[]),
		       COALESCE(satisfies, '{}'::text[])
		FROM lakehouse_shape_capability
		ORDER BY name`)
	if err != nil {
		log.Printf("MISSION-ACT: shape vocab load skipped (fail-open): %v", err)
		return nil
	}
	defer rows.Close()
	var out []shapeCap
	for rows.Next() {
		var sc shapeCap
		var ex, sat pq.StringArray
		if err := rows.Scan(&sc.Name, &sc.Description, &ex, &sat); err != nil {
			log.Printf("MISSION-ACT: shape vocab scan: %v", err)
			continue
		}
		sc.Examples = []string(ex)
		sc.Satisfies = []string(sat)
		out = append(out, sc)
	}
	return out
}

// llmRequirementHint is what the LLM returns per decomposed item — both the
// decomposition itself and its shape-aware coverage verdict against the
// candidate Intent parameter table. The verdict half (Covered / CoveredBy /
// UncoveredReason) is the LLM's job because shape matching ("year range" vs
// "single-month YYYY-MM prefix") is semantic, not declarative.
type llmRequirementHint struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Shape           string   `json:"shape"`
	Why             string   `json:"why"`
	Covered         *bool    `json:"covered"`
	CoveredBy       []string `json:"covered_by"`
	UncoveredReason string   `json:"uncovered_reason"`
}

// decomposeResult is the full decompose-LLM output: the distinct
// sub-questions the user asked (so a multi-part question becomes a task
// completion list) plus the per-element coverage hints.
type decomposeResult struct {
	SubQuestions []string
	Requirements []llmRequirementHint
}

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

	// Load the data-driven shape vocabulary once per gate invocation. Empty
	// or error → vocab is nil → all downstream code degrades to legacy
	// behaviour (LLM-only shape judgement, no deterministic match check).
	vocab := loadShapeVocab(ctx, db)

	// ── LLM call: decomposition + shape-aware coverage in one pass ───────────
	res, err := decomposeQuestion(ctx, db, question, intents, vocab)
	if err != nil {
		log.Printf("MISSION-ACT: reachability decompose skipped (fail-open): %v", err)
		return ""
	}
	if len(res.Requirements) == 0 {
		return ""
	}

	// ── Build verdict from LLM hints; deterministic guard rails ──────────────
	verdict := buildVerdictFromLLMHints(res.Requirements, intents, vocab)

	// ── Persist (best-effort) ────────────────────────────────────────────────
	sm.recordReachability(ctx, verdict)

	// ── Seed the task completion list for multi-part questions ───────────────
	// Only when the question is feasible AND has 2+ distinct sub-questions —
	// a single atomic question needs no checklist. reconcileMissionTasks (run
	// in the turn-end defer) flips each task passing/blocked against the
	// final answer.
	if verdict.Feasible && len(res.SubQuestions) >= 2 {
		sm.seedTasksFromSubQuestions(ctx, res.SubQuestions)
	}

	if verdict.Feasible {
		return ""
	}
	return buildInfeasibilityAnswer(verdict)
}

// buildVerdictFromLLMHints turns the LLM's per-item judgement into a
// ReachabilityVerdict. The LLM owns the shape-aware coverage decision; this
// function adds two deterministic guards:
//
//   - covered_by names are sanitised against the real Intent name set, so a
//     hallucinated intent name cannot prop up a feasible verdict.
//   - if after sanitisation a dimension/filter has covered=true but no real
//     covered_by, it falls back to declarative name-match against the
//     intent parameter table; if that also fails the item is forced uncovered.
func buildVerdictFromLLMHints(hints []llmRequirementHint, intents []recall.MetricIntent, vocab []shapeCap) mission.ReachabilityVerdict {
	realIntentNames := map[string]bool{}
	for _, mi := range intents {
		realIntentNames[mi.Name] = true
	}
	vocabKnown := map[string]bool{}
	satisfies := map[string][]string{}
	for _, sc := range vocab {
		vocabKnown[sc.Name] = true
		satisfies[sc.Name] = sc.Satisfies
	}
	specs := buildIntentSpecs(intents) // for fallback declarative match

	v := mission.ReachabilityVerdict{Feasible: true}
	for _, h := range hints {
		rc := mission.RequirementCoverage{
			Dimension: h.Name,
			Kind:      h.Kind,
			Shape:     h.Shape,
			Why:       h.Why,
		}

		// metric / unknown kinds never gate feasibility — they're query targets.
		if h.Kind != "dimension" && h.Kind != "filter" {
			rc.Covered = true
			v.Requirements = append(v.Requirements, rc)
			continue
		}

		// Sanitise covered_by against real intent names.
		var realCoveredBy []string
		for _, n := range h.CoveredBy {
			if realIntentNames[n] {
				realCoveredBy = append(realCoveredBy, n)
			}
		}

		llmSaysCovered := h.Covered != nil && *h.Covered
		switch {
		case llmSaysCovered && len(realCoveredBy) > 0:
			// Hole-2 guard: when shape is a registered vocab name, the cited
			// Intents must ALSO declare a parameter with the same
			// shapeCapability. Without this, "real intent name + wrong shape"
			// slips through — which is the exact bug class this gate exists
			// to catch. Skipped (legacy behaviour) when shape is empty / not
			// in the vocab, so an unpopulated vocab never causes regression.
			if vocabKnown[h.Shape] && !anyCitedIntentServesShape(realCoveredBy, h.Shape, intents, satisfies) {
				rc.MissingNote = fmt.Sprintf(
					"已授权 Intent (%s) 真实，但没有任何参数声明可服务「%s」形态的能力",
					strings.Join(realCoveredBy, "、"), h.Shape)
				v.Feasible = false
				break
			}
			rc.Covered = true
			rc.CoveredBy = realCoveredBy
		case llmSaysCovered && len(realCoveredBy) == 0:
			// LLM said covered but cited no real intent — fall back to declarative
			// name match. If even that fails, force uncovered.
			fallback := mission.CoveringIntents(mission.DecompItem{Name: h.Name, Kind: h.Kind}, specs)
			if len(fallback) > 0 {
				rc.Covered = true
				rc.CoveredBy = fallback
			} else {
				rc.MissingNote = "LLM 标可达但未指明真实 Intent；按参数表也无匹配"
				v.Feasible = false
			}
		default: // LLM says uncovered (or didn't decide).
			if h.UncoveredReason != "" {
				rc.MissingNote = h.UncoveredReason
			} else {
				rc.MissingNote = fmt.Sprintf("没有已授权 Intent 以「%s」形态覆盖「%s」", h.Shape, h.Name)
			}
			v.Feasible = false
		}
		v.Requirements = append(v.Requirements, rc)
	}
	v.Reason = buildVerdictReason(v)
	return v
}

// anyCitedIntentServesShape reports whether at least one of the cited
// Intents has a parameter that can serve the required shape — either by
// declaring it exactly, or by declaring a broader shape that subsumes it
// (declaredShape ∈ satisfies-closure). Used by the hole-2 guard in
// buildVerdictFromLLMHints.
//
// The subsumption tolerance is what prevents false refusals when the LLM
// classifies a requirement with a narrower-but-compatible label than the
// parameter's declaration (e.g. param declares a range shape, LLM labels
// the requirement as the point shape the range subsumes). Empty required
// shape returns false — by then the gate has already decided to skip the
// deterministic check for that requirement.
func anyCitedIntentServesShape(cited []string, required string, intents []recall.MetricIntent, satisfies map[string][]string) bool {
	if required == "" {
		return false
	}
	citedSet := map[string]bool{}
	for _, n := range cited {
		citedSet[n] = true
	}
	for _, mi := range intents {
		if !citedSet[mi.Name] {
			continue
		}
		for _, p := range mi.Parameters {
			declared := p.ShapeCapability
			if declared == "" {
				continue
			}
			if declared == required {
				return true
			}
			for _, sat := range satisfies[declared] {
				if sat == required {
					return true
				}
			}
		}
	}
	return false
}

// buildVerdictReason composes the human-readable verdict explanation from the
// finalised requirements list. Feasible: name the dimensions covered.
// Infeasible: name the uncovered ones AND surface the LLM's shape reason
// (since the failure is usually about shape mismatch, not absence).
func buildVerdictReason(v mission.ReachabilityVerdict) string {
	type un struct {
		name, shape, why string
	}
	var uncovered []un
	var covered []string
	for _, r := range v.Requirements {
		if r.Kind != "dimension" && r.Kind != "filter" {
			continue
		}
		if r.Covered {
			covered = append(covered, r.Dimension)
		} else {
			uncovered = append(uncovered, un{name: r.Dimension, shape: r.Shape, why: r.MissingNote})
		}
	}
	if v.Feasible {
		if len(covered) == 0 {
			return "可行：问题不涉及需要授权的筛选维度。"
		}
		return fmt.Sprintf("可行：所需维度（%s）均被已授权 Intent 以匹配形态覆盖。", strings.Join(collectNames(covered), "、"))
	}
	var parts []string
	for _, u := range uncovered {
		s := fmt.Sprintf("「%s」", u.name)
		if u.shape != "" {
			s += fmt.Sprintf("（需要 %s 形态）", u.shape)
		}
		if u.why != "" {
			s += "：" + u.why
		}
		parts = append(parts, s)
	}
	return "不可行：" + strings.Join(parts, "；") + "。"
}

// collectNames dedupes a string slice while preserving order.
func collectNames(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// decomposeQuestion calls the agent LLM once to do both jobs: identify the
// distinct sub-questions and break the question into shape-aware coverage
// hints. Returns a zero-value result + err on any failure (fail-open).
//
// Prompt contract: returns ONLY a JSON object:
//
//	{"sub_questions":["..."],"requirements":[{"kind":"...","name":"...",...}]}
//
// For dimension/filter requirements, name MUST use the candidate Intent
// parameter property names — that is what the coverage match keys on.
func decomposeQuestion(
	ctx context.Context,
	db *sql.DB,
	question string,
	intents []recall.MetricIntent,
	vocab []shapeCap,
) (decomposeResult, error) {
	var zero decomposeResult
	baseURL, apiKey, modelName, _, _, vendor := llmclient.GetConfigForRole(db, "agent")
	if baseURL == "" {
		return zero, fmt.Errorf("no agent LLM config available")
	}

	systemPrompt := `你是一个数据可达性裁判。给定用户问题与可用的 Intent 参数表，做三件事：

第一件：把用户这一句里包含的「独立子问题」分出来。一句话里可能并列了多个问题（例如"上海的总营收是多少？外卖占比是多少？"是两个子问题）。每个子问题用一句自然语言描述。如果只有一个问题，就只返回一个。
第二件：把问题拆解为它所需的数据要素（metric / dimension / filter）。
第三件：对每个 dimension / filter 要素，**严格基于参数表的 op / type / 描述** 判断有哪个 Intent 真正支持这个要素所要求的"形态"（范围 / 单月前缀 / 等值 ...）。这件事是关键，仅靠 property 名字相同就判可达是错的。

只输出一个 JSON 对象，不要包裹 markdown 代码块，不要输出任何其他文字：
{"sub_questions":["子问题1","子问题2"],"requirements":[{"kind":"metric|dimension|filter","name":"...","shape":"...","why":"...","covered":true|false,"covered_by":["intent_a"],"uncovered_reason":"..."}]}

sub_questions 规则：
- 每个元素是一句完整的自然语言子问题，尽量保留用户原话里的关键词。
- 并列的问题要分开；递进/修饰同一目标的不要硬拆。
- 最多 6 个。

requirements 字段规则：
- kind="metric" 表示用户想查询的指标（如销售额、数量）。
- kind="dimension" 表示用户想按其分组的维度。
- kind="filter" 表示用户想按其筛选的条件。
- name：对于 dimension/filter，必须使用下方参数表中真实出现的参数 property 名称；如果整张参数表里没有匹配的 property，用问题中的原始词，并把这一项标为 covered=false。
- shape：一两个词描述该要素的形态。
  - metric 用"标量/求和/计数"等。
  - dimension 用"分组"。
  - filter 用具体形态："等值"/"单值"/"前缀匹配"/"区间"/"年范围"/"月范围"/"日期范围"/"枚举集合" 等。**形态必须精确**，不要笼统写"范围"。
- why：一句话说明问题为什么需要这个要素。
- covered（仅 dimension/filter 需要；metric 可省略，等同 true）：
  - true 仅当存在某个 Intent 的参数同时满足：(a) property 与 name 一致；(b) op/type/描述 表明它能支撑这个 shape。
  - 例如 shape="年范围" 但参数 op="starts with" 且 描述="YYYY-MM" → 单月前缀，无法直接覆盖年范围 → covered=false。
  - 例如 shape="等值" 且参数 op="=" → 覆盖。
  - 例如 shape="枚举集合" 且参数 type="enum_ref" 且 op="=" → 单次只能选一个枚举值 → 仍是 covered=false（除非问题就是单值）。
- covered_by：covered=true 时，列出真正满足形态的 Intent 名（必须出现在参数表里）。
- uncovered_reason：covered=false 时，一句话说明为什么形态对不上（例如"参数仅支持 YYYY-MM 单月前缀，不直接支持 2024-2025 年范围"）。
- 如果问题只是通用查询（无特定筛选），requirements 只返回 metric 项。
- requirements 元素不超过 8 个。`

	userPrompt := buildDecomposeUserPrompt(question, intents, vocab)

	llmMessages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	content, _, err := llmclient.DoChatWithUsage(baseURL, apiKey, map[string]interface{}{
		"model":       modelName,
		"messages":    llmMessages,
		"max_tokens":  1600,
		"temperature": 0.1,
		"_vendor":     vendor,
	})
	if err != nil {
		return zero, fmt.Errorf("decompose LLM call failed: %w", err)
	}

	return parseDecompJSON(llmclient.StripThinkTags(content))
}

// buildDecomposeUserPrompt constructs the user-turn prompt. For every Intent
// it lists each parameter's full schema (property, op, type, description) so
// the LLM can shape-match — name match alone is not enough to judge whether
// a question's "year range" filter can really be served by a "starts-with
// YYYY-MM" parameter.
func buildDecomposeUserPrompt(question string, intents []recall.MetricIntent, vocab []shapeCap) string {
	var sb strings.Builder
	sb.WriteString("## 用户问题\n")
	sb.WriteString(question)
	sb.WriteString("\n\n## 可用 Intent 参数表（按形态判断可达性时必须以此为准）\n")
	if len(intents) == 0 {
		sb.WriteString("（无已召回 Intent）\n")
	} else {
		for _, mi := range intents {
			sb.WriteString(fmt.Sprintf("### Intent: %s\n", mi.Name))
			if len(mi.Parameters) == 0 {
				sb.WriteString("（无参数）\n")
				continue
			}
			for _, p := range mi.Parameters {
				prop := p.Property
				if prop == "" {
					prop = p.Name
				}
				sb.WriteString(fmt.Sprintf("  - property=%q, name=%q, type=%q, op=%q",
					prop, p.Name, p.Type, p.Op))
				if p.Description != "" {
					sb.WriteString(fmt.Sprintf(", desc=%q", p.Description))
				}
				if p.ShapeCapability != "" {
					sb.WriteString(fmt.Sprintf(", shape_capability=%q", p.ShapeCapability))
				}
				if len(p.AllowedValues) > 0 {
					// Cap allowed-values length so very large enums don't blow the prompt.
					vs := p.AllowedValues
					if len(vs) > 8 {
						vs = append(append([]string{}, vs[:8]...), "…")
					}
					sb.WriteString(fmt.Sprintf(", allowed=[%s]", strings.Join(vs, ",")))
				}
				sb.WriteString("\n")
			}
		}
	}
	if len(vocab) > 0 {
		sb.WriteString("\n## 形态词表（封闭词表 — shape 字段必须从下面挑一个 name；都不匹配就留空字符串）\n")
		for _, sc := range vocab {
			sb.WriteString(fmt.Sprintf("  - %s — %s", sc.Name, sc.Description))
			if len(sc.Examples) > 0 {
				sb.WriteString(fmt.Sprintf("（例：%s）", strings.Join(sc.Examples, "; ")))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n仅当某 Intent 的某个参数声明了 shape_capability 等于该要素 shape 时，才算形态对得上；否则即便 property 名字一致，也要把 covered 置为 false。\n")
	}
	sb.WriteString("\n请输出 JSON 对象。")
	return sb.String()
}

// reqItemJSON is one requirement entry as the LLM emits it. `covered` is
// *bool so we can distinguish "LLM didn't decide" from "explicit false".
type reqItemJSON struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Shape           string   `json:"shape"`
	Why             string   `json:"why"`
	Covered         *bool    `json:"covered"`
	CoveredBy       []string `json:"covered_by"`
	UncoveredReason string   `json:"uncovered_reason"`
}

// parseDecompJSON parses the LLM reply into a decomposeResult. The contract is
// an object {sub_questions, requirements}; for resilience it also accepts a
// bare requirements array (older shape) and treats sub_questions as empty.
func parseDecompJSON(raw string) (decomposeResult, error) {
	cleaned := llmclient.ExtractJSON(raw)
	if cleaned == "" {
		cleaned = strings.TrimSpace(raw)
	}

	// Preferred shape: object.
	var obj struct {
		SubQuestions []string      `json:"sub_questions"`
		Requirements []reqItemJSON `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(cleaned), &obj); err == nil && (len(obj.Requirements) > 0 || len(obj.SubQuestions) > 0) {
		return decomposeResult{
			SubQuestions: cleanSubQuestions(obj.SubQuestions),
			Requirements: normalizeReqItems(obj.Requirements),
		}, nil
	}

	// Fallback shape: bare array of requirements.
	var arr []reqItemJSON
	if err := json.Unmarshal([]byte(cleaned), &arr); err != nil {
		return decomposeResult{}, fmt.Errorf("decompose JSON parse failed (%q): %w", truncateStr(cleaned, 200), err)
	}
	return decomposeResult{Requirements: normalizeReqItems(arr)}, nil
}

// normalizeReqItems maps raw LLM items to llmRequirementHint, defaulting an
// unknown kind to "metric" and dropping nameless entries.
func normalizeReqItems(items []reqItemJSON) []llmRequirementHint {
	out := make([]llmRequirementHint, 0, len(items))
	for _, it := range items {
		kind := strings.ToLower(strings.TrimSpace(it.Kind))
		switch kind {
		case "metric", "dimension", "filter":
		default:
			kind = "metric"
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		out = append(out, llmRequirementHint{
			Kind:            kind,
			Name:            name,
			Shape:           strings.TrimSpace(it.Shape),
			Why:             strings.TrimSpace(it.Why),
			Covered:         it.Covered,
			CoveredBy:       it.CoveredBy,
			UncoveredReason: strings.TrimSpace(it.UncoveredReason),
		})
	}
	return out
}

// cleanSubQuestions trims, drops empties, and dedupes the sub-question list.
func cleanSubQuestions(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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

// reconcileMissionTasks is the turn-end hook. Given the mission's
// sub-question task list and the final assistant answer, it asks the LLM
// once which sub-questions the answer actually addressed, then records the
// per-task passing/blocked verdict. Fail-open and best-effort: any LLM /
// parse error is logged and leaves the tasks as-is. No-op when the shadow
// path is inert or there are no sub-question tasks.
func reconcileMissionTasks(ctx context.Context, db *sql.DB, sm *shadowMission, finalAnswer string) {
	if !missionActEnabled || sm == nil || sm.m == nil || len(sm.m.Tasks) == 0 {
		return
	}
	if strings.TrimSpace(finalAnswer) == "" {
		return
	}

	baseURL, apiKey, modelName, _, _, vendor := llmclient.GetConfigForRole(db, "agent")
	if baseURL == "" {
		return
	}

	var sb strings.Builder
	sb.WriteString("## 子问题清单（id → 子问题）\n")
	for _, t := range sm.m.Tasks {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.ID, t.Behavior))
	}
	sb.WriteString("\n## 系统给出的最终回答\n")
	sb.WriteString(finalAnswer)
	sb.WriteString("\n\n请判断每个子问题是否在最终回答里得到了实际回答。只输出 JSON 对象，键是子问题 id，值是 true/false：\n{\"q1\":true,\"q2\":false}")

	systemPrompt := `你是一个核对助手。给定一组子问题与系统的最终回答，判断每个子问题是否被实际回答了（有具体数值/结论才算 true；只是提到、回避、或说做不到算 false）。只输出 JSON 对象，不要包裹 markdown，不要多余文字。`

	content, _, err := llmclient.DoChatWithUsage(baseURL, apiKey, map[string]interface{}{
		"model":       modelName,
		"messages":    []map[string]interface{}{{"role": "system", "content": systemPrompt}, {"role": "user", "content": sb.String()}},
		"max_tokens":  300,
		"temperature": 0.1,
		"_vendor":     vendor,
	})
	if err != nil {
		log.Printf("MISSION-ACT: reconcile skipped (fail-open): %v", err)
		return
	}

	cleaned := llmclient.ExtractJSON(llmclient.StripThinkTags(content))
	if cleaned == "" {
		cleaned = strings.TrimSpace(content)
	}
	var results map[string]bool
	if err := json.Unmarshal([]byte(cleaned), &results); err != nil {
		log.Printf("MISSION-ACT: reconcile parse failed (%q): %v", truncateStr(cleaned, 200), err)
		return
	}
	sm.applyTaskReconciliation(ctx, results)
}

// truncateStr truncates s to max bytes for log messages.
func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
