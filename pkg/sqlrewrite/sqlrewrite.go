// Package sqlrewrite is the single source of truth for the security-critical
// ontology-SQL rewrite + validation logic shared by the public
// /api/ontology/sql-passthrough handler (backend-api) and the SQL-mode metric
// runtime (lakehouse-sql-server).
//
// It is a PURE module: no database handle, no service imports, no I/O. That
// keeps it safe to import from any layer under the 4-layer rule (pkg/* must
// never depend on services/*).
//
// Pipeline order matters for safety. Callers MUST run:
//
//  1. SubstitutePlaceholders — replace every `{{name}}` with a positional `$N`
//     BEFORE any keyword/identifier scanning, so brace payloads can never hide
//     content from RejectDDL / ExtractReferencedNames.
//  2. RejectDDL — block DDL/DML verbs, dollar-quoting, cross-schema escapes,
//     and multi-statement input on the post-substitution text.
//  3. ExtractReferencedNames — pull FROM/JOIN identifiers for OD validation.
//  4. BuildCTEPrefix + MaybeInjectLimit — wrap canonical_query CTEs and cap rows.
//
// All user/LLM-supplied VALUES flow exclusively through the positional `$N`
// driver args produced by SubstitutePlaceholders — they are NEVER concatenated
// into the SQL text.
package sqlrewrite

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// dollarQuoteRe matches a PostgreSQL dollar-quote opener: $$ or $tag$ where the
// optional tag is an identifier. Presence of any opener is treated as hostile in
// the read-only passthrough path (see RejectDDL).
var dollarQuoteRe = regexp.MustCompile(`\$[A-Za-z_0-9]*\$`)

// RejectDDL blocks any DDL / dangerous keyword AND escape attempts that
// would let a passthrough query read outside its project's lakehouse
// schema (the `SET LOCAL search_path` set just before execution).
//
// The original implementation only filtered DML/DDL verbs. That leaves
// every read path open: `SELECT * FROM public."user"` happily exfiltrates
// password hashes, `SELECT * FROM pg_catalog.pg_authid` walks role
// credentials. So we additionally reject:
//
//   - explicit references to "public.", "pg_catalog.", "information_schema."
//   - any reference to a quoted "user" table
//   - multi-statement input via top-level ';'
//   - SET / RESET / SHOW (would let an attacker swap search_path mid-query)
//
// NOTE: this is the SINGLE SOURCE of the blocklist. It is byte-identical in
// behavior to the former handler.rejectDDL in backend-api's
// handler_sql_passthrough.go (which now delegates here).
func RejectDDL(sqlText string) error {
	// PG dollar-quoting ($$...$$ or $tag$...$tag$) lets a payload hide content
	// from the comment-strip + keyword scan below (the quoted body is opaque to
	// our --/ /* */ stripping and to the ';' multi-statement check). A read-only
	// single-statement passthrough SELECT never legitimately needs dollar-quotes,
	// so reject any dollar-quote opener outright rather than try to parse them.
	if dollarQuoteRe.MatchString(sqlText) {
		return fmt.Errorf("禁止美元引用（$$ / $tag$）语法")
	}

	cleaned := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(sqlText, " ")
	cleaned = regexp.MustCompile(`/\*[\s\S]*?\*/`).ReplaceAllString(cleaned, " ")
	lower := strings.ToLower(cleaned)

	banned := []string{
		`\bdrop\b`, `\btruncate\b`, `\balter\b`, `\bcreate\b`,
		`\bgrant\b`, `\brevoke\b`, `\bvacuum\b`, `\breindex\b`,
		`\binsert\b`, `\bupdate\b`, `\bdelete\b`, `\bcopy\b`,
		`\bset\b`, `\breset\b`, `\bshow\b`,
	}
	for _, p := range banned {
		if matched, _ := regexp.MatchString(p, lower); matched {
			return fmt.Errorf("只读查询，禁止的语句: %s", strings.Trim(p, `\b`))
		}
	}

	// Cross-schema escapes. We allow only references to the project's
	// lakehouse schema (caller wraps `SET LOCAL search_path TO <proj>,
	// public`); explicit "public.x" and any "pg_*" / "information_schema"
	// references are bona-fide exfiltration attempts.
	forbidden := []string{
		`\bpg_catalog\.`,
		`\binformation_schema\.`,
		`\bpg_authid\b`, `\bpg_shadow\b`, `\bpg_user\b`,
		`\bpublic\."?user"?\b`,
		`"user"\.|"user"\s*(?:\)|\(|where|on|left|right|inner|cross|join|from)`,
	}
	for _, p := range forbidden {
		if matched, _ := regexp.MatchString(p, lower); matched {
			return fmt.Errorf("禁止跨 schema 引用: %s", strings.Trim(p, `\b`))
		}
	}

	// Multi-statement protection. We strip trailing semicolons before
	// counting so an innocuous "SELECT 1;" still passes; anything that
	// has a ';' followed by non-whitespace is a chained statement.
	trimmed := strings.TrimRight(lower, "; \t\r\n")
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("只允许单条语句，禁止 ';' 分隔的多语句")
	}
	return nil
}

