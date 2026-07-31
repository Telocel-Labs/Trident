-- event_outbox: transactional outbox decoupling the Redis publish from the
-- Postgres write (issue #200).
--
-- The indexer inserts the event row and its outbox row in one transaction, so a
-- committed event always has a delivery record. A relay task then publishes
-- unpublished rows to the Redis Stream in `seq` order and flips `published`.
-- Delivery is at-least-once: a crash between XADD and the UPDATE re-delivers
-- the event, and consumers dedupe on `event_id`.
CREATE TABLE IF NOT EXISTS event_outbox (
    seq          BIGSERIAL   PRIMARY KEY,
    event_id     UUID        NOT NULL UNIQUE,
    payload      JSONB       NOT NULL,
    published    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ
);

-- The relay only ever scans unpublished rows in insertion order; a partial
-- index keeps that scan O(backlog) rather than O(table).
CREATE INDEX IF NOT EXISTS idx_event_outbox_unpublished
    ON event_outbox (seq)
    WHERE published = FALSE;
