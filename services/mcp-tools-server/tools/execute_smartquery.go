package tools

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lakehouse2ontology/services/mcp-tools-server/client"
)

// executeSmartqueryInput accepts the QuerySpec opaquely. v0 does not
// validate the spec shape on this side — lakehouse-sql-server is the
// authoritative validator and will return a 400 with a parse error if
// the spec is malformed.
type executeSmartqueryInput struct {
	envelope
	Spec json.RawMessage `json:"spec"`
}

// handleExecuteSmartquery: forward QuerySpec execution to
// lakehouse-sql-server. The MCP client gets the rows response verbatim
// (rows + columns + row_summary + _spec_filters + … whatever the
// engine returned).
func handleExecuteSmartquery(w http.ResponseWriter, r *http.Request) {
	var in executeSmartqueryInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if in.ProjectID == "" {
		writeErr(w, http.StatusBadRequest, "projectId required")
		return
	}
	if len(in.Spec) == 0 || string(in.Spec) == "null" {
		writeErr(w, http.StatusBadRequest, "spec required (canonical QuerySpec object)")
		return
	}

	res, err := client.SmartQueryExecute(r.Context(), client.SmartQueryExecuteRequest{
		ProjectID: in.ProjectID,
		Spec:      in.Spec,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("lakehouse-sql-server execute: %v", err))
		return
	}
	// Forward the response body verbatim.
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res)
}