// nameRefRe extracts all table-like identifiers referenced in FROM/JOIN
// clauses. Handles both `"Quoted"` and unquoted forms. Comments are stripped
// inside ExtractReferencedNames before scanning.
var nameRefRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+(?:"([^"]+)"|([a-zA-Z_][a-zA-Z0-9_]*))`)

// ExtractReferencedNames returns all table-like identifiers referenced in
// FROM/JOIN clauses, in source order (duplicates preserved — the caller dedups).
func ExtractReferencedNames(sqlText string) []string {
	// Strip comments first.
	clean := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(sqlText, " ")
	clean = regexp.MustCompile(`/\*[\s\S]*?\*/`).ReplaceAllString(clean, " ")
	var out []string
	for _, m := range nameRefRe.FindAllStringSubmatch(clean, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if name == "" {
			continue
		}
		low := strings.ToLower(name)
		if low == "lateral" || low == "only" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// BuildCTEPrefix builds `WITH "Od1" AS (cq1), "Od2" AS (cq2)\n` using the keys
// of odCanonical (which carry the ORIGINAL Od names) as quoted identifiers, so
// quoted-identifier case matches the user's SQL.
//
// usedNames lists the Od names (matching odCanonical keys) actually referenced
// by the query, in the order they should appear in the WITH clause. Names not
// present in odCanonical are skipped.
func BuildCTEPrefix(odCanonical map[string]string, usedNames []string) string {
	var parts []string
	for _, name := range usedNames {
		cq, ok := odCanonical[name]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%q AS (\n%s\n)", name, cq))
	}
	if len(parts) == 0 {
		return ""
	}
	return "WITH " + strings.Join(parts, ",\n") + "\n"
}

var hasLimitRe = regexp.MustCompile(`(?i)\blimit\s+\d+`)

// MaybeInjectLimit appends `LIMIT <limit>` only if the SQL has no existing
// numeric LIMIT clause. Trailing semicolons / whitespace are trimmed first.
func MaybeInjectLimit(sqlText string, limit int) string {
	trimmed := strings.TrimRight(sqlText, "; \t\n\r")
	if hasLimitRe.MatchString(trimmed) {
		return sqlText
	}
	return trimmed + fmt.Sprintf("\nLIMIT %d", limit)
}

// placeholderRe matches a `{{name}}` placeholder. Whitespace inside the braces
// is tolerated and trimmed; the captured name is a bare identifier
// ([a-zA-Z_][a-zA-Z0-9_]*).
var placeholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// SubstitutePlaceholders replaces every `{{name}}` occurrence in sql with a
// positional `$N` placeholder, numbered by occurrence order (the first
// placeholder becomes $1, the second $2, …). The returned names slice lists the
// parameter name for each $N in $N order — so names[0] is the name bound to $1.
//
// A parameter that appears twice produces TWO positional placeholders ($1 and
// $2) with the name repeated in names — the caller binds the same value twice.
// This keeps the substitution purely textual and occurrence-driven (no name→$N
// memoization), which is the simplest correct behavior for the binder.
//
// IMPORTANT: placeholders must be BARE / UNQUOTED in the authored SQL. A
// placeholder written inside a string literal — e.g. `'{{x}}'` — is ALSO
// substituted (the regex does not inspect surrounding quotes), yielding `'$1'`,
// which Postgres treats as the literal two-character string `$1`, NOT a bound
// parameter. Authors must therefore write `col = {{x}}` (or `col = ANY({{x}})`),
// never `col = '{{x}}'`.
//
// This MUST run BEFORE RejectDDL / ExtractReferencedNames so the brace syntax
// cannot smuggle content past those scanners.
func SubstitutePlaceholders(sql string) (rewritten string, names []string) {
	idx := 0
	rewritten = placeholderRe.ReplaceAllStringFunc(sql, func(match string) string {
		sub := placeholderRe.FindStringSubmatch(match)
		// sub[1] is the trimmed bare name (regex already excludes surrounding
		// whitespace from the capture group via \s* outside the class).
		name := sub[1]
		idx++
		names = append(names, name)
		return fmt.Sprintf("$%d", idx)
	})
	return rewritten, names
}

// ─── Inline {sys.req/opt.NAME} parameter system ─────────────────────────────
//
// A SQL-mode metric declares its parameters INLINE inside query_sql, so there
// is no separate param table — the params are DERIVED by parsing the SQL:
//
//	{sys.req.NAME}   REQUIRED parameter
//	{sys.opt.NAME}   OPTIONAL parameter
//
// NAME = [A-Za-z_][A-Za-z0-9_]*. The req/opt segment is the required/optional
// flag. The same token can appear in two structurally distinct POSITIONS, and
// the runtime classifies each occurrence by the preceding non-whitespace
// context (see RenderSysParams):
//
//   - VALUE position (preceded by a comparison operator, "(", or the words
//     in/like/ilike/is): provided → the token becomes a positional `$N` and the
//     value is appended to the driver args (NEVER concatenated); optional+absent
//     → the whole predicate is dropped (best-effort), cleaning an adjacent
//     AND/OR.
//   - IDENTIFIER/dimension position (anything else — SELECT list, GROUP BY,
//     ORDER BY): provided → the token becomes a quoted identifier `"NAME"`;
//     optional+absent → the token plus ONE adjacent comma is removed.
//
// Required + absent in either position records the NAME in missingRequired.
//
// The IDENTIFIER position uses "key-presence" (key exists in `values`, regardless
// of whether its value is empty), NOT "has a usable value". This is what models
// the user's natural three-state intent for a single dimension/filter param:
//
//   key absent                  → dimension PRUNED + predicate PRUNED
//                                 (the column disappears from select/where/group)
//   key present, empty value    → dimension RENDERED + predicate PRUNED
//                                 ("show me a breakdown by this dim, no filter")
//   key present, non-empty val  → dimension RENDERED + predicate BOUND to $N
//                                 ("show breakdown AND filter to this value")
//
// So a single `{sys.opt.GEO}` placed in both SELECT/GROUP BY and `"GEO" = …` in
// WHERE flips through all three states from one knob.

// SysParam is one inline parameter derived from query_sql.
type SysParam struct {
	Name     string
	Required bool
}

// sysParamRe matches a `{sys.req.NAME}` / `{sys.opt.NAME}` token. Group 1 is the
// flag ("req"|"opt"), group 2 is the NAME. Whitespace inside the braces is not
// permitted (the syntax is authored verbatim); the match is case-sensitive on
// the literal "sys", "req", "opt" segments.
var sysParamRe = regexp.MustCompile(`\{sys\.(req|opt)\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// ParseSysParams returns the DISTINCT inline params in source order. If a NAME
// appears as both req and opt, it is reported once with Required=true (required
// wins). The result feeds the metric's derived `parameters` JSONB so the
// recall/agent layer's required-param knowledge stays in sync with the SQL.
func ParseSysParams(sql string) []SysParam {
	var out []SysParam
	idx := map[string]int{} // name → position in out
	for _, m := range sysParamRe.FindAllStringSubmatch(sql, -1) {
		req := m[1] == "req"
		name := m[2]
		if pos, seen := idx[name]; seen {
			// Required wins if the name appears with both flags.
			if req && !out[pos].Required {
				out[pos].Required = true
			}
			continue
		}
		idx[name] = len(out)
		out = append(out, SysParam{Name: name, Required: req})
	}
	return out
}

// valueCtxRe tests whether the text immediately preceding a token (with trailing
// whitespace already trimmed) ends in a VALUE-position context: a comparison
// operator, an opening paren (e.g. `IN (`), or one of the value keywords
// in/like/ilike/is. Anchored at end ($). Case-insensitive for the keywords.
var valueCtxRe = regexp.MustCompile(`(?i)(?:!=|<>|<=|>=|=|<|>|\(|\bin|\blike|\bilike|\bis)$`)

// quoteSysIdent renders NAME as a Postgres quoted identifier, escaping `"`→`""`.
func quoteSysIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// RenderSysParams position-awarely substitutes every `{sys.req/opt.NAME}` token.
//
// values maps NAME→value. The value is either a SCALAR (string/number, JSON
// string) or a LIST (JSON array → []interface{} / []string). A key being PRESENT
// with a non-empty scalar OR a non-empty list means "provided"; a missing key,
// nil, an empty string, or an empty list are all treated as NOT provided (so a
// blank editor field / empty LLM arg neither binds a stray value nor emits an
// empty dimension).
//
// It returns:
//   - rendered: the rewritten SQL (values gone to $N, dimensions quoted, absent
//     optionals removed),
//   - args: the ordered $N driver args (one per emitted $N, in $N order). A LIST
//     value is pushed as a single []string arg bound to ONE $N — the author must
//     use `= ANY({sys.x.NAME})` (NOT `IN (…)`) so the executor can bind it as a
//     Postgres array. The executor layer wraps []string args in pq.Array.
//   - missingRequired: the NAMEs of REQUIRED params that were absent.
//
// VALUES ALWAYS bind via the returned $N args — they are NEVER concatenated into
// rendered. RejectDDL MUST still run on rendered afterwards.
func RenderSysParams(sql string, values map[string]interface{}) (rendered string, args []interface{}, missingRequired []string) {
	provided := func(name string) (interface{}, bool) {
		v, ok := values[name]
		if !ok || v == nil {
			return nil, false
		}
		switch t := v.(type) {
		case string:
			if t == "" {
				return nil, false
			}
			return t, true
		case []interface{}:
			if len(t) == 0 {
				return nil, false
			}
			return t, true
		case []string:
			if len(t) == 0 {
				return nil, false
			}
			return t, true
		default:
			// numbers / bools decoded from JSON → bound as a scalar value.
			return t, true
		}
	}
	missingSet := map[string]bool{}
	argIdx := 0

	// We rebuild the string by walking match-by-match so we can inspect the
	// preceding context (for VALUE vs IDENTIFIER classification) and, for absent
	// optionals, reach back into already-emitted output to strip operands /
	// commas. `out` is the rewritten-so-far buffer; `last` is the end of the
	// previous match in sql.
	var out strings.Builder
	last := 0
	locs := sysParamRe.FindAllStringSubmatchIndex(sql, -1)
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		flag := sql[loc[2]:loc[3]]
		name := sql[loc[4]:loc[5]]
		required := flag == "req"

		// Emit the literal text between the previous match and this one.
		out.WriteString(sql[last:start])
		last = end

		// Classify by the preceding non-whitespace context of what we've emitted.
		emitted := out.String()
		ctx := strings.TrimRight(emitted, " \t\r\n")
		isValue := valueCtxRe.MatchString(ctx)

		val, has := provided(name)

		if isValue {
			if has {
				argIdx++
				out.WriteString(fmt.Sprintf("$%d", argIdx))
				// A list value binds as a single []string arg (→ pq.Array at the
				// executor); a scalar binds as-is. Either way it is ONE $N.
				if sl, ok := toStringSlice(val); ok {
					args = append(args, sl)
				} else {
					args = append(args, val)
				}
			} else if required {
				missingSet[name] = true
				// Leave a harmless empty marker? No — keep the operand for the
				// error path; the caller returns before executing. We drop the
				// token so RejectDDL on rendered stays clean if ever reached.
			} else {
				// Optional VALUE absent → drop the predicate best-effort:
				// remove the preceding `[operand] [op]` we already emitted, then
				// clean an adjacent AND/OR around the resulting gap. If the token
				// sat inside a `IN ( … )` / `= ANY( … )` wrapper (ctx ended in `(`),
				// also swallow the matching `)` that follows so no paren dangles.
				openedParen := strings.HasSuffix(ctx, "(")
				trimmed := dropOptionalValuePredicate(emitted)
				dropped := trimmed != emitted
				out.Reset()
				out.WriteString(trimmed)
				if openedParen && dropped {
					if m := leadingCloseParenRe.FindStringIndex(sql[last:]); m != nil && m[0] == 0 {
						last += m[1]
					}
				}
			}
		} else {
			// IDENTIFIER/dimension position — the rendered identifier is the param
			// NAME (which equals the column name); KEY-PRESENCE (even with an empty
			// value) is what toggles the dimension ON, NOT having a non-empty value.
			// This is what makes the user's three-state intent work:
			//   key absent      → prune dim
			//   key present ""  → render dim (no filter — value pos handles that)
			//   key present val → render dim (and value pos binds $N too)
			// `val` is unused; the rendered ident is the param name.
			_ = val
			_, keyPresent := values[name]
			if keyPresent {
				out.WriteString(quoteSysIdent(name))
			} else if required {
				missingSet[name] = true
			} else {
				// Optional IDENTIFIER/dimension absent → remove the token and ONE
				// adjacent comma. Prefer trailing `{token}\s*,`; here the token is
				// not yet emitted, so we look ahead in the remaining sql for a
				// following comma; else strip a leading comma already in `emitted`.
				rest := sql[last:]
				if m := leadingCommaRe.FindStringIndex(rest); m != nil && m[0] == 0 {
					// Trailing comma case: `…, {token},` → drop the FOLLOWING comma
					// (keep the separator before the token). Trim the whitespace we
					// emitted before the token so `"COUNTRY", {token},` collapses to
					// `"COUNTRY",` (one separator), then the rest ` sum(...)` reads
					// `"COUNTRY", sum(...)` — no double space, no dangling comma.
					out.Reset()
					out.WriteString(strings.TrimRight(emitted, " \t\r\n"))
					last += m[1]
				} else {
					// Leading comma case: `…, {token}` → drop the comma we emitted.
					out.Reset()
					out.WriteString(stripTrailingComma(emitted))
				}
			}
		}
	}
	out.WriteString(sql[last:])
	rendered = cleanDanglingConnectors(out.String())

	for name := range missingSet {
		missingRequired = append(missingRequired, name)
	}
	sort.Strings(missingRequired)
	return rendered, args, missingRequired
}

// toStringSlice converts a list-shaped value ([]interface{} from JSON, or a
// []string) to []string for array binding. Returns ok=false for scalars.
func toStringSlice(v interface{}) ([]string, bool) {
	switch t := v.(type) {
	case []string:
		return t, true
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if e == nil {
				continue
			}
			out = append(out, fmt.Sprint(e))
		}
		return out, true
	}
	return nil, false
}

// whereAndRe collapses a `WHERE` immediately followed by a dangling `AND`/`OR`
// (the case where the FIRST predicate after WHERE was an absent optional value,
// leaving `WHERE  AND x=1`). The captured `where` keyword (preserving its
// original case) is re-emitted; the dangling connector is dropped.
var whereAndRe = regexp.MustCompile(`(?i)\bwhere\b\s+(?:and|or)\b\s+`)

// trailingConnRe trims a dangling `AND`/`OR` left at the very end of a clause
// (e.g. `WHERE col={opt}` as the last predicate would leave `WHERE x=1 AND `).
// Anchored to clause boundaries: end-of-string or a following GROUP/ORDER/LIMIT/
// HAVING keyword.
var trailingConnRe = regexp.MustCompile(`(?i)\s+(?:and|or)\s+(\b(?:group|order|limit|having)\b|$)`)

// danglingWhereRe removes a `WHERE` that ended up with no predicate at all
// (e.g. the sole predicate was an absent optional value): `WHERE GROUP BY` or a
// trailing bare `WHERE`. Keeps the following clause keyword.
var danglingWhereRe = regexp.MustCompile(`(?i)\bwhere\b\s+(\b(?:group|order|limit|having)\b|$)`)

// cleanDanglingConnectors reconciles connector debris left by dropping absent
// optional VALUE predicates. Order matters: collapse `WHERE AND/OR` first, then
// trailing connectors, then an empty WHERE.
func cleanDanglingConnectors(s string) string {
	prev := ""
	// Iterate to a fixpoint so chained drops (multiple absent optionals) settle.
	for s != prev {
		prev = s
		s = whereAndRe.ReplaceAllStringFunc(s, func(m string) string {
			// Preserve the original WHERE token + a single trailing space.
			idx := strings.IndexFunc(m, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })
			where := m
			if idx >= 0 {
				where = m[:idx]
			}
			return where + " "
		})
		s = trailingConnRe.ReplaceAllString(s, " $1")
	}
	s = danglingWhereRe.ReplaceAllString(s, "$1")
	return s
}

// leadingCommaRe matches optional whitespace then a comma at the start of a
// string — used to detect a trailing comma AFTER an omitted dimension token.
var leadingCommaRe = regexp.MustCompile(`^\s*,`)

// trailingCommaRe matches a comma (with surrounding whitespace) at the END of a
// string — used to strip a leading comma BEFORE an omitted dimension token.
var trailingCommaRe = regexp.MustCompile(`,\s*$`)

// stripTrailingComma removes a single trailing comma (and surrounding space) so
// `select "COUNTRY", ` collapses to `select "COUNTRY"` after the dimension after
// the comma is dropped.
func stripTrailingComma(s string) string {
	return trailingCommaRe.ReplaceAllString(s, "")
}

// optionalPredicateRe matches a trailing `[operand] [op]` at the end of the
// already-emitted text, optionally preceded by an AND/OR connector which it
// captures so the connector can be reconciled. Group 1 = leading and|or or
// empty; the rest (operand + op) is discarded. WHERE is deliberately NOT a
// consumable connector — `WHERE col={opt}` must keep WHERE and drop only `col=`,
// with the dangling `WHERE AND` collapsed later by cleanDanglingConnectors.
//
// operand is a column-ish token: a quoted "ident", a dotted a.b ident, or a bare
// ident; op is one of the comparison / membership operators. The `in (` case is
// handled by also tolerating a trailing `(` after `in`; the `= ANY(` case is
// handled by tolerating an optional `any` keyword before that paren.
var optionalPredicateRe = regexp.MustCompile(`(?i)(\b(?:and|or)\b\s+)?(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_.]*)\s*(?:!=|<>|<=|>=|=|<|>|\bnot\s+in|\bin|\bnot\s+like|\blike|\bilike|\bis)\s*(?:any\s*)?\(?\s*$`)

// leadingCloseParenRe matches optional whitespace then a closing paren at the
// start of a string — used to swallow the `)` that follows an omitted optional
// value inside a `IN ( … )` / `= ANY( … )` wrapper.
var leadingCloseParenRe = regexp.MustCompile(`^\s*\)`)

// dropOptionalValuePredicate removes the trailing `[operand] [op]` from emitted
// (an absent optional value sat right after it) and reconciles connectors:
//
//	WHERE x=1 AND col=    → WHERE x=1        (drop the dangling AND col=)
//	WHERE col=    AND x=1 → handled by the caller's next-token AND cleanup; here
//	                        we drop `col=` leaving `WHERE  AND x=1`, then the
//	                        leadingConnector cleanup turns `WHERE AND` → `WHERE`.
//
// Strategy: strip the predicate. If it carried a leading AND/OR, the gap is
// already closed. If it was the FIRST predicate after WHERE (no leading
// connector), a following `AND`/`OR` in the remaining SQL must be swallowed by
// the caller — but since we only see `emitted` here, we instead leave WHERE in
// place and let the dangling connector be cleaned by danglingConnectorRe.
func dropOptionalValuePredicate(emitted string) string {
	loc := optionalPredicateRe.FindStringSubmatchIndex(emitted)
	if loc == nil {
		// No recognizable operand/op precedes the token; just drop nothing extra
		// (defensive — should not happen for a value-position token).
		return emitted
	}
	leadConnPresent := loc[2] != -1 // capture group 1 matched a connector
	head := emitted[:loc[0]]
	if leadConnPresent {
		// `... AND col=` → `...` (connector consumed with the predicate).
		return strings.TrimRight(head, " \t\r\n")
	}
	// First predicate after WHERE (no leading connector). Keep WHERE; a following
	// AND/OR (in the not-yet-emitted SQL) becomes leading and is cleaned post-hoc
	// by RenderSysParams via danglingConnectorRe applied to the final string.
	// We re-append a single space so a following `AND x=1` reads `WHERE  AND x=1`,
	// which the final cleanup collapses to `WHERE x=1`.
	return strings.TrimRight(head, " \t\r\n") + " "
}
