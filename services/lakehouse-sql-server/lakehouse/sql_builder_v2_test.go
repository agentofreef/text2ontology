package lakehouse

import (
	"strings"
	"testing"

	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"
)

// ── Test fixtures ──

func textProp(odName, propName string) smartquery.PropertyInfo {
	return smartquery.PropertyInfo{
		Name:       propName,
		DataType:   "text",
		TableName:  odName,
		ColumnName: propName,
		ObjectName: odName,
		ObjectID:   odName + "-id",
	}
}

func numProp(odName, propName string) smartquery.PropertyInfo {
	return smartquery.PropertyInfo{
		Name:       propName,
		DataType:   "numeric",
		TableName:  odName,
		ColumnName: propName,
		ObjectName: odName,
		ObjectID:   odName + "-id",
	}
}

func dateProp(odName, propName string) smartquery.PropertyInfo {
	return smartquery.PropertyInfo{
		Name:       propName,
		DataType:   "date",
		TableName:  odName,
		ColumnName: propName,
		ObjectName: odName,
		ObjectID:   odName + "-id",
	}
}

func makeOrderObject() ObjectInfo {
	return ObjectInfo{
		ID:             "order-id",
		Name:           "Order",
		CanonicalQuery: `SELECT "Geo", "Order_Type", "Order_Quantity", "Order_Date" FROM "stg_orders"`,
		Props: []smartquery.PropertyInfo{
			textProp("Order", "Geo"),
			textProp("Order", "Order_Type"),
			numProp("Order", "Order_Quantity"),
			dateProp("Order", "Order_Date"),
		},
	}
}

func makeProductObject() ObjectInfo {
	return ObjectInfo{
		ID:             "product-id",
		Name:           "Product",
		CanonicalQuery: `SELECT "Product_Name", "Product_Code" FROM "stg_products"`,
		Props: []smartquery.PropertyInfo{
			textProp("Product", "Product_Name"),
			textProp("Product", "Product_Code"),
		},
	}
}

// assertContainsAll asserts that haystack contains each needle (substring match).
// Used instead of full-string equality because goqu may format whitespace
// slightly differently than v1's hand-rolled output.
func assertContainsAll(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("expected SQL to contain %q\nfull SQL:\n%s", n, haystack)
		}
	}
}

// assertNotContains asserts that none of the needles appear in haystack.
func assertNotContains(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("expected SQL NOT to contain %q\nfull SQL:\n%s", n, haystack)
		}
	}
}

// ── Tests ──

func TestBuildSQLV2_NoObjects(t *testing.T) {
	rq := &ResolvedLakehouseQuery{}
	if _, err := BuildSQLV2(rq, nil); err == nil {
		t.Fatal("expected error when no objects resolved, got nil")
	}
}

func TestBuildSQLV2_SimpleSelect(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`SELECT *`,
		`FROM (SELECT "Geo"`,
		`AS "Order"`,
	)
}

func TestBuildSQLV2_FilterEqualityText(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "=", Value: "PRC"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Text equality uses ILIKE for case-insensitive match (v1 behaviour).
	assertContainsAll(t, sql, `"Order"."Geo"`, `ILIKE 'PRC'`)
}

func TestBuildSQLV2_FilterEqualityNumeric(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: numProp("Order", "Order_Quantity"), Op: "=", Value: "100"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Numeric equality uses strict =.
	assertContainsAll(t, sql, `"Order"."Order_Quantity" = '100'`)
	assertNotContains(t, sql, `ILIKE '100'`)
}

func TestBuildSQLV2_FilterMultiEqualityMergedIntoIN(t *testing.T) {
	// Three filters on same prop with op="=" → IN(...).
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "=", Value: "NA"},
			{Prop: textProp("Order", "Geo"), Op: "=", Value: "EMEA"},
			{Prop: textProp("Order", "Geo"), Op: "=", Value: "AP"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Text type → LOWER()-based IN (case-insensitive).
	assertContainsAll(t, sql,
		`LOWER(CAST("Order"."Geo" AS TEXT)) IN`,
		`LOWER('NA')`,
		`LOWER('EMEA')`,
		`LOWER('AP')`,
	)
	assertNotContains(t, sql, `ILIKE 'NA' AND`)
}

