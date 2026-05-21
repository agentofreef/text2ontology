package core

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

// RegisterAdminRoutes wires the admin-only user-management surface under
// /api/admin/*. Every handler calls authmw.RequireAdmin first, so a non-admin
// (or unauthenticated) caller gets 403/401 before any work happens. These
// endpoints ride the normal /api/* bearer middleware too, but RequireAdmin is
// the authoritative gate — the middleware only proves the token is valid, not
// that the caller is an admin.
func RegisterAdminRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/admin/users", handleAdminUsers(db))
	mux.HandleFunc("/api/admin/users/", handleAdminUserByID(db))
	mux.HandleFunc("/api/admin/settings", handleAdminSettings(db))
}

// countActiveAdmins returns how many active admin accounts exist. Used to keep
// the last admin from being demoted, disabled, or deleted (which would lock
// everyone out of the admin surface).
func countActiveAdmins(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM "user" WHERE role = 'admin' AND is_active = true`).Scan(&n)
	return n, err
}

// handleAdminUsers — GET /api/admin/users. Lists every account with the key
// fields the management UI shows, including each user's project membership
// count (LEFT JOIN so users in no project still appear with 0).
func handleAdminUsers(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if _, ok := authmw.RequireAdmin(w, r, db); !ok {
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		rows, err := db.Query(`
			SELECT u.id, u.username, COALESCE(u.display_name,''), u.role, u.is_active, u.created_at,
			       COUNT(pm.project_id) AS project_count
			FROM "user" u
			LEFT JOIN project_member pm ON pm.user_id = u.id
			GROUP BY u.id
			ORDER BY u.created_at ASC`)
		if err != nil {
			log.Printf("[admin] list users failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"error": "查询失败"})
			return
		}
		defer rows.Close()

		users := []M{}
		for rows.Next() {
			var id, username, displayName, role string
			var isActive bool
			var createdAt time.Time
			var projectCount int
			if err := rows.Scan(&id, &username, &displayName, &role, &isActive, &createdAt, &projectCount); err != nil {
				log.Printf("[admin] scan user failed: %v", err)
				continue
			}
			users = append(users, M{
				"id":           id,
				"username":     username,
				"displayName":  displayName,
				"role":         role,
				"isActive":     isActive,
				"createdAt":    createdAt.Format(time.RFC3339),
				"projectCount": projectCount,
			})
		}
		JsonResp(w, M{"data": users})
	}
}

// handleAdminUserByID dispatches the per-user routes:
//
//	PATCH  /api/admin/users/{id}                  → role / is_active
//	DELETE /api/admin/users/{id}                  → delete account
//	POST   /api/admin/users/{id}/reset-password   → set a new password
func handleAdminUserByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		callerID, ok := authmw.RequireAdmin(w, r, db)
		if !ok {
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
		if rest == "" {
			w.WriteHeader(http.StatusNotFound)
			JsonResp(w, M{"error": "missing user id"})
			return
		}

		if strings.HasSuffix(rest, "/reset-password") {
			id := strings.TrimSuffix(rest, "/reset-password")
			adminResetPassword(w, r, db, id)
			return
		}

		// Path is the bare user id.
		id := rest
		switch r.Method {
		case http.MethodPatch:
			adminUpdateUser(w, r, db, callerID, id)
		case http.MethodDelete:
			adminDeleteUser(w, r, db, callerID, id)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// adminUpdateUser applies role and/or is_active changes, refusing edits that
// would lock the caller out (modifying their own role/status) or remove the
// last active admin.
func adminUpdateUser(w http.ResponseWriter, r *http.Request, db *sql.DB, callerID, id string) {
	var curRole string
	var curActive bool
	if err := db.QueryRow(`SELECT role, is_active FROM "user" WHERE id = $1`, id).Scan(&curRole, &curActive); err != nil {
		w.WriteHeader(http.StatusNotFound)
		JsonResp(w, M{"success": false, "error": "用户不存在"})
		return
	}

	body := ReadBody(r)
	newRole := curRole
	if _, ok := body["role"]; ok {
		newRole = StrVal(body, "role")
		if newRole != "user" && newRole != "admin" {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"success": false, "error": "无效的角色"})
			return
		}
	}
	newActive := curActive
	if _, ok := body["isActive"]; ok {
		newActive = BoolVal(body, "isActive")
	}

	// Self-protection: don't let an admin demote or disable their own account.
	if id == callerID {
		if newRole != "admin" || !newActive {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"success": false, "error": "不能修改自己的角色或禁用自己"})
			return
		}
	}

	// Last-admin protection: block any change that removes the final active
	// admin (demotion to user, or disabling).
	losingAdmin := curRole == "admin" && curActive && (newRole != "admin" || !newActive)
	if losingAdmin {
		n, err := countActiveAdmins(db)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"success": false, "error": "服务器错误"})
			return
		}
		if n <= 1 {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"success": false, "error": "至少保留一个启用的管理员"})
			return
		}
	}

	if _, err := db.Exec(
		`UPDATE "user" SET role = $1, is_active = $2, updated_at = now() WHERE id = $3`,
		newRole, newActive, id); err != nil {
		log.Printf("[admin] update user failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		JsonResp(w, M{"success": false, "error": "更新失败"})
		return
	}
	JsonResp(w, M{"success": true})
}

// adminDeleteUser removes an account. project_member rows cascade, but
// project.owner_id has no ON DELETE, so a user who still owns projects can't be
// deleted — we surface that as a clear 400 rather than a raw FK error.
func adminDeleteUser(w http.ResponseWriter, r *http.Request, db *sql.DB, callerID, id string) {
	if id == callerID {
		w.WriteHeader(http.StatusBadRequest)
		JsonResp(w, M{"success": false, "error": "不能删除自己"})
		return
	}

	var role string
	var isActive bool
	if err := db.QueryRow(`SELECT role, is_active FROM "user" WHERE id = $1`, id).Scan(&role, &isActive); err != nil {
		w.WriteHeader(http.StatusNotFound)
		JsonResp(w, M{"success": false, "error": "用户不存在"})
		return
	}

	if role == "admin" && isActive {
		n, err := countActiveAdmins(db)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"success": false, "error": "服务器错误"})
			return
		}
		if n <= 1 {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"success": false, "error": "至少保留一个启用的管理员"})
			return
		}
	}

	var owned int
	if err := db.QueryRow(`SELECT COUNT(*) FROM project WHERE owner_id = $1`, id).Scan(&owned); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		JsonResp(w, M{"success": false, "error": "服务器错误"})
		return
	}
	if owned > 0 {
		w.WriteHeader(http.StatusBadRequest)
		JsonResp(w, M{"success": false, "error": "该用户仍拥有项目，请先转移或删除其项目"})
		return
	}

	if _, err := db.Exec(`DELETE FROM "user" WHERE id = $1`, id); err != nil {
		log.Printf("[admin] delete user failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		JsonResp(w, M{"success": false, "error": "删除失败"})
		return
	}
	JsonResp(w, M{"success": true})
}

// adminResetPassword sets a new password for any account. No current-password
// check — this is the admin override path, distinct from /api/auth/change-password.
func adminResetPassword(w http.ResponseWriter, r *http.Request, db *sql.DB, id string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body := ReadBody(r)
	next := StrVal(body, "newPassword")
	if len(next) < 6 {
		w.WriteHeader(http.StatusBadRequest)
		JsonResp(w, M{"success": false, "error": "新密码至少 6 个字符"})
		return
	}
	hash, err := authmw.HashPassword(next)
	if err != nil {
		log.Printf("[admin] reset-password hash failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		JsonResp(w, M{"success": false, "error": "服务器错误"})
		return
	}
	res, err := db.Exec(`UPDATE "user" SET password_hash = $1, updated_at = now() WHERE id = $2`, hash, id)
	if err != nil {
		log.Printf("[admin] reset-password update failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		JsonResp(w, M{"success": false, "error": "更新失败"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		w.WriteHeader(http.StatusNotFound)
		JsonResp(w, M{"success": false, "error": "用户不存在"})
		return
	}
	JsonResp(w, M{"success": true})
}

// handleAdminSettings exposes the global app_setting toggle(s) the admin UI
// controls. Currently just allow_registration.
//
//	GET /api/admin/settings → {"allowRegistration": bool}
//	PUT /api/admin/settings → body {"allowRegistration": bool}
func handleAdminSettings(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if _, ok := authmw.RequireAdmin(w, r, db); !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			JsonResp(w, M{"allowRegistration": registrationAllowed(db)})
		case http.MethodPut:
			body := ReadBody(r)
			val := "false"
			if BoolVal(body, "allowRegistration") {
				val = "true"
			}
			if _, err := db.Exec(
				`INSERT INTO app_setting (key, value, updated_at)
				 VALUES ('allow_registration', $1, now())
				 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				val); err != nil {
				log.Printf("[admin] update settings failed: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				JsonResp(w, M{"success": false, "error": "更新失败"})
				return
			}
			JsonResp(w, M{"success": true, "allowRegistration": val == "true"})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
