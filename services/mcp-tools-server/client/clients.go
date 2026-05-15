// Package client holds the three upstream HTTP clients mcp-tools-server
// needs to service tool calls. All three share the same auth + trace
// injection shape: X-Internal-Token from INTERNAL_TOKEN env, X-Caller-Service
// identifies mcp-tools-server in audit logs, and traceparent is injected
// via the current span context so Jaeger nests spans correctly.
//
// Upstream contracts (internal-only):
//
//	recall-server         /internal/recall/build-context   (POST JSON)
//	lakehouse-sql-server  /internal/smartquery/execute     (POST JSON)
//	backend-api           /internal/backend-api/objects    (GET)
//	backend-api           /internal/backend-api/properties (GET)
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/lakehouse2ontology/observability"
)

// ── Shared helpers ─────────────────────────────────────────────────────────

var httpClient = &http.Client{Timeout: 30 * time.Second}

// internalToken returns INTERNAL_TOKEN env, panicking at init time if
// absent. Called once per outbound request; the overhead of os.Getenv
// is trivial and we get immediate failure on misconfiguration.
func internalToken() string {
	t := os.Getenv("INTERNAL_TOKEN")
	if t == "" {
		panic("INTERNAL_TOKEN is required for mcp-tools-server to call internal services")
	}
	return t
}

// onBehalfOf pulls the MCP caller identity from the incoming request
// context if set; defaults to the generic "mcp-external" tag so audit
// logs still attribute correctly when the MCP external caller is
// anonymous.
func onBehalfOf(ctx context.Context) string {
	if v, ok := ctx.Value(CallerKey).(string); ok && v != "" {
		return v
	}
	return "mcp-external"
}

type ctxKey string

// CallerKey stores the external caller identity on the request context
// so the upstream HTTP layer can pass it through to internal services.
// For v0 this is always "mcp-external"; future work wires per-key
// identity from API-key → caller mapping.
const CallerKey ctxKey = "mcp.caller"

func postJSON(ctx context.Context, target, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	u, err := url.JoinPath(target, path)
	if err != nil {
		return fmt.Errorf("join URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", internalToken())
	req.Header.Set("X-On-Behalf-Of", onBehalfOf(ctx))
	req.Header.Set("X-Caller-Service", "mcp-tools-server")
	observability.InjectTraceContext(ctx, req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upstream %s → %d: %s", u, resp.StatusCode, string(b))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func getJSON(ctx context.Context, target, path string, query url.Values, out any) error {
	u, err := url.JoinPath(target, path)
	if err != nil {
		return fmt.Errorf("join URL: %w", err)
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Internal-Token", internalToken())
	req.Header.Set("X-On-Behalf-Of", onBehalfOf(ctx))
	req.Header.Set("X-Caller-Service", "mcp-tools-server")
	observability.InjectTraceContext(ctx, req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upstream %s → %d: %s", u, resp.StatusCode, string(b))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// ── Recall client ──────────────────────────────────────────────────────────

// RecallBuildContextRequest mirrors recall-server's wire envelope.
type RecallBuildContextRequest struct {
	ProjectID string   `json:"projectId"`
	Tokens    []string `json:"tokens"`
	Question  string   `json:"question"`
}

// RecallResult is opaque at this layer — mcp-tools-server returns it
// verbatim to the external caller without interpreting the shape.
type RecallResult = json.RawMessage

// RecallBuildContext calls recall-server's /internal/recall/build-context.
// Returns the JSON blob so the MCP client can render it directly.
func RecallBuildContext(ctx context.Context, req RecallBuildContextRequest) (RecallResult, error) {
	target := os.Getenv("RECALL_SERVER_URL")
	if target == "" {
		return nil, fmt.Errorf("RECALL_SERVER_URL is required")
	}
	var out json.RawMessage
	if err := postJSON(ctx, target, "/internal/recall/build-context", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── Lakehouse-SQL client ───────────────────────────────────────────────────

// SmartQueryExecuteRequest wires through to lakehouse-sql-server's
// /internal/smartquery/execute endpoint. Spec is an opaque QuerySpec
// (see services/lakehouse-sql-server/smartquery/types.go) — we pass it through
// without parsing to avoid tight schema coupling.
type SmartQueryExecuteRequest struct {
	ProjectID string          `json:"projectId"`
	Spec      json.RawMessage `json:"spec"`
}

// SmartQueryResult is opaque; forwarded verbatim to the MCP caller.
type SmartQueryResult = json.RawMessage

// SmartQueryExecute calls lakehouse-sql-server's execute endpoint.
func SmartQueryExecute(ctx context.Context, req SmartQueryExecuteRequest) (SmartQueryResult, error) {
	target := os.Getenv("LAKEHOUSE_SQL_URL")
	if target == "" {
		return nil, fmt.Errorf("LAKEHOUSE_SQL_URL is required")
	}
	var out json.RawMessage
	if err := postJSON(ctx, target, "/internal/smartquery/execute", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── Backend-API client ─────────────────────────────────────────────────────

// Object summarizes a single ont_object_type row as returned by
// backend-api's /internal/backend-api/objects list. We only extract the
// fields lookup_od needs; leftovers stay in the underlying JSON.
type Object struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	SourceTable string `json:"sourceTable"`
}

// Property mirrors a single ont_property row.
type Property struct {
	ID           string `json:"id"`
	ObjectTypeID string `json:"objectTypeId"`
	Name         string `json:"name"`
	DisplayName  string `json:"displayName"`
	DataType     string `json:"dataType"`
	SourceColumn string `json:"sourceColumn"`
	IsFilterable bool   `json:"isFilterable"`
	IsGroupable  bool   `json:"isGroupable"`
	Description  string `json:"description"`
}

type listEnvelope[T any] struct {
	Data  []T `json:"data"`
	Total int `json:"total"`
}

// ListObjects calls backend-api's /internal/backend-api/objects list
// endpoint. name is optional; empty name returns all objects in the
// project+version.
func ListObjects(ctx context.Context, projectID, name string) ([]Object, error) {
	target := os.Getenv("BACKEND_API_URL")
	if target == "" {
		return nil, fmt.Errorf("BACKEND_API_URL is required")
	}
	q := url.Values{"projectId": {projectID}}
	if name != "" {
		q.Set("name", name)
	}
	var out listEnvelope[Object]
	if err := getJSON(ctx, target, "/internal/backend-api/objects", q, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ListProperties calls backend-api's /internal/backend-api/properties
// list endpoint for a given objectTypeId.
func ListProperties(ctx context.Context, projectID, objectTypeID string) ([]Property, error) {
	target := os.Getenv("BACKEND_API_URL")
	if target == "" {
		return nil, fmt.Errorf("BACKEND_API_URL is required")
	}
	q := url.Values{"projectId": {projectID}, "objectTypeId": {objectTypeID}}
	var out listEnvelope[Property]
	if err := getJSON(ctx, target, "/internal/backend-api/properties", q, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
