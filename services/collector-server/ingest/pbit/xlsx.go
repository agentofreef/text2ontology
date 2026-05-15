// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/xuri/excelize/v2"
)

// BindingAutoThreshold: score >= this → state "confirmed" (no human needed).
const BindingAutoThreshold = 0.85

// BindingSuggestThreshold: score >= this (and < Auto) → state "suggested".
const BindingSuggestThreshold = 0.50

// BindingState represents the current binding resolution status between an
// Excel file and a PBIT table.
type BindingState string

const (
	BindingUnmatched BindingState = "unmatched"
	BindingSuggested BindingState = "suggested"
	BindingConfirmed BindingState = "confirmed"
	BindingSkipped   BindingState = "skipped"
	BindingUnrelated BindingState = "unrelated"
)

// XlsxBinding holds the fuzzy-match result for one Excel file against the
// full PBIT table catalogue.
type XlsxBinding struct {
	FileName      string
	TableName     string
	Score         float64
	State         BindingState
	Columns       []string
	AllCandidates []struct {
		Table string
		Score float64
	}
}

// InferredCol is a column name + heuristically inferred SQL data type.
type InferredCol struct {
	Name     string
	DataType string // text | bigint | double precision | timestamp | boolean
}

// NormalizeFileName lowercases the base name, strips the extension, and
// collapses whitespace, underscores, and hyphens into a single space.
func NormalizeFileName(s string) string {
	base := filepath.Base(s)
	ext := filepath.Ext(base)
	if ext != "" {
		base = base[:len(base)-len(ext)]
	}
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.ToLower(base) {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// FilenameSimilarity returns a normalised Levenshtein similarity score in [0,1]
// between the normalised versions of a and b.
func FilenameSimilarity(a, b string) float64 {
	na, nb := NormalizeFileName(a), NormalizeFileName(b)
	if na == nb {
		return 1.0
	}
	dist := levenshtein(na, nb)
	maxLen := len([]rune(na))
	if l := len([]rune(nb)); l > maxLen {
		maxLen = l
	}
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(dist)/float64(maxLen)
}

// HeaderJaccard returns the Jaccard similarity of two header slices after
// lowercasing and trimming all entries.
func HeaderJaccard(a, b []string) float64 {
	setA := headerSet(a)
	setB := headerSet(b)
	intersection := 0
	for k := range setA {
		if setB[k] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0.0
	}
	return float64(intersection) / float64(union)
}

// MatchScore computes the combined binding score for a file↔table pair.
// score = 0.6 * FilenameSimilarity(fileName, tableName) + 0.4 * HeaderJaccard(fileHeaders, tableHeaders)
func MatchScore(fileHeaders, tableHeaders []string, fileName, tableName string) float64 {
	fnSim := FilenameSimilarity(fileName, tableName)
	hjac := HeaderJaccard(fileHeaders, tableHeaders)
	return 0.6*fnSim + 0.4*hjac
}

// ReadXlsxHeaders opens the xlsx at path and returns the headers from the
// first non-empty row of the first sheet.
//
// Uses the streaming Rows() API (instead of GetRows() which materialises the
// whole sheet into memory). For a 100K-row xlsx, this drops the read from
// hundreds of MB / several seconds to a couple of ms — only the first row
// is touched.
func ReadXlsxHeaders(path string) (headers []string, sheet string, err error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, "", nil
	}
	sheet = sheets[0]

	rowsIter, err := f.Rows(sheet)
	if err != nil {
		return nil, sheet, err
	}
	defer rowsIter.Close() //nolint:errcheck

	if !rowsIter.Next() {
		// Empty sheet — propagate any iterator error.
		return nil, sheet, rowsIter.Error()
	}
	cols, err := rowsIter.Columns()
	if err != nil {
		return nil, sheet, err
	}
	for _, h := range cols {
		headers = append(headers, strings.TrimSpace(h))
	}
	return headers, sheet, nil
}

// SheetHeader is one xlsx sheet's name + first-row headers.
// Used by ReadXlsxAllSheetsHeaders to surface multi-sheet workbooks.
type SheetHeader struct {
	Name    string
	Headers []string
}

// ReadXlsxAllSheetsHeaders opens the xlsx at path and returns headers from
// the first non-empty row of EVERY sheet. Empty sheets and sheets without a
// header row are silently skipped. Uses streaming Rows() per sheet so even
// large workbooks finish in milliseconds (no full-sheet load).
func ReadXlsxAllSheetsHeaders(path string) ([]SheetHeader, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sheets := f.GetSheetList()
	out := make([]SheetHeader, 0, len(sheets))
	for _, sheet := range sheets {
		rowsIter, err := f.Rows(sheet)
		if err != nil {
			continue // skip unreadable sheet
		}
		if !rowsIter.Next() {
			rowsIter.Close()
			continue // empty sheet
		}
		cols, err := rowsIter.Columns()
		rowsIter.Close()
		if err != nil {
			continue
		}
		var headers []string
		for _, h := range cols {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			headers = append(headers, h)
		}
		if len(headers) == 0 {
			continue
		}
		out = append(out, SheetHeader{Name: sheet, Headers: headers})
	}
	return out, nil
}

// ReadXlsxRows returns a streaming iterator over data rows (skipping the
// header row) of the given sheet.  The caller must call close() when done.
func ReadXlsxRows(path string, sheet string) (iter func() ([]string, error), close func() error, err error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, nil, err
	}

	rowsIter, err := f.Rows(sheet)
	if err != nil {
		f.Close()
		return nil, nil, err
	}

	// Skip header row.
	rowsIter.Next()

	closeFn := func() error {
		rowsIter.Close()
		return f.Close()
	}

	iterFn := func() ([]string, error) {
		if !rowsIter.Next() {
			return nil, nil // EOF
		}
		cols, err := rowsIter.Columns()
		if err != nil {
			return nil, err
		}
		return cols, nil
	}

	return iterFn, closeFn, nil
}

