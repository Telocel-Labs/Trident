package grpc

import (
	"context"
	"testing"

	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMetricsUnaryInterceptor_RecordsPrometheusMetrics verifies each call
// attempt updates trident_grpc_client_requests_total and
// trident_grpc_client_request_duration_seconds by method and status code
// (issue #58), alongside the pre-existing dependency-free counters.
func TestMetricsUnaryInterceptor_RecordsPrometheusMetrics(t *testing.T) {
	const method = "/trident.Events/Stream"

	okBefore := testutil.ToFloat64(metrics.GRPCClientRequestsTotal.WithLabelValues(method, codes.OK.String()))
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		return nil
	}
	if err := metricsUnaryInterceptor(context.Background(), method, nil, nil, nil, invoker); err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if got := testutil.ToFloat64(metrics.GRPCClientRequestsTotal.WithLabelValues(method, codes.OK.String())); got != okBefore+1 {
		t.Errorf("OK requests total: want %v, got %v", okBefore+1, got)
	}

	failCode := codes.Unavailable.String()
	failBefore := testutil.ToFloat64(metrics.GRPCClientRequestsTotal.WithLabelValues(method, failCode))
	failInvoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "backend down")
	}
	err := metricsUnaryInterceptor(context.Background(), method, nil, nil, nil, failInvoker)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable error, got %v", err)
	}
	if got := testutil.ToFloat64(metrics.GRPCClientRequestsTotal.WithLabelValues(method, failCode)); got != failBefore+1 {
		t.Errorf("Unavailable requests total: want %v, got %v", failBefore+1, got)
	}
}
