package lakehouse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/agent-server/smartquery"
)

// RemoteClient speaks to lakehouse-sql-server over HTTP instead of running
// the Engine in-process. Drop-in replacement for `(*Engine).Execute`: same
// signature, same LakehouseResult semantics. Used by the monolith during
// the Phase 1 rollout window; the in-process Engine is kept behind an env
// flag so a broken service-side deploy can be rolled back by unsetting
// LAKEHOUSE_SQL_URL without a monolith restart.
type RemoteClient struct {
	BaseURL    string       // e.g. http://127.0.0.1:18094
	Token      string       // matches lakehouse-sql-server INTERNAL_TOKEN
	OnBehalfOf string       // audited by pkg/authmw; at minimum non-empty
	HTTP       *http.Client // required; supply own timeout
}

// Execute POSTs `{spec: ...}` to /internal/smartquery/execute and decodes
// the JSON LakehouseResult. Wire-compatible with the monolith's in-tree
// lakehouse.LakehouseResult thanks to the freeze clause in
// pkg/contracts/CONTRACTS.md.
//
// Emits a `cross_service_http` span so monolith→service calls remain
// visible in the agent.turn trace even before the service has its own
// OTel SDK wired (Phase 1 D2.5).
func (c *RemoteClient) Execute(ctx context.Context, spec smartquery.QuerySpec) LakehouseResult {
	start := time.Now()
	ctx, span := observability.Tracer().Start(ctx, "cross_service_http",
		trace.WithAttributes(
			attribute.String("peer.service", "lakehouse-sql-server"),
			attribute.String("http.method", http.MethodPost),
			attribute.String("http.route", "/internal/smartquery/execute"),
			attribute.String("project_id", spec.ProjectID),
		))
	defer span.End()
	defer func() {
		observability.CrossSvcHTTPDuration.
			WithLabelValues("monolith", "lakehouse-sql-server", "/internal/smartquery/execute").
			Observe(float64(time.Since(start).Milliseconds()))
	}()

	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := json.Marshal(struct {
		Spec smartquery.QuerySpec `json:"spec"`
	}{Spec: spec})
	if err != nil {
		return errResult(fmt.Sprintf("remote client marshal: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/smartquery/execute", bytes.NewReader(body))
	if err != nil {
		return errResult(fmt.Sprintf("remote client new request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.Token)
	req.Header.Set("X-On-Behalf-Of", c.OnBehalfOf)
	req.Header.Set("X-Caller-Service", "monolith")
	observability.InjectTraceContext(ctx, req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errResult(fmt.Sprintf("remote client do: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errResult(fmt.Sprintf("remote status %d: %s", resp.StatusCode, string(peek)))
	}

	var result LakehouseResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errResult(fmt.Sprintf("remote client decode: %v", err))
	}
	return result
}

// ExecutePlan POSTs `{plan, params, projectId}` to /internal/smartquery/execute-plan
// and decodes the JSON LakehouseResult. Used when the matched Metric Intent has
// a non-null `plan` column (composite Intent) — see
// .omc/specs/plan-mode-composite-intent.md §3.5.
func (c *RemoteClient) ExecutePlan(ctx context.Context, planJSON []byte, params map[string]string, projectID string) LakehouseResult {
	start := time.Now()
	ctx, span := observability.Tracer().Start(ctx, "cross_service_http",
		trace.WithAttributes(
			attribute.String("peer.service", "lakehouse-sql-server"),
			attribute.String("http.method", http.MethodPost),
			attribute.String("http.route", "/internal/smartquery/execute-plan"),
			attribute.String("project_id", projectID),
		))
	defer span.End()
	defer func() {
		observability.CrossSvcHTTPDuration.
			WithLabelValues("monolith", "lakehouse-sql-server", "/internal/smartquery/execute-plan").
			Observe(float64(time.Since(start).Milliseconds()))
	}()

	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := json.Marshal(struct {
		Plan      json.RawMessage   `json:"plan"`
		Params    map[string]string `json:"params"`
		ProjectID string            `json:"projectId"`
	}{Plan: planJSON, Params: params, ProjectID: projectID})
	if err != nil {
		return errResult(fmt.Sprintf("remote client marshal: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/smartquery/execute-plan", bytes.NewReader(body))
	if err != nil {
		return errResult(fmt.Sprintf("remote client new request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.Token)
	req.Header.Set("X-On-Behalf-Of", c.OnBehalfOf)
	req.Header.Set("X-Caller-Service", "monolith")
	observability.InjectTraceContext(ctx, req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errResult(fmt.Sprintf("remote client do: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errResult(fmt.Sprintf("remote status %d: %s", resp.StatusCode, string(peek)))
	}

	var result LakehouseResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errResult(fmt.Sprintf("remote client decode: %v", err))
	}
	return result
}

// executeSQLResponse mirrors lakehouse-sql-server's /internal/smartquery/
// execute-sql response body: {ok, sql, columns, rows, rowCount, error}. rows
// is the raw []map result set; we re-marshal it into LakehouseResult.ResultJSON
// so downstream consumers (the agent's data-template renderer) see the same
// JSON-array shape the structured Execute path produces.
type executeSQLResponse struct {
	OK       bool                     `json:"ok"`
	SQL      string                   `json:"sql"`
	Columns  []string                 `json:"columns"`
	Rows     []map[string]interface{} `json:"rows"`
	RowCount int                      `json:"rowCount"`
	Error    string                   `json:"error"`
}

// ExecuteSQLMetric POSTs `{projectId, sql, params}` to
// /internal/smartquery/execute-sql and maps the response into a LakehouseResult.
// sql is the human-authored, OD-name SQL declaring inline {sys.req/opt.NAME}
// parameters; params are the LLM-filled values keyed by NAME. The service-side
// handler performs the security-critical rewrite (RenderSysParams → missing-
// required check → RejectDDL → OD-CTE → MaybeInjectLimit), binding every VALUE
// via positional $N driver args (never concatenated) and escaping any provided
// dimension as a quoted identifier. Mirrors Execute / ExecutePlan.
func (c *RemoteClient) ExecuteSQLMetric(ctx context.Context, projectID, sql string, params map[string]interface{}) LakehouseResult {
	start := time.Now()
	ctx, span := observability.Tracer().Start(ctx, "cross_service_http",
		trace.WithAttributes(
			attribute.String("peer.service", "lakehouse-sql-server"),
			attribute.String("http.method", http.MethodPost),
			attribute.String("http.route", "/internal/smartquery/execute-sql"),
			attribute.String("project_id", projectID),
		))
	defer span.End()
	defer func() {
		observability.CrossSvcHTTPDuration.
			WithLabelValues("monolith", "lakehouse-sql-server", "/internal/smartquery/execute-sql").
			Observe(float64(time.Since(start).Milliseconds()))
	}()

	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := json.Marshal(struct {
		ProjectID string                 `json:"projectId"`
		SQL       string                 `json:"sql"`
		Params    map[string]interface{} `json:"params"`
	}{ProjectID: projectID, SQL: sql, Params: params})
	if err != nil {
		return errResult(fmt.Sprintf("remote client marshal: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/smartquery/execute-sql", bytes.NewReader(body))
	if err != nil {
		return errResult(fmt.Sprintf("remote client new request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.Token)
	req.Header.Set("X-On-Behalf-Of", c.OnBehalfOf)
	req.Header.Set("X-Caller-Service", "monolith")
	observability.InjectTraceContext(ctx, req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errResult(fmt.Sprintf("remote client do: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errResult(fmt.Sprintf("remote status %d: %s", resp.StatusCode, string(peek)))
	}

	var out executeSQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return errResult(fmt.Sprintf("remote client decode: %v", err))
	}

	result := LakehouseResult{
		SQL:         out.SQL,
		OntologySQL: sql, // Layer-1 view: the human-authored OD-name SQL pre-rewrite.
		ExecutionOK: out.OK,
	}
	if !out.OK {
		result.ErrorMessage = out.Error
		return result
	}
	// Re-marshal rows into the JSON-array ResultJSON shape the structured path
	// produces. A nil rows slice marshals to "[]" after the guard below.
	rows := out.Rows
	if rows == nil {
		rows = []map[string]interface{}{}
	}
	if b, mErr := json.Marshal(rows); mErr == nil {
		result.ResultJSON = string(b)
	}
	return result
}

func errResult(msg string) LakehouseResult {
	return LakehouseResult{ExecutionOK: false, ErrorMessage: msg}
}
