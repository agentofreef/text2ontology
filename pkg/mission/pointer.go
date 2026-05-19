package mission

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Pointer invariant (spec §0.5, "纸带"): any value produced by a prior
// tool result must appear in later dispatch args / verify expressions /
// evidence as a structural reference, never as a copied literal. A
// reference is mechanically verifiable and carries provenance; a copied
// literal can be mistyped or hallucinated.
//
// This file provides the two primitives the mutator gate needs:
//   - IsRef:          recognise a reference token
//   - ScanForLiteral: find a literal that should have been a reference

// refPattern matches a structural reference:
//
//	t1            whole table
//	t1.city       a column
//	t1.city[0]    one cell (column, row)
//	mABC.t1.city[0]   the same, in a parent mission (cross-mission)
var refPattern = regexp.MustCompile(`^(?:m[0-9A-Za-z_-]+\.)?t\d+(?:\.[^.\[\]]+(?:\[\d+\])?)?$`)

// IsRef reports whether s is a structural reference rather than a
// literal value. A reference needs no pointer-invariant check — it is
// the pointer.
func IsRef(s string) bool {
	return refPattern.MatchString(strings.TrimSpace(s))
}

// Violation describes a literal that should have been a reference.
type Violation struct {
	Literal  string // the offending copied value
	Path     string // where it was found inside the scanned value (for debug)
	ShouldBe string // the reference it matches, e.g. "t1.city[0]"
}

func (v Violation) Error() string {
	return fmt.Sprintf("POINTER_INVARIANT_VIOLATED: literal %q at %s should be the reference %q",
		v.Literal, v.Path, v.ShouldBe)
}

// ScanForLiteral walks value (string / map / slice, as produced by JSON
// unmarshalling) and returns the first string leaf that exactly matches
// a cell already present in steps — i.e. a value the LLM copied that
// should be a reference. Literals in exempt (e.g. question-origin
// tokens) are allowed through. The scan is deterministic: maps are
// walked in sorted key order so the "first" violation is stable.
//
// Numeric leaves are not scanned in this milestone — the dominant
// transcription risk (enum/dimension values, WHERE filter values) is
// string-typed, and answer-text numbers are already covered by the
// data-template references. Numeric dispatch-arg scanning lands in M2.
func ScanForLiteral(value any, steps map[string]StepResult, exempt map[string]bool) (Violation, bool) {
	return scan(value, "$", buildCellIndex(steps), exempt)
}

// buildCellIndex maps every string cell value to a reference pointing
// at it. First occurrence wins, so the reference is stable.
func buildCellIndex(steps map[string]StepResult) map[string]string {
	idx := make(map[string]string)
	stepIDs := make([]string, 0, len(steps))
	for id := range steps {
		stepIDs = append(stepIDs, id)
	}
	sort.Strings(stepIDs)
	for _, stepID := range stepIDs {
		for rowIdx, row := range steps[stepID].Rows {
			cols := make([]string, 0, len(row))
			for c := range row {
				cols = append(cols, c)
			}
			sort.Strings(cols)
			for _, col := range cols {
				s, ok := row[col].(string)
				if !ok || strings.TrimSpace(s) == "" {
					continue
				}
				if _, exists := idx[s]; !exists {
					idx[s] = fmt.Sprintf("%s.%s[%d]", stepID, col, rowIdx)
				}
			}
		}
	}
	return idx
}

func scan(value any, path string, index map[string]string, exempt map[string]bool) (Violation, bool) {
	switch v := value.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" || IsRef(s) || exempt[s] {
			return Violation{}, false
		}
		if ref, hit := index[s]; hit {
			return Violation{Literal: s, Path: path, ShouldBe: ref}, true
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if viol, found := scan(v[k], path+"."+k, index, exempt); found {
				return viol, true
			}
		}
	case []any:
		for i, item := range v {
			if viol, found := scan(item, fmt.Sprintf("%s[%d]", path, i), index, exempt); found {
				return viol, true
			}
		}
	}
	return Violation{}, false
}
