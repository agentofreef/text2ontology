package recall

import "testing"

// fixtureLineage covers the production blast scenario for IPS5 15IWC11:
//
//	PRODUCT.PRODUCT_OFFERING_SHORT_NAME (root, prop=prod_offering)
//	    │
//	    │ Tier 1: property-level join_key edge (cross-name canonical mapping)
//	    ▼
//	MTM.CODE_NAME (prop=mtm_code)
//	    │
//	    │ Tier 2: Od-level join_key edge on a DIFFERENT column (MODEL_NUMBER),
//	    │         same-name CODE_NAME column lives on both Ods → fold via name.
//	    ▼
//	MTM_CHAR_VALUE.CODE_NAME (prop=mtm_char_code)
//
// SUPPLIER.NAME and CUSTOMER.NAME are isolated roots used to prove that two
// disjoint lineages stay as two distinct hits after dedup.
func fixtureLineage() *propertyLineage {
	return &propertyLineage{
		// Tier 1 edges — property-level cross-name canonical mappings.
		parent: map[string]lineageParent{
			"mtm_code": {
				PropID: "prod_offering", OdID: "od_product", OdName: "PRODUCT",
				PropName: "PRODUCT_OFFERING_SHORT_NAME",
			},
		},
		// Tier 2 edges — Od-level join_key edges (any column).
		parentOds: map[string][]string{
			"od_mtm_char": {"od_mtm"}, // MTM_CHAR_VALUE → MTM via MODEL_NUMBER
			"od_mtm":      {"od_product"},
		},
		// Per-Od property index for Tier 2 same-name lookup.
		propsByOd: map[string]map[string]lineageParent{
			"od_mtm": {
				"code_name": {
					PropID: "mtm_code", OdID: "od_mtm", OdName: "MTM",
					PropName: "CODE_NAME",
				},
				"model_number": {
					PropID: "mtm_model", OdID: "od_mtm", OdName: "MTM",
					PropName: "MODEL_NUMBER",
				},
			},
			"od_product": {
				"product_offering_short_name": {
					PropID: "prod_offering", OdID: "od_product", OdName: "PRODUCT",
					PropName: "PRODUCT_OFFERING_SHORT_NAME",
				},
			},
		},
	}
}

// TestWalkUpstream_PropertyLevelEdge: Tier 1 — explicit cross-name canonical
// mapping. MTM.CODE_NAME walks directly to PRODUCT.PRODUCT_OFFERING_SHORT_NAME
// via the property-level edge.
func TestWalkUpstream_PropertyLevelEdge(t *testing.T) {
	pl := fixtureLineage()
	up, ok := pl.walkUpstream("mtm_code", "CODE_NAME", "od_mtm")
	if !ok {
		t.Fatal("MTM.CODE_NAME must walk up via Tier 1 edge")
	}
	if up.PropID != "prod_offering" || up.OdName != "PRODUCT" {
		t.Fatalf("Tier 1 walk root=(%s,%s); want (prod_offering,PRODUCT)", up.PropID, up.OdName)
	}
}

// TestWalkUpstream_OdLevelSameNameFallback: Tier 2 — Od-level join_key on a
// different column (MTM_CHAR_VALUE → MTM via MODEL_NUMBER), but the same-name
// CODE_NAME column exists on the parent. The walk hops via Tier 2 to
// MTM.CODE_NAME, then chains Tier 1 up to PRODUCT.
//
// This is the production bug: pre-fix, the walk stopped at MTM_CHAR_VALUE
// because no property-level edge exists from CODE_NAME→CODE_NAME, and the
// hit stayed visible alongside PRODUCT.
func TestWalkUpstream_OdLevelSameNameFallback(t *testing.T) {
	pl := fixtureLineage()
	up, ok := pl.walkUpstream("mtm_char_code", "CODE_NAME", "od_mtm_char")
	if !ok {
		t.Fatal("MTM_CHAR_VALUE.CODE_NAME must walk via Tier 2 → Tier 1")
	}
	if up.OdName != "PRODUCT" || up.PropName != "PRODUCT_OFFERING_SHORT_NAME" {
		t.Fatalf("Tier 2 walk root=(%s,%s); want PRODUCT.PRODUCT_OFFERING_SHORT_NAME",
			up.OdName, up.PropName)
	}
}

// TestWalkUpstream_AlreadyRoot: a hit on the canonical Od has no further
// upstream — function returns (zero, false).
func TestWalkUpstream_AlreadyRoot(t *testing.T) {
	pl := fixtureLineage()
	if _, ok := pl.walkUpstream("prod_offering", "PRODUCT_OFFERING_SHORT_NAME", "od_product"); ok {
		t.Fatal("root property must not have an upstream")
	}
}

