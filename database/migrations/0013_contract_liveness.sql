-- contract_liveness (issue #271)
--
-- Tracks Soroban contract TTL / archival state for each monitored contract.
-- Populated by the indexer polling getLedgerEntries for the contract instance
-- key and comparing liveUntilLedger against the current ledger sequence.
--
-- One row per contract_id (upserted on each poll). `live_until_ledger` is NULL
-- when the contract has already been archived (entry absent from getLedgerEntries
-- response). `ledgers_until_archive` is 0 when archived, positive when live.
CREATE TABLE IF NOT EXISTS contract_liveness (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id             TEXT        NOT NULL,
    network                 TEXT        NOT NULL DEFAULT 'testnet',
    status                  TEXT        NOT NULL CHECK (status IN ('live', 'archived')),
    live_until_ledger       BIGINT,
    ledgers_until_archive   BIGINT,
    last_checked_ledger     BIGINT      NOT NULL,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT contract_liveness_contract_network_unique UNIQUE (contract_id, network)
);

CREATE INDEX IF NOT EXISTS contract_liveness_contract_id_idx
    ON contract_liveness (contract_id);

-- Partial index to efficiently query contracts nearing archival for alerting.
CREATE INDEX IF NOT EXISTS contract_liveness_near_archive_idx
    ON contract_liveness (ledgers_until_archive)
    WHERE status = 'live' AND ledgers_until_archive IS NOT NULL;
