package grpc

import (
	"context"
	"time"

	"github.com/Depo-dev/trident/services/api/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Retry policy for idempotent unary RPCs: up to retryMaxAttempts total
// attempts with exponential backoff starting at retryBaseDelay. Only
// Unavailable is retried — it marks a transient transport failure.
// DeadlineExceeded is never retried because the caller's time budget is
// already spent.
const (
	retryMaxAttempts = 3
	retryBaseDelay   = 100 * time.Millisecond
)

// retryableMethods lists the unary RPCs that are safe to auto-retry:
// read-only and idempotent. Mutating RPCs must never be added here, and
// streams never pass through a unary interceptor at all.
var retryableMethods = map[string]bool{
	gen.Events_ListEvents_FullMethodName: true,
	gen.Events_GetEvent_FullMethodName:   true,
}

// retryUnaryInterceptor retries idempotent unary RPCs on Unavailable with
// exponential backoff, bounded by retryMaxAttempts and the call deadline.
func retryUnaryInterceptor(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	if !retryableMethods[method] {
		return invoker(ctx, method, req, reply, cc, opts...)
	}

	delay := retryBaseDelay
	for attempt := 1; ; attempt++ {
		err := invoker(ctx, method, req, reply, cc, opts...)
		if status.Code(err) != codes.Unavailable || attempt == retryMaxAttempts {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		delay *= 2
	}
}