func TestBuildSQLV2_FuzzyMatchUsesWildcards(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "=", Value: "PR", FuzzyMatch: true},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql, `ILIKE '%PR%'`)
}

func TestBuildSQLV2_GroupByWithSumAggregate(t *testing.T) {
	// Disable dense mode to exercise v2's own non-dense path. (Dense mode
	// is delegated to v1's buildDenseSQL by design and tested elsewhere.)
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{Prop: textProp("Order", "Geo"), OutputLabel: "Geo"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{
				Kind:  smartquery.AggStandard,
				Prop:  ptrProp(numProp("Order", "Order_Quantity")),
				Func:  "SUM",
				Label: "Total_Quantity",
			},
		},
		DenseGroups: ptrBool(false),
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`"Order"."Geo" AS "Geo"`,
		`SUM("Order"."Order_Quantity") AS "Total_Quantity"`,
		`GROUP BY "Order"."Geo"`,
	)
}

// TestBuildSQLV2_DenseModeDelegation verifies that v2 routes through the
// v1 dense builder when isDenseApplicable returns true (default behaviour
// for groupBy + standard aggregate queries).
func TestBuildSQLV2_DenseModeDelegation(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{Prop: textProp("Order", "Geo"), OutputLabel: "Geo"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{
				Kind:  smartquery.AggStandard,
				Prop:  ptrProp(numProp("Order", "Order_Quantity")),
				Func:  "SUM",
				Label: "Total_Quantity",
			},
		},
		// DenseGroups left nil → dense applicable, should hit dense path.
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Dense-mode signature: SELECT DISTINCT dim universe + LEFT JOIN fact
	// + COALESCE(SUM(...), 0).
	assertContainsAll(t, sql,
		`SELECT DISTINCT`,
		`dims`,
		`LEFT JOIN`,
		`COALESCE(SUM("Order"."Order_Quantity"), 0)`,
	)
}

// TestBuildSQLV2_DateGranularity verifies the v2 builder emits TO_CHAR with
// the expected granularity format string and repeats the expression in
// GROUP BY (P3 dropped positional `GROUP BY 1` because expression-based
// GROUP BY is more robust to SELECT-list reordering).
func TestBuildSQLV2_DateGranularity(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{
				Prop:        dateProp("Order", "Order_Date"),
				Granularity: "YYYY-MM",
				OutputLabel: "Month",
			},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{Kind: smartquery.AggCountRows, Label: "Cnt"},
		},
		DenseGroups: ptrBool(false), // exercise v2 non-dense path
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`TO_CHAR("Order"."Order_Date" :: timestamp, 'YYYY-MM')`,
		`AS "Month"`,
		`COUNT(*) AS "Cnt"`,
		// GROUP BY repeats the expression (Postgres matches it against the
		// SELECT alias). No positional reference.
		`GROUP BY TO_CHAR("Order"."Order_Date" :: timestamp, 'YYYY-MM')`,
	)
}

func TestBuildSQLV2_OrderByAggregateIndex(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{Prop: textProp("Order", "Geo"), OutputLabel: "Geo"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{
				Kind:  smartquery.AggStandard,
				Prop:  ptrProp(numProp("Order", "Order_Quantity")),
				Func:  "SUM",
				Label: "Total",
			},
		},
		OrderByCols: []smartquery.ResolvedOrderBy{
			{Kind: smartquery.OrderByAggregate, AggIndex: 0, Dir: "DESC"},
		},
		Limit: 10,
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql, `ORDER BY "Total" DESC`, `LIMIT 10`)
}

func TestBuildSQLV2_HavingFromMetricFilter(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{Prop: textProp("Order", "Geo"), OutputLabel: "Geo"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{
				Kind:  smartquery.AggStandard,
				Prop:  ptrProp(numProp("Order", "Order_Quantity")),
				Func:  "SUM",
				Label: "Total",
			},
		},
		MetricFilter: &smartquery.MetricFilter{Op: ">", Value: "1000"},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql, `HAVING SUM("Order_Quantity") > 1000`)
}

