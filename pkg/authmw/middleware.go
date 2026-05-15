package authmw

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// UserChecker abstracts the active-user lookup so Middleware can be tested
// without a real database. The default implementation is SQLUserChecker.
type UserChecker interface {
	IsActive(ctx context.Context, userID string) (bool, error)
}

// SQLUserChecker implements UserChecker against the `user` table.
type SQLUserChecker struct {
	DB *sql.DB
}

// IsActive returns true when the user row exists and is_active = true.
func (c *SQLUserChecker) IsActive(ctx context.Context, userID string) (bool, error) {
	var isActive bool
	err := c.DB.QueryRowContext(ctx, `SELECT is_active FROM "user" WHERE id = $1`, userID).Scan(&isActive)
	if err != nil {
		return false, err
	}
	return isActive, nil
}

// statusRecorder wraps http.ResponseWriter to capture the final HTTP status code
// written by the downstream handler. Used so the audit log records the real code.
//
// Must also forward http.Flusher so SSE endpoints (e.g. agent-server's
// /internal/agent/stream) keep working when wrapped by auth middleware.
// Without the explicit Flush method, the type assertion
// `w.(http.Flusher)` inside the SSE handler fails (interface embedding
// is NOT interface re-implementation in Go), and the handler falls
// back to a 500 "streaming not supported". Discovered during Phase 2
// A2 smoke test.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.code = code
	sr.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the wrapped ResponseWriter's Flusher if present,
// a silent no-op otherwise. Presence is checked at each call rather
// than stored, keeping the struct itself a plain value.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// statusCode returns the captured code, defaulting to 200 if WriteHeader was never called.
func (sr *statusRecorder) statusCode() int {
	if sr.code == 0 {
		return http.StatusOK
	}
	return sr.code
}

// Middleware holds dependencies for bearer-token auth and internal-call audit.
type Middleware struct {
	checker     UserChecker
	db          *sql.DB // for project_member access checks; may be nil in tests
	AuditWriter AuditWriter
}

// New constructs a Middleware. w may be nil; in that case internal calls are
// rejected with 401 rather than logged (safe-fail).
// Existing callers pass *sql.DB; New wraps it in SQLUserChecker internally.
// The same *sql.DB is also used for the project_member access check
// invoked on every /api/* request that carries a ?projectId= query.
func New(db *sql.DB, w AuditWriter) *Middleware {
	return &Middleware{checker: &SQLUserChecker{DB: db}, db: db, AuditWriter: w}
}

// NewWithChecker constructs a Middleware with an explicit UserChecker (for tests).
// db remains nil; project-access enforcement is skipped (tests that need it
// should construct a real Middleware against an sqlite/postgres test DB).
func NewWithChecker(checker UserChecker, w AuditWriter) *Middleware {
	return &Middleware{checker: checker, AuditWriter: w}
}

// Wrap returns an http.Handler that enforces auth before calling next.
//
// Routing rules (mirror services/backend-api/cmd/server/main.go:254-281 exactly):
//   - OPTIONS and non-/api/* paths pass through without auth.
//   - /api/auth/login and /api/health are public.
//   - /internal/* paths require X-Internal-Token matching the INTERNAL_TOKEN env var
//     AND X-On-Behalf-Of header identifying the proxied user. An audit entry is
//     emitted on every accepted internal call (§3.5.4).
//   - All other /api/* paths require a Bearer token of the form "bearer-<userID>"
//     with an active row in the `user` table.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Internal service-to-service calls under /internal/* are checked
		// FIRST — they are independent of the /api/ prefix and must never
		// ride the non-/api/ pass-through (that would silently route
		// them as static-asset requests).
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			m.handleInternal(w, r, next)
			return
		}

		// Pass OPTIONS and non-API paths through unconditionally.
		if r.Method == http.MethodOptions ||
			!strings.HasPrefix(r.URL.Path, "/api/") ||
			r.URL.Path == "/api/auth/login" ||
			r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}

		// HMAC-signed bearer token: <userID>.<expiryUnix>.<sig>.
		// VerifyToken handles signature, expiry, and shape; any failure
		// surfaces as 401 — never leak the specific reason to the wire.
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		userID, err := VerifyToken(token)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		isActive, err := m.checker.IsActive(r.Context(), userID)
		if err != nil || !isActive {
			writeUnauthorized(w)
			return
		}

		// Project-access enforcement. Catches every /api/* call that
		// passes ?projectId=<uuid> in the query string. POST bodies
		// with projectId fields still need handler-level checks via
		// UserCanAccessProject — but query-string is the bulk of
		// read traffic and the highest-value chokepoint to gate here.
		if pid := r.URL.Query().Get("projectId"); pid != "" && m.db != nil {
			if err := UserCanAccessProject(r.Context(), m.db, userID, pid); err != nil {
				writeForbidden(w)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleInternal enforces §3.5.4 rules for /internal/* paths:
//   - X-Internal-Token must match INTERNAL_TOKEN env var (fail-closed: 401).
//   - X-On-Behalf-Of must be present (fail with 400 — all internal calls must attribute to a user).
//   - On success, wrap the writer to capture the real status code, call next, then emit audit log.
func (m *Middleware) handleInternal(w http.ResponseWriter, r *http.Request, next http.Handler) {
	expected := os.Getenv("INTERNAL_TOKEN")
	provided := r.Header.Get("X-Internal-Token")
	if expected == "" || provided != expected {
		jsonHeader(w)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
		return
	}
	onBehalf := r.Header.Get("X-On-Behalf-Of")
	if onBehalf == "" {
		jsonHeader(w)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"X-On-Behalf-Of required for internal calls"}`)
		return
	}

	// Wrap the writer to capture the real status code written by downstream.
	rec := &statusRecorder{ResponseWriter: w}
	next.ServeHTTP(rec, r)

	if m.AuditWriter != nil {
		_ = m.AuditWriter.WriteAudit(r.Context(), AuditEvent{
			Timestamp:     timeNow(),
			RequestID:     r.Header.Get("X-Request-ID"),
			CallerService: r.Header.Get("X-Caller-Service"),
			OnBehalfOf:    onBehalf,
			ProjectID:     r.Header.Get("X-Project-ID"),
			Path:          r.URL.Path,
			Method:        r.Method,
			StatusCode:    rec.statusCode(),
		})
	}
}

func writeUnauthorized(w http.ResponseWriter) {
	jsonHeader(w)
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(w, `{"error":"unauthorized"}`)
}

func writeForbidden(w http.ResponseWriter) {
	jsonHeader(w)
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, `{"error":"forbidden"}`)
}

// jsonHeader sets Content-Type to application/json on the response.
func jsonHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
}
