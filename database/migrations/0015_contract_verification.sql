-- Issue #273: persist source-verification status and build metadata so the
-- API can expose a verified flag and consumers can trust contract provenance.

CREATE TABLE IF NOT EXISTS contract_verification (
    id                 BIGSERIAL PRIMARY KEY,
    contract_id        TEXT        NOT NULL,
    network            TEXT        NOT NULL,
    status             TEXT        NOT NULL CHECK (status IN ('unverified', 'pending', 'verified', 'mismatch', 'failed')),
    on_chain_hash      TEXT        NOT NULL,
    source_hash        TEXT,
    repository_url     TEXT,
    commit_sha         TEXT,
    toolchain_version  TEXT,
    build_command      TEXT,
    wasm_path          TEXT,
    verified_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (contract_id, network)
);

CREATE INDEX IF NOT EXISTS idx_contract_verification_contract_id
    ON contract_verification (contract_id);

CREATE INDEX IF NOT EXISTS idx_contract_verification_status
    ON contract_verification (status);
