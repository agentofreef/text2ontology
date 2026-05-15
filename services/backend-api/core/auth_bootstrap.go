package core

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/lakehouse2ontology/authmw"
)

// BootstrapAdminIfNeeded runs once at startup. Behavior:
//
//   - If no user has password_hash == BootstrapSentinel, do nothing (admin
//     already configured, or operator pre-seeded a custom hash).
//   - If at least one user carries the sentinel, ADMIN_PASSWORD must be set;
//     we hash it and UPDATE every sentinel row. Missing ADMIN_PASSWORD is
//     a hard error — refuse to start rather than serve an unauthenticated
//     admin account.
//
// This intentionally does NOT create users. Schema seed inserts the admin
// row with the sentinel; this just swaps in a real hash on first boot.
func BootstrapAdminIfNeeded(db *sql.DB) error {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM "user" WHERE password_hash = $1`, authmw.BootstrapSentinel).Scan(&count)
	if err != nil {
		return fmt.Errorf("scan sentinel users: %w", err)
	}
	if count == 0 {
		return nil
	}

	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		return errors.New("found user(s) with BOOTSTRAP_REQUIRED password_hash but ADMIN_PASSWORD env is not set — set ADMIN_PASSWORD to install an admin password before first login")
	}
	// Warn loudly on weak defaults but allow them — the OSS distribution
	// ships with admin/admin for trivial first-run UX. Operators running
	// anything non-local must rotate ADMIN_PASSWORD before exposing the
	// instance; the README makes that explicit.
	weak := map[string]bool{"admin": true, "password": true, "123456": true, "root": true}
	if weak[password] || len(password) < 8 {
		log.Printf("[auth] WARNING: ADMIN_PASSWORD is weak (%q-class). Acceptable for local trial only — rotate before any real deployment.", password[:1]+"…")
	}

	hash, err := authmw.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	res, err := db.Exec(`UPDATE "user" SET password_hash = $1, updated_at = now() WHERE password_hash = $2`, hash, authmw.BootstrapSentinel)
	if err != nil {
		return fmt.Errorf("update sentinel users: %w", err)
	}
	n, _ := res.RowsAffected()
	log.Printf("[auth] bootstrapped %d admin account(s) from ADMIN_PASSWORD env", n)
	return nil
}
