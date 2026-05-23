package mission

import (
	"fmt"
	"strings"
)

// 任务可达器 — the task-reachability judge.
//
// This is NOT about tasks, plans, or execution. Its single job: given a
// user question (decomposed into what it requires) and the authorized
// Intents, decide whether the system CAN answer the question from
// authorized data — and say why / why not.
//
// The verdict is BINARY and WHOLE-QUESTION: a single uncovered
// dimension makes the entire question infeasible. The judge does not
// answer a feasible subset — when infeasible the system stops and
// explains; it may state which parts it could have answered, but it
// does not execute a partial answer.
//
// It runs first, before any querying, and gates everything after:
// feasible → proceed; infeasible → stop with the reason.
//
// The decomposition (what the question requires) is the LLM's job. Given
// the decomposition plus the authorized IntentSpecs, the verdict here is
// fully deterministic.

// RequirementCoverage is one decomposed requirement and whether the
// authorized Intents cover it. It carries the full decomposition detail
// (shape, why) so the 任务可达器 panel can render the breakdown, not
// just a verdict.
type RequirementCoverage struct {
	Dimension   string   `json:"dimension"`
	Kind        string   `json:"kind"`            // metric | dimension | filter
	Shape       string   `json:"shape,omitempty"` // scalar | group-by | range | ...
	Why         string   `json:"why,omitempty"`   // why the question needs this element
	Covered     bool     `json:"covered"`
	CoveredBy   []string `json:"covered_by,omitempty"`   // Intent names that cover it (the "why feasible")
	MissingNote string   `json:"missing_note,omitempty"` // why it is uncovered (the "why not")
}

// ReachabilityVerdict is the judge's output — the primary, user-facing
// deliverable. Feasible gates execution; Requirements + Reason explain.
type ReachabilityVerdict struct {
	Feasible     bool                  `json:"feasible"`
	Requirements []RequirementCoverage `json:"requirements"`
	Reason       string                `json:"reason"`
	// Kind classifies an infeasible verdict for answer rendering: "gap"
	// (cannot be answered — a required field/value/metric is absent) or
	// "clarify" (a filter value is ambiguous across several fields — ask the
	// user which). Empty when feasible.
	Kind string `json:"kind,omitempty"`
}

// Judge produces the reachability verdict. Pure + deterministic.
//
// A metric requirement is the query target, not a filter dimension, so
// it never gates feasibility — only dimension/filter requirements do.
// Feasible == every dimension/filter requirement is covered by at least
// one authorized Intent.
func Judge(decomposition []DecompItem, intents []IntentSpec) ReachabilityVerdict {
	v := ReachabilityVerdict{Feasible: true}
	for _, d := range decomposition {
		rc := RequirementCoverage{Dimension: d.Name, Kind: d.Kind, Shape: d.Shape, Why: d.WhyRequired}
		if d.Kind != "dimension" && d.Kind != "filter" {
			rc.Covered = true // metric / other — not a coverage gate
			v.Requirements = append(v.Requirements, rc)
			continue
		}
		if covering := CoveringIntents(d, intents); len(covering) > 0 {
			rc.Covered = true
			rc.CoveredBy = covering
		} else {
			rc.MissingNote = fmt.Sprintf("没有任何已授权指标提供「%s」这个维度", d.Name)
			v.Feasible = false
		}
		v.Requirements = append(v.Requirements, rc)
	}
	v.Reason = renderReason(v)
	return v
}

// renderReason builds the human-readable verdict explanation. Feasible:
// names what covers the question. Infeasible: names the uncovered
// dimensions — the located "why not".
func renderReason(v ReachabilityVerdict) string {
	var uncovered []string
	for _, r := range v.Requirements {
		if (r.Kind == "dimension" || r.Kind == "filter") && !r.Covered {
			uncovered = append(uncovered, r.Dimension)
		}
	}
	if v.Feasible {
		var dims []string
		for _, r := range v.Requirements {
			if r.Kind == "dimension" || r.Kind == "filter" {
				dims = append(dims, r.Dimension)
			}
		}
		if len(dims) == 0 {
			return "可行:问题不涉及需要授权的筛选维度。"
		}
		return fmt.Sprintf("可行:问题所需的维度(%s)均被已授权指标覆盖。",
			strings.Join(dims, "、"))
	}
	return fmt.Sprintf(
		"不可行:问题需要按「%s」筛选,但没有任何已授权指标提供这个(些)维度——"+
			"因此这个问题的整体回答现在做不到。",
		strings.Join(uncovered, "、"))
}

// AnswerableSubset returns the dimensions that ARE covered — used to
// tell the user "I could answer these parts" even when the whole
// question is infeasible. It never licenses executing a partial
// answer; it is explanatory only.
func (v ReachabilityVerdict) AnswerableSubset() []string {
	var out []string
	for _, r := range v.Requirements {
		if (r.Kind == "dimension" || r.Kind == "filter") && r.Covered {
			out = append(out, r.Dimension)
		}
	}
	return out
}
