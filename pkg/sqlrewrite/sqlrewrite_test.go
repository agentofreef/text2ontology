package sqlrewrite

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestRejectDDL_AcceptsPlainSelect(t *testing.T) {
	cases := []string{
		`SELECT * FROM "Orders" WHERE qty > 10`,
		`SELECT a, b FROM "Orders" JOIN "Products" ON a = b LIMIT 100`,
		`SELECT 1;`, // trailing semicolon is stripped, still single statement
	}
	for _, c := range cases {
		if err := RejectDDL(c); err != nil {
			t.Errorf("RejectDDL(%q) = %v, want nil", c, err)
		}
	}
}

func TestRejectDDL_RejectsDangerous(t *testing.T) {
	cases := map[string]string{
		"drop":            `DROP TABLE "Orders"`,
		"insert":          `INSERT INTO "Orders" VALUES (1)`,
		"update":          `UPDATE "Orders" SET x = 1`,
		"delete":          `DELETE FROM "Orders"`,
		"set":             `SET search_path TO public`,
		"reset":           `RESET search_path`,
		"show":            `SHOW search_path`,
		"multi_statement": `SELECT 1; SELECT 2`,
		"dollar_quote":    `SELECT $$payload$$`,
		"dollar_tag":      `SELECT $tag$payload$tag$`,
		"pg_catalog":      `SELECT * FROM pg_catalog.pg_authid`,
		"information_sch": `SELECT * FROM information_schema.tables`,
		"public_user":     `SELECT * FROM public."user"`,
		"create":          `CREATE TABLE x (a int)`,
		"truncate":        `TRUNCATE "Orders"`,
	}
	for name, c := range cases {
		if err := RejectDDL(c); err == nil {
			t.Errorf("RejectDDL(%q) [%s] = nil, want error", c, name)
		}
	}
}

func TestExtractReferencedNames(t *testing.T) {
	got := ExtractReferencedNames(`SELECT * FROM "Orders" o JOIN Products p ON o.id = p.id`)
	want := []string{"Orders", "Products"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractReferencedNames = %v, want %v", got, want)
	}
}

func TestExtractReferencedNames_SkipsLateralOnly(t *testing.T) {
	got := ExtractReferencedNames(`SELECT * FROM "Orders" JOIN LATERAL (SELECT 1) x ON true`)
	want := []string{"Orders"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractReferencedNames = %v, want %v", got, want)
	}
}

func TestBuildCTEPrefix(t *testing.T) {
	od := map[string]string{
		"Orders":   "SELECT 1 AS id",
		"Products": "SELECT 2 AS id",
	}
	got := BuildCTEPrefix(od, []string{"Orders", "Products"})
	if !strings.HasPrefix(got, "WITH ") {
		t.Errorf("BuildCTEPrefix missing WITH prefix: %q", got)
	}
	if !strings.Contains(got, `"Orders" AS (`) || !strings.Contains(got, `"Products" AS (`) {
		t.Errorf("BuildCTEPrefix missing quoted Od identifiers: %q", got)
	}
	// Unknown names are skipped.
	got2 := BuildCTEPrefix(od, []string{"Orders", "Unknown"})
	if strings.Contains(got2, "Unknown") {
		t.Errorf("BuildCTEPrefix should skip unknown names: %q", got2)
	}
	// Empty used → empty string (no naked WITH).
	if BuildCTEPrefix(od, nil) != "" {
		t.Errorf("BuildCTEPrefix(nil) should be empty")
	}
}

func TestMaybeInjectLimit(t *testing.T) {
	if got := MaybeInjectLimit(`SELECT 1`, 100); !strings.Contains(got, "LIMIT 100") {
		t.Errorf("MaybeInjectLimit did not inject: %q", got)
	}
	if got := MaybeInjectLimit(`SELECT 1 LIMIT 5`, 100); strings.Contains(got, "LIMIT 100") {
		t.Errorf("MaybeInjectLimit should not double-inject: %q", got)
	}
}

