// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/services/collector-server/ingest/pgschema"
)

// MergeStagingIntoFinal moves all tables from stagingSchema into finalSchema,
// supporting the incremental scenario where finalSchema already exists.
// This replaces RenameStagingToFinal (which uses ALTER SCHEMA RENAME and
// fails if the final schema exists).
//
// Behaviour:
//  1. Ensure finalSchema exists (CREATE SCHEMA IF NOT EXISTS).
//  2. For each table in stagingSchema: DROP CASCADE any same-named table in
//     finalSchema, then ALTER TABLE ... SET SCHEMA finalSchema.
//  3. DROP the now-empty stagingSchema CASCADE.
//
// Must be called within an open transaction — atomicity with PopulateOntology
// is the caller's responsibility.
func MergeStagingIntoFinal(tx *sql.Tx, stagingSchema, finalSchema string) error {
	// 1. Ensure final schema exists (plus best-effort reader grants).
	if err := pgschema.CreateSchemaWithGrants(context.Background(), tx, finalSchema); err != nil {
		return fmt.Errorf("MergeStagingIntoFinal: ensure final schema %q: %w", finalSchema, err)
	}

	// 2. List all tables in staging schema.
	rows, err := tx.Query(
		`SELECT tablename FROM pg_tables WHERE schemaname = $1 ORDER BY tablename`,
		stagingSchema,
	)
	if err != nil {
		return fmt.Errorf("MergeStagingIntoFinal: list staging tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return fmt.Errorf("MergeStagingIntoFinal: scan table name: %w", err)
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("MergeStagingIntoFinal: iterate staging tables: %w", err)
	}

	// 3. Move each table (drop conflict, then set schema).
	for _, t := range tables {
		// Drop any existing same-named table in the final schema (incremental replace).
		if _, err := tx.Exec(fmt.Sprintf(
			`DROP TABLE IF EXISTS %s.%s CASCADE`,
			pq.QuoteIdentifier(finalSchema),
			pq.QuoteIdentifier(t),
		)); err != nil {
			return fmt.Errorf("MergeStagingIntoFinal: drop conflicting %s.%s: %w", finalSchema, t, err)
		}
		// Move from staging to final.
		if _, err := tx.Exec(fmt.Sprintf(
			`ALTER TABLE %s.%s SET SCHEMA %s`,
			pq.QuoteIdentifier(stagingSchema),
			pq.QuoteIdentifier(t),
			pq.QuoteIdentifier(finalSchema),
		)); err != nil {
			return fmt.Errorf("MergeStagingIntoFinal: move %s.%s → %s: %w", stagingSchema, t, finalSchema, err)
		}
	}

	// 4. Drop the now-empty staging schema.
	if _, err := tx.Exec(fmt.Sprintf(
		`DROP SCHEMA %s CASCADE`,
		pq.QuoteIdentifier(stagingSchema),
	)); err != nil {
		return fmt.Errorf("MergeStagingIntoFinal: drop staging schema %q: %w", stagingSchema, err)
	}
	return nil
}

// CleanOntologyByTableNames deletes ont_object_type rows (and their CASCADE
// dependents: ont_property, lakehouse_keyword, ont_link_type) for the specific
// table names being re-imported. This makes import idempotent on projects that
// already have ontology rows from a prior import of the same tables.
// Idempotent — safe to call when no matching rows exist.
func CleanOntologyByTableNames(ctx context.Context, tx *sql.Tx, projectID string, tableNames []string) error {
	if len(tableNames) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM ont_object_type
		WHERE project_id = $1 AND name = ANY($2)
	`, projectID, pq.Array(tableNames))
	if err != nil {
		return fmt.Errorf("CleanOntologyByTableNames: %w", err)
	}
	return nil
}

// SweepStagingOnBoot removes stale *_staging schemas older than 24h from a prior crash,
// and logs (does NOT drop) orphan final proj_<uuid> schemas not referenced by any project.lakehouse_schema.
func SweepStagingOnBoot(db *sql.DB, root string) {
	sweepFileSystem(root)
	sweepStagingSchemas(db)
	// TODO(NEW-2c): log orphan proj_<uuid> schemas unreferenced by project.lakehouse_schema
	logOrphanSchemas(db)
}

// sweepFileSystem removes staging directories under root that are older than 24h.
func sweepFileSystem(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("[pbitlakehouse/sweep] ReadDir %q: %v", root, err)
		return
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirPath := filepath.Join(root, e.Name())
		info, err := e.Info()
		if err != nil {
			log.Printf("[pbitlakehouse/sweep] stat %q: %v", dirPath, err)
			continue
		}
		if info.ModTime().Before(cutoff) {
			log.Printf("[pbitlakehouse/sweep] removing stale staging dir %q (mtime %s)", dirPath, info.ModTime().Format(time.RFC3339))
			if err := os.RemoveAll(dirPath); err != nil {
				log.Printf("[pbitlakehouse/sweep] remove %q: %v", dirPath, err)
			}
		}
	}
}

// sweepStagingSchemas drops any proj_*_staging schemas in PostgreSQL.
// These are left over from a prior import that crashed before the terminal transaction.
func sweepStagingSchemas(db *sql.DB) {
	rows, err := db.Query(`
		SELECT nspname FROM pg_namespace
		WHERE nspname LIKE 'proj_%'
		  AND nspname LIKE '%_staging'`)
	if err != nil {
		log.Printf("[pbitlakehouse/sweep] query staging schemas: %v", err)
		return
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			schemas = append(schemas, name)
		}
	}
	rows.Close()

	for _, schema := range schemas {
		log.Printf("[pbitlakehouse/sweep] dropping stale staging schema %q", schema)
		_, err := db.Exec(`DROP SCHEMA IF EXISTS ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		if err != nil {
			log.Printf("[pbitlakehouse/sweep] drop schema %q: %v", schema, err)
		}
	}
}

// logOrphanSchemas queries for proj_<uuid> schemas that are not referenced by any
// project.lakehouse_schema and logs a warning for each.  Does NOT drop them.
// TODO(NEW-2c): Wire an admin route or scheduled sweep to handle these.
func logOrphanSchemas(db *sql.DB) {
	rows, err := db.Query(`
		SELECT nspname FROM pg_namespace
		WHERE nspname LIKE 'proj_%'
		  AND nspname NOT LIKE '%_staging'
		  AND NOT EXISTS (
			SELECT 1 FROM project WHERE lakehouse_schema = nspname
		  )`)
	if err != nil {
		log.Printf("[pbitlakehouse/sweep] query orphan schemas: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			if strings.HasPrefix(name, "proj_") {
				log.Printf("[pbitlakehouse/sweep] WARNING: orphan lakehouse schema %q not referenced by any project (manual cleanup may be needed)", name)
			}
		}
	}
}
