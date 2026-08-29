-- failed_events: dead-letter table for well-formed events whose insert into
-- soroban_events repeatedly failed after bounded retries (issue #208).
--
-- Distinct from parse_errors (0008): those events never decoded at all. A
-- failed_events row decoded successfully — the failure is on the DB-insert
-- side (a transient failure that exhausted its retries, or a permanent one
-- such as a data/constraint error) — so the payload here is the same
-- normalised shape that would otherwise have gone to soroban_events, kept
-- for operator inspection and manual replay.
CREATE TABLE IF NOT EXISTS failed_events (
    event_id         UUID        PRIMARY KEY,
    contract_id      TEXT        NOT NULL,
    network          TEXT        NOT NULL,
    ledger_sequence  BIGINT      NOT NULL,
    transaction_hash TEXT        NOT NULL,
    event_index      INT         NOT NULL,
    payload          JSONB       NOT NULL,
    error_message    TEXT        NOT NULL,
    attempts         INT         NOT NULL DEFAULT 1,
    first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_failed_events_last_seen_at ON failed_events (last_seen_at DESC);
