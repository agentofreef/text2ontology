package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// SyncTables copies all rows from the listed tables in sourceDB into
// targetSchema of targetDB using pq.CopyIn for efficient bulk insertion.
// Each table is created in targetSchema before copying.
// fullName may be "schema.table" or bare "table" (defaults to "public").
func SyncTables(ctx context.Context, sourceDB, targetDB *sql.DB, targetSchema string, tables []string) error {
	for _, fullName := range tables {
		parts := strings.SplitN(fullName, ".", 2)
		sourceSchema, tableName := "public", fullName
		if len(parts) == 2 {
			sourceSchema, tableName = parts[0], parts[1]
		}
		if err := syncOneTable(ctx, sourceDB, targetDB, sourceSchema, tableName, targetSchema); err != nil {
			return fmt.Errorf("sync %s.%s: %w", sourceSchema, tableName, err)
		}
	}
	return nil
}

func syncOneTable(ctx context.Context, src, dst *sql.DB, srcSchema, name, dstSchema string) error {
	// 1. Discover columns from source
	colRows, err := src.QueryContext(ctx, `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position
	`, srcSchema, name)
	if err != nil {
		return err
	}
	var cols []string
	var types []string
	for colRows.Next() {
		var c, t string
		if err := colRows.Scan(&c, &t); err != nil {
			colRows.Close()
			return err
		}
		cols = append(cols, c)
		types = append(types, t)
	}
	colRows.Close()
	if err := colRows.Err(); err != nil {
		return err
	}

	if len(cols) == 0 {
		return fmt.Errorf("table %s.%s: no columns found", srcSchema, name)
	}

	// 2. Create table in destination schema (simplified types — complex types → TEXT)
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		pgType := mapToStagingType(types[i])
		quotedCols[i] = fmt.Sprintf("%s %s", pq.QuoteIdentifier(c), pgType)
	}
	createSQL := fmt.Sprintf(
		`CREATE TABLE %s.%s (%s)`,
		pq.QuoteIdentifier(dstSchema),
		pq.QuoteIdentifier(name),
		strings.Join(quotedCols, ", "),
	)
	if _, err := dst.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create target table: %w", err)
	}

	// 3. Read all rows from source
	quotedSrcCols := make([]string, len(cols))
	for i, c := range cols {
		quotedSrcCols[i] = pq.QuoteIdentifier(c)
	}
	selectSQL := fmt.Sprintf(
		`SELECT %s FROM %s.%s`,
		strings.Join(quotedSrcCols, ", "),
		pq.QuoteIdentifier(srcSchema),
		pq.QuoteIdentifier(name),
	)
	srcRows, err := src.QueryContext(ctx, selectSQL)
	if err != nil {
		return err
	}
	defer srcRows.Close()

	// 4. Bulk-insert into destination via pq.CopyIn
	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// pq.CopyIn requires unqualified table name; set search_path first.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"SET LOCAL search_path TO %s", pq.QuoteIdentifier(dstSchema),
	)); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn(name, cols...))
	if err != nil {
		return err
	}

	valBuf := make([]any, len(cols))
	valPtr := make([]any, len(cols))
	for i := range valBuf {
		valPtr[i] = &valBuf[i]
	}

	for srcRows.Next() {
		if err := srcRows.Scan(valPtr...); err != nil {
			stmt.Close()
			return err
		}
		if _, err := stmt.ExecContext(ctx, valBuf...); err != nil {
			stmt.Close()
			return err
		}
	}
	if err := srcRows.Err(); err != nil {
		stmt.Close()
		return err
	}
	// Flush the COPY buffer
	if _, err := stmt.ExecContext(ctx); err != nil {
		stmt.Close()
		return err
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// mapToStagingType converts a Postgres information_schema data_type string to a
// safe DDL type for the staging table. Anything the pq driver can't round-trip
// cleanly as text is mapped to TEXT so CopyIn never sees type-mismatch errors.
func mapToStagingType(pgType string) string {
	switch pgType {
	// Exact matches for IS data_type values that pq encodes as hex/binary.
	case "uuid", "json", "jsonb", "xml", "hstore",
		"ARRAY", "USER-DEFINED",
		"bit", "bit varying",
		"bytea", "money",
		"tsquery", "tsvector",
		"pg_lsn", "txid_snapshot",
		"point", "line", "lseg", "box", "path", "polygon", "circle",
		"cidr", "inet", "macaddr", "macaddr8":
		return "text"
	default:
		return pgType
	}
}
