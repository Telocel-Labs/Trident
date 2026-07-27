-- Migration: 0013_well_known_sac_seeds
-- ---------------------------------------------------------------------------
-- Adds a seeding-audit table that records which well-known SAC contract ids
-- were seeded into indexed_contracts on startup, and when.
-- This lets operators inspect what was auto-registered and by whom, and
-- supports idempotent re-seeding (the unique constraint blocks double-inserts).
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS sac_seed_log (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id TEXT        NOT NULL,
    network     TEXT        NOT NULL,
    label       TEXT,
    seeded_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_sac_seed_log_contract_network UNIQUE (contract_id, network)
);

CREATE INDEX IF NOT EXISTS idx_sac_seed_log_network ON sac_seed_log (network);

COMMENT ON TABLE sac_seed_log IS
  'Audit log of well-known SAC contract ids that were auto-seeded into indexed_contracts on indexer startup.';
