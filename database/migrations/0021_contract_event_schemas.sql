-- Issue #272: persist per-contract event schemas so SDKs can fetch a typed,
-- versioned registry keyed by the contract code hash.

CREATE TABLE IF NOT EXISTS contract_event_schemas (
    id              BIGSERIAL PRIMARY KEY,
    contract_id     TEXT        NOT NULL,
    network         TEXT        NOT NULL,
    event_name      TEXT        NOT NULL,
    code_hash       TEXT        NOT NULL,
    field_schema    JSONB       NOT NULL,
    observed_source TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (contract_id, network, event_name, code_hash)
);

CREATE INDEX IF NOT EXISTS idx_contract_event_schemas_contract
    ON contract_event_schemas (contract_id, network, code_hash);

CREATE INDEX IF NOT EXISTS idx_contract_event_schemas_event_name
    ON contract_event_schemas (event_name);
