package recall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/lakehouse2ontology/observability"
)

// RemoteClient speaks to recall-server over HTTP instead of running the
// recall pipeline in-process. Phase 1 R3: the two public entry points
// (BuildLakehouseContext and BuildLakehouseContextCached) short-circuit
// through this client when RECALL_SERVER_URL is set, keeping the
// in-process path as a dev/test fallback until R4c removes it.
type RemoteClient struct {
	BaseURL    string
	Token      string
	OnBehalfOf string
	HTTP       *http.Client
}

type buildContextRequest struct {
	ProjectID string         `json:"projectId"`
	Tokens    []string       `json:"tokens"`
	Question  string         `json:"question"`
	Cached    *CachedContext `json:"cached,omitempty"`
}

// BuildContext POSTs a build-context request to recall-server. Matches
// the service's JSON wire shape exactly (freeze clause). Emits a
// cross_service_http span so the hop is visible in Jaeger even before
// full W3C traceparent propagation lands.
func (c *RemoteClient) BuildContext(ctx context.Context, projectID string, tokens []string, question string, cached *CachedContext) RecallResult {
	start := time.Now()
	ctx, span := observability.Tracer().Start(ctx, "cross_service_http",
		trace.WithAttributes(
			attribute.String("peer.service", "recall-server"),
			attribute.String("http.method", http.MethodPost),
			attribute.String("http.route", "/internal/recall/build-context"),
			attribute.String("project_id", projectID),
			attribute.Int("token_count", len(tokens)),
			attribute.Bool("cached", cached != nil),
		))
	defer span.End()
	defer func() {
		observability.CrossSvcHTTPDuration.
			WithLabelValues("monolith", "recall-server", "/internal/recall/build-context").
			Observe(float64(time.Since(start).Milliseconds()))
	}()

	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}

	body, err := json.Marshal(buildContextRequest{
		ProjectID: projectID,
		Tokens:    tokens, Question: question, Cached: cached,
	})
	if err != nil {
		return errResult(fmt.Sprintf("recall remote marshal: %v", err))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/recall/build-context", bytes.NewReader(body))
	if err != nil {
		return errResult(fmt.Sprintf("recall remote new request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.Token)
	req.Header.Set("X-On-Behalf-Of", c.OnBehalfOf)
	req.Header.Set("X-Caller-Service", "monolith")
	observability.InjectTraceContext(ctx, req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errResult(fmt.Sprintf("recall remote do: %v", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errResult(fmt.Sprintf("recall remote status %d: %s", resp.StatusCode, string(peek)))
	}

	var result RecallResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errResult(fmt.Sprintf("recall remote decode: %v", err))
	}
	return result
}

func errResult(msg string) RecallResult {
	return RecallResult{
		ContextMD: fmt.Sprintf("recall error: %s", msg),
	}
}

// recallRemote returns a lazily-constructed *RemoteClient when
// RECALL_SERVER_URL is set, nil otherwise. First-call log indicates the
// active path so dev/ops see which mode is wired.
var (
	recallRemoteOnce sync.Once
	recallRemoteInst *RemoteClient
)

func recallRemote() *RemoteClient {
	recallRemoteOnce.Do(func() {
		url := os.Getenv("RECALL_SERVER_URL")
		if url == "" {
			log.Printf("   RecallExec: in-process (RECALL_SERVER_URL unset)")
			return
		}
		token := os.Getenv("INTERNAL_TOKEN")
		if token == "" {
			log.Printf("   RecallExec: in-process (RECALL_SERVER_URL set but INTERNAL_TOKEN empty)")
			return
		}
		log.Printf("   RecallExec: remote → %s", url)
		recallRemoteInst = &RemoteClient{
			BaseURL:    url,
			Token:      token,
			OnBehalfOf: "monolith-internal",
			HTTP:       &http.Client{Timeout: 60 * time.Second},
		}
	})
	return recallRemoteInst
}
