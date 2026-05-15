// Package httputil: SSE helpers shared across ingest packages.
// SSESetup and WriteSSE are the canonical exported names for setting up
// Server-Sent Events responses and writing individual events.
package httputil

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSESetup configures the response writer for a Server-Sent Events stream
// and sends the initial flush. Callers must call this once before any WriteSSE.
func SSESetup(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// WriteSSE marshals data as JSON and writes a single SSE data frame,
// then flushes the response writer.
func WriteSSE(w http.ResponseWriter, data interface{}) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
