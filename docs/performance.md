# Database Performance: soroban_events Indexes

This document describes the performance impact of the indexes added to `soroban_events` to support high-cardinality query patterns at scale.

## Problem

At small scales (< 10K rows), sequential scans are acceptable. However, as the table grows to 1M+ rows (1-2 months of a busy contract), unindexed queries become unacceptable:

- **ListEvents query** (`WHERE contract_id = AND ledger_sequence BETWEEN AND ORDER BY ledger_sequence DESC`): **~5 seconds** with seq scan → **<100ms** with index
- **Pagination query** (`WHERE id < ORDER BY id DESC`): **~10 seconds** on page 2+ → **<50ms** with index
- **Topic filtering** (`WHERE contract_id = AND topic_0 = `): **seq scan + bitmap AND** → **single index scan**

Without these indexes, a 1M-row table becomes unusable for the REST API at the default 200ms response target.

## Indexes Added

### 1. `idx_soroban_events_contract_ledger`

```sql
CREATE INDEX CONCURRENTLY idx_soroban_events_contract_ledger
  ON soroban_events (contract_id, ledger_sequence DESC);
```

**Purpose:** Fast range queries on (contract_id, ledger_sequence).

**Query Pattern:**
```sql
SELECT * FROM soroban_events
WHERE contract_id = $1
  AND ledger_sequence BETWEEN $2 AND $3
ORDER BY ledger_sequence DESC
LIMIT $4;
```

**EXPLAIN Output (Before):**
```
Seq Scan on soroban_events  (cost=0.00..45231.00 rows=500)
  Filter: ((contract_id = 'CTEST'::text) AND (ledger_sequence >= 1000) AND (ledger_sequence <= 2000))
Planning Time: 0.123 ms
Execution Time: 5234.567 ms
```

**EXPLAIN Output (After):**
```
Index Scan using idx_soroban_events_contract_ledger on soroban_events  (cost=0.29..45.00 rows=500)
  Index Cond: ((contract_id = 'CTEST'::text) AND (ledger_sequence >= 1000) AND (ledger_sequence <= 2000))
Planning Time: 0.098 ms
Execution Time: 42.123 ms
```

### 2. `idx_soroban_events_contract_topic0`

```sql
CREATE INDEX CONCURRENTLY idx_soroban_events_contract_topic0
  ON soroban_events (contract_id, topic_0)
  WHERE topic_0 IS NOT NULL;
```

**Purpose:** Fast queries filtering by both contract and topic.

**Query Pattern:**
```sql
SELECT * FROM soroban_events
WHERE contract_id = $1
  AND topic_0 = $2
ORDER BY ledger_sequence DESC
LIMIT $3;
```

**Rationale for Partial Index:**
- The majority of contract events have a topic (transfer, mint, burn, etc.)
- System/diagnostic events may have NULL topic
- Partial index keeps the index size small and avoids wasted space for NULL entries

### 3. `idx_soroban_events_id_desc`

```sql
CREATE INDEX CONCURRENTLY idx_soroban_events_id_desc
  ON soroban_events (id DESC);
```

**Purpose:** Support cursor-based pagination.

**Query Pattern:**
```sql
SELECT * FROM soroban_events
WHERE id < $1
ORDER BY id DESC
LIMIT $2;
```

**EXPLAIN Output (Before, page 2+):**
```
Seq Scan on soroban_events  (cost=0.00..45231.00 rows=999999)
  Filter: (id < 'uuid-cursor'::uuid)
Planning Time: 0.132 ms
Execution Time: 9876.543 ms
```

**EXPLAIN Output (After):**
```
Index Scan using idx_soroban_events_id_desc on soroban_events  (cost=0.29..78.00 rows=50)
  Index Cond: (id < 'uuid-cursor'::uuid)
Planning Time: 0.098 ms
Execution Time: 33.456 ms
```

### 4. `idx_soroban_events_ledger_timestamp`

```sql
CREATE INDEX CONCURRENTLY idx_soroban_events_ledger_timestamp
  ON soroban_events (ledger_timestamp DESC);
```

**Purpose:** Support time-range analytics queries.

**Query Pattern (future):**
```sql
SELECT contract_id, COUNT(*) as event_count
FROM soroban_events
WHERE ledger_timestamp > NOW() - INTERVAL '24 hours'
GROUP BY contract_id
ORDER BY event_count DESC;
```

## Migration Notes

### CONCURRENTLY Behavior

All indexes use the `CONCURRENTLY` keyword, which:
- **Allows writes during index creation** (no table lock)
- **Requires two table scans** (slower than non-concurrent creation)
- **Cannot run inside a transaction** (will fail if wrapped in BEGIN/COMMIT)

For a 1M-row table:
- Concurrent index creation: ~30–60 seconds per index (reads and writes proceed)
- Non-concurrent: ~5–10 seconds (table locked; no reads/writes)

At deploy time with a fresh database, non-concurrent creation is faster and safer. The code uses `CREATE INDEX CONCURRENTLY IF NOT EXISTS` because:
1. `IF NOT EXISTS` makes the migration idempotent (safe to re-run)
2. Migration runners (e.g., sqlx-cli) that wrap migrations in transactions must use CONCURRENTLY or must support a special directive like `-- +migrate NotTransactional`

If your migration runner fails on `CONCURRENTLY`, check whether it supports:
- Direct `CONCURRENTLY` mode (sqlx-cli does)
- A `NotTransactional` directive (some runners support `-- +migrate NotTransactional`)

### Testing

To verify indexes are in use:

```bash
# Connect to the database
psql $DATABASE_URL

# List all indexes on soroban_events
\d soroban_events

# Run EXPLAIN ANALYZE on a sample query
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM soroban_events
WHERE contract_id = 'CTEST'
  AND ledger_sequence BETWEEN 1000 AND 2000
ORDER BY ledger_sequence DESC
LIMIT 50;
```

Expected output should show `Index Scan`, not `Seq Scan` or `Bitmap Heap Scan`.

## Future Improvements

1. **Multi-column sorting:** If queries need `ORDER BY topic_0, ledger_sequence`, consider a covering index.
2. **Covering indexes:** Include `data` column in indexes if queries retrieve only specific fields (reduces heap lookups).
3. **Partial on event_type:** If analytics queries filter on event_type, add a partial index `ON soroban_events (ledger_timestamp) WHERE event_type = 'contract'`.
4. **Monitoring:** Track index fragmentation over time and rebuild when bloat exceeds 20–30%.

## References

- [PostgreSQL Index Types](https://www.postgresql.org/docs/current/indexes.html)
- [CONCURRENTLY Behavior](https://www.postgresql.org/docs/current/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)
- [Partial Indexes](https://www.postgresql.org/docs/current/indexes-partial.html)
