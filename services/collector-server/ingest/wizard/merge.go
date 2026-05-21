// Package wizard manages wizard state persistence in data_source.wizard_state.
package wizard

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/services/collector-server/ingest/pgschema"
)

// mergeStagingIntoFinal moves all tables from stagingSchema into finalSchema,
// supporting the incremental scenario where finalSchema already exists.
// This replaces pbit.RenameStagingToFinal (which uses ALTER SCHEMA RENAME and
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
func mergeStagingIntoFinal(tx *sql.Tx, stagingSchema, finalSchema string) error {
	// 1. Ensure final schema exists (plus best-effort reader grants).
	if err := pgschema.CreateSchemaWithGrants(context.Background(), tx, finalSchema); err != nil {
		return fmt.Errorf("mergeStagingIntoFinal: ensure final schema %q: %w", finalSchema, err)
	}

	// 2. List all tables in staging schema.
	rows, err := tx.Query(
		`SELECT tablename FROM pg_tables WHERE schemaname = $1 ORDER BY tablename`,
		stagingSchema,
	)
	if err != nil {
		return fmt.Errorf("mergeStagingIntoFinal: list staging tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return fmt.Errorf("mergeStagingIntoFinal: scan table name: %w", err)
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("mergeStagingIntoFinal: iterate staging tables: %w", err)
	}

	// 3. Move each table (drop conflict, then set schema).
	for _, t := range tables {
		// Drop any existing same-named table in the final schema (incremental replace).
		if _, err := tx.Exec(fmt.Sprintf(
			`DROP TABLE IF EXISTS %s.%s CASCADE`,
			pq.QuoteIdentifier(finalSchema),
			pq.QuoteIdentifier(t),
		)); err != nil {
			return fmt.Errorf("mergeStagingIntoFinal: drop conflicting %s.%s: %w", finalSchema, t, err)
		}
		// Move from staging to final.
		if _, err := tx.Exec(fmt.Sprintf(
			`ALTER TABLE %s.%s SET SCHEMA %s`,
			pq.QuoteIdentifier(stagingSchema),
			pq.QuoteIdentifier(t),
			pq.QuoteIdentifier(finalSchema),
		)); err != nil {
			return fmt.Errorf("mergeStagingIntoFinal: move %s.%s → %s: %w", stagingSchema, t, finalSchema, err)
		}
	}

	// 4. Drop the now-empty staging schema.
	if _, err := tx.Exec(fmt.Sprintf(
		`DROP SCHEMA %s CASCADE`,
		pq.QuoteIdentifier(stagingSchema),
	)); err != nil {
		return fmt.Errorf("mergeStagingIntoFinal: drop staging schema %q: %w", stagingSchema, err)
	}
	return nil
}
