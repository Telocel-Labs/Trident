CREATE EXTENSION IF NOT EXISTS \
pgcrypto\;

-- ---------------------------------------------------------------------------
-- soroban_events
-- Primary store for every indexed Soroban contract event.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS soroban_events (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id         TEXT        NOT NULL,
    ledger_sequence     BIGINT      NOT NULL,
    ledger_timestamp    TIMESTAMPTZ NOT NULL,
    transaction_hash    TEXT        NOT NULL,
    event_index         INTEGER     NOT NULL,
    event_type          TEXT        NOT NULL CHECK (event_type IN ('contract', 'system', 'diagnostic')),
    topics              JSONB       NOT NULL DEFAULT '[]',
    topic_0             TEXT        GENERATED ALWAYS AS (topics ->> 0) STORED,
    topic_1             TEXT        GENERATED ALWAYS AS (topics ->> 1) STORED,
    data                JSONB       NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- system_state
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS system_state (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO system_state (key, value)
VALUES ('latest_ledger_cursor', '0')
ON CONFLICT (key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- system_state
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS system_state (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO system_state (key, value)
VALUES ('latest_ledger_cursor', '0')
ON CONFLICT (key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- indexed_contracts
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS indexed_contracts (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     TEXT        NOT NULL,
    network         TEXT,
    label           TEXT,
    index_from      BIGINT      NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_indexed_contracts_id_network UNIQUE (contract_id, network)
);

CREATE INDEX IF NOT EXISTS idx_indexed_contracts_contract_id ON indexed_contracts (contract_id);

-- ---------------------------------------------------------------------------
-- ledger_metadata
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS ledger_metadata (
    ledger_sequence     BIGINT      PRIMARY KEY,
    ledger_hash         TEXT        NOT NULL,
    ledger_timestamp    TIMESTAMPTZ NOT NULL,
    event_count         INTEGER     NOT NULL DEFAULT 0,
    processed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ledger_metadata_timestamp ON ledger_metadata (ledger_timestamp);
