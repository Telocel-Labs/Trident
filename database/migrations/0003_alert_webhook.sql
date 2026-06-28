-- Migration 0003: add alerting state columns to system_state (issue #75)
-- Additive only — existing rows and all other consumers are unaffected.
ALTER TABLE system_state
    ADD COLUMN IF NOT EXISTS last_alert_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS alert_fired     BOOLEAN NOT NULL DEFAULT FALSE;
