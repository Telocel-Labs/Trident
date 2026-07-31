-- Renumbered from 0018 to 0022 (issue #371). It was merged as a second 0018
-- alongside 0018_contract_specs, and sqlx derives a migration's version from
-- the filename prefix — so the pair collided on the primary key of
-- _sqlx_migrations and `sqlx migrate run` aborted for everyone.
--
-- Forward-only and safe to re-apply: every statement below is guarded with
-- IF NOT EXISTS, so a database that already applied this file under the old
-- 0018 version records 0022 and makes no schema changes. Nothing here depends
-- on 0018-0021, so the later position is not a behaviour change.
--
-- Issue #263: per-contract token metadata (name, symbol, decimals), resolved
-- via a read-only `simulateTransaction` call against name()/symbol()/decimals()
-- (SEP-41) and cached here so consumers can render amounts in human units
-- without re-simulating on every request.
--
-- `is_token = false` caches a negative result (the contract does not expose
-- the SEP-41 read interface) so the indexer does not re-simulate every poll
-- cycle for known non-token contracts; `name`/`symbol`/`decimals` stay NULL
-- in that case.
CREATE TABLE IF NOT EXISTS token_metadata (
    id          BIGSERIAL PRIMARY KEY,
    contract_id TEXT        NOT NULL,
    network     TEXT        NOT NULL,
    name        TEXT,
    symbol      TEXT,
    decimals    INTEGER,
    is_token    BOOLEAN     NOT NULL DEFAULT TRUE,
    resolved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (contract_id, network)
);

-- Serves the API's per-contract metadata lookup (GET /v1/contracts/{id}/metadata).
CREATE INDEX IF NOT EXISTS idx_token_metadata_contract ON token_metadata (contract_id, network);
