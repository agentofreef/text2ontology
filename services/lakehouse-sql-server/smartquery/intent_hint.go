package smartquery

import (
	"fmt"
	"strings"
)

// IntentHint carries the spec-level fields of a Metric Intent that the agent
// has selected for the current question. It is **purely additive** input —
// the lakehouse-sql-server consumes it via applyIntentHint to mutate the
// spec deterministically, without ever querying lakehouse_metric_intent
// itself. This keeps the SQL service stateless w.r.t. intent metadata.
//
// Origin: agent-server resolves the intent (priority + keyword gate) and
// fills this struct. Result post-processing (pivot / share suffix /
// response template) stays in agent-server and reads the same intent
// directly from its own DB; it does not flow through this hint.
type IntentHint struct {
	IntentID string `json:"intentId,omitempty"`
	Name     string `json:"name,omitempty"`

	// CanonicalMetric overrides spec.Metric when set and structurally
	// different (case + whitespace insensitive). Blocks the "AI substituted
	// count(...) for sum(...)" failure mode end-to-end.
	CanonicalMetric string `json:"canonicalMetric,omitempty"`

	// CanonicalFilters are filters the intent always applies (e.g. a
	// "ConfirmedSales" intent might always WHERE Status='Confirmed').
	// Merged into spec.Filters; existing filters on the same prop win.
	CanonicalFilters []FilterItem `json:"canonicalFilters,omitempty"`

	// AutoGroupBy lists the property tokens the Intent insists must appear
	// in spec.GroupBy.
	AutoGroupBy []string `json:"autoGroupBy,omitempty"`

	// ReplaceGroupBy=true wipes spec.GroupBy and uses AutoGroupBy as the
	// authoritative list. Used when Intent owns the entire grouping (e.g.
	// "X11 占比" with a Distribution intent that fixes axis to PRODUCT).
	ReplaceGroupBy bool `json:"replaceGroupBy,omitempty"`

	// AddShareColumnSafe controls whether INJECT applies under
	// AddShareColumn=true. Default false preserves user's chosen share
	// granularity (don't widen "per-X share" to "per-(X×Y) share").
	AddShareColumnSafe bool `json:"addShareColumnSafe,omitempty"`

	// DefaultOrderByLabel — SELECT alias to ORDER BY when spec.OrderBy is
	// empty. Typically the metric's output label (e.g. "Total_LineTotal").
	// Empty = no default; LLM must supply orderBy if it wants ranking.
	DefaultOrderByLabel string `json:"defaultOrderByLabel,omitempty"`

	// DefaultOrderByDir — "ASC" or "DESC" (or empty). Used together with
	// DefaultOrderByLabel.
	DefaultOrderByDir string `json:"defaultOrderByDir,omitempty"`

	// DefaultLimit — injected into spec.Limit when the LLM didn't supply
	// one. Ranking intents typically set 10; total intents set 1.
	// Zero = no default.
	DefaultLimit int `json:"defaultLimit,omitempty"`
}

// IntentParameter mirrors one entry of lakehouse_metric_intent.parameters JSONB.
// It declares a typed, user-level knob the strict-mode dispatch (P7) consumes
// via BindIntentParams. Same shape as agent-server/smartquery.IntentParameter
// — both packages keep parallel implementations because the Phase 1 D4d
// service-deps gate forbids cross-service Go imports. The HTTP DTOs marshal
// identically across the wire.
//
// Type semantics (v1):
//
//	"int":             numeric value; binder writes to spec.Limit
//	"string":          if Property set, treats as filter value on Property using Op
//	                   (default "="); reserved for future custom routing otherwise
//	"property_filter": LLM provides a value; binder appends spec.Filters with
//	                   {Prop=Property, Op=Op (default "="), Value, FuzzyMatch}
type IntentParameter struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Property    string      `json:"property,omitempty"`
	Op          string      `json:"op,omitempty"`
	Default     interface{} `json:"default,omitempty"`
	Optional    bool        `json:"optional,omitempty"`
	Description string      `json:"description,omitempty"`
	FuzzyMatch  bool        `json:"fuzzyMatch,omitempty"`

	// AllowedValues is a runtime-only view (not persisted in JSON). For
	// Type=="enum_ref" the caller (handler) populates this slice from the
	// project's lakehouse_keyword table — see spec
	// .omc/specs/bounded-value-ref-contract.md §3.2. Semantics:
	//   - nil   → caller could not / chose not to populate (e.g. dry-run
	//             save validation without DB context); binding falls back
	//             to Type="string" pass-through. Preserves backward compat
	//             so structural validators still pass.
	//   - non-nil (possibly empty) → caller asserts this is the full
	//             candidate set; binding fails loudly with PARAM_VALUE_UNKNOWN
	//             when the LLM-supplied value is not in the set.
	// json:"-" so it doesn't accidentally serialize back into Intent records.
	AllowedValues []string `json:"-"`
}

