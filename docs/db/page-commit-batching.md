# Page commit batching

How the indexer writes one RPC page to Postgres, and what changed in issue #199.

## Before

`poll_once` walked the page and, per event, issued one `INSERT` and one Redis
`XADD`. The cursor update and the `ledger_metadata` insert followed as two more
statements, each in its own implicit transaction.

Two consequences:

- **Round-trips scale with page size.** Every row paid a full network round-trip
  to Postgres, so ingest latency was dominated by round-trip count rather than by
  the work Postgres actually did.
- **The cursor could outrun the data.** The cursor advanced in a separate
  transaction from the events it covered. A crash in between left the cursor
  pointing past events that were never written, and the next poll resumed after
  them — a silent gap.

## After

The page is accumulated in memory, then written by `db::commit_page` in a single
transaction:

1. events, via `INSERT ... SELECT * FROM UNNEST(...)`, chunked to `DB_BATCH_SIZE`
2. the token projection rows (issue #211), same batching
3. the cursor update
4. the `ledger_metadata` row

Redis publishing moves after the commit, so a subscriber can never observe an
event that a rolled-back transaction never persisted.

## Round-trip count

Statements sent to Postgres per page, at the default `MAX_EVENTS_PER_POLL=200`
and `DB_BATCH_SIZE=1000`:

| Page size | Before        | After                    |
| --------- | ------------- | ------------------------ |
| 1         | 3             | 4 (BEGIN + 2 + COMMIT)   |
| 50        | 52            | 4                        |
| 200       | 202           | 4                        |
| 2000      | 2002          | 6 (2 event chunks)       |

A full default page drops from 202 statements to 4 — the batched insert, the
cursor update, and the transaction envelope — independent of how many events the
page holds, until the page exceeds `DB_BATCH_SIZE` and adds one statement per
extra chunk. Tiny pages pay two extra statements for the transaction envelope,
which is the cost of the cursor no longer being able to outrun the data.

## Tuning `DB_BATCH_SIZE`

The default of 1000 means every default-sized page commits in one statement.
Raising it past `MAX_EVENTS_PER_POLL` changes nothing. Lower it only if you have
raised `MAX_EVENTS_PER_POLL` far enough that a single statement's parameter
arrays become a memory concern; chunking splits the statement, never the
transaction, so atomicity is unaffected either way.

## Verification

`crates/indexer/src/db/mod.rs` covers this directly: a 25-event page committed
with `batch_size: 10` lands in full, advances the cursor exactly once, and
inserts nothing new when the identical page is replayed.
