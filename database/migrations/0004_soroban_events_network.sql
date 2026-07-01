-- Add network column to soroban_events for per-network data isolation.
-- Existing rows default to 'testnet' to match the initial deployment target;
-- adjust via a backfill if mainnet data was already ingested.

ALTER TABLE soroban_events
    ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'testnet';

CREATE INDEX IF NOT EXISTS idx_soroban_events_network ON soroban_events (network);

-- Composite index for the most common authenticated query pattern:
-- events filtered by network + contract.
CREATE INDEX IF NOT EXISTS idx_soroban_events_network_contract
    ON soroban_events (network, contract_id);
