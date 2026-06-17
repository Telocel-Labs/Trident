# syntax=docker/dockerfile:1

# ── builder ──────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Cache module downloads before copying source.
COPY services/api/go.mod services/api/go.sum ./
RUN go mod download

# Copy source and build the binary.
COPY services/api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /api ./...

# ── runtime ──────────────────────────────────────────────────────────────────
FROM alpine:3.19 AS runtime

RUN adduser -S -u 1001 trident

COPY --from=builder /api /usr/local/bin/api

USER trident

EXPOSE 3000

ENTRYPOINT ["/usr/local/bin/api"]
