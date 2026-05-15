package httputil

import (
	"net/http"
	"strings"
)

// CORSMiddleware returns a middleware that adds CORS response headers
// and short-circuits OPTIONS preflight requests.
//
// allowedOrigins is a comma-separated list of origins (e.g.
// "http://localhost:3000,http://127.0.0.1:18091"). "*" is accepted
// with the usual caveat: browsers block "*" combined with credentials.
// For credentialed fetches the middleware echoes the request's Origin
// header back only if it matches one of the configured entries.
//
// The allowed HTTP methods + headers are fixed to what the lakehouse
// frontend currently sends; widen as needed when new verbs or headers
// land.
//
// Typical wiring (OUTERMOST so OPTIONS preflight never hits auth /
// trace / business layers):
//
//	handler := httputil.CORSMiddleware(os.Getenv("CORS_ALLOW_ORIGINS"))(
//	    observability.TraceContextMiddleware(
//	      ... inner middlewares ... (mux)))
func CORSMiddleware(allowedOriginsCSV string) func(http.Handler) http.Handler {
	allow := parseOrigins(allowedOriginsCSV)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				w.Header().Set("Vary", "Origin")
				if allowOriginMatch(allow, origin) {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Project-ID, X-Requested-With")
					w.Header().Set("Access-Control-Max-Age", "600")
				} else {
					// Origin was present but not allow-listed. Set a "null"
					// ACAO sentinel so browsers block the response AND so
					// legacy pkg/httputil.CorsHeaders (which sets `*`) sees
					// a non-empty value and keeps its hands off.
					w.Header().Set("Access-Control-Allow-Origin", "null")
				}
			}
			if r.Method == http.MethodOptions {
				// Preflight — no body needed; 204 is the conventional reply.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func parseOrigins(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func allowOriginMatch(allow []string, origin string) bool {
	for _, a := range allow {
		if a == "*" || a == origin {
			return true
		}
	}
	return false
}