// InferColumnTypes inspects up to the first 50 sample rows and heuristically
// assigns a SQL data type to each header.
// Supported types: text | bigint | double precision | timestamp | boolean
func InferColumnTypes(headers []string, sampleRows [][]string) []InferredCol {
	cols := make([]InferredCol, len(headers))
	for i, h := range headers {
		cols[i] = InferredCol{Name: h, DataType: "text"}
	}

	limit := 50
	if len(sampleRows) < limit {
		limit = len(sampleRows)
	}

	for i := range headers {
		counts := map[string]int{"bigint": 0, "double": 0, "timestamp": 0, "boolean": 0, "text": 0}
		total := 0
		for _, row := range sampleRows[:limit] {
			if i >= len(row) {
				continue
			}
			v := strings.TrimSpace(row[i])
			if v == "" {
				continue
			}
			total++
			switch {
			case isBoolValue(v):
				counts["boolean"]++
			case isIntValue(v):
				counts["bigint"]++
			case isFloatValue(v):
				counts["double"]++
			case isTimestampValue(v):
				counts["timestamp"]++
			default:
				counts["text"]++
			}
		}
		if total == 0 {
			continue
		}
		// Pick the type with the highest count (text wins ties).
		best, bestN := "text", -1
		for _, t := range []string{"boolean", "bigint", "double", "timestamp"} {
			if counts[t] > bestN {
				bestN = counts[t]
				best = t
			}
		}
		// Only upgrade if ≥80% of non-empty cells agree.
		if best != "text" && float64(counts[best])/float64(total) >= 0.80 {
			if best == "double" {
				cols[i].DataType = "double precision"
			} else {
				cols[i].DataType = best
			}
		}
	}
	return cols
}

// --- helpers ---

func headerSet(h []string) map[string]bool {
	s := make(map[string]bool, len(h))
	for _, v := range h {
		k := strings.ToLower(strings.TrimSpace(v))
		if k != "" {
			s[k] = true
		}
	}
	return s
}

// levenshtein computes the edit distance between two strings (rune-aware).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func isBoolValue(v string) bool {
	// Do NOT treat "0"/"1" as boolean: they are also valid bigint, and a later
	// sample row containing "2" would then fail the COPY with
	// "invalid input syntax for type boolean". Require an explicit textual
	// boolean literal so ambiguous numeric flags stay bigint.
	l := strings.ToLower(v)
	return l == "true" || l == "false" || l == "yes" || l == "no"
}

func isIntValue(v string) bool {
	_, err := strconv.ParseInt(v, 10, 64)
	return err == nil
}

func isFloatValue(v string) bool {
	_, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return false
	}
	// Must actually have a decimal point to distinguish from int.
	return strings.ContainsAny(v, ".eE")
}

func isTimestampValue(v string) bool {
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006/01/02 15:04:05",
		"2006-01-02",
		"01/02/2006",
		"02-Jan-2006",
	}
	for _, l := range layouts {
		if _, err := time.Parse(l, v); err == nil {
			return true
		}
	}
	return false
}
