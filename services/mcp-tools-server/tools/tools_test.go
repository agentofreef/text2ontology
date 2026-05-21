package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lakehouse2ontology/services/mcp-tools-server/auth"
)

// TestAllowedForCaller covers the three permission states encoded in the
// request context by the auth middleware:
//   - no AllowedToolsKey (nil)        → admin, every tool permitted
//   - non-nil whitelist               → only listed tools permitted
//   - empty (non-nil) whitelist       → explicit lockdown, nothing permitted
func TestAllowedForCaller(t *testing.T) {
	cases := []struct {
		name    string
		ctxVal  any // value stored under auth.AllowedToolsKey, or "absent"
		tool    string
		allowed bool
	}{
		{"admin key (absent)", "absent", "lookup_od", true},
		{"whitelist allows listed", []string{"lookup_od", "recall_tokens"}, "recall_tokens", true},
		{"whitelist blocks unlisted", []string{"lookup_od"}, "execute_smartquery", false},
		{"empty whitelist locks down", []string{}, "lookup_od", false},
		{"wrong type fails closed", 42, "lookup_od", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if s, ok := tc.ctxVal.(string); !ok || s != "absent" {
				ctx = context.WithValue(ctx, auth.AllowedToolsKey, tc.ctxVal)
			}
			if got := allowedForCaller(ctx, tc.tool); got != tc.allowed {
				t.Fatalf("allowedForCaller(%q) = %v, want %v", tc.tool, got, tc.allowed)
			}
		})
	}
}

// TestDispatch_MethodNotAllowed: only POST is accepted on the tool route.
func TestDispatch_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/v1/tools/lookup_od", nil)
	Dispatch(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: code = %d, want 405", rec.Code)
	}
	assertErrBody(t, rec, "POST only")
}

// TestDispatch_Forbidden: a key whose whitelist excludes the requested tool is
// rejected with 403 before the handler (and therefore the DB) is reached.
func TestDispatch_Forbidden(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/v1/tools/execute_smartquery", nil)
	ctx := context.WithValue(req.Context(), auth.AllowedToolsKey, []string{"lookup_od"})
	Dispatch(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	assertErrBody(t, rec, "not permitted")
}

// TestDispatch_UnknownTool: an admin caller hitting a non-existent tool gets
// 404 with the available-tools hint (still no DB access).
func TestDispatch_UnknownTool(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/v1/tools/does_not_exist", nil)
	// No AllowedToolsKey → admin, so the permission gate passes and we reach
	// the switch default.
	Dispatch(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	assertErrBody(t, rec, "unknown tool")
}

func assertErrBody(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not JSON: %v (raw=%s)", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("expected an 'error' key in body, got %v", body)
	}
	if !strings.Contains(body["error"], want) {
		t.Fatalf("error %q must contain %q", body["error"], want)
	}
}
