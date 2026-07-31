-- 0016: standardise created_at/updated_at + BEFORE UPDATE triggers (#253).
--
-- Audit of mutable tables:
--   system_state          — updated_at exists but was SET manually in queries.
--                           Trigger added; manual SETs remain compatible.
--   indexed_contracts     — created_at only; updated_at added + trigger.
--   api_keys              — created_at only; updated_at added + trigger.
--   webhook_subscriptions — created_at only; updated_at added + trigger.
--
-- Immutable tables skipped (no updated_at needed):
--   soroban_events   — append-only; events are never modified after indexing.
--   ledger_metadata  — append-only; one row per ledger, never mutated.
--   event_outbox     — only published/published_at columns change; the
--                      published_at column already captures that transition.
--   audit_log        — immutable audit trail by design.
--   parse_errors     — immutable error record.

-- Shared trigger function stamped on every UPDATE.
-- CREATE OR REPLACE is idempotent; safe to re-run.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

-- system_state -----------------------------------------------------------------
-- updated_at already exists; add trigger so writers no longer need to SET it
-- manually. Existing manual SETs still work but are now redundant.
DROP TRIGGER IF EXISTS trg_system_state_updated_at ON system_state;
CREATE TRIGGER trg_system_state_updated_at
    BEFORE UPDATE ON system_state
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- indexed_contracts ------------------------------------------------------------
ALTER TABLE indexed_contracts
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

DROP TRIGGER IF EXISTS trg_indexed_contracts_updated_at ON indexed_contracts;
CREATE TRIGGER trg_indexed_contracts_updated_at
    BEFORE UPDATE ON indexed_contracts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- api_keys --------------------------------------------------------------------
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

DROP TRIGGER IF EXISTS trg_api_keys_updated_at ON api_keys;
CREATE TRIGGER trg_api_keys_updated_at
    BEFORE UPDATE ON api_keys
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- webhook_subscriptions -------------------------------------------------------
ALTER TABLE webhook_subscriptions
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

DROP TRIGGER IF EXISTS trg_webhook_subscriptions_updated_at ON webhook_subscriptions;
CREATE TRIGGER trg_webhook_subscriptions_updated_at
    BEFORE UPDATE ON webhook_subscriptions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
