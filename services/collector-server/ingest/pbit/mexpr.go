// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

// PartitionKind classifies the M expression found in a PBIT partition.
type PartitionKind int

const (
	KindUnsupported PartitionKind = iota // anything not recognised
	KindCombine                          // Table.Combine({src1, src2, ...})
	KindConstantCsv                      // Binary.FromText("...", BinaryEncoding.Base64) + Csv.Document
	KindUnpivot                          // Table.Unpivot / Table.UnpivotOtherColumns
)

// MExprMeta carries the parsed details of a partition M expression.
type MExprMeta struct {
	Kind         PartitionKind
	Sources      []string // KindCombine: table names referenced
	DecodedBytes []byte   // KindConstantCsv: decoded CSV bytes
	HasHeader    bool     // KindConstantCsv: true if Csv.Document has HasHeaders=true
	RawM         string   // always set: original M expression
	Warning      string   // any parse warning
}

// Regexes used by ClassifyPartition.
var (
	// Table.Combine({<ref1>, <ref2>, ...}) — capture the inner brace group.
	reCombine = regexp.MustCompile(`(?i)Table\.Combine\s*\(\s*\{([^}]*)\}`)

	// Reference to another table in Power Query: <TableName> or #"<TableName>"
	reTableRef = regexp.MustCompile(`#?"([^"]+)"|(\b[A-Za-z_]\w*\b)`)

	// Binary.FromText("...", BinaryEncoding.Base64)
	reBase64 = regexp.MustCompile(`(?i)Binary\.FromText\s*\(\s*"([^"]+)"\s*,\s*BinaryEncoding\.Base64`)

	// Csv.Document(..., [HasHeaders=true ...])
	reCsvHasHeaders = regexp.MustCompile(`(?i)HasHeaders\s*=\s*true`)

	// Table.Unpivot / Table.UnpivotOtherColumns
	reUnpivot = regexp.MustCompile(`(?i)Table\.(Unpivot|UnpivotOtherColumns)\s*\(`)
)

// ClassifyPartition analyses a raw M expression string and returns its kind
// plus the parsed metadata.  It never returns an error for unrecognised
// expressions — those become KindUnsupported with RawM preserved.
func ClassifyPartition(m string) (PartitionKind, MExprMeta, error) {
	trimmed := strings.TrimSpace(m)
	meta := MExprMeta{RawM: trimmed}

	// --- KindCombine ---
	if matches := reCombine.FindStringSubmatch(trimmed); len(matches) >= 2 {
		inner := matches[1]
		sources := extractTableRefs(inner)
		meta.Kind = KindCombine
		meta.Sources = sources
		return KindCombine, meta, nil
	}

	// --- KindConstantCsv ---
	if b64match := reBase64.FindStringSubmatch(trimmed); len(b64match) >= 2 {
		decoded, err := base64.StdEncoding.DecodeString(b64match[1])
		if err != nil {
			// Try URL-safe encoding as a fallback.
			decoded, err = base64.URLEncoding.DecodeString(b64match[1])
		}
		if err != nil {
			meta.Warning = fmt.Sprintf("base64 decode failed: %v", err)
			meta.Kind = KindUnsupported
			return KindUnsupported, meta, nil
		}
		meta.Kind = KindConstantCsv
		meta.DecodedBytes = decoded
		meta.HasHeader = reCsvHasHeaders.MatchString(trimmed)
		return KindConstantCsv, meta, nil
	}

	// --- KindUnpivot ---
	if reUnpivot.MatchString(trimmed) {
		meta.Kind = KindUnpivot
		meta.Warning = "Table.Unpivot detected; shape mapped best-effort"
		return KindUnpivot, meta, nil
	}

	// --- KindUnsupported ---
	meta.Kind = KindUnsupported
	meta.Warning = "unsupported M expression"
	return KindUnsupported, meta, nil
}

// extractTableRefs parses the inner brace content of Table.Combine and
// returns a deduplicated list of referenced table names.
// Power Query references look like: <Name> or #"Table Name"
func extractTableRefs(inner string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reTableRef.FindAllStringSubmatch(inner, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
