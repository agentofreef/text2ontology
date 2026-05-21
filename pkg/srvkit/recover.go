package srvkit

import (
	"log"
	"net/http"
	"runtime/debug"
)

// RecoverMiddleware recovers panics raised by downstream handlers, logs a
// structured line (method, path, the recovered value, and the stack from
// debug.Stack()), and writes a 500 if no response has been started yet.
//
// It deliberately re-panics http.ErrAbortHandler: net/http uses that sentinel
// to abort a request without logging, and swallowing it would defeat the
// stdlib's own connection-teardown semantics.
//
// Wire it just inside CORS so panics are caught while CORS response headers
// are still applied (CORS → RecoverMiddleware → trace → span → auth → mux).
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is the stdlib's "abort silently" sentinel.
			// Re-panic so net/http handles it as designed.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			log.Printf("srvkit: panic recovered method=%s path=%s err=%v\n%s",
				r.Method, r.URL.Path, rec, debug.Stack())
			// Only write a 500 if the handler hasn't already started a
			// response — otherwise we'd corrupt a partially written body or
			// emit a superfluous WriteHeader warning.
			if !sw.wrote {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// statusWriter tracks whether the response has begun so RecoverMiddleware can
// avoid a double WriteHeader. It transparently delegates everything else.
type statusWriter struct {
	http.ResponseWriter
	wrote bool
}

func (s *statusWriter) WriteHeader(code int) {
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so SSE / streaming handlers that
// type-assert http.Flusher keep flushing per-chunk through this wrapper.
// Without it, agent-server's text/event-stream handler would silently lose
// its flush capability when RecoverMiddleware sits in front of it.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
