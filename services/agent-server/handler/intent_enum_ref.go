// Bounded value-ref contract — caller-side helpers
// (spec .omc/specs/bounded-value-ref-contract.md §3.2 / §6).
//
// The smartquery binder is intentionally pure: it does not touch DB. The
// caller (handler) is responsible for resolving each "enum_ref" parameter's
// candidate list out of lakehouse_keyword and populating
// IntentParameter.AllowedValues BEFORE invoking BindIntentParams. This file
// holds that resolution logic plus the prompt-rendering hint extender so
// the LLM sees the candidates instead of guessing.

package handler

import (
	"database/sql"
	"fmt"

	"github.com/lakehouse2ontology/services/agent-server/smartquery"
)

// enumRefCandidateCap caps the candidate list a single enum_ref parameter
// will surface. Above the cap the parameter degrades to "string" behavior:
//   - resolveEnumRefCandidates reports Truncated=true and an empty slice
//   - applyEnumRefCandidates leaves AllowedValues nil → binder skips strict
//     check (PARAM_VALUE_UNKNOWN won't fire) → prompt renderer also skips
//     the candidate listing
//
// Rationale: spec §3.3 — high-cardinality enums blow up the LLM context
// and are usually a sign someone declared enum_ref on the wrong property
// (e.g. member_id). 50 is a deliberately tight bound; raising it should be
// a separate Intent-author decision, not silently allowed.
const enumRefCandidateCap = 50

// resolveEnumRefCandidates returns the lakehouse_keyword.keyword set for
// (projectID, p.Property), case-insensitively de-duplicated and sorted so
// prompt rendering is stable across runs.
//
// Returns:
//   - candidates: sorted slice; empty when the property has no keywords
//   - truncated:  true when the underlying row count exceeds
//     enumRefCandidateCap; candidates is then empty so the caller can
//     decide to degrade the param to string-behavior (don't strict-check
//     against an incomplete list).
//   - err:        sql / arg errors. Empty Property is an Intent-author
//     mistake — surface it here so the caller can log + fall back rather
//     than producing a misleading PARAM_VALUE_UNKNOWN at bind time.
//
// Lock-step with spec §3.2 query semantics: matches the keyword column
// directly (aliases column is consulted only at bind/correction time, not
// at candidate enumeration time — surfacing aliases would explode the
// prompt list with noise that's already covered by case-insensitive match
// in the binder).
func resolveEnumRefCandidates(db *sql.DB, projectID string, p smartquery.IntentParameter) ([]string, bool, error) {
	if p.Property == "" {
		return nil, false, fmt.Errorf("enum_ref param %q missing Property", p.Name)
	}
	if db == nil {
		return nil, false, fmt.Errorf("nil db")
	}
	// First check count — cheap exit when the property is over-cardinality
	// (saves dragging 5k member_id rows back over the wire only to discard).
	var n int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM lakehouse_keyword k
		JOIN ont_property p ON p.id = k.property_id
		WHERE k.project_id = $1
		  AND LOWER(p.name) = LOWER($2)
		  AND COALESCE(k.is_stopword, false) = false`,
		projectID, p.Property,
	).Scan(&n); err != nil {
		return nil, false, fmt.Errorf("count enum_ref candidates: %w", err)
	}
	if n == 0 {
		return nil, false, nil
	}
	if n > enumRefCandidateCap {
		return nil, true, nil
	}

	rows, err := db.Query(`
		SELECT DISTINCT k.keyword
		FROM lakehouse_keyword k
		JOIN ont_property p ON p.id = k.property_id
		WHERE k.project_id = $1
		  AND LOWER(p.name) = LOWER($2)
		  AND COALESCE(k.is_stopword, false) = false
		ORDER BY k.keyword`,
		projectID, p.Property)
	if err != nil {
		return nil, false, fmt.Errorf("list enum_ref candidates: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, n)
	for rows.Next() {
		var kw string
		if err := rows.Scan(&kw); err != nil {
			return nil, false, err
		}
		out = append(out, kw)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, false, nil
}

// applyEnumRefCandidates walks the parameter schema, resolves candidates
// for every enum_ref entry, and returns a NEW slice with AllowedValues
// populated. Non-enum_ref entries pass through unchanged; enum_ref entries
// whose property exceeds enumRefCandidateCap also pass through with
// AllowedValues nil (binder will fall back to string semantics — see
// IntentParameter doc comment).
//
// The function never mutates the input slice — strict-mode dispatch reuses
// the schema for prompt rendering (renderEnumRefHints) and we don't want
// the side-channel "I've been populated already" flag bleeding between
// callers.
func applyEnumRefCandidates(db *sql.DB, projectID string, params []smartquery.IntentParameter) []smartquery.IntentParameter {
	if len(params) == 0 {
		return params
	}
	out := make([]smartquery.IntentParameter, len(params))
	copy(out, params)
	for i := range out {
		if out[i].Type != "enum_ref" {
			continue
		}
		cands, truncated, err := resolveEnumRefCandidates(db, projectID, out[i])
		if err != nil || truncated {
			// Truncated or error → leave AllowedValues nil so binder falls
			// back to string semantics. Logging happens at the caller so
			// this helper stays pure-data.
			continue
		}
		// Non-nil even when empty — that's the binder signal "strict mode
		// is on, this property simply has no candidates". Empty list
		// produces PARAM_VALUE_UNKNOWN for any non-empty input, which is
		// what the spec asks for (§3.2 step 5).
		if cands == nil {
			cands = []string{}
		}
		out[i].AllowedValues = cands
	}
	return out
}

