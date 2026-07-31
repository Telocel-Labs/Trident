# syntax=docker/dockerfile:1

# Small dedicated image for running the sqlx-tracked migration chain
# (database/migrations/*.sql) as a Helm pre-install/pre-upgrade hook Job
# (issue #308) — deliberately separate from the app images so it can be
# built/tagged/rolled back independently of trident-go-api/grpc-api/indexer.

# ── builder ──────────────────────────────────────────────────────────────────
# Same base + digest pin as crates/api/Dockerfile and crates/indexer/Dockerfile
# so the sqlx-cli install reuses the same cached layer conventions.
FROM rust:1.88-slim@sha256:8474d3e6c65da3ac9f0a39c5c77db2ff6c8c9e3a6eb4dff09b5ed7ed6c0f2f33 AS builder

# Native build deps for sqlx-cli's postgres feature (openssl-sys needs
# pkg-config + libssl-dev + a C toolchain).
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config libssl-dev \
    && rm -rf /var/lib/apt/lists/*

# Pin sqlx-cli to the same sqlx major/minor the app crates build against
# (see Cargo.lock) so the `_sqlx_migrations` tracking table format matches.
RUN cargo install sqlx-cli --version ^0.7 --no-default-features --features rustls,postgres --locked

# ── runtime ──────────────────────────────────────────────────────────────────
# Same digest-pinned debian:bookworm-slim as the other runtime stages.
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libssl3 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 1001 --no-create-home trident

COPY --from=builder /usr/local/cargo/bin/sqlx /usr/local/bin/sqlx
COPY database/migrations /migrations

USER trident

# `sqlx migrate run` is idempotent — it records applied migrations in the
# database's own _sqlx_migrations table and only applies files that aren't
# already recorded there, so re-running this Job on every `helm upgrade` is
# safe. DATABASE_URL is injected by the Helm hook Job from the existing
# secret (global.existingSecret) — never baked into this image.
ENTRYPOINT ["sqlx", "migrate", "run", "--source", "/migrations"]
