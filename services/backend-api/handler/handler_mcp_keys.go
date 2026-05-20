package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

// ============================================================================
// MCP API keys — per-user CRUD for external clients (Claude Code / MCP SDK).
//
// Auth model: /api/* is gated by authmw which requires a `Authorization: Bearer <token>` header
// where token is an HMAC-signed `<userID>.<exp>.<sig>` (see pkg/authmw/token.go).
// handlers re-parse the bearer to get userID (authmw doesn't put it in context).
// Users can only list / create / revoke their own rows (user_id = caller).
//
// Raw keys are minted server-side and returned exactly once. The DB stores
// only the SHA-256 hex hash, matching services/mcp-tools-server/auth/apikey.go.
//
// Routes (registered in cmd/server/main.go):
//   GET    /api/ontology/mcp-keys          — list caller's active keys
//   POST   /api/ontology/mcp-keys          — mint a new key (body: {label, allowedTools?})
//   DELETE /api/ontology/mcp-keys/{id}     — revoke (mark revoked_at=now())
// ============================================================================

// ALLOWED_TOOL_NAMES mirrors services/mcp-tools-server/tools/tools.go.
// Keep in sync when new MCP tools are added.
var allowedToolNames = map[string]bool{
	"lookup_od":          true,
	"execute_smartquery": true,
	"recall_tokens":      true,
}

func userIDFromRequest(r *http.Request) string {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	id, err := authmw.VerifyToken(token)
	if err != nil {
		return ""
	}
	return id
}

func mintRawKey() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to panic — server can't mint keys, refuse to start serving
		panic("mcp-keys: crypto/rand failed: " + err.Error())
	}
	// URL-safe base64 without padding, prefixed for discoverability.
	return "mcp_" + base64.RawURLEncoding.EncodeToString(buf)
}

func hashRawKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func handleMCPKeys(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		userID := userIDFromRequest(r)
		if userID == "" {
			w.WriteHeader(401)
			JsonResp(w, M{"error": "未认证"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			listKeys(w, db, userID)
		case http.MethodPost:
			createKey(w, r, db, userID)
		default:
			w.WriteHeader(405)
			JsonResp(w, M{"error": "method not allowed"})
		}
	}
}

func handleMCPKeyByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		userID := userIDFromRequest(r)
		if userID == "" {
			w.WriteHeader(401)
			JsonResp(w, M{"error": "未认证"})
			return
		}

		// Prefix-agnostic: the route can arrive at either /internal/backend-api/mcp-keys/{id}
		// or /api/ontology/mcp-keys/{id}. Both have "/mcp-keys/" as the discriminator.
		idx := strings.LastIndex(r.URL.Path, "/mcp-keys/")
		var id string
		if idx >= 0 {
			id = strings.TrimPrefix(r.URL.Path[idx+len("/mcp-keys/"):], "/")
			// strip any trailing sub-paths defensively
			if slash := strings.IndexByte(id, '/'); slash >= 0 {
				id = id[:slash]
			}
		}
		if id == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "missing id"})
			return
		}
		// Cross-user IDOR guard: only the owning user (or a global admin) may
		// operate on this key. Bypasses /internal/* service calls.
		if !authmw.EnforceEntityOwner(w, r, db, "mcp_api_key", "id", "user_id", id) {
			return
		}

		if r.Method != http.MethodDelete {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "method not allowed"})
			return
		}

		// Soft delete — keeps audit trail. Scope by user_id so other users
		// can't revoke keys they don't own.
		res, err := db.ExecContext(r.Context(), `
			UPDATE mcp_api_key SET revoked_at = now()
			WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		`, id, userID)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			w.WriteHeader(404)
			JsonResp(w, M{"error": "key 不存在或已撤销"})
			return
		}
		JsonResp(w, M{"ok": true})
	}
}

func listKeys(w http.ResponseWriter, db *sql.DB, userID string) {
	rows, err := db.Query(`
		SELECT id, label,
		       COALESCE(allowed_tools, ARRAY[]::TEXT[]) AS allowed_tools,
		       created_at, last_used_at
		FROM mcp_api_key
		WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	defer rows.Close()

	out := []M{}
	for rows.Next() {
		var id, label string
		var tools pq.StringArray
		var createdAt sql.NullTime
		var lastUsedAt sql.NullTime
		if err := rows.Scan(&id, &label, &tools, &createdAt, &lastUsedAt); err != nil {
			continue
		}
		row := M{
			"id":           id,
			"label":        label,
			"allowedTools": []string(tools),
			"createdAt":    timeOrNil(createdAt),
			"lastUsedAt":   timeOrNil(lastUsedAt),
		}
		out = append(out, row)
	}
	JsonResp(w, M{"data": out})
}

func createKey(w http.ResponseWriter, r *http.Request, db *sql.DB, userID string) {
	body := ReadBody(r)
	label := strings.TrimSpace(StrVal(body, "label"))
	if label == "" {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "label 必填"})
		return
	}
	if len(label) > 64 {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "label 不能超过 64 字符"})
		return
	}

	// allowedTools: nil/missing → NULL (admin-style all tools),
	// [] → lockdown (auth rejects every tool), [...] → whitelist.
	var allowedTools pq.StringArray
	rawTools, hasTools := body["allowedTools"]
	if hasTools {
		arr, ok := rawTools.([]interface{})
		if !ok {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "allowedTools 必须是字符串数组"})
			return
		}
		for _, v := range arr {
			s, _ := v.(string)
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if !allowedToolNames[s] {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "未知 tool: " + s})
				return
			}
			allowedTools = append(allowedTools, s)
		}
	}

	raw := mintRawKey()
	hash := hashRawKey(raw)

	var id string
	var toolsArg interface{} = allowedTools
	if !hasTools {
		toolsArg = nil // NULL = all tools
	}
	err := db.QueryRowContext(r.Context(), `
		INSERT INTO mcp_api_key (key_hash, label, allowed_tools, user_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, hash, label, toolsArg, userID).Scan(&id)
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}

	resp := M{
		"id":           id,
		"label":        label,
		"rawKey":       raw, // ← shown once; client must save it
		"allowedTools": []string(allowedTools),
	}
	if !hasTools {
		resp["allowedTools"] = nil
	}
	JsonResp(w, resp)
}

func timeOrNil(t sql.NullTime) interface{} {
	if !t.Valid {
		return nil
	}
	return t.Time
}
