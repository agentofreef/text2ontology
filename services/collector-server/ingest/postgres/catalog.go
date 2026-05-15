package postgres

import (
	"context"
	"database/sql"

	"github.com/lakehouse2ontology/contracts"
)

// Discover queries information_schema of the source DB and returns all
// user-land tables (excluding pg_catalog / information_schema / pg_toast)
// with their columns and foreign-key relationships.
func Discover(ctx context.Context, db *sql.DB) ([]contracts.TableInfo, error) {
	// ── 1. Tables ────────────────────────────────────────────────────────────
	rows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name
		FROM information_schema.tables
		WHERE table_type = 'BASE TABLE'
		  AND table_schema NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
		ORDER BY table_schema, table_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type tblKey struct{ schema, name string }
	var tables []contracts.TableInfo
	keyToIdx := map[tblKey]int{}

	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return nil, err
		}
		full := schema + "." + name
		keyToIdx[tblKey{schema, name}] = len(tables)
		tables = append(tables, contracts.TableInfo{Name: full})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(tables) == 0 {
		return tables, nil
	}

	// ── 2. Columns ───────────────────────────────────────────────────────────
	colRows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
		ORDER BY table_schema, table_name, ordinal_position
	`)
	if err != nil {
		return nil, err
	}
	defer colRows.Close()

	for colRows.Next() {
		var schema, name, col, typ string
		if err := colRows.Scan(&schema, &name, &col, &typ); err != nil {
			return nil, err
		}
		idx, ok := keyToIdx[tblKey{schema, name}]
		if !ok {
			continue
		}
		tables[idx].Columns = append(tables[idx].Columns, contracts.ColumnInfo{
			Name:     col,
			DataType: typ,
		})
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}

	// ── 3. Foreign keys ──────────────────────────────────────────────────────
	fkRows, err := db.QueryContext(ctx, `
		SELECT
			tc.table_schema, tc.table_name, kcu.column_name,
			ccu.table_schema AS f_schema, ccu.table_name AS f_table, ccu.column_name AS f_column
		FROM information_schema.table_constraints AS tc
		JOIN information_schema.key_column_usage AS kcu
			USING (constraint_schema, constraint_name)
		JOIN information_schema.constraint_column_usage AS ccu
			USING (constraint_schema, constraint_name)
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
	`)
	if err != nil {
		return nil, err
	}
	defer fkRows.Close()

	for fkRows.Next() {
		var fromSchema, fromTable, fromCol, toSchema, toTable, toCol string
		if err := fkRows.Scan(&fromSchema, &fromTable, &fromCol, &toSchema, &toTable, &toCol); err != nil {
			return nil, err
		}
		idx, ok := keyToIdx[tblKey{fromSchema, fromTable}]
		if !ok {
			continue
		}
		tables[idx].ForeignKeys = append(tables[idx].ForeignKeys, contracts.FKInfo{
			FromTable:  fromSchema + "." + fromTable,
			FromColumn: fromCol,
			ToTable:    toSchema + "." + toTable,
			ToColumn:   toCol,
		})
	}
	if err := fkRows.Err(); err != nil {
		return nil, err
	}

	return tables, nil
}
