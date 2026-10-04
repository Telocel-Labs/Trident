-- Migration 0036: partition pre-creation and event-retention helper
-- functions (issues #431, #433).
--
-- Both `crates/indexer/src/main.rs`'s `maintain_partitions` and
-- `run_retention_job` background tasks call these by name via a bare
-- `SELECT fn(...)`, not through sqlx's compile-time-checked query macro, so
-- their absence was never caught by `cargo check` — only at runtime, the
-- first time each job's daily interval ticked.
--
-- ensure_future_event_partitions(months_ahead): calls the existing
-- create_soroban_partition (migration 0017) for however many of the next
-- 2,000,000-ledger ranges are needed to keep partition coverage at least
-- `months_ahead` months ahead of the newest already-named partition's upper
-- bound, estimated at Stellar's ~17,280 ledgers/day.
--
-- prune_soroban_events(days): drops whole soroban_events partitions once
-- every row in them is older than `days`. A DROP TABLE on a partition is
-- near-instant and avoids the vacuum churn of a row-by-row DELETE across a
-- partitioned table at this scale. The DEFAULT partition is never dropped:
-- it is the catch-all for out-of-range rows, not a dated range.

CREATE OR REPLACE FUNCTION ensure_future_event_partitions(
    months_ahead INT DEFAULT 3
) RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    ledgers_per_month BIGINT := 17280 * 30;
    partition_span     BIGINT := 2000000;
    highest_upper       BIGINT;
    target_upper         BIGINT;
    next_start            BIGINT;
    created_count            INT := 0;
    result_msg               TEXT;
BEGIN
    SELECT COALESCE(MAX(
        (regexp_match(c.relname, '^soroban_events_p\d+_(\d+)$'))[1]::BIGINT + 1
    ), 0)
    INTO highest_upper
    FROM pg_class c
    JOIN pg_inherits i ON i.inhrelid = c.oid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname = 'soroban_events'
      AND c.relname ~ '^soroban_events_p\d+_\d+$';

    target_upper := highest_upper + (ledgers_per_month * months_ahead);
    next_start := highest_upper;

    WHILE next_start < target_upper LOOP
        SELECT create_soroban_partition(next_start, next_start + partition_span)
        INTO result_msg;
        IF result_msg LIKE 'created:%' THEN
            created_count := created_count + 1;
        END IF;
        next_start := next_start + partition_span;
    END LOOP;

    RETURN format('ensured partitions up to %s ledgers ahead (%s created)',
        target_upper, created_count);
END;
$$;

CREATE OR REPLACE FUNCTION prune_soroban_events(
    retention_days INT
) RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    cutoff_ts   TIMESTAMPTZ := NOW() - (retention_days || ' days')::INTERVAL;
    part_name   TEXT;
    part_max_ts TIMESTAMPTZ;
    dropped_count INT := 0;
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
        EXECUTE format('SELECT MAX(ledger_timestamp) FROM %I', part_name)
        INTO part_max_ts;

        IF part_max_ts IS NOT NULL AND part_max_ts < cutoff_ts THEN
            EXECUTE format('DROP TABLE %I', part_name);
            dropped_count := dropped_count + 1;
        END IF;
    END LOOP;

    RETURN format('pruned %s partition(s) older than %s days', dropped_count, retention_days);
END;
$$;
