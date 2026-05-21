package authmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests cover the constant-time INTERNAL_TOKEN comparison in
// handleInternal. The comparison must:
//   - accept a correct token (and proceed to the on-behalf-of check),
//   - reject a wrong token of the same length,
//   - reject a wrong token of a different length (no panic),
//   - reject when the expected token is empty/unset (fail-closed).
//
// We assert observable behaviour (status code) rather than timing — the
// constant-time property is structural (subtle.ConstantTimeCompare) and
// cannot be asserted reliably in a unit test, but the reject/accept
// semantics and the length-safety guarantee can be.

// okHandler returns 200; used as the downstream handler so an accepted
// internal call is distinguishable from a rejected one.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func internalRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/internal/agent/ping", nil)
	if token != "" {
		r.Header.Set("X-Internal-Token", token)
	}
	r.Header.Set("X-On-Behalf-Of", "user-123")
	return r
}

func TestHandleInternal_CorrectTokenAccepted(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "s3cr3t-token-value")
	m := NewWithChecker(nil, nil)
	w := httptest.NewRecorder()
	m.Wrap(okHandler()).ServeHTTP(w, internalRequest("s3cr3t-token-value"))
	if w.Code != http.StatusOK {
		t.Fatalf("correct token must be accepted (200), got %d", w.Code)
	}
}

func TestHandleInternal_WrongTokenSameLengthRejected(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "s3cr3t-token-value")
	m := NewWithChecker(nil, nil)
	w := httptest.NewRecorder()
	// Same byte length, different content.
	m.Wrap(okHandler()).ServeHTTP(w, internalRequest("WRONG-token-value!"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token (same length) must be rejected (401), got %d", w.Code)
	}
}

func TestHandleInternal_WrongTokenDifferentLengthRejected(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "s3cr3t-token-value")
	m := NewWithChecker(nil, nil)
	w := httptest.NewRecorder()
	// Different length must not panic and must reject — ConstantTimeCompare
	// returns 0 for differing lengths.
	m.Wrap(okHandler()).ServeHTTP(w, internalRequest("short"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token (different length) must be rejected (401), got %d", w.Code)
	}
}

func TestHandleInternal_EmptyExpectedTokenRejected(t *testing.T) {
	// Unset INTERNAL_TOKEN: must fail-closed (401) even if the caller
	// presents an empty token. Never authenticate with no configured secret.
	t.Setenv("INTERNAL_TOKEN", "")
	m := NewWithChecker(nil, nil)

	// Caller presents a non-empty token but server has none configured.
	w := httptest.NewRecorder()
	m.Wrap(okHandler()).ServeHTTP(w, internalRequest("anything"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty expected token with non-empty provided must reject (401), got %d", w.Code)
	}

	// Caller also presents an empty token: still reject (no empty==empty accept).
	w = httptest.NewRecorder()
	m.Wrap(okHandler()).ServeHTTP(w, internalRequest(""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty expected token with empty provided must reject (401), got %d", w.Code)
	}
}
