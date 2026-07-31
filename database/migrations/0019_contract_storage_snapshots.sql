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
