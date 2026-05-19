package mission

import (
	"fmt"
	"strings"
)

// Coverage check (spec §3.5, M2): given the question's decomposition and
// the candidate Intents' declared parameters, decide which required
// dimensions can be filtered at all. A dimension covered by no Intent
// is a no_param capability gap — the structural "unanswerable" case the
// agent must declare honestly instead of grinding or excusing.
//
// The declarative half of the coverage check lives here and is fully
// mechanical. The semantic half (does a covering parameter support the
// *shape* the question needs — e.g. a year range vs a single month) is
// the LLM's job; this file does not attempt it.

// IntentParam is the minimal declared-parameter view the coverage check
// needs — a projection of lakehouse_metric_intent.parameters.
type IntentParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`     // string | int | enum_ref | property_filter | ...
	Property string `json:"property"` // the ontology property the param filters on
}

// IntentSpec is a candidate Intent as the coverage check sees it.
type IntentSpec struct {
	Name   string        `json:"name"`
	Params []IntentParam `json:"params"`
}

// Covers reports whether this parameter declaratively covers the given
// decomposition dimension. The match is deterministic: the dimension
// name must equal (case-insensitively) the parameter's name or the
// property it filters on. The LLM is instructed to name decomposition
// dimensions using the candidate Intents' real property names, which
// keeps this check mechanical (spec §3.5 no_param gate).
func (p IntentParam) Covers(dim DecompItem) bool {
	n := strings.TrimSpace(dim.Name)
	if n == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(p.Name), n) ||
		strings.EqualFold(strings.TrimSpace(p.Property), n)
}

// CoveringIntents returns the names of candidate Intents that have at
// least one parameter covering the dimension. An empty result means the
// dimension is a no_param capability gap.
func CoveringIntents(dim DecompItem, intents []IntentSpec) []string {
	var out []string
	for _, in := range intents {
		for _, p := range in.Params {
			if p.Covers(dim) {
				out = append(out, in.Name)
				break
			}
		}
	}
	return out
}

// UncoveredDimensions returns the decomposition dimensions that NO
// candidate Intent can filter on. Only kind "dimension" and "filter"
// items are checked — a metric is the query target, not a filter.
func UncoveredDimensions(decomposition []DecompItem, intents []IntentSpec) []DecompItem {
	var out []DecompItem
	for _, d := range decomposition {
		if d.Kind != "dimension" && d.Kind != "filter" {
			continue
		}
		if len(CoveringIntents(d, intents)) == 0 {
			out = append(out, d)
		}
	}
	return out
}

// VerifyNoParamGap is the verification gate for a no_param capability
// gap (spec §3.5). It re-checks the LLM's claim mechanically: the
// claimed missing dimension must be covered by NO parameter of the
// named candidate Intents. Returns nil if the gap claim holds, or an
// error naming the parameter that contradicts it — which the agent
// loop surfaces so the LLM retries instead of falsely giving up.
func VerifyNoParamGap(missingDim string, candidates []IntentSpec) error {
	if strings.TrimSpace(missingDim) == "" {
		return fmt.Errorf("capability gap claim rejected: empty missing dimension")
	}
	dim := DecompItem{Name: missingDim, Kind: "filter"}
	for _, in := range candidates {
		for _, p := range in.Params {
			if p.Covers(dim) {
				return fmt.Errorf(
					"capability gap claim rejected: intent %q has parameter %q covering dimension %q",
					in.Name, p.Name, missingDim)
			}
		}
	}
	return nil
}
