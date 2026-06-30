-- CI smoke-test seed data.
-- Inserts a pre-computed test API key so the smoke test can authenticate
-- against GET /v1/events without needing to call POST /v1/api-keys first.
--
-- Raw key    : ci-test-api-key-do-not-use-in-production
-- Key prefix : ci-test-api-ke
-- key_hash is the SHA-256 hex of the raw key (NOT HMAC — the DB stores a
-- plain SHA-256 digest used by the admin handler for lookup display only;
-- the auth middleware uses HMAC-SHA256 keyed on API_KEY_SALT, which is
-- pre-computed and stored in .env.ci as API_KEY_HASHES).
--
-- DO NOT use this key or this salt in any environment other than CI.

INSERT INTO api_keys (id, key_hash, key_prefix, label, network, rate_limit_tier)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    encode(sha256('ci-test-api-key-do-not-use-in-production'::bytea), 'hex'),
    'ci-test-api-ke',
    'CI smoke-test key',
    'testnet',
    'standard'
)
ON CONFLICT DO NOTHING;
