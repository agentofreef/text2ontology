// Package pbitlakehouse — cross-source name de-collision for multi-source projects.
//
// A project may hold several data sources (multiple PBIX / file / postgres /
// sqlite). They all land physical tables in the SAME per-project schema
// (proj_<hex>) and register ont_object_type rows under the SAME project_id.
// Power BI models routinely reuse generic table names (Sales, Calendar,
// DimDate), so two sources in one project collide on both the physical table
// name and the ont_object_type (project_id, name) unique key.
//
// The whole query stack (smartquery resolves an object by (project_id, name);
// recall derives keywords from the object name) assumes the object name is
// UNIQUE within a project. Rather than weaken that invariant everywhere, we
// keep it true by de-colliding at ingest time: when an incoming table name is
// already taken by ANOTHER data source, the new table + object get a
// deterministic suffix derived from the source label, e.g. "Sales (北区)".
//
// Re-running the SAME source (job retry → same data_source_id) reuses the
// original names, so the de-collision is idempotent. This file MUST be called
// while holding the per-project advisory lock (see WithProjectLock) so the
// "is this name taken?" probes are race-free against a concurrent import into
// the same project.
package pbit

import (
	"crypto/sha1" //nolint:gosec // short non-cryptographic uniqueness suffix only
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// maxIdentBytes is the PostgreSQL identifier byte limit. Names (table + object)
// are kept at or under this so the generated DDL never silently truncates.
const maxIdentBytes = 63

// maxDecollideAttempts bounds the suffixed-candidate search before falling back
// to a data-source-hash suffix (which is effectively always unique).
const maxDecollideAttempts = 1000

// truncToBytes returns s truncated to at most n UTF-8 bytes without splitting a
// multi-byte rune, trimming any trailing space left by the cut.
func truncToBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	b := make([]byte, 0, n)
	for _, r := range s {
		rb := []byte(string(r))
		if len(b)+len(rb) > n {
			break
		}
		b = append(b, rb...)
	}
	return strings.TrimRight(string(b), " ")
}

// fitIdent clamps an identifier to the Postgres byte limit (rune-safe).
func fitIdent(s string) string { return truncToBytes(s, maxIdentBytes) }

// sanitizeLabel turns a data_source label (often the upload filename) into a
// short, human-readable suffix token: drops a known file extension, strips
// quotes/control chars, collapses whitespace, caps length, and never empty.
func sanitizeLabel(label string) string {
	l := label
	for _, ext := range []string{".pbix", ".pbit", ".csv", ".xlsx", ".xls", ".sqlite", ".db"} {
		if strings.HasSuffix(strings.ToLower(l), ext) {
			l = l[:len(l)-len(ext)]
			break
		}
	}
	l = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' {
			return ' '
		}
		return r
	}, l)
	l = strings.Join(strings.Fields(l), " ")
	if rs := []rune(l); len(rs) > 24 {
		l = strings.TrimSpace(string(rs[:24]))
	}
	if l == "" {
		return "src"
	}
	return l
}

// candidateName returns the attempt-th de-collision candidate for raw:
//
//	attempt 0       → raw                       (the original, unsuffixed name)
//	attempt 1       → "raw (label)"
//	attempt N >= 2  → "raw (label N)"
//
// The result is always within maxIdentBytes, reserving room for the suffix so
// the disambiguating tail is never the part that gets truncated.
func candidateName(raw, label string, attempt int) string {
	if attempt <= 0 {
		return fitIdent(raw)
	}
	var suffix string
	if attempt == 1 {
		suffix = " (" + label + ")"
	} else {
		suffix = fmt.Sprintf(" (%s %d)", label, attempt)
	}
	if len(suffix) >= maxIdentBytes {
		return fitIdent(raw + suffix)
	}
	return truncToBytes(raw, maxIdentBytes-len(suffix)) + suffix
}

// shortHash is a non-cryptographic 8-hex-char digest used as a last-resort
// uniqueness suffix keyed on the data_source id.
func shortHash(s string) string {
	h := sha1.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(h[:4])
}

// ResolveCollisionFreeNames maps each raw table name to a name that is unique
// within the project (across all data sources), so multiple sources coexist in
// one proj_<hex> schema without clobbering each other.
//
// Contract:
//   - Call while holding the per-project advisory lock (WithProjectLock).
//   - dsID is the importing data_source id. A raw name whose ont_object_type
//     already belongs to dsID is reused as-is (idempotent retry of the same
//     source). A name taken by a DIFFERENT source (or a NULL/manual object, or
//     an existing physical table) is suffixed via the label until free.
//   - The returned final name is used for BOTH the physical table and the
//     ont_object_type.name, keeping smartquery/recall's "name unique per
//     project" invariant intact.
func ResolveCollisionFreeNames(db *sql.DB, schema, projectID, dsID, label string, rawNames []string) (map[string]string, error) {
	lbl := sanitizeLabel(label)
	out := make(map[string]string, len(rawNames))
	taken := make(map[string]bool, len(rawNames))

	for _, raw := range rawNames {
		// Idempotent retry: this object name already belongs to this source.
		owner, exists, err := objectOwner(db, projectID, raw)
		if err != nil {
			return nil, err
		}
		if exists && owner == dsID {
			out[raw] = raw
			taken[raw] = true
			continue
		}

		final := ""
		for attempt := 0; attempt < maxDecollideAttempts; attempt++ {
			cand := candidateName(raw, lbl, attempt)
			if taken[cand] {
				continue
			}
			free, err := nameFree(db, schema, projectID, cand)
			if err != nil {
				return nil, err
			}
			if free {
				final = cand
				break
			}
		}
		if final == "" {
			// Pathological: fall back to a per-source hash suffix.
			final = fitIdent(truncToBytes(raw, maxIdentBytes-12) + " (" + shortHash(dsID) + ")")
		}
		out[raw] = final
		taken[final] = true
	}
	return out, nil
}

// objectOwner returns the data_source_id (as text, "" when NULL) of the
// ont_object_type row named name in this project, and whether such a row exists.
func objectOwner(db *sql.DB, projectID, name string) (owner string, exists bool, err error) {
	var o sql.NullString
	err = db.QueryRow(
		`SELECT data_source_id::text FROM ont_object_type WHERE project_id=$1 AND name=$2`,
		projectID, name,
	).Scan(&o)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if o.Valid {
		return o.String, true, nil
	}
	return "", true, nil
}

// nameFree reports whether name is unused as both an ont_object_type name and a
// physical table in the project schema. (Same-source reuse is handled by the
// caller before nameFree is consulted.)
func nameFree(db *sql.DB, schema, projectID, name string) (bool, error) {
	_, exists, err := objectOwner(db, projectID, name)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	pe, err := physicalTableExists(db, schema, name)
	if err != nil {
		return false, err
	}
	return !pe, nil
}

// physicalTableExists reports whether schema.name is a real table already.
func physicalTableExists(db *sql.DB, schema, name string) (bool, error) {
	var x int
	err := db.QueryRow(
		`SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2`,
		schema, name,
	).Scan(&x)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
