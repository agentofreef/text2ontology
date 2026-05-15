package core

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

// RegisterAuthRoutes wires /api/auth/login and /api/auth/me.
// Both routes are exempted from authmw — login bootstraps the token,
// /me uses the same token format and verifies it inline.
func RegisterAuthRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/auth/login", handleLogin(db))
	mux.HandleFunc("/api/auth/me", handleMe(db))
	mux.HandleFunc("/api/auth/change-password", handleChangePassword(db))
}

func handleLogin(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		CorsHeaders(w)
		body := ReadBody(r)
		username := StrVal(body, "username")
		password := StrVal(body, "password")
		if username == "" || password == "" {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "用户名和密码不能为空"})
			return
		}

		var id, displayName, role, passwordHash string
		var isActive bool
		err := db.QueryRow(`SELECT id, COALESCE(display_name,''), role, is_active, password_hash FROM "user" WHERE username = $1`, username).
			Scan(&id, &displayName, &role, &isActive, &passwordHash)
		if err != nil {
			// Generic message — do not leak whether the username exists.
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "用户名或密码错误"})
			return
		}
		if !isActive {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "用户已禁用"})
			return
		}
		if err := authmw.VerifyPassword(password, passwordHash); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "用户名或密码错误"})
			return
		}

		token, err := authmw.SignToken(id)
		if err != nil {
			// AUTH_TOKEN_SECRET missing — operator misconfiguration.
			log.Printf("[auth] SignToken failed: %v (is AUTH_TOKEN_SECRET set?)", err)
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"success": false, "error": "服务器配置错误"})
			return
		}
		JsonResp(w, M{
			"success": true,
			"token":   token,
			"user":    M{"username": username, "displayName": displayName, "role": role},
		})
	}
}

func handleMe(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		userID, err := authmw.VerifyToken(token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"error": "unauthorized"})
			return
		}

		var username, displayName, role string
		err = db.QueryRow(`SELECT username, COALESCE(display_name,''), role FROM "user" WHERE id = $1 AND is_active = true`, userID).
			Scan(&username, &displayName, &role)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"error": "unauthorized"})
			return
		}
		JsonResp(w, M{"username": username, "displayName": displayName, "role": role})
	}
}

// handleChangePassword — POST /api/auth/change-password.
//
// Body: {"currentPassword": "...", "newPassword": "..."}.
// Auth: Bearer token (same format as the rest of /api/*). We verify the token
// inline rather than going through the global authmw because this handler is
// registered alongside /api/auth/login and /api/auth/me, which are also
// exempt from middleware to avoid bootstrap loops.
func handleChangePassword(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		CorsHeaders(w)

		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		userID, err := authmw.VerifyToken(token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "unauthorized"})
			return
		}

		body := ReadBody(r)
		current := StrVal(body, "currentPassword")
		next := StrVal(body, "newPassword")
		if current == "" || next == "" {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"success": false, "error": "当前密码与新密码不能为空"})
			return
		}
		if len(next) < 6 {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"success": false, "error": "新密码至少 6 个字符"})
			return
		}
		if current == next {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"success": false, "error": "新密码不能与当前密码相同"})
			return
		}

		var passwordHash string
		var isActive bool
		err = db.QueryRow(`SELECT password_hash, is_active FROM "user" WHERE id = $1`, userID).
			Scan(&passwordHash, &isActive)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "unauthorized"})
			return
		}
		if !isActive {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "用户已禁用"})
			return
		}
		if err := authmw.VerifyPassword(current, passwordHash); err != nil {
			// Mirror login's generic error — don't leak which field is wrong.
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"success": false, "error": "当前密码错误"})
			return
		}

		newHash, err := authmw.HashPassword(next)
		if err != nil {
			log.Printf("[auth] HashPassword failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"success": false, "error": "服务器错误"})
			return
		}
		if _, err := db.Exec(`UPDATE "user" SET password_hash = $1, updated_at = now() WHERE id = $2`,
			newHash, userID); err != nil {
			log.Printf("[auth] change-password update failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"success": false, "error": "更新失败"})
			return
		}
		JsonResp(w, M{"success": true})
	}
}

// AssertAuthEnv panics at startup if the required auth secrets are missing.
// Call this from main() before serving traffic — fail-closed, never silent.
func AssertAuthEnv() {
	if os.Getenv("AUTH_TOKEN_SECRET") == "" {
		log.Fatal("[auth] AUTH_TOKEN_SECRET environment variable must be set (generate with: openssl rand -hex 32)")
	}
}
