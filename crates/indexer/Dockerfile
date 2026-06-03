# syntax=docker/dockerfile:1

# ── builder ──────────────────────────────────────────────────────────────────
FROM rust:1.84-slim AS builder

WORKDIR /build

# Cache dependency compilation separately from source changes.
# Cargo.lock is gitignored; cargo will generate one if absent.
COPY Cargo.toml ./
COPY crates/common/Cargo.toml crates/common/Cargo.toml
COPY crates/indexer/Cargo.toml crates/indexer/Cargo.toml
COPY crates/api/Cargo.toml crates/api/Cargo.toml

# Dummy source files so `cargo build --release` can compile deps.
RUN mkdir -p crates/common/src crates/indexer/src crates/api/src && \
    echo "pub struct Dummy;" > crates/common/src/lib.rs && \
    echo "fn main() {}" > crates/indexer/src/main.rs && \
    echo "fn main() {}" > crates/api/src/main.rs && \
    cargo build --release -p trident-indexer && \
    rm -rf crates/common/src crates/indexer/src crates/api/src

# Copy real source and rebuild only what changed.
COPY crates/common/src crates/common/src
COPY crates/indexer/src crates/indexer/src
RUN touch crates/indexer/src/main.rs && \
    cargo build --release -p trident-indexer

# ── runtime ──────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --system --no-create-home --uid 1001 trident

COPY --from=builder /build/target/release/trident-indexer /usr/local/bin/trident-indexer

USER trident

ENTRYPOINT ["/usr/local/bin/trident-indexer"]
