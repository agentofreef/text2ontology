package httputil

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests cover the constant-time INTERNAL_TOKEN comparison gating the
// ?check=db branch of InstallHealthzDB. The comparison must:
//   - accept a correct token (proceed into the authenticated DB-ping branch),
//   - reject a wrong token of the same length (plain liveness, no DB info),
//   - reject a wrong token of a different length (no panic),
//   - reject when the expected token is empty/unset (fail-closed, no DB info).
//
// "Reject" here means the handler returns the plain liveness body
// `{"ok":true}` with no db_target_hash — never disclosing DB info or pinging.

// pingErrConnector is a minimal database/sql driver that always fails Ping,
// letting us prove the request reached the authenticated DB-ping branch
// (and thus that the token was accepted) without a real database.
type pingErrConnector struct{}

func (pingErrConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("ping failed: no database")
}
func (pingErrConnector) Driver() driver.Driver { return pingErrDriver{} }

type pingErrDriver struct{}

func (pingErrDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("open failed: no database")
}

func newPingErrDB() *sql.DB { return sql.OpenDB(pingErrConnector{}) }

func dbCheckRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/healthz?check=db", nil)
	if token != "" {
		r.Header.Set("X-Internal-Token", token)
	}
	return r
}

// serve registers the handler and runs the request, returning the recorder.
func serve(db *sql.DB, dsn string, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	InstallHealthzDB(mux, dsn, db, "svc-test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

const testDSN = "postgres://u:p@host:5432/db"

func TestHealthzDB_CorrectTokenReachesDBPing(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "s3cr3t-token-value")
	db := newPingErrDB()
	defer db.Close()
	w := serve(db, testDSN, dbCheckRequest("s3cr3t-token-value"))
	// Accepted token → authenticated branch → db.Ping() fails → 503.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("correct token must reach DB ping (503 on ping fail), got %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "db_target_hash") {
		t.Fatalf("ping-failure body must not leak db_target_hash: %s", w.Body.String())
	}
}

func TestHealthzDB_WrongTokenSameLengthRejected(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "s3cr3t-token-value")
	db := newPingErrDB()
	defer db.Close()
	w := serve(db, testDSN, dbCheckRequest("WRONG-token-value!"))
	assertPlainLiveness(t, w)
}

func TestHealthzDB_WrongTokenDifferentLengthRejected(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "s3cr3t-token-value")
	db := newPingErrDB()
	defer db.Close()
	// Different length must not panic and must reject.
	w := serve(db, testDSN, dbCheckRequest("short"))
	assertPlainLiveness(t, w)
}

func TestHealthzDB_EmptyExpectedTokenRejected(t *testing.T) {
	// Unset INTERNAL_TOKEN: fail-closed, never disclose DB info, never ping.
	t.Setenv("INTERNAL_TOKEN", "")
	db := newPingErrDB()
	defer db.Close()

	// Non-empty provided token, no server token configured.
	w := serve(db, testDSN, dbCheckRequest("anything"))
	assertPlainLiveness(t, w)

	// Empty provided token: still reject (no empty==empty accept).
	w = serve(db, testDSN, dbCheckRequest(""))
	assertPlainLiveness(t, w)
}

// assertPlainLiveness asserts the rejected response is the safe plain-liveness
// body with a 200 status and no DB disclosure.
func assertPlainLiveness(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("rejected ?check=db must return plain liveness 200, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != `{"ok":true}` {
		t.Fatalf("rejected ?check=db must return exactly plain liveness, got %q", body)
	}
	if strings.Contains(body, "db_target_hash") {
		t.Fatalf("rejected response must not leak db_target_hash: %s", body)
	}
}
