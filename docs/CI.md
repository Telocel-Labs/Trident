# CI caching and job layout

This document explains how CI caching is set up (issue #309) so future changes
to `.github/workflows/ci.yml` don't accidentally regress it, and how to verify
cache effectiveness on any given run.

## Rust: sccache + shared rust-cache keys

Every Rust job (`rust`, `rust-integration`, `sdk-rust`, `contracts`,
`e2e-contract-events`) sets:

```yaml
env:
  RUSTC_WRAPPER: sccache
  SCCACHE_GHA_ENABLED: "true"
```

and runs `mozilla-actions/sccache-action` before the `Swatinem/rust-cache`
step. `sccache` caches individual compiled objects (keyed by crate, flags,
and rustc version) in the GitHub Actions cache backend, rather than only
caching a job-local `target/` directory. That makes the cache reusable
*across* jobs that compile overlapping code, not just across re-runs of the
same job:

- `rust` and `rust-integration` build the exact same root workspace against
  the same `Cargo.lock`. They now share one `Swatinem/rust-cache` key
  (`shared-key: rust-workspace`) plus the sccache object cache. These two
  jobs have no `needs:` between them and run in parallel, so on a run where
  the cache is cold, both build independently and only one of them wins the
  cache save at end-of-job (GitHub Actions cache keys are immutable — the
  second write is a harmless no-op). The payoff is therefore mostly a
  cross-run one: once either job has populated `rust-workspace`, subsequent
  runs of both jobs reuse it, rather than each job maintaining its own
  separate, job-scoped cache as before.
- `contracts` and `e2e-contract-events` both compile the `token` contract to
  `wasm32v1-none`. `e2e-contract-events` has `needs: [contracts, ...]`, so by
  the time it runs, `contracts` has already populated the shared
  `contracts-wasm` cache key — the second build becomes a near-total cache
  hit instead of a full wasm rebuild.

Each job ends with a `sccache --show-stats` step (`if: always()`) so cache
hit/miss counts for that run are visible directly in the job log without
needing a separate dashboard.

`cargo fmt --all -- --check` in the `rust` job was also moved to run
immediately after toolchain setup, before the Postgres-dependent `sqlx-cli`
install / migrate / `sqlx prepare` steps. Formatting is the most common CI
failure and needs neither the database nor a compiled dependency graph, so it
now fails in seconds instead of after several minutes of DB setup.

## Docker: per-image cache scopes

The `docker`, `e2e`, and `e2e-contract-events` jobs build/restore three
images (Go API, Rust gRPC API, Rust indexer) using
`docker/build-push-action` with `cache-from`/`cache-to: type=gha`.

Previously none of these specified a `scope`, which means the `gha` cache
backend defaulted all three builds to the same `buildkit` scope. Each image
build's cache write was overwriting the previous image's cache blobs for that
scope, so effective hit-rate degraded as more images were added to the
workflow. Each image now uses its own scope (`scope=go-api`, `scope=grpc-api`,
`scope=indexer`) on both the write side (`docker` job) and every read side
(`e2e`, `e2e-contract-events`), so the three caches accumulate independently
instead of colliding.

The Dockerfiles themselves (`services/api/Dockerfile`,
`crates/api/Dockerfile`, `crates/indexer/Dockerfile`) already copy manifest
files (`go.mod`/`go.sum`, `Cargo.toml`/`Cargo.lock`) and build a
dependency-only stub before `COPY . .`, so dependency-fetch layers stay
cached across changes that only touch application source. That ordering was
already correct and is unchanged here.

## Fast feedback vs. heavy jobs

- A workflow-level `concurrency` group cancels superseded runs on the same
  branch/PR, so pushing a fixup commit doesn't leave a stale, doomed run
  burning a runner for another 10+ minutes.
- `rust`, `go`, `typescript`, `openapi`, `sdk-*`, and `contracts` run in
  parallel with no `needs`, so lint/unit feedback isn't serialized behind the
  heavy jobs.
- `e2e` and `e2e-contract-events` (full Docker Compose stacks, and for the
  latter, a local Soroban network) are gated behind `needs:` on the jobs that
  produce their inputs (images, contract WASM), so they only start once
  there's a reasonable chance the fast checks will also pass, and they never
  block the fast jobs from reporting first.

## Verifying the improvement on a PR

There's no static "before/after" number to hardcode here, since actual
wall-clock depends on GitHub's shared-runner load at the time. To verify on a
real PR:

1. Compare the `rust-integration` job's cache-setup and build step durations
   against a `rust` job run in the same workflow run — after the first run
   populates the `rust-workspace` sccache/rust-cache entries, `sccache
   --show-stats` in `rust-integration` should show a high hit count.
2. Compare `contracts` vs. `e2e-contract-events`'s "Build the reference token
   contract" step duration — the latter should be dramatically shorter once
   the `contracts-wasm` cache is warm.
3. In the `docker`, `e2e`, and `e2e-contract-events` job logs, look for
   `CACHED` markers on the Dockerfile steps prior to `COPY . .` — these
   should now hit consistently across runs that don't change dependency
   manifests, instead of only on the specific job that most recently wrote
   the shared `buildkit` scope.
