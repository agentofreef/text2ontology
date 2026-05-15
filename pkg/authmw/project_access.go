package authmw

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// ErrNoProjectAccess is returned when a caller tries to operate on a
// project they aren't a member of. Treat as 403 at the HTTP edge.
var ErrNoProjectAccess = errors.New("user has no access to this project")

// EnforceProjectFromRequest is the one-liner POST/body handlers call to
// confirm the caller has access to the named project. Returns true on
// success; on failure it has already written a 401/403 response and the
// caller should `return` immediately.
//
// Why this lives in authmw rather than each handler: we want exactly
// one path for "extract user from token + check project membership" so
// every audit log entry has the same shape and every error path emits
// the same {"error":"forbidden"} body.
func EnforceProjectFromRequest(w http.ResponseWriter, r *http.Request, db *sql.DB, projectID string) bool {
	if projectID == "" {
		writeForbidden(w)
		return false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	userID, err := VerifyToken(token)
	if err != nil {
		writeUnauthorized(w)
		return false
	}
	if err := UserCanAccessProject(r.Context(), db, userID, projectID); err != nil {
		writeForbidden(w)
		return false
	}
	return true
}

// UserCanAccessProject returns nil when the user is either an admin
// (system-wide bypass) or a project_member of the given project.
// Anything else returns ErrNoProjectAccess so the caller can render a
// generic 403 without leaking project existence.
//
// Pure read; safe to call in middleware. Two short index hits — admin
// role check + composite-PK membership check — both O(1) under load.
func UserCanAccessProject(ctx context.Context, db *sql.DB, userID, projectID string) error {
	if userID == "" || projectID == "" {
		return ErrNoProjectAccess
	}

	// Admin bypass — global role, not per-project.
	var role string
	if err := db.QueryRowContext(ctx, `SELECT role FROM "user" WHERE id = $1 AND is_active = true`, userID).Scan(&role); err != nil {
		return ErrNoProjectAccess
	}
	if role == "admin" {
		return nil
	}

	// Membership check.
	var ok bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM project_member WHERE project_id = $1 AND user_id = $2)`,
		projectID, userID).Scan(&ok)
	if err != nil || !ok {
		return ErrNoProjectAccess
	}
	return nil
}
