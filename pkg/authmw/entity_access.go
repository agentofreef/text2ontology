package authmw

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

// EnforceEntityProject is the by-id chokepoint for public /api/* handlers.
// Given an entity that lives in `table` keyed by `idCol`, it resolves the
// entity's project_id and confirms the bearer-authenticated caller can
// access that project. Returns true on success; on any failure it has
// already written the response and the caller must `return` immediately.
//
// Why this exists: pkg/authmw middleware only enforces project access when
// `?projectId=` is in the query string. By-id endpoints carry the entity
// UUID in the path with no projectId, so without this check any
// authenticated user could read/mutate another project's data by passing a
// foreign entity UUID (IDOR). Spec: .omc/specs/deep-interview-idor-isolation.md.
//
// `table` and `idCol` MUST be compile-time constants supplied by the
// handler — never user input — because they are interpolated into SQL.
func EnforceEntityProject(w http.ResponseWriter, r *http.Request, db *sql.DB, table, idCol, id string) bool {
	// #nosec G201 -- table/idCol are handler-supplied constants, never user input.
	return EnforceEntityProjectVia(w, r, db,
		fmt.Sprintf(`SELECT project_id FROM %s WHERE %s = $1`, table, idCol), id)
}

// EnforceEntityProjectVia is the general form for entities whose project_id
// lives on a parent row: pass a constant SQL string that selects exactly one
// project_id column given the entity id as $1 (e.g. a JOIN from a child table
// up to ont_test_suite). Same contract as EnforceEntityProject.
//
// `resolveSQL` MUST be a compile-time constant — never built from user input.
func EnforceEntityProjectVia(w http.ResponseWriter, r *http.Request, db *sql.DB, resolveSQL, id string) bool {
	// Internal service-to-service calls (/internal/*) are authenticated by
	// INTERNAL_TOKEN at the middleware; per-project authorization is the
	// calling service's responsibility. Discriminate by request path, NOT by
	// the presence of X-Internal-Token (which an attacker could forge on an
	// /api/* request) — the path is what the router actually dispatched on.
	if strings.HasPrefix(r.URL.Path, "/internal/") {
		return true
	}

	if id == "" {
		writeNotFound(w)
		return false
	}

	var projectID string
	if err := db.QueryRowContext(r.Context(), resolveSQL, id).Scan(&projectID); err != nil {
		// Entity does not exist (or query failed). Return 404 either way so we
		// never confirm the existence of an entity in another project.
		writeNotFound(w)
		return false
	}

	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	userID, err := VerifyToken(token)
	if err != nil {
		writeUnauthorized(w)
		return false
	}

	if err := UserCanAccessProject(r.Context(), db, userID, projectID); err != nil {
		// 404, NOT 403: a forbidden response would confirm the entity exists.
		// For a cross-project IDOR probe, "not found" leaks nothing.
		writeNotFound(w)
		return false
	}
	return true
}

// EnforceProjectAccess gates a public handler that already knows the project
// id (typically read from the request body, which the middleware does NOT
// gate — it only gates the `?projectId=` query string). Bypasses /internal/*
// service calls (middleware-authenticated). For /api/*, verifies the bearer
// caller can access projectID; writes 401/403 and returns false on failure.
func EnforceProjectAccess(w http.ResponseWriter, r *http.Request, db *sql.DB, projectID string) bool {
	if strings.HasPrefix(r.URL.Path, "/internal/") {
		return true
	}
	return EnforceProjectFromRequest(w, r, db, projectID)
}

// EnforceEntityOwner gates a per-user (not per-project) entity such as an MCP
// API key: it resolves the owning user_id from `table` and allows the request
// only when the bearer caller IS that owner or is a global admin. Same
// write-and-return-false contract; 404 on missing/unauthorized so ownership
// isn't leaked. `table`/`idCol`/`ownerCol` MUST be handler constants.
func EnforceEntityOwner(w http.ResponseWriter, r *http.Request, db *sql.DB, table, idCol, ownerCol, id string) bool {
	if strings.HasPrefix(r.URL.Path, "/internal/") {
		return true
	}
	if id == "" {
		writeNotFound(w)
		return false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	userID, err := VerifyToken(token)
	if err != nil {
		writeUnauthorized(w)
		return false
	}
	var ownerID string
	// #nosec G201 -- table/idCol/ownerCol are handler-supplied constants.
	q := fmt.Sprintf(`SELECT %s::text FROM %s WHERE %s = $1`, ownerCol, table, idCol)
	if err := db.QueryRowContext(r.Context(), q, id).Scan(&ownerID); err != nil {
		writeNotFound(w)
		return false
	}
	if ownerID == userID {
		return true
	}
	// Admin override (global role).
	var role string
	if err := db.QueryRowContext(r.Context(), `SELECT role FROM "user" WHERE id = $1 AND is_active = true`, userID).Scan(&role); err == nil && role == "admin" {
		return true
	}
	writeNotFound(w)
	return false
}

// RequireAdmin gates an admin-only /api/* handler. It verifies the bearer token
// and confirms the caller has the global admin role and an active account.
// Returns the caller's userID and true on success; on any failure it writes the
// response (401 for a bad/absent token, 403 for a non-admin) and returns false,
// so the caller must `return` immediately. Unlike the project chokepoints above,
// these endpoints don't expose per-project entities, so a plain 403 is fine —
// there is nothing to leak by confirming the route exists.
func RequireAdmin(w http.ResponseWriter, r *http.Request, db *sql.DB) (string, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	userID, err := VerifyToken(token)
	if err != nil {
		writeUnauthorized(w)
		return "", false
	}
	var role string
	err = db.QueryRowContext(r.Context(),
		`SELECT role FROM "user" WHERE id = $1 AND is_active = true`, userID).Scan(&role)
	if err != nil || role != "admin" {
		writeForbidden(w)
		return "", false
	}
	return userID, true
}

func writeNotFound(w http.ResponseWriter) {
	jsonHeader(w)
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `{"error":"not found"}`)
}
