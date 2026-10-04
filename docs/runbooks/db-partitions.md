# Database Partition Capacity Planning (issue #680)

## Overview

`soroban_events` is RANGE-partitioned by `ledger_sequence`
(`database/migrations/0017_soroban_events_partitioning.sql`, issue #244).
Each named partition covers 2,000,000 ledgers (~115 days at Stellar's current
mainnet throughput of ~17,280 ledgers/day). Rows that fall outside every
named range land in `soroban_events_default`, an unindexed catch-all the
indexer refuses to grow into on purpose — see `TridentPartitionExhausted`
below.

There is no automatic partition creation. Partition pre-creation is a
**manual, deliberate operator action**, informed by this document's
capacity-planning procedure rather than left purely reactive.

## Partition Pre-Creation

Add the next ledger-range partition with the idempotent helper function
defined in migration `0017_soroban_events_partitioning.sql`:

```sql
SELECT create_soroban_partition(60000000, 62000000);
```

Calling it for a range that already exists is a safe no-op — it returns
`'already exists: <name>'` rather than erroring, so it's always safe to
re-run.

## Capacity Planning Procedure

Use `scripts/plan-soroban-partitions.sh` to compute which partitions need
to exist ahead of a known future ledger height, with a safety margin, rather
than waiting for the reactive `TridentPartitionExhaustionWarning` alert
(below) to fire first.

**Ahead of a projected mainnet ledger height:**

```bash
scripts/plan-soroban-partitions.sh --target-ledger 70000000
```

**Ahead of a known future date** (e.g. a launch date or a planned traffic
increase), projecting from the current partition coverage read out of the
database:

```bash
scripts/plan-soroban-partitions.sh \
  --target-date 2026-12-31 \
  --database-url "$DATABASE_URL"
```

Both forms default to a 5,000,000-ledger safety margin beyond the target
(the same margin the `TridentPartitionExhaustionWarning` alert threshold
uses, so a plan run today already covers a comfortable buffer past that
alert's own trigger point) — override with `--margin-ledgers`. Without
`--apply` the script only prints the plan (safe to run repeatedly, e.g. from
a monthly cron or as a pre-launch checklist step); pass `--apply` together
with `--database-url` to execute the missing `create_soroban_partition`
calls directly.

**Example: planning for a launch at a projected height, six months out.**

1. Estimate the projected mainnet ledger height at the launch date (from
   Stellar's own network status, or `current tip + days_until_launch *
   17280`).
2. Run the planner against that projected height:
   ```bash
   scripts/plan-soroban-partitions.sh \
     --target-date 2027-03-01 \
     --database-url "$DATABASE_URL"
   ```
3. Review the printed plan, then re-run with `--apply` to create the
   partitions.
4. Repeat this procedure roughly monthly, or whenever a major traffic
   increase is anticipated, so partition headroom never depends on someone
   remembering to react to an alert.

## Monitoring & Alerts

The reactive backstop — for when capacity planning was skipped or a
projection was wrong — is documented in
[`docs/runbooks/alerts.md`](alerts.md):

- **[`TridentPartitionExhaustionWarning`](alerts.md#tridentpartitionexhaustionwarning)**:
  fires when `trident_indexer_partition_lookahead_ledgers` has been below
  5,000,000 ledgers (~289 days of runway) for 30 minutes.
- **[`TridentPartitionExhausted`](alerts.md#tridentpartitionexhausted)**:
  fires when the lookahead reaches 0. The indexer halts ingestion rather
  than silently writing into the unindexed default partition
  (`assert_no_default_partition_overflow` in `crates/indexer/src/db/mod.rs`).
  This is a total ingest outage (SEV-1) — the capacity-planning procedure
  above exists specifically to make reaching this alert a rare, deliberate
  planning failure rather than routine.
