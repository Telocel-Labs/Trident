-- Per-API-key usage rollup for billing/usage-limit readiness.
--
-- audit_log records every request but is retained only 90 days and is
-- expensive to scan on every usage lookup. usage_rollup aggregates it into
-- one row per (api_key_id, day), giving O(days) storage per key instead of
-- O(requests) and making usage lookups a simple indexed range scan.
--
-- Populated by a periodic background job (see services/api/handlers/usage.go
-- RollupUsage) that re-aggregates recent audit_log buckets on an upsert basis,
-- so it stays correct even if a run is skipped or overlaps the previous one.

CREATE TABLE IF NOT EXISTS usage_rollup (
    api_key_id      UUID             NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    period_start    TIMESTAMPTZ      NOT NULL, -- start of the UTC day bucket (inclusive)
    period_end      TIMESTAMPTZ      NOT NULL, -- end of the UTC day bucket (exclusive)
    request_count   BIGINT           NOT NULL DEFAULT 0,
    error_count     BIGINT           NOT NULL DEFAULT 0, -- status_code >= 400
    avg_duration_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    PRIMARY KEY (api_key_id, period_start)
);

-- Range queries for a single key over a period (self-serve + admin lookups).
CREATE INDEX IF NOT EXISTS idx_usage_rollup_key_period ON usage_rollup (api_key_id, period_start DESC);
