# syntax=docker/dockerfile:1

# ── builder ──────────────────────────────────────────────────────────────────
FROM rust:1.78-slim AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    protobuf-compiler \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Cache dependency compilation separately from source changes.
# Cargo.lock is gitignored; cargo will generate one if absent.
COPY Cargo.toml ./
COPY crates/common/Cargo.toml crates/common/Cargo.toml
COPY crates/api/Cargo.toml crates/api/Cargo.toml
COPY crates/indexer/Cargo.toml crates/indexer/Cargo.toml

# Dummy sources so `cargo build --release` can compile deps.
RUN mkdir -p crates/common/src crates/api/src crates/indexer/src && \
    echo "pub struct Dummy;" > crates/common/src/lib.rs && \
    echo "fn main() {}" > crates/api/src/main.rs && \
    echo "fn main() {}" > crates/indexer/src/main.rs && \
    cargo build --release -p trident-api && \
    rm -rf crates/common/src crates/api/src crates/indexer/src

# Copy real source and proto, then rebuild only changed crates.
COPY proto proto
COPY crates/common/src crates/common/src
COPY crates/api/src crates/api/src
COPY crates/api/build.rs crates/api/build.rs
RUN touch crates/api/src/main.rs && \
    cargo build --release -p trident-api

# ── runtime ──────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --system --no-create-home --uid 1001 trident

COPY --from=builder /build/target/release/trident-api /usr/local/bin/trident-api

USER trident

EXPOSE 50051

ENTRYPOINT ["/usr/local/bin/trident-api"]
