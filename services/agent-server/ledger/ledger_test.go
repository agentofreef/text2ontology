package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"
	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// --- Pure-Go tests (no DB) ---------------------------------------------

func TestNew_Empty(t *testing.T) {
	l := New()
	if !l.IsEmpty() {
		t.Fatal("New() should be empty")
	}
	if l.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", l.SchemaVersion, SchemaVersion)
	}
	// Maps must be non-nil so callers can write without nil check.
	l.Ods["foo"] = &LedgerOd{OdBlock: recall.OdBlock{Name: "foo"}}
	l.Tokens["bar"] = &LedgerToken{FirstSeen: 1}
	if l.IsEmpty() {
		t.Fatal("ledger with content shouldn't be empty")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	orig := New()
	orig.TurnCount = 3
	orig.Ods["od1"] = &LedgerOd{
		OdBlock: recall.OdBlock{
			OdID: "od1", Name: "ORDER", Kind: "fact",
			Description: "order table",
			AllPropNames: []string{"Order_Quantity", "Order_Type"},
		},
		LoadedInTurn: 1,
		LoadMethod:   "lookup",
	}
	orig.Intents["i1"] = &LedgerIntent{
		MetricIntent: recall.MetricIntent{
			IntentID: "i1", Name: "Order.Quantity",
			CanonicalMetric: "sum(Order_Quantity)",
			AutoGroupBy:     []string{"Order_Type"},
			MatchedTokens:   []string{"订单"},
		},
		FirstSeenInTurn: 1,
	}
	orig.Tokens["订单"] = &LedgerToken{
		FirstSeen: 1, LastSeen: 2, StrongHit: true,
		MatchedOds:     []string{"od1"},
		MatchedIntents: []string{"i1"},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back Ledger
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	back.EnsureMaps()

	if back.TurnCount != 3 {
		t.Fatalf("top-level fields dropped: %+v", back)
	}
	od, ok := back.Ods["od1"]
	if !ok {
		t.Fatal("od1 missing after roundtrip")
	}
	if od.Name != "ORDER" || od.LoadedInTurn != 1 || od.LoadMethod != "lookup" {
		t.Fatalf("LedgerOd fields wrong after roundtrip: %+v", od)
	}
	intent, ok := back.Intents["i1"]
	if !ok || intent.Name != "Order.Quantity" || intent.FirstSeenInTurn != 1 {
		t.Fatalf("LedgerIntent wrong: %+v", intent)
	}
	tok, ok := back.Tokens["订单"]
	if !ok || !tok.StrongHit || len(tok.MatchedOds) != 1 {
		t.Fatalf("LedgerToken wrong: %+v", tok)
	}
}

func TestEnsureMaps_NilSafe(t *testing.T) {
	var l Ledger
	l.EnsureMaps()
	if l.Ods == nil || l.Intents == nil || l.Tokens == nil {
		t.Fatal("EnsureMaps left a nil map")
	}
	// Writes should not panic after EnsureMaps.
	l.Ods["x"] = &LedgerOd{}
	l.Tokens["y"] = &LedgerToken{}
}

func TestBuildCachedContext_FiltersWeakTokens(t *testing.T) {
	l := New()
	l.Ods["od1"] = &LedgerOd{OdBlock: recall.OdBlock{OdID: "od1", Name: "ORDER"}}
	l.Intents["i1"] = &LedgerIntent{MetricIntent: recall.MetricIntent{IntentID: "i1", Name: "Order.Quantity"}}
	l.Tokens["strong"] = &LedgerToken{FirstSeen: 1, StrongHit: true, MatchedOds: []string{"od1"}, MatchedIntents: []string{"i1"}}
	l.Tokens["weak"] = &LedgerToken{FirstSeen: 1, StrongHit: false, MatchedOds: []string{"od1"}}

	c := BuildCachedContext(l)
	if c == nil {
		t.Fatal("nil cached context")
	}
	if _, ok := c.Tokens["weak"]; ok {
		t.Fatal("weak token must not appear in CachedContext.Tokens")
	}
	strong, ok := c.Tokens["strong"]
	if !ok || !strong.StrongHit {
		t.Fatalf("strong token missing or not cold: %+v", strong)
	}
	if len(c.Ods) != 1 || c.Ods["od1"].Name != "ORDER" {
		t.Fatalf("Ods projection wrong: %+v", c.Ods)
	}
	if len(c.Intents) != 1 || c.Intents["i1"].Name != "Order.Quantity" {
		t.Fatalf("Intents projection wrong: %+v", c.Intents)
	}
}

func TestBuildCachedContext_NilLedger(t *testing.T) {
	if BuildCachedContext(nil) != nil {
		t.Fatal("nil ledger must produce nil CachedContext")
	}
}

func TestFormatContextWithLedger_NilAndEmpty(t *testing.T) {
	r := recall.RecallResult{}
	// nil ledger → falls through to plain FormatContext (should not panic).
	got := FormatContextWithLedger(r, []string{"x"}, "q", nil, 1)
	if got == "" {
		t.Fatal("nil ledger should still produce some output")
	}
	// Empty ledger → same.
	got2 := FormatContextWithLedger(r, []string{"x"}, "q", New(), 1)
	if got2 == "" {
		t.Fatal("empty ledger should still produce some output")
	}
}

func TestFormatContextWithLedger_OrphanFooter(t *testing.T) {
	// Ledger has Customer (Od) and Order.Quantity (Intent) from earlier;
	// this turn's result has neither → both should appear in orphan
	// footer.
	l := New()
	l.TurnCount = 2
	l.Ods["od-customer"] = &LedgerOd{
		OdBlock:      recall.OdBlock{OdID: "od-customer", Name: "CUSTOMER", Kind: "dim", Description: "customer master data"},
		LoadedInTurn: 1,
		LoadMethod:   "recall-hit",
	}
	l.Intents["i-qty"] = &LedgerIntent{
		MetricIntent:    recall.MetricIntent{IntentID: "i-qty", Name: "Order.Quantity", CanonicalMetric: "sum(Order_Quantity)", MatchedTokens: []string{"订单"}},
		FirstSeenInTurn: 1,
	}
	// Turn 2 result — fresh but about something unrelated.
	r := recall.RecallResult{
		OdBlocks: []recall.OdBlock{{OdID: "od-product", Name: "PRODUCT", Kind: "dim"}},
	}
	out := FormatContextWithLedger(r, []string{"产品"}, "产品种类", l, 2)

	if !strings.Contains(out, "🧠 线程记忆") {
		t.Fatal("missing thread-memory header")
	}
	if !strings.Contains(out, "📚 线程其它记忆") {
		t.Fatal("missing orphan footer")
	}
	if !strings.Contains(out, "CUSTOMER") {
		t.Fatal("CUSTOMER Od should appear in orphan footer")
	}
	if !strings.Contains(out, "Order.Quantity") {
		t.Fatal("Order.Quantity intent should appear in orphan footer")
	}
}

func TestFormatContextWithLedger_NoOrphans(t *testing.T) {
	// Every ledger entry is covered by this turn's result → no
	// orphan footer rendered.
	l := New()
	l.Ods["od-order"] = &LedgerOd{
		OdBlock:      recall.OdBlock{OdID: "od-order", Name: "ORDER", Kind: "fact"},
		LoadedInTurn: 1,
	}
	r := recall.RecallResult{
		OdBlocks: []recall.OdBlock{{OdID: "od-order", Name: "ORDER", Kind: "fact"}},
	}
	out := FormatContextWithLedger(r, []string{"订单"}, "订单总数", l, 2)
	if strings.Contains(out, "📚 线程其它记忆") {
		t.Fatal("no orphans expected; footer should be suppressed")
	}
}

// --- DB-backed tests (require DATABASE_URL) ---------------------------

// ledgerTestDB opens a Postgres connection from DATABASE_URL. Skips the
// test if unset — mirrors the pattern in smartquery_regression_test.go.
func ledgerTestDB(t *testing.T) *sql.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping: %v", err)
	}
	return db
}

