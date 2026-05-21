package srvkit

import (
	"database/sql"
	"database/sql/driver"
	"testing"
)

// fakeDriver is a no-op database/sql driver registered solely so sql.Open
// succeeds without a real DB. The SetMax*/SetConn* setters configure the
// pool without ever opening a connection, so Open never has to connect.
type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) { return nil, driver.ErrBadConn }

func init() {
	sql.Register("srvkit_fake", fakeDriver{})
}

// TestTunePool asserts the pool setters are applied. MaxOpenConnections is
// observable via db.Stats(); the others have no public getter, so we assert
// they don't panic and the open cap takes effect.
func TestTunePool(t *testing.T) {
	db, err := sql.Open("srvkit_fake", "ignored")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	TunePool(db)

	if got := db.Stats().MaxOpenConnections; got != poolMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, poolMaxOpenConns)
	}
}

// TestTunePool_NilSafe asserts TunePool tolerates a nil *sql.DB.
func TestTunePool_NilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TunePool(nil) panicked: %v", r)
		}
	}()
	TunePool(nil)
}
