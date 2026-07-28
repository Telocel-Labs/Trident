package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// requestIDMetadataKey is the gRPC metadata key used to propagate the API
// request id to downstream services. Lower-case per gRPC metadata convention.
const requestIDMetadataKey = "x-request-id"

// requestIDUnaryInterceptor copies the request id attached to the call context
// by the RequestID middleware into outgoing gRPC metadata, so a request can be
// correlated across the API gateway and backend services.
func requestIDUnaryInterceptor(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	if id := httputil.RequestIDFromContext(ctx); id != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, requestIDMetadataKey, id)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

// Client wraps the gRPC connection and client
type Client struct {
	conn *grpc.ClientConn
	gen.EventsClient
}

// NewClient creates a new gRPC client connection.
//
// The connection reconnects transparently with exponential backoff (base
// 200 ms, multiplier 1.6, max 30 s) and probes liveness with keepalive pings
// (every 10 s, 5 s timeout) so a restarted backend is picked up without
// restarting the API (issue #227). Idempotent unary RPCs are retried on
// transient failures by retryUnaryInterceptor; every attempt is measured by
// metricsUnaryInterceptor, which must stay innermost in the chain.
func NewClient(_ context.Context, addr string) (*Client, error) {
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(10*1024*1024)),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  200 * time.Millisecond,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   30 * time.Second,
			},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithChainUnaryInterceptor(traceContextUnaryInterceptor, requestIDUnaryInterceptor, retryUnaryInterceptor, metricsUnaryInterceptor),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial gRPC server: %w", err)
	}

	slog.Info("connected to gRPC server", "addr", addr)
	return &Client{
		conn:         conn,
		EventsClient: gen.NewEventsClient(conn),
	}, nil
}

// Close closes the gRPC connection
func (c *Client) Close() error {
	return c.conn.Close()
}
