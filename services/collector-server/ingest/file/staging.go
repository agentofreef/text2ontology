package file

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lib/pq"

	pbit "github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
)

// progressReporter is the small surface writeAllSheetsToStaging needs from
// job.Reporter. Defining it locally avoids forcing every test caller to spin
// up a real *job.Reporter; *job.Reporter satisfies it implicitly.
type progressReporter interface {
	Update(phase string, pct int, rowsDone, rowsTotal, bytesDone int64, msg string)
	Cancelled() bool
}

// nopReporter is a stand-in when no progress sink is wired (e.g. tests).
type nopReporter struct{}

func (nopReporter) Update(string, int, int64, int64, int64, string) {}
func (nopReporter) Cancelled() bool                                   { return false }

// writeAllSheetsToStaging creates a staging schema in db and bulk-loads each
// SheetInfo into its own table named after the sheet. For xlsx files this
// supports multi-sheet workbooks where each sheet becomes a separate staging
// table. CSV files always have exactly one SheetInfo (named after the file).
//
// Each table is bulk-loaded via PostgreSQL COPY (lib/pq's CopyInSchema)
// inside a single transaction — partial commits aren't possible: if any
// sheet fails, the whole upload is rolled back so data_source stays clean.
//
// rep receives progress at every 1000 rows (per-sheet) and at sheet
// boundaries so the user can watch a long ingest tick along.
func writeAllSheetsToStaging(
	ctx context.Context,
	db *sql.DB,
	filePath, ext string,
	sheets []SheetInfo,
	stagingSchema string,
	rep progressReporter,
) error {
	if rep == nil {
		rep = nopReporter{}
	}
	if len(sheets) == 0 {
		return nil
	}

	// 1. CREATE SCHEMA IF NOT EXISTS (idempotent; outside tx so future
	//    re-uploads of the same data_source can reuse the schema).
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, stagingSchema),
	); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// 2. Single transaction wraps all per-sheet CREATE TABLE + COPY. lib/pq
	//    requires the COPY statement to live on the same connection as the
	//    transaction; PrepareContext on tx satisfies that.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	totalSheets := len(sheets)
	var globalRows int64

	for idx, sh := range sheets {
		if len(sh.Columns) == 0 {
			continue
		}
		if rep.Cancelled() {
			return errors.New("cancelled")
		}
		rep.Update(
			"copy_staging",
			percentOf(idx, totalSheets),
			globalRows, 0, 0,
			fmt.Sprintf("Sheet %d/%d · %s", idx+1, totalSheets, sh.Name),
		)
		written, err := stageOneSheet(ctx, tx, filePath, ext, sh, stagingSchema, rep, idx, totalSheets, globalRows)
		if err != nil {
			return fmt.Errorf("sheet %q: %w", sh.Name, err)
		}
		globalRows += written
	}

	rep.Update("copy_staging", 100, globalRows, globalRows, 0, "commit")
	return tx.Commit()
}

