package recall

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnalysisPattern is the decoded skill_config of an analysis-pattern OK card.
//
// Loaded from OkEntry.SkillConfig via ParseAnalysisPattern when
// OkEntry.IsAnalysisPattern() is true. Drives plan-mode in agent-server.
//
// See schema.sql (ont_knowledge comments) and
// .omc/specs/plan-from-ontology-knowledge.md §3.1 for full semantics.
type AnalysisPattern struct {
	Trigger   AnalysisTrigger   `json:"trigger"`
	Features  []AnalysisFeature `json:"features"`
	Synthesis AnalysisSynthesis `json:"synthesis"`
}

// AnalysisTrigger describes when the LLM should pick this pattern.
type AnalysisTrigger struct {
	Keywords        []string `json:"keywords"`
	StructuralHints []string `json:"structural_hints,omitempty"`
}

// AnalysisFeature is the L08 three-tuple: behavior + verification + tool hints.
// State/evidence/retry are runtime fields managed by the FeatureLedger, not
// stored on the card.
type AnalysisFeature struct {
	ID           string              `json:"id"`
	Behavior     string              `json:"behavior"`
	Verification string              `json:"verification"`
	ToolHints    []AnalysisToolHint  `json:"tool_hints,omitempty"`
}

// AnalysisToolHint is a soft suggestion ("try this tool first"). LLM is free
// to pick any tool — these are not binding (spec §9.3 + design principle 5).
type AnalysisToolHint struct {
	Tool   string `json:"tool"`             // "smartquery" | "compose_query" | "query_dag" | "lookup"
	Intent string `json:"intent,omitempty"` // for tool=smartquery
	Ref    string `json:"ref,omitempty"`    // for tool=query_dag
}

// AnalysisSynthesis is the final-answer recipe.
//
// Template is a Go text/template (§9.4); Caveats are appended verbatim by
// agent-server after the rendered template — not let the LLM rephrase (§9.5).
type AnalysisSynthesis struct {
	Template string   `json:"template"`
	Caveats  []string `json:"caveats,omitempty"`
}

// formatAnalysisSkills renders all analysis_pattern OK cards in entries as a
// prominent "📊 分析 Skill" markdown block. Cards whose skill_config fails to
// parse are skipped silently (defense-in-depth — §9.2 says invalid cards
// should already be filtered before recall returns them).
//
// Writes nothing if there are no valid analysis-pattern cards.
func formatAnalysisSkills(sb *strings.Builder, entries []OkEntry) {
	type skill struct {
		entry   OkEntry
		pattern *AnalysisPattern
	}
	var skills []skill
	for _, e := range entries {
		if !e.IsAnalysisPattern() {
			continue
		}
		p, err := ParseAnalysisPattern(e.SkillConfig)
		if err != nil {
			continue
		}
		skills = append(skills, skill{entry: e, pattern: p})
	}
	if len(skills) == 0 {
		return
	}

	sb.WriteString("### 📊 分析 Skill（可调用的分析模式）\n\n")
	sb.WriteString("以下分析模式卡片命中了用户问题。每张卡片是一个**可调用的 skill**：" +
		"若问题确实需要多维度展开（影响 / 为什么 / 综合评估），用 `start_analysis_plan` " +
		"传入对应 `patternId` 进入分析计划；若只是快查一个数，忽略本块、直接用 smartquery。\n\n")

	for _, s := range skills {
		sb.WriteString(fmt.Sprintf("- **%s**  `patternId=%s`\n", s.entry.Title, s.entry.ID))
		if s.entry.Summary != "" {
			sb.WriteString("  - " + s.entry.Summary + "\n")
		}
		if len(s.pattern.Trigger.StructuralHints) > 0 {
			sb.WriteString("  - 典型问法：" +
				strings.Join(s.pattern.Trigger.StructuralHints, " / ") + "\n")
		}
		sb.WriteString(fmt.Sprintf("  - 分析维度（%d 个特征）：\n", len(s.pattern.Features)))
		for _, f := range s.pattern.Features {
			sb.WriteString(fmt.Sprintf("    - `%s` — %s\n", f.ID, f.Behavior))
		}
	}
	sb.WriteString("\n")
}

// ParseAnalysisPattern decodes the OkEntry.SkillConfig JSON into a typed
// AnalysisPattern. Returns a typed error when the JSON is malformed or
// structurally invalid (missing features, empty IDs, etc.).
func ParseAnalysisPattern(raw json.RawMessage) (*AnalysisPattern, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("analysis_pattern: empty skill_config")
	}
	var p AnalysisPattern
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("analysis_pattern: decode skill_config: %w", err)
	}
	if len(p.Features) == 0 {
		return nil, fmt.Errorf("analysis_pattern: features list is empty")
	}
	seen := map[string]bool{}
	for i, f := range p.Features {
		if f.ID == "" {
			return nil, fmt.Errorf("analysis_pattern: feature[%d] missing id", i)
		}
		if f.Behavior == "" {
			return nil, fmt.Errorf("analysis_pattern: feature[%s] missing behavior", f.ID)
		}
		if f.Verification == "" {
			return nil, fmt.Errorf("analysis_pattern: feature[%s] missing verification", f.ID)
		}
		if seen[f.ID] {
			return nil, fmt.Errorf("analysis_pattern: duplicate feature id %q", f.ID)
		}
		seen[f.ID] = true
	}
	if p.Synthesis.Template == "" {
		return nil, fmt.Errorf("analysis_pattern: synthesis.template missing")
	}
	return &p, nil
}
