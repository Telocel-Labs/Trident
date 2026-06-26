package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Depo-dev/trident/services/api/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the gRPC connection and client
type Client struct {
	conn *grpc.ClientConn
	gen.EventsClient
}

// NewClient creates a new gRPC client connection
func NewClient(ctx context.Context, addr string) (*Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		dialCtx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(10 * 1024 * 1024)),
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