// stageOneSheet creates one staging table + bulk-loads its rows via COPY.
// Returns the number of data rows written.
func stageOneSheet(
	ctx context.Context,
	tx *sql.Tx,
	filePath, ext string,
	sh SheetInfo,
	stagingSchema string,
	rep progressReporter,
	sheetIdx, totalSheets int,
	rowsBeforeSheet int64,
) (int64, error) {
	// CREATE TABLE (all columns TEXT for simplicity — type inference is
	// deferred to the wizard mapping step).
	quotedCols := make([]string, len(sh.Columns))
	for i, c := range sh.Columns {
		quotedCols[i] = fmt.Sprintf("%q TEXT", c.Name)
	}
	createSQL := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %q.%q (%s)`,
		stagingSchema, sh.Name, strings.Join(quotedCols, ", "),
	)
	if _, err := tx.ExecContext(ctx, createSQL); err != nil {
		return 0, fmt.Errorf("create table: %w", err)
	}

	colNames := make([]string, len(sh.Columns))
	for i, c := range sh.Columns {
		colNames[i] = c.Name
	}
	copyStmt, err := tx.PrepareContext(ctx, pq.CopyInSchema(stagingSchema, sh.Name, colNames...))
	if err != nil {
		return 0, fmt.Errorf("prepare COPY: %w", err)
	}

	rowCb := func(n int64) {
		if rep.Cancelled() {
			return
		}
		rep.Update(
			"copy_staging",
			percentOf(sheetIdx, totalSheets),
			rowsBeforeSheet+n, 0, 0,
			fmt.Sprintf("Sheet %d/%d · %d 行", sheetIdx+1, totalSheets, rowsBeforeSheet+n),
		)
	}

	var written int64
	switch ext {
	case ".csv":
		written, err = copyCSVRows(ctx, copyStmt, filePath, len(sh.Columns), rep, rowCb)
	case ".xlsx", ".xls":
		written, err = copyXlsxRows(ctx, copyStmt, filePath, sh.Name, len(sh.Columns), rep, rowCb)
	default:
		copyStmt.Close()
		return 0, fmt.Errorf("unsupported extension: %s", ext)
	}
	if err != nil {
		copyStmt.Close()
		return written, err
	}

	if _, err := copyStmt.ExecContext(ctx); err != nil {
		copyStmt.Close()
		return written, fmt.Errorf("flush COPY: %w", err)
	}
	if err := copyStmt.Close(); err != nil {
		return written, fmt.Errorf("close COPY: %w", err)
	}
	return written, nil
}

// copyCSVRows streams rows from a CSV file (skipping the header) into the
// active COPY statement. Calls rowCb every 1000 rows for progress reporting.
func copyCSVRows(ctx context.Context, stmt *sql.Stmt, filePath string, numCols int, rep progressReporter, rowCb func(int64)) (int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open csv: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	if _, err := r.Read(); err != nil {
		if err == io.EOF {
			return 0, nil
		}
		return 0, fmt.Errorf("read csv header: %w", err)
	}

	var n int64
	for {
		if rep.Cancelled() {
			return n, errors.New("cancelled")
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, fmt.Errorf("read csv row: %w", err)
		}
		args := rowToArgs(row, numCols)
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return n, fmt.Errorf("copy csv row: %w", err)
		}
		n++
		if n%1000 == 0 {
			rowCb(n)
		}
	}
	rowCb(n)
	return n, nil
}

// copyXlsxRows streams rows from an xlsx file into the active COPY statement.
func copyXlsxRows(ctx context.Context, stmt *sql.Stmt, filePath, sheet string, numCols int, rep progressReporter, rowCb func(int64)) (int64, error) {
	iterFn, closeFn, err := pbit.ReadXlsxRows(filePath, sheet)
	if err != nil {
		return 0, fmt.Errorf("open xlsx rows: %w", err)
	}
	defer closeFn() //nolint:errcheck

	var n int64
	for {
		if rep.Cancelled() {
			return n, errors.New("cancelled")
		}
		row, err := iterFn()
		if err != nil {
			return n, fmt.Errorf("read xlsx row: %w", err)
		}
		if row == nil {
			break
		}
		args := rowToArgs(row, numCols)
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return n, fmt.Errorf("copy xlsx row: %w", err)
		}
		n++
		if n%1000 == 0 {
			rowCb(n)
		}
	}
	rowCb(n)
	return n, nil
}

// rowToArgs converts a string slice to []any with length numCols,
// padding with "" if the row is shorter.
func rowToArgs(row []string, numCols int) []any {
	args := make([]any, numCols)
	for i := range args {
		if i < len(row) {
			args[i] = row[i]
		} else {
			args[i] = ""
		}
	}
	return args
}

func percentOf(i, total int) int {
	if total <= 0 {
		return 0
	}
	p := (i * 100) / total
	if p > 100 {
		p = 100
	}
	return p
}
