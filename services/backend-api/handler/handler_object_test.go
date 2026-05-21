package handler

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
// validation paths under test reject the request before any query runs, so this
// keeps the tests hermetic — no Postgres required — while still passing a
// non-nil handle.
func lazyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestHandleObjects_OptionsPreflight: CORS preflight returns 204 and never
// touches the DB.
func TestHandleObjects_OptionsPreflight(t *testing.T) {
	h := HandleObjects(lazyDB(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/ontology/objects", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS: code = %d, want 204", rec.Code)
	}
}

// TestHandleObjects_GetWithoutProjectReturnsEmptyList: a GET with no/invalid
// projectId must short-circuit to an empty list (200) instead of querying the
// DB with a bad project filter.
func TestHandleObjects_GetWithoutProjectReturnsEmptyList(t *testing.T) {
	for _, q := range []string{"", "?projectId=", "?projectId=not-a-uuid"} {
		h := HandleObjects(lazyDB(t))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ontology/objects"+q, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %q: code = %d, want 200", q, rec.Code)
		}
		var body struct {
			Data  []any `json:"data"`
			Total int   `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %q: body not JSON: %v (raw=%s)", q, err, rec.Body.String())
		}
		if len(body.Data) != 0 || body.Total != 0 {
			t.Fatalf("GET %q: want empty list, got %+v", q, body)
		}
	}
}

// TestHandleObjects_PostInvalidProjectRejected: creating an object with a
// missing/invalid projectId fails with 400 before any INSERT — the validation
// gate that protects the auth/ownership re-check below it.
func TestHandleObjects_PostInvalidProjectRejected(t *testing.T) {
	h := HandleObjects(lazyDB(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ontology/objects",
		strings.NewReader(`{"name":"Customer"}`)) // no projectId
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST: code = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("POST: body not JSON: %v (raw=%s)", err, rec.Body.String())
	}
	if !strings.Contains(body["error"], "projectId") {
		t.Fatalf("POST: error %q must mention projectId", body["error"])
	}
}
