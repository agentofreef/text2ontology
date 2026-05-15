package builder_ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- TestNew_Empty -----------------------------------------------------------

func TestNew_Empty(t *testing.T) {
	l := New()
	if !l.IsEmpty() {
		t.Fatal("New() should be empty")
	}
	if l.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", l.SchemaVersion, SchemaVersion)
	}
	// Maps must be non-nil so callers can write without nil check.
	l.TablesExplored["tbl"] = &TableExplored{Table: "tbl"}
	if l.IsEmpty() {
		t.Fatal("ledger with a TablesExplored entry should not be empty")
	}
}

// --- TestJSONRoundTrip -------------------------------------------------------

func TestJSONRoundTrip(t *testing.T) {
	orig := New()
	orig.TurnCount = 5

	// 1 TableExplored
	orig.TablesExplored["order_header"] = &TableExplored{
		Table:       "order_header",
		RowCount:    12345,
		ColumnCount: 7,
		Hypotheses:  []string{"fact table", "order grain"},
		KeyColumns: []KeyColumn{
			{Name: "order_id", DataType: "int8", Cardinality: 12345, UniqueRatio: 1.0, IsLikelyPK: true},
			{Name: "product_id", DataType: "int4", Cardinality: 500, IsLikelyFK: true},
		},
		LowCardinalityCols: []ColumnEnum{
			{Name: "status", Cardinality: 3, ValueDistribution: []ValueCount{
				{Value: "open", Count: 100, Pct: 50.0},
			}},
		},
		ExploredInTurn: 2,
	}

	// 1 RelationshipAnalyzed
	orig.RelationshipsAnalyzed["order_header|product"] = &RelationshipAnalyzed{
		Tables: []string{"order_header", "product"},
		TopCandidates: []RelationshipCandidate{
			{FromTable: "order_header", FromColumn: "product_id", ToTable: "product", ToColumn: "id", Confidence: 0.95},
			{FromTable: "order_header", FromColumn: "region_id", ToTable: "region", ToColumn: "id", Confidence: 0.80},
		},
		AnalyzedInTurn:  2,
		TotalCandidates: 4,
	}

	// 1 SearchKeyword
	orig.SearchKeywords["X11:order_header"] = &SearchKeyword{
		Keyword: "X11",
		InTable: "order_header",
		Matches: []SearchMatchedCol{
			{Column: "product_code", TotalOccurrences: 42, SampleValueCount: 3},
			{Column: "sku", TotalOccurrences: 7, SampleValueCount: 1},
		},
		SearchedInTurn: 3,
	}

	// 1 LakehouseTables index
	orig.LakehouseTables = &TablesIndex{
		Tables: []TableSummary{
			{Name: "order_header", Type: "table", EstimatedRows: 12345},
			{Name: "product", Type: "table", EstimatedRows: 500},
			{Name: "region", Type: "view", EstimatedRows: 20},
		},
		LoadedInTurn: 1,
	}

	// 1 DraftProposed (od type, status pending)
	orig.DraftsProposed["draft-od-1"] = &DraftProposed{
		ID:             "draft-od-1",
		Type:           "od",
		Name:           "Order",
		Status:         "pending",
		Kind:           "fact",
		Summary:        "Order (fact, 5 props)",
		ProposedInTurn: 3,
	}

	// 1 OntologySnapshot with 1 OD + 1 Intent
	orig.OntologySnapshot = &OntologySnapshot{
		Ods: []OdSummary{
			{ID: "od-uuid-1", Name: "Product", Kind: "dim", PropCount: 4, Mark: true},
		},
		Intents: []IntentSummary{
			{ID: "intent-uuid-1", Name: "Order.Quantity", ObjectName: "Order", CanonicalMetric: "sum(qty)", Mark: true},
		},
		SnapshottedInTurn: 2,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back BuilderLedger
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	back.EnsureMaps()

	if back.TurnCount != 5 {
		t.Fatalf("TurnCount dropped: got %d", back.TurnCount)
	}
	if back.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion dropped: got %d", back.SchemaVersion)
	}

	// TableExplored
	te, ok := back.TablesExplored["order_header"]
	if !ok {
		t.Fatal("order_header TableExplored missing after roundtrip")
	}
	if te.RowCount != 12345 || te.ColumnCount != 7 || te.ExploredInTurn != 2 {
		t.Fatalf("TableExplored fields wrong: %+v", te)
	}
	if len(te.KeyColumns) != 2 {
		t.Fatalf("KeyColumns len = %d, want 2", len(te.KeyColumns))
	}
	if len(te.LowCardinalityCols) != 1 {
		t.Fatalf("LowCardinalityCols len = %d, want 1", len(te.LowCardinalityCols))
	}

	// RelationshipAnalyzed
	ra, ok := back.RelationshipsAnalyzed["order_header|product"]
	if !ok {
		t.Fatal("RelationshipAnalyzed entry missing after roundtrip")
	}
	if len(ra.TopCandidates) != 2 || ra.TotalCandidates != 4 {
		t.Fatalf("RelationshipAnalyzed wrong: %+v", ra)
	}

	// SearchKeyword
	sk, ok := back.SearchKeywords["X11:order_header"]
	if !ok {
		t.Fatal("SearchKeyword missing after roundtrip")
	}
	if sk.Keyword != "X11" || sk.InTable != "order_header" || len(sk.Matches) != 2 {
		t.Fatalf("SearchKeyword fields wrong: %+v", sk)
	}

	// LakehouseTables
	if back.LakehouseTables == nil || len(back.LakehouseTables.Tables) != 3 {
		t.Fatal("LakehouseTables not preserved across roundtrip")
	}

	// DraftProposed
	dp, ok := back.DraftsProposed["draft-od-1"]
	if !ok {
		t.Fatal("DraftProposed missing after roundtrip")
	}
	if dp.Type != "od" || dp.Status != "pending" || dp.ProposedInTurn != 3 {
		t.Fatalf("DraftProposed fields wrong: %+v", dp)
	}

	// OntologySnapshot
	if back.OntologySnapshot == nil {
		t.Fatal("OntologySnapshot missing after roundtrip")
	}
	if len(back.OntologySnapshot.Ods) != 1 || back.OntologySnapshot.Ods[0].Name != "Product" {
		t.Fatalf("OntologySnapshot.Ods wrong: %+v", back.OntologySnapshot.Ods)
	}
	if len(back.OntologySnapshot.Intents) != 1 || back.OntologySnapshot.Intents[0].Name != "Order.Quantity" {
		t.Fatalf("OntologySnapshot.Intents wrong: %+v", back.OntologySnapshot.Intents)
	}
}

