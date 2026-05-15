package synthesizer

import (
	"strings"
	"testing"
)

func TestExtractIndicators(t *testing.T) {
	tests := []struct {
		name string
		q    string
		want []string
	}{
		{"single 占比", "X11 PRC 地区 Real Order 占比", []string{"占比"}},
		{"longest match wins", "X10 占比最大的 GEO", []string{"占比"}},
		{"share + 占比", "share of X10 占比", []string{"share", "占比"}}, // longest-first by rune count: share=5 > 占比=2
		{"no indicator", "X10 销量", nil},
		{"chinese ratio", "X10 转化率多少", []string{"转化率"}},
		{"explicit 全球占比", "X11 全球占比", []string{"全球占比"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractIndicators(tt.q)
			if len(got) != len(tt.want) {
				t.Errorf("ExtractIndicators(%q) = %v, want %v", tt.q, got, tt.want)
				return
			}
			for i, w := range tt.want {
				if !strings.EqualFold(got[i], w) {
					t.Errorf("got[%d]=%q want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestCheckUserTermsPreserved(t *testing.T) {
	in := Input{UserTerms: []string{"占比"}}
	if g := checkUserTermsPreserved("PRC 占比 25%", in); g != nil {
		t.Errorf("expected pass, got gap %+v", g)
	}
	if g := checkUserTermsPreserved("PRC 比率 25%", in); g == nil {
		t.Errorf("expected term_drift gap, got nil")
	} else if g.Type != "term_drift" {
		t.Errorf("got type %q, want term_drift", g.Type)
	}
}

func TestCheckBlacklistedTermsAbsent(t *testing.T) {
	in := Input{UserTerms: []string{"占比"}}
	// Pass: only uses 占比
	if g := checkBlacklistedTermsAbsent("PRC 占比 25%", in); g != nil {
		t.Errorf("expected pass, got %+v", g)
	}
	// Fail: introduces 比率 (synonym of 占比) in addition
	if g := checkBlacklistedTermsAbsent("PRC 占比 25%, 也即比率 25%", in); g == nil {
		t.Errorf("expected blacklist_term gap, got nil")
	}
	// Pass when user said both — no blacklisting
	in2 := Input{UserTerms: []string{"占比", "转化率"}}
	if g := checkBlacklistedTermsAbsent("占比 25%, 转化率 25%", in2); g != nil {
		t.Errorf("expected pass, got %+v", g)
	}
	// Skip when user used no indicators
	in3 := Input{UserTerms: nil}
	if g := checkBlacklistedTermsAbsent("占比比率份额", in3); g != nil {
		t.Errorf("expected skip (nil) when no UserTerms, got %+v", g)
	}
}

func TestCheckFiltersEchoed(t *testing.T) {
	in := Input{Filters: []FilterRef{
		{Prop: "GEO", Op: "=", Value: "PRC"},
		{Prop: "Product", Op: "=", Value: "X11"},
	}}
	if g := checkFiltersEchoed("PRC 的 X11 总量是 100", in); g != nil {
		t.Errorf("expected pass, got %+v", g)
	}
	if g := checkFiltersEchoed("总量是 100（地区未提）", in); g == nil {
		t.Errorf("expected filter_missing gap, got nil")
	}
	// Short numeric values skipped
	in2 := Input{Filters: []FilterRef{{Prop: "Year", Op: "=", Value: "1"}}}
	if g := checkFiltersEchoed("总量是 100", in2); g != nil {
		t.Errorf("short numeric should skip, got %+v", g)
	}
}

func TestCheckResponseTplFollowed(t *testing.T) {
	in := Input{ResponseTpl: "共 {total} 订单，其中 {real} 已转 Real Order"}
	if g := checkResponseTplFollowed("共 386410 订单，其中 254000 已转 Real Order", in); g != nil {
		t.Errorf("expected pass, got %+v", g)
	}
	if g := checkResponseTplFollowed("订单总量是 386410", in); g == nil {
		t.Errorf("expected template_skipped gap, got nil")
	}
	// Empty template = skip
	in2 := Input{ResponseTpl: ""}
	if g := checkResponseTplFollowed("anything", in2); g != nil {
		t.Errorf("expected skip, got %+v", g)
	}
}

func TestCheckPivotSuffixUsed(t *testing.T) {
	in := Input{
		PivotColumns: []string{"未转换的Real Order 占比", "Real Order 占比"},
		IntentSuffix: "占比",
	}
	if g := checkPivotSuffixUsed("未转换的Real Order 占比 25.1%", in); g != nil {
		t.Errorf("expected pass (column match), got %+v", g)
	}
	// Suffix-only fallback
	if g := checkPivotSuffixUsed("PRC 在 X11 内的占比 25%", in); g != nil {
		t.Errorf("expected pass (suffix fallback), got %+v", g)
	}
	if g := checkPivotSuffixUsed("PRC 数量是 25", in); g == nil {
		t.Errorf("expected template_skipped gap, got nil")
	}
	// No pivot context = skip
	in2 := Input{}
	if g := checkPivotSuffixUsed("anything", in2); g != nil {
		t.Errorf("expected skip when no pivot, got %+v", g)
	}
}

func TestCheckScopeStated(t *testing.T) {
	in := Input{PercentAxis: "row", IntentName: "ORDER.ORDER_QUANTITY"}
	if g := checkScopeStated("PRC 切片内 占比 25%", in); g != nil {
		t.Errorf("expected pass, got %+v", g)
	}
	if g := checkScopeStated("占比 25%（分母为全部 GEO）", in); g != nil {
		t.Errorf("expected pass, got %+v", g)
	}
	if g := checkScopeStated("PRC 占比 25%", in); g != nil {
		// "占" alone marker passes, this is acceptable lax.
		t.Errorf("expected pass on %q, got %+v", "占", g)
	}
	if g := checkScopeStated("数量是 25", Input{PercentAxis: "row", IntentName: "X"}); g == nil {
		t.Errorf("expected scope_unstated gap, got nil")
	}
	// No percent context = skip
	if g := checkScopeStated("数量是 25", Input{}); g != nil {
		t.Errorf("expected skip when no percent, got %+v", g)
	}
}

func TestCheckShareHasDenominator(t *testing.T) {
	// Trigger case: user asked share-by-partition, but SQL filtered to one value.
	in := Input{
		Question:  "PRC Real Order 在所有 GEO 中占比多少",
		UserTerms: []string{"占比"},
		GroupBy:   []string{"GEO", "Order_Type"},
		Filters: []FilterRef{
			{Prop: "GEO", Op: "=", Value: "PRC"},
		},
		Rows: []map[string]interface{}{
			{"GEO": "PRC", "Order_Type": "未转换的Real Order", "Total_ORDER_QUANTITY": 46200},
			{"GEO": "PRC", "Order_Type": "Real Order", "Total_ORDER_QUANTITY": 1573004},
		},
	}
	if g := checkShareHasDenominator("anything", in); g == nil {
		t.Errorf("expected data_insufficient gap (degenerate share), got nil")
	} else if g.Type != "data_insufficient" {
		t.Errorf("got type %q, want data_insufficient", g.Type)
	}

	// Pass: enough rows, no degeneracy.
	in2 := Input{
		Question:  "PRC 在所有 GEO 中占比",
		UserTerms: []string{"占比"},
		GroupBy:   []string{"GEO"},
		Filters:   []FilterRef{{Prop: "GEO", Op: "=", Value: "PRC"}},
		Rows: []map[string]interface{}{
			{"GEO": "PRC"}, {"GEO": "EMEA"}, {"GEO": "AP"},
			{"GEO": "N.A."}, {"GEO": "LAS"},
		},
	}
	if g := checkShareHasDenominator("anything", in2); g != nil {
		t.Errorf("expected pass (5 rows = enough denominator), got %+v", g)
	}

	// Pass: no share term in question — different question type.
	in3 := Input{
		Question:  "PRC 早单数量", // no share term
		UserTerms: nil,
		GroupBy:   []string{"GEO", "Order_Type"},
		Filters:   []FilterRef{{Prop: "GEO", Op: "=", Value: "PRC"}},
		Rows: []map[string]interface{}{
			{"GEO": "PRC", "Order_Type": "未转换的Real Order"},
			{"GEO": "PRC", "Order_Type": "Real Order"},
		},
	}
	if g := checkShareHasDenominator("anything", in3); g != nil {
		t.Errorf("expected skip (no share term), got %+v", g)
	}

	// Pass: filter prop NOT in groupBy → unrelated filter, not the share dim.
	in4 := Input{
		Question:  "GEO 占比",
		UserTerms: []string{"占比"},
		GroupBy:   []string{"GEO"},
		Filters:   []FilterRef{{Prop: "FISCAL_YEAR", Op: "=", Value: "2026"}},
		Rows:      []map[string]interface{}{{"GEO": "PRC"}, {"GEO": "EMEA"}},
	}
	if g := checkShareHasDenominator("anything", in4); g != nil {
		t.Errorf("expected skip (filter on unrelated dim FISCAL_YEAR), got %+v", g)
	}

	// Pass: question doesn't mention the filter dim — unrelated filter case.
	in5 := Input{
		Question:  "Brand 占比", // mentions Brand, not GEO
		UserTerms: []string{"占比"},
		GroupBy:   []string{"GEO"},
		Filters:   []FilterRef{{Prop: "GEO", Op: "=", Value: "PRC"}},
		Rows:      []map[string]interface{}{{"GEO": "PRC"}, {"GEO": "EMEA"}},
	}
	if g := checkShareHasDenominator("anything", in5); g != nil {
		t.Errorf("expected skip (question doesn't mention GEO dim), got %+v", g)
	}

	// Pattern B trigger: filter on Y, question says "所有Y百分比", but Y NOT in groupBy.
	// Run A regression case: filter GEO=AP, groupBy=[ORDER_TYPE], question
	// "AP地区X10代Real Order的数量占所有geo百分比". Result has 1 pivoted row,
	// no GEO dimension to recover share — must drop GEO filter and add to groupBy.
	in6 := Input{
		Question:  "AP地区X10代Real Order的数量占所有geo百分比是多少",
		UserTerms: []string{"百分比"},
		GroupBy:   []string{"ORDER_TYPE"},
		Filters: []FilterRef{
			{Prop: "GEO", Op: "=", Value: "AP"},
			{Prop: "Product", Op: "=", Value: "X10"},
		},
		Rows: []map[string]interface{}{
			{"ORDER_TYPE": "未转换的Real Order", "Total_Order_Quantity": 120067},
		},
	}
	g6 := checkShareHasDenominator("AP占所有GEO的100%", in6)
	if g6 == nil {
		t.Fatalf("Pattern B: expected data_insufficient gap, got nil")
	}
	if g6.Type != "data_insufficient" {
		t.Errorf("Pattern B: got type %q, want data_insufficient", g6.Type)
	}
	if !strings.Contains(g6.Recommendation, "GEO") {
		t.Errorf("Pattern B: recommendation should name GEO filter, got %q", g6.Recommendation)
	}

	// Pattern B negative: filter on Y, but no all-Y prefix — unrelated filter,
	// just a date or product filter that mentions "GEO" coincidentally.
	in7 := Input{
		Question:  "GEO PRC 的早单数量", // "GEO" appears but no 所有/全部 prefix
		UserTerms: []string{"占比"},
		GroupBy:   []string{"ORDER_TYPE"},
		Filters:   []FilterRef{{Prop: "GEO", Op: "=", Value: "PRC"}},
		Rows:      []map[string]interface{}{{"ORDER_TYPE": "未转换的Real Order"}},
	}
	if g := checkShareHasDenominator("anything", in7); g != nil {
		t.Errorf("Pattern B negative: GEO mentioned but no all-Y prefix, expected skip, got %+v", g)
	}
}

func TestCheckDenomLabelPrecise(t *testing.T) {
	// Pass: no filter → "全球" is accurate, gate skips.
	in1 := Input{Filters: nil}
	if g := checkDenomLabelPrecise("X10 全球总订单量 1059469", in1); g != nil {
		t.Errorf("no-filter case: expected pass, got %+v", g)
	}

	// Pass: filter present but draft doesn't say "全球".
	in2 := Input{Filters: []FilterRef{{Prop: "GEN", Op: "=", Value: "X10"}}}
	if g := checkDenomLabelPrecise("X10 全部地区合计 1059469", in2); g != nil {
		t.Errorf("clean-label case: expected pass, got %+v", g)
	}

	// Pass: user themselves used "全球占比" — preserving user vocabulary trumps gate.
	in3 := Input{
		Filters:   []FilterRef{{Prop: "GEN", Op: "=", Value: "X10"}},
		UserTerms: []string{"全球占比"},
	}
	if g := checkDenomLabelPrecise("X10 全球占比 25%", in3); g != nil {
		t.Errorf("user-said-全球 case: expected pass, got %+v", g)
	}

	// Fail: filter scopes the data but draft labels denom as "全球".
	in4 := Input{
		Filters:   []FilterRef{{Prop: "GEN", Op: "=", Value: "X10"}},
		UserTerms: []string{"占比"}, // does not contain 全球
	}
	g4 := checkDenomLabelPrecise("X10 全球总订单量 1059469 pcs", in4)
	if g4 == nil {
		t.Fatalf("scoped-with-全球 case: expected denom_mislabeled gap, got nil")
	}
	if g4.Type != "denom_mislabeled" {
		t.Errorf("got type %q, want denom_mislabeled", g4.Type)
	}
}

func TestCheckZeroTotalRowsNotClaimed(t *testing.T) {
	totalLabel := "总订单数量"
	// Pass: TotalLabel empty → skip
	if g := checkZeroTotalRowsNotClaimed("anything", Input{TotalLabel: ""}); g != nil {
		t.Errorf("empty TotalLabel: expected pass, got %+v", g)
	}

	// Pass: rows empty → skip
	if g := checkZeroTotalRowsNotClaimed("anything", Input{TotalLabel: totalLabel}); g != nil {
		t.Errorf("no rows: expected pass, got %+v", g)
	}

	// Pass: zero-row's product NOT mentioned in draft.
	in1 := Input{
		TotalLabel: totalLabel,
		Question:   "X11 哪10个产品订单数量最多",
		Rows: []map[string]interface{}{
			{"PRODUCT_NAME": "LEGION 16IRX11", totalLabel: 79000.0},
			{"PRODUCT_NAME": "GHOST 14APH11", totalLabel: 0.0},
		},
	}
	if g := checkZeroTotalRowsNotClaimed("LEGION 16IRX11 共 79,000 单订单", in1); g != nil {
		t.Errorf("zero-row not in draft: expected pass, got %+v", g)
	}

	// Fail: draft mentions a 0-total product.
	if g := checkZeroTotalRowsNotClaimed("Top 产品: LEGION 16IRX11 (79000), GHOST 14APH11 (0 单)", in1); g == nil {
		t.Fatalf("zero-row mentioned in draft: expected phantom_zero_row gap, got nil")
	} else if g.Type != "phantom_zero_row" {
		t.Errorf("got type %q, want phantom_zero_row", g.Type)
	}

	// Pass: user explicitly asked about the 0-total product → not phantom.
	in2 := Input{
		TotalLabel: totalLabel,
		Question:   "GHOST 14APH11 有多少订单",
		Rows: []map[string]interface{}{
			{"PRODUCT_NAME": "GHOST 14APH11", totalLabel: 0.0},
		},
	}
	if g := checkZeroTotalRowsNotClaimed("GHOST 14APH11 没有订单数据", in2); g != nil {
		t.Errorf("user explicitly asked: expected pass, got %+v", g)
	}

	// Pass: pivot column / total column values are skipped (not treated as dim).
	in3 := Input{
		TotalLabel:   totalLabel,
		PivotColumns: []string{"未转化的early Order", "已转化的early Order"},
		Question:     "X11 top 产品",
		Rows: []map[string]interface{}{
			{"PRODUCT_NAME": "LEGION 16IRX11", totalLabel: 0.0,
				"未转化的early Order": "0", "已转化的early Order": "0"},
		},
	}
	// "0" is too short to trigger; product not in draft → pass.
	if g := checkZeroTotalRowsNotClaimed("无产品有数据", in3); g != nil {
		t.Errorf("pivot-col-only-mention: expected pass, got %+v", g)
	}
}

func TestCheckRowCountUsesDistinct(t *testing.T) {
	// Pass: RowSummary nil → skip
	if g := checkRowCountUsesDistinct("anything 62 款", Input{}); g != nil {
		t.Errorf("nil RowSummary: expected pass, got %+v", g)
	}

	// Pass: total_rows == distinct_dim_items → no inflation risk
	in1 := Input{
		Question: "X11 哪些机型有订单",
		RowSummary: map[string]interface{}{
			"total_rows":         60.0,
			"distinct_dim_items": 60.0,
		},
	}
	if g := checkRowCountUsesDistinct("共 60 款机型", in1); g != nil {
		t.Errorf("equal counts: expected pass, got %+v", g)
	}

	// Pass: draft doesn't quote the inflated number
	in2 := Input{
		Question: "X11 哪些机型",
		RowSummary: map[string]interface{}{
			"total_rows":         62.0,
			"distinct_dim_items": 60.0,
		},
	}
	if g := checkRowCountUsesDistinct("X11 多个机型有数据", in2); g != nil {
		t.Errorf("draft no number: expected pass, got %+v", g)
	}

	// Pass: draft contains BOTH numbers (LLM showed corrected one)
	if g := checkRowCountUsesDistinct("共 60 款机型有数据（结果集 62 行含空行）", in2); g != nil {
		t.Errorf("both numbers shown: expected pass, got %+v", g)
	}

	// Pass: user explicitly mentioned the inflated number in question
	in3 := Input{
		Question: "为什么 62 行只有 60 个真实产品",
		RowSummary: map[string]interface{}{
			"total_rows":         62.0,
			"distinct_dim_items": 60.0,
		},
	}
	if g := checkRowCountUsesDistinct("结果共 62 行", in3); g != nil {
		t.Errorf("user-quoted number: expected pass, got %+v", g)
	}

	// Fail: draft says inflated total without distinct count
	in4 := Input{
		Question: "X11 哪些机型有订单",
		RowSummary: map[string]interface{}{
			"total_rows":         62.0,
			"distinct_dim_items": 60.0,
			"zero_data_rows":     1.0,
		},
	}
	g4 := checkRowCountUsesDistinct("X11 全部 62 款有订单的机型", in4)
	if g4 == nil {
		t.Fatalf("inflated count: expected total_rows_inflated gap, got nil")
	}
	if g4.Type != "total_rows_inflated" {
		t.Errorf("got type %q, want total_rows_inflated", g4.Type)
	}
	if !strings.Contains(g4.Recommendation, "60") {
		t.Errorf("recommendation should suggest distinct count 60, got %q", g4.Recommendation)
	}
}

func TestExtractTemplateLiterals(t *testing.T) {
	got := extractTemplateLiterals("共 {total} 订单，其中 {real} 已转 Real Order")
	want := []string{"共 ", " 订单，其中 ", " 已转 Real Order"}
	if len(got) != len(want) {
		t.Fatalf("got %d literals, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("literal[%d]=%q want %q", i, got[i], w)
		}
	}
}

func TestRunMechanicalChecksAllPass(t *testing.T) {
	// Post-Phase-0 schema: ORDER.ORDER_QUANTITY suffix corrected from 转化率
	// to 占比, so column literals align with user vocabulary.
	in := Input{
		Question:     "X11 PRC 地区 Real Order 占比",
		UserTerms:    []string{"占比"},
		Filters:      []FilterRef{{Prop: "GEO", Op: "=", Value: "PRC"}, {Prop: "Product", Op: "=", Value: "X11"}},
		IntentName:   "ORDER.ORDER_QUANTITY",
		IntentSuffix: "占比",
		PercentAxis:  "row",
		PercentScope: "filtered",
		ResponseTpl:  "",
		PivotColumns: []string{"未转换的Real Order 占比", "Real Order 占比"},
	}
	draft := "PRC X11 内部，未转换的Real Order 占比 25.1%（切片内分母）"
	gaps := runMechanicalChecks(draft, in)
	if len(gaps) > 0 {
		t.Errorf("expected pass, got gaps: %+v", gaps)
	}
}

func TestRunMechanicalChecksFailsOnTermDrift(t *testing.T) {
	in := Input{
		UserTerms:    []string{"占比"},
		Filters:      []FilterRef{{Prop: "GEO", Op: "=", Value: "PRC"}},
		IntentName:   "X",
		IntentSuffix: "占比",
		PercentAxis:  "row",
		PivotColumns: []string{"未转换的Real Order 占比"},
	}
	// Draft uses "比率" instead of user's "占比" — should fail term_drift + blacklist.
	draft := "PRC 的比率是 25%（切片内分母）"
	gaps := runMechanicalChecks(draft, in)
	if len(gaps) == 0 {
		t.Errorf("expected gaps for term drift, got pass")
	}
	hasTermDrift := false
	for _, g := range gaps {
		if g.Type == "term_drift" {
			hasTermDrift = true
		}
	}
	if !hasTermDrift {
		t.Errorf("expected term_drift gap among %+v", gaps)
	}
}
