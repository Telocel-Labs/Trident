# Post-cutover data verification

**Status: documented procedure, not yet run against a real mainnet
cutover.** This asks a different question than
[`testnet-cutover.md`](testnet-cutover.md) and
[`../LAUNCH_CHECKLIST.md`](../LAUNCH_CHECKLIST.md): those gate whether the
*infrastructure* is ready to launch. This runbook runs immediately
*after* the mainnet indexer starts producing data, to confirm that data is
actually correct, not just that the deployment looks healthy. A perfectly
healthy-looking indexer can still be indexing the wrong thing entirely (a
stale network tag, the wrong starting ledger), and nothing in the existing
pre-launch checklist asks that second question.

Run this before declaring the mainnet indexer live, not as a background
check sometime after.

## Why "healthy" is not the same as "correct"

`/readyz` and the Prometheus heartbeat metric
(`trident_indexer_last_poll_timestamp_seconds`) only prove the process is up
and polling. They say nothing about:

- whether it is tagging rows with the right `network` value (a typo'd or
  stale value, e.g. `'testnett'`, would be caught at write time by
  `chk_soroban_events_network`'s CHECK constraint, but a correctly-spelled
  *wrong* value, e.g. tagging mainnet data as `'testnet'` from a leftover
  config, passes that constraint silently and simply puts the data in the
  wrong partition of the dataset);
- whether the events it is producing are the actual events the chain
  emitted, versus a parsing or RPC-response-shape bug silently
  misdecoding something;
- whether it started indexing from the ledger it was actually supposed to,
  versus an off-by-some-margin cursor seed leaving a gap or double-counted
  range at the start.

## Procedure

### 1. Confirm the network tag end to end — *operator*

Query a handful of freshly-indexed rows directly:

```sql
SELECT DISTINCT network FROM soroban_events
WHERE processed_at > NOW() - INTERVAL '10 minutes';
```

This must return exactly `mainnet` and nothing else. If it returns
`testnet`, `futurenet`, or more than one value, stop here: this is the
exact silent-wrong-tag failure mode `chk_soroban_events_network`'s CHECK
constraint does not catch (see its own comment in `database/schema.sql`),
and continuing to index compounds the mistake rather than catching it
early.

Cross-check the configured passphrase actually matches, the same way
[`../deployment.md`](../deployment.md#testnet-vs-mainnet-configuration)
already documents for pre-deploy verification:

```bash
curl -s -X POST "$STELLAR_RPC_URL" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"getNetwork"}'
```

The response's `passphrase` must be
`Public Global Stellar Network ; September 2015`.

### 2. Spot-check at least three known transactions — *operator*

Pick two or three transaction hashes from recent mainnet activity that you
can independently verify (a transaction you submitted yourself during
cutover testing is ideal, since you know its exact expected contents).

For each hash, compare three independent sources and confirm they agree:

- **The real chain**, via the RPC node directly:
  ```bash
  curl -s -X POST "$STELLAR_RPC_URL" -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"getTransaction","params":{"hash":"<TX_HASH>"}}'
  ```
- **What the indexer stored**, via Postgres directly:
  ```sql
  SELECT contract_id, event_type, ledger_sequence, topics, data
  FROM soroban_events WHERE transaction_hash = '<TX_HASH>' AND network = 'mainnet';
  ```
- **What the API actually serves**, via `GET /v1/events/{id}` for each
  returned event id, or `GET /v1/events?ledgerFrom=<N>&ledgerTo=<N>` for the
  transaction's ledger if you do not have an event id yet (see
  `api/openapi.yaml` for the exact query parameters).

All three must describe the same events, same contract ids, same topics
and data payloads. A mismatch between the RPC response and what Postgres
stored points at a parsing bug (`crates/indexer/src/parser/`); a mismatch
between Postgres and what the API serves points at a query or
serialization bug in `services/api/handlers/events.go` or the gRPC backend
it calls into, not the indexer itself.

### 3. Confirm ledger continuity from the intended starting point — *operator*

Decide and write down, before this step, what the intended starting ledger
for this cutover actually was (the value `INDEX_FROM_LEDGER` or an
equivalent was configured to, or "chain tip at cutover time" if indexing
was meant to start live rather than backfilled).

```sql
SELECT MIN(ledger_sequence), MAX(ledger_sequence), COUNT(DISTINCT ledger_sequence)
FROM ledger_metadata;
```

- `MIN(ledger_sequence)` should match the intended starting ledger from
  above (within the margin of however long cutover took to execute, if
  indexing started live).
- `COUNT(DISTINCT ledger_sequence)` should equal `MAX - MIN + 1`. If it is
  smaller, there is a gap in `ledger_metadata` inside the indexed range,
  which is the same condition `scan_and_enqueue_gaps`
  (`crates/indexer/src/streamer/mod.rs`) exists to find on an ongoing
  basis, but this is the one-time check that nothing was already missing
  from the very first ledgers indexed, before the gap-scanner's own first
  pass has had a chance to run.

If a gap is found, do not wait for the gap-scanner: follow
[`gap-detection-backfill-drill.md`](gap-detection-backfill-drill.md) to
close it immediately, since a gap at the very start of a mainnet dataset is
exactly the kind of silent, easy-to-miss hole this runbook exists to catch
before anyone is relying on the data being complete.

### 4. Record the result — *operator*

Note the outcome of steps 1-3 (pass/fail per step, the transaction hashes
checked, and the starting-ledger numbers found) wherever this cutover's
other launch evidence is being tracked (see
[`../LAUNCH_CHECKLIST.md`](../LAUNCH_CHECKLIST.md)'s Evidence column
convention). Do not declare the mainnet indexer live until all three steps
pass.

## If a check fails

- **Wrong network tag** (step 1): stop the indexer immediately
  (`STELLAR_NETWORK`/equivalent config is wrong) rather than letting it
  keep writing under the wrong tag; fix the config and restart. Rows
  already written under the wrong tag need to be identified and corrected
  or deleted before resuming, not left mixed into the dataset.
- **Transaction mismatch** (step 2): this is a correctness bug, not an
  infrastructure problem. Treat it as a SEV-1 per
  [`incident-response.md`](incident-response.md) if the indexer is already
  serving production traffic; otherwise hold cutover and file the specific
  discrepancy found (which field, which transaction, RPC vs. Postgres vs.
  API) rather than re-running this runbook hoping it was transient.
- **Ledger gap at the start** (step 3): follow
  [`gap-detection-backfill-drill.md`](gap-detection-backfill-drill.md).

## What this runbook still needs (not done here)

- [ ] A real run against an actual mainnet cutover, by whoever executes
      that cutover, to confirm the three checks above are sufficient and
      the SQL/API calls are exactly right against production's real schema
      and routes, not inferred from reading the code. This is issue #689's
      own literal "done when" criterion.
- [ ] A scripted version of steps 1 and 3 (both are mechanical SQL queries
      with a pass/fail condition), so this does not depend on an operator
      retyping them correctly under cutover pressure. Step 2 likely stays
      manual, since picking which transactions to spot-check is a judgment
      call.