// --- TestMergeAnalyzeTable_Idempotent ----------------------------------------

func TestMergeAnalyzeTable_Idempotent(t *testing.T) {
	l := New()

	args := M{"tableName": "orders"}
	result := M{
		"table":            "orders",
		"rowCount":         float64(100),
		"totalColumnCount": float64(5),
		"hypotheses":       []interface{}{"fact"},
	}

	l.MergeAnalyzeTable(args, result, 1)
	l.MergeAnalyzeTable(args, result, 2) // second call same args

	if len(l.TablesExplored) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(l.TablesExplored))
	}
	// The second call replaces the entry, so exploredInTurn should be the LATER turn.
	te := l.TablesExplored["orders"]
	if te.ExploredInTurn != 2 {
		t.Fatalf("exploredInTurn = %d, want 2 (later call should refresh)", te.ExploredInTurn)
	}
}

// --- TestMergeAnalyzeTable_RefreshesEntry ------------------------------------

func TestMergeAnalyzeTable_RefreshesEntry(t *testing.T) {
	l := New()

	args := M{"tableName": "orders"}

	result1 := M{
		"table":            "orders",
		"rowCount":         float64(100),
		"totalColumnCount": float64(15),
	}
	l.MergeAnalyzeTable(args, result1, 1)

	result2 := M{
		"table":            "orders",
		"rowCount":         float64(100),
		"totalColumnCount": float64(18),
	}
	l.MergeAnalyzeTable(args, result2, 2)

	te := l.TablesExplored["orders"]
	if te.ColumnCount != 18 {
		t.Fatalf("columnCount = %d, want 18 (refresh semantics)", te.ColumnCount)
	}
}

// --- TestMergeAnalyzeRelationships_Keying ------------------------------------

func TestMergeAnalyzeRelationships_Keying(t *testing.T) {
	l := New()

	// First call: tables in ["a","b"] order
	args1 := M{}
	result1 := M{
		"tables":             []interface{}{"a", "b"},
		"candidates":         []interface{}{},
		"totalPairsExamined": float64(2),
	}
	l.MergeAnalyzeRelationships(args1, result1, 1)

	// Second call: tables in ["b","a"] order (reversed)
	args2 := M{}
	result2 := M{
		"tables":             []interface{}{"b", "a"},
		"candidates":         []interface{}{},
		"totalPairsExamined": float64(2),
	}
	l.MergeAnalyzeRelationships(args2, result2, 2)

	// Both should merge into the same key "a|b"
	if len(l.RelationshipsAnalyzed) != 1 {
		t.Fatalf("expected 1 entry (sorted key), got %d entries: %v", len(l.RelationshipsAnalyzed), l.RelationshipsAnalyzed)
	}
	if _, ok := l.RelationshipsAnalyzed["a|b"]; !ok {
		t.Fatalf("expected key 'a|b', got keys: %v", l.RelationshipsAnalyzed)
	}
}

