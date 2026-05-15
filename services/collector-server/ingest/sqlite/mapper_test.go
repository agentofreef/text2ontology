package sqlite

import "testing"

func TestNormalizeSqliteType(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// SQLite 5 affinity classes — cover each path
		{"INTEGER", "integer"},
		{"BIGINT", "integer"},
		{"INT2", "integer"},
		{"TINYINT", "integer"},
		{"REAL", "double precision"},
		{"DOUBLE PRECISION", "double precision"},
		{"FLOAT", "double precision"},
		{"NUMERIC", "numeric"},
		{"NUMERIC(10,2)", "numeric"},
		{"DECIMAL(10,2)", "numeric"},
		{"BOOLEAN", "boolean"},
		{"BOOL", "boolean"},
		{"DATETIME", "timestamp"},
		{"TIMESTAMP", "timestamp"},
		{"DATE", "date"},
		{"TIME", "time"},
		{"TEXT", "text"},
		{"VARCHAR", "text"},
		{"VARCHAR(20)", "text"},
		{"CHARACTER(8)", "text"},
		{"NVARCHAR(255)", "text"},
		{"CLOB", "text"},
		{"STRING", "text"},
		{"BLOB", "bytea"},
		// Edge cases
		{"", "text"},
		{"  ", "text"},
		{"unknown_type", "text"},
		// Case insensitivity
		{"integer", "integer"},
		{"Integer", "integer"},
		{"varchar(20)", "text"},
		// Chinook real-world types
		{"NVARCHAR(160)", "text"},
		{"INTEGER PRIMARY KEY", "integer"},
		// "INTEGER PRIMARY KEY" actually contains INT so → integer ✓
	}
	for _, c := range cases {
		got := normalizeSqliteType(c.in)
		if got != c.want {
			t.Errorf("normalizeSqliteType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Album", `"Album"`},
		{"PlaylistTrack", `"PlaylistTrack"`},
		{"weird name", `"weird name"`},
		// Embedded double quotes must be doubled per SQLite spec.
		{`with"quote`, `"with""quote"`},
	}
	for _, c := range cases {
		got := quoteIdent(c.in)
		if got != c.want {
			t.Errorf("quoteIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEncodeForText(t *testing.T) {
	cases := []struct {
		in   any
		want any // nil = stay nil; otherwise must equal
	}{
		{nil, nil},
		{"hello", "hello"},
		{[]byte("bytes"), "bytes"},
		{int64(42), "42"},
		{int(7), "7"},
		{float64(3.14), "3.14"},
		{true, "true"},
		{false, "false"},
	}
	for _, c := range cases {
		got := encodeForText(c.in)
		if got != c.want {
			t.Errorf("encodeForText(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
