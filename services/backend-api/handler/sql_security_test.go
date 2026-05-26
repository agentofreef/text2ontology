package handler

import (
	"errors"
	"strings"
	"testing"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/sqlrewrite"
)

// TestRejectDDL_DollarQuoteBypass: PG dollar-quoting must be rejected outright.
// Without this, a payload can hide banned keywords / chained statements from the
// comment-strip + keyword scan inside an opaque $$...$$ / $tag$...$tag$ body.
func TestRejectDDL_DollarQuote(t *testing.T) {
	hostile := []string{
		`SELECT $$ ; DROP TABLE x $$`,
		`SELECT $tag$ anything $tag$`,
		`SELECT * FROM t WHERE c = $$value$$`,
		`SELECT $$`, // lone opener
	}
	for _, q := range hostile {
		if err := sqlrewrite.RejectDDL(q); err == nil {
			t.Fatalf("rejectDDL(%q) = nil, want rejection of dollar-quoting", q)
		}
	}

	// A normal read-only SELECT (no dollar-quotes, no banned verbs) still passes.
	if err := sqlrewrite.RejectDDL(`SELECT id, name FROM "Customer" WHERE region = 'NA'`); err != nil {
		t.Fatalf("RejectDDL rejected a benign SELECT: %v", err)
	}
}

// TestRejectDDL_StillBlocksKnownVectors guards the pre-existing protections so
// the dollar-quote addition didn't regress them.
func TestRejectDDL_StillBlocksKnownVectors(t *testing.T) {
	cases := []string{
		`DROP TABLE customers`,
		`SELECT * FROM pg_catalog.pg_authid`,
		`SELECT 1; SELECT 2`,
		`SET search_path TO public`,
	}
	for _, q := range cases {
		if err := sqlrewrite.RejectDDL(q); err == nil {
			t.Fatalf("rejectDDL(%q) = nil, want rejection", q)
		}
	}
}

// TestSanitizeSQLExecError: classified, schema-free messages out; raw Postgres
// detail (table/column names, positions) must never appear in the returned text.
func TestSanitizeSQLExecError(t *testing.T) {
	if got := sanitizeSQLExecError(nil); got != "" {
		t.Fatalf("nil error → %q, want empty", got)
	}

	// SQLSTATE 42P01 = undefined_table; the raw message names the secret table.
	raw := &pq.Error{Code: "42P01", Message: `relation "secret_users_table" does not exist`}
	got := sanitizeSQLExecError(raw)
	if strings.Contains(got, "secret_users_table") {
		t.Fatalf("sanitized message leaked the raw table name: %q", got)
	}
	if got == "" {
		t.Fatal("sanitized message must be non-empty for a known error class")
	}

	// Unknown / non-pq error falls back to a generic message, no raw echo.
	generic := sanitizeSQLExecError(errors.New("pq: column \"ssn\" of relation \"people\" violates"))
	if strings.Contains(generic, "ssn") || strings.Contains(generic, "people") {
		t.Fatalf("fallback leaked raw identifiers: %q", generic)
	}
	if generic == "" {
		t.Fatal("fallback message must be non-empty")
	}
}
