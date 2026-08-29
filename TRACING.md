# End-to-End OpenTelemetry Tracing

This document describes how Trident implements and uses OpenTelemetry tracing to provide distributed tracing across the indexer, gRPC API, and Go HTTP API.

## Architecture

The tracing infrastructure spans three services:

1. **Go HTTP API** (`services/api`) - Entry point for HTTP requests
2. **gRPC API** (`crates/api`) - RPC interface for indexing and querying
3. **Indexer** (`crates/indexer`) - Soroban event processor

Trace context flows as follows:

```
HTTP Request (W3C traceparent header)
    ↓
Go API (extract W3C context, create HTTP span)
    ↓
gRPC Call (propagate trace context in metadata)
    ↓
Rust API & Indexer (continue trace, emit OTLP spans)
    ↓
OTLP Collector (centralized trace collection)
```

## Configuration

### Go API

Tracing is configured via environment variables:

- `OTEL_EXPORTER_OTLP_ENDPOINT` - gRPC endpoint for OTLP trace export (e.g., `localhost:4317`)
  - If unset, tracing is disabled (no-op exporter)
- `OTEL_SAMPLING_RATIO` - Sampling ratio (0.0-1.0, default 0.1 = 10%)

Example:
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
export OTEL_SAMPLING_RATIO=0.5  # 50% sampling
cargo run -p api
```

### Rust Indexer

Tracing is configured via environment variables:

- `OTEL_EXPORTER_OTLP_ENDPOINT` - gRPC endpoint for OTLP trace export (e.g., `localhost:4317`)
  - If unset, tracing is disabled (no-op exporter)
- `RUST_LOG` - Log level filter (e.g., `info,trident=debug`)

Example:
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
cargo run -p indexer
```

## Trace Context Propagation

### HTTP → gRPC

When an HTTP request arrives at the Go API:

1. The `TracingMiddleware` extracts W3C trace context from the `traceparent` header
2. A span is created for the HTTP request with attributes:
   - `http.method` - HTTP method (GET, POST, etc.)
   - `http.url` - Full URL
   - `http.target` - URL path
   - `http.status_code` - Response status code
   - `http.client_ip` - Client IP address
3. The trace context is stored in the request context
4. When a gRPC call is made, the `traceContextUnaryInterceptor` propagates the trace context into gRPC metadata
5. The Rust API and indexer receive the trace context in the gRPC metadata and continue the span

### Request ID Correlation

The API also propagates request IDs via gRPC metadata (in addition to trace IDs):

- HTTP middleware sets `X-Request-ID` header
- gRPC interceptor copies it to `x-request-id` gRPC metadata
- Backend services can correlate logs and spans using the request ID

## Viewing Traces

### Local Development with Jaeger

To visualize traces locally:

1. **Start Jaeger** (all-in-one):

```bash
docker run -d \
  -p 6831:6831/udp \
  -p 6832:6832/udp \
  -p 5778:5778 \
  -p 16686:16686 \
  -p 14268:14268 \
  jaegertracing/all-in-one:latest
```

2. **Start services with OTLP endpoint**:

```bash
# Terminal 1: Indexer
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
cargo run -p indexer

# Terminal 2: Go API (in a separate shell after starting indexer)
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
cargo run -p api

# Terminal 3: Make a request
curl http://localhost:3000/v1/health
```

3. **View traces** in Jaeger UI: http://localhost:16686

### Interpreting Traces

A typical trace shows:

```
Trace ID: <uuid>
└── GET /v1/health (root span, Go API)
    ├── Attributes: http.method=GET, http.status_code=200
    └── Child: QueryEvents (gRPC call)
        ├── Attributes: grpc.method=QueryEvents
        └── Spans from Rust backend
```

Each span shows:
- Duration (time spent in that operation)
- Attributes (contextual data like HTTP status, gRPC method)
- Events (discrete moments, e.g., "query started")

## Adding New Spans

To add custom spans in Go handlers:

```go
import "go.opentelemetry.io/otel"

func MyHandler(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    tracer := otel.Tracer("my-package")
    
    ctx, span := tracer.Start(ctx, "my-operation")
    defer span.End()
    
    // Your handler logic
}
```

To add custom spans in Rust:

```rust
use tracing::info_span;

let span = info_span!("my_operation", key = "value");
let _enter = span.enter();

// Your logic here
```

## Testing Trace Propagation

To verify trace context is propagated end-to-end:

1. Make a request to the Go API with a manual trace ID:

```bash
curl -H "traceparent: 00-$(uuidgen | tr '[:upper:]' '[:lower:]' | tr -d '-')-$(hexdump -n 8 -v -e '/1 "%02x"' /dev/urandom)-01" \
  http://localhost:3000/v1/health
```

2. Look in Jaeger UI for the trace ID (first 32 hex chars from above)
3. Verify the trace includes spans from:
   - Go HTTP handler
   - gRPC client call
   - Rust backend processing

## Troubleshooting

### No traces appearing in Jaeger

- Verify OTEL_EXPORTER_OTLP_ENDPOINT is set correctly
- Check that the OTLP collector is reachable: `nc -zv localhost 4317`
- Verify the service is making requests (check logs)
- Check sampling ratio isn't too low (try 1.0 for 100%)

### Traces incomplete (missing backend spans)

- Verify the Rust services also have `OTEL_EXPORTER_OTLP_ENDPOINT` set
- Check that gRPC calls are actually being made
- Verify the trace context interceptor is in the gRPC client chain

### High memory usage

- Reduce the sampling ratio (OTEL_SAMPLING_RATIO)
- Enable batch span exporter in the collector config
