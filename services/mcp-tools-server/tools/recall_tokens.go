package tools

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lakehouse2ontology/services/mcp-tools-server/client"
)

// recallTokensInput — external callers supply either pre-split tokens
// or a natural-language question (or both). recall-server's
// build-context endpoint accepts both paths and runs the appropriate
// pipeline on the server side.
type recallTokensInput struct {
	envelope
	Tokens   []string `json:"tokens"`
	Question string   `json:"question"`
}

// handleRecallTokens: forward to recall-server's BuildContext. The
// response is the full RecallResult JSON including Od blocks, MetricIntents,
// MatchedOks, MatchedOls, per-token scoring, and the composed context
// markdown. Useful for external tools that want to piggyback on the
// ontology-aware recall pipeline without re-implementing it.
func handleRecallTokens(w http.ResponseWriter, r *http.Request) {
	var in recallTokensInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if in.ProjectID == "" {
		writeErr(w, http.StatusBadRequest, "projectId required")
		return
	}
	if len(in.Tokens) == 0 && in.Question == "" {
		writeErr(w, http.StatusBadRequest, "either tokens or question is required")
		return
	}

	res, err := client.RecallBuildContext(r.Context(), client.RecallBuildContextRequest{
		ProjectID: in.ProjectID,
		Tokens:    in.Tokens,
		Question:  in.Question,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("recall-server build-context: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res)
}
