// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// DerivedSpec describes a single partition that needs a derived view or table
// created in the staging schema.
type DerivedSpec struct {
	ViewName string
	Kind     PartitionKind
	Meta     MExprMeta
	Cols     []PbitColumn
}

// DerivedResult records the outcome of creating one derived view/table.
type DerivedResult struct {
	ViewName    string
	Kind        PartitionKind
	BaseTables  []string
	MExpression string
	Warning     string
}

// BuildDerivedViews iterates over specs and creates the corresponding
// views / physical tables in the staging schema.  Each operation is
// autocommit (no enclosing transaction).  Failures on individual specs
// set Warning instead of returning an error — the import does NOT fail.
func BuildDerivedViews(db *sql.DB, schema string, specs []DerivedSpec) ([]DerivedResult, error) {
	results := make([]DerivedResult, 0, len(specs))
	for _, spec := range specs {
		r := DerivedResult{
			ViewName:    spec.ViewName,
			Kind:        spec.Kind,
			MExpression: spec.Meta.RawM,
		}

		var buildErr error
		switch spec.Kind {
		case KindCombine:
			buildErr = buildCombineView(db, schema, spec, &r)
		case KindConstantCsv:
			buildErr = buildConstantCsvTable(db, schema, spec, &r)
		case KindUnpivot:
			buildErr = buildUnpivotView(db, schema, spec, &r)
		default:
			buildErr = buildUnsupportedTable(db, schema, spec, &r)
		}

		if buildErr != nil {
			r.Warning = fmt.Sprintf("build error: %v", buildErr)
		}
		results = append(results, r)
	}
	return results, nil
}

// buildCombineView creates a UNION ALL view over the listed base tables.
//
//	CREATE VIEW "schema"."view" AS
//	  SELECT * FROM "schema"."A"
//	  UNION ALL SELECT * FROM "schema"."B" ...
func buildCombineView(db *sql.DB, schema string, spec DerivedSpec, r *DerivedResult) error {
	if len(spec.Meta.Sources) == 0 {
		r.Warning = "KindCombine: no source tables found in M expression"
		return buildUnsupportedTable(db, schema, spec, r)
	}

	var parts []string
	for _, src := range spec.Meta.Sources {
		parts = append(parts, fmt.Sprintf(`SELECT * FROM %s.%s`,
			pq.QuoteIdentifier(schema),
			pq.QuoteIdentifier(src),
		))
	}
	body := strings.Join(parts, "\n  UNION ALL ")

	ddl := fmt.Sprintf(`CREATE OR REPLACE VIEW %s.%s AS %s`,
		pq.QuoteIdentifier(schema),
		pq.QuoteIdentifier(spec.ViewName),
		body,
	)
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("create combine view %q: %w", spec.ViewName, err)
	}
	r.BaseTables = spec.Meta.Sources
	return nil
}

// buildConstantCsvTable creates a physical table and populates it from the
// decoded CSV bytes embedded in the M expression.
func buildConstantCsvTable(db *sql.DB, schema string, spec DerivedSpec, r *DerivedResult) error {
	// Parse CSV bytes.
	reader := csv.NewReader(bytes.NewReader(spec.Meta.DecodedBytes))
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		r.Warning = fmt.Sprintf("CSV parse error: %v", err)
		return buildUnsupportedTable(db, schema, spec, r)
	}

	if len(records) == 0 {
		// Empty CSV — create empty table.
		if tableErr := CreateLakehouseTable(db, schema, spec.ViewName, spec.Cols); tableErr != nil {
			return tableErr
		}
		r.Warning = "constant CSV was empty"
		return nil
	}

	// If HasHeader, use first row as headers; otherwise use PBIT columns.
	var headers []string
	dataRows := records
	if spec.Meta.HasHeader && len(records) > 0 {
		headers = records[0]
		dataRows = records[1:]
	} else {
		for i, c := range spec.Cols {
			if c.Expression == "" { // skip calculated
				headers = append(headers, c.Name)
				_ = i
			}
		}
		if len(headers) == 0 {
			for i := range records[0] {
				headers = append(headers, fmt.Sprintf("col_%d", i+1))
			}
		}
	}

	// Build synthetic PbitColumns from headers for table creation.
	synthCols := make([]PbitColumn, len(headers))
	for i, h := range headers {
		synthCols[i] = PbitColumn{Name: h, DataType: "string"}
	}

	if tableErr := CreateLakehouseTable(db, schema, spec.ViewName, synthCols); tableErr != nil {
		return tableErr
	}

	// Insert decoded rows.
	idx := 0
	rowIter := func() ([]string, error) {
		if idx >= len(dataRows) {
			return nil, nil
		}
		row := dataRows[idx]
		idx++
		return row, nil
	}
	if _, copyErr := CopyRowsInto(db, schema, spec.ViewName, headers, rowIter); copyErr != nil {
		r.Warning = fmt.Sprintf("constant CSV insert partial: %v", copyErr)
	}
	return nil
}

// buildUnpivotView attempts a best-effort Unpivot view.  If the shape cannot
// be determined from the stored columns, it falls back to an empty table.
func buildUnpivotView(db *sql.DB, schema string, spec DerivedSpec, r *DerivedResult) error {
	// Best-effort: we don't attempt to fully translate Table.Unpivot M expressions
	// to SQL UNPIVOT syntax.  Create an empty physical table with PBIT columns
	// and record the raw M for future manual population.
	r.Warning = spec.Meta.Warning
	if r.Warning == "" {
		r.Warning = "Table.Unpivot not auto-translated; empty table created, raw M stored"
	}
	return CreateLakehouseTable(db, schema, spec.ViewName, spec.Cols)
}

// buildUnsupportedTable creates an empty physical table with PBIT-declared
// columns and records the warning.
func buildUnsupportedTable(db *sql.DB, schema string, spec DerivedSpec, r *DerivedResult) error {
	if r.Warning == "" {
		r.Warning = "unsupported M expression"
	}
	return CreateLakehouseTable(db, schema, spec.ViewName, spec.Cols)
}
