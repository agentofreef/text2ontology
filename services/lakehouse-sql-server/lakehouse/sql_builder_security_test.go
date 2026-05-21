package lakehouse

import (
	"strings"
	"testing"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// TestEscapeLikeValue: user-supplied LIKE/ILIKE metacharacters (% _ \) are
// neutralized so a value like "50%" cannot widen a system-built pattern, and a
// trailing backslash cannot escape the pattern boundary. Single quotes are still
// SQL-escaped for the surrounding string literal.
func TestEscapeLikeValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"50%", `50\%`},
		{"a_b", `a\_b`},
		{`c\d`, `c\\d`},
		{"plain", "plain"},
		{`100%_x\`, `100\%\_x\\`},
		{"o'brien", "o''brien"}, // quote escaping still applies
	}
	for _, tc := range cases {
		if got := escapeLikeValue(tc.in); got != tc.want {
			t.Fatalf("escapeLikeValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildFilterCondition_ILikeEscaping: the system-wildcard ops escape user
// metacharacters, while the explicit "like" op honors the user's wildcards.
func TestBuildFilterCondition_ILikeEscaping(t *testing.T) {
	textProp := smartquery.PropertyInfo{DataType: "text"}

	// "contains" wraps with %...%; the user's literal % must be escaped so it
	// matches a literal percent, not "any string".
	contains := buildFilterCondition(`t."c"`, smartquery.ResolvedFilter{
		Op: "contains", Value: "50%", Prop: textProp,
	})
	if !strings.Contains(contains, `\%`) {
		t.Fatalf("contains: user %% must be escaped, got %q", contains)
	}
	// The system's own surrounding wildcards remain unescaped.
	if !strings.HasPrefix(contains, `CAST(t."c" AS TEXT) ILIKE '%`) {
		t.Fatalf("contains: system wildcards must remain, got %q", contains)
	}

	// "like" passes the user pattern through unescaped (caller owns wildcards).
	like := buildFilterCondition(`t."c"`, smartquery.ResolvedFilter{
		Op: "like", Value: "A%B_C", Prop: textProp,
	})
	if strings.Contains(like, `\%`) || strings.Contains(like, `\_`) {
		t.Fatalf("like: user pattern must NOT be escaped, got %q", like)
	}
	if !strings.Contains(like, "A%B_C") {
		t.Fatalf("like: user pattern must pass through, got %q", like)
	}
}
