package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lakehouse2ontology/authmw"
)

// adminOnly wraps a handler so only authenticated users with role='admin'
// reach it. authmw already validated the bearer token, but it does NOT
// distinguish roles — `/api/ontology/_debug/*` and similar maintenance
// endpoints need that extra check.
//
// Returns 403 on any non-admin caller. The token itself must still be
// valid; otherwise authmw's outer wrap already returned 401.
func adminOnly(db *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		userID, err := authmw.VerifyToken(token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		var role string
		if err := db.QueryRowContext(r.Context(),
			`SELECT role FROM "user" WHERE id = $1 AND is_active = true`, userID).Scan(&role); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
			return
		}
		next.ServeHTTP(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
