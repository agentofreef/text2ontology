package authmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These cover the branches that need no DB/token: the /internal/* bypass and
// the empty-id short-circuit. The membership/admin/missing-entity branches
// require a database and are exercised by integration tests.

func TestEnforceEntityProject_InternalBypass(t *testing.T) {
	// /internal/* is authenticated by INTERNAL_TOKEN at the middleware;
	// the helper must return true without touching the (nil) DB.
	r := httptest.NewRequest(http.MethodGet, "/internal/agent/lh-test-suites/abc", nil)
	w := httptest.NewRecorder()
	if !EnforceEntityProject(w, r, nil, "ont_test_suite", "id", "abc") {
		t.Fatal("internal path must bypass and return true")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("internal bypass must not write an error status, got %d", w.Code)
	}
}

func TestEnforceEntityProject_EmptyID404(t *testing.T) {
	// Public path, empty id → 404 before any DB access (nil DB is safe).
	r := httptest.NewRequest(http.MethodGet, "/api/ontology/objects/", nil)
	w := httptest.NewRecorder()
	if EnforceEntityProject(w, r, nil, "ont_object_type", "id", "") {
		t.Fatal("empty id must be denied")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("empty id must yield 404, got %d", w.Code)
	}
}

func TestEnforceProjectAccess_InternalBypass(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/internal/agent/lakehouse-agent-stream", nil)
	w := httptest.NewRecorder()
	if !EnforceProjectAccess(w, r, nil, "any-project") {
		t.Fatal("internal path must bypass project access")
	}
}

func TestEnforceEntityOwner_InternalBypassAndEmpty(t *testing.T) {
	// internal bypass
	r := httptest.NewRequest(http.MethodGet, "/internal/backend-api/mcp-keys/x", nil)
	w := httptest.NewRecorder()
	if !EnforceEntityOwner(w, r, nil, "mcp_api_key", "id", "user_id", "x") {
		t.Fatal("internal path must bypass owner check")
	}
	// empty id on public path → 404 before DB
	r2 := httptest.NewRequest(http.MethodDelete, "/api/ontology/mcp-keys/", nil)
	w2 := httptest.NewRecorder()
	if EnforceEntityOwner(w2, r2, nil, "mcp_api_key", "id", "user_id", "") {
		t.Fatal("empty id must be denied")
	}
	if w2.Code != http.StatusNotFound {
		t.Fatalf("empty id must yield 404, got %d", w2.Code)
	}
}
