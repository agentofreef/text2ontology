package recall

import "testing"

func TestCachedContext_IsCold(t *testing.T) {
	var nilCtx *CachedContext
	if nilCtx.IsCold("foo") {
		t.Fatal("nil cached context should never report cold")
	}
	empty := &CachedContext{}
	if empty.IsCold("foo") {
		t.Fatal("empty tokens map should report no cold token")
	}
	c := &CachedContext{
		Tokens: map[string]CachedToken{
			"strong": {StrongHit: true},
			"weak":   {StrongHit: false},
		},
	}
	if !c.IsCold("strong") {
		t.Fatal("StrongHit token should be cold")
	}
	if c.IsCold("weak") {
		t.Fatal("StrongHit=false token must stay hot")
	}
	if c.IsCold("missing") {
		t.Fatal("unknown token must stay hot")
	}
}

func TestPartitionTokens(t *testing.T) {
	c := &CachedContext{
		Tokens: map[string]CachedToken{
			"订单":  {StrongHit: true},
			"客户":  {StrongHit: true},
			"弱匹配": {StrongHit: false},
		},
	}
	hot, cold := partitionTokens([]string{"订单", "早鸟", "客户", "  ", "弱匹配", ""}, c)
	if len(cold) != 2 || cold[0] != "订单" || cold[1] != "客户" {
		t.Fatalf("cold = %v, expected [订单 客户]", cold)
	}
	if len(hot) != 2 || hot[0] != "早鸟" || hot[1] != "弱匹配" {
		t.Fatalf("hot = %v, expected [早鸟 弱匹配]", hot)
	}
}

func TestSpliceColdTokens_DedupAndPseudoHit(t *testing.T) {
	cached := &CachedContext{
		Tokens: map[string]CachedToken{
			"订单": {StrongHit: true, MatchedOdIDs: []string{"od1"}, MatchedIntentIDs: []string{"i1"}},
		},
		Ods: map[string]OdBlock{
			"od1": {OdID: "od1", Name: "ORDER"},
		},
		Intents: map[string]MetricIntent{
			"i1": {IntentID: "i1", Name: "Order.Quantity"},
		},
		OkEntries: map[string]OkEntry{
			"ok1": {ID: "ok1", Title: "OrderPlaybook"},
		},
	}
	r := RecallResult{
		TokenDetails: map[string][]KeywordHit{},
		OdBlocks:     []OdBlock{{OdID: "od2", Name: "CUSTOMER"}},
	}
	spliceColdTokens(&r, []string{"订单"}, cached)

	if len(r.OdBlocks) != 2 {
		t.Fatalf("expected 2 Ods after splice (CUSTOMER + ORDER), got %d", len(r.OdBlocks))
	}
	if len(r.MetricIntents) != 1 || r.MetricIntents[0].IntentID != "i1" {
		t.Fatalf("expected 1 intent i1, got %+v", r.MetricIntents)
	}
	if len(r.OkEntries) != 1 || r.OkEntries[0].ID != "ok1" {
		t.Fatalf("expected 1 Ok entry splice, got %+v", r.OkEntries)
	}
	// Pseudo-hit tag so operator debug can see cache reuse.
	hits := r.TokenDetails["订单"]
	if len(hits) != 1 || hits[0].Tier != "CACHED" {
		t.Fatalf("expected CACHED pseudo-hit, got %+v", hits)
	}

	// Second splice is idempotent.
	spliceColdTokens(&r, []string{"订单"}, cached)
	if len(r.OdBlocks) != 2 || len(r.MetricIntents) != 1 || len(r.OkEntries) != 1 {
		t.Fatalf("second splice should be idempotent, got ods=%d intents=%d ok=%d",
			len(r.OdBlocks), len(r.MetricIntents), len(r.OkEntries))
	}
}

func TestSpliceColdTokens_SkipsAlreadyPresent(t *testing.T) {
	cached := &CachedContext{
		Tokens: map[string]CachedToken{"订单": {StrongHit: true, MatchedOdIDs: []string{"od1"}}},
		Ods:    map[string]OdBlock{"od1": {OdID: "od1", Name: "ORDER_FROM_CACHE"}},
	}
	r := RecallResult{
		TokenDetails: map[string][]KeywordHit{},
		// Already present via fresh recall with different (fresher) content.
		OdBlocks: []OdBlock{{OdID: "od1", Name: "ORDER_FROM_FRESH"}},
	}
	spliceColdTokens(&r, []string{"订单"}, cached)
	if len(r.OdBlocks) != 1 || r.OdBlocks[0].Name != "ORDER_FROM_FRESH" {
		t.Fatalf("fresh Od should win over cached, got %+v", r.OdBlocks)
	}
}
