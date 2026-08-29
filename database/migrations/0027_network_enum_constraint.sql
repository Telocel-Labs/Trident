-- Migration 0027: DB: store network as a typed enum/constraint and validate on write (issue #252)
-- Enforces network scoping across all relevant tables: pubnet, testnet, futurenet, local.

-- 1. Normalise existing values in all tables before adding constraints
UPDATE soroban_events SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE soroban_events SET network = 'local' WHERE network = 'standalone';

UPDATE indexed_contracts SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE indexed_contracts SET network = 'local' WHERE network = 'standalone';

UPDATE api_keys SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE api_keys SET network = 'local' WHERE network = 'standalone';

UPDATE audit_log SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE audit_log SET network = 'local' WHERE network = 'standalone';

UPDATE webhook_subscriptions SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE webhook_subscriptions SET network = 'local' WHERE network = 'standalone';

UPDATE token_events SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE token_events SET network = 'local' WHERE network = 'standalone';

UPDATE contract_invocation_metrics SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE contract_invocation_metrics SET network = 'local' WHERE network = 'standalone';

UPDATE contract_liveness SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE contract_liveness SET network = 'local' WHERE network = 'standalone';

UPDATE contract_verification SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE contract_verification SET network = 'local' WHERE network = 'standalone';

UPDATE contract_specs SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE contract_specs SET network = 'local' WHERE network = 'standalone';

UPDATE contract_stats_rollup SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE contract_stats_rollup SET network = 'local' WHERE network = 'standalone';

UPDATE contract_event_schemas SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE contract_event_schemas SET network = 'local' WHERE network = 'standalone';

UPDATE token_metadata SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE token_metadata SET network = 'local' WHERE network = 'standalone';

UPDATE contract_storage_snapshots SET network = 'pubnet' WHERE network = 'mainnet';
UPDATE contract_storage_snapshots SET network = 'local' WHERE network = 'standalone';

-- 2. Update default on api_keys to 'pubnet'
ALTER TABLE api_keys ALTER COLUMN network SET DEFAULT 'pubnet';

-- 3. Add CHECK constraints across relevant tables
ALTER TABLE soroban_events
    ADD CONSTRAINT chk_soroban_events_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE indexed_contracts
    ADD CONSTRAINT chk_indexed_contracts_network
    CHECK (network IS NULL OR network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE audit_log
    ADD CONSTRAINT chk_audit_log_network
    CHECK (network IS NULL OR network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT chk_webhook_subscriptions_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE token_events
    ADD CONSTRAINT chk_token_events_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE contract_invocation_metrics
    ADD CONSTRAINT chk_contract_invocation_metrics_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE contract_liveness
    ADD CONSTRAINT chk_contract_liveness_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE contract_verification
    ADD CONSTRAINT chk_contract_verification_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE contract_specs
    ADD CONSTRAINT chk_contract_specs_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE contract_stats_rollup
    ADD CONSTRAINT chk_contract_stats_rollup_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE contract_event_schemas
    ADD CONSTRAINT chk_contract_event_schemas_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE token_metadata
    ADD CONSTRAINT chk_token_metadata_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));

ALTER TABLE contract_storage_snapshots
    ADD CONSTRAINT chk_contract_storage_snapshots_network
    CHECK (network IN ('pubnet', 'testnet', 'futurenet', 'local'));
