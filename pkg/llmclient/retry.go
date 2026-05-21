package llmclient

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// maxRetries bounds the number of retry attempts on transient failures
// (HTTP 429 and 5xx). The initial attempt is not counted, so the request is
// made at most maxRetries+1 times.
const maxRetries = 3

// baseBackoff is the first retry delay; subsequent retries double it
// (exponential backoff) plus a small random jitter to avoid thundering-herd
// retries when a worker pool fans out concurrent calls against one provider.
const baseBackoff = 500 * time.Millisecond

// maxBackoff caps the per-retry sleep so a Retry-After or exponential growth
// never blocks a worker for an unreasonable time.
const maxBackoff = 8 * time.Second

// shouldRetryStatus reports whether an HTTP status code is worth retrying.
// 429 (rate limited) and any 5xx (transient server error) are retryable;
// everything else (including 4xx other than 429) is a permanent outcome.
func shouldRetryStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code <= 599)
}

// retryDelay computes how long to wait before the next attempt. It honors the
// server's Retry-After header when present (seconds form), otherwise falls back
// to exponential backoff with jitter. attempt is 0-based (0 = first retry).
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
				d := time.Duration(secs) * time.Second
				if d > maxBackoff {
					d = maxBackoff
				}
				return d
			}
		}
	}
	// Exponential: base * 2^attempt, capped, plus up to 50% jitter.
	d := baseBackoff << uint(attempt)
	if d > maxBackoff {
		d = maxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(d/2) + 1))
	return d + jitter
}

// doWithRetry executes req via client, retrying on 429/5xx and transport
// errors with exponential backoff (honoring Retry-After). It respects
// ctx cancellation: a cancelled ctx aborts immediately and returns ctx.Err().
//
// Stdlib only — no external retry library. The request body for these LLM
// calls is a bytes.Reader created fresh per call site, so re-issuing the same
// *http.Request is safe (Body is rewindable via GetBody when set by
// NewRequestWithContext on a bytes.Reader).
func doWithRetry(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Re-seat the body for retries. NewRequestWithContext populates GetBody
		// for bytes.Reader/bytes.Buffer/strings.Reader bodies, so we can rewind.
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err == nil {
				req.Body = body
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			// Transport error (includes ctx cancellation surfaced by Do).
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			if attempt == maxRetries {
				return nil, err
			}
			if !sleepCtx(ctx, retryDelay(nil, attempt)) {
				return nil, ctx.Err()
			}
			continue
		}

		if attempt < maxRetries && shouldRetryStatus(resp.StatusCode) {
			delay := retryDelay(resp, attempt)
			// Drain+close so the connection can be reused for the retry.
			drainAndClose(resp)
			if !sleepCtx(ctx, delay) {
				return nil, ctx.Err()
			}
			continue
		}

		return resp, nil
	}
	return nil, lastErr
}

// drainAndClose discards the remaining body (bounded) and closes it so the
// underlying keep-alive connection can be reused on the retry.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns true if the full
// duration elapsed, false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
