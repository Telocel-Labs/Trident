// Package grpcclient provides a gRPC connection to the Rust API with
// exponential backoff reconnection and keepalive probing.
package grpcclient

import (
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// Connect dials the gRPC target non-blocking. The returned *grpc.ClientConn
// will transparently reconnect with exponential backoff if the server goes away.
//
// Backoff: base 200 ms, multiplier 1.6, max 30 s, jitter 0.2.
// Keepalive: ping every 10 s, 5 s timeout, allowed without streams.
func Connect(target string) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		target,
		grpc.WithInsecure(), //nolint:staticcheck // TLS is terminated at the load balancer
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
	)
}

// IsUnavailable reports whether err is a gRPC Unavailable status.
func IsUnavailable(err error) bool {
	return status.Code(err) == codes.Unavailable
}

// LogIfUnavailable logs a warning when the gRPC call fails with Unavailable.
func LogIfUnavailable(err error, target string) {
	if IsUnavailable(err) {
		slog.Warn("gRPC target unavailable", "target", target)
	}
}

// StateString returns a human-readable label for a connectivity state.
func StateString(s connectivity.State) string {
	switch s {
	case connectivity.Idle:
		return "idle"
	case connectivity.Connecting:
		return "connecting"
	case connectivity.Ready:
		return "ready"
	case connectivity.TransientFailure:
		return "error"
	case connectivity.Shutdown:
		return "shutdown"
	default:
		return "unknown"
	}
}