// --- TestMergeAnalyzeRelationships_TopFiveCap --------------------------------

func TestMergeAnalyzeRelationships_TopFiveCap(t *testing.T) {
	l := New()

	// Build 10 candidates with descending confidence
	candidates := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		candidates[i] = M{
			"fromTable":  "a",
			"fromColumn": "col",
			"toTable":    "b",
			"toColumn":   "col",
			"confidence": float64(10 - i), // 10, 9, 8, ..., 1
			"evidence": M{
				"valueOverlap":    float64(0),
				"nameSimilarity":  float64(0),
				"cardinalityHint": "many_to_one",
			},
		}
	}

	args := M{}
	result := M{
		"tables":             []interface{}{"a", "b"},
		"candidates":         candidates,
		"totalPairsExamined": float64(10),
	}
	l.MergeAnalyzeRelationships(args, result, 1)

	ra := l.RelationshipsAnalyzed["a|b"]
	if ra == nil {
		t.Fatal("no entry created")
	}
	if len(ra.TopCandidates) != 5 {
		t.Fatalf("TopCandidates len = %d, want 5 (cap)", len(ra.TopCandidates))
	}
	// Highest confidence should be first
	if ra.TopCandidates[0].Confidence != 10 {
		t.Fatalf("first candidate confidence = %v, want 10", ra.TopCandidates[0].Confidence)
	}
}

// --- TestMergePropose_OdIntentLink -------------------------------------------

func TestMergePropose_OdIntentLink(t *testing.T) {
	l := New()

	// propose_od
	l.MergePropose("propose_od", M{}, M{
		"objectId": "od-uuid-1",
		"name":     "Order",
		"kind":     "fact",
	}, 1)

	// propose_intent
	l.MergePropose("propose_intent", M{"objectId": "od-uuid-1"}, M{
		"intentId":        "intent-uuid-1",
		"name":            "Order.Quantity",
		"canonicalMetric": "sum(qty)",
	}, 1)

	// propose_link
	l.MergePropose("propose_link", M{}, M{
		"linkId":       "link-uuid-1",
		"linkName":     "Order_Product",
		"fromObjectId": "od-uuid-1",
		"toObjectId":   "od-uuid-2",
		"fkColumn":     "product_id",
	}, 1)

	if len(l.DraftsProposed) != 3 {
		t.Fatalf("DraftsProposed len = %d, want 3", len(l.DraftsProposed))
	}

	od := l.DraftsProposed["od-uuid-1"]
	if od == nil || od.Type != "od" || od.Status != "pending" {
		t.Fatalf("od draft wrong: %+v", od)
	}
	intent := l.DraftsProposed["intent-uuid-1"]
	if intent == nil || intent.Type != "intent" || intent.Status != "pending" {
		t.Fatalf("intent draft wrong: %+v", intent)
	}
	link := l.DraftsProposed["link-uuid-1"]
	if link == nil || link.Type != "link" || link.Status != "pending" {
		t.Fatalf("link draft wrong: %+v", link)
	}
}

// --- TestMergeUpdate_TouchesExistingDraft ------------------------------------

func TestMergeUpdate_TouchesExistingDraft(t *testing.T) {
	l := New()

	// Propose first
	l.MergePropose("propose_od", M{}, M{
		"objectId": "od-uuid-1",
		"name":     "Order",
		"kind":     "fact",
	}, 1)

	draft := l.DraftsProposed["od-uuid-1"]
	if draft == nil {
		t.Fatal("draft not created")
	}
	origUpdatedTurn := draft.LastUpdatedInTurn // should be 1

	// Update at turn 3
	l.MergeUpdate("update_od", M{
		"objectId": "od-uuid-1",
		"edits":    M{"name": "OrderHeader"},
	}, M{
		"objectId": "od-uuid-1",
	}, 3)

	if draft.LastUpdatedInTurn != 3 {
		t.Fatalf("LastUpdatedInTurn = %d, want 3 (was %d)", draft.LastUpdatedInTurn, origUpdatedTurn)
	}
	if draft.Status != "pending" {
		t.Fatalf("Status = %s, want pending (update must not change status)", draft.Status)
	}
	if draft.Name != "OrderHeader" {
		t.Fatalf("Name = %s, want OrderHeader", draft.Name)
	}
}

