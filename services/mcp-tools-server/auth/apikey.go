// Package auth is the MCP-specific bearer-token middleware. Keys live
// in the `mcp_api_key` table (key_hash = lowercase SHA-256 hex of the
// raw key); the service never stores or logs raw keys. Keys are loaded
// once at process start into a read-through cache that refreshes on
// miss, so adding/revoking keys only requires a SQL UPDATE — no
// restart.
//
// Auth contract:
//
//	Authorization: Bearer <key>     (preferred)
//	X-API-Key: <key>                (alternative)
//
// Missing / wrong key → 401 with JSON {"error":"unauthorized: …"}.
//
// Bootstrap: if MCP_API_KEY env is set at Init() time, its hash is
// upserted as a `bootstrap` row in mcp_api_key so the first operator
// has a working key. After that env var can be removed; keys are
// managed via SQL.
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/services/mcp-tools-server/client"
)

// CtxAllowedToolsKey stores the caller's allowed_tools slice on
// r.Context() so tool-call dispatchers can enforce per-key permissions
// without round-tripping the DB again.
type ctxPermKey struct{}

// AllowedToolsKey is the context key type for the per-request
// allowed_tools slice (nil = admin/all, empty = none, list = whitelist).
var AllowedToolsKey = ctxPermKey{}

// cacheEntry holds a positive auth result with an expiry. Negative
// results are not cached so revoked keys lose access immediately after
// the cache entry expires (no poison-cache window).
//
// allowedTools: nil means admin (all tools); a non-nil slice (possibly
// empty) is the exact whitelist applied in the dispatcher.
type cacheEntry struct {
	label         string
	allowedTools  []string // nil = all allowed; non-nil = whitelist
	allowedIsNull bool
	expiresAt     time.Time
}

// Authenticator holds the DB handle + a short-TTL cache of active key
// hashes. TTL is 30s: bounds the revocation propagation window so
// operators don't need a process restart to revoke a compromised key.
//
// On hit (unexpired): O(1) map read, no Postgres.
// On miss or expired: single SELECT against mcp_api_key.
type Authenticator struct {
	db            *sql.DB
	cache         sync.Map // key_hash(string) → cacheEntry
	bootstrapOnce sync.Once
}

// cacheTTL is how long a successful auth stays trusted before being
// re-validated against the DB. Short enough that revocation is
// effectively immediate; long enough that a burst of requests from
// the same consumer doesn't hammer Postgres.
const cacheTTL = 30 * time.Second

// New returns an Authenticator wired to db. If MCP_API_KEY is set, the
// bootstrap key row is upserted at the first authentication (lazy so
// startup failure modes are clearer).
func New(db *sql.DB) *Authenticator { return &Authenticator{db: db} }

// hashKey returns the lowercase hex SHA-256 of the raw API key. Same
// algorithm used for DB seeding so equality comparison works.
func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// bootstrap upserts the key derived from MCP_API_KEY env if any rows
// are missing. Runs once per process (sync.Once) and swallows errors
// to log rather than crashing startup — the operator can still add
// keys via SQL if bootstrap fails.
func (a *Authenticator) bootstrap(ctx context.Context) {
	a.bootstrapOnce.Do(func() {
		raw := os.Getenv("MCP_API_KEY")
		if raw == "" {
			return
		}
		h := hashKey(raw)
		_, err := a.db.ExecContext(ctx, `
			INSERT INTO mcp_api_key (key_hash, label)
			VALUES ($1, 'bootstrap')
			ON CONFLICT (key_hash) DO NOTHING`, h)
		if err != nil {
			log.Printf("mcp-auth: bootstrap upsert failed: %v (continuing; add keys via SQL)", err)
			return
		}
		log.Printf("mcp-auth: bootstrap key ensured (label=bootstrap, hash=%s...)", h[:12])
	})
}

// Lookup returns (entry, ok). Cache hit is only honored while unexpired;
// stale entries are removed and refetched so revocation propagates
// within cacheTTL. DB failure is treated as "not found" so misconfig
// doesn't accidentally grant access.
//
// The returned cacheEntry carries both label and allowed_tools so
// permission enforcement happens without a second DB hop.
func (a *Authenticator) Lookup(ctx context.Context, raw string) (cacheEntry, bool) {
	h := hashKey(raw)
	if v, ok := a.cache.Load(h); ok {
		ent := v.(cacheEntry)
		if time.Now().Before(ent.expiresAt) {
			return ent, true
		}
		a.cache.Delete(h)
	}
	var label string
	var allowed pq.StringArray
	var allowedNull bool
	err := a.db.QueryRowContext(ctx, `
		SELECT COALESCE(label,''), allowed_tools, allowed_tools IS NULL
		FROM mcp_api_key
		WHERE key_hash = $1 AND revoked_at IS NULL`, h,
	).Scan(&label, &allowed, &allowedNull)
	if err != nil {
		return cacheEntry{}, false
	}
	ent := cacheEntry{
		label:         label,
		allowedIsNull: allowedNull,
		expiresAt:     time.Now().Add(cacheTTL),
	}
	if !allowedNull {
		ent.allowedTools = []string(allowed)
	}
	a.cache.Store(h, ent)
	return ent, true
}

// touchLastUsed is a best-effort UPDATE of last_used_at for auditing.
// Errors are logged and swallowed — auth already succeeded.
func (a *Authenticator) touchLastUsed(ctx context.Context, raw string) {
	h := hashKey(raw)
	if _, err := a.db.ExecContext(ctx,
		`UPDATE mcp_api_key SET last_used_at = now() WHERE key_hash = $1 AND revoked_at IS NULL`, h,
	); err != nil {
		log.Printf("mcp-auth: touchLastUsed failed: %v", err)
	}
}

// Middleware produces the http.Handler wrapper. /healthz and /metrics
// are intentionally bypassed so liveness probes don't need credentials.
func (a *Authenticator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}
			a.bootstrap(r.Context())

			raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if raw == "" {
				raw = r.Header.Get("X-API-Key")
			}
			if raw == "" {
				unauth(w)
				return
			}
			ent, ok := a.Lookup(r.Context(), raw)
			if !ok {
				unauth(w)
				return
			}

			// Attach caller label + allowed_tools to ctx so (a) outbound
			// clients record the caller in audit logs and (b) tool
			// dispatchers enforce per-key permissions without another
			// DB lookup. allowed_tools=nil means admin (no restriction).
			ctx := context.WithValue(r.Context(), client.CallerKey, "mcp:"+ent.label)
			if !ent.allowedIsNull {
				ctx = context.WithValue(ctx, AllowedToolsKey, ent.allowedTools)
			}
			// Best-effort last_used_at touch in background — do not
			// block the request on DB write.
			go func(raw string) {
				ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				a.touchLastUsed(ctx2, raw)
			}(raw)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func unauth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized: provide Authorization: Bearer <api-key> or X-API-Key header"}`))
}
