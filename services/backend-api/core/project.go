package core

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

func RegisterProjectRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/projects", handleProjects(db))
	mux.HandleFunc("/api/projects/", handleProjectByID(db))
}

// callerIdentity resolves the bearer caller's userID and global role. The
// project routes are NOT in the authmw public allowlist, so the middleware has
// already proven the token is valid before we get here — but we re-verify to
// learn *which* user is calling, which membership filtering depends on.
func callerIdentity(db *sql.DB, r *http.Request) (userID, role string, ok bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	uid, err := authmw.VerifyToken(token)
	if err != nil {
		return "", "", false
	}
	_ = db.QueryRow(`SELECT role FROM "user" WHERE id = $1 AND is_active = true`, uid).Scan(&role)
	return uid, role, true
}

func handleProjects(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		userID, role, ok := callerIdentity(db, r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			JsonResp(w, M{"data": []M{}, "total": 0, "error": "unauthorized"})
			return
		}

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			name := StrVal(body, "name")
			var exists bool
			db.QueryRow(`SELECT EXISTS(SELECT 1 FROM project WHERE name=$1)`, name).Scan(&exists)
			if exists {
				JsonResp(w, M{"success": false, "error": "项目名称已存在"})
				return
			}
			// Owner is the creating user — NOT a hardcoded admin. The creator is
			// also added to project_member as 'owner' so the membership-filtered
			// list (below) and UserCanAccessProject see them immediately.
			var id string
			err := db.QueryRow(`INSERT INTO project (name, description, owner_id, source_type, status)
				VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
				name, StrVal(body, "description"), userID, StrVal(body, "sourceType")).Scan(&id)
			if err != nil {
				JsonResp(w, M{"success": false, "error": err.Error()})
				return
			}
			if _, err := db.Exec(`INSERT INTO project_member (project_id, user_id, role)
				VALUES ($1, $2, 'owner') ON CONFLICT (project_id, user_id) DO NOTHING`, id, userID); err != nil {
				// Membership is the access boundary — if we can't record it, fail
				// loudly rather than create an orphaned, inaccessible project.
				db.Exec(`DELETE FROM project WHERE id = $1`, id)
				JsonResp(w, M{"success": false, "error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true, "data": M{"id": id, "name": name}})
			return
		}

		// List: admins see everything (global role bypass, mirroring
		// UserCanAccessProject); everyone else sees only projects they belong to
		// via project_member. This is the fix for the cross-user project leak.
		const cols = `p.id, p.name, p.description, p.owner_id, COALESCE(p.source_type,''), COALESCE(p.source_file,''),
			COALESCE(p.compatibility,0), p.status, p.created_at, p.updated_at`
		var rows *sql.Rows
		var err error
		if role == "admin" {
			rows, err = db.Query(`SELECT ` + cols + ` FROM project p ORDER BY p.created_at`)
		} else {
			rows, err = db.Query(`SELECT `+cols+` FROM project p
				JOIN project_member pm ON pm.project_id = p.id
				WHERE pm.user_id = $1 ORDER BY p.created_at`, userID)
		}
		if err != nil {
			JsonResp(w, M{"data": []M{}, "total": 0, "error": err.Error()})
			return
		}
		defer rows.Close()
		var projects []M
		for rows.Next() {
			var id, name, desc, ownerID, srcType, srcFile, status string
			var compat int
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &name, &desc, &ownerID, &srcType, &srcFile, &compat, &status, &createdAt, &updatedAt)
			projects = append(projects, M{
				"id": id, "name": name, "description": desc, "ownerId": ownerID,
				"sourceType": srcType, "sourceFile": srcFile, "compatibility": compat,
				"status": status, "createdAt": createdAt.Format(time.RFC3339), "updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if projects == nil {
			projects = []M{}
		}
		ListResp(w, projects, len(projects))
	}
}

func handleProjectByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/projects")

		if r.Method == http.MethodDelete {
			// Only the project owner (or a global admin) may delete it. Without
			// this, any authenticated user could delete another user's project
			// by id (IDOR). EnforceEntityOwner resolves project.owner_id and
			// writes 404 on a non-owner/non-admin caller.
			if !authmw.EnforceEntityOwner(w, r, db, "project", "id", "owner_id", id) {
				return
			}
			// Clean up references that may not have ON DELETE CASCADE
			db.Exec(`DELETE FROM graph_node_position WHERE project_id = $1`, id)
			_, err := db.Exec(`DELETE FROM project WHERE id = $1`, id)
			if err != nil {
				w.WriteHeader(400)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		http.NotFound(w, r)
	}
}
