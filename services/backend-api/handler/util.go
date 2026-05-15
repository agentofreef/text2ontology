package handler

import (
	"fmt"

	. "github.com/lakehouse2ontology/httputil"
)

// Shared helpers used across multiple shadow-copied handler files in
// this package. Phase 3 B2 brought numVal over for handler_skill.go;
// Phase 3 B5 added itoa for handler_ol.go (learned-facts pagination).

// numVal extracts a numeric value from a map (JSON numbers are float64 in Go).
func numVal(m M, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// itoa converts an int to its decimal string representation.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// intValDefault lives in handler_intent.go (X2). Kept near numVal/itoa
// in spirit; the call sites are handler_export_import.go + handler_intent.go.

// sanitizeFilenameASCII strips disallowed filename chars and non-ASCII
// so browser-safe `filename=` headers can fall back to an ASCII form.
// Duplicates the monolith helper of the same name (which stays in
// handler_lh_testing_export.go until lh_testing migrates too).
func sanitizeFilenameASCII(s string) string {
	var b []byte
	for _, r := range s {
		if r < 32 || r > 126 {
			continue
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b = append(b, '_')
		default:
			b = append(b, byte(r))
		}
	}
	out := string(b)
	for len(out) > 0 && (out[0] == ' ' || out[len(out)-1] == ' ') {
		if out[0] == ' ' {
			out = out[1:]
			continue
		}
		out = out[:len(out)-1]
	}
	// Replace spaces with underscores.
	rep := make([]byte, 0, len(out))
	for i := 0; i < len(out); i++ {
		if out[i] == ' ' {
			rep = append(rep, '_')
		} else {
			rep = append(rep, out[i])
		}
	}
	return string(rep)
}
