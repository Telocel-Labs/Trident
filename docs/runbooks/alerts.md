# Alert runbook

One section per alert in [`monitoring/alerts.yml`](../../monitoring/alerts.yml).
Each section covers what the alert means, why its threshold was picked, and
the first steps to take when it fires. See
[`docs/metrics-catalog.md`](../metrics-catalog.md) for what every metric
referenced here actually measures.

## TridentIndexerLagWarning

**Means:** the indexer is more than 200 ledgers behind the Stellar chain tip,
sustained for 10 minutes.

**Why this threshold:** 200 ledgers is the same default as
`ALERT_LAG_THRESHOLD`, the indexer's own app-level webhook alert (issue #75)
— reusing it keeps the two alerting paths (webhook + Prometheus) agreeing on
what "behind" means. The 10-minute `for` absorbs normal RPC jitter and brief
upstream slowdowns without paging.

**First steps:**
1. Check `trident_indexer_rpc_request_duration_seconds` and
   `trident_indexer_rpc_errors_total` — is the Stellar RPC node slow or
   erroring? (see `TridentIndexerRPCErrorRateHigh` below)
2. Check `trident_indexer_db_pool_size`/`_idle_connections` — is the
   indexer's own Postgres write path the bottleneck?
3. If both look healthy, check indexer logs for retries/backoff — the RPC
   node may be silently rate-limiting.

## TridentIndexerLagCritical

**Means:** the indexer is more than 1000 ledgers behind, sustained for 5
minutes — API consumers are now reading meaningfully stale data.

**Why this threshold:** 1000 ledgers (~1.5 hours of Stellar ledger time) is
well past "temporarily slow" and into "effectively stalled." The shorter
5-minute window reflects the higher severity — page sooner once lag is this
large.

**First steps:** same as `TridentIndexerLagWarning`, but treat as
page-worthy immediately; also check `TridentIndexerHeartbeatStale` — a lag
this large with a stale heartbeat means the poll loop itself is hung, not
just slow.

## TridentIndexerHeartbeatStale

**Means:** `trident_indexer_heartbeat_timestamp_seconds` — updated once per
poll-loop iteration regardless of outcome — hasn't advanced in over 5
minutes.

**Why this threshold:** the heartbeat ticks once per loop iteration
(typically every `POLL_INTERVAL_MS`, default 1s, capped at 60s). 5 minutes is
a large multiple of even the slowest configured poll interval, so staleness
past that point means the loop itself is hung — deadlocked, panicked past a
supervisor's catch, or blocked on I/O that will never return — not just
running slowly.

**First steps:**
1. Check indexer process status / restart count (`kubectl get pods` or
   `docker compose ps`) — has it actually crashed and is failing to restart?
2. If the process is running but heartbeat is stale, capture a stack
   dump/profile if possible before restarting — this is the "hung but alive"
   case the dead-man's-switch exists to catch.
3. Restart the indexer; the cursor is persisted in `system_state`, so a
   restart resumes safely without reprocessing or data loss.

## TridentIndexerMetricsMissing

**Means:** no `trident_indexer_heartbeat_timestamp_seconds` series exists at
all — Prometheus can't find the metric, as opposed to finding it stale.

**Why this threshold:** distinguishes "the indexer is emitting metrics but
hung" (`TridentIndexerHeartbeatStale`) from "the indexer's `/metrics` isn't
scrapeable at all" (crashed, network partition, misconfigured scrape target).
3 minutes gives one scrape-interval's worth of margin before treating it as
real.

**First steps:** check the indexer process is running and `/metrics` is
reachable from Prometheus's network (`curl http://indexer:9090/metrics`);
check Prometheus's Targets page for scrape errors on the `trident-indexer`
job.

## TridentIndexerProcessDown

**Means:** Prometheus's own `up{job="trident-indexer"}` is 0 — the scrape
itself is failing (connection refused/timeout), for 2 minutes.

**Why this threshold:** `up` is the standard, library-level signal for "this
target isn't reachable at all." 2 minutes covers one or two missed scrapes
without paging on a single transient network blip.

**First steps:** same as `TridentIndexerMetricsMissing` — this is usually the
same underlying problem (process down or network partition) observed from
Prometheus's scrape health instead of the metric's own staleness.

## TridentIndexerParseErrorRateHigh

**Means:** over 1% of events in the last 10 minutes failed XDR decoding and
were written to `parse_errors` (parse-error isolation) instead of being
indexed.

**Why this threshold:** a small, steady trickle of parse errors is expected
(unusual contract event shapes, XDR edge cases) and shouldn't page. 1%
sustained for 15 minutes indicates something systemic — e.g. an RPC/XDR
schema change the parser doesn't handle — rather than a handful of one-off
malformed events.

**First steps:**
1. Query `parse_errors` for the most recent rows and inspect `raw_payload` /
   `error_message` for a common pattern.
