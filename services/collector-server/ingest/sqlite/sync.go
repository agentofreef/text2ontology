package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
)

// SyncTables iterates the listed tables, copying each one's rows from the
// open SQLite connection into a freshly created TEXT-column table inside
// targetSchema of targetDB. Same staging-as-text strategy as the file
// connector: type fidelity is recovered downstream by MapToPbitSchema +
// MergeStagingIntoFinal.
//
// progress is called periodically with (table, rowsDone) so callers can
// emit SSE progress events. Pass nil to suppress.
func SyncTables(
	ctx context.Context,
	srcDB, targetDB *sql.DB,
	targetSchema string,
	tables []string,
	progress func(table string, rowsDone int64),
) error {
	for _, t := range tables {
		if err := syncOneTable(ctx, srcDB, targetDB, t, targetSchema, progress); err != nil {
			return fmt.Errorf("sync %s: %w", t, err)
		}
	}
	return nil
}

func syncOneTable(
	ctx context.Context,
	src, dst *sql.DB,
	table, dstSchema string,
	progress func(string, int64),
) error {
	// 1. Read column list from PRAGMA (already used by Discover).
	cols, err := tableInfo(ctx, src, table)
	if err != nil {
		return fmt.Errorf("read columns: %w", err)
	}
	if len(cols) == 0 {
		return fmt.Errorf("table %q has no columns", table)
	}

	// 2. Create staging table with all columns as TEXT (file-connector
	//    pattern). Type fidelity is recovered later by the mapper +
	//    MergeStagingIntoFinal pipeline. TEXT-only sidesteps every
	//    pq.CopyIn type-coercion edge case (NULL, mixed affinity, etc.).
	colNames := make([]string, len(cols))
	quotedDefs := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = c.Name
		quotedDefs[i] = pq.QuoteIdentifier(c.Name) + " TEXT"
	}
	createSQL := fmt.Sprintf(
		`CREATE TABLE %s.%s (%s)`,
		pq.QuoteIdentifier(dstSchema),
		pq.QuoteIdentifier(table),
		strings.Join(quotedDefs, ", "),
	)
	if _, err := dst.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create staging table: %w", err)
	}

	// 3. Stream rows from SQLite.
	selectSQL := fmt.Sprintf(`SELECT * FROM %s`, quoteIdent(table))
	srcRows, err := src.QueryContext(ctx, selectSQL)
	if err != nil {
		return fmt.Errorf("read source rows: %w", err)
	}
	defer srcRows.Close()

	// 4. Bulk insert into target via pq.CopyIn.
	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`SET LOCAL search_path TO %s`, pq.QuoteIdentifier(dstSchema),
	)); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn(table, colNames...))
	if err != nil {
		return fmt.Errorf("prepare COPY: %w", err)
	}

	valBuf := make([]any, len(cols))
	valPtr := make([]any, len(cols))
	for i := range valBuf {
		valPtr[i] = &valBuf[i]
	}

	var n int64
	for srcRows.Next() {
		if err := srcRows.Scan(valPtr...); err != nil {
			stmt.Close()
			return fmt.Errorf("scan row: %w", err)
		}
		args := make([]any, len(valBuf))
		for i, v := range valBuf {
			args[i] = encodeForText(v)
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			stmt.Close()
			return fmt.Errorf("copy row %d: %w", n, err)
		}
		n++
		if progress != nil && n%1000 == 0 {
			progress(table, n)
		}
	}
	if err := srcRows.Err(); err != nil {
		stmt.Close()
		return err
	}

	// Flush COPY buffer.
	if _, err := stmt.ExecContext(ctx); err != nil {
		stmt.Close()
		return fmt.Errorf("flush COPY: %w", err)
	}
	if err := stmt.Close(); err != nil {
		return fmt.Errorf("close COPY: %w", err)
	}
	if progress != nil {
		progress(table, n)
	}
	return tx.Commit()
}

// encodeForText converts whatever Go type the SQLite driver produced for a
// row value into something pq.CopyIn can write into a TEXT column. Returning
// nil preserves SQL NULL; everything else is rendered as a string so lib/pq's
// driver.Value path doesn't try to negotiate binary encodings against TEXT.
func encodeForText(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		// Strings can still arrive non-UTF-8 from SQLite when the source
		// blob carried mislabelled byte data — PostgreSQL TEXT requires
		// valid UTF-8, so hex-encode the offender rather than crash.
		if !utf8.ValidString(x) {
			return "\\x" + hex.EncodeToString([]byte(x))
		}
		return x
	case []byte:
		// SQLite BLOB columns (Categories.Picture, Employees.Photo, etc.)
		// arrive as []byte. If the bytes happen to be valid UTF-8 they
		// were really TEXT under the hood and we keep them as a string;
		// otherwise hex-encode with a "\x" prefix (postgres-bytea-literal
		// shape, also recognisable downstream as "this was binary").
		if utf8.Valid(x) {
			return string(x)
		}
		return "\\x" + hex.EncodeToString(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		// FormatFloat with -1 precision uses the shortest round-trip form.
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case bool:
		return strconv.FormatBool(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", v)
	}
}
