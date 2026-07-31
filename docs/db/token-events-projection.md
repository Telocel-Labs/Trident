# token_events projection

Typed projection of the standard SEP-41 / Stellar-Asset-Contract token events
(issue #211).

## Why a separate table

`soroban_events` stores every event generically: topics as a JSON array, body as
JSON. That is right for a general indexer, but the dominant analytics question —
"who sent what to whom" — then costs a JSON parse per row and cannot use an
index on the sender or recipient.

`token_events` answers those queries directly: one row per decoded token event,
with `from_address`, `to_address`, `spender_address`, `admin_address`, and
`amount` as real columns, indexed for the account- and contract-centric lookups.

## What gets projected

Only the five value-movement events:

| Event      | Populated fields                              |
| ---------- | --------------------------------------------- |
| `transfer` | `from`, `to`, `amount`                        |
| `mint`     | `admin`, `to`, `amount`                       |
| `burn`     | `from`, `amount`                              |
| `clawback` | `admin`, `from`, `amount`                     |
| `approve`  | `from`, `spender`, `amount`, `expiration_ledger` |

Administrative events (`set_admin`, `set_authorized`, `increase_supply`) move no
value and are not projected; they remain available in `soroban_events`.

Decoding is strict about shape, not just about the topic symbol. Any contract may
emit an event whose first topic is `transfer`; unless the rest of the payload
matches the token interface layout, it is not projected. A permissive decoder
would let unrelated contracts inject rows into the transfer analytics.

## Amounts are strings

`amount` is `TEXT`. Token amounts are `i128`, which does not survive a JSON
number or a `BIGINT` (issue #210). Queries needing arithmetic should cast
explicitly:

```sql
SELECT SUM(amount::NUMERIC) FROM token_events
WHERE contract_id = $1 AND event_type = 'transfer';
```

## Consistency with soroban_events

`event_id` is both the primary key and a foreign key to `soroban_events(id)`,
with `ON DELETE CASCADE`. Because that ID is a deterministic UUIDv5 of
`(contract_id, ledger_sequence, event_index)`:

- a replayed page re-derives the same key, and `ON CONFLICT DO NOTHING` absorbs it
- the projection is written in the same transaction as the event it projects, so
  it can never reference a row that a rolled-back page never wrote
- deleting an event removes its projection

## Fixtures

`crates/indexer/fixtures/token_events/` holds wire-format golden inputs — base64
XDR topics and bodies as `getEvents` returns them — with the expected decode.
See the README there before adding cases.