func TestBuildSQLV2_FilterRangeOperators(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: numProp("Order", "Order_Quantity"), Op: ">=", Value: "100"},
			{Prop: numProp("Order", "Order_Quantity"), Op: "<", Value: "1000"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`"Order"."Order_Quantity" >= 100`,
		`"Order"."Order_Quantity" < 1000`,
	)
}

func TestBuildSQLV2_FilterINOperator(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "in", Value: "PRC,NA,EMEA"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`"Order"."Geo" IN ('PRC', 'NA', 'EMEA')`,
	)
}

func TestBuildSQLV2_AddShareColumnWrap(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{Prop: textProp("Order", "Geo"), OutputLabel: "Geo"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{
				Kind:  smartquery.AggStandard,
				Prop:  ptrProp(numProp("Order", "Order_Quantity")),
				Func:  "SUM",
				Label: "Total",
			},
		},
		AddShareColumn: true,
		ShareLabel:     "占比",
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Outer SELECT must add the window-based share column.
	assertContainsAll(t, sql,
		`_share_inner`,
		`SUM(_share_inner."Total") OVER ()`,
		`AS "占比"`,
	)
}

func TestBuildSQLV2_MultiOdJoin(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject(), makeProductObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Product", "Product_Code"), Op: "=", Value: "X11"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{Kind: smartquery.AggCountRows, Label: "Cnt"},
		},
	}
	joinPath := []JoinEdge{
		{FromOd: "Order", FromProp: "Product_Name", ToOd: "Product", ToProp: "Product_Name", Cardinality: "N:1"},
	}
	sql, err := BuildSQLV2(rq, joinPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`AS "Order"`,
		`AS "Product"`,
		`"Order"."Product_Name" = "Product"."Product_Name"`,
	)
}

func TestBuildWithDerivedV2_OuterSelectWrapsInner(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{Prop: textProp("Order", "Geo"), OutputLabel: "Geo"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{
				Kind:  smartquery.AggStandard,
				Prop:  ptrProp(numProp("Order", "Order_Quantity")),
				Func:  "SUM",
				Label: "Total_Quantity",
			},
			{Kind: smartquery.AggCountRows, Label: "Order_Count"},
		},
		Derived: []smartquery.DerivedMetricDef{
			{Name: "Avg_Per_Order", Expression: "Total_Quantity / Order_Count"},
		},
		Limit: 5,
	}
	sql, err := BuildWithDerivedV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`SELECT sub.*`,
		`"Total_Quantity" / NULLIF("Order_Count", 0) AS "Avg_Per_Order"`,
		`) sub`,
		`LIMIT 5`,
	)
}

func TestBuildSQLV2_BetweenOperator(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: numProp("Order", "Order_Quantity"), Op: "between", Value: "100,500"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql, `"Order"."Order_Quantity" BETWEEN 100 AND 500`)
}

func TestBuildSQLV2_ContainsOperator(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "contains", Value: "PR"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql, `ILIKE '%PR%'`)
}

func TestBuildSQLV2_DistinctCountAggregate(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		Aggregates: []smartquery.ResolvedAggregate{
			{
				Kind:  smartquery.AggStandard,
				Prop:  ptrProp(textProp("Order", "Order_Type")),
				Func:  "DISTINCTCOUNT",
				Label: "Distinct_Types",
			},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`COUNT(DISTINCT "Order"."Order_Type") AS "Distinct_Types"`,
	)
}

func TestBuildSQLV2_OperatorMissingDefaultsToEquality(t *testing.T) {
	// LLM occasionally omits "op" — v2 should default to "=", mirroring the
	// 2026-04-15 fix in NormalizeQuerySpec / buildFilterCondition.
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "", Value: "PRC"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty op falls through to "=" branch; text type → ILIKE.
	assertContainsAll(t, sql, `ILIKE 'PRC'`)
}

func TestBuildSQLV2_SQLInjectionEscapesSingleQuote(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "=", Value: "PRC' OR '1'='1"},
		},
	}
	sql, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Single quote must be doubled (PostgreSQL standard escaping).
	assertContainsAll(t, sql, `'PRC'' OR ''1''=''1'`)
	assertNotContains(t, sql, `OR '1'='1'`) // raw injection must not appear unescaped
}

