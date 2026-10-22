-- Down migration for 0039_webhook_delivery_idempotency_and_retry_state.sql.
DROP INDEX IF EXISTS idx_webhook_deliveries_pending_retry;
DROP INDEX IF EXISTS uq_webhook_deliveries_idempotency;

ALTER TABLE webhook_deliveries
    DROP COLUMN IF EXISTS next_attempt_at;
