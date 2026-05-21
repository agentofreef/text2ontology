package srvkit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRecoverMiddleware_PanicTo500 asserts a panicking handler is recovered,
// the process does not crash, and a 500 is written when nothing was sent yet.
func TestRecoverMiddleware_PanicTo500(t *testing.T) {
	h := RecoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	// If RecoverMiddleware failed to recover, this call would panic and fail
	// the test outright.
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestRecoverMiddleware_PreservesWrittenStatus asserts that when the handler
// already wrote a response before panicking, RecoverMiddleware does not
// overwrite the status with 500.
func TestRecoverMiddleware_PreservesWrittenStatus(t *testing.T) {
	h := RecoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial"))
		panic("late boom")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (must not overwrite a started response)", rec.Code)
	}
}

// TestRecoverMiddleware_AbortHandlerRepanics asserts http.ErrAbortHandler is
// NOT swallowed — it must propagate so net/http handles it as designed.
func TestRecoverMiddleware_AbortHandlerRepanics(t *testing.T) {
	h := RecoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected http.ErrAbortHandler to re-panic, but it was swallowed")
		}
		if rec != http.ErrAbortHandler {
			t.Fatalf("re-panicked with %v, want http.ErrAbortHandler", rec)
		}
	}()
	h.ServeHTTP(rec, req)
}

// TestRecoverMiddleware_NoPanicPassthrough asserts a normal handler is left
// untouched (status + body preserved, Flush forwarded).
func TestRecoverMiddleware_NoPanicPassthrough(t *testing.T) {
	h := RecoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
	if !rec.Flushed {
		t.Fatal("expected Flush to be forwarded through statusWriter")
	}
}