// applyIntentHint mutates spec in place per the rules originally implemented
// in agent-server's enforceIntentAutoGroupBy. Pure function: no DB, no I/O.
//
// Returns a list of human-readable change records (suitable for warnings /
// debug info). Empty slice when hint is nil or has nothing to apply.
//
// Rules (in order):
//
//  1. Metric override — if hint.CanonicalMetric is set and structurally
//     differs from spec.Metric, replace.
//  2. ReplaceGroupBy=true — wipe spec.GroupBy, refill from hint.AutoGroupBy,
//     and strip equality filters on those props (they would collapse the
//     dim universe to one value).
//  3. ReplaceGroupBy=false — for each AutoGroupBy entry not already present
//     in spec.GroupBy:
//     * if a matching equality filter exists → MOVE (strip filter, inject
//     into groupBy as the dim enumeration).
//     * otherwise → INJECT, unless spec.AddShareColumn=true and
//     hint.AddShareColumnSafe=false (skip to preserve share granularity).
func applyIntentHint(spec *QuerySpec, hint *IntentHint) []string {
	if hint == nil {
		return nil
	}
	var changes []string

	// 1. Metric override.
	if hint.CanonicalMetric != "" && normMetric(spec.Metric) != normMetric(hint.CanonicalMetric) {
		changes = append(changes, fmt.Sprintf(
			"intent metric override: %q → %q (Intent=%s)",
			spec.Metric, hint.CanonicalMetric, hint.Name))
		spec.Metric = hint.CanonicalMetric
	}

	// 2. Canonical filters merge — Intent declares always-applied filters
	// (e.g. Status='Confirmed' for a ConfirmedSales intent). Existing
	// LLM-supplied filter on the same prop (canonical key) wins to allow
	// user override of the default.
	if len(hint.CanonicalFilters) > 0 {
		existing := map[string]bool{}
		for _, f := range spec.Filters {
			existing[CanonicalPropKey(f.Prop)] = true
		}
		var added []string
		for _, cf := range hint.CanonicalFilters {
			k := CanonicalPropKey(cf.Prop)
			if existing[k] {
				continue
			}
			spec.Filters = append(spec.Filters, cf)
			existing[k] = true
			added = append(added, fmt.Sprintf("%s %s %q", cf.Prop, cf.Op, cf.Value))
		}
		if len(added) > 0 {
			changes = append(changes, fmt.Sprintf("intent canonical filters merged: %v (Intent=%s)", added, hint.Name))
		}
	}

	// 6. Default ORDER BY — applies whenever spec.OrderBy is empty AND
	// the intent declares a default. This is the deterministic encoding of
	// "ranking intents must default to metric DESC" so the LLM doesn't
	// have to remember it.
	if len(spec.OrderBy) == 0 && hint.DefaultOrderByLabel != "" {
		dir := hint.DefaultOrderByDir
		if dir != "ASC" && dir != "DESC" {
			dir = "DESC"
		}
		spec.OrderBy = append(spec.OrderBy, OrderByItem{Prop: hint.DefaultOrderByLabel, Dir: dir})
		changes = append(changes, fmt.Sprintf("intent default order by: %s %s (Intent=%s)", hint.DefaultOrderByLabel, dir, hint.Name))
	}

	// 7. Default LIMIT — applies when LLM didn't supply one. Zero / negative
	// values are treated as "unset" (NormalizeQuerySpec sets 1000 as a
	// sanity ceiling — we override that ceiling only when intent declares
	// a tighter bound).
	if hint.DefaultLimit > 0 && (spec.Limit <= 0 || spec.Limit >= 1000) {
		spec.Limit = hint.DefaultLimit
		changes = append(changes, fmt.Sprintf("intent default limit: %d (Intent=%s)", hint.DefaultLimit, hint.Name))
	}

	if len(hint.AutoGroupBy) == 0 {
		return changes
	}

	// 2. ReplaceGroupBy.
	if hint.ReplaceGroupBy {
		gbSet := map[string]bool{}
		for _, g := range hint.AutoGroupBy {
			gbSet[CanonicalPropKey(g)] = true
		}
		if dropped := stripEqualityFiltersOn(spec, gbSet); len(dropped) > 0 {
			changes = append(changes, fmt.Sprintf(
				"intent [replace_group_by] strip eq filters: %v (Intent=%s)",
				dropped, hint.Name))
		}
		before := append([]string(nil), spec.GroupBy...)
		spec.GroupBy = nil
		for _, g := range hint.AutoGroupBy {
			spec.AppendGroupBy(g)
		}
		changes = append(changes, fmt.Sprintf(
			"intent [replace_group_by] groupBy %v → %v (Intent=%s)",
			before, spec.GroupBy, hint.Name))
		return changes
	}

	// 3. Per-prop MOVE / INJECT / SKIP.
	filterEqProps := map[string]bool{}
	for _, f := range spec.Filters {
		if isEqualityOp(f.Op) {
			filterEqProps[CanonicalPropKey(f.Prop)] = true
		}
	}
	stripProps := map[string]bool{}
	var injectedMove, injectedFresh []string
	for _, g := range hint.AutoGroupBy {
		if spec.HasGroupBy(g) {
			continue
		}
		k := CanonicalPropKey(g)
		switch {
		case filterEqProps[k]:
			stripProps[k] = true
			injectedMove = append(injectedMove, g)
		case !spec.AddShareColumn || hint.AddShareColumnSafe:
			injectedFresh = append(injectedFresh, g)
		default:
			// AddShareColumn=true AND prop is a net-new dim → preserve user's
			// share granularity; don't inject silently.
		}
	}
	injected := append(append([]string(nil), injectedMove...), injectedFresh...)
	if len(injected) > 0 {
		injectedKeys := map[string]bool{}
		for _, g := range injected {
			injectedKeys[CanonicalPropKey(g)] = true
		}
		rest := spec.GroupBy[:0]
		for _, g := range spec.GroupBy {
			if !injectedKeys[CanonicalPropKey(g)] {
				rest = append(rest, g)
			}
		}
		spec.GroupBy = append(injected, rest...)
		changes = append(changes, fmt.Sprintf(
			"intent inject auto_group_by: move=%v fresh=%v (Intent=%s)",
			injectedMove, injectedFresh, hint.Name))
	}
	if len(stripProps) > 0 {
		if dropped := stripEqualityFiltersOn(spec, stripProps); len(dropped) > 0 {
			changes = append(changes, fmt.Sprintf(
				"intent strip eq filters on auto_group_by props: %v (Intent=%s)",
				dropped, hint.Name))
		}
	}
	return changes
}

// stripEqualityFiltersOn removes equality filters whose canonical prop key
// matches keys, in place. Returns the human-readable list of dropped
// filters for change tracking.
func stripEqualityFiltersOn(spec *QuerySpec, keys map[string]bool) []string {
	if len(spec.Filters) == 0 || len(keys) == 0 {
		return nil
	}
	kept := spec.Filters[:0]
	var dropped []string
	for _, f := range spec.Filters {
		if keys[CanonicalPropKey(f.Prop)] && isEqualityOp(f.Op) {
			dropped = append(dropped, fmt.Sprintf("%s %s %q", f.Prop, f.Op, f.Value))
			continue
		}
		kept = append(kept, f)
	}
	spec.Filters = kept
	return dropped
}

// isEqualityOp returns true for operators that collapse a dimension to a
// single value (or small set, in IN's case) — those are the ones safe to
// MOVE into groupBy under intent enforcement.
func isEqualityOp(op string) bool {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "", "=", "==", "ilike", "in":
		return true
	}
	return false
}

// normMetric lowercases and strips whitespace so cosmetic differences
// don't trigger spurious "metric override" actions.
func normMetric(s string) string {
	s = strings.ToLower(s)
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
}
