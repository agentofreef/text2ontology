package sqlrewrite

import (
	"reflect"
	"strings"
	"testing"
)

// The exact authoring shape the new "simple" metric editor produces — and
// what the existing lakehouse_metric row `72fff2f8` already stores. The
// parser must decompose it into (EARLY_ORDER, sum("ORDER_QUANTITY"),
// [ORDER_TYPE]) so the runtime structured engine can consume it without any
// string surgery on author SQL.
func TestParseBareMetricSQL_AcceptanceShape(t *testing.T) {
	sql := `select "ORDER_TYPE",sum("ORDER_QUANTITY") from  "EARLY_ORDER" GROUP by  "ORDER_TYPE"`
	od, m, dims, err := ParseBareMetricSQL(sql)
	if err != nil {
		t.Fatalf("ParseBareMetricSQL err = %v", err)
	}
	if od != "EARLY_ORDER" {
		t.Errorf("primaryOD = %q, want EARLY_ORDER", od)
	}
	if m != `sum("ORDER_QUANTITY")` {
		t.Errorf("measure = %q, want sum(\"ORDER_QUANTITY\")", m)
	}
	if !reflect.DeepEqual(dims, []string{"ORDER_TYPE"}) {
		t.Errorf("baseDims = %v, want [ORDER_TYPE]", dims)
	}
}

// Alias on the measure ("as qty") is PRESERVED in the stored measure expression
// so the runtime resolver can use the author's label instead of `Total_<col>`.
func TestParseBareMetricSQL_AliasPreserved(t *testing.T) {
	sql := `select "ORDER_TYPE", sum("ORDER_QUANTITY") as qty from "EARLY_ORDER" group by "ORDER_TYPE"`
	_, m, _, err := ParseBareMetricSQL(sql)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if m != `sum("ORDER_QUANTITY") as qty` {
		t.Errorf("measure = %q, want full form including alias", m)
	}
}

// Multiple base dims — both must end up in baseDims AND in GROUP BY.
func TestParseBareMetricSQL_MultipleDims(t *testing.T) {
	sql := `select "ORDER_TYPE", "GEO", avg("ORDER_QUANTITY") from "EARLY_ORDER" group by "ORDER_TYPE", "GEO"`
	od, m, dims, err := ParseBareMetricSQL(sql)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if od != "EARLY_ORDER" || m != `avg("ORDER_QUANTITY")` ||
		!reflect.DeepEqual(dims, []string{"ORDER_TYPE", "GEO"}) {
		t.Errorf("got od=%q m=%q dims=%v", od, m, dims)
	}
}

// Multi-line formatting + extra whitespace is normalized.
func TestParseBareMetricSQL_MultilineNormalized(t *testing.T) {
	sql := `select
            "ORDER_TYPE",
            sum("ORDER_QUANTITY")  as qty
         from
            "EARLY_ORDER"
         group   by
            "ORDER_TYPE"`
	od, _, dims, err := ParseBareMetricSQL(sql)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if od != "EARLY_ORDER" || !reflect.DeepEqual(dims, []string{"ORDER_TYPE"}) {
		t.Errorf("got od=%q dims=%v", od, dims)
	}
}

// JOIN at metric storage layer is rejected — runtime handles cross-OD via
// ont_causality(join_key), the author never writes JOINs.
func TestParseBareMetricSQL_RejectJoin(t *testing.T) {
	sql := `select "ORDER_TYPE", sum("ORDER_QUANTITY") from "EARLY_ORDER" e join "PRODUCT" p on e.x = p.y group by "ORDER_TYPE"`
	if _, _, _, err := ParseBareMetricSQL(sql); err == nil {
		t.Error("expected JOIN rejection, got nil")
	}
}

// Subquery in FROM is rejected — simple metrics are single-level.
func TestParseBareMetricSQL_RejectSubqueryFROM(t *testing.T) {
	sql := `select sum(x) from (select x from "EARLY_ORDER") t`
	if _, _, _, err := ParseBareMetricSQL(sql); err == nil {
		t.Error("expected subquery-in-FROM rejection, got nil")
	}
}

// No aggregate → not a metric.
func TestParseBareMetricSQL_RejectNoAggregate(t *testing.T) {
	sql := `select "ORDER_TYPE" from "EARLY_ORDER" group by "ORDER_TYPE"`
	if _, _, _, err := ParseBareMetricSQL(sql); err == nil ||
		!strings.Contains(err.Error(), "聚合") {
		t.Errorf("expected no-aggregate error, got %v", err)
	}
}

// Multiple aggregates → reject (one measure per simple metric).
func TestParseBareMetricSQL_RejectMultipleAggregates(t *testing.T) {
	sql := `select "ORDER_TYPE", sum("ORDER_QUANTITY"), avg("ORDER_QUANTITY") from "EARLY_ORDER" group by "ORDER_TYPE"`
	if _, _, _, err := ParseBareMetricSQL(sql); err == nil {
		t.Error("expected multi-aggregate rejection, got nil")
	}
}

// Non-aggregate column missing from GROUP BY → reject (Postgres would too).
func TestParseBareMetricSQL_RejectDimMissingFromGroupBy(t *testing.T) {
	sql := `select "ORDER_TYPE", "GEO", sum("ORDER_QUANTITY") from "EARLY_ORDER" group by "ORDER_TYPE"`
	if _, _, _, err := ParseBareMetricSQL(sql); err == nil ||
		!strings.Contains(err.Error(), "GROUP BY") {
		t.Errorf("expected GROUP BY mismatch error, got %v", err)
	}
}

// No GROUP BY but has a dim → reject.
func TestParseBareMetricSQL_RejectMissingGroupBy(t *testing.T) {
	sql := `select "ORDER_TYPE", sum("ORDER_QUANTITY") from "EARLY_ORDER"`
	if _, _, _, err := ParseBareMetricSQL(sql); err == nil ||
		!strings.Contains(err.Error(), "GROUP BY") {
		t.Errorf("expected missing-GROUP-BY error, got %v", err)
	}
}

// Pure-aggregate metric (no dims, no GROUP BY) is legal — sum over the whole
// OD, broken down only at runtime.
func TestParseBareMetricSQL_PureAggregateNoGroupBy(t *testing.T) {
	sql := `select sum("ORDER_QUANTITY") from "EARLY_ORDER"`
	od, m, dims, err := ParseBareMetricSQL(sql)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if od != "EARLY_ORDER" || m != `sum("ORDER_QUANTITY")` || len(dims) != 0 {
		t.Errorf("got od=%q m=%q dims=%v", od, m, dims)
	}
}

// Window function (sum() OVER ...) is NOT treated as a plain aggregate — that
// authoring shape needs the legacy escape hatch.
func TestParseBareMetricSQL_RejectWindowAggregate(t *testing.T) {
	sql := `select "ORDER_TYPE", sum("ORDER_QUANTITY") over (partition by "ORDER_TYPE") from "EARLY_ORDER" group by "ORDER_TYPE"`
	if _, _, _, err := ParseBareMetricSQL(sql); err == nil ||
		!strings.Contains(err.Error(), "聚合") {
		t.Errorf("expected window-agg rejection (no plain aggregate), got %v", err)
	}
}