func TestSubstitutePlaceholders_SingleParam(t *testing.T) {
	got, names := SubstitutePlaceholders(`SELECT * FROM "Orders" WHERE qty > {{minQty}}`)
	want := `SELECT * FROM "Orders" WHERE qty > $1`
	if got != want {
		t.Errorf("rewritten = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(names, []string{"minQty"}) {
		t.Errorf("names = %v, want [minQty]", names)
	}
}

func TestSubstitutePlaceholders_SameParamTwice(t *testing.T) {
	// Same param appearing twice produces two positional placeholders ($1, $2),
	// with the name repeated in $N order. Caller binds the value twice.
	got, names := SubstitutePlaceholders(`SELECT * FROM "Orders" WHERE a = {{x}} OR b = {{x}}`)
	want := `SELECT * FROM "Orders" WHERE a = $1 OR b = $2`
	if got != want {
		t.Errorf("rewritten = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(names, []string{"x", "x"}) {
		t.Errorf("names = %v, want [x x]", names)
	}
}

func TestSubstitutePlaceholders_WhitespaceTrimmed(t *testing.T) {
	got, names := SubstitutePlaceholders(`WHERE a = {{  spaced  }}`)
	if got != `WHERE a = $1` {
		t.Errorf("rewritten = %q, want %q", got, `WHERE a = $1`)
	}
	if !reflect.DeepEqual(names, []string{"spaced"}) {
		t.Errorf("names = %v, want [spaced]", names)
	}
}

// TestSubstitutePlaceholders_QuotedIsAlsoSubstituted documents the BARE/UNQUOTED
// contract: a placeholder written inside a string literal is ALSO substituted
// (the regex ignores surrounding quotes), producing the literal string `'$1'`
// rather than a bound parameter. Authors MUST write bare placeholders.
func TestSubstitutePlaceholders_QuotedIsAlsoSubstituted(t *testing.T) {
	got, names := SubstitutePlaceholders(`WHERE name = '{{x}}'`)
	want := `WHERE name = '$1'`
	if got != want {
		t.Errorf("rewritten = %q, want %q (quoted placeholder is still substituted)", got, want)
	}
	if !reflect.DeepEqual(names, []string{"x"}) {
		t.Errorf("names = %v, want [x]", names)
	}
}

func TestSubstitutePlaceholders_NoPlaceholders(t *testing.T) {
	got, names := SubstitutePlaceholders(`SELECT 1`)
	if got != `SELECT 1` {
		t.Errorf("rewritten = %q, want unchanged", got)
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want empty", names)
	}
}

// TestPipelineOrder confirms substitution runs cleanly before the DDL scan:
// a placeholder value can't smuggle a banned keyword because the brace content
// is restricted to bare identifiers, and the substituted text is what RejectDDL
// sees.
func TestPipelineOrder(t *testing.T) {
	rewritten, _ := SubstitutePlaceholders(`SELECT * FROM "Orders" WHERE qty > {{minQty}}`)
	if err := RejectDDL(rewritten); err != nil {
		t.Errorf("RejectDDL(post-substitution) = %v, want nil", err)
	}
}

// ─── {sys.req/opt.NAME} inline-parameter tests ──────────────────────────────

// acceptanceSQL is the spec's end-to-end example: an optional dimension in the
// SELECT + GROUP BY and a required value in the WHERE.
const acceptanceSQL = `select "COUNTRY", {sys.opt.PRODUCT_NAME}, sum("ORDER_QUANTITY")
from "EARLY_ORDER"
where "COUNTRY" = {sys.req.COUNTRY}
group by "COUNTRY", {sys.opt.PRODUCT_NAME}`

// 1. ACCEPTANCE: both params provided. PRODUCT_NAME (dimension) → "PRODUCT_NAME"
// in select + group by; COUNTRY (value) → $1 in WHERE; args=["中国"];
// missingRequired empty.
func TestRenderSysParams_AcceptanceBothProvided(t *testing.T) {
	rendered, args, missing := RenderSysParams(acceptanceSQL, map[string]interface{}{
		"COUNTRY":      "中国",
		"PRODUCT_NAME": "X",
	})
	if !strings.Contains(rendered, `where "COUNTRY" = $1`) {
		t.Errorf("WHERE value not bound to $1:\n%s", rendered)
	}
	if strings.Count(rendered, `"PRODUCT_NAME"`) != 2 {
		t.Errorf("PRODUCT_NAME dimension should appear in select + group by:\n%s", rendered)
	}
	if !reflect.DeepEqual(args, []interface{}{"中国"}) {
		t.Errorf("args = %v, want [中国]", args)
	}
	if len(missing) != 0 {
		t.Errorf("missingRequired = %v, want empty", missing)
	}
	if strings.Contains(rendered, "{sys.") {
		t.Errorf("leftover sys token in rendered:\n%s", rendered)
	}
}

// 2. ACCEPTANCE: only COUNTRY provided. PRODUCT_NAME absent → removed from
// select (no dangling comma) AND from group by; COUNTRY → $1; args=["中国"];
// missingRequired empty.
func TestRenderSysParams_AcceptanceOptionalDimensionAbsent(t *testing.T) {
	rendered, args, missing := RenderSysParams(acceptanceSQL, map[string]interface{}{
		"COUNTRY": "中国",
	})
	if strings.Contains(rendered, "PRODUCT_NAME") {
		t.Errorf("absent optional dimension should be removed:\n%s", rendered)
	}
	// No dangling comma in the SELECT list: `"COUNTRY", , sum(` must not appear.
	if strings.Contains(rendered, ", ,") || strings.Contains(rendered, ",,") {
		t.Errorf("dangling comma in select:\n%s", rendered)
	}
	if !strings.Contains(rendered, `select "COUNTRY", sum("ORDER_QUANTITY")`) {
		t.Errorf("select list not cleaned to `\"COUNTRY\", sum(...)`:\n%s", rendered)
	}
	if !strings.Contains(rendered, `group by "COUNTRY"`) || strings.Contains(rendered, `group by "COUNTRY",`) {
		t.Errorf("group by not cleaned to `\"COUNTRY\"`:\n%s", rendered)
	}
	if !strings.Contains(rendered, `where "COUNTRY" = $1`) {
		t.Errorf("WHERE value not bound to $1:\n%s", rendered)
	}
	if !reflect.DeepEqual(args, []interface{}{"中国"}) {
		t.Errorf("args = %v, want [中国]", args)
	}
	if len(missing) != 0 {
		t.Errorf("missingRequired = %v, want empty", missing)
	}
}

// 3. No params provided → missingRequired = [COUNTRY].
func TestRenderSysParams_MissingRequired(t *testing.T) {
	_, _, missing := RenderSysParams(acceptanceSQL, map[string]interface{}{})
	if !reflect.DeepEqual(missing, []string{"COUNTRY"}) {
		t.Errorf("missingRequired = %v, want [COUNTRY]", missing)
	}
}

// 4. ParseSysParams on the example → [{COUNTRY,true},{PRODUCT_NAME,false}]
// in SOURCE order (PRODUCT_NAME first appears in the select, COUNTRY in WHERE...
// but source order is: PRODUCT_NAME(select) then COUNTRY(where) then
// PRODUCT_NAME(group by)). Distinct in source order = [PRODUCT_NAME, COUNTRY].
func TestParseSysParams_Acceptance(t *testing.T) {
	got := ParseSysParams(acceptanceSQL)
	want := []SysParam{
		{Name: "PRODUCT_NAME", Required: false},
		{Name: "COUNTRY", Required: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseSysParams = %+v, want %+v", got, want)
	}
}

// 4b. required wins when a name appears as both req and opt.
func TestParseSysParams_RequiredWins(t *testing.T) {
	got := ParseSysParams(`select {sys.opt.X} where a = {sys.req.X}`)
	want := []SysParam{{Name: "X", Required: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseSysParams = %+v, want %+v (required wins)", got, want)
	}
}

// 5a. Optional VALUE param absent, trailing predicate → predicate dropped + the
// leading AND cleaned: `WHERE x=1 AND col={opt}` → `WHERE x=1`.
func TestRenderSysParams_OptionalValueDroppedTrailingAnd(t *testing.T) {
	sql := `select * from "T" where x = 1 and "COL" = {sys.opt.C}`
	rendered, args, missing := RenderSysParams(sql, map[string]interface{}{})
	if strings.Contains(rendered, "COL") || strings.Contains(rendered, "{sys.") {
		t.Errorf("optional value predicate not dropped:\n%s", rendered)
	}
	if !strings.Contains(rendered, "where x = 1") {
		t.Errorf("WHERE x=1 not preserved:\n%s", rendered)
	}
	// No dangling AND.
	if regexpMustMatch(t, `(?i)and\s*$`, strings.TrimSpace(rendered)) {
		t.Errorf("dangling AND remains:\n%s", rendered)
	}
	if len(args) != 0 || len(missing) != 0 {
		t.Errorf("args=%v missing=%v, want empty", args, missing)
	}
}

// 5b. Optional VALUE param absent, leading predicate → predicate dropped + the
// trailing AND cleaned: `WHERE col={opt} AND x=1` → `WHERE x=1`.
func TestRenderSysParams_OptionalValueDroppedLeadingAnd(t *testing.T) {
	sql := `select * from "T" where "COL" = {sys.opt.C} and x = 1`
	rendered, _, _ := RenderSysParams(sql, map[string]interface{}{})
	if strings.Contains(rendered, "COL") || strings.Contains(rendered, "{sys.") {
		t.Errorf("optional value predicate not dropped:\n%s", rendered)
	}
	if !strings.Contains(rendered, "where x = 1") {
		t.Errorf("expected `where x = 1`, got:\n%s", rendered)
	}
}

// 6. RejectDDL still catches DDL when run on the RENDERED text.
func TestRenderSysParams_RejectDDLOnRendered(t *testing.T) {
	// A rendered query that (hypothetically) carries a banned verb must still be
	// caught by RejectDDL on the post-render text.
	rendered, _, _ := RenderSysParams(`drop table "T" where a = {sys.req.A}`,
		map[string]interface{}{"A": "x"})
	if err := RejectDDL(rendered); err == nil {
		t.Errorf("RejectDDL(rendered) = nil, want error (DROP)")
	}
	// And a clean rendered SELECT passes.
	rendered2, _, _ := RenderSysParams(`select * from "T" where a = {sys.req.A}`,
		map[string]interface{}{"A": "x"})
	if err := RejectDDL(rendered2); err != nil {
		t.Errorf("RejectDDL(clean rendered) = %v, want nil", err)
	}
}

// Empty-string value is treated as NOT provided (required → missing).
func TestRenderSysParams_EmptyStringIsAbsent(t *testing.T) {
	_, args, missing := RenderSysParams(`select * from "T" where a = {sys.req.A}`,
		map[string]interface{}{"A": ""})
	if len(args) != 0 {
		t.Errorf("empty value should not bind an arg, got %v", args)
	}
	if !reflect.DeepEqual(missing, []string{"A"}) {
		t.Errorf("missingRequired = %v, want [A]", missing)
	}
}

// IN ( … ) value position binds to $N.
func TestRenderSysParams_InListValue(t *testing.T) {
	rendered, args, _ := RenderSysParams(`select * from "T" where c in ({sys.req.C})`,
		map[string]interface{}{"C": "v"})
	if !strings.Contains(rendered, "in ($1)") {
		t.Errorf("IN value not bound to $1:\n%s", rendered)
	}
	if !reflect.DeepEqual(args, []interface{}{"v"}) {
		t.Errorf("args = %v, want [v]", args)
	}
}

// ─── list-value ( = ANY($N) ) tests ─────────────────────────────────────────

const anyListSQL = `select "CATEGORY", sum("ORDER_QUANTITY")
from "EARLY_ORDER"
where "CATEGORY" = ANY({sys.opt.CATS})
group by "CATEGORY"`

// A list value provided for a `= ANY(...)` param binds as a SINGLE []string arg
// to one $N (the executor wraps it in pq.Array); the SQL keeps `= ANY($1)`.
func TestRenderSysParams_AnyListValue(t *testing.T) {
	rendered, args, missing := RenderSysParams(anyListSQL, map[string]interface{}{
		"CATS": []interface{}{"NBKBLAYOUT", "NBADOBE_ACROBAT"},
	})
	if !strings.Contains(rendered, `= ANY($1)`) {
		t.Errorf("list value not bound to ANY($1):\n%s", rendered)
	}
	if strings.Contains(rendered, "{sys.") {
		t.Errorf("leftover sys token:\n%s", rendered)
	}
	if len(args) != 1 {
		t.Fatalf("want 1 arg (the list bound to $1), got %d: %v", len(args), args)
	}
	if !reflect.DeepEqual(args[0], []string{"NBKBLAYOUT", "NBADOBE_ACROBAT"}) {
		t.Errorf("arg[0] = %#v, want []string{NBKBLAYOUT, NBADOBE_ACROBAT}", args[0])
	}
	if len(missing) != 0 {
		t.Errorf("missingRequired = %v, want empty", missing)
	}
}

// A single-element list still binds as a 1-element []string arg (so an editor
// that always sends ANY-params as arrays works for one value too).
func TestRenderSysParams_AnySingleElementList(t *testing.T) {
	_, args, _ := RenderSysParams(anyListSQL, map[string]interface{}{
		"CATS": []string{"NBKBLAYOUT"},
	})
	if len(args) != 1 {
		t.Fatalf("want 1 arg, got %d", len(args))
	}
	if !reflect.DeepEqual(args[0], []string{"NBKBLAYOUT"}) {
		t.Errorf("arg[0] = %#v, want []string{NBKBLAYOUT}", args[0])
	}
}

// An absent optional `= ANY(...)` param drops the whole predicate WITHOUT
// leaving a dangling `)` — the matching close-paren is swallowed.
func TestRenderSysParams_AnyListAbsentDropped(t *testing.T) {
	rendered, args, missing := RenderSysParams(anyListSQL, map[string]interface{}{})
	if strings.Contains(rendered, "CATS") || strings.Contains(rendered, "{sys.") {
		t.Errorf("absent ANY predicate not dropped:\n%s", rendered)
	}
	// ANY( and its matching ) must be gone (the legitimate sum(...) paren stays).
	if regexpMustMatch(t, `(?i)\bany\b`, rendered) || strings.Contains(rendered, "()") {
		t.Errorf("dangling ANY( / ) left behind:\n%s", rendered)
	}
	// The sole WHERE predicate was the absent optional → WHERE itself is gone.
	if regexpMustMatch(t, `(?i)\bwhere\b`, rendered) {
		t.Errorf("empty WHERE not removed:\n%s", rendered)
	}
	if len(args) != 0 || len(missing) != 0 {
		t.Errorf("args=%v missing=%v, want empty", args, missing)
	}
}

// ─── Three-state semantic tests (key absent / present-empty / present-value) ─
//
// A single `{sys.opt.NAME}` placed in BOTH identifier position (SELECT/GROUP BY)
// AND value position (WHERE) flips through three behaviors driven by what the
// caller puts in `values`:
//   1. key absent                  → dim pruned + predicate pruned
//   2. key present, empty value    → dim rendered + predicate pruned
//   3. key present, non-empty val  → dim rendered + predicate bound to $N

const triStateSQL = `select "ORDER_TYPE", {sys.opt.GEO}, sum("ORDER_QUANTITY") qty
from "EARLY_ORDER"
where "GEO" = {sys.opt.GEO}
group by "ORDER_TYPE", {sys.opt.GEO}`

// State 1: key absent → GEO is gone everywhere.
func TestRenderSysParams_TriState_KeyAbsent(t *testing.T) {
	rendered, args, missing := RenderSysParams(triStateSQL, map[string]interface{}{})
	if strings.Contains(rendered, `"GEO"`) || strings.Contains(rendered, "{sys.") {
		t.Errorf("key-absent: GEO should be pruned everywhere:\n%s", rendered)
	}
	if regexpMustMatch(t, `(?i)\bwhere\b`, rendered) {
		t.Errorf("key-absent: empty WHERE not removed:\n%s", rendered)
	}
	if len(args) != 0 || len(missing) != 0 {
		t.Errorf("args=%v missing=%v want empty", args, missing)
	}
}

// State 2: key present with empty value → GEO appears in SELECT + GROUP BY
// (dim toggled on by key-presence), but the WHERE predicate is dropped (no
// non-empty value to bind).
func TestRenderSysParams_TriState_KeyPresentEmpty(t *testing.T) {
	rendered, args, missing := RenderSysParams(triStateSQL, map[string]interface{}{"GEO": ""})
	// Dim rendered in select + group by → "GEO" appears twice (once each).
	if strings.Count(rendered, `"GEO"`) < 2 {
		t.Errorf("key-present-empty: GEO dim should appear in select + group by:\n%s", rendered)
	}
	// WHERE predicate dropped → no `"GEO" =`, no bare `WHERE` left over.
	if regexpMustMatch(t, `(?i)"GEO"\s*=`, rendered) {
		t.Errorf("key-present-empty: WHERE predicate should be pruned:\n%s", rendered)
	}
	if regexpMustMatch(t, `(?i)\bwhere\b`, rendered) {
		t.Errorf("key-present-empty: empty WHERE not removed:\n%s", rendered)
	}
	if len(args) != 0 || len(missing) != 0 {
		t.Errorf("args=%v missing=%v want empty", args, missing)
	}
}

// State 3: key present with a value → dim rendered AND WHERE predicate binds $N.
func TestRenderSysParams_TriState_KeyPresentValue(t *testing.T) {
	rendered, args, missing := RenderSysParams(triStateSQL, map[string]interface{}{"GEO": "AP"})
	if strings.Count(rendered, `"GEO"`) < 3 {
		t.Errorf("key-present-value: GEO should appear in select + where-lhs + group by:\n%s", rendered)
	}
	if !strings.Contains(rendered, `"GEO" = $1`) {
		t.Errorf("key-present-value: WHERE should bind $1:\n%s", rendered)
	}
	if !reflect.DeepEqual(args, []interface{}{"AP"}) {
		t.Errorf("args = %v, want [AP]", args)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v want empty", missing)
	}
}

// An empty list is treated as NOT provided (same as an empty string), so an
// optional `= ANY(...)` predicate is dropped.
func TestRenderSysParams_AnyEmptyListIsAbsent(t *testing.T) {
	rendered, args, _ := RenderSysParams(anyListSQL, map[string]interface{}{
		"CATS": []interface{}{},
	})
	if strings.Contains(rendered, "ANY") || strings.Contains(rendered, "{sys.") {
		t.Errorf("empty list should drop the predicate:\n%s", rendered)
	}
	if len(args) != 0 {
		t.Errorf("empty list should bind no args, got %v", args)
	}
}

func regexpMustMatch(t *testing.T, pattern, s string) bool {
	t.Helper()
	m, err := regexp.MatchString(pattern, s)
	if err != nil {
		t.Fatalf("bad pattern %q: %v", pattern, err)
	}
	return m
}
