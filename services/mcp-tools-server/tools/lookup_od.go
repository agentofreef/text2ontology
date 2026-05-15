package tools

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lakehouse2ontology/services/mcp-tools-server/client"
)

// lookupOdInput extends the common envelope with the Od name to look up.
// name can be exact (preferred) or partial (returns first match).
type lookupOdInput struct {
	envelope
	Name string `json:"name"`
}

// lookupOdOutput packs one Od summary plus its property list. Shape is
// stable across MCP v0 — downstream consumers can rely on these keys.
type lookupOdOutput struct {
	Found      bool               `json:"found"`
	Object     *client.Object     `json:"object,omitempty"`
	Properties []client.Property  `json:"properties,omitempty"`
	Candidates []client.Object    `json:"candidates,omitempty"`
}

// handleLookupOd: resolve an Od by name within a project and return its schema.
//
//  1. POST backend-api → ListObjects(projectId, name)
//  2. If more than 1 match, return candidate list (no properties fetched
//     — let the caller narrow by exact name).
//  3. If exactly 1 match, POST backend-api → ListProperties(projectId,
//     objectTypeId=matched.ID) and return the pair.
//
// The external wire contract is symmetric with the monolith's legacy
// `lakehouseToolLookup` output ("found" + "object" + "properties")
// without the full knowledge graph expansion that the agent needs.
func handleLookupOd(w http.ResponseWriter, r *http.Request) {
	var in lookupOdInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if in.ProjectID == "" {
		writeErr(w, http.StatusBadRequest, "projectId required")
		return
	}
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}

	objects, err := client.ListObjects(r.Context(), in.ProjectID, in.Name)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("backend-api ListObjects: %v", err))
		return
	}

	if len(objects) == 0 {
		writeJSON(w, http.StatusOK, lookupOdOutput{Found: false})
		return
	}
	if len(objects) > 1 {
		writeJSON(w, http.StatusOK, lookupOdOutput{Found: false, Candidates: objects})
		return
	}

	obj := objects[0]
	props, err := client.ListProperties(r.Context(), in.ProjectID, obj.ID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("backend-api ListProperties: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, lookupOdOutput{
		Found:      true,
		Object:     &obj,
		Properties: props,
	})
}