// --- TestMergeDelete_FlipsStatus ---------------------------------------------

func TestMergeDelete_FlipsStatus(t *testing.T) {
	l := New()

	l.MergePropose("propose_od", M{}, M{
		"objectId": "od-uuid-del",
		"name":     "ToDelete",
		"kind":     "dim",
	}, 1)

	l.MergeDelete("delete_od", M{"objectId": "od-uuid-del"}, M{
		"objectId": "od-uuid-del",
	}, 2)

	draft := l.DraftsProposed["od-uuid-del"]
	if draft == nil {
		t.Fatal("draft removed from map — should remain with status=deleted")
	}
	if draft.Status != "deleted" {
		t.Fatalf("Status = %s, want deleted", draft.Status)
	}
}

// --- TestMarkActivated -------------------------------------------------------

func TestMarkActivated(t *testing.T) {
	l := New()

	l.MergePropose("propose_od", M{}, M{
		"objectId": "od-uuid-act",
		"name":     "Product",
		"kind":     "dim",
	}, 1)

	l.MarkActivated("od-uuid-act", 3)

	draft := l.DraftsProposed["od-uuid-act"]
	if draft == nil {
		t.Fatal("draft missing after MarkActivated")
	}
	if draft.Status != "activated" {
		t.Fatalf("Status = %s, want activated", draft.Status)
	}
	if draft.LastUpdatedInTurn != 3 {
		t.Fatalf("LastUpdatedInTurn = %d, want 3", draft.LastUpdatedInTurn)
	}
}

// --- TestMergeQueryData_KeywordSearchOnly ------------------------------------

func TestMergeQueryData_KeywordSearchOnly(t *testing.T) {
	l := New()

	// SQL mode — should NOT add entry
	l.MergeQueryData(
		M{"mode": "sql"},
		M{"mode": "sql", "keyword": "X11", "table": "orders"},
		1,
	)
	if len(l.SearchKeywords) != 0 {
		t.Fatalf("SQL mode should not populate SearchKeywords, got %d entries", len(l.SearchKeywords))
	}

	// keyword_search mode — should add entry
	l.MergeQueryData(
		M{"searchKeyword": "X11", "inTable": "orders"},
		M{
			"mode":    "keyword_search",
			"keyword": "X11",
			"table":   "orders",
			"matches": []interface{}{
				M{"column": "sku", "totalOccurrences": float64(5)},
			},
		},
		2,
	)
	if len(l.SearchKeywords) != 1 {
		t.Fatalf("keyword_search mode should add 1 entry, got %d", len(l.SearchKeywords))
	}
}

// --- TestMergeListOds_RefreshesSnapshot --------------------------------------

func TestMergeListOds_RefreshesSnapshot(t *testing.T) {
	l := New()

	// First call: 2 ODs
	l.MergeListOds(M{
		"ods": []interface{}{
			M{"id": "od-1", "name": "Order", "kind": "fact", "mark": true},
			M{"id": "od-2", "name": "Product", "kind": "dim", "mark": true},
		},
	}, 1)

	if l.OntologySnapshot == nil || len(l.OntologySnapshot.Ods) != 2 {
		t.Fatalf("expected 2 ODs after first call, got %+v", l.OntologySnapshot)
	}

	// Second call: different (1 OD) — should REPLACE, not merge
	l.MergeListOds(M{
		"ods": []interface{}{
			M{"id": "od-3", "name": "Region", "kind": "dim", "mark": true},
		},
	}, 3)

	if len(l.OntologySnapshot.Ods) != 1 {
		t.Fatalf("expected 1 OD after second call (full replace), got %d", len(l.OntologySnapshot.Ods))
	}
	if l.OntologySnapshot.Ods[0].Name != "Region" {
		t.Fatalf("expected Region, got %s", l.OntologySnapshot.Ods[0].Name)
	}
	if l.OntologySnapshot.SnapshottedInTurn != 3 {
		t.Fatalf("SnapshottedInTurn = %d, want 3", l.OntologySnapshot.SnapshottedInTurn)
	}
}

// --- TestFormatPrefix_Empty --------------------------------------------------

func TestFormatPrefix_Empty(t *testing.T) {
	l := New()
	out := l.FormatPrefix()
	// Empty ledger should return empty string (IsEmpty == true path)
	if out != "" {
		t.Fatalf("empty ledger FormatPrefix should return empty string, got %q", out)
	}
}

// --- TestFormatPrefix_PopulatedSections --------------------------------------

