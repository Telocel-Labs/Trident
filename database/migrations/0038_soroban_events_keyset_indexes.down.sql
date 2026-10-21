-- Down migration for 0038_soroban_events_keyset_indexes.sql.

DROP INDEX IF EXISTS idx_soroban_events_keyset;
DROP INDEX IF EXISTS idx_soroban_events_contract_keyset;
DROP INDEX IF EXISTS idx_soroban_events_contract_topic1;

CREATE INDEX IF NOT EXISTS idx_soroban_events_network
    ON soroban_events (network);

CREATE INDEX IF NOT EXISTS idx_soroban_events_id_desc
    ON soroban_events (id DESC);
