package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captured records what the fake upstream observed about an inbound request, so
// the test can assert on the cross-service contract (headers + path + method).
type captured struct {
	method      string
	path        string
	query       string
	internalTok string
	onBehalfOf  string
	callerSvc   string
	body        []byte
}

// fakeUpstream stands up an httptest server that records the request and
// replies with the provided JSON body + status. Returns the server URL and a
// pointer to the captured request for assertions.
func fakeUpstream(t *testing.T, status int, respJSON string) (string, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.internalTok = r.Header.Get("X-Internal-Token")
		cap.onBehalfOf = r.Header.Get("X-On-Behalf-Of")
		cap.callerSvc = r.Header.Get("X-Caller-Service")
		if r.Body != nil {
			cap.body, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respJSON))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, cap
}

// TestListObjects_CrossServiceContract is an integration-style test: the
// backend-api client posts to a fake upstream and we assert (a) the documented
// internal-auth headers are attached, (b) the projectId + name query is wired,
// and (c) the list envelope is parsed into typed Objects.
func TestListObjects_CrossServiceContract(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "test-internal-token")
	url, cap := fakeUpstream(t, http.StatusOK,
		`{"data":[{"id":"o1","name":"Customer","displayName":"Customer","kind":"entity"}],"total":1}`)
	t.Setenv("BACKEND_API_URL", url)

	ctx := context.WithValue(context.Background(), CallerKey, "mcp:tester")
	objs, err := ListObjects(ctx, "proj-1", "Customer")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}

	// Response parsing.
	if len(objs) != 1 || objs[0].ID != "o1" || objs[0].Name != "Customer" {
		t.Fatalf("parsed objects = %+v, want one Customer", objs)
	}
	// Contract: method, path, internal-auth headers, caller propagation.
	if cap.method != http.MethodGet {
		t.Fatalf("method = %q, want GET", cap.method)
	}
	if cap.path != "/internal/backend-api/objects" {
		t.Fatalf("path = %q, want /internal/backend-api/objects", cap.path)
	}
	if cap.internalTok != "test-internal-token" {
		t.Fatalf("X-Internal-Token = %q, want test-internal-token", cap.internalTok)
	}
	if cap.callerSvc != "mcp-tools-server" {
		t.Fatalf("X-Caller-Service = %q, want mcp-tools-server", cap.callerSvc)
	}
	if cap.onBehalfOf != "mcp:tester" {
		t.Fatalf("X-On-Behalf-Of = %q, want mcp:tester (from ctx)", cap.onBehalfOf)
	}
	if !contains(cap.query, "projectId=proj-1") || !contains(cap.query, "name=Customer") {
		t.Fatalf("query = %q, want projectId+name", cap.query)
	}
}

// TestListObjects_AnonymousCallerDefault: when no caller is on the context the
// client must still attribute the request as "mcp-external" for audit logs.
func TestListObjects_AnonymousCallerDefault(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "tok")
	url, cap := fakeUpstream(t, http.StatusOK, `{"data":[],"total":0}`)
	t.Setenv("BACKEND_API_URL", url)

	if _, err := ListObjects(context.Background(), "p", ""); err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if cap.onBehalfOf != "mcp-external" {
		t.Fatalf("X-On-Behalf-Of = %q, want mcp-external default", cap.onBehalfOf)
	}
}

// TestRecallBuildContext_PostsEnvelope: the recall client POSTs the
// {projectId,tokens,question} envelope and forwards the opaque JSON result.
func TestRecallBuildContext_PostsEnvelope(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "tok")
	url, cap := fakeUpstream(t, http.StatusOK, `{"odBlocks":[]}`)
	t.Setenv("RECALL_SERVER_URL", url)

	out, err := RecallBuildContext(context.Background(), RecallBuildContextRequest{
		ProjectID: "p1", Tokens: []string{"PCV", "品牌"}, Question: "q",
	})
	if err != nil {
		t.Fatalf("RecallBuildContext: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/internal/recall/build-context" {
		t.Fatalf("got %s %s, want POST /internal/recall/build-context", cap.method, cap.path)
	}
	var sent RecallBuildContextRequest
	if err := json.Unmarshal(cap.body, &sent); err != nil {
		t.Fatalf("upstream did not receive a valid envelope: %v (raw=%s)", err, cap.body)
	}
	if sent.ProjectID != "p1" || len(sent.Tokens) != 2 {
		t.Fatalf("envelope = %+v, want projectId=p1 + 2 tokens", sent)
	}
	if string(out) != `{"odBlocks":[]}` {
		t.Fatalf("forwarded result = %s, want verbatim upstream JSON", out)
	}
}

// TestUpstreamErrorPropagates: a non-2xx upstream surfaces as an error (with the
// upstream status), not a silent empty result.
func TestUpstreamErrorPropagates(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "tok")
	url, _ := fakeUpstream(t, http.StatusInternalServerError, `{"error":"boom"}`)
	t.Setenv("BACKEND_API_URL", url)

	if _, err := ListObjects(context.Background(), "p", ""); err == nil {
		t.Fatal("expected an error when upstream returns 500")
	}
}

// TestMissingUpstreamURL: an unset target env is a configuration error, not a
// nil-deref or a request to an empty host.
func TestMissingUpstreamURL(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "tok")
	t.Setenv("BACKEND_API_URL", "")
	if _, err := ListObjects(context.Background(), "p", ""); err == nil {
		t.Fatal("expected an error when BACKEND_API_URL is unset")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return len(needle) == 0
}