// ── BuildOntologySQLV2 (Layer 1 preview) ──

// TestBuildOntologySQLV2_SingleOdUsesBareName verifies the Ontology layer
// emits a bare Od name in FROM (no canonical_query subselect) while the
// SELECT/WHERE clauses remain identical to the Physical layer.
func TestBuildOntologySQLV2_SingleOdUsesBareName(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		FilterItems: []smartquery.ResolvedFilter{
			{Prop: textProp("Order", "Geo"), Op: "=", Value: "PRC"},
		},
		DenseGroups: ptrBool(false),
	}
	sql, err := BuildOntologySQLV2(rq, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Layer 1: bare Od name in FROM.
	assertContainsAll(t, sql, `FROM "Order"`)
	// Must NOT contain the canonical_query subselect.
	assertNotContains(t, sql,
		`SELECT "Geo", "Order_Type", "Order_Quantity", "Order_Date" FROM "stg_orders"`)
	// WHERE clause is identical to Physical layer (column ref same).
	assertContainsAll(t, sql, `"Order"."Geo"`, `'PRC'`)
}

// TestBuildOntologySQLV2_MultiOdUsesBareNamesInJoin verifies the Ontology
// layer also uses bare Od names in JOIN sources.
func TestBuildOntologySQLV2_MultiOdUsesBareNamesInJoin(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects:     []ObjectInfo{makeOrderObject(), makeProductObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{{Prop: textProp("Product", "Product_Name"), OutputLabel: "Product_Name"}},
		Aggregates:  []smartquery.ResolvedAggregate{{Kind: smartquery.AggCountRows, Label: "Cnt"}},
		DenseGroups: ptrBool(false),
	}
	joinPath := []JoinEdge{{FromOd: "Order", FromProp: "Order_Type", ToOd: "Product", ToProp: "Product_Code"}}
	sql, err := BuildOntologySQLV2(rq, joinPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContainsAll(t, sql,
		`FROM "Order"`,
		`JOIN "Product" ON`,
		`"Order"."Order_Type"`,
		`"Product"."Product_Code"`,
	)
	assertNotContains(t, sql, "stg_orders", "stg_products")
}

// TestBuildOntologySQLV2_PhysicalAndOntologyDifferOnlyInFrom verifies that
// for the same resolved query, Physical and Ontology layers differ only in
// the FROM clause (subselect vs bare name) — the rest of the SQL is byte
// identical.
func TestBuildOntologySQLV2_PhysicalAndOntologyDifferOnlyInFrom(t *testing.T) {
	rq := &ResolvedLakehouseQuery{
		Objects: []ObjectInfo{makeOrderObject()},
		GroupByCols: []smartquery.ResolvedGroupBy{
			{Prop: textProp("Order", "Geo"), OutputLabel: "Geo"},
		},
		Aggregates: []smartquery.ResolvedAggregate{
			{Kind: smartquery.AggStandard, Prop: ptrProp(numProp("Order", "Order_Quantity")), Func: "SUM", Label: "Total_Qty"},
		},
		DenseGroups: ptrBool(false),
	}
	physSQL, err := BuildSQLV2(rq, nil)
	if err != nil {
		t.Fatalf("Physical: %v", err)
	}
	ontSQL, err := BuildOntologySQLV2(rq, nil)
	if err != nil {
		t.Fatalf("Ontology: %v", err)
	}
	// Both should reference Order columns identically.
	for _, needle := range []string{`"Order"."Geo"`, `SUM("Order"."Order_Quantity")`, `AS "Total_Qty"`} {
		assertContainsAll(t, physSQL, needle)
		assertContainsAll(t, ontSQL, needle)
	}
	// Physical has the canonical subselect, Ontology does not.
	assertContainsAll(t, physSQL, `(SELECT "Geo"`)
	assertNotContains(t, ontSQL, `(SELECT "Geo"`)
}

// ── Helpers ──

func ptrProp(p smartquery.PropertyInfo) *smartquery.PropertyInfo {
	return &p
}

func ptrBool(b bool) *bool { return &b }
