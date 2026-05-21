// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/services/collector-server/ingest/pgschema"
)

// ErrTargetSchemaExists is returned when the final lakehouse schema already
// exists in pg_namespace, indicating a prior import was not cleaned up.
var ErrTargetSchemaExists = errors.New("TARGET_SCHEMA_EXISTS: target lakehouse schema already present; clean up orphan before retry")

// SanitizeSchemaName converts a project UUID to a stable pg schema name:
//
//	"proj_" + hex32(projectID)   e.g. "proj_550e8400e29b41d4a716446655440000"
func SanitizeSchemaName(projectID string) string {
	hex := strings.ToLower(strings.ReplaceAll(projectID, "-", ""))
	return "proj_" + hex
}

// StagingName returns the staging schema name used during bulk load before
// the atomic rename step.
func StagingName(finalSchema string) string {
	return finalSchema + "_staging"
}

// CreateStagingSchema creates the staging schema via autocommit DDL and emits
// the per-schema reader grants (best-effort).
func CreateStagingSchema(db *sql.DB, schema string) error {
	if err := pgschema.CreateSchemaWithGrants(context.Background(), db, schema); err != nil {
		return fmt.Errorf("pbitlakehouse: create staging schema %q: %w", schema, err)
	}
	return nil
}

// pbitTypeToSQL maps PBIT column dataType strings to PostgreSQL column types.
func pbitTypeToSQL(dataType string) string {
	switch strings.ToLower(dataType) {
	case "string":
		return "text"
	case "int64":
		return "bigint"
	case "double":
		return "double precision"
	case "datetime":
		return "timestamp"
	case "boolean":
		return "boolean"
	case "decimal":
		return "numeric"
	default:
		return "text"
	}
}

// CreateLakehouseTable issues an autocommit CREATE TABLE IF NOT EXISTS in the
// given schema using PBIT column definitions.
func CreateLakehouseTable(db *sql.DB, schema, tableName string, cols []PbitColumn) error {
	if len(cols) == 0 {
		// Create a minimal placeholder table so derived views can reference it.
		_, err := db.Exec(fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s.%s (_placeholder text)`,
			pq.QuoteIdentifier(schema),
			pq.QuoteIdentifier(tableName),
		))
		if err != nil {
			return fmt.Errorf("pbitlakehouse: create placeholder table %q.%q: %w", schema, tableName, err)
		}
		return nil
	}

	var colDefs []string
	for _, c := range cols {
		// Skip calculated columns — they don't map to physical storage.
		if c.Expression != "" {
			continue
		}
		sqlType := pbitTypeToSQL(c.DataType)
		colDefs = append(colDefs, fmt.Sprintf("%s %s", pq.QuoteIdentifier(c.Name), sqlType))
	}
	if len(colDefs) == 0 {
		colDefs = []string{"_placeholder text"}
	}

	ddl := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s.%s (%s)`,
		pq.QuoteIdentifier(schema),
		pq.QuoteIdentifier(tableName),
		strings.Join(colDefs, ", "),
	)
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("pbitlakehouse: create table %q.%q: %w", schema, tableName, err)
	}
	return nil
}

// CopyRowsInto streams rows from rowIter into schema.table using COPY via
// pq.CopyIn.  Empty strings are converted to NULL for non-text columns.
// Returns the number of rows inserted.
func CopyRowsInto(db *sql.DB, schema, table string, cols []string, rowIter func() ([]string, error)) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("pbitlakehouse: begin tx for COPY: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback() //nolint:errcheck
		}
	}()

	// pq.CopyIn doesn't accept schema-qualified names; set search_path first.
	if _, spErr := tx.Exec(fmt.Sprintf("SET LOCAL search_path TO %s", pq.QuoteIdentifier(schema))); spErr != nil {
		return 0, fmt.Errorf("pbitlakehouse: set search_path: %w", spErr)
	}
	stmt, err := tx.Prepare(pq.CopyIn(table, cols...))
	if err != nil {
		return 0, fmt.Errorf("pbitlakehouse: prepare COPY INTO %s.%s: %w", schema, table, err)
	}
	defer stmt.Close()

	var rowCount int64
	for {
		row, err := rowIter()
		if err != nil {
			return rowCount, fmt.Errorf("pbitlakehouse: rowIter error at row %d: %w", rowCount, err)
		}
		if row == nil {
			break // EOF
		}

		// Build args slice; pad short rows with nil.
		args := make([]interface{}, len(cols))
		for i := range cols {
			if i < len(row) && row[i] != "" {
				args[i] = row[i]
			} else {
				args[i] = nil
			}
		}

		if _, err := stmt.Exec(args...); err != nil {
			return rowCount, fmt.Errorf("pbitlakehouse: COPY row %d: %w", rowCount, err)
		}
		rowCount++
	}

	if _, err := stmt.Exec(); err != nil {
		return rowCount, fmt.Errorf("pbitlakehouse: flush COPY: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return rowCount, fmt.Errorf("pbitlakehouse: commit COPY tx: %w", err)
	}
	return rowCount, nil
}

// RenameStagingToFinal renames the staging schema to the final name inside an
// existing transaction.  Must be called within the terminal metadata tx.
func RenameStagingToFinal(tx *sql.Tx, stagingSchema, finalSchema string) error {
	_, err := tx.Exec(fmt.Sprintf(
		`ALTER SCHEMA %s RENAME TO %s`,
		pq.QuoteIdentifier(stagingSchema),
		pq.QuoteIdentifier(finalSchema),
	))
	if err != nil {
		return fmt.Errorf("pbitlakehouse: rename schema %q → %q: %w", stagingSchema, finalSchema, err)
	}
	return nil
}

// CheckTargetSchemaExists queries pg_namespace inside the given transaction.
// Returns ErrTargetSchemaExists if the final schema name is already taken.
func CheckTargetSchemaExists(tx *sql.Tx, finalSchema string) error {
	var exists int
	err := tx.QueryRow(`SELECT 1 FROM pg_namespace WHERE nspname=$1`, finalSchema).Scan(&exists)
	if err == nil {
		return ErrTargetSchemaExists
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return fmt.Errorf("pbitlakehouse: check schema existence %q: %w", finalSchema, err)
}

// DropSchemaCascade drops the named schema and all its objects.  Used for
// failure cleanup of the staging schema.
func DropSchemaCascade(db *sql.DB, schema string) error {
	_, err := db.Exec(fmt.Sprintf(
		`DROP SCHEMA IF EXISTS %s CASCADE`,
		pq.QuoteIdentifier(schema),
	))
	if err != nil {
		return fmt.Errorf("pbitlakehouse: drop schema %q cascade: %w", schema, err)
	}
	return nil
}
