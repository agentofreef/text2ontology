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

func errResult(msg string) LakehouseResult {
	return LakehouseResult{ExecutionOK: false, ErrorMessage: msg}
}
