-- Migration 0025: enforce natural-key uniqueness on soroban_events
-- ---------------------------------------------------------------------------
-- Decision record
-- ---------------
-- The `id` column is a deterministic UUIDv5 derived from
-- (contract_id, ledger_sequence, event_index) — it is NOT a pure function of
-- the Stellar protocol's natural key (transaction_hash, event_index).
-- Therefore a separate unique constraint on the natural key is needed to:
--   1. Guard against genuine duplicates that would produce different UUIDs
--      (e.g. same tx/index pair arriving via two different code paths with
--      different contract_id or ledger_sequence values due to a bug).
--   2. Provide a constraint target that mirrors the Stellar protocol guarantee:
--      within a given network a (transaction_hash, event_index) pair
--      identifies exactly one event.
--   3. Future-proof the schema against id-scheme changes without losing
--      the correctness guarantee.
--
-- The constraint is network-scoped because the same transaction hash CAN
-- appear on different networks in test/local environments, and because
-- the `network` column is the partition key candidate per issue #244.
--
-- The existing `ON CONFLICT (id) DO NOTHING` insert strategy is preserved
-- as-is; the natural-key constraint is an additional safety net at the DB
-- layer that catches any bugs in id derivation.
-- ---------------------------------------------------------------------------

-- Validate existing data before adding the constraint.
-- If any duplicates exist they will surface here and must be resolved before
-- deploying this migration.  In a fresh database (CI, new deployments) this
-- is a no-op.
DO $$
DECLARE
    dup_count BIGINT;
BEGIN
    SELECT COUNT(*)
      INTO dup_count
      FROM (
          SELECT transaction_hash, event_index, network
            FROM soroban_events
           GROUP BY transaction_hash, event_index, network
          HAVING COUNT(*) > 1
      ) sub;

    IF dup_count > 0 THEN
        RAISE EXCEPTION
            'Cannot add natural-key constraint: % duplicate (transaction_hash, event_index, network) group(s) found. '
            'Resolve duplicates before applying this migration.',
            dup_count;
    END IF;
END $$;

-- Add the unique constraint.
ALTER TABLE soroban_events
    ADD CONSTRAINT uq_soroban_events_tx_index_network
    UNIQUE (transaction_hash, event_index, network);
