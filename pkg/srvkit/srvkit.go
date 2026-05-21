// Package srvkit centralizes the production-hardening boilerplate every
// service main shares: an http.Server with sane timeouts, signal-driven
// graceful shutdown with ordered post-drain hooks, a panic-recover
// middleware, and database/sql connection-pool tuning.
//
// It exists so the six service binaries (agent-server, backend-api,
// recall-server, lakehouse-sql-server, mcp-tools-server, collector-server)
// don't each hand-roll a divergent copy of the same shutdown/timeout/recover
// logic. Keep the dependency surface to the standard library only — this
// package is imported by every service and must not drag transitive deps in.
//
// Addresses the production-readiness items:
//   - P0-1 graceful shutdown (Run: SIGINT/SIGTERM-driven drain so deferred
//     db.Close()/observability flush actually run).
//   - P1-1 server timeouts (Run: ReadHeaderTimeout/ReadTimeout/IdleTimeout/
//     WriteTimeout on the http.Server).
//   - P1-2 DB pool tuning (TunePool).
//   - P1-7 recover middleware (RecoverMiddleware).
package srvkit

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

// HTTP server timeout defaults. These bound how long the server will wait
// at each phase of a request, defending against slow-loris-style clients
// and leaked connections.
//
//   - defaultReadHeaderTimeout: time allowed to read request headers.
//   - defaultReadTimeout:       total time allowed to read the full request
//     (headers + body).
//   - defaultIdleTimeout:       keep-alive idle time before the connection
//     is closed.
//   - defaultWriteTimeout:      total time allowed to write the response.
//     Streaming endpoints (SSE) must disable this via WithWriteTimeout(0)
//     because a long-lived event stream legitimately keeps writing past
//     any fixed window.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 60 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultWriteTimeout      = 60 * time.Second

	// defaultShutdownTimeout bounds how long graceful drain waits for
	// in-flight requests to complete before the server is forcibly closed.
	defaultShutdownTimeout = 25 * time.Second
)

// config holds the resolved options for a Run invocation.
type config struct {
	writeTimeout    time.Duration
	shutdownTimeout time.Duration
	onShutdown      []func(context.Context)
}

// Option customizes Run behavior.
type Option func(*config)

// WithWriteTimeout overrides the http.Server WriteTimeout. Pass 0 to disable
// the write timeout entirely — required for SSE / long-lived streaming
// handlers whose responses outlive any fixed window. Negative values are
// treated as the default.
func WithWriteTimeout(d time.Duration) Option {
	return func(c *config) {
		if d < 0 {
			return
		}
		c.writeTimeout = d
	}
}

// WithShutdownTimeout overrides the bounded drain timeout used by Shutdown.
// Non-positive values are ignored (the default is kept).
func WithShutdownTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.shutdownTimeout = d
		}
	}
}

// WithOnShutdown registers a hook that runs AFTER the HTTP server has fully
// drained, in registration order. Use it to stop background workers before
// the caller's deferred db.Close()/observability shutdown fires, so workers
// never write to a closed pool. Hooks receive a context bounded by the
// shutdown timeout.
func WithOnShutdown(fn func(context.Context)) Option {
	return func(c *config) {
		if fn != nil {
			c.onShutdown = append(c.onShutdown, fn)
		}
	}
}

// Run builds an http.Server with hardened timeouts, serves it in a
// background goroutine, and blocks until ctx is cancelled (the caller is
// expected to pass a signal.NotifyContext(SIGINT, SIGTERM) context) or the
// listener fails. On shutdown it drains in-flight requests within a bounded
// timeout, then runs any registered OnShutdown hooks in order.
//
// Run returns nil on a clean (signal-driven) shutdown so the caller's
// deferred cleanup (db.Close, observability flush) runs normally. It returns
// a non-nil error only on a real ListenAndServe failure (e.g. the port is
// already bound).
func Run(ctx context.Context, addr string, handler http.Handler, opts ...Option) error {
	cfg := config{
		writeTimeout:    defaultWriteTimeout,
		shutdownTimeout: defaultShutdownTimeout,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		IdleTimeout:       defaultIdleTimeout,
		WriteTimeout:      cfg.writeTimeout,
	}

	// serveErr carries a real listen error (e.g. address in use). A clean
	// shutdown via Shutdown surfaces http.ErrServerClosed, which we drop.
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// Listener failed before any shutdown signal — surface it.
		return err
	case <-ctx.Done():
		// Signal received: drain, then run post-drain hooks.
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Drain exceeded the bound (or was itself cancelled). Log but keep
		// going so OnShutdown hooks + the caller's deferred cleanup still
		// run — a forced close is better than skipping resource teardown.
		log.Printf("srvkit: graceful shutdown incomplete: %v", err)
	}

	// Post-drain hooks run in registration order, AFTER the HTTP server has
	// stopped accepting/serving requests. They share the bounded shutdown
	// context so a hung hook can't block exit forever.
	for _, fn := range cfg.onShutdown {
		fn(shutdownCtx)
	}

	return nil
}
