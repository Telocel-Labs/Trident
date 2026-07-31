package grpc

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// failNTimesInvoker returns an invoker that fails with the given code n times
// before succeeding, counting every attempt.
func failNTimesInvoker(n int, code codes.Code, attempts *int) grpc.UnaryInvoker {
	return func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		*attempts++
		if *attempts <= n {
			return status.Error(code, "transient")
		}
		return nil
	}
}

func TestRetry_TransientUnavailableRetried(t *testing.T) {
	var attempts int
	invoker := failNTimesInvoker(2, codes.Unavailable, &attempts)

	err := retryUnaryInterceptor(context.Background(), gen.Events_GetEvent_FullMethodName, nil, nil, nil, invoker)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetry_AttemptsBounded(t *testing.T) {
	var attempts int
	invoker := failNTimesInvoker(10, codes.Unavailable, &attempts)

	err := retryUnaryInterceptor(context.Background(), gen.Events_ListEvents_FullMethodName, nil, nil, nil, invoker)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable after exhausting retries, got %v", err)
	}
	if attempts != retryMaxAttempts {
		t.Errorf("expected %d attempts, got %d", retryMaxAttempts, attempts)
	}
}

func TestRetry_DeadlineExceededNotRetried(t *testing.T) {
	var attempts int
	invoker := failNTimesInvoker(10, codes.DeadlineExceeded, &attempts)

	err := retryUnaryInterceptor(context.Background(), gen.Events_GetEvent_FullMethodName, nil, nil, nil, invoker)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("deadline errors must not be retried; got %d attempts", attempts)
	}
}

func TestRetry_NonIdempotentMethodNotRetried(t *testing.T) {
	var attempts int
	invoker := failNTimesInvoker(10, codes.Unavailable, &attempts)

	err := retryUnaryInterceptor(context.Background(), "/trident.Events/SomeMutation", nil, nil, nil, invoker)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("unlisted methods must not be retried; got %d attempts", attempts)
	}
}

func TestRetry_CancelledContextStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var attempts int
	invoker := func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		attempts++
		cancel() // the call context dies during the first attempt
		return status.Error(codes.Unavailable, "transient")
	}

	err := retryUnaryInterceptor(ctx, gen.Events_GetEvent_FullMethodName, nil, nil, nil, invoker)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected the last attempt error, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected no retry after context cancel, got %d attempts", attempts)
	}
}

func TestMetricsInterceptor_RecordsAttempts(t *testing.T) {
	invoker := func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "transient")
	}
	if err := metricsUnaryInterceptor(context.Background(), gen.Events_GetEvent_FullMethodName, nil, nil, nil, invoker); err == nil {
		t.Fatal("expected error passthrough")
	}

	var buf bytes.Buffer
	WriteClientMetrics(&buf)
	out := buf.String()
	if !strings.Contains(out, `trident_grpc_client_requests_total{method="/trident.Events/GetEvent",code="Unavailable"}`) {
		t.Errorf("missing request counter, got:\n%s", out)
	}
	if !strings.Contains(out, `trident_grpc_client_latency_seconds_total{method="/trident.Events/GetEvent"}`) {
		t.Errorf("missing latency counter, got:\n%s", out)
	}
}
