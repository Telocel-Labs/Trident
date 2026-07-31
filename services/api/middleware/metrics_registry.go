package middleware

// Prometheus-registry HTTP metrics (issue #58), served on the dedicated
// METRICS_PORT listener in internal/metrics.
//
// This lives alongside metrics.go rather than inside it: that file holds the
// hand-rolled counters rendered into the public GET /metrics route by
// handlers.MetricsHandler, and the two are independent — different storage,
// different exposition path, no shared state. Keeping them in separate files
// keeps that boundary obvious.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/metrics"
)

// legacyMetricsPattern is the route pattern of the pre-existing hand-rolled
// /metrics endpoint on the public mux (handlers.MetricsHandler, main.go).
// Excluded from duration tracking per issue #58 — it isn't a "real" endpoint
// whose latency is meaningful, and self-scraping would otherwise skew the
// distribution.
const legacyMetricsPattern = "GET /metrics"

// NewMetrics returns middleware that records per-endpoint HTTP request
// counts and latency to the internal Prometheus registry (issue #58).
//
// mux is the same *http.ServeMux the request will ultimately be routed
// through; mux.Handler(r) is a side-effect-free lookup that resolves the
// registered route pattern (e.g. "GET /v1/events/{id}") for use as a
// bounded-cardinality label, and works even for requests rejected by an
// earlier middleware (auth, rate limiting) before ever reaching mux.
func NewMetrics(mux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, pattern := mux.Handler(r)
			if pattern == "" {
				pattern = "unmatched"
			}

			if pattern == legacyMetricsPattern {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			wrapped := &LoggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(wrapped, r)
			duration := time.Since(start)

			status := strconv.Itoa(wrapped.statusCode)
			metrics.HTTPRequestsTotal.WithLabelValues(r.Method, pattern, status).Inc()
			metrics.HTTPRequestDuration.WithLabelValues(r.Method, pattern, status).Observe(duration.Seconds())
		})
	}
}
