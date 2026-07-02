-- parse_errors: audit trail for events that failed XDR decoding
CREATE TABLE IF NOT EXISTS parse_errors (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    ledger_sequence  BIGINT      NOT NULL,
    event_index      INT         NOT NULL,
    raw_payload      TEXT        NOT NULL,
    error_message    TEXT        NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_parse_errors_occurred_at ON parse_errors (occurred_at DESC);
