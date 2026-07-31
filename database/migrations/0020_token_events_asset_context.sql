-- ---------------------------------------------------------------------------
-- token_events asset context (issue #262)
--
-- Stellar Asset Contract (SAC) instances are implicitly-deployed wrappers
-- around classic assets (code + issuer, or native XLM). Their contract id is
-- fully determined by the asset and network, so the indexer can recognise a
-- configured allowlist of tracked assets and attach the asset identity to
-- their transfer/mint/burn/clawback/approve events.
--
-- asset_code/asset_issuer are populated only when the emitting contract_id
-- matches a recognised, operator-configured SAC. They stay NULL for every
-- other token-interface contract: an arbitrary custom token contract is not
-- a SAC and must never be misattributed to a classic asset.
-- ---------------------------------------------------------------------------
ALTER TABLE token_events
    ADD COLUMN asset_code   TEXT,
    ADD COLUMN asset_issuer TEXT;

-- "What happened to this asset" — the asset-centric activity lookup.
CREATE INDEX IF NOT EXISTS idx_token_events_asset_code
  ON token_events (asset_code, ledger_sequence DESC)
  WHERE asset_code IS NOT NULL;
