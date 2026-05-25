package recall

import (
	"log"
	"os"
	"strconv"
	"sync"
)

// Truncation budgets for FormatContext (the LLM-facing recall markdown).
//
// Original hard-coded values were too aggressive — see comments next to each
// call site. Both knobs are env-configurable so a project with denser
// business descriptions can lift the cap without a code change.
//
//   RECALL_UNMATCHED_PROP_DESC_LEN — others[] TOON row description budget.
//                                    Default: 100 runes (was 15).
//   RECALL_AMBIG_PROP_DESC_LEN     — ⚠ 需要澄清 candidate property desc.
//                                    Default: 300 runes (was 40).
//
// Bad/missing env values fall back to defaults with a one-shot WARN log.

const (
	defaultUnmatchedPropDescLen = 100
	defaultAmbigPropDescLen     = 300
)

var (
	unmatchedPropDescLenOnce  sync.Once
	unmatchedPropDescLenValue int
	ambigPropDescLenOnce      sync.Once
	ambigPropDescLenValue     int
)

func unmatchedPropDescLen() int {
	unmatchedPropDescLenOnce.Do(func() {
		unmatchedPropDescLenValue = readEnvInt("RECALL_UNMATCHED_PROP_DESC_LEN", defaultUnmatchedPropDescLen)
	})
	return unmatchedPropDescLenValue
}

func ambigPropDescLen() int {
	ambigPropDescLenOnce.Do(func() {
		ambigPropDescLenValue = readEnvInt("RECALL_AMBIG_PROP_DESC_LEN", defaultAmbigPropDescLen)
	})
	return ambigPropDescLenValue
}

// readEnvInt parses a positive int from env or returns the default. Negative /
// zero / unparseable values are treated as "use default" with a WARN — silently
// clamping a misconfiguration would hide the typo from the operator.
func readEnvInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("recall: invalid %s=%q (want positive int); using default %d", name, raw, fallback)
		return fallback
	}
	return n
}
