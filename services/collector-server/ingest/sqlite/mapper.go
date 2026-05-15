package sqlite

import "strings"

// normalizeSqliteType maps a SQLite-declared type string to a canonical
// postgres information_schema-style string so the existing postgres mapper
// (pgTypeToPbit) can produce correct PBIT data types without source-type
// awareness.
//
// SQLite uses "type affinity" — declared types are loose suggestions; any
// column can store any value. We map by substring keyword per the SQLite
// spec's affinity rules (https://www.sqlite.org/datatype3.html § 3.1):
//
//	Contains "INT"  → INTEGER affinity
//	Contains "CHAR"/"CLOB"/"TEXT" → TEXT affinity
//	Contains "BLOB" or empty → BLOB affinity
//	Contains "REAL"/"FLOA"/"DOUB" → REAL affinity
//	Else → NUMERIC affinity
//
// Plus a few common SQL92 names (DATETIME / DATE / TIME / BOOL / NUMERIC /
// DECIMAL) that the spec doesn't call out explicitly but which appear in
// real-world SQLite schemas (e.g. Chinook uses NUMERIC, DATETIME).
func normalizeSqliteType(declared string) string {
	t := strings.ToUpper(strings.TrimSpace(declared))
	if t == "" {
		return "text"
	}
	switch {
	case strings.Contains(t, "INT"):
		// INTEGER, BIGINT, SMALLINT, TINYINT, INT2, INT4, INT8, MEDIUMINT
		return "integer"
	case strings.Contains(t, "BOOL"):
		// BOOLEAN, BOOL
		return "boolean"
	case strings.Contains(t, "DATETIME") || strings.Contains(t, "TIMESTAMP"):
		return "timestamp"
	case strings.Contains(t, "DATE"):
		return "date"
	case strings.Contains(t, "TIME"):
		return "time"
	case strings.Contains(t, "REAL") || strings.Contains(t, "FLOA") || strings.Contains(t, "DOUB"):
		// REAL, FLOAT, DOUBLE, DOUBLE PRECISION
		return "double precision"
	case strings.Contains(t, "NUMERIC") || strings.Contains(t, "DECIMAL"):
		return "numeric"
	case strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || strings.Contains(t, "TEXT") || strings.Contains(t, "STRING"):
		// VARCHAR, NVARCHAR, CHARACTER, TEXT, CLOB
		return "text"
	case strings.Contains(t, "BLOB"):
		return "bytea"
	default:
		// Unknown declared type → safe textual fallback (will become "string"
		// in PBIT mapper).
		return "text"
	}
}
