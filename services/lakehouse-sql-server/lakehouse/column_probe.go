package lakehouse

// Column-probe support for canonical_query ↔ property name alignment.
//
// Problem background
// ──────────────────
// `ont_object_type.canonical_query` is the semantic SQL for a lakehouse Od.
// The SQL builder wraps it as `FROM (<canonical_query>) AS "<od>"` and then
// emits column references like `"<od>"."<property_name>"`. PostgreSQL treats
// double-quoted identifiers as case- and whitespace-sensitive, so if the
// canonical_query emits a column aliased `"Product Name"` (space) while the
// property in ont_property is stored as `Product_Name` (underscore), the
// generated reference `"<od>"."Product_Name"` raises `column does not exist`.
//
// Solution
// ────────
// At ResolveQuery time we probe canonical_query with `SELECT * ... LIMIT 0`
// to learn the real output column names, then rewrite canonical_query so its
// outermost SELECT aliases each real column to the matching property name.
// Matching is done on a normalized key that folds case/whitespace/underscore
// differences so `Product Name ↔ Product_Name ↔ PRODUCT_NAME` all align.
//
// The rewrite is a no-op when all property names already match real columns
// byte-for-byte, so queries that were already working get identical SQL.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// probeCache memoizes canonical_query → real output column names, keyed by
// sha256(canonicalQuery). Process-scoped; survives many requests, invalidated
// on restart. Safe for concurrent read+write from request goroutines.
//
// Invalidation is implicit: if the user re-solidifies canonical_query in the
// lakehouse-objects UI, the new string hashes differently and we re-probe.
var probeCache sync.Map // map[string][]string

// probeCanonicalColumns runs `SELECT * FROM (<canonicalQuery>) __probe LIMIT 0`
// against the Postgres connection and returns the output column names in the
// order canonical_query emits them. Column names are returned with their
// original case/whitespace preserved (pq hands them through verbatim).
//
// LIMIT 0 ensures Postgres plans but never executes row fetch, so cost is
// effectively a single round-trip.
//
// Returns (nil, nil) when canonicalQuery is empty, which callers treat as
// "skip alignment — leave canonical_query unchanged".
func probeCanonicalColumns(db *sql.DB, canonicalQuery string) ([]string, error) {
	q := cleanCanonicalForProbe(canonicalQuery)
	if q == "" {
		return nil, nil
	}
	key := sha256Hex(q)
	if v, ok := probeCache.Load(key); ok {
		return v.([]string), nil
	}
	rows, err := db.Query(fmt.Sprintf("SELECT * FROM (%s) __probe LIMIT 0", q))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	// Copy to own the slice for the cache's lifetime.
	out := make([]string, len(cols))
	copy(out, cols)
	probeCache.Store(key, out)
	return out, nil
}

// cleanCanonicalForProbe trims leading/trailing whitespace and strips a
// terminating semicolon so the string is safe to wrap inside `SELECT * FROM (…)`.
// A trailing `;` inside parentheses is a Postgres syntax error.
func cleanCanonicalForProbe(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	return s
}

// normalizeColKey folds case, whitespace, and underscores into a single
// equivalence class. Matches the frontend's identifier normalization (`\s+`→`_`
// + uppercase) and additionally treats existing underscores as whitespace-
// equivalent. Result: "Product Name", "Product_Name", "PRODUCT  NAME",
// "product__name" all collapse to "PRODUCT_NAME".
//
// Non-Latin characters (Chinese, Japanese, etc.) pass through uppercase
// unchanged and retain their identity.
func normalizeColKey(s string) string {
	s = strings.TrimSpace(s)
	var sb strings.Builder
	prevSep := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '_' {
			if !prevSep {
				sb.WriteRune('_')
				prevSep = true
			}
			continue
		}
		sb.WriteRune(r)
		prevSep = false
	}
	return strings.ToUpper(strings.Trim(sb.String(), "_"))
}

// alignCanonicalQuery rewrites canonical_query so its outermost SELECT
// exposes one column per property, aliased to property.Name. For each
// property with a normalized match in actualCols it emits either `"realCol"`
// (when byte-identical to property.Name) or `"realCol" AS "propName"`.
// Properties without any match are returned as `unmatched`.
//
// When every property name matches the corresponding real column byte-for-byte
// (no remap needed) the original canonical_query is returned unchanged to
// keep the generated physical SQL identical for already-working queries.
//
// Matching of normalized keys prefers the first `actualCols` entry that
// collapses to a given key, so canonical_queries that accidentally emit two
// columns normalizing to the same key behave deterministically.
func alignCanonicalQuery(canonical string, propNames []string, actualCols []string) (rewritten string, unmatched []string) {
	trimmed := cleanCanonicalForProbe(canonical)
	if trimmed == "" || len(actualCols) == 0 || len(propNames) == 0 {
		return canonical, nil
	}

	realByKey := make(map[string]string, len(actualCols))
	for _, c := range actualCols {
		k := normalizeColKey(c)
		if _, exists := realByKey[k]; !exists {
			realByKey[k] = c
		}
	}

	type aliasPair struct{ real, prop string }
	var pairs []aliasPair
	seen := make(map[string]bool, len(propNames))
	needsRewrite := false

	for _, p := range propNames {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		real, ok := realByKey[normalizeColKey(p)]
		if !ok {
			unmatched = append(unmatched, p)
			continue
		}
		if real != p {
			needsRewrite = true
		}
		pairs = append(pairs, aliasPair{real: real, prop: p})
	}

	if !needsRewrite || len(pairs) == 0 {
		// Every property name is already a byte-identical real column — the
		// existing canonical_query already works. Leave it alone to minimise
		// SQL churn for already-working queries.
		return canonical, unmatched
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].prop < pairs[j].prop })

	aliasParts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if p.real == p.prop {
			aliasParts = append(aliasParts, quoteIdent(p.real))
		} else {
			aliasParts = append(aliasParts, fmt.Sprintf("%s AS %s", quoteIdent(p.real), quoteIdent(p.prop)))
		}
	}

	return fmt.Sprintf("SELECT %s FROM (%s) __aligned", strings.Join(aliasParts, ", "), trimmed), unmatched
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
