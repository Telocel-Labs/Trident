# Data freshness: ingest lag as a public contract

Consumers of the Trident API need to answer one question before trusting
anything else it returns: **is this data caught up with the chain?** This
document defines "freshness" precisely — the metric, the public API field,
and how the two relate — so the answer is a documented contract rather than
an implementation detail (issue #294).

## The two numbers

Freshness is expressed two ways, computed from the same underlying value:

1. **`lag_ledgers`** — the chain tip ledger sequence minus the last ledger
   the indexer has committed. The ground-truth figure; whole ledgers, exact.
2. **`lag_seconds_estimated`** — `lag_ledgers * ~5s` (Stellar's
   protocol-target ledger close time). An *estimate*, not a measurement: the
   indexer does not retain per-ledger close timestamps once a page is
   processed, so this is lag_ledgers converted to a human-scale "how stale,
   roughly, in wall-clock time" figure rather than a tracked rolling average.
   Treat it as an order-of-magnitude signal, not a precise SLA number — use
   `lag_ledgers` if you need exactness.

Both numbers are null together whenever the chain tip is unknown (RPC lookup
failed — see below) or the indexer has not indexed anything yet.

## Where each number lives

| Surface | Fields | Notes |
|---|---|---|
| Indexer Prometheus metrics (`crates/indexer/src/metrics.rs`, port 9090) | `trident_indexer_ledger_lag`, `trident_indexer_ledger_lag_seconds_estimated` | Set together by `metrics::set_ledger_lag`, computed every poll cycle from the RPC's `latestLedger` vs. the indexer's own cursor — no separate chain-tip lookup needed. |
| API service Prometheus metrics (`services/api/handlers/stats.go`, `GET /metrics`) | `trident_indexer_lag_ledgers`, `trident_indexer_lag_seconds_estimated` | Set on each `GET /v1/stats/indexer` request from the same computation the JSON response uses (see below) — not scraped independently from the indexer process. |
| Public REST API | `GET /v1/stats/indexer` → `lag_ledgers`, `lag_seconds_estimated`, `last_ledger_indexed`, `chain_tip_ledger`, `status` | The stable, documented contract external consumers should build against — see `api/openapi.yaml`. No API key required. |

The indexer's own gauge and the API's `/v1/stats/indexer` figure are computed
independently (the indexer measures lag against the RPC page it just
processed; the API measures it against a cached, separately-fetched chain
tip) and can disagree briefly — by design, since the API's view is what
external consumers see and must not depend on the indexer's internal metrics
port being reachable.

**Why `~5s` and not a measured average:** Stellar's protocol targets a
constant ~5 second ledger close time by design (unlike, say, Bitcoin's
variable block time), so a fixed constant is a reasonable estimate without
the complexity of tracking a rolling average. `AVG_LEDGER_CLOSE_SECONDS` in
`crates/indexer/src/metrics.rs` and `avgLedgerCloseSeconds` in
`services/api/handlers/stats.go` must be kept in sync if this ever changes.

## `status` semantics

`GET /v1/stats/indexer`'s `status` field is the coarse, three-value summary:

| `status` | Condition | HTTP status |
|---|---|---|
| `healthy` | Last poll within 60s and lag_ledgers ≤ 10 (or unknown) | 200 |
| `lagging` | Last poll within 60s but lag_ledgers > 10 | 200 |
| `stalled` | No successful poll in the last 60s | 503 |

`stalled` is the only case that returns a non-2xx status — `lagging` is still
a 200 because the data is real, just behind, which is a materially different
situation for a consumer than "the indexer isn't running."

## Alerting

See `docs/slo.md` (SLO 1 — Ingest freshness) for the burn-rate-based alerting
built on `trident_indexer_ledger_lag`, and `observability/burn-rate-alerts.yml`
for the rule definitions, including `TridentIngestLagSustainedHigh`, a direct
threshold alert on sustained high lag independent of the SLO error-budget
calculation (issue #293).
