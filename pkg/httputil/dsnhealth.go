// Package httputil provides shared HTTP utilities for the lakehouse2ontology
// service split. dsnhealth.go implements the /healthz?check=db endpoint and
// the service_db_target_hash_int Prometheus gauge described in §3.8 #7 (Δ-2).
//
// Security notes:
//   - /healthz?check=db requires an X-Internal-Token header matching the
//     INTERNAL_TOKEN env var. Unauthenticated callers receive only a plain
//     liveness response with no DB information or hash.
//   - DB ping errors are logged server-side only; the response body never
//     contains err.Error() to prevent host/DSN disclosure.
//   - Recommend external rate limiting on this endpoint to prevent pool
//     exhaustion via concurrent db.Ping() calls.
package httputil

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var dbTargetHashGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "service_db_target_hash_int",
	Help: "First 8 bytes (as int64) of sha256(server+database) — drift across services signals split-brain DSN",
})

// DBHealth holds the dependencies for the /healthz handler.
type DBHealth struct {
	DB  *sql.DB
	DSN string
}

// New constructs a DBHealth.
func New(db *sql.DB, dsn string) *DBHealth {
	return &DBHealth{DB: db, DSN: dsn}
}

// Install registers GET /healthz on mux and seeds the Prometheus gauge.
// Equivalent to the inline InstallHealthzDB from §3.8 #7.
func (h *DBHealth) Install(mux *http.ServeMux) {
	InstallHealthzDB(mux, h.DSN, h.DB, "")
}

// InstallHealthzDB registers GET /healthz?check=db on mux. The hash is
// deterministic for the same (server, database) tuple and stable across
// service restarts. All 6 services must report the SAME hash at cutover
// day T-0 00:40.
//
// Access control: ?check=db requires X-Internal-Token matching INTERNAL_TOKEN
// env var. Without it, only a plain liveness {"ok":true} is returned.
func InstallHealthzDB(mux *http.ServeMux, dsn string, db *sql.DB, serviceName string) {
	hash := dsnTargetHash(dsn)
	if v := hashPrefixAsInt(hash); v != 0 {
		dbTargetHashGauge.Set(float64(v))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("check") != "db" {
			// Liveness-only healthz — no auth required, no DB info disclosed.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"ok":true,"service":%q}`, serviceName)
			return
		}

		// DB readiness check: require internal token to prevent info disclosure
		// and pool exhaustion by unauthenticated callers.
		expected := os.Getenv("INTERNAL_TOKEN")
		provided := r.Header.Get("X-Internal-Token")
		if expected == "" || provided != expected {
			// Return plain liveness — do not reveal db_target_hash or ping errors.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
			return
		}

		// Authenticated DB ping.
		if err := db.Ping(); err != nil {
			// Log real error server-side only; never echo to response body.
			log.Printf("[healthz] db ping failed for service %q: %v", serviceName, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "service": serviceName, "db": "unhealthy",
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "service": serviceName, "db_target_hash": hash,
		})
	})
}

// dsnTargetHash = sha256(scheme+host+port+path) of the parsed DSN,
// hex-encoded. Intentionally excludes user/password so it's safe to
// publish in logs/metrics. Intentionally ignores sslmode/connect_timeout.
func dsnTargetHash(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	target := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.ToLower(u.Path)
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:])
}

// hashPrefixAsInt converts the first 16 hex chars of h to an int64.
// Uses strconv.ParseUint for correct overflow handling.
func hashPrefixAsInt(h string) int64 {
	if len(h) < 16 {
		return 0
	}
	v, err := strconv.ParseUint(h[:16], 16, 64)
	if err != nil {
		return 0
	}
	return int64(v)
}
