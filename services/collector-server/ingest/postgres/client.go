// Package postgres provides connection helpers for a source Postgres DB
// used by the collector connector. Distinct from the collector's own DB
// (which uses DATABASE_URL from env); this package opens connections to
// user-supplied source databases for catalog discovery + row sync.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// Config holds connection parameters for a source Postgres database.
type Config struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string // defaults to "disable" when empty
}

// DSNFor builds a lib/pq key=value DSN from Config.
func DSNFor(cfg Config) string {
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	return fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=%s connect_timeout=10",
		cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password, sslmode,
	)
}

// TestConnection opens a transient connection and pings the source DB.
// The connection is closed before returning.
func TestConnection(ctx context.Context, cfg Config) error {
	db, err := sql.Open("postgres", DSNFor(cfg))
	if err != nil {
		return err
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return db.PingContext(pingCtx)
}

// Open opens and verifies a persistent connection to the source DB.
// Caller is responsible for calling db.Close().
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	db, err := sql.Open("postgres", DSNFor(cfg))
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
