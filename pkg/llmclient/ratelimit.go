package llmclient

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// Per-baseURL rate limiting for outbound LLM calls. The lakehouse test runner
// fans out 10+ concurrent requests against the same provider — without a
// throttle the OpenAI / Anthropic gateways respond with 429s and the worker
// pool burns retries.
//
// One *rate.Limiter per baseURL keeps providers isolated (a slow MiniMax
// shouldn't throttle Claude). Defaults to 60 RPM (1 token/sec, burst 5);
// override via SetLLMRateLimit at startup.
//
// Lookup is the hot-path of every LLM call so it must be allocation-free in
// the steady state — sync.Map.Load returns the cached limiter without a lock
// once the key is populated.

const (
	// defaultRPM is the steady-state cap; covers most paid-tier defaults and
	// is conservative enough to never trigger 429 on free tiers either.
	defaultRPM   = 60
	defaultBurst = 5
)

var llmLimiters sync.Map // map[string]*rate.Limiter, key = baseURL ("" allowed)

// limiterFor returns the limiter for baseURL, lazily constructing one with
// default settings on first call.
func limiterFor(baseURL string) *rate.Limiter {
	if v, ok := llmLimiters.Load(baseURL); ok {
		return v.(*rate.Limiter)
	}
	// rate.Limit is tokens/second.
	lim := rate.NewLimiter(rate.Limit(float64(defaultRPM)/60.0), defaultBurst)
	actual, _ := llmLimiters.LoadOrStore(baseURL, lim)
	return actual.(*rate.Limiter)
}

// waitLLMRate blocks until the limiter for baseURL grants a token. Called
// from the public DoChat* entry points before each network request. Uses
// context.Background() so callers never see context-cancel errors from the
// limiter — backpressure is handled by waiting, not by failing.
func waitLLMRate(baseURL string) {
	_ = limiterFor(baseURL).Wait(context.Background())
}

// SetLLMRateLimit overrides the limiter for a given baseURL. Call once at
// startup (e.g. from main.go) when a provider needs a different RPM. Setting
// rpm <= 0 disables limiting for that provider by replacing the limiter with
// rate.Inf (which still issues tokens but never blocks).
func SetLLMRateLimit(baseURL string, rpm int) {
	var lim *rate.Limiter
	if rpm <= 0 {
		lim = rate.NewLimiter(rate.Inf, 1)
	} else {
		burst := defaultBurst
		// Scale burst with rate so very-fast providers don't immediately
		// stall on a single-burst window. Cap at 20 to avoid pathological
		// 1000 RPM configs that defeat the point of rate-limiting.
		if rpm/60 > burst {
			burst = rpm / 60
			if burst > 20 {
				burst = 20
			}
		}
		lim = rate.NewLimiter(rate.Limit(float64(rpm)/60.0), burst)
	}
	llmLimiters.Store(baseURL, lim)
}
