# Invocation metrics fixtures

Golden inputs for the per-invocation fee + resource decoder
(`crates/indexer/src/parser/invocation_metrics.rs`, issue #266).

Each file holds the `envelopeXdr` and `resultXdr` base64 strings exactly as
the Soroban `getTransaction` RPC returns them, plus the metrics the decoder is
expected to produce. The payloads are XDR-encoded from a synthetic
`InvokeHostFunction` transaction rather than captured from a live network —
the wire encoding is real, so a change to the transaction layout or to
`stellar-xdr` breaks these tests.

See `docs/contract-invocation-metering.md` for why `cpu_instructions`,
`read_bytes`, and `write_bytes` are the transaction's *declared* Soroban
resource budget rather than host-measured usage, and what `provenance`
records about that distinction.
