-- Renumbered from 0019 to 0023 (issue #371). It was merged as a second 0019
-- alongside 0019_contract_stats_rollup, and sqlx derives a migration's version
-- from the filename prefix — so the pair collided on the primary key of
-- _sqlx_migrations and `sqlx migrate run` aborted for everyone.
--
-- Forward-only and safe to re-apply: every statement below is guarded with
-- IF NOT EXISTS, so a database that already applied this file under the old
-- 0019 version records 0023 and makes no schema changes. Nothing here depends
-- on 0019-0022, so the later position is not a behaviour change.
--
-- Issue #270: contract storage (ledger entry) snapshots for tracked
-- contracts. One row per observed change to a storage key — an append-only
-- change log with ledger provenance, not a single mutable "current value"
-- row, so historical values stay queryable.

CREATE TABLE IF NOT EXISTS contract_storage_snapshots (
    id              BIGSERIAL PRIMARY KEY,
    contract_id     TEXT        NOT NULL,
    network         TEXT        NOT NULL,
    storage_key     TEXT        NOT NULL,
    key_json        JSONB,
    value_json      JSONB,
    ledger_sequence BIGINT      NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (contract_id, network, storage_key, ledger_sequence)
);

CREATE INDEX IF NOT EXISTS idx_contract_storage_snapshots_latest
    ON contract_storage_snapshots (contract_id, network, storage_key, ledger_sequence DESC);
