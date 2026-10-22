-- ---------------------------------------------------------------------------
-- 0038_system_state_rpc_degraded_alert.sql
--
-- Persist RPC-degraded alerting state in system_state (issue #632).
--
-- Preserves cooldown timestamps and resolved state across restarts and poll
-- iterations so RPC-degraded alerts do not spam continuously and emit recovery
-- notifications properly when healthy.
-- ---------------------------------------------------------------------------

ALTER TABLE system_state
    ADD COLUMN IF NOT EXISTS rpc_degraded_last_alert_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS rpc_degraded_fired         BOOLEAN NOT NULL DEFAULT FALSE;