// TestWalkUpstream_NoSameNameOnParent: Tier 2 fallback finds an Od-level
// parent but the parent has no same-name column → walk stops. The hit stays
// where it was; ambiguity detector handles it downstream.
func TestWalkUpstream_NoSameNameOnParent(t *testing.T) {
	pl := &propertyLineage{
		parent:    map[string]lineageParent{},
		parentOds: map[string][]string{"od_child": {"od_parent"}},
		propsByOd: map[string]map[string]lineageParent{
			"od_parent": {
				// Parent has DIFFERENT columns; child's NAME column has no
				// match → walk stops.
				"other_col": {PropID: "other", OdID: "od_parent", PropName: "OTHER_COL"},
			},
		},
	}
	if _, ok := pl.walkUpstream("child_prop", "NAME", "od_child"); ok {
		t.Fatal("parent without same-name column must stop the walk")
	}
}

// TestWalkUpstream_CycleDoesNotInfinite: an accidental A→B→A cycle at the
// property level must not hang the walk.
func TestWalkUpstream_CycleDoesNotInfinite(t *testing.T) {
	pl := &propertyLineage{
		parent: map[string]lineageParent{
			"A": {PropID: "B", OdID: "odB", PropName: "b"},
			"B": {PropID: "A", OdID: "odA", PropName: "a"}, // cycle
		},
		parentOds: map[string][]string{},
		propsByOd: map[string]map[string]lineageParent{},
	}
	up, ok := pl.walkUpstream("A", "a", "odA")
	if !ok {
		t.Fatal("cycle walk must still surface its first ancestor")
	}
	if up.PropID != "B" {
		t.Fatalf("cycle walk root=%q, want B", up.PropID)
	}
}

// TestWalkUpstream_NilOrEmpty: defensive paths — all return (zero, false)
// without panic.
func TestWalkUpstream_NilOrEmpty(t *testing.T) {
	var nilPl *propertyLineage
	if _, ok := nilPl.walkUpstream("anything", "x", "od1"); ok {
		t.Fatal("nil lineage must return false")
	}
	empty := &propertyLineage{
		parent:    map[string]lineageParent{},
		parentOds: map[string][]string{},
		propsByOd: map[string]map[string]lineageParent{},
	}
	if _, ok := empty.walkUpstream("anything", "x", "od1"); ok {
		t.Fatal("empty lineage must return false")
	}
	pl := fixtureLineage()
	if _, ok := pl.walkUpstream("", "x", "od1"); ok {
		t.Fatal("empty propID must return false")
	}
}

// TestNormalizeValueAliasHits_CollapsesAllThreeIPS5Variants: the production
// scenario — token "IPS5 15IWC11" EXACT-hits PRODUCT, MTM, AND MTM_CHAR_VALUE.
// After normalisation only PRODUCT survives, regardless of which Tier each hit
// needed to walk through.
func TestNormalizeValueAliasHits_CollapsesAllThreeIPS5Variants(t *testing.T) {
	pl := fixtureLineage()
	hits := []lakehouseHit{
		// Tier 2 walk: MTM_CHAR_VALUE.CODE_NAME → MTM.CODE_NAME → PRODUCT.
		{
			KeywordHit: KeywordHit{
				Keyword: "IPS5 15IWC11", Tier: "EXACT",
				MappedTable: "MTM_CHAR_VALUE", MappedField: "CODE_NAME",
				MatchedToken: "IPS5 15IWC11", IsColumnRef: false,
			},
			PropertyID: "mtm_char_code", PropName: "CODE_NAME",
			OdID: "od_mtm_char", OdName: "MTM_CHAR_VALUE",
		},
		// Tier 1 walk: MTM.CODE_NAME → PRODUCT (property-level edge).
		{
			KeywordHit: KeywordHit{
				Keyword: "IPS5 15IWC11", Tier: "EXACT",
				MappedTable: "MTM", MappedField: "CODE_NAME",
				MatchedToken: "IPS5 15IWC11", IsColumnRef: false,
			},
			PropertyID: "mtm_code", PropName: "CODE_NAME",
			OdID: "od_mtm", OdName: "MTM",
		},
		// Already root — passes through unchanged.
		{
			KeywordHit: KeywordHit{
				Keyword: "IPS5 15IWC11", Tier: "EXACT",
				MappedTable: "PRODUCT", MappedField: "PRODUCT_OFFERING_SHORT_NAME",
				MatchedToken: "IPS5 15IWC11", IsColumnRef: false,
			},
			PropertyID: "prod_offering", PropName: "PRODUCT_OFFERING_SHORT_NAME",
			OdID: "od_product", OdName: "PRODUCT",
		},
	}
	out := normalizeValueAliasHits(hits, pl)
	if len(out) != 1 {
		t.Fatalf("3 lineage variants must collapse to 1 canonical hit; got %d (%+v)", len(out), out)
	}
	if out[0].OdName != "PRODUCT" || out[0].PropName != "PRODUCT_OFFERING_SHORT_NAME" {
		t.Fatalf("collapsed hit must point at PRODUCT.PRODUCT_OFFERING_SHORT_NAME; got %s.%s",
			out[0].OdName, out[0].PropName)
	}
}

