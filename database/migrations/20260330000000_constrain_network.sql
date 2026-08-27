-- Constrain network column to known valid values across soroban_events, api_keys, and webhook_subscriptions

-- 1. Fail loudly on any existing unexpected values
DO $$
DECLARE
    invalid_count INT;
BEGIN
    SELECT COUNT(*) INTO invalid_count
    FROM soroban_events
    WHERE network NOT IN ('mainnet', 'testnet', 'futurenet', 'sandbox');
    
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'Migration failed: % unexpected network values found in soroban_events', invalid_count;
    END IF;

    SELECT COUNT(*) INTO invalid_count
    FROM api_keys
    WHERE network NOT IN ('mainnet', 'testnet', 'futurenet', 'sandbox');
    
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'Migration failed: % unexpected network values found in api_keys', invalid_count;
    END IF;

    SELECT COUNT(*) INTO invalid_count
    FROM webhook_subscriptions
    WHERE network NOT IN ('mainnet', 'testnet', 'futurenet', 'sandbox');
    
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'Migration failed: % unexpected network values found in webhook_subscriptions', invalid_count;
    END IF;
END $$;

-- 2. Add CHECK constraints to enforce allowed network values
ALTER TABLE soroban_events
    ADD CONSTRAINT chk_soroban_events_network CHECK (network IN ('mainnet', 'testnet', 'futurenet', 'sandbox'));

ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_network CHECK (network IN ('mainnet', 'testnet', 'futurenet', 'sandbox'));

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT chk_webhook_subscriptions_network CHECK (network IN ('mainnet', 'testnet', 'futurenet', 'sandbox'));
