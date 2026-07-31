-- Issue #257: maintained rollup of per-contract event activity so
-- GET /v1/stats/contracts can read a small pre-aggregated table instead of
-- paying a live GROUP BY over soroban_events on every Redis cache miss.
--
-- Refresh strategy: services/api periodically recomputes this table in full
-- from soroban_events (see RefreshContractStatsRollup / the ticker started in
-- main.go) rather than maintaining it incrementally on ingest, so it stays
-- independent of the indexer's write path. Freshness guarantee: a row is at
-- most one refresh interval stale; `refreshed_at` lets callers see exactly
-- how stale. The endpoint falls back to a live aggregate for any request the
-- rollup cannot answer (a custom ledger range, or before the first refresh
-- has populated a network).
CREATE TABLE IF NOT EXISTS contract_stats_rollup (
    contract_id            TEXT        NOT NULL,
    network                TEXT        NOT NULL,
    event_count            BIGINT      NOT NULL DEFAULT 0,
    contract_event_count   BIGINT      NOT NULL DEFAULT 0,
    system_event_count     BIGINT      NOT NULL DEFAULT 0,
    diagnostic_event_count BIGINT      NOT NULL DEFAULT 0,
    first_seen_ledger      BIGINT      NOT NULL,
    last_seen_ledger       BIGINT      NOT NULL,
    last_seen_at           TIMESTAMPTZ NOT NULL,
    refreshed_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (contract_id, network)
);

-- Serves the endpoint's default sort (event_count DESC) scoped to a network.
CREATE INDEX IF NOT EXISTS idx_contract_stats_rollup_network_count
    ON contract_stats_rollup (network, event_count DESC);
