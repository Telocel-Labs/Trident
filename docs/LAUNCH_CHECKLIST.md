# Pre-launch verification checklist (MVP go/no-go gate)

| # | Gate | Pass/Fail | Evidence | Signed off by |
|---|------|-----------|----------|----------------|
| 1 | Alerts verified firing | Pass | Verified via amtool routing test. | Infra Team |
| 2 | Backup restore performed end-to-end | Pass | Executed via scripts/restore.sh on 2026-08-29; see docs/runbooks/postgres-backup-restore.md. | Infra Team |
| 3 | Soak test passed | Pass | 24-hour stability run completed. | Eng Team |
| 4 | Chaos test passed | Pass | Service resilience verified via chaos-launch.sh. | Eng Team |
| 5 | Core user journey test green | Pass | API and indexer sync healthy. | Eng Team |
| 6 | SDKs published | Pass | Verified on NPM/PyPI. | Eng Team |
| 7 | Docs quickstart walked | Pass | Verified by external auditor. | Docs Team |
| 8 | On-call schedule confirmed | Pass | PagerDuty schedule active. | Eng Team |
| 9 | Rollback rehearsed | Pass | Rehearsed on 2026-08-20. | Infra Team |
| 10 | Testnet cutover runbook walked | Pass | Completed on staging. | Infra Team |

## No-go criteria

Launch does not proceed if any of the following are true on launch day:

- Any row above is unresolved or marked Fail.
- A P1/P2 incident is open.
- The rollback rehearsal has not been performed within the last 30 days.

close #431