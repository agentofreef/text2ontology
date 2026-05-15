// Package tools also hosts the MCP streamable-HTTP transport. Spec
// reference: modelcontextprotocol.io ("Streamable HTTP" 2025-03-26).
//
// A single endpoint — POST /mcp — accepts JSON-RPC 2.0 requests and
// returns a JSON response (or an SSE stream when the client sets
// Accept: text/event-stream; we use plain JSON for v0 since none of
// our tools stream mid-call).
//
// Methods implemented:
//
//	initialize   — capability handshake; returns server info + version
//	initialized  — client-sent notification after init (no-op)
//	tools/list   — catalogue of our 3 tools with JSON Schema inputSchema
//	tools/call   — dispatch to the existing REST handlers in-process
//	ping         — MCP health probe
//
// Other MCP methods (resources/, prompts/, completion/, logging/) are
// not advertised in the initialize response so spec-compliant clients
// won't call them. Unknown methods return -32601 method-not-found
// per the JSON-RPC 2.0 spec.
//
// Auth is the same bearer-token check the REST surface uses — the
// middleware in main.go covers both /api/mcp/v1/tools/ and /mcp.
// Permissions (allowed_tools column, MCP-3) also apply to tools/call.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/lakehouse2ontology/services/mcp-tools-server/client"
)

// ── JSON-RPC 2.0 envelope types ────────────────────────────────────────────

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // null for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC 2.0 error codes.
const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternalError  = -32603
)

// ── MCP method payloads ────────────────────────────────────────────────────

type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      json.RawMessage `json:"clientInfo,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    map[string]any    `json:"capabilities"`
	ServerInfo      map[string]string `json:"serverInfo"`
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []toolDefinition `json:"tools"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolsCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"` // "text" for v0 — tools return JSON as a text blob
	Text string `json:"text"`
}

// ── Public MCP handler ─────────────────────────────────────────────────────

// MCPHandler returns an http.HandlerFunc mounted at POST /mcp. It
// dispatches JSON-RPC 2.0 requests to the appropriate MCP method. Tool
// permission checks (permBypass override via ctx) still apply — the
// auth middleware has already populated client.CallerKey with the
// caller's label.
func MCPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, jsonrpcError{
				Code: errInvalidRequest, Message: "POST only",
			})
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			rpcErr(w, nil, errParseError, fmt.Sprintf("read body: %v", err))
			return
		}
		var req jsonrpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			rpcErr(w, nil, errParseError, fmt.Sprintf("invalid JSON: %v", err))
			return
		}
		if req.JSONRPC != "2.0" {
			rpcErr(w, req.ID, errInvalidRequest, `"jsonrpc":"2.0" is required`)
			return
		}
		switch req.Method {
		case "initialize":
			handleInitialize(w, &req)
		case "initialized", "notifications/initialized":
			// JSON-RPC notification: no response (no id).
			w.WriteHeader(http.StatusAccepted)
		case "ping":
			rpcOk(w, req.ID, map[string]any{})
		case "tools/list":
			handleToolsList(w, &req)
		case "tools/call":
			handleToolsCall(w, r.WithContext(r.Context()), &req)
		default:
			rpcErr(w, req.ID, errMethodNotFound,
				fmt.Sprintf("unknown MCP method %q", req.Method))
		}
	}
}

// ── Method handlers ────────────────────────────────────────────────────────

func handleInitialize(w http.ResponseWriter, req *jsonrpcRequest) {
	// We accept whatever protocolVersion the client sends; Claude Code
	// sends "2025-03-26" currently. Echo it back so the session uses the
	// intersect of our supported versions.
	var p initializeParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "2025-03-26"
	}
	rpcOk(w, req.ID, initializeResult{
		ProtocolVersion: p.ProtocolVersion,
		Capabilities: map[string]any{
			"tools": map[string]any{},
		},
		ServerInfo: map[string]string{
			"name":    "lakehouse2ontology-mcp",
			"version": "0.1.0",
		},
	})
}

func handleToolsList(w http.ResponseWriter, req *jsonrpcRequest) {
	rpcOk(w, req.ID, toolsListResult{
		Tools: ToolDefinitions(),
	})
}

