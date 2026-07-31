-- Adds a `status` column to webhook_deliveries so dead-lettered deliveries
-- can be distinguished from in-progress ones and surfaced for operator replay.
-- `attempts` tracks total attempts at the delivery level (not per row).
--
-- Status lifecycle:
--   pending  → initial state when a delivery row is created
--   success  → delivery received a 2xx response
--   failed   → attempt failed; a retry will follow if under the cap
--   dead_lettered → max attempts exhausted with no success

ALTER TABLE webhook_deliveries
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'success', 'failed', 'dead_lettered'));

ALTER TABLE webhook_deliveries
    ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 1;

-- Partial index: only dead-lettered rows, since that's the query path for
-- the inspection/replay endpoint.
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_dead_lettered
    ON webhook_deliveries (subscription_id, delivered_at DESC)
    WHERE status = 'dead_lettered';
