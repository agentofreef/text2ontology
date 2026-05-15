package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lakehouse2ontology/contracts"
)

// Discover queries sqlite_master + PRAGMA table_info / foreign_key_list
// to build a []contracts.TableInfo identical in shape to what postgres.Discover
// returns, so downstream MapToPbitSchema works without source-type awareness.
//
// Internal SQLite tables (sqlite_*) and views are skipped — only user 'table'
// rows are included.
func Discover(ctx context.Context, db *sql.DB) ([]contracts.TableInfo, error) {
	// 1. Tables
	rows, err := db.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type='table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("read sqlite_master: %w", err)
	}
	defer rows.Close()

	var tables []contracts.TableInfo
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, contracts.TableInfo{Name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 2. Per-table columns + foreign keys (PRAGMA can't be batched).
	for i := range tables {
		cols, err := tableInfo(ctx, db, tables[i].Name)
		if err != nil {
			return nil, fmt.Errorf("PRAGMA table_info(%s): %w", tables[i].Name, err)
		}
		tables[i].Columns = cols

		fks, err := foreignKeys(ctx, db, tables[i].Name)
		if err != nil {
			return nil, fmt.Errorf("PRAGMA foreign_key_list(%s): %w", tables[i].Name, err)
		}
		tables[i].ForeignKeys = fks
	}
	return tables, nil
}

// tableInfo returns column metadata for one table via PRAGMA table_info(t).
// SQLite-declared types (INTEGER, VARCHAR(20), DATETIME, ...) are normalized
// to canonical postgres-style strings so postgres.MapToPbitSchema's pgTypeToPbit
// can handle them without modification.
func tableInfo(ctx context.Context, db *sql.DB, table string) ([]contracts.ColumnInfo, error) {
	// PRAGMA can't take parameterized identifiers; quote with SQLite spec.
	q := fmt.Sprintf(`PRAGMA table_info(%s)`, quoteIdent(table))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []contracts.ColumnInfo
	for rows.Next() {
		var (
			cid             int
			name, declared  string
			notnull, pk     int
			dflt            sql.NullString
		)
		if err := rows.Scan(&cid, &name, &declared, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, contracts.ColumnInfo{
			Name:     name,
			DataType: normalizeSqliteType(declared),
		})
	}
	return cols, rows.Err()
}

// foreignKeys returns FK metadata for one table via PRAGMA foreign_key_list(t).
// Only the parent column is captured (compound FKs degrade to one entry per
// member column — same minimal-FK approach as postgres mapper).
func foreignKeys(ctx context.Context, db *sql.DB, fromTable string) ([]contracts.FKInfo, error) {
	q := fmt.Sprintf(`PRAGMA foreign_key_list(%s)`, quoteIdent(fromTable))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fks []contracts.FKInfo
	for rows.Next() {
		var (
			id, seq                       int
			toTable, fromCol, toCol       string
			onUpdate, onDelete, matchKind sql.NullString
		)
		if err := rows.Scan(&id, &seq, &toTable, &fromCol, &toCol, &onUpdate, &onDelete, &matchKind); err != nil {
			return nil, err
		}
		fks = append(fks, contracts.FKInfo{
			FromTable:  fromTable,
			FromColumn: fromCol,
			ToTable:    toTable,
			ToColumn:   toCol,
		})
	}
	return fks, rows.Err()
}
