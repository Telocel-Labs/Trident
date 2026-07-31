-- ---------------------------------------------------------------------------
-- token_events (issue #211)
--
-- Normalised projection of the standard SEP-41 / Stellar-Asset-Contract token
-- events. soroban_events keeps the generic positional payload; this table gives
-- transfer analytics typed, indexable columns without re-parsing JSON per query.
--
-- amount is TEXT, not NUMERIC or BIGINT: token amounts are i128 and must round
-- trip exactly through the API (issue #210). Queries needing arithmetic can
-- cast to NUMERIC at read time.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS token_events (
    -- Shares the primary key of the soroban_events row it projects, so the
    -- projection inherits that row's replay-idempotency for free.
    event_id            UUID        PRIMARY KEY
                                    REFERENCES soroban_events (id) ON DELETE CASCADE,
    contract_id         TEXT        NOT NULL,
    network             TEXT        NOT NULL DEFAULT 'testnet',
    event_type          TEXT        NOT NULL
                                    CHECK (event_type IN ('transfer', 'mint', 'burn', 'clawback', 'approve')),
    from_address        TEXT,
    to_address          TEXT,
    spender_address     TEXT,
    admin_address       TEXT,
    amount              TEXT,
    expiration_ledger   BIGINT,
    ledger_sequence     BIGINT      NOT NULL,
    ledger_timestamp    TIMESTAMPTZ NOT NULL,
    transaction_hash    TEXT        NOT NULL,
    event_index         INTEGER     NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Primary read pattern: one token's activity, newest first.
CREATE INDEX IF NOT EXISTS idx_token_events_contract_ledger
  ON token_events (contract_id, ledger_sequence DESC);

-- "What did this account send / receive" — the two account-centric lookups.
CREATE INDEX IF NOT EXISTS idx_token_events_from
  ON token_events (from_address, ledger_sequence DESC)
  WHERE from_address IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_token_events_to
  ON token_events (to_address, ledger_sequence DESC)
  WHERE to_address IS NOT NULL;

-- Narrowing a contract's activity to one event kind (e.g. mints only).
CREATE INDEX IF NOT EXISTS idx_token_events_type
  ON token_events (event_type, ledger_sequence DESC);

-- Joining a projection row back to its originating transaction.
CREATE INDEX IF NOT EXISTS idx_token_events_tx_hash
  ON token_events (transaction_hash);
