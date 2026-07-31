-- contract_invocation_metrics (issue #266)
--
-- Per-invocation resource + fee metering for tracked contracts, sourced from
-- the Soroban RPC `getTransaction` call. `fee_charged` is the exact amount
-- charged (TransactionResult.feeCharged). The CPU/read/write fields are the
-- *declared* SorobanTransactionData resources from the transaction envelope —
-- the budget simulation computed and the submitter signed — not host-measured
-- actual usage, which the RPC only exposes via diagnostic events most public
-- nodes disable. `provenance` records this distinction so consumers never
-- mistake a declared limit for a measured one. See
-- docs/contract-invocation-metering.md.
--
-- One row per (contract_id, transaction_hash): a transaction that invokes
-- several tracked contracts gets one row per contract, all sharing the same
-- transaction-level fee/resource numbers.
CREATE TABLE IF NOT EXISTS contract_invocation_metrics (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id         TEXT        NOT NULL,
    network             TEXT        NOT NULL DEFAULT 'testnet',
    transaction_hash    TEXT        NOT NULL,
    ledger_sequence     BIGINT      NOT NULL,
    ledger_timestamp    TIMESTAMPTZ NOT NULL,
    fee_charged         BIGINT      NOT NULL,
    resource_fee        BIGINT,
    cpu_instructions    BIGINT,
    read_bytes          BIGINT,
    write_bytes         BIGINT,
    provenance          TEXT        NOT NULL DEFAULT 'declared_resources',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_contract_invocation_metrics UNIQUE (contract_id, transaction_hash)
);

-- Primary read pattern: one contract's invocation cost history, newest first.
CREATE INDEX IF NOT EXISTS idx_contract_invocation_metrics_contract_ledger
  ON contract_invocation_metrics (contract_id, ledger_sequence DESC);

-- Joining a metrics row back to the events it's associated with.
CREATE INDEX IF NOT EXISTS idx_contract_invocation_metrics_tx_hash
  ON contract_invocation_metrics (transaction_hash);
