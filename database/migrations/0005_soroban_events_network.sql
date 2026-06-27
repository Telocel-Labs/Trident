-- Migration 0005: add network column to soroban_events
-- Enables multi-network indexing and network-scoped analytics queries (#65, analytics endpoints)

-- Add network column with default 'testnet' for backward compatibility
ALTER TABLE soroban_events
    ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'testnet';

-- Create indexes for network filtering and analytics
-- Single network index for basic network filtering
CREATE INDEX IF NOT EXISTS idx_soroban_events_network ON soroban_events (network);

-- Compound index for analytics: (network, contract_id, ledger_sequence DESC)
-- This index is critical for the stats/contracts query performance (#61 equivalent)
-- Allows efficient GROUP BY contract_id within a network and ledger range
CREATE INDEX IF NOT EXISTS idx_soroban_events_network_contract_ledger 
    ON soroban_events (network, contract_id, ledger_sequence DESC);
