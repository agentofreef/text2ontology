// Package tools implements the MCP tool dispatcher. Each tool is a
// plain http.HandlerFunc bound to POST /api/mcp/v1/tools/<name>.
// Request body is JSON; response body is JSON on success, JSON with
// "error" key on failure (always 200 for tool-level errors so MCP
// clients treat failure as tool output, not transport error).
//
// Tool surface (v0):
//
//	lookup_od          — resolve an Od name to its schema (properties)
//	execute_smartquery — run a canonical QuerySpec and return rows
//	recall_tokens      — token → Od/Ok/Ol/MetricIntent recall
//
// All three accept at minimum {"projectId":"<uuid>"}.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/lakehouse2ontology/services/mcp-tools-server/auth"
)

// envelope is the common input shape for all three tools.
type envelope struct {
	ProjectID string `json:"projectId"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("tools: write response failed: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// Dispatch is the single /api/mcp/v1/tools/ route. Inspects the trailing
// path segment to pick which tool to invoke. Keeps routes.go in main.go
// to one line.
func Dispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/mcp/v1/tools/")
	if !allowedForCaller(r.Context(), name) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("tool %q not permitted for this key", name))
		return
	}
	switch name {
	case "lookup_od":
		handleLookupOd(w, r)
	case "execute_smartquery":
		handleExecuteSmartquery(w, r)
	case "recall_tokens":
		handleRecallTokens(w, r)
	default:
		writeErr(w, http.StatusNotFound, fmt.Sprintf("unknown tool %q (available: lookup_od, execute_smartquery, recall_tokens)", name))
	}
}

// allowedForCaller checks the per-key allowed_tools list (populated by
// auth middleware). A nil/missing value means admin (all tools
// permitted). An empty slice means explicit lockdown (no tools).
func allowedForCaller(ctx context.Context, name string) bool {
	v := ctx.Value(auth.AllowedToolsKey)
	if v == nil {
		return true // admin key (allowed_tools IS NULL)
	}
	list, ok := v.([]string)
	if !ok {
		return false
	}
	for _, t := range list {
		if t == name {
			return true
		}
	}
	return false
}
