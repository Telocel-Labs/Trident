# Pre-launch verification checklist

**Status: template — not yet executed.** This is the checklist structure
issue #459 asks for; running it against production configuration with
evidence and named sign-off is real work that needs to happen with actual
infrastructure access and team availability, which this pass doesn't have.
Filling in "Evidence" and "Signed off by" for each row, against production
config, is what turns this from a template into a completed launch gate.

| # | Gate | Pass/Fail | Evidence | Signed off by |
|---|------|-----------|----------|----------------|
| 1 | Alerts verified firing (trigger each alert deliberately, confirm on-call receives it) | | | |
| 2 | Backup restore performed end-to-end (see `scripts/backup.sh`/`restore.sh` if present, or the DB's documented procedure) | | | |
| 3 | Soak test passed (sustained load for the documented duration with no leaks/degradation) | | | |
| 4 | Chaos test passed (kill a pod/dependency mid-traffic, confirm recovery) | | | |
| 5 | Core user journey test green against production configuration | | | |
| 6 | SDKs published and installable from the real package registry (not just built locally) | | | |
| 7 | Docs quickstart walked by someone who hasn't seen the codebase, start to finish | | | |
| 8 | On-call schedule confirmed and reachable | | | |
| 9 | Rollback rehearsed — see `docs/ROLLBACK_RUNBOOK.md` | | | |

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
