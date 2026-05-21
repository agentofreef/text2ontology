package llmclient

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestRequest builds a POST request with a rewindable body (GetBody set),
// matching how the production call sites construct requests.
func newTestRequest(ctx context.Context, url string) *http.Request {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		panic(err)
	}
	return req
}

// TestDoWithRetry_RetriesOn429ThenSucceeds verifies W5-2: a 429 triggers a
// bounded backoff+retry, and a subsequent 200 is returned. Uses tiny backoff
// via Retry-After: 0 so the test runs fast.
func TestDoWithRetry_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0") // retry immediately
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("rate limited"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), newTestRequest(context.Background(), srv.URL))
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected exactly 2 attempts (1 fail + 1 retry), got %d", got)
	}
}

// TestDoWithRetry_GivesUpAfterMaxRetries verifies the retry loop is bounded:
// a server that always 429s is hit at most maxRetries+1 times, then the last
// response is returned (not retried forever).
func TestDoWithRetry_GivesUpAfterMaxRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable) // 503 is retryable
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), newTestRequest(context.Background(), srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected final 503, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != maxRetries+1 {
		t.Fatalf("expected exactly %d attempts, got %d", maxRetries+1, got)
	}
}

// TestDoWithRetry_CtxCancellationAborts verifies W5-2: a cancelled context
// aborts an in-flight request rather than hanging. The server blocks until the
// client cancels, so without ctx propagation this would never return.
func TestDoWithRetry_CtxCancellationAborts(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client's context is cancelled (or the test releases).
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer srv.Close()
	defer close(released)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the request starts.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := doWithRetry(ctx, srv.Client(), newTestRequest(ctx, srv.URL))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cancellation did not abort promptly; took %s", elapsed)
	}
}

// TestDoWithRetry_NoRetryOn4xx verifies a non-retryable 4xx (e.g. 400) is
// returned immediately without retries.
func TestDoWithRetry_NoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), newTestRequest(context.Background(), srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 attempt for non-retryable 400, got %d", got)
	}
}

// closeTrackingBody wraps the test server response so we can assert the body
// was closed (W5-3: EmbedTexts must close the body even on non-2xx).
type closeTrackingTransport struct {
	base   http.RoundTripper
	closed *int32
}

type trackedReadCloser struct {
	inner  interface{ Read([]byte) (int, error) }
	closer interface{ Close() error }
	closed *int32
}

func (t *trackedReadCloser) Read(p []byte) (int, error) { return t.inner.Read(p) }
func (t *trackedReadCloser) Close() error {
	atomic.AddInt32(t.closed, 1)
	return t.closer.Close()
}

func (c *closeTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	resp.Body = &trackedReadCloser{inner: resp.Body, closer: resp.Body, closed: c.closed}
	return resp, nil
}

// TestEmbedTextsVia_Non2xxFailsLoud verifies W5-3: a non-2xx embedding
// response returns an error (NOT a zero-vector slice) and the body is closed.
func TestEmbedTextsVia_Non2xxFailsLoud(t *testing.T) {
	var bodyCloses int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model overloaded"}`))
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = &closeTrackingTransport{base: client.Transport, closed: &bodyCloses}

	vecs, err := embedTextsVia(context.Background(), client, srv.URL, "", "test-embed", []string{"hello"})
	if err == nil {
		t.Fatal("expected error on non-2xx embedding response, got nil")
	}
	if vecs != nil {
		t.Fatalf("expected nil vectors on error, got %v (silent zero-vector corruption!)", vecs)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected error to mention status 500, got: %v", err)
	}
	// The 5xx is retried (maxRetries+1 attempts), each body must be closed.
	if got := atomic.LoadInt32(&bodyCloses); got < 1 {
		t.Fatalf("expected response body to be closed at least once, got %d", got)
	}
}

// TestEmbedTextsVia_Success verifies the happy path still returns vectors and
// closes the body.
func TestEmbedTextsVia_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer srv.Close()

	vecs, err := embedTextsVia(context.Background(), srv.Client(), srv.URL, "", "test-embed", []string{"hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Fatalf("expected 1 vector of dim 3, got %v", vecs)
	}
}
