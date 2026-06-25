-- Migration 0002: add indexer health columns to system_state
-- Additive only — existing cursor row and all other consumers are unaffected.

ALTER TABLE system_state
    ADD COLUMN IF NOT EXISTS last_poll_at          TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_ledger_indexed   BIGINT,
    ADD COLUMN IF NOT EXISTS events_indexed_total  BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS events_in_last_poll   INT     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS poll_duration_ms      INT     NOT NULL DEFAULT 0;
