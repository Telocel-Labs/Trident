-- API key management table (issues #138, #139).
-- Only the SHA-256 hash of each key is stored — the plaintext is never written
-- to the database or logs. The raw key is returned exactly once at creation.

CREATE TABLE IF NOT EXISTS api_keys (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash         TEXT        NOT NULL UNIQUE,   -- SHA-256 hex of full key
    key_prefix       TEXT        NOT NULL,           -- first 16 chars of plaintext key (for display)
    label            TEXT        NOT NULL DEFAULT '',
    network          TEXT        NOT NULL DEFAULT 'mainnet',
    rate_limit_tier  TEXT        NOT NULL DEFAULT 'standard',
    last_used_at     TIMESTAMPTZ,
    request_count    BIGINT      NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys (key_hash);
