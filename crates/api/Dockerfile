# syntax=docker/dockerfile:1

# ── builder ──────────────────────────────────────────────────────────────────
FROM rust:1.88-slim@sha256:38bc5a86d998772d4aec2348656ed21438d20fcdce2795b56ca434cf21430d89 AS builder

WORKDIR /app

# Native build dependencies for -sys crates (openssl-sys needs pkg-config +
# libssl-dev + a C toolchain).
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config libssl-dev \
    && rm -rf /var/lib/apt/lists/*

# Cache dependency fetch separately from source compilation.
COPY Cargo.toml Cargo.lock ./
COPY proto/ proto/
COPY crates/common/Cargo.toml crates/common/Cargo.toml
COPY crates/api/Cargo.toml    crates/api/Cargo.toml
COPY crates/indexer/Cargo.toml crates/indexer/Cargo.toml
COPY crates/backfill/Cargo.toml crates/backfill/Cargo.toml
COPY sdk/rust/Cargo.toml sdk/rust/Cargo.toml

RUN mkdir -p crates/common/src crates/api/src crates/indexer/src crates/backfill/src sdk/rust/src \
    && echo "pub fn _stub() {}" > crates/common/src/lib.rs \
    && echo "fn main() {}"      > crates/api/src/main.rs \
    && echo "fn main() {}"      > crates/indexer/src/main.rs \
    && echo "fn main() {}"      > crates/backfill/src/main.rs \
    && echo "pub fn _stub() {}" > sdk/rust/src/lib.rs \
    && cargo build --release -p trident-api

COPY . .

# Refresh mtimes so cargo rebuilds the real crates instead of reusing the
# cached stub artifacts from the dependency-caching layer above.
RUN find crates sdk -name '*.rs' -exec touch {} + \
    && cargo build --release -p trident-api

# ── runtime ──────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libssl3 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 1001 --no-create-home trident

COPY --from=builder /app/target/release/trident-api /usr/local/bin/trident-api

USER trident

EXPOSE 50051

ENTRYPOINT ["/usr/local/bin/trident-api"]
