package smartquery

import "strings"

// StripObjectPrefix is the exported version of stripObjectPrefix.
func StripObjectPrefix(token string) (objectName, propName string) {
	return stripObjectPrefix(token)
}

// StripDateGranularity is the exported version of stripDateGranularity.
func StripDateGranularity(name string) (cleanName, formatStr, outputLabel string) {
	return stripDateGranularity(name)
}

// StripAggWrapper is the exported version of stripAggWrapper.
func StripAggWrapper(s string) string {
	return stripAggWrapper(s)
}

// CanonicalPropKey returns the representation-insensitive identity of a
// property token. Two tokens that refer to the same (Od-scoped, granularity-
// aware) column must produce the same key.
//
// Rules (in order):
//   1. Strip leading `Od.` prefix (if present).
//   2. Strip trailing date-granularity suffix (`(月)`, `:month`, `_year`, …).
//   3. Trim whitespace + lowercase.
//
// Use this as the single dedup key whenever multiple passes push tokens into
// `spec.GroupBy` / `spec.Filters[].Prop` (e.g. `promoteFilterPropsToGroupBy`,
// `enforceIntentAutoGroupBy`). A pass that appends via this key is guaranteed
// idempotent against itself, and commutes with any other pass that also uses
// this key — which is the "anti-seesaw" invariant.
//
// Note: this intentionally DROPS the Od prefix. For groupBy / filter
// deduplication that is fine because resolve.go later binds the bare prop
// name to the correct Od via the 4-tier cascade (local-exact / global-exact
// / fuzzy). Two different Ods sharing a prop name (e.g.
// `PRODUCT.CATALOG_NAME` vs `ORDER.CATALOG_NAME`) are extremely rare in
// practice and the spec layer treats them as the same dim — resolve picks
// the owning Od by membership in `spec.Objects`.
func CanonicalPropKey(token string) string {
	if token == "" {
		return ""
	}
	// StripObjectPrefix returns ("", token) when no dot — take propName.
	_, prop := StripObjectPrefix(token)
	if prop == "" {
		prop = token
	}
	prop, _, _ = StripDateGranularity(prop)
	return strings.ToLower(strings.TrimSpace(prop))
}

// stripObjectPrefix strips an "Object." prefix from a token.
// Returns (objectName, propName). If no "." is present, objectName is "".
func stripObjectPrefix(token string) (objectName, propName string) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", token
}

// stripDateGranularity returns (cleanName, formatStr, outputLabel).
func stripDateGranularity(name string) (cleanName, formatStr, outputLabel string) {
	lower := strings.ToLower(name)
	type suffixEntry struct {
		suffix string
		fmt    string
		label  string
	}
	entries := []suffixEntry{
		{"(月)", "YYYY-MM", "Month"},
		{"(年)", "YYYY", "Year"},
		{"(日)", "YYYY-MM-DD", "Day"},
		{"(季)", "YYYY-Q", "Quarter"},
		{"(周)", "YYYY-WW", "Week"},
		{":month", "YYYY-MM", "Month"},
		{":year", "YYYY", "Year"},
		{":day", "YYYY-MM-DD", "Day"},
		{":quarter", "YYYY-Q", "Quarter"},
		{":week", "YYYY-WW", "Week"},
		{":hour", "YYYY-MM-DD HH", "Hour"},
		// Underscore suffixes — lower priority, for AI-natural format.
		{"_month", "YYYY-MM", "Month"},
		{"_year", "YYYY", "Year"},
		{"_day", "YYYY-MM-DD", "Day"},
		{"_quarter", "YYYY-Q", "Quarter"},
		{"_week", "YYYY-WW", "Week"},
	}
	for _, e := range entries {
		if strings.HasSuffix(lower, e.suffix) {
			clean := name[:len(name)-len(e.suffix)]
			label := clean + "_" + e.label
			return clean, e.fmt, label
		}
	}
	return name, "", name
}

// stripAggWrapper removes leading aggregation function wrappers from a metric/column name.
// e.g. "sum(Order.Quantity)" → "Order.Quantity".
func stripAggWrapper(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, fn := range []string{"sum(", "count(", "avg(", "average(", "max(", "min(", "distinctcount("} {
		if strings.HasPrefix(lower, fn) && strings.HasSuffix(s, ")") {
			return strings.TrimSpace(s[len(fn) : len(s)-1])
		}
	}
	return s
}
