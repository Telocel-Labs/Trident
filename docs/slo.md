# Service Level Objectives (SLOs)

Defines what "healthy" means for Trident's MVP, in measurable terms, so
alerting has principled thresholds instead of guessed ones (issue #296).

## Status of the underlying metrics

| SLO | Metrics it needs | Status |
|---|---|---|
| Ingest freshness | `trident_indexer_ledger_lag`, `trident_indexer_last_poll_timestamp_seconds` | **Live today** — see `crates/indexer/src/metrics.rs` |
| API latency | RED metrics (`http_requests_total`, `http_request_duration_seconds`) on `services/api` | **Not yet instrumented** — tracked by issue #295 |
| API availability | Same RED metrics as above | **Not yet instrumented** — tracked by issue #295 |

The API latency/availability PromQL below is the target contract for issue
#295's instrumentation — the metric names, label names (`route`,
`method`, `status_code`), and histogram bucket boundaries a RED
implementation needs to produce for these exact queries to work. Until #295
ships, those two SLOs are defined but not yet measurable; the ingest
freshness SLO is fully measurable today.

## SLO 1 — Ingest freshness

**Target:** p95 ledger lag (chain tip minus last-indexed ledger) stays under
30 seconds, measured over a rolling 28-day window.

**Why 30s:** the indexer polls on a fixed interval (`trident_indexer_effective_poll_interval_ms`,
typically single-digit seconds) — 30s gives headroom for a handful of
consecutive slow/retried polls (RPC failover, `trident_indexer_rpc_retries_total`)
without immediately breaching, while still catching a genuinely stalled
indexer quickly.

**Measuring query:** `trident_indexer_ledger_lag` (see
`crates/indexer/src/metrics.rs`) is recorded as a gauge, not a histogram, and
the indexer runs as a single replica (`values.yaml`: "Distributed cursor
management is not yet supported") — so there's no per-request distribution
to take a quantile *of*; the SLI is "fraction of time the gauge was within
target," using the standard good-ratio idiom for a single time series:

```promql
avg_over_time((trident_indexer_ledger_lag <= bool 30)[28d:1m])
```

(`<= bool 30` yields `1`/`0` per sample; averaging that over the 28-day
window is the fraction of time the objective held — directly comparable to
the 95% target.) For a live, right-now check instead of the rolling
28-day ratio:

```promql
trident_indexer_ledger_lag
```

**Error budget:** 30 days × (1 − 0.95) allowed-breach-time = 36 hours/month
where p95 lag may exceed 30s before the budget is exhausted.

**Public API contract:** the same lag figure (plus an estimated-seconds
conversion, `trident_indexer_ledger_lag_seconds_estimated`) is exposed
outside Prometheus too, as `lag_ledgers` / `lag_seconds_estimated` on
`GET /v1/stats/indexer` — see `docs/observability/data-freshness.md` for the
full public freshness contract (issue #294) and
`TridentIngestLagSustainedHigh` in `observability/burn-rate-alerts.yml` for a
direct threshold alert on sustained high lag, independent of the burn-rate
math below (issue #293).

**Dead-man's-switch (heartbeat) companion query** — catches a fully stalled
indexer, which a lag metric alone can miss if the indexer stops updating its
own gauge:

```promql
time() - trident_indexer_last_poll_timestamp_seconds > 120
```

## SLO 2 — API p95 latency (per route class)

**Target:** p95 request latency stays under 500ms for read routes
(`GET /v1/events`, `GET /v1/health`, etc.) and under 1s for write/heavier
routes (anything invoking gRPC + Postgres), measured over a rolling 28-day
window.

**Route classes:** partition by a `route` label carrying the route
*template* (e.g. `/v1/events`, not the literal request path with IDs
interpolated in) so cardinality stays bounded, plus a coarse `class` label
(`read` | `write`) if per-route budgets prove too granular to alert on
individually.

**Measuring query** (once #295 ships `http_request_duration_seconds` as a
histogram with `route` and `class` labels):

```promql
histogram_quantile(0.95,
  sum(rate(http_request_duration_seconds_bucket{class="read"}[5m])) by (le)
)
```

```promql
histogram_quantile(0.95,
  sum(rate(http_request_duration_seconds_bucket{class="write"}[5m])) by (le)
)
```

**Error budget:** 28 days × (1 − 0.95) = 33.6 hours where p95 may exceed
target before the budget is exhausted, per class.

## SLO 3 — API availability (success ratio)

**Target:** 99.5% of requests return a non-5xx status, measured over a
rolling 28-day window.

**Why not 99.9%:** this is a self-hosted MVP without HA Postgres/Redis by
default (see `docs/kubernetes.md` — those are BYO), so a single-AZ outage on
either dependency should be within budget rather than paging immediately.
Revisit once managed HA Postgres/Redis is the documented default.

**Measuring query** (once #295 ships `http_requests_total{status_code}`):

```promql
sum(rate(http_requests_total{status_code!~"5.."}[5m]))
/
sum(rate(http_requests_total[5m]))
```

**Error budget:** 28 days × (1 − 0.995) = 3.36 hours of unavailability
before the budget is exhausted.

## Burn-rate alerting

Alert on *burn rate* (how fast the error budget is being consumed), not on
the raw SLI crossing its target — a single 2-minute latency spike shouldn't
page anyone if it doesn't threaten the 28-day budget, but the same spike
sustained for an hour should. Uses the standard Google SRE multi-window
multi-burn-rate pattern: a short *and* a long window must both breach before
firing, so alerts are both fast (short window) and not flaky (long window
confirms it's sustained).

Rules live in `observability/burn-rate-alerts.yml` — see that file for the
actual PromQL and thresholds (2%/hour fast-burn, 5%/day slow-burn budget
consumption, standard SRE workbook multipliers). It only defines rules for
SLO 1 (ingest freshness) today, since that's the only SLO with live metrics;
SLO 2/3 rules are stubbed with a comment pointing at issue #295 and should be
filled in once those metrics exist.

Validate with `promtool check rules observability/burn-rate-alerts.yml`
(not run as part of this change — no `promtool` available in the environment
this was authored in; verify in CI or locally before merging).

## Review cadence

- **Monthly:** review actual burn-rate alert firings against real incidents —
  did they page for genuine user-impacting issues, or noise? Adjust
  thresholds if either.
- **Quarterly:** re-derive the error budget math and target numbers above
  against a quarter of real traffic/incident data once available — the
  current targets are initial estimates, not yet validated against
  production load.
- **On every new route added:** confirm it's classified into `read` or
  `write` for SLO 2's per-class budget (once #295 ships).
