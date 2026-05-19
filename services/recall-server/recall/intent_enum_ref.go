// Bounded value-ref contract — recall-server side
// (spec .omc/specs/bounded-value-ref-contract.md §3.3).
//
// recall-server is responsible for filling MetricIntentParameter.AllowedValues
// for every Type=="enum_ref" parameter when it surfaces a MetricIntent over
// the wire. Without this step the LLM prompt renderer in agent-server has
// no candidate set to display, so the LLM falls back to guessing and the
// strict binder responds with PARAM_VALUE_UNKNOWN — the spec's exact
// failure mode this contract exists to remove.
//
// Kept symmetric with services/agent-server/handler/intent_enum_ref.go
// (runtime binder side) on query semantics and the 50-candidate cap.

package recall

import (
	"database/sql"
	"log"
)

// enumRefCandidateCap mirrors the agent-server cap (handler/intent_enum_ref.go).
// Above the cap the candidate slice stays empty so the prompt degrades
// gracefully — emitting a 200-item list would torch the context window.
const enumRefCandidateCap = 50

// fillEnumRefAllowedValues walks the parameter slice and, for every
// Type=="enum_ref" entry, queries the project's lakehouse_keyword candidate
// set and populates AllowedValues IN PLACE on a copy. The input slice is
// not mutated (recall caches MetricIntent structs by ID; mutation would
// poison future cross-project requests).
//
// Errors are logged and the offending parameter is left empty rather than
// failing the whole intent lookup — losing prompt enrichment is recoverable;
// missing the intent entirely is not.
func fillEnumRefAllowedValues(db *sql.DB, projectID string, params []MetricIntentParameter) []MetricIntentParameter {
	if len(params) == 0 {
		return params
	}
	hasEnumRef := false
	for i := range params {
		if params[i].Type == "enum_ref" {
			hasEnumRef = true
			break
		}
	}
	if !hasEnumRef {
		return params
	}
	out := make([]MetricIntentParameter, len(params))
	copy(out, params)
	for i := range out {
		if out[i].Type != "enum_ref" || out[i].Property == "" {
			continue
		}
		cands, truncated, err := resolveEnumRefCandidates(db, projectID, out[i].Property)
		if err != nil {
			log.Printf("recall_lakehouse: enum_ref candidates for %q.%q: %v", projectID, out[i].Property, err)
			continue
		}
		if truncated {
			// > 50 candidates — leave AllowedValues nil so the renderer
			// degrades to type:string display. Logging once per intent is
			// enough to surface "this Intent is mis-declared as enum_ref
			// on a high-cardinality property".
			log.Printf("recall_lakehouse: enum_ref candidates for %q.%q exceed cap (%d), degrading to string", projectID, out[i].Property, enumRefCandidateCap)
			continue
		}
		out[i].AllowedValues = cands
	}
	return out
}

// resolveEnumRefCandidates returns the deduped, sorted keyword set for
// (projectID, propertyName). Counted first to short-circuit high-cardinality
// properties — saves dragging O(thousand) member_id rows over the wire only
// to discard. Matches services/agent-server/handler/intent_enum_ref.go
// byte-for-byte on the query (lock-step duplication is the dependency-gate
// price we pay for keeping the two services independently deployable).
func resolveEnumRefCandidates(db *sql.DB, projectID, propertyName string) ([]string, bool, error) {
	if db == nil || projectID == "" || propertyName == "" {
		return nil, false, nil
	}
	var n int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM lakehouse_keyword k
		JOIN ont_property p ON p.id = k.property_id
		WHERE k.project_id = $1
		  AND LOWER(p.name) = LOWER($2)
		  AND COALESCE(k.is_stopword, false) = false`,
		projectID, propertyName,
	).Scan(&n); err != nil {
		return nil, false, err
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
		projectID, propertyName)
	if err != nil {
		return nil, false, err
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
