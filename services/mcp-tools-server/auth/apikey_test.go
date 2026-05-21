package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/lakehouse2ontology/services/mcp-tools-server/client"
)

// lazyDB returns a *sql.DB that never actually connects: sql.Open is lazy, so
// no I/O happens until a query runs, and any query then fails with a connection
// error (which the auth code logs + swallows, or treats as "not found"). This
// lets cache-hit fast paths and the best-effort touchLastUsed goroutine run
// without a real Postgres and without nil-pointer panics.
func lazyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestHashKey pins the on-disk hash format: lowercase hex SHA-256 of the raw
// key. The mcp_api_key table is seeded with exactly this, so any drift here
// silently locks every operator out.
func TestHashKey(t *testing.T) {
	raw := "super-secret-key"
	sum := sha256.Sum256([]byte(raw))
	want := hex.EncodeToString(sum[:])
	got := hashKey(raw)
	if got != want {
		t.Fatalf("hashKey(%q) = %q, want %q", raw, got, want)
	}
	if got != strings.ToLower(got) {
		t.Fatalf("hashKey must be lowercase hex, got %q", got)
	}
	if len(got) != 64 {
		t.Fatalf("SHA-256 hex must be 64 chars, got %d", len(got))
	}
	// Determinism: same input → same hash.
	if hashKey(raw) != got {
		t.Fatal("hashKey must be deterministic")
	}
	// Different input → different hash.
	if hashKey("other-key") == got {
		t.Fatal("distinct keys must hash differently")
	}
}

// seedCache puts a positive auth entry directly into the read-through cache so
// Lookup can be exercised on its hit path without a database.
func (a *Authenticator) seedCache(raw, label string, allowedTools []string, ttl time.Duration) {
	ent := cacheEntry{
		label:        label,
		expiresAt:    time.Now().Add(ttl),
		allowedIsNull: allowedTools == nil,
	}
	if allowedTools != nil {
		ent.allowedTools = allowedTools
	}
	a.cache.Store(hashKey(raw), ent)
}

// TestLookup_CacheHit verifies the O(1) cache fast-path returns the stored
// entry, and that an expired entry is evicted and falls through to the DB (which
// here is unreachable, so the result is a graceful miss rather than a panic).
func TestLookup_CacheHit(t *testing.T) {
	a := New(lazyDB(t))

	a.seedCache("live-key", "ci", []string{"lookup_od"}, cacheTTL)
	ent, ok := a.Lookup(context.Background(), "live-key")
	if !ok {
		t.Fatal("expected cache hit for live-key")
	}
	if ent.label != "ci" {
		t.Fatalf("label = %q, want %q", ent.label, "ci")
	}
	if ent.allowedIsNull {
		t.Fatal("allowedIsNull must be false for a whitelist key")
	}
	if len(ent.allowedTools) != 1 || ent.allowedTools[0] != "lookup_od" {
		t.Fatalf("allowedTools = %v, want [lookup_od]", ent.allowedTools)
	}

	// An expired entry must be evicted and re-fetched. With an unreachable DB
	// the refetch fails → graceful miss (no access granted, no panic). This
	// proves the "no poison-cache" revocation guarantee fails closed.
	a.seedCache("stale-key", "old", nil, -time.Second)
	if _, ok := a.Lookup(context.Background(), "stale-key"); ok {
		t.Fatal("expired entry must not report a hit when the DB cannot revalidate")
	}
}

// TestMiddleware_HealthAndMetricsBypass: liveness/metrics endpoints must not
// require credentials, otherwise probes fail and the pod is killed.
func TestMiddleware_HealthAndMetricsBypass(t *testing.T) {
	a := New(lazyDB(t))
	called := false
	h := a.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/healthz", "/metrics"} {
		called = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil) // no auth header
		h.ServeHTTP(rec, req)
		if !called {
			t.Fatalf("%s must bypass auth and reach the next handler", path)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200", path, rec.Code)
		}
	}
}

// TestMiddleware_MissingCredential returns 401 with the documented JSON body.
func TestMiddleware_MissingCredential(t *testing.T) {
	a := New(lazyDB(t))
	h := a.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not run when no credential is supplied")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/v1/tools/lookup_od", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Fatalf("body %q must mention 'unauthorized'", rec.Body.String())
	}
}

// TestMiddleware_BearerAndXAPIKey: a valid key supplied via either header
// authenticates, reaches the next handler, and the caller label is attached to
// the request context for downstream audit/permission use.
func TestMiddleware_BearerAndXAPIKey(t *testing.T) {
	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"Authorization Bearer", "Authorization", "Bearer good-key"},
		{"X-API-Key", "X-API-Key", "good-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := New(lazyDB(t))
			a.seedCache("good-key", "tester", []string{"lookup_od"}, cacheTTL)

			var gotCaller any
			var gotAllowed any
			h := a.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotCaller = r.Context().Value(client.CallerKey)
				gotAllowed = r.Context().Value(AllowedToolsKey)
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/mcp/v1/tools/lookup_od", nil)
			req.Header.Set(tc.header, tc.value)
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if gotCaller != "mcp:tester" {
				t.Fatalf("caller = %v, want mcp:tester", gotCaller)
			}
			allowed, ok := gotAllowed.([]string)
			if !ok || len(allowed) != 1 || allowed[0] != "lookup_od" {
				t.Fatalf("allowed tools ctx = %v, want [lookup_od]", gotAllowed)
			}
		})
	}
}

// TestMiddleware_AdminKeyHasNoToolRestriction: a key whose allowed_tools IS
// NULL must NOT place an AllowedToolsKey on the context (downstream treats
// absence as "admin / all tools").
func TestMiddleware_AdminKeyHasNoToolRestriction(t *testing.T) {
	a := New(lazyDB(t))
	a.seedCache("admin-key", "root", nil, cacheTTL) // nil allowedTools → admin

	var sawAllowedKey bool
	h := a.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAllowedKey = r.Context().Value(AllowedToolsKey) != nil
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/v1/tools/lookup_od", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if sawAllowedKey {
		t.Fatal("admin key (allowed_tools IS NULL) must not set AllowedToolsKey on ctx")
	}
}
