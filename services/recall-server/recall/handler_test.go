package recall

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// lazyDB returns a *sql.DB that never connects (sql.Open is lazy). The handler
// paths under test reject the request before any query, so no Postgres is
// needed while still passing a non-nil handle.
func lazyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestSplitTokenString covers the delimiter handling that callers (other
// services posting a token string) rely on: commas, pipes, semicolons and
// newlines split; leading spaces are dropped; intra-token spaces are preserved.
//
// Note: the function only strips LEADING spaces (a space is dropped when the
// current token is empty). A space immediately before a delimiter is retained
// (e.g. "PCV ,品牌" → "PCV "). These cases pin that real behavior so a future
// refactor can't silently change tokenization semantics.
func TestSplitTokenString(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"a|b;c\nd", []string{"a", "b", "c", "d"}},
		{"  PCV,品牌", []string{"PCV", "品牌"}}, // leading spaces dropped
		{"Product Name,Customer", []string{"Product Name", "Customer"}},
		{"PCV ,品牌", []string{"PCV ", "品牌"}}, // trailing-before-delimiter space retained
		{"", nil},
		{",,,", nil},
	}
	for _, tc := range cases {
		got := splitTokenString(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitTokenString(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("splitTokenString(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// TestIsColumnRef: the DB flag wins; otherwise a case-insensitive match against
// the property name / display name / mapped field marks the keyword as a column
// reference (vs a data value). This drives whether downstream emits a column or
// a value filter, so the classification is load-bearing.
func TestIsColumnRef(t *testing.T) {
	if !isColumnRef(KeywordHit{IsColumnRef: true, Keyword: "anything"}, "Geo", "Region") {
		t.Fatal("explicit IsColumnRef flag must win")
	}
	if !isColumnRef(KeywordHit{Keyword: "geo"}, "Geo", "Region") {
		t.Fatal("case-insensitive match on property name must be a column ref")
	}
	if !isColumnRef(KeywordHit{Keyword: "region"}, "Geo", "Region") {
		t.Fatal("case-insensitive match on display name must be a column ref")
	}
	if !isColumnRef(KeywordHit{Keyword: "sku", MappedField: "SKU"}, "Geo", "Region") {
		t.Fatal("match on mapped field must be a column ref")
	}
	if isColumnRef(KeywordHit{Keyword: "IPS5 15IWC11"}, "Geo", "Region") {
		t.Fatal("a data value must not be classified as a column ref")
	}
}

// TestAppendUnique: deduplicates and drops empties while preserving order.
func TestAppendUnique(t *testing.T) {
	got := appendUnique([]string{"a"}, "b", "a", "", "c", "b")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("appendUnique = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("appendUnique[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestHandleLakehouseDebug_ContractGuards exercises the recall HTTP contract
// that other services depend on, without a database: preflight, method
// rejection, and the empty-tokens validation all resolve before any DB access.
func TestHandleLakehouseDebug_ContractGuards(t *testing.T) {
	h := HandleLakehouseDebug(lazyDB(t))

	t.Run("OPTIONS preflight", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/api/ontology/lakehouse-token-recall-debug", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("OPTIONS code = %d, want 204", rec.Code)
		}
	})

	t.Run("GET rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ontology/lakehouse-token-recall-debug", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET code = %d, want 405", rec.Code)
		}
	})

	t.Run("empty tokens rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			"/api/ontology/lakehouse-token-recall-debug?projectId=p1",
			strings.NewReader(`{"tokens":[]}`))
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty tokens code = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if e, _ := body["error"].(string); !strings.Contains(e, "tokens") {
			t.Fatalf("error %q must mention tokens", e)
		}
	})
}