// TestNormalizeValueAliasHits_PreservesColumnRef: column-name aliases must NOT
// be upstreamed — they identify the column itself, not the value.
func TestNormalizeValueAliasHits_PreservesColumnRef(t *testing.T) {
	pl := fixtureLineage()
	hits := []lakehouseHit{
		{
			KeywordHit: KeywordHit{
				Keyword: "code_name", Tier: "EXACT",
				MappedTable: "MTM", MappedField: "CODE_NAME",
				MatchedToken: "code_name", IsColumnRef: true, // ← column-name alias
			},
			PropertyID: "mtm_code", PropName: "CODE_NAME",
			OdID: "od_mtm", OdName: "MTM",
		},
	}
	out := normalizeValueAliasHits(hits, pl)
	if len(out) != 1 {
		t.Fatalf("column-ref hit must pass through; got %d hits", len(out))
	}
	if out[0].OdName != "MTM" {
		t.Fatalf("column-ref hit must not be upstreamed; got %s", out[0].OdName)
	}
}

// TestNormalizeValueAliasHits_PreservesOdAliasHits: Od-level alias hits
// (PropertyID=="") have no property to walk from; they must pass through.
func TestNormalizeValueAliasHits_PreservesOdAliasHits(t *testing.T) {
	pl := fixtureLineage()
	hits := []lakehouseHit{
		{
			KeywordHit: KeywordHit{
				Keyword: "early order", Tier: "EXACT",
				MappedTable: "EARLY_ORDER", MappedField: "",
				MatchedToken: "early order",
			},
			PropertyID: "", // Od-alias hit
			OdID:       "od_early_order", OdName: "EARLY_ORDER",
		},
	}
	out := normalizeValueAliasHits(hits, pl)
	if len(out) != 1 {
		t.Fatalf("Od-alias hit must pass through; got %d hits", len(out))
	}
	if out[0].OdName != "EARLY_ORDER" {
		t.Fatalf("Od-alias hit must not be rewritten; got %s", out[0].OdName)
	}
}

// TestNormalizeValueAliasHits_DistinctRootsStayDistinct: when a token genuinely
// hits two unrelated lineages, the post-normalisation dedup must NOT collapse
// them — they're different roots and the ambiguity detector should still see
// both.
func TestNormalizeValueAliasHits_DistinctRootsStayDistinct(t *testing.T) {
	pl := fixtureLineage() // no edges between CUSTOMER and SUPPLIER
	hits := []lakehouseHit{
		{
			KeywordHit: KeywordHit{
				Keyword: "Lenovo", Tier: "EXACT",
				MatchedToken: "Lenovo", IsColumnRef: false,
			},
			PropertyID: "customer_name", OdID: "od_customer", OdName: "CUSTOMER",
			PropName: "NAME",
		},
		{
			KeywordHit: KeywordHit{
				Keyword: "Lenovo", Tier: "EXACT",
				MatchedToken: "Lenovo", IsColumnRef: false,
			},
			PropertyID: "supplier_name", OdID: "od_supplier", OdName: "SUPPLIER",
			PropName: "NAME",
		},
	}
	out := normalizeValueAliasHits(hits, pl)
	if len(out) != 2 {
		t.Fatalf("two disjoint roots must remain as 2 hits; got %d", len(out))
	}
}

// TestNormalizeValueAliasHits_NilOrEmpty: pass-through when lineage is
// unavailable (e.g. DB query failed).
func TestNormalizeValueAliasHits_NilOrEmpty(t *testing.T) {
	hits := []lakehouseHit{{PropertyID: "p1", OdID: "od1", PropName: "X"}}

	if out := normalizeValueAliasHits(hits, nil); len(out) != 1 {
		t.Fatalf("nil lineage must pass through (got %d)", len(out))
	}
	empty := &propertyLineage{
		parent:    map[string]lineageParent{},
		parentOds: map[string][]string{},
		propsByOd: map[string]map[string]lineageParent{},
	}
	if out := normalizeValueAliasHits(hits, empty); len(out) != 1 {
		t.Fatalf("empty lineage must pass through (got %d)", len(out))
	}
	if out := normalizeValueAliasHits(nil, fixtureLineage()); out != nil {
		t.Fatalf("nil hits must return nil")
	}
}
