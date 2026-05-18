package analysis

import (
	"fmt"
	"strings"
	"text/template"
)

// RenderSynthesis produces the final analysis answer from a settled ledger.
//
// The answer has three machine-stitched segments (spec §3.4):
//
//  1. Template render — synthesis.template evaluated against feature data.
//     Variable schema (§9.4):
//       {{ .features.<id>.summary }}  feature result digest (single line)
//       {{ .features.<id>.rows }}     row count
//       {{ .features.<id>.value }}    scalar result, if any
//       {{ .features.<id>.error }}    blocked reason, if any
//  2. Caveats — appended VERBATIM from the pattern card (§9.5). The LLM never
//     gets a chance to rephrase or omit them.
//  3. Blocked declarations — one honest line per blocked feature, so a missing
//     dimension is stated outright rather than silently dropped (§3.3 / §7.7).
//
// RenderSynthesis does not require AllSettled() — it renders whatever state the
// ledger is in, treating not_started/active features the same as blocked for
// the purpose of the template (empty values). Callers normally call it only
// after AllSettled() is true.
func RenderSynthesis(l *FeatureLedger) (string, error) {
	if l == nil || l.pattern == nil {
		return "", fmt.Errorf("analysis: RenderSynthesis on nil ledger")
	}
	snap := l.Snapshot()

	// ── segment 1: template render ───────────────────────────────────────
	features := make(map[string]map[string]any, len(snap))
	for _, r := range snap {
		view := map[string]any{
			"summary": "",
			"rows":    0,
			"value":   "",
			"error":   "",
		}
		if r.Evidence != nil {
			view["error"] = r.Evidence.Error
			// Only a PASSING feature contributes data to the template body.
			// A blocked / not_started / active feature exposes nothing but its
			// error: the LLM may attach a partial or wrong-kind summary/value
			// to a blocked verify_feature call (observed: a row count handed to
			// a date-window feature), and that must NOT leak into the rendered
			// answer. A blocked feature is accounted for solely by the blocked-
			// declaration section below.
			if r.State == StatePassing {
				view["summary"] = r.Evidence.Summary
				view["rows"] = r.Evidence.RowCount
				view["value"] = r.Evidence.Value
			}
		}
		features[r.ID] = view
	}
	data := map[string]any{"features": features}

	tpl, err := template.New("synthesis").
		Option("missingkey=zero").
		Parse(l.pattern.Synthesis.Template)
	if err != nil {
		return "", fmt.Errorf("analysis: parse synthesis template: %w", err)
	}
	var buf strings.Builder
	if err := tpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("analysis: execute synthesis template: %w", err)
	}

	out := strings.TrimRight(buf.String(), "\n")

	// ── segment 2: caveats, verbatim ─────────────────────────────────────
	if caveats := l.pattern.Synthesis.Caveats; len(caveats) > 0 {
		out += "\n\n注意事项："
		for _, c := range caveats {
			out += "\n- " + c
		}
	}

	// ── segment 3: blocked-feature honest declarations ───────────────────
	var blocked []FeatureRuntime
	for _, r := range snap {
		if r.State == StateBlocked {
			blocked = append(blocked, r)
		}
	}
	if len(blocked) > 0 {
		out += "\n\n以下维度本次未取到："
		for _, r := range blocked {
			line := "\n- " + r.Behavior
			if r.Evidence != nil && r.Evidence.Error != "" {
				line += "（" + r.Evidence.Error + "）"
			}
			out += line
		}
	}

	return out, nil
}
