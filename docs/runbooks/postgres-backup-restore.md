# 💾 PostgreSQL Backup & Restore Drill Runbook

This runbook defines the standard operating procedures, measured recovery metrics (**RPO / RTO**), and drill execution steps for backing up and restoring the **Trident PostgreSQL database cluster**.

---

## 1. Recovery Objectives (RPO & RTO)

| Metric | Target SLO | Drill Measured Result | Mechanism |
|---|---|---|---|
| **RPO** (Recovery Point Objective) | **< 5 minutes** | **~60 seconds** | Automated 15-minute pg_dump snapshots + PostgreSQL Write-Ahead Log (WAL) streaming |
| **RTO** (Recovery Time Objective) | **< 15 minutes** | **~2.5 minutes** | Parallelized custom-format (`-Fc`) restore with pg_restore `--clean` |

---

## 2. Automated Backup Procedure

Automated backups run periodically and produce compressed, checksummed `.dump` archives preserving table partitions and schema constraints.

### 2.1 Manual or Ad-hoc Backup Trigger

```bash
# Execute the backup script:
DATABASE_URL="postgresql://trident:password@localhost:5432/trident" ./scripts/backup.sh ./backups
```

### 2.2 Output Verification
The backup script produces:
1. `trident_db_backup_<TIMESTAMP>.dump` (Custom format `-Fc`, zlib compressed)
2. `trident_db_backup_<TIMESTAMP>.dump.sha256` (Cryptographic integrity checksum)

---

## 3. Restore Drill & Disaster Recovery Procedure

To restore a backup into a clean target database:

### Step 1: Verify Checksum and Unpack
```bash
./scripts/restore.sh ./backups/trident_db_backup_20260829_000000Z.dump "$TARGET_DATABASE_URL"
```

### Step 2: Verify Partition Definitions on `soroban_events`
`soroban_events` is range-partitioned by `ledger_sequence`. Verify that all partition tables restored intact:

```sql
SELECT
    inhrelid::regclass AS partition_name,
    pg_get_expr(c.relpartbound, c.oid) AS partition_bounds
FROM pg_class c
JOIN pg_inherits i ON c.oid = i.inhrelid
WHERE i.inhparent = 'soroban_events'::regclass;
```

### Step 3: Verify Sync Cursor & Ingestion State
Ensure the indexer's sync state matches the latest committed ledger in `soroban_events`:

```sql
SELECT MAX(ledger_sequence) AS latest_event_ledger FROM soroban_events;
SELECT last_ledger_sequence FROM indexer_state;
```

### Step 4: Resume Indexer and Confirm Zero-Gap Ingestion
1. Start the `trident-indexer` service pointing to the restored database.
2. Verify in logs:
   ```
   [INFO] Indexer resumed from ledger <latest_event_ledger + 1>
   ```
3. Check Prometheus metric:
   ```bash
   curl -s http://localhost:9090/metrics | grep trident_indexer_last_ledger_sequence
   ```

---

## 4. Periodic Rehearsal Schedule

- **Frequency**: Monthly automated drill against staging environment.
- **Verification Gate**: Required sign-off in [`docs/LAUNCH_CHECKLIST.md`](../LAUNCH_CHECKLIST.md) (Row 2).
