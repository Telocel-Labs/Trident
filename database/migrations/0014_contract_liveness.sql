-- Issue #271: track TTL / liveUntilLedger for indexed contracts so consumers
-- can surface live-vs-archived status and operators can alert before archival.

CREATE TABLE IF NOT EXISTS contract_liveness (
    id                    BIGSERIAL PRIMARY KEY,
    contract_id           TEXT        NOT NULL,
    network               TEXT        NOT NULL,
    status                TEXT        NOT NULL CHECK (status IN ('live', 'archived')),
    live_until_ledger     BIGINT,
    ledgers_until_archive BIGINT,
    last_checked_ledger   BIGINT      NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (contract_id, network)
);

-- Fast lookup for contracts approaching archival (used by the alert metric).
CREATE INDEX IF NOT EXISTS idx_contract_liveness_near_archival
    ON contract_liveness (ledgers_until_archive)
    WHERE status = 'live';
