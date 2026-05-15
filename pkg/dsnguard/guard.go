// Package dsnguard promotes assertSafeDSN from services/backend-api/cmd/server/main.go into a shared
// library so every service in the split architecture applies the same DSN
// safety checks at startup. See §3.8 #6 of the consensus plan.
package dsnguard

import (
	"fmt"
	"regexp"
)

// unsafeDSNRegex matches any known legacy credential pattern. Uses
// case-insensitive matching and word boundaries to prevent bypasses via
// URL-encoding variants, adjacent whitespace, or superstring names like
// `text2dax_archive` sneaking past a plain substring check.
//
// Patterns covered (all variants share the `text2dax` token):
//   - postgres://text2dax[:@]...            — URL form with user=text2dax
//   - user=text2dax, dbname=text2dax        — key=value DSN form (word-boundary anchored)
//   - /text2dax(?|$|&|whitespace)           — path component ending the DB name exactly
//   - :5488/text2dax                        — legacy docker-compose port + db
var unsafeDSNRegex = regexp.MustCompile(
	`(?i)(` +
		`postgres://text2dax[:@]|` +
		`\buser=text2dax\b|` +
		`\bdbname=text2dax\b|` +
		`/text2dax(\?|$|\s|&)|` +
		`:5488/text2dax` +
		`)`,
)

// validDSNPrefixRegex matches the two canonical Postgres DSN shapes accepted by
// lib/pq:
//   - URL form:       postgres:// or postgresql://
//   - key=value form: starts with `<key>=`
//
// Anything else (empty string, leading whitespace, accidental double-prefix
// like `DATABASE_URL= DATABASE_URL=postgres://...`) is treated as malformed.
var validDSNPrefixRegex = regexp.MustCompile(`^(postgres(ql)?://|[a-zA-Z_][a-zA-Z0-9_]*=)`)

// AssertSafeDSN returns an error if dsn is either malformed or matches any
// known legacy text2dax credentials pattern. Callers decide whether to fatal
// or handle gracefully — this function does not panic.
//
// Two guards:
//  1. Format — must start with postgres[ql]:// or a key=value pair.
//  2. Legacy content — refuses any DSN containing text2dax patterns.
func AssertSafeDSN(dsn string) error {
	if !validDSNPrefixRegex.MatchString(dsn) {
		return fmt.Errorf("CONFIG: DSN %q is malformed — must start with postgres:// / postgresql:// / <key>= — refusing to start", dsn)
	}
	if unsafeDSNRegex.MatchString(dsn) {
		return fmt.Errorf("SECURITY: DSN %q matches legacy text2dax pattern — refusing to start", dsn)
	}
	return nil
}
