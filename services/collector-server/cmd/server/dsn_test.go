package main

import (
	"os"
	"testing"

	"github.com/lakehouse2ontology/dsnguard"
)

// TestCollectorDSNPasses verifies that the DATABASE_URL used by the collector
// process is not blocked by dsnguard. pkg/dsnguard/guard.go only matches
// the legacy text2dax credential pattern; the enterprise clone DSN
// (lakehouse2ontology-enterprise) passes cleanly.
//
// This is the empirical closure for OQ-2 (ralplan consensus R2 decision):
// dsnguard blocks on "text2dax" token only — it does NOT block based on
// service identity, schema name, or which tables the service writes.
func TestCollectorDSNPasses(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping (CI environment injects it)")
	}
	if err := dsnguard.AssertSafeDSN(dsn); err != nil {
		t.Fatalf("collector DSN blocked by dsnguard: %v", err)
	}
}
