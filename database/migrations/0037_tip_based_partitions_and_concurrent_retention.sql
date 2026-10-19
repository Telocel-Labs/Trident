-- Migration 0037: tip-based future event partition maintenance and non-blocking concurrent partition pruning (issues #728, #729).

-- Drop previous overloaded signatures if needed
DROP FUNCTION IF EXISTS ensure_future_event_partitions(INT);

CREATE OR REPLACE FUNCTION ensure_future_event_partitions(
    current_tip BIGINT DEFAULT NULL,
    months_ahead INT DEFAULT 3
) RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    ledgers_per_month BIGINT := 17280 * 30;
    partition_span    BIGINT := 2000000;
    highest_upper     BIGINT;
    target_upper      BIGINT;
    next_start        BIGINT;
    created_count     INT := 0;
    cleaned_count     INT := 0;
    result_msg        TEXT;
    part_name         TEXT;
    part_lower        BIGINT;
    row_count         BIGINT;
BEGIN
    IF current_tip IS NULL OR current_tip <= 0 THEN
        SELECT COALESCE(MAX(sequence), 0)
        INTO current_tip
        FROM ledger_metadata;
    END IF;

    SELECT COALESCE(MAX(
        (regexp_match(c.relname, '^soroban_events_p\d+_(\d+)$'))[1]::BIGINT + 1
    ), 0)
    INTO highest_upper
    FROM pg_class c
    JOIN pg_inherits i ON i.inhrelid = c.oid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname = 'soroban_events'
      AND c.relname ~ '^soroban_events_p\d+_\d+$';

    target_upper := current_tip + (ledgers_per_month * months_ahead);

    -- Only create partitions if current partition horizon is behind target_upper
    IF highest_upper < target_upper THEN
        next_start := highest_upper;
        WHILE next_start < target_upper LOOP
            SELECT create_soroban_partition(next_start, next_start + partition_span)
            INTO result_msg;
            IF result_msg LIKE 'created:%' THEN
                created_count := created_count + 1;
            END IF;
            next_start := next_start + partition_span;
        END LOOP;
    END IF;

    -- Clean up excess empty future partitions that extend beyond target_upper + partition_span
    FOR part_name, part_lower IN
        SELECT c.relname, (regexp_match(c.relname, '^soroban_events_p(\d+)_\d+$'))[1]::BIGINT
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'soroban_events'
          AND c.relname ~ '^soroban_events_p\d+_\d+$'
          AND (regexp_match(c.relname, '^soroban_events_p(\d+)_\d+$'))[1]::BIGINT > target_upper + partition_span
        ORDER BY c.relname DESC
    LOOP
        EXECUTE format('SELECT COUNT(*) FROM (SELECT 1 FROM %I LIMIT 1) s', part_name) INTO row_count;
        IF row_count = 0 THEN
            EXECUTE format('ALTER TABLE soroban_events DETACH PARTITION %I', part_name);
            EXECUTE format('DROP TABLE %I', part_name);
            cleaned_count := cleaned_count + 1;
        END IF;
    END LOOP;

    RETURN format('ensured partitions up to %s (tip %s, %s created, %s cleaned)',
        target_upper, current_tip, created_count, cleaned_count);
END;
$$;

-- Overload for single-argument call (months_ahead)
CREATE OR REPLACE FUNCTION ensure_future_event_partitions(
    months_ahead INT
) RETURNS TEXT LANGUAGE plpgsql AS $$
BEGIN
    RETURN ensure_future_event_partitions(NULL, months_ahead);
END;
$$;

CREATE OR REPLACE FUNCTION prune_soroban_events(
    retention_days INT
) RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    cutoff_ts     TIMESTAMPTZ := NOW() - (retention_days || ' days')::INTERVAL;
    part_name     TEXT;
    from_seq      BIGINT;
    to_seq        BIGINT;
    part_max_ts   TIMESTAMPTZ;
    dropped_count INT := 0;
    matches       TEXT[];
BEGIN
    FOR part_name IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'soroban_events'
          AND c.relname ~ '^soroban_events_p\d+_\d+$'
        ORDER BY c.relname
    LOOP
        matches := regexp_match(part_name, '^soroban_events_p(\d+)_(\d+)$');
        IF matches IS NOT NULL THEN
            from_seq := matches[1]::BIGINT;
            to_seq := matches[2]::BIGINT;

            -- Check max timestamp without scanning entire table if ledger_metadata exists
            SELECT MAX(closed_at) INTO part_max_ts
            FROM ledger_metadata
            WHERE sequence >= from_seq AND sequence <= to_seq;

            IF part_max_ts IS NULL THEN
                EXECUTE format('SELECT MAX(ledger_timestamp) FROM %I', part_name)
                INTO part_max_ts;
            END IF;

            IF part_max_ts IS NOT NULL AND part_max_ts < cutoff_ts THEN
                -- Detach partition first to avoid holding ACCESS EXCLUSIVE on the parent
                EXECUTE format('ALTER TABLE soroban_events DETACH PARTITION %I', part_name);
                EXECUTE format('DROP TABLE %I', part_name);

                -- Prune dependent tables for the dropped range
                DELETE FROM token_events WHERE ledger_sequence >= from_seq AND ledger_sequence <= to_seq;
                DELETE FROM contract_invocation_metrics WHERE ledger_sequence >= from_seq AND ledger_sequence <= to_seq;
                DELETE FROM contract_storage_snapshots WHERE ledger_sequence >= from_seq AND ledger_sequence <= to_seq;
                DELETE FROM ledger_metadata WHERE sequence >= from_seq AND sequence <= to_seq;

                dropped_count := dropped_count + 1;
            END IF;
        END IF;
    END LOOP;

    RETURN format('pruned %s partition(s) older than %s days', dropped_count, retention_days);
END;
$$;
