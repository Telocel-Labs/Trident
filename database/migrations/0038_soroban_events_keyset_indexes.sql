-- Migration 0038: align soroban_events indexes with keyset pagination (#795).
--
-- Why this migration exists
-- -------------------------
-- ListEvents (crates/api/src/services/events.rs) filters by `network` (always),
-- optionally `contract_id`, `topic_0`, `topic_1` and a ledger range, and orders
-- by `(ledger_sequence ASC, event_index ASC)` with a keyset predicate on the
-- same pair: `WHERE (ledger_sequence, event_index) > ($cur_seq, $cur_idx)`.
--
-- Prior to this migration, soroban_events had:
--   - (network)                                   -- low-selectivity (2-4 distinct values), unusable for ordering
--   - (network, contract_id)                      -- unusable for keyset ordering without sorting in-memory
--   - (contract_id, ledger_sequence DESC)         -- descending, missing event_index
--   - (contract_id, topic_0)                      -- partial index for topic_0, but no index for topic_1
--   - (id DESC)                                   -- obsolete since issue #603 replaced id-based cursors
--   - (ledger_timestamp DESC)
--
-- Consequently:
--   1. Keyset queries without contract_id filter performed an in-memory Sort node over matched rows.
--   2. Keyset queries with contract_id filter could not execute an index-driven forward scan.
--   3. Queries filtering on topic_1 required sequential scans or post-filters.
--   4. Low-selectivity indexes `idx_soroban_events_network` and `idx_soroban_events_id_desc` added
--      unnecessary write overhead on every ingested event across all partitions.
--
-- Changes:
--   1. CREATE INDEX idx_soroban_events_keyset ON soroban_events (network, ledger_sequence, event_index);
--   2. CREATE INDEX idx_soroban_events_contract_keyset ON soroban_events (network, contract_id, ledger_sequence, event_index);
--   3. CREATE INDEX idx_soroban_events_contract_topic1 ON soroban_events (contract_id, topic_1) WHERE topic_1 IS NOT NULL;
--   4. DROP INDEX IF EXISTS idx_soroban_events_network;
--   5. DROP INDEX IF EXISTS idx_soroban_events_id_desc;
--
-- lint:allow-long-lock  sqlx wraps each migration in a transaction, and
--   CREATE INDEX CONCURRENTLY cannot run inside a transaction block (#642).
--   soroban_events is range-partitioned, so creating indexes on the parent
--   table propagates to all child partitions. Run during a maintenance window
--   or pre-create concurrently in production.

CREATE INDEX IF NOT EXISTS idx_soroban_events_keyset
    ON soroban_events (network, ledger_sequence, event_index);

CREATE INDEX IF NOT EXISTS idx_soroban_events_contract_keyset
    ON soroban_events (network, contract_id, ledger_sequence, event_index);

CREATE INDEX IF NOT EXISTS idx_soroban_events_contract_topic1
    ON soroban_events (contract_id, topic_1)
    WHERE topic_1 IS NOT NULL;

DROP INDEX IF EXISTS idx_soroban_events_network;

DROP INDEX IF EXISTS idx_soroban_events_id_desc;
