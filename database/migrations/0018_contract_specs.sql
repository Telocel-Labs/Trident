-- Issue #260: persist each tracked contract's parsed spec (contractmeta /
-- SEP-48 contractspecv0 entries — functions, keyed by code hash so a
-- redeploy with new code refreshes the row instead of accumulating stale ones.
-- Issue #269: interface detection tags (contract_type/interfaces) derived
-- from the parsed spec's function signatures ride alongside it — they are
-- recomputed together whenever the code hash changes.

CREATE TABLE IF NOT EXISTS contract_specs (
    id             BIGSERIAL PRIMARY KEY,
    contract_id    TEXT        NOT NULL,
    network        TEXT        NOT NULL,
    code_hash      TEXT        NOT NULL,
    has_spec       BOOLEAN     NOT NULL DEFAULT FALSE,
    functions      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    contract_type  TEXT        NOT NULL DEFAULT 'unknown',
    interfaces     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    fetched_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (contract_id, network)
);

CREATE INDEX IF NOT EXISTS idx_contract_specs_code_hash
    ON contract_specs (code_hash);

CREATE INDEX IF NOT EXISTS idx_contract_specs_contract_type
    ON contract_specs (contract_type);
