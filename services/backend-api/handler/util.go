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
