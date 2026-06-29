-- Migration 0004: add network column to soroban_events
-- Adds network filtering capability for multi-network support (issue #65).
-- Defaults to 'testnet' for backwards compatibility with existing data.

ALTER TABLE soroban_events
    ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'testnet';

-- Create index for efficient network-based filtering
CREATE INDEX IF NOT EXISTS idx_soroban_events_network ON soroban_events (network);

-- Create compound index for analytics queries (contract_id, network)
CREATE INDEX IF NOT EXISTS idx_soroban_events_contract_network ON soroban_events (contract_id, network);