// ledgerTestThread creates a disposable thread row and returns its id.
// Caller should defer cleanup via t.Cleanup(db.Exec DELETE).
func ledgerTestThread(t *testing.T, db *sql.DB) string {
	var id string
	// Use any existing project — ledger tests don't need specific project
	// content, only a legal thread row.
	var projectID string
	if err := db.QueryRow(`SELECT id::text FROM project ORDER BY created_at LIMIT 1`).Scan(&projectID); err != nil {
		t.Skipf("no project row available for test: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO ont_agent_thread (project_id, title, agent_type)
		VALUES ($1, 'ledger-test', 'lakehouse') RETURNING id::text`, projectID).Scan(&id); err != nil {
		t.Fatalf("create test thread: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM ont_agent_thread WHERE id = $1`, id)
	})
	return id
}

func TestLoad_MissingThread(t *testing.T) {
	db := ledgerTestDB(t)
	l, err := Load(context.Background(), db, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("Load on missing thread should not error: %v", err)
	}
	if !l.IsEmpty() {
		t.Fatal("Load on missing thread should return empty ledger")
	}
}

func TestLoad_EmptyThread(t *testing.T) {
	db := ledgerTestDB(t)
	threadID := ledgerTestThread(t, db)

	l, err := Load(context.Background(), db, threadID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !l.IsEmpty() {
		t.Fatalf("fresh thread should have empty ledger, got %+v", l)
	}
	if l.Version != 0 {
		t.Fatalf("fresh ledger Version = %d, want 0", l.Version)
	}
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	db := ledgerTestDB(t)
	threadID := ledgerTestThread(t, db)

	l := New()
	l.TurnCount = 1
	l.Ods["od1"] = &LedgerOd{
		OdBlock:      recall.OdBlock{OdID: "od1", Name: "ORDER"},
		LoadedInTurn: 1,
		LoadMethod:   "lookup",
	}
	l.Tokens["订单"] = &LedgerToken{FirstSeen: 1, LastSeen: 1, StrongHit: true, MatchedOds: []string{"od1"}}

	if err := Save(db, threadID, l, 0); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if l.Version != 1 {
		t.Fatalf("Save should bump Version to 1, got %d", l.Version)
	}

	got, err := Load(context.Background(), db, threadID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != 1 || got.TurnCount != 1 {
		t.Fatalf("roundtrip lost top-level: %+v", got)
	}
	if od, ok := got.Ods["od1"]; !ok || od.Name != "ORDER" || od.LoadMethod != "lookup" {
		t.Fatalf("roundtrip lost Od: %+v", od)
	}
	if tok, ok := got.Tokens["订单"]; !ok || !tok.StrongHit {
		t.Fatalf("roundtrip lost Token: %+v", tok)
	}
}

func TestSave_VersionConflict(t *testing.T) {
	db := ledgerTestDB(t)
	threadID := ledgerTestThread(t, db)

	// First write succeeds (oldVersion=0, no prior ledger).
	l := New()
	l.TurnCount = 1
	if err := Save(db, threadID, l, 0); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Second write with oldVersion=0 must now fail — the DB has version=1.
	stale := New()
	stale.TurnCount = 2
	if err := Save(db, threadID, stale, 0); err != ErrVersionConflict {
		t.Fatalf("expected ErrVersionConflict, got %v", err)
	}
	// Correct oldVersion (1) succeeds.
	if err := Save(db, threadID, stale, 1); err != nil {
		t.Fatalf("retry with correct version: %v", err)
	}
}

// Note: the former TestLoad_ProjectVersionMismatch was deleted together
// with the version concept. Stale-Od cleanup is now the lookup tool's
// responsibility (it returns "not found" when an Od UUID is no longer
// in the catalog), so the ledger no longer needs a drift check.

// --- Merge tests ------------------------------------------------------

func makeRecallResult() recall.RecallResult {
	return recall.RecallResult{
		OdBlocks: []recall.OdBlock{
			{
				OdID: "od1", Name: "ORDER", Kind: "fact",
				Description:  "order table",
				AllPropNames: []string{"Order_Quantity", "Order_Type"},
				MatchedProps: []recall.PropertyMatch{
					{
						PropertyID: "p1", Name: "Order_Quantity", DisplayName: "Order_Quantity",
						Keywords: []recall.KeywordHit{
							{Keyword: "订单量", Tier: "EXACT", MatchedToken: "订单量", MappedTable: "ORDER", MappedField: "Order_Quantity"},
						},
					},
				},
				MatchedVia: []string{"property"},
			},
		},
		MetricIntents: []recall.MetricIntent{
			{IntentID: "i1", Name: "Order.Quantity", CanonicalMetric: "sum(Order_Quantity)",
				MatchedTokens: []string{"订单"}, Tier: "EXACT"},
		},
		TokenDetails: map[string][]recall.KeywordHit{
			"订单量": {{Keyword: "订单量", Tier: "EXACT", MatchedToken: "订单量", MappedTable: "ORDER", MappedField: "Order_Quantity"}},
			"订单":  {{Keyword: "订单", Tier: "EXACT", MatchedToken: "订单"}},
		},
	}
}

func TestMerge_FirstTurn(t *testing.T) {
	l := New()
	r := makeRecallResult()
	d := l.MergeRecallResult(r, 1)

	if len(d.NewOdIDs) != 1 || d.NewOdIDs[0] != "od1" {
		t.Fatalf("expected new od1, got %+v", d)
	}
	if len(d.NewIntentIDs) != 1 || d.NewIntentIDs[0] != "i1" {
		t.Fatalf("expected new intent i1, got %+v", d)
	}
	if l.Ods["od1"].LoadedInTurn != 1 || l.Ods["od1"].LoadMethod != "recall-hit" {
		t.Fatalf("Od metadata wrong: %+v", l.Ods["od1"])
	}
	tok := l.Tokens["订单量"]
	if tok == nil || !tok.StrongHit {
		t.Fatalf("订单量 should be StrongHit, got %+v", tok)
	}
	if len(tok.MatchedOds) != 1 || tok.MatchedOds[0] != "od1" {
		t.Fatalf("订单量 should back-ref od1, got %+v", tok)
	}
	if tok2 := l.Tokens["订单"]; tok2 == nil || !tok2.StrongHit ||
		len(tok2.MatchedIntents) != 1 || tok2.MatchedIntents[0] != "i1" {
		t.Fatalf("订单 should back-ref intent, got %+v", tok2)
	}
}

func TestMerge_Idempotent(t *testing.T) {
	l := New()
	r := makeRecallResult()
	l.MergeRecallResult(r, 1)
	d2 := l.MergeRecallResult(r, 2)

	// Second merge is a full repeat — nothing NEW.
	if !d2.IsEmpty() {
		t.Fatalf("second merge should produce no new entries, got %+v", d2)
	}
	// Counts stay singleton.
	if len(l.Ods) != 1 || len(l.Intents) != 1 {
		t.Fatalf("merge not idempotent: ods=%d intents=%d", len(l.Ods), len(l.Intents))
	}
	// LoadedInTurn stays at first turn (stronger guarantee for FSM).
	if l.Ods["od1"].LoadedInTurn != 1 {
		t.Fatalf("LoadedInTurn shouldn't change on re-merge, got %d", l.Ods["od1"].LoadedInTurn)
	}
	// Token LastSeen DOES update.
	if l.Tokens["订单量"].LastSeen != 2 {
		t.Fatalf("LastSeen should bump to turn 2, got %d", l.Tokens["订单量"].LastSeen)
	}
}

func TestMerge_StrengthOverride(t *testing.T) {
	l := New()
	// Seed with recall-fallback (weak).
	l.MergeRecallResult(recall.RecallResult{
		DirectOds: []recall.OdBlock{
			{OdID: "od1", Name: "ORDER", Kind: "fact"},
		},
	}, 1)
	if l.Ods["od1"].LoadMethod != "recall-fallback" {
		t.Fatalf("seed: %+v", l.Ods["od1"].LoadMethod)
	}

	// Upgrade to lookup (strongest).
	l.MergeLookupOd(recall.OdBlock{
		OdID: "od1", Name: "ORDER", Kind: "fact",
		AllPropNames: []string{"Order_Quantity", "Order_Type"},
		Description:  "refreshed",
	}, 3)

	if l.Ods["od1"].LoadMethod != "lookup" {
		t.Fatalf("expected lookup override, got %q", l.Ods["od1"].LoadMethod)
	}
	if l.Ods["od1"].Description != "refreshed" {
		t.Fatalf("description should refresh on upgrade, got %q", l.Ods["od1"].Description)
	}
	if len(l.Ods["od1"].AllPropNames) != 2 {
		t.Fatalf("AllPropNames should refresh, got %v", l.Ods["od1"].AllPropNames)
	}
}

func TestMerge_FuzzyDoesNotPromoteStrong(t *testing.T) {
	l := New()
	r := recall.RecallResult{
		TokenDetails: map[string][]recall.KeywordHit{
			"bla": {{Keyword: "blah", Tier: "FUZZY", MatchedToken: "bla"}},
		},
	}
	l.MergeRecallResult(r, 1)
	if l.Tokens["bla"].StrongHit {
		t.Fatal("FUZZY-only token must not be StrongHit")
	}
}

func TestSaveWithRetry_Recovers(t *testing.T) {
	db := ledgerTestDB(t)
	threadID := ledgerTestThread(t, db)

	// Seed initial ledger at version 1.
	initial := New()
	initial.TurnCount = 1
	if err := Save(db, threadID, initial, 0); err != nil {
		t.Fatal(err)
	}

	// Caller holds a stale ledger (oldVersion=0) and expects SaveWithRetry
	// to reload + remerge and succeed.
	stale := New()
	stale.TurnCount = 99

	callCount := 0
	err := SaveWithRetry(context.Background(), db, threadID, stale, 0, func(fresh *Ledger) *Ledger {
		callCount++
		fresh.TurnCount = 99
		return fresh
	}, 2)
	if err != nil {
		t.Fatalf("SaveWithRetry: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("reapply should be called once, got %d", callCount)
	}
	got, _ := Load(context.Background(), db, threadID)
	if got.TurnCount != 99 {
		t.Fatalf("reapply result not persisted: TurnCount=%d", got.TurnCount)
	}
	if got.Version != 2 {
		t.Fatalf("version should be 2 after retry, got %d", got.Version)
	}
}
