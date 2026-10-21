-- ---------------------------------------------------------------------------
-- 0038_system_state_rpc_degraded_alert.down.sql
-- ---------------------------------------------------------------------------

ALTER TABLE system_state
    DROP COLUMN IF EXISTS rpc_degraded_last_alert_at,
    DROP COLUMN IF EXISTS rpc_degraded_fired;
