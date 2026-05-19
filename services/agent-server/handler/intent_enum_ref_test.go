package handler

import (
	"database/sql"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/lakehouse2ontology/services/agent-server/smartquery"
	_ "github.com/lib/pq"
)

// enumRefTestDB opens a postgres connection from DATABASE_URL pointed at the
// project Postgres (lakehouse_keyword lives here). Skips when DATABASE_URL is
// unset — matches the pre-existing pattern in handler_agent_builder_test.go.
func enumRefTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping enum_ref resolver test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("DB unreachable, skipping: %v", err)
	}
	return db
}

// fdwVerifyProject is the demo project id seeded with city / channel /
// ownership / store_format / cost_layer keywords (see
// .omc/specs/bounded-value-ref-contract.md §3.5 and §5).
const fdwVerifyProject = "57832811-fed2-482b-be41-9bf27e49ccf6"

// T5 · candidate resolver returns lakehouse_keyword.keyword values for the
// given (project, property) and reports truncation when count > capped.
func TestResolveEnumRefCandidates_City(t *testing.T) {
	db := enumRefTestDB(t)
	defer db.Close()
	p := smartquery.IntentParameter{Name: "city", Type: "enum_ref", Property: "city"}
	got, truncated, err := resolveEnumRefCandidates(db, fdwVerifyProject, p)
	if err != nil {
		t.Fatalf("resolveEnumRefCandidates: %v", err)
	}
	// Demo seeds 10 city keywords (5 zh + 5 en) — see
	// services/.../demo_seeds/03-keywords.sql. Truncated must be false at <=50.
	if truncated {
		t.Errorf("city should not be truncated (≤ 50 candidates)")
	}
	if len(got) == 0 {
		t.Fatal("expected at least one city candidate")
	}
	// Both zh and en variants must surface — the resolver is the union of all
	// keyword rows for the property, mirroring the spec's §3.2 query.
	have := map[string]bool{}
	for _, v := range got {
		have[strings.ToLower(v)] = true
	}
	for _, must := range []string{"上海", "北京", "shanghai", "beijing"} {
		if !have[strings.ToLower(must)] {
			t.Errorf("missing candidate %q in %v", must, got)
		}
	}
	// Deterministic order: resolver sorts so prompt rendering is stable
	// across runs (prevents diff churn in golden tests downstream).
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	for i, v := range got {
		if v != sorted[i] {
			t.Errorf("candidate list not sorted: got[%d]=%q sorted[%d]=%q", i, v, i, sorted[i])
		}
	}
}

// T5b · property without keywords returns an empty list, NOT an error. This
// is the "schema misalignment" case (Intent points at a property whose
// candidate list hasn't been seeded yet) — the binder will then surface
// PARAM_VALUE_UNKNOWN with allowed=[] which is enough signal.
func TestResolveEnumRefCandidates_EmptyProperty(t *testing.T) {
	db := enumRefTestDB(t)
	defer db.Close()
	p := smartquery.IntentParameter{Name: "ghost", Type: "enum_ref", Property: "this_property_does_not_exist"}
	got, truncated, err := resolveEnumRefCandidates(db, fdwVerifyProject, p)
	if err != nil {
		t.Fatalf("resolveEnumRefCandidates on missing prop: %v", err)
	}
	if truncated {
		t.Error("empty result should not be flagged truncated")
	}
	if len(got) != 0 {
		t.Errorf("expected empty candidate list, got %v", got)
	}
}

// T5c · empty property string is a schema bug, caller must catch upstream
// — resolver returns ("invalid arg" style nil result without DB roundtrip
// so the binder's PARAM_SCHEMA_INVALID fires).
func TestResolveEnumRefCandidates_EmptyPropertyName(t *testing.T) {
	db := enumRefTestDB(t)
	defer db.Close()
	p := smartquery.IntentParameter{Name: "x", Type: "enum_ref" /* Property:"" */}
	got, _, err := resolveEnumRefCandidates(db, fdwVerifyProject, p)
	if err == nil {
		t.Fatal("expected error for empty Property in IntentParameter")
	}
	if len(got) != 0 {
		t.Errorf("expected nil candidates on schema error, got %v", got)
	}
}
