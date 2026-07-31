package grpc

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// traceContextUnaryInterceptor propagates the W3C trace context from the
// incoming HTTP request into outgoing gRPC metadata, so a single trace ID
// spans the API gateway and backend services.
func traceContextUnaryInterceptor(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	// Inject the current trace context into gRPC metadata.
	md := metadata.Pairs()
	otel.GetTextMapPropagator().Inject(ctx, metadataCarrier{md: md})

	// Append injected trace context to outgoing metadata.
	ctx = metadata.AppendToOutgoingContext(ctx, extractMetadataPairs(md)...)

	return invoker(ctx, method, req, reply, cc, opts...)
}

// metadataCarrier implements propagation.TextMapCarrier for gRPC metadata.
type metadataCarrier struct {
	md metadata.MD
}

// Compile-time check that metadataCarrier really satisfies the interface the
// comment above claims. Without this the propagation import is unused and the
// package does not build.
var _ propagation.TextMapCarrier = metadataCarrier{}

// Get retrieves a value by key.
func (c metadataCarrier) Get(key string) string {
	values := c.md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// Set stores a key-value pair, appending to existing values.
func (c metadataCarrier) Set(key, value string) {
	c.md.Append(key, value)
}

// Keys returns all keys present in the carrier.
func (c metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.md))
	for key := range c.md {
		keys = append(keys, key)
	}
	return keys
}

// extractMetadataPairs converts metadata.MD to alternating key-value strings for AppendToOutgoingContext.
func extractMetadataPairs(md metadata.MD) []string {
	pairs := make([]string, 0, len(md)*2)
	for key, values := range md {
		for _, value := range values {
			pairs = append(pairs, key, value)
		}
	}
	return pairs
}