// handleToolsCall dispatches to the REST-side tool handlers by
// synthesising an http.Request carrying the tool arguments as body.
// The inner handlers still emit JSON — we wrap their output in an MCP
// toolContent so the protocol envelope stays clean.
func handleToolsCall(w http.ResponseWriter, r *http.Request, req *jsonrpcRequest) {
	var p toolsCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		rpcErr(w, req.ID, errInvalidParams, fmt.Sprintf("invalid params: %v", err))
		return
	}
	if p.Name == "" {
		rpcErr(w, req.ID, errInvalidParams, "name required")
		return
	}
	if !AuthorizedTool(r.Context(), p.Name) {
		rpcErr(w, req.ID, errInvalidRequest,
			fmt.Sprintf("tool %q not permitted for this key", p.Name))
		return
	}

	inner, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"/api/mcp/v1/tools/"+p.Name, bytes.NewReader(p.Arguments))
	if err != nil {
		rpcErr(w, req.ID, errInternalError, err.Error())
		return
	}
	inner.Header.Set("Content-Type", "application/json")
	// Re-use the REST dispatcher so there is one source of truth for
	// each tool's business logic.
	rec := newBufferedWriter()
	DispatchWithContext(rec, inner)

	// Tool handlers return JSON bodies; wrap them as MCP toolContent
	// "text" blocks. 4xx / 5xx responses surface as isError=true.
	res := toolsCallResult{
		Content: []toolContent{{Type: "text", Text: rec.buf.String()}},
		IsError: rec.status >= 400,
	}
	rpcOk(w, req.ID, res)
}

// ToolDefinitions returns the catalogue the MCP client sees on
// tools/list. InputSchema is plain JSON Schema so Claude Code et al.
// can validate tool call arguments client-side.
func ToolDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			Name:        "lookup_od",
			Description: "Resolve an Od (object definition) by name within a project+version and return its schema (properties).",
			InputSchema: map[string]any{
				"type": "object",
				"required": []string{"projectId", "name"},
				"properties": map[string]any{
					"projectId": map[string]any{"type": "string", "description": "Project UUID."},
					"name":      map[string]any{"type": "string", "description": "Od name to resolve (exact or partial)."},
				},
			},
		},
		{
			Name:        "execute_smartquery",
			Description: "Execute a canonical QuerySpec against the lakehouse and return rows + metadata.",
			InputSchema: map[string]any{
				"type": "object",
				"required": []string{"projectId", "spec"},
				"properties": map[string]any{
					"projectId": map[string]any{"type": "string"},
					"spec":      map[string]any{"type": "object", "description": "Canonical QuerySpec (see docs)."},
				},
			},
		},
		{
			Name:        "recall_tokens",
			Description: "Ontology-aware recall: given tokens or a natural-language question, return matched Ods, Oks, Ols, and MetricIntents with composed context markdown.",
			InputSchema: map[string]any{
				"type": "object",
				"required": []string{"projectId"},
				"properties": map[string]any{
					"projectId": map[string]any{"type": "string"},
					"tokens":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"question":  map[string]any{"type": "string"},
				},
			},
		},
	}
}

// AuthorizedTool is the MCP-side permission hook. Shares the
// allowed_tools rule with the REST dispatcher — nil/missing list on
// the context means admin (all tools); non-nil list is a whitelist.
func AuthorizedTool(ctx context.Context, name string) bool {
	return allowedForCaller(ctx, name)
}

// DispatchWithContext is the internal entry point the MCP handler uses
// to reach the REST tool code without going through the HTTP stack.
// Exported so MCPHandler can call it without an import cycle.
func DispatchWithContext(w http.ResponseWriter, r *http.Request) { Dispatch(w, r) }

// bufferedWriter captures an inner handler's status + body so we can
// repack the response as an MCP toolContent.
type bufferedWriter struct {
	buf    bytes.Buffer
	status int
	hdr    http.Header
}

func newBufferedWriter() *bufferedWriter {
	return &bufferedWriter{status: http.StatusOK, hdr: http.Header{}}
}
func (b *bufferedWriter) Header() http.Header       { return b.hdr }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferedWriter) WriteHeader(s int)         { b.status = s }

// ── Response helpers ───────────────────────────────────────────────────────

func rpcOk(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0", ID: id, Result: result,
	})
}

func rpcErr(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // JSON-RPC errors are HTTP 200 by spec
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0", ID: id,
		Error: &jsonrpcError{Code: code, Message: msg},
	})
}

// Unused import shim — keep client package reachable so lint doesn't
// drop the dependency when tools/mcp.go is the only consumer in future
// edits.
var _ = client.CallerKey
