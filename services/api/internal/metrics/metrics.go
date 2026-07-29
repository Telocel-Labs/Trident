// Package metrics provides a client_golang-backed Prometheus registry served
// on its own internal port (issue #58), separate from the public API port.
//
// It is additive to the pre-existing hand-rolled, dependency-free
// Prometheus-text metrics mounted at GET /metrics on the public mux
// (handlers.MetricsHandler and friends) — that endpoint is untouched. This
// package covers the specific gaps called out by #58: per-endpoint HTTP
// request counts/latency, active WebSocket connections and message totals,
// outbound gRPC call metrics, and rate-limiting rejections.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DefaultPort is used when METRICS_PORT is unset.
const DefaultPort = "9091"

// Registry is a dedicated registry (not the global default) so this endpoint
// exposes exactly the collectors defined here — no Go-runtime default
// collectors mixed in.
var Registry = prometheus.NewRegistry()

var (
	HTTPRequestsTotal = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "trident_http_requests_total",
		Help: "Total HTTP requests handled by the Go API, by method, route pattern, and status code.",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.With(Registry).NewHistogramVec(prometheus.HistogramOpts{
		Name:    "trident_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds, by method, route pattern, and status code.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status"})

	WSActiveConnections = promauto.With(Registry).NewGauge(prometheus.GaugeOpts{
		Name: "trident_ws_active_connections",
		Help: "Currently active WebSocket subscribers (REST WS + GraphQL subscriptions).",
	})

	WSConnectsTotal = promauto.With(Registry).NewCounter(prometheus.CounterOpts{
		Name: "trident_ws_connects_total",
		Help: "Total WebSocket subscriber registrations since startup.",
	})

	WSDisconnectsTotal = promauto.With(Registry).NewCounter(prometheus.CounterOpts{
		Name: "trident_ws_disconnects_total",
		Help: "Total WebSocket subscriber unregistrations since startup.",
	})

	WSMessagesTotal = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "trident_ws_messages_total",
		Help: "Total WebSocket broadcast messages, by outcome.",
	}, []string{"result"}) // result: sent|dropped

	GRPCClientRequestsTotal = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "trident_grpc_client_requests_total",
		Help: "Total outbound gRPC client call attempts, by method and status code.",
	}, []string{"method", "code"})

	GRPCClientRequestDuration = promauto.With(Registry).NewHistogramVec(prometheus.HistogramOpts{
		Name:    "trident_grpc_client_request_duration_seconds",
		Help:    "Outbound gRPC client call duration in seconds, by method and status code.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "code"})

	RateLimitRejectionsTotal = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "trident_ratelimit_rejections_total",
		Help: "Total requests rejected by a rate limiter, by limiter.",
	}, []string{"limiter"}) // limiter: per_key|per_ip|global_concurrency
)

// Port returns the port the metrics server listens on (METRICS_PORT, or
// DefaultPort).
func Port() string {
	if p := os.Getenv("METRICS_PORT"); p != "" {
		return p
	}
	return DefaultPort
}

// Handler builds a mux exposing only GET /metrics, backed by Registry.
// Exposed for testing.
func Handler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
	return mux
}

// Start launches the internal metrics server on METRICS_PORT (default 9091)
// and returns its *http.Server so the caller can shut it down.
func Start() *http.Server {
	addr := ":" + Port()
	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("metrics server listening", "addr", addr)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server error", "err", err)
		}
	}()

	return srv
}

// Shutdown gracefully stops the metrics server (nil-safe).
func Shutdown(srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
