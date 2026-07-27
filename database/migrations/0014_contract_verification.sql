-- contract_verification (issue #273)
--
-- Records the outcome of matching a deployed contract's on-chain WASM code hash
-- to a hash produced from a submitted source build. One row per contract_id;
-- upserted each time a new verification attempt is made.
--
-- Verification flow:
--   1. Caller submits source metadata (repo URL, commit SHA, toolchain, build
--      command) via the verification API endpoint.
--   2. The API fetches the on-chain code hash from getLedgerEntries.
--   3. The caller (or a server-side build) produces the WASM and submits the
--      SHA-256 of the built artefact.
--   4. Hashes are compared and `status` is set to 'verified' or 'mismatch'.
--
-- `source_hash` and `on_chain_hash` are lowercase hex-encoded SHA-256 digests.
CREATE TABLE IF NOT EXISTS contract_verification (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id         TEXT        NOT NULL,
    network             TEXT        NOT NULL DEFAULT 'testnet',
    status              TEXT        NOT NULL DEFAULT 'unverified'
                            CHECK (status IN ('unverified', 'pending', 'verified', 'mismatch', 'failed')),
    on_chain_hash       TEXT        NOT NULL,
    source_hash         TEXT,
    repository_url      TEXT,
    commit_sha          TEXT,
    toolchain_version   TEXT,
    build_command       TEXT,
    wasm_path           TEXT,
    verified_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT contract_verification_contract_network_unique UNIQUE (contract_id, network)
);

CREATE INDEX IF NOT EXISTS contract_verification_contract_id_idx
    ON contract_verification (contract_id);

CREATE INDEX IF NOT EXISTS contract_verification_status_idx
    ON contract_verification (status);
