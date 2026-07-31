# Contract invocation resource + fee metering (issue #266)

## What the Soroban RPC actually exposes

`getTransaction` (and the paged `getTransactions`) return three XDR blobs for
a processed transaction: `envelopeXdr`, `resultXdr`, and `resultMetaXdr`. Only
the first two are used here, and for a documented reason:

| Field | Source | What it contains | Reliability |
|---|---|---|---|
| `fee_charged` | `resultXdr` → `TransactionResult.feeCharged` | The actual total fee charged in stroops (inclusion fee + Soroban resource fee) | Always present for any processed transaction, success or failure |
| CPU instructions, ledger read bytes, ledger write bytes | `envelopeXdr` → `TransactionV1Envelope.tx.ext` (`TransactionExt::V1` → `SorobanTransactionData.resources`) | The resource limits the transaction *declared* — what `simulateTransaction` computed and the submitter signed | Present on every Soroban (`InvokeHostFunction`) transaction; **this is the requested/declared budget, not host-measured actual consumption** |
| `resource_fee` | same `SorobanTransactionData.resourceFee` | The declared resource-fee portion of the total fee | Same caveat as above |

**True per-invocation *measured* metering (actual CPU instructions burned,
actual bytes read/written) is only emitted as `core_metrics` diagnostic
events inside `resultMetaXdr`'s `SorobanTransactionMeta.diagnosticEvents`,
and only when the RPC node runs with diagnostic events enabled** (`--diagnostic-events`
in `stellar-core` / `soroban-rpc` config). This is off by default on most
public RPC endpoints (including Stellar's own public testnet/mainnet nodes)
because of the volume it adds, so relying on it would make metering silently
unavailable for the majority of deployments.

## Decision for the MVP

Trident persists the fields that are **unconditionally available** from
`getTransaction` — `feeCharged` (exact) and the declared `SorobanResources`
(instructions / disk_read_bytes / write_bytes, an accurate upper bound rather
than a measured value) — and records this in a `provenance` column
(`declared_resources`) so consumers of the data are never misled into
thinking these are host-measured numbers. If a future RPC surface exposes
measured `core_metrics` reliably, a second provenance value (e.g. `metered`)
can be added without a schema change.

A failed transaction (`TxFailed`, or fee-bump-inner failure) still charged a
fee, so its `fee_charged` is recorded, but it never reached the resource
budget declared in its envelope — CPU/read/write fields are left `NULL` for
those rows.

## Volume bound

Metering only runs when the indexer has a non-empty contract allowlist
(`indexed_contracts` has rows for the active network). In index-all mode
there is no bound on how many extra `getTransaction` calls a poll cycle could
make, so metering is skipped entirely rather than adding an unbounded RPC
fan-out — this mirrors the existing allowlist-gating pattern used for
server-side event filtering (issue #203).

For each poll page, one `getTransaction` call is made per **unique
transaction hash** among the page's already-allowlisted events (not one call
per event), and the result is attributed to every tracked contract invoked in
that transaction.

## Where this lives

- `crates/indexer/src/rpc/mod.rs` — `RpcClient::get_transaction`
- `crates/indexer/src/parser/invocation_metrics.rs` — XDR decode of
  `envelopeXdr` + `resultXdr` into `InvocationMetrics`
- `crates/indexer/src/db/mod.rs` — persistence into
  `contract_invocation_metrics`
- `database/migrations/0012_contract_invocation_metrics.sql`
- `services/api/handlers/stats.go` — aggregated into `GET /v1/stats/contracts`