func TestFormatPrefix_PopulatedSections(t *testing.T) {
	l := New()

	// 2 tables
	l.TablesExplored["order_header"] = &TableExplored{
		Table:          "order_header",
		RowCount:       5000,
		ColumnCount:    8,
		ExploredInTurn: 1,
		Hypotheses:     []string{},
		KeyColumns:     []KeyColumn{},
		LowCardinalityCols: []ColumnEnum{},
	}
	l.TablesExplored["product"] = &TableExplored{
		Table:          "product",
		RowCount:       200,
		ColumnCount:    5,
		ExploredInTurn: 1,
		Hypotheses:     []string{},
		KeyColumns:     []KeyColumn{},
		LowCardinalityCols: []ColumnEnum{},
	}

	// 1 relationship with candidates that have confidence values
	l.RelationshipsAnalyzed["order_header|product"] = &RelationshipAnalyzed{
		Tables: []string{"order_header", "product"},
		TopCandidates: []RelationshipCandidate{
			{FromTable: "order_header", FromColumn: "product_id",
				ToTable: "product", ToColumn: "id",
				Confidence: 0.93, ValueOverlap: 0.88, Cardinality: "many_to_one"},
		},
		TotalCandidates: 1,
		AnalyzedInTurn:  1,
	}

	// 1 keyword
	l.SearchKeywords["X11:order_header"] = &SearchKeyword{
		Keyword: "X11", InTable: "order_header",
		Matches: []SearchMatchedCol{{Column: "sku", TotalOccurrences: 5}},
		SearchedInTurn: 1,
	}

	// 1 draft
	l.DraftsProposed["draft-1"] = &DraftProposed{
		ID: "draft-1", Type: "od", Name: "OrderDraft",
		Status: "pending", Kind: "fact", Summary: "OrderDraft (fact, 3 props)",
		ProposedInTurn: 1,
	}

	// OntologySnapshot
	l.OntologySnapshot = &OntologySnapshot{
		Ods: []OdSummary{
			{ID: "od-1", Name: "ExistingOd", Kind: "dim", Mark: true},
		},
		SnapshottedInTurn: 1,
	}

	out := l.FormatPrefix()

	checks := []struct {
		label   string
		contain string
	}{
		{"header", "📚 本会话已知信息"},
		{"table1", "order_header"},
		{"table2", "product"},
		{"draft name", "OrderDraft"},
		{"confidence value", "0.93"},
	}
	for _, c := range checks {
		if !strings.Contains(out, c.contain) {
			t.Errorf("FormatPrefix missing %s: expected to contain %q\nOutput:\n%s", c.label, c.contain, out)
		}
	}
}

// --- TestFormatPrefix_CapsLongList -------------------------------------------

func TestFormatPrefix_CapsLongList(t *testing.T) {
	l := New()

	// Populate 20 tables
	for i := 0; i < 20; i++ {
		name := strings.Repeat("t", 3) + string(rune('A'+i))
		l.TablesExplored[name] = &TableExplored{
			Table:          name,
			RowCount:       100,
			ColumnCount:    3,
			ExploredInTurn: 1,
			Hypotheses:     []string{},
			KeyColumns:     []KeyColumn{},
			LowCardinalityCols: []ColumnEnum{},
		}
	}

	out := l.FormatPrefix()

	// render.go caps at 10 and writes "还有 N 张表已勘探" or similar
	if !strings.Contains(out, "还有") {
		t.Errorf("FormatPrefix with 20 tables should contain truncation indicator (还有...). Output:\n%s", out)
	}
}

// --- TestEnsureMaps_OnUnmarshalled -------------------------------------------

func TestEnsureMaps_OnUnmarshalled(t *testing.T) {
	// Marshal an empty ledger (no maps populated) then unmarshal to zero value.
	orig := New()
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back BuilderLedger
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	back.EnsureMaps()

	// All maps must be non-nil after EnsureMaps
	if back.TablesExplored == nil {
		t.Fatal("TablesExplored is nil after EnsureMaps")
	}
	if back.RelationshipsAnalyzed == nil {
		t.Fatal("RelationshipsAnalyzed is nil after EnsureMaps")
	}
	if back.SearchKeywords == nil {
		t.Fatal("SearchKeywords is nil after EnsureMaps")
	}
	if back.DraftsProposed == nil {
		t.Fatal("DraftsProposed is nil after EnsureMaps")
	}

	// Writes should not panic.
	back.TablesExplored["x"] = &TableExplored{}
	back.DraftsProposed["y"] = &DraftProposed{}
}