2. Check whether a Stellar protocol upgrade or RPC node version bump
   coincides with the spike.
3. If it's a new, valid event shape, this is a parser bug — file/fix rather
   than treating it as transient.

## TridentIndexerRPCErrorRateHigh

**Means:** over 5% of Stellar RPC calls (`getEvents`/`getLedgers`) errored in
the last 5 minutes (issue #297).

**Why this threshold:** RPC nodes occasionally return isolated errors under
load; 5% sustained for 10 minutes is well above normal noise and usually
means the upstream node is degraded or rate-limiting.

**First steps:**
1. Check `trident_indexer_rpc_request_duration_seconds` for the same method
   — is latency also elevated (overload) or normal (outright rejections)?
2. Check the RPC provider's status page / try a manual `getHealth` call
   against `STELLAR_RPC_URL`.
3. If a fallback/alternate RPC endpoint is configured, consider failing over.

## TridentIndexerRPCErrorRateCritical

**Means:** over 25% of Stellar RPC calls errored in the last 5 minutes.

**Why this threshold:** at this error rate the indexer is very likely
effectively stalled (most poll cycles failing outright). Page immediately
rather than waiting the full 10-minute window used for the warning tier.

**First steps:** same as `TridentIndexerRPCErrorRateHigh`, treated as
immediate: fail over to an alternate RPC endpoint if one exists, or escalate
to the RPC provider.

## TridentAPIHTTP5xxRateHigh

**Means:** over 5% of Go API requests returned a 5xx in the last 5 minutes,
sustained for 10 minutes.

**Why this threshold:** individual 5xx responses happen (a single bad
request, a momentary DB blip); 5% sustained is a real, ongoing failure mode
rather than noise.

**First steps:**
1. Check `trident_api_db_pool_acquired_connections` /
   `trident_api_db_pool_max_connections` — is the pool saturated?
2. Check `TridentAPIDependencyUnhealthy` — is Postgres/Redis/gRPC down?
3. Break down by `route` (`trident_api_http_requests_total{status=~"5..",...}`)
   to see if it's isolated to one endpoint or API-wide.

## TridentAPIHTTP5xxRateCritical

**Means:** over 25% of Go API requests are failing with a 5xx, sustained for
5 minutes — the API is largely unusable.

**Why this threshold:** at 25%+ the service is failing for a meaningful
fraction of all callers; the shorter window pages faster than the warning
tier.

**First steps:** same as `TridentAPIHTTP5xxRateHigh`, treated as immediate —
check dependency health first since a downed Postgres/Redis is the most
common cause of an API-wide 5xx spike this large.

## TridentAPIProcessDown

**Means:** `up{job="trident-api"}` is 0 for 2 minutes — Prometheus can't
scrape the Go API at all.

**Why this threshold:** same reasoning as `TridentIndexerProcessDown` — `up`
is the standard scrape-health signal, 2 minutes covers a missed scrape or
two without paging on a blip.

**First steps:** check process status and container/pod logs; check
`readinessProbe`/`livenessProbe` results if running under Kubernetes (see
`helm/trident/values.yaml`).

## TridentAPIDependencyUnhealthy

**Means:** `GET /v1/health` has been returning non-200 for 5 minutes. That
endpoint checks Postgres, Redis, and the gRPC backend concurrently and fails
if any one of them does.

**Why this threshold:** relies on the Helm chart's liveness/readiness probes
(or an equivalent external health check) polling `/v1/health` every ~10s to
keep `trident_api_http_requests_total{route="GET /v1/health"}` moving; 5
minutes gives ample margin over that polling interval before treating a
failure as sustained rather than a single flaky check. If you deploy without
those probes (e.g. bare `docker compose`), add an equivalent periodic health
check — otherwise this alert has no samples to evaluate.

**First steps:**
1. `curl` `/v1/health` directly and read the `checks` field
   (`postgres`/`redis`/`grpc_api`) to see which dependency is failing.
2. If Postgres: check `TridentAPIDBPoolSaturated` and the database's own
   health/connection count.
3. If Redis: check Redis process health and `trident_api_redis_stream_length`
   for a consumer that's stopped reading.

## TridentAPIDBPoolSaturated

**Means:** over 90% of the Go API's Postgres connection pool has been
checked out for 10 minutes.

**Why this threshold:** 90% is a leading indicator — the pool isn't
exhausted yet, but requests will start queuing (and eventually timing out on
acquire) once it is. 10 minutes filters out brief bursts that a healthy pool
absorbs on its own.

**First steps:**
1. Check for slow queries or a query holding connections open longer than
   expected (`pg_stat_activity` on the database).
2. Check request volume — is this organic traffic growth that needs a larger
   `GO_API_DB_POOL_SIZE`, or a leak/regression?
3. As a mitigation, `GO_API_DB_POOL_SIZE` can be raised without a code
   change, but treat it as a stopgap if the root cause is a query regression.
