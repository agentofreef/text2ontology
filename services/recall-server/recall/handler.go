package recall

import (
	"database/sql"
	"encoding/json"
	"net/http"

	. "github.com/lakehouse2ontology/httputil"
)

// HandleLakehouseDebug returns an HTTP handler for the lakehouse token recall debug endpoint.
// Uses BuildLakehouseContext (lakehouse_keyword 2-tier) instead of BuildContext (keyword_explanation 3-tier).
//
//	POST /api/ontology/lakehouse-token-recall-debug?projectId=xxx
//	Body: { "tokens": ["PCV", "品牌", "customer"] }
func HandleLakehouseDebug(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "POST only"})
			return
		}

		pid := GetProjectID(r)
		body := ReadBody(r)

		var tokens []string
		if raw, ok := body["tokens"]; ok {
			switch v := raw.(type) {
			case []interface{}:
				for _, item := range v {
					if s, ok := item.(string); ok && s != "" {
						tokens = append(tokens, s)
					}
				}
			case string:
				for _, s := range splitTokenString(v) {
					if s != "" {
						tokens = append(tokens, s)
					}
				}
			}
		}

		if len(tokens) == 0 {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "tokens array required"})
			return
		}

		result := BuildLakehouseContext(r.Context(), db, pid, tokens, "debug")

		// Debug-only top-N vector candidates for tokens that produced no Tier 1/2
		// hit. Same UNION the correction Tier 4 uses, but unfiltered. The live
		// agent never calls this — it's strictly a visibility aid for this page.
		vectorCandidates := map[string][]VectorCandidate{}
		for _, tok := range tokens {
			if hits, ok := result.TokenDetails[tok]; ok && len(hits) > 0 {
				continue
			}
			if cands := LakehouseVectorTopN(r.Context(), db, pid, tok, 5); cands != nil {
				vectorCandidates[tok] = cands
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(M{
			"recall":           result,
			"vectorCandidates": vectorCandidates,
		})
	}
}

// splitTokenString splits a string by common delimiters into tokens.
func splitTokenString(s string) []string {
	var tokens []string
	var current []rune
	for _, r := range s {
		if r == ',' || r == '\n' || r == '|' || r == ';' {
			if len(current) > 0 {
				tokens = append(tokens, string(current))
				current = current[:0]
			}
		} else if r == ' ' && len(current) > 0 {
			// Keep spaces within tokens
			current = append(current, r)
		} else if r != ' ' {
			current = append(current, r)
		}
	}
	if len(current) > 0 {
		tokens = append(tokens, string(current))
	}
	// Trim trailing spaces
	for i := range tokens {
		trimmed := ""
		for _, r := range tokens[i] {
			trimmed = string(append([]rune(trimmed), r))
		}
		tokens[i] = trimmed
	}
	return tokens
}
