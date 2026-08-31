-- Constrain `network` to the known Stellar network set across every table
-- that carries it, so a typo (e.g. 'tesnet') can no longer create a silent,
-- invisible data partition that auth and queries never see (issue #252).
--
-- Allowed values: 'mainnet', 'testnet'. This mirrors the set already
-- enforced at the Go API layer (services/api/validation/events.go,
-- validNetworks) -- the SDK's Network enum (sdk/rust/src/types.rs) additionally
-- exposes 'futurenet' for client-side configuration, but no backend surface
-- (this API or the indexer) accepts it today, so it stays out of the
-- constraint until a real write path for it exists.
-- 'pubnet' is a name Stellar tooling sometimes uses for the same network as
-- 'mainnet' (see default_network_passphrase in crates/indexer/src/config.rs)
-- so any existing 'pubnet' rows are normalised to 'mainnet' before the
-- constraint is added, rather than being rejected.
--
-- Constraints are added NOT VALID + validated in a separate statement so
-- adding them does not take a long-lived lock scanning the whole table
-- up front (soroban_events and token_events are large/partitioned) --
-- lint:allow-long-lock NOT VALID defers the table scan to VALIDATE
-- CONSTRAINT, which only takes a SHARE UPDATE EXCLUSIVE lock (issue #252).

-- ---------------------------------------------------------------------------
-- Normalise legacy/alias values before constraining.
-- ---------------------------------------------------------------------------
UPDATE soroban_events              SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE indexed_contracts           SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE api_keys                    SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE audit_log                   SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE webhook_subscriptions       SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE token_events                SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE contract_invocation_metrics SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE contract_liveness           SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE contract_verification       SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE contract_specs              SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE contract_stats_rollup       SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE contract_event_schemas      SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE token_metadata              SET network = 'mainnet' WHERE network = 'pubnet';
UPDATE contract_storage_snapshots  SET network = 'mainnet' WHERE network = 'pubnet';

-- ---------------------------------------------------------------------------
-- NOT NULL `network` columns.
-- ---------------------------------------------------------------------------
ALTER TABLE soroban_events
    ADD CONSTRAINT chk_soroban_events_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT chk_webhook_subscriptions_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE token_events
    ADD CONSTRAINT chk_token_events_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE contract_invocation_metrics
    ADD CONSTRAINT chk_contract_invocation_metrics_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE contract_liveness
    ADD CONSTRAINT chk_contract_liveness_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE contract_verification
    ADD CONSTRAINT chk_contract_verification_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE contract_specs
    ADD CONSTRAINT chk_contract_specs_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE contract_stats_rollup
    ADD CONSTRAINT chk_contract_stats_rollup_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE contract_event_schemas
    ADD CONSTRAINT chk_contract_event_schemas_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE token_metadata
    ADD CONSTRAINT chk_token_metadata_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE contract_storage_snapshots
    ADD CONSTRAINT chk_contract_storage_snapshots_network
    CHECK (network IN ('mainnet', 'testnet')) NOT VALID;

-- ---------------------------------------------------------------------------
-- Nullable `network` columns: NULL means "all networks" / "not recorded" and
-- must remain a valid value.
-- ---------------------------------------------------------------------------
ALTER TABLE indexed_contracts
    ADD CONSTRAINT chk_indexed_contracts_network
    CHECK (network IS NULL OR network IN ('mainnet', 'testnet')) NOT VALID;

ALTER TABLE audit_log
    ADD CONSTRAINT chk_audit_log_network
    CHECK (network IS NULL OR network IN ('mainnet', 'testnet')) NOT VALID;

-- ---------------------------------------------------------------------------
-- Validate. Each scans its table but under SHARE UPDATE EXCLUSIVE, which
-- does not block concurrent reads/writes (only other schema changes).
-- ---------------------------------------------------------------------------
ALTER TABLE soroban_events              VALIDATE CONSTRAINT chk_soroban_events_network;
ALTER TABLE indexed_contracts           VALIDATE CONSTRAINT chk_indexed_contracts_network;
ALTER TABLE api_keys                    VALIDATE CONSTRAINT chk_api_keys_network;
ALTER TABLE audit_log                   VALIDATE CONSTRAINT chk_audit_log_network;
ALTER TABLE webhook_subscriptions       VALIDATE CONSTRAINT chk_webhook_subscriptions_network;
ALTER TABLE token_events                VALIDATE CONSTRAINT chk_token_events_network;
ALTER TABLE contract_invocation_metrics VALIDATE CONSTRAINT chk_contract_invocation_metrics_network;
ALTER TABLE contract_liveness           VALIDATE CONSTRAINT chk_contract_liveness_network;
ALTER TABLE contract_verification       VALIDATE CONSTRAINT chk_contract_verification_network;
ALTER TABLE contract_specs              VALIDATE CONSTRAINT chk_contract_specs_network;
ALTER TABLE contract_stats_rollup       VALIDATE CONSTRAINT chk_contract_stats_rollup_network;
ALTER TABLE contract_event_schemas      VALIDATE CONSTRAINT chk_contract_event_schemas_network;
ALTER TABLE token_metadata              VALIDATE CONSTRAINT chk_token_metadata_network;
ALTER TABLE contract_storage_snapshots  VALIDATE CONSTRAINT chk_contract_storage_snapshots_network;
