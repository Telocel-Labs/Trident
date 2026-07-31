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
--      identifies exactly one event. (Scoped per-partition in practice — see
--      the note on ledger_sequence at the ALTER TABLE below.)
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
--
-- ledger_sequence is part of the key only because it has to be: migration
-- 0017 made soroban_events RANGE-partitioned on that column, and PostgreSQL
-- requires every unique constraint on a partitioned table to include the
-- partition key. `UNIQUE (transaction_hash, event_index, network)` alone is
-- rejected outright — the same trade-off 0017 already documents for the
-- primary key, which became (ledger_sequence, id) for this reason.
--
-- What this costs: the constraint no longer catches the same
-- (transaction_hash, event_index, network) triple appearing under two
-- *different* ledger_sequence values. That would mean the indexer recorded
-- one protocol event against two different ledgers, which is a distinct bug
-- from the duplicate-insert case this migration exists to catch, and it is
-- still caught by the pre-flight check above on any existing data. Within a
-- ledger — where duplicates actually arise, from replays and overlapping
-- code paths — the guarantee is unchanged.
ALTER TABLE soroban_events
    ADD CONSTRAINT uq_soroban_events_tx_index_network
    UNIQUE (ledger_sequence, transaction_hash, event_index, network);
