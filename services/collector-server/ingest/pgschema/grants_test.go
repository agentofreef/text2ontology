package pgschema

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// fakeExecer records the statements issued by CreateSchemaWithGrants without a
// real database. Role existence is controlled separately via the roleLookup
// seam (see setRoles), so QueryContext is never actually exercised here.
type fakeExecer struct {
	execStmts []string // every ExecContext query, in order
	execErr   error    // if set, every ExecContext returns this error
}

func (f *fakeExecer) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	f.execStmts = append(f.execStmts, query)
	if f.execErr != nil {
		return nil, f.execErr
	}
	return driverResult{}, nil
}

func (f *fakeExecer) QueryContext(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, errors.New("QueryContext should not be called when roleLookup is stubbed")
}

type driverResult struct{}

func (driverResult) LastInsertId() (int64, error) { return 0, nil }
func (driverResult) RowsAffected() (int64, error) { return 0, nil }

// setRoles overrides the roleLookup seam to report the given roles as existing,
// restoring the original on test cleanup.
func setRoles(t *testing.T, present []string) {
	t.Helper()
	orig := roleLookup
	t.Cleanup(func() { roleLookup = orig })
	roleLookup = func(_ context.Context, _ Execer, _ []string) (map[string]bool, error) {
		m := make(map[string]bool, len(present))
		for _, r := range present {
			m[r] = true
		}
		return m, nil
	}
}

// setRoleLookupErr overrides roleLookup to fail, simulating restricted catalog
// access.
func setRoleLookupErr(t *testing.T, err error) {
	t.Helper()
	orig := roleLookup
	t.Cleanup(func() { roleLookup = orig })
	roleLookup = func(_ context.Context, _ Execer, _ []string) (map[string]bool, error) {
		return nil, err
	}
}

func has(stmts []string, substr string) bool {
	for _, s := range stmts {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func countWith(stmts []string, substr string) int {
	n := 0
	for _, s := range stmts {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

func TestCreateSchemaWithGrants_AllRolesPresent(t *testing.T) {
	setRoles(t, append(append([]string{}, readerRoles...), ownerRole))
	f := &fakeExecer{}

	if err := CreateSchemaWithGrants(context.Background(), f, "proj_abc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// CREATE SCHEMA is mandatory and must be quoted (pq.QuoteIdentifier).
	if !has(f.execStmts, `CREATE SCHEMA IF NOT EXISTS "proj_abc"`) {
		t.Fatalf("missing/incorrect CREATE SCHEMA, got: %v", f.execStmts)
	}
	if !has(f.execStmts, "GRANT USAGE ON SCHEMA") {
		t.Fatalf("missing GRANT USAGE, got: %v", f.execStmts)
	}
	for _, r := range readerRoles {
		if !has(f.execStmts, `"`+r+`"`) {
			t.Fatalf("GRANT must reference reader role %q, got: %v", r, f.execStmts)
		}
	}
	if !has(f.execStmts, `ALTER DEFAULT PRIVILEGES FOR ROLE "collector_server_user"`) {
		t.Fatalf("missing ALTER DEFAULT PRIVILEGES, got: %v", f.execStmts)
	}
}

func TestCreateSchemaWithGrants_NoRolesPresent(t *testing.T) {
	// Pre-cutover: no roles exist. CREATE SCHEMA must still run; NO grants.
	setRoles(t, nil)
	f := &fakeExecer{}

	if err := CreateSchemaWithGrants(context.Background(), f, "collector_x"); err != nil {
		t.Fatalf("unexpected error (role-absent must be tolerated): %v", err)
	}
	if !has(f.execStmts, "CREATE SCHEMA IF NOT EXISTS") {
		t.Fatalf("CREATE SCHEMA must still run, got: %v", f.execStmts)
	}
	if has(f.execStmts, "GRANT USAGE") || has(f.execStmts, "ALTER DEFAULT PRIVILEGES") {
		t.Fatalf("no grants must be emitted when no roles exist, got: %v", f.execStmts)
	}
}

func TestCreateSchemaWithGrants_OnlyReadersNoOwner(t *testing.T) {
	// Readers exist but the owner role does not: GRANT USAGE yes, ALTER no.
	setRoles(t, readerRoles)
	f := &fakeExecer{}

	if err := CreateSchemaWithGrants(context.Background(), f, "proj_y"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !has(f.execStmts, "GRANT USAGE ON SCHEMA") {
		t.Fatalf("GRANT USAGE expected when readers exist, got: %v", f.execStmts)
	}
	if has(f.execStmts, "ALTER DEFAULT PRIVILEGES") {
		t.Fatalf("ALTER DEFAULT PRIVILEGES must be skipped without owner role, got: %v", f.execStmts)
	}
}

func TestCreateSchemaWithGrants_CreateSchemaFatal(t *testing.T) {
	// CREATE SCHEMA failing is fatal-on-error.
	setRoles(t, nil)
	f := &fakeExecer{execErr: errors.New("permission denied")}

	err := CreateSchemaWithGrants(context.Background(), f, "proj_z")
	if err == nil {
		t.Fatal("expected CREATE SCHEMA failure to be returned as fatal error")
	}
	if !strings.Contains(err.Error(), "create schema") {
		t.Fatalf("error should mention create schema, got: %v", err)
	}
}

func TestCreateSchemaWithGrants_GrantErrorIgnored(t *testing.T) {
	// A grant statement failing must NOT fail the call (best-effort).
	setRoles(t, readerRoles)
	f := &grantFailExecer{}

	if err := CreateSchemaWithGrants(context.Background(), f, "proj_q"); err != nil {
		t.Fatalf("grant failure must be swallowed, got error: %v", err)
	}
	if f.createCount != 1 {
		t.Fatalf("CREATE SCHEMA should run exactly once, got %d", f.createCount)
	}
}

func TestCreateSchemaWithGrants_RoleLookupFailureSkipsGrants(t *testing.T) {
	// If the pg_roles lookup itself fails, grants are skipped but CREATE
	// SCHEMA still succeeds (no error returned).
	setRoleLookupErr(t, errors.New("catalog access denied"))
	f := &fakeExecer{}

	if err := CreateSchemaWithGrants(context.Background(), f, "proj_r"); err != nil {
		t.Fatalf("role lookup failure must not fail the call, got: %v", err)
	}
	if countWith(f.execStmts, "GRANT") != 0 {
		t.Fatalf("no grants when role lookup fails, got: %v", f.execStmts)
	}
	if !has(f.execStmts, "CREATE SCHEMA") {
		t.Fatalf("CREATE SCHEMA must still run, got: %v", f.execStmts)
	}
}

// grantFailExecer succeeds on CREATE SCHEMA but fails every GRANT/ALTER.
type grantFailExecer struct {
	createCount int
}

func (g *grantFailExecer) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	if strings.Contains(query, "CREATE SCHEMA") {
		g.createCount++
		return driverResult{}, nil
	}
	return nil, errors.New("grant failed")
}

func (g *grantFailExecer) QueryContext(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, errors.New("QueryContext should not be called when roleLookup is stubbed")
}
