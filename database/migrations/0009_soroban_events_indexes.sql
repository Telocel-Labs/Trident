-- Composite indexes for high-cardinality query patterns on soroban_events.
-- Note: sqlx wraps each migration in a transaction, so CONCURRENTLY (which
-- cannot run inside a transaction block) is not usable here.

-- Index 1: contract + ledger range (the primary read pattern)
-- Used by: WHERE contract_id = AND ledger_sequence BETWEEN AND ORDER BY ledger_sequence DESC
CREATE INDEX IF NOT EXISTS idx_soroban_events_contract_ledger
  ON soroban_events (contract_id, ledger_sequence DESC);

-- Index 2: contract + topic filtering (partial index for non-null topics)
-- Used by: WHERE contract_id = AND topic_0 = (and is not null check)
CREATE INDEX IF NOT EXISTS idx_soroban_events_contract_topic0
  ON soroban_events (contract_id, topic_0)
  WHERE topic_0 IS NOT NULL;

-- Index 3: descending ID for cursor-based pagination
-- Used by: WHERE id < ORDER BY id DESC (for next-page queries)
CREATE INDEX IF NOT EXISTS idx_soroban_events_id_desc
  ON soroban_events (id DESC);

-- Index 4: timestamp for time-range queries (future analytics)
-- Used by: WHERE ledger_timestamp > AND ledger_timestamp < ORDER BY ledger_timestamp DESC
CREATE INDEX IF NOT EXISTS idx_soroban_events_ledger_timestamp
  ON soroban_events (ledger_timestamp DESC);
