# Launch Checklist

Gate list for a production launch. Every row must be checked off with an owner,
date, and evidence. A row left unverified fails the launch.

## Mainnet configuration (hard gate)

| # | Check | How to verify | Owner / date / evidence |
|---|-------|---------------|-------------------------|
| M1 | `NETWORK=mainnet` is set on the indexer (not defaulted) | `fly secrets list -a trident-indexer` / inspect `.env`; indexer start logs must NOT contain "NETWORK is not set — DEFAULTING TO TESTNET" | |
| M2 | `STELLAR_RPC_URL(S)` point at real mainnet endpoints and are reachable | `curl -s -X POST $URL -H 'content-type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"getNetwork"}'` returns `"passphrase":"Public Global Stellar Network ; September 2015"` for **every** URL; `getLatestLedger` returns a current sequence | |
| M3 | `NETWORK_PASSPHRASE` unset, or equal to the mainnet passphrase | inspect env | |
| M4 | `TRACKED_SAC_ASSETS` matches the reviewed list in [mainnet-sac-assets.md](mainnet-sac-assets.md) and derived ids were re-verified | follow its verification procedure | |
| M5 | Partitions cover the current mainnet ledger height plus headroom | take `latest_ledger` from M2, then in Postgres confirm a `soroban_events` partition covers it and the next range (`\d+ soroban_events`); add with `SELECT create_soroban_partition(start_ledger, end_ledger);` ([migration 0017](../database/migrations/0017_soroban_events_partitioning.sql)). Events landing in the default partition is a failure | |

## Core launch rows

| # | Check | Owner / date / evidence |
|---|-------|-------------------------|
| 1 | Alerts ([monitoring/alerts.yml](../monitoring/alerts.yml)) loaded and firing test passed; runbook links valid ([alerts.md](runbooks/alerts.md)) | |
| 2 | Backup and restore tested | |
| 3 | Soak test passed | |
| 4 | Chaos test passed | |
| 5 | End-to-end user journey verified | |
| 6 | SDKs released and documented | |
| 7 | Docs reviewed ([deployment.md](deployment.md)) | |
| 8 | On-call staffed incl. cutover coverage ([incident-response.md](runbooks/incident-response.md)) | |
| 9 | Rollback plan rehearsed ([deployment.md](deployment.md#rollback)) | |
# Pre-launch verification checklist (MVP go/no-go gate)

**Status: template — not yet executed.** This is the checklist structure
issue #459 asks for; running it against production configuration with
evidence and named sign-off is real work that needs to happen with actual
infrastructure access and team availability, which this pass doesn't have.
Filling in "Evidence" and "Signed off by" for each row, against production
config, is what turns this from a template into a completed launch gate.

## Enforcement

This table is the blocking set for testnet launch (issue #503) — every row
below is a launch blocker, distinct from the ~50 other open launch issues that
are not. Each row's Pass/Fail column is meant to be an objective, checkable
fact rather than an opinion, and `scripts/check-launch-gate.sh` enforces that
mechanically instead of relying on someone reading the table carefully:

```bash
scripts/check-launch-gate.sh              # checks docs/LAUNCH_CHECKLIST.md
scripts/check-launch-gate.sh path/to.md   # or an explicit path
```

It fails (exit 1) if any row's Pass/Fail column is blank, unrecognized, or
`Fail`, if a row marked `Pass` is missing Evidence or a Signed-off-by name, or
if the rollback rehearsal (row 9) has no dated evidence or is older than the
30-day limit below. It exits 0 only when the table itself says every gate is
satisfied. Run it locally before any go/no-go call; see the script's header
comment for what it deliberately does not check (truthfulness of the Evidence
text, and open P1/P2 incidents — both still require a human).

Row 1 (alerts verified firing) and row 8 (on-call schedule confirmed) have
their own dedicated mechanical check, `scripts/check-oncall-readiness.sh`
(issue #620), which fails while
[`incident-response.md`'s on-call contacts](./runbooks/incident-response.md#on-call-owner--launch-week)
or [`alert-routing.md`'s PagerDuty/Slack keys and pre-launch routing test
table](./runbooks/alert-routing.md#pre-launch-routing-test) are still the
unexecuted template — the same "not yet done" state this checklist's rows 1
and 8 already report as blocking, made checkable at the document level
instead of only at the summary-table level.

| # | Gate | Pass/Fail | Evidence | Signed off by |
|---|------|-----------|----------|----------------|
| 1 | Alerts verified firing (trigger each alert deliberately, confirm on-call receives it) | | | |
| 2 | Backup restore performed end-to-end — see [`scripts/backup.sh`](../scripts/backup.sh), [`scripts/restore.sh`](../scripts/restore.sh), and [`docs/runbooks/postgres-backup-restore.md`](./runbooks/postgres-backup-restore.md) | | | |
| 3 | Soak test passed (sustained load for the documented duration with no leaks/degradation) | | | |
| 4 | Chaos test passed (kill a pod/dependency mid-traffic, confirm recovery) | | | |
| 5 | Core user journey test green against production configuration | | | |
| 6 | SDKs published and installable from the real package registry (not just built locally) | | | |
| 7 | Docs quickstart walked by someone who hasn't seen the codebase, start to finish | | | |
| 8 | On-call schedule confirmed and reachable | | | |
| 9 | Rollback rehearsed — see `docs/ROLLBACK_RUNBOOK.md` | | | |
| 10 | Testnet cutover runbook walked through — see `docs/runbooks/testnet-cutover.md` | | | |

## No-go criteria

Launch does not proceed if any of the following are true on launch day:

- Any row above is unresolved (blank Pass/Fail) or marked Fail.
- A P1/P2 incident is open and unresolved on a launch-critical path.
- The rollback rehearsal (row 9) has not been performed within the last
  30 days.

## Rollback decision procedure

- **Who calls it**: the on-call engineer, or the launch owner if reachable
  and available within 5 minutes — do not wait for a quorum during an
  active incident.
- **On what signal**: error rate on a launch-critical path exceeds its
  documented SLO threshold for more than 5 consecutive minutes, or a P1
  incident is declared.
- **Then**: follow `docs/ROLLBACK_RUNBOOK.md`.

## After this checklist is real

Once executed, this file should be checked into the repo with its
evidence and sign-offs filled in, so the next launch has a completed
reference to work from rather than starting over.
