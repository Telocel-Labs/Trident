package grpc

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// gRPC client metrics in the same dependency-free Prometheus text-exposition
// style as the gauges in handlers/stats.go. Every call attempt (including
// each retry attempt) is recorded with its method and result code.

type clientMetricsRegistry struct {
	mu             sync.Mutex
	requests       map[string]uint64  // "method\x00code" → attempt count
	latencySeconds map[string]float64 // method → cumulative call latency
}

var clientMetrics = clientMetricsRegistry{
	requests:       make(map[string]uint64),
	latencySeconds: make(map[string]float64),
}

func (m *clientMetricsRegistry) record(method, code string, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[method+"\x00"+code]++
	m.latencySeconds[method] += elapsed.Seconds()
}

// metricsUnaryInterceptor records latency and result code for every unary
// call attempt. It sits innermost in the interceptor chain so each retried
// attempt is measured and counted individually.
func metricsUnaryInterceptor(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	start := time.Now()
	err := invoker(ctx, method, req, reply, cc, opts...)
	clientMetrics.record(method, status.Code(err).String(), time.Since(start))
	return err
}

// WriteClientMetrics writes the gRPC client metrics in Prometheus text
// format. Mounted into the API's /metrics endpoint by handlers.MetricsHandler.
func WriteClientMetrics(w io.Writer) {
	clientMetrics.mu.Lock()
	defer clientMetrics.mu.Unlock()

	if len(clientMetrics.requests) > 0 {
		_, _ = fmt.Fprintf(w, "# HELP trident_grpc_client_requests_total gRPC client call attempts by method and status code.\n")
		_, _ = fmt.Fprintf(w, "# TYPE trident_grpc_client_requests_total counter\n")
		keys := make([]string, 0, len(clientMetrics.requests))
		for k := range clientMetrics.requests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			method, code, _ := strings.Cut(k, "\x00")
			_, _ = fmt.Fprintf(w, "trident_grpc_client_requests_total{method=%q,code=%q} %d\n", method, code, clientMetrics.requests[k])
		}
	}

	if len(clientMetrics.latencySeconds) > 0 {
		_, _ = fmt.Fprintf(w, "# HELP trident_grpc_client_latency_seconds_total Cumulative gRPC client call latency by method.\n")
		_, _ = fmt.Fprintf(w, "# TYPE trident_grpc_client_latency_seconds_total counter\n")
		methods := make([]string, 0, len(clientMetrics.latencySeconds))
		for m := range clientMetrics.latencySeconds {
			methods = append(methods, m)
		}
		sort.Strings(methods)
		for _, m := range methods {
			_, _ = fmt.Fprintf(w, "trident_grpc_client_latency_seconds_total{method=%q} %g\n", m, clientMetrics.latencySeconds[m])
		}
	}
}
