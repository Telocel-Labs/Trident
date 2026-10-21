-- Down migration for 0038_per_network_cursor.sql.
DELETE FROM system_state
WHERE key IN (
    'latest_ledger_cursor:mainnet',
    'latest_ledger_cursor:testnet',
    'latest_ledger_cursor:futurenet',
    'latest_ledger_cursor:sandbox'
);
