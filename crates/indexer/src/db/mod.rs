use std::collections::{HashMap, HashSet};
use std::str::FromStr;
use std::time::Duration;

use chrono::{DateTime, Utc};
use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use sqlx::PgPool;
use trident_common::{EventType, SorobanEvent, TridentError};
use uuid::Uuid;

use crate::parser::token_events::TokenEvent;

/// Build a bounded Postgres connection pool sized for this service.
///
/// `statement_cache_capacity(0)` disables sqlx's named-prepared-statement cache.
/// This is mandatory when connecting through PgBouncer in transaction pooling
/// mode: a cached prepared statement is bound to one server connection, but in
/// transaction mode the next transaction may be routed to a different server
/// connection where that statement does not exist, which makes the query fail.
/// See docs/deployment.md (issue #87).
#[allow(dead_code)]
pub async fn connect_pool(database_url: &str, pool_size: u32) -> Result<PgPool, TridentError> {
    let connect_options = PgConnectOptions::from_str(database_url)
        .map_err(|e| TridentError::config(anyhow::Error::new(e).context("invalid DATABASE_URL")))?
        .statement_cache_capacity(0);

    PgPoolOptions::new()
        .max_connections(pool_size)
        .connect_with(connect_options)
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("connect_pool")))
}

// Stable namespace for deterministic event UUIDs (UUIDv5).
// Using the DNS namespace is arbitrary; what matters is that it is fixed.
const EVENT_NS: Uuid = Uuid::NAMESPACE_DNS;

/// Derive a deterministic UUID for an event from its natural key.
/// Using the same inputs will always produce the same UUID, so duplicate
/// events produce the same (ledger_sequence, id) pair and the insert's
/// `ON CONFLICT DO NOTHING` absorbs the replay.
fn event_uuid(contract_id: &str, ledger_sequence: u64, event_index: u32) -> Uuid {
    let key = format!("{contract_id}:{ledger_sequence}:{event_index}");
    Uuid::new_v5(&EVENT_NS, key.as_bytes())
}

pub mod outbox;

/// Ledger provenance recorded alongside a committed page.
pub struct LedgerMeta<'a> {
    pub sequence: u64,
    pub hash: &'a str,
    pub timestamp: &'a str,
    pub event_count: i32,
}

/// Everything one RPC page contributes to the database, committed atomically.
///
/// Bundling the events with the cursor advance is what stops a crash between
/// the two from leaving the cursor ahead of the data it claims to cover.
/// A decoded token event paired with the indexed event it projects (issue #211).
pub struct TokenProjection<'a> {
    pub event: &'a SorobanEvent,
    pub token: &'a TokenEvent,
}

/// One (contract, transaction) invocation metrics row, ready to persist into
/// `contract_invocation_metrics` (issue #266).
pub struct InvocationMetricRow<'a> {
    pub contract_id: &'a str,
    pub transaction_hash: &'a str,
    pub ledger_sequence: u64,
    /// ISO 8601 UTC timestamp of the ledger close.
    pub ledger_timestamp: &'a str,
    pub metrics: &'a crate::parser::invocation_metrics::InvocationMetrics,
}

pub struct PageCommit<'a> {
    pub events: &'a [SorobanEvent],
    /// Normalised token-event rows for this page. Every referenced event must
    /// also appear in `events` — the projection is foreign-keyed to it.
    pub token_events: &'a [TokenProjection<'a>],
    /// Per-invocation fee + declared-resource metering for tracked contracts
    /// in this page (issue #266). Empty in index-all mode — metering is
    /// bounded to the allowlist.
    pub invocation_metrics: &'a [InvocationMetricRow<'a>],
    /// Contract storage snapshot changes observed in this page (issue #270).
    /// Empty unless a tracked contract was detected as a SEP-41 token and one
    /// of its holders moved funds in this page.
    pub storage_snapshots: &'a [StorageSnapshotRow<'a>],
    /// Network these storage snapshots belong to (empty string tolerated
    /// when `storage_snapshots` is empty). Also stamped on any event
    /// dead-lettered to `failed_events` (issue #208) — real callers should
    /// always pass the actual network regardless of whether
    /// `storage_snapshots` is populated.
    pub network: &'a str,
    /// New cursor value, when the page advanced it.
    pub cursor: Option<u64>,
    /// Ledger metadata row, written only when the cursor advanced.
    pub ledger: Option<LedgerMeta<'a>>,
    /// Maximum rows per INSERT statement, bounding statement size and memory.
    pub batch_size: usize,
}

/// Columns of a batch of events, transposed into the parallel arrays that the
/// `UNNEST` insert binds. Building this once avoids re-deriving per row.
struct EventColumns {
    ids: Vec<Uuid>,
    contract_ids: Vec<String>,
    ledger_sequences: Vec<i64>,
    ledger_timestamps: Vec<DateTime<Utc>>,
    transaction_hashes: Vec<String>,
    event_indexes: Vec<i32>,
    event_types: Vec<String>,
    topics: Vec<serde_json::Value>,
    data: Vec<serde_json::Value>,
}

fn event_type_str(event_type: &EventType) -> &'static str {
    match event_type {
        EventType::Contract => "contract",
        EventType::System => "system",
        EventType::Diagnostic => "diagnostic",
    }
}

impl EventColumns {
    fn build(events: &[SorobanEvent]) -> Result<Self, TridentError> {
        let mut cols = EventColumns {
            ids: Vec::with_capacity(events.len()),
            contract_ids: Vec::with_capacity(events.len()),
            ledger_sequences: Vec::with_capacity(events.len()),
            ledger_timestamps: Vec::with_capacity(events.len()),
            transaction_hashes: Vec::with_capacity(events.len()),
            event_indexes: Vec::with_capacity(events.len()),
            event_types: Vec::with_capacity(events.len()),
            topics: Vec::with_capacity(events.len()),
            data: Vec::with_capacity(events.len()),
        };

        for event in events {
            let ledger_ts: DateTime<Utc> = event.ledger_timestamp.parse().map_err(|e| {
                TridentError::storage(anyhow::Error::new(e).context("ledger timestamp parse"))
            })?;
            let topics = serde_json::to_value(&event.topics).map_err(|e| {
                TridentError::storage(anyhow::Error::new(e).context("topics serialise"))
            })?;

            cols.ids.push(event_uuid(
                &event.contract_id,
                event.ledger_sequence,
                event.event_index,
            ));
            cols.contract_ids.push(event.contract_id.clone());
            cols.ledger_sequences.push(event.ledger_sequence as i64);
            cols.ledger_timestamps.push(ledger_ts);
            cols.transaction_hashes.push(event.transaction_hash.clone());
            cols.event_indexes.push(event.event_index as i32);
            cols.event_types
                .push(event_type_str(&event.event_type).to_string());
            cols.topics.push(topics);
            cols.data.push(event.data.clone());
        }

        Ok(cols)
    }
}

/// Insert a batch of events in a single statement (issue #199).
///
/// One round-trip per batch instead of one per row. A replayed page inserts
/// nothing new: the deterministic UUIDv5 id reproduces the same
/// (ledger_sequence, id) pair, and `ON CONFLICT DO NOTHING` absorbs it.
///
/// The conflict clause is deliberately **untargeted** (issue #418).
/// `soroban_events` carries two unique constraints:
///
///   1. `PRIMARY KEY (ledger_sequence, id)` — from migration 0017, which made
///      the table RANGE-partitioned by ledger_sequence. PostgreSQL requires
///      every unique constraint on a partitioned table to include the
///      partition key, so a single-column PK on id alone no longer exists.
///   2. `uq_soroban_events_tx_index_network
///      (ledger_sequence, transaction_hash, event_index, network)` — from
///      migration 0025, mirroring the protocol guarantee that a
///      (transaction_hash, event_index) pair identifies exactly one event.
///
/// A targeted `ON CONFLICT (ledger_sequence, id)` only guards the first. That
/// is enough for a single writer replaying its own page, but not for two
/// indexer replicas committing the same page concurrently: the second writer's
/// rows collide on the natural key, which the targeted clause does not absorb,
/// and the whole batch fails with a unique-violation error rather than being
/// silently ignored. In production that aborts a poll cycle during any rollout
/// overlap or double deploy. Caught by
/// `concurrent_indexers_persist_each_event_exactly_once`.
///
/// Untargeted `DO NOTHING` covers both constraints. It does not weaken the
/// safety net migration 0025 describes: a genuine id-derivation bug still
/// cannot write a duplicate row, it is simply dropped rather than raised. The
/// natural-key constraint remains as the enforcement point, and 0025's
/// pre-flight duplicate check still runs against existing data.
///
/// `commit_page` no longer calls this directly — it goes through
/// [`insert_events_with_dead_letter`], which wraps the same statement with
/// bounded-retry-then-dead-letter fallback (issue #208). This is kept public
/// and exercised directly by the integration tests below, which validate the
/// batch-insert conflict/idempotency semantics independent of that fallback.
#[allow(dead_code)]
pub async fn insert_events_batch<'e, E>(
    executor: E,
    events: &[SorobanEvent],
) -> Result<(), TridentError>
where
    E: sqlx::PgExecutor<'e>,
{
    insert_events_batch_inner(executor, events)
        .await
        .map_err(|e| e.into_trident("insert_events_batch"))
}

/// A batch-insert failure, still carrying the underlying `sqlx::Error` (or
/// the column-encoding failure that preceded it) so the dead-letter path
/// (issue #208) can classify transient vs permanent before deciding whether
/// to retry. `insert_events_batch` collapses this into a `TridentError` for
/// every other caller.
enum InsertBatchError {
    /// Failed while building the columns to bind (e.g. an unparseable
    /// `ledger_timestamp`). Never transient — the same input reproduces the
    /// same failure every time.
    Encode(TridentError),
    Db(sqlx::Error),
    /// Failure managing the SAVEPOINT itself (issue #208) — SAVEPOINT,
    /// ROLLBACK TO, or RELEASE failed, which means the transaction's state
    /// is no longer trustworthy for further per-row attempts. Callers must
    /// propagate this immediately rather than falling back to dead-lettering,
    /// since dead-lettering itself needs a working transaction to write into.
    Fatal(TridentError),
}

impl InsertBatchError {
    fn into_trident(self, context: &str) -> TridentError {
        match self {
            InsertBatchError::Encode(e) => e,
            InsertBatchError::Fatal(e) => e,
            InsertBatchError::Db(e) => {
                TridentError::storage(anyhow::Error::new(e).context(context.to_string()))
            }
        }
    }

    /// Whether retrying the exact same insert has a chance of succeeding
    /// (issue #208). Connection-level failures and the two well-known
    /// Postgres SQLSTATE codes for a conflicting concurrent transaction are
    /// transient; everything else — constraint violations, data exceptions,
    /// and column-encoding failures — reproduces identically on retry.
    fn is_transient(&self) -> bool {
        match self {
            InsertBatchError::Encode(_) | InsertBatchError::Fatal(_) => false,
            InsertBatchError::Db(e) => is_transient_db_error(e),
        }
    }
}

/// Classify a `sqlx::Error` from an insert as transient (worth retrying) or
/// permanent (issue #208). `sqlx::Error::Database` carries the real Postgres
/// SQLSTATE via `.code()`: `40001` is a serialization failure and `40P01` is
/// a detected deadlock — both mean "some other transaction won a race, retry
/// against the now-settled state", not "this row is malformed". Every other
/// database error (constraint violations, data/type errors) is permanent:
/// retrying binds the identical values and gets the identical error.
/// Non-`Database` variants (`Io`, pool exhaustion/closure, a crashed worker)
/// are connection-level and always worth a retry.
fn is_transient_db_error(err: &sqlx::Error) -> bool {
    match err {
        sqlx::Error::Database(db_err) => {
            matches!(db_err.code().as_deref(), Some("40001") | Some("40P01"))
        }
        sqlx::Error::Io(_)
        | sqlx::Error::PoolTimedOut
        | sqlx::Error::PoolClosed
        | sqlx::Error::WorkerCrashed => true,
        _ => false,
    }
}

async fn insert_events_batch_inner<'e, E>(
    executor: E,
    events: &[SorobanEvent],
) -> Result<(), InsertBatchError>
where
    E: sqlx::PgExecutor<'e>,
{
    if events.is_empty() {
        return Ok(());
    }

    let cols = EventColumns::build(events).map_err(InsertBatchError::Encode)?;

    sqlx::query(
        r#"
        INSERT INTO soroban_events
            (id, contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
             event_index, event_type, topics, data)
        SELECT * FROM UNNEST(
            $1::uuid[], $2::text[], $3::bigint[], $4::timestamptz[], $5::text[],
            $6::int[], $7::text[], $8::jsonb[], $9::jsonb[]
        )
        ON CONFLICT DO NOTHING
        "#,
    )
    .bind(&cols.ids)
    .bind(&cols.contract_ids)
    .bind(&cols.ledger_sequences)
    .bind(&cols.ledger_timestamps)
    .bind(&cols.transaction_hashes)
    .bind(&cols.event_indexes)
    .bind(&cols.event_types)
    .bind(&cols.topics)
    .bind(&cols.data)
    .execute(executor)
    .await
    .map_err(InsertBatchError::Db)?;

    Ok(())
}

/// Bounded retry attempts for a chunk (or, in fallback, a single row) insert
/// before it is treated as exhausted (issue #208). Three attempts with
/// 100/200/400ms backoff ride out a brief connection blip or serialization
/// conflict without stalling the poll loop for long.
const INSERT_DEAD_LETTER_RETRIES: u32 = 3;

async fn savepoint(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    op: &str,
) -> Result<(), TridentError> {
    sqlx::query("SAVEPOINT trident_dead_letter")
        .execute(&mut **tx)
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context(op.to_string())))?;
    Ok(())
}

async fn release_savepoint(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    op: &str,
) -> Result<(), TridentError> {
    sqlx::query("RELEASE SAVEPOINT trident_dead_letter")
        .execute(&mut **tx)
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context(op.to_string())))?;
    Ok(())
}

async fn rollback_to_savepoint(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    op: &str,
) -> Result<(), TridentError> {
    sqlx::query("ROLLBACK TO SAVEPOINT trident_dead_letter")
        .execute(&mut **tx)
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context(op.to_string())))?;
    Ok(())
}

/// Try `insert_events_batch_inner` up to [`INSERT_DEAD_LETTER_RETRIES`] times,
/// wrapping each attempt in its own SAVEPOINT so a failed attempt does not
/// poison the rest of the caller's transaction. Retries only continue while
/// the failure is classified transient; a permanent failure returns after the
/// first attempt. Returns the last error on exhaustion.
async fn try_insert_with_retries(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    events: &[SorobanEvent],
    op: &str,
) -> Result<(), InsertBatchError> {
    let mut last_err = None;
    for attempt in 0..INSERT_DEAD_LETTER_RETRIES {
        if attempt > 0 {
            if let Some(err) = &last_err {
                if !InsertBatchError::is_transient(err) {
                    break;
                }
            }
        }

        savepoint(tx, op).await.map_err(InsertBatchError::Fatal)?;

        match insert_events_batch_inner(&mut **tx, events).await {
            Ok(()) => {
                release_savepoint(tx, op)
                    .await
                    .map_err(InsertBatchError::Fatal)?;
                return Ok(());
            }
            Err(e) => {
                rollback_to_savepoint(tx, op)
                    .await
                    .map_err(InsertBatchError::Fatal)?;
                last_err = Some(e);
            }
        }
    }
    Err(last_err.expect("loop always runs at least once"))
}

/// Insert one page's events with bounded-retry-then-dead-letter fallback
/// (issue #208).
///
/// `insert_events_batch`'s `UNNEST` statement is all-or-nothing: one poison
/// row anywhere in `events` fails the whole chunk alongside it. Before this,
/// that failure propagated straight out of [`commit_page`] via `?`, aborting
/// the entire poll cycle — and since nothing about the poison row changes
/// between polls, the next poll hit the identical failure on the identical
/// event, wedging the cursor forever.
///
/// This retries the whole chunk first (useful only for a transient failure —
/// a connection blip or serialization conflict). If the chunk still fails
/// after retries — or the failure was permanent from the start — it falls
/// back to inserting one row at a time, each in its own bounded retry. A row
/// that still fails is written to `failed_events` and skipped; every other
/// row in the chunk still commits, and the caller can advance the cursor
/// past the page.
///
/// Returns the ids of any events that were dead-lettered, so the caller can
/// exclude them from dependent projections (token_events) that would
/// otherwise reference a `soroban_events` row that was never written.
async fn insert_events_with_dead_letter(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    network: &str,
    events: &[SorobanEvent],
) -> Result<HashSet<Uuid>, TridentError> {
    if events.is_empty() {
        return Ok(HashSet::new());
    }

    match try_insert_with_retries(tx, events, "insert_events_with_dead_letter chunk").await {
        Ok(()) => return Ok(HashSet::new()),
        // The SAVEPOINT machinery itself is broken — the transaction can no
        // longer be trusted for a per-row fallback (which also needs
        // SAVEPOINTs), so this must propagate like any other fatal failure
        // did before this feature existed.
        Err(InsertBatchError::Fatal(e)) => return Err(e),
        Err(_) => {} // chunk-level insert exhausted its retries; fall through.
    }

    // The chunk did not succeed as a unit: fall back to per-row isolation so
    // the poison row(s) cannot block their siblings.
    let mut dead_lettered = HashSet::new();
    for event in events {
        let one = std::slice::from_ref(event);
        match try_insert_with_retries(tx, one, "insert_events_with_dead_letter row").await {
            Ok(()) => {}
            Err(InsertBatchError::Fatal(e)) => return Err(e),
            Err(err) => {
                let id = event_uuid(&event.contract_id, event.ledger_sequence, event.event_index);
                let trident_err = err.into_trident("insert_events_with_dead_letter row");
                insert_failed_event(&mut **tx, network, id, event, &trident_err).await?;
                crate::metrics::record_insert_dead_lettered();
                dead_lettered.insert(id);
            }
        }
    }
    Ok(dead_lettered)
}

/// Write an event that exhausted its insert retries to `failed_events`
/// (issue #208), for operator inspection and manual replay. Keyed by the
/// same deterministic event id as `soroban_events`, so a repeat failure on a
/// later poll (the event has not yet been fixed/replayed) updates the
/// attempt count and latest error instead of accumulating duplicate rows.
async fn insert_failed_event<'e, E>(
    executor: E,
    network: &str,
    event_id: Uuid,
    event: &SorobanEvent,
    error: &TridentError,
) -> Result<(), TridentError>
where
    E: sqlx::PgExecutor<'e>,
{
    let payload = serde_json::json!({
        "contract_id": event.contract_id,
        "topics": event.topics,
        "data": event.data,
        "ledger_sequence": event.ledger_sequence,
        "ledger_timestamp": event.ledger_timestamp,
        "transaction_hash": event.transaction_hash,
        "event_index": event.event_index,
    });

    sqlx::query(
        r#"
        INSERT INTO failed_events
            (event_id, contract_id, network, ledger_sequence, transaction_hash,
             event_index, payload, error_message, attempts)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1)
        ON CONFLICT (event_id) DO UPDATE SET
            attempts = failed_events.attempts + 1,
            error_message = EXCLUDED.error_message,
            payload = EXCLUDED.payload,
            last_seen_at = NOW()
        "#,
    )
    .bind(event_id)
    .bind(&event.contract_id)
    .bind(network)
    .bind(event.ledger_sequence as i64)
    .bind(&event.transaction_hash)
    .bind(event.event_index as i32)
    .bind(payload)
    .bind(error.to_string())
    .execute(executor)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("insert_failed_event")))?;

    Ok(())
}

/// Insert a batch of decoded token events into the `token_events` projection
/// (issue #211).
///
/// Keyed by the originating event's UUID, so replaying a page re-projects the
/// same rows and `ON CONFLICT DO NOTHING` absorbs them.
pub async fn insert_token_events_batch<'e, E>(
    executor: E,
    projections: &[TokenProjection<'_>],
) -> Result<(), TridentError>
where
    E: sqlx::PgExecutor<'e>,
{
    if projections.is_empty() {
        return Ok(());
    }

    let mut event_ids = Vec::with_capacity(projections.len());
    let mut contract_ids = Vec::with_capacity(projections.len());
    let mut event_types = Vec::with_capacity(projections.len());
    let mut from_addresses = Vec::with_capacity(projections.len());
    let mut to_addresses = Vec::with_capacity(projections.len());
    let mut spender_addresses = Vec::with_capacity(projections.len());
    let mut admin_addresses = Vec::with_capacity(projections.len());
    let mut amounts = Vec::with_capacity(projections.len());
    let mut expiration_ledgers = Vec::with_capacity(projections.len());
    let mut asset_codes = Vec::with_capacity(projections.len());
    let mut asset_issuers = Vec::with_capacity(projections.len());
    let mut ledger_sequences = Vec::with_capacity(projections.len());
    let mut ledger_timestamps = Vec::with_capacity(projections.len());
    let mut transaction_hashes = Vec::with_capacity(projections.len());
    let mut event_indexes = Vec::with_capacity(projections.len());

    for projection in projections {
        let event = projection.event;
        let token = projection.token;
        let ledger_ts: DateTime<Utc> = event.ledger_timestamp.parse().map_err(|e| {
            TridentError::storage(anyhow::Error::new(e).context("ledger timestamp parse"))
        })?;

        event_ids.push(event_uuid(
            &event.contract_id,
            event.ledger_sequence,
            event.event_index,
        ));
        contract_ids.push(event.contract_id.clone());
        event_types.push(token.event_type.as_str().to_string());
        from_addresses.push(token.from.clone());
        to_addresses.push(token.to.clone());
        spender_addresses.push(token.spender.clone());
        admin_addresses.push(token.admin.clone());
        amounts.push(token.amount.clone());
        expiration_ledgers.push(token.expiration_ledger);
        asset_codes.push(token.asset_code.clone());
        asset_issuers.push(token.asset_issuer.clone());
        ledger_sequences.push(event.ledger_sequence as i64);
        ledger_timestamps.push(ledger_ts);
        transaction_hashes.push(event.transaction_hash.clone());
        event_indexes.push(event.event_index as i32);
    }

    sqlx::query(
        r#"
        INSERT INTO token_events
            (event_id, contract_id, event_type, from_address, to_address,
             spender_address, admin_address, amount, expiration_ledger,
             asset_code, asset_issuer,
             ledger_sequence, ledger_timestamp, transaction_hash, event_index)
        SELECT * FROM UNNEST(
            $1::uuid[], $2::text[], $3::text[], $4::text[], $5::text[],
            $6::text[], $7::text[], $8::text[], $9::bigint[],
            $10::text[], $11::text[],
            $12::bigint[], $13::timestamptz[], $14::text[], $15::int[]
        )
        ON CONFLICT (event_id) DO NOTHING
        "#,
    )
    .bind(&event_ids)
    .bind(&contract_ids)
    .bind(&event_types)
    .bind(&from_addresses)
    .bind(&to_addresses)
    .bind(&spender_addresses)
    .bind(&admin_addresses)
    .bind(&amounts)
    .bind(&expiration_ledgers)
    .bind(&asset_codes)
    .bind(&asset_issuers)
    .bind(&ledger_sequences)
    .bind(&ledger_timestamps)
    .bind(&transaction_hashes)
    .bind(&event_indexes)
    .execute(executor)
    .await
    .map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("insert_token_events_batch"))
    })?;

    Ok(())
}

/// Insert a batch of per-invocation fee/resource metering rows into
/// `contract_invocation_metrics` (issue #266).
///
/// Keyed by `(contract_id, transaction_hash)`; a replayed page inserts
/// nothing new via `ON CONFLICT DO NOTHING`, matching the idempotency of the
/// other page-scoped inserts.
pub async fn insert_invocation_metrics_batch<'e, E>(
    executor: E,
    rows: &[InvocationMetricRow<'_>],
) -> Result<(), TridentError>
where
    E: sqlx::PgExecutor<'e>,
{
    if rows.is_empty() {
        return Ok(());
    }

    let mut contract_ids = Vec::with_capacity(rows.len());
    let mut transaction_hashes = Vec::with_capacity(rows.len());
    let mut ledger_sequences = Vec::with_capacity(rows.len());
    let mut ledger_timestamps = Vec::with_capacity(rows.len());
    let mut fee_charged = Vec::with_capacity(rows.len());
    let mut resource_fee = Vec::with_capacity(rows.len());
    let mut cpu_instructions = Vec::with_capacity(rows.len());
    let mut read_bytes = Vec::with_capacity(rows.len());
    let mut write_bytes = Vec::with_capacity(rows.len());
    let mut provenance = Vec::with_capacity(rows.len());

    for row in rows {
        let ledger_ts: DateTime<Utc> = row.ledger_timestamp.parse().map_err(|e| {
            TridentError::storage(anyhow::Error::new(e).context("ledger timestamp parse"))
        })?;

        contract_ids.push(row.contract_id.to_string());
        transaction_hashes.push(row.transaction_hash.to_string());
        ledger_sequences.push(row.ledger_sequence as i64);
        ledger_timestamps.push(ledger_ts);
        fee_charged.push(row.metrics.fee_charged);
        resource_fee.push(row.metrics.resource_fee);
        cpu_instructions.push(row.metrics.cpu_instructions);
        read_bytes.push(row.metrics.read_bytes);
        write_bytes.push(row.metrics.write_bytes);
        provenance.push(row.metrics.provenance.to_string());
    }

    sqlx::query(
        r#"
        INSERT INTO contract_invocation_metrics
            (contract_id, transaction_hash, ledger_sequence, ledger_timestamp,
             fee_charged, resource_fee, cpu_instructions, read_bytes, write_bytes, provenance)
        SELECT * FROM UNNEST(
            $1::text[], $2::text[], $3::bigint[], $4::timestamptz[],
            $5::bigint[], $6::bigint[], $7::bigint[], $8::bigint[], $9::bigint[], $10::text[]
        )
        ON CONFLICT (contract_id, transaction_hash) DO NOTHING
        "#,
    )
    .bind(&contract_ids)
    .bind(&transaction_hashes)
    .bind(&ledger_sequences)
    .bind(&ledger_timestamps)
    .bind(&fee_charged)
    .bind(&resource_fee)
    .bind(&cpu_instructions)
    .bind(&read_bytes)
    .bind(&write_bytes)
    .bind(&provenance)
    .execute(executor)
    .await
    .map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("insert_invocation_metrics_batch"))
    })?;

    Ok(())
}

/// Insert the outbox rows for a batch of events in a single statement (#200).
///
/// Mirrors [`insert_events_batch`]: same deterministic ids, same idempotency.
/// `ON CONFLICT (event_id) DO NOTHING` means a replayed page does not re-queue
/// a delivery for an event that already has one.
pub async fn insert_outbox_batch<'e, E>(
    executor: E,
    events: &[SorobanEvent],
) -> Result<(), TridentError>
where
    E: sqlx::PgExecutor<'e>,
{
    if events.is_empty() {
        return Ok(());
    }

    let mut ids: Vec<Uuid> = Vec::with_capacity(events.len());
    let mut payloads: Vec<serde_json::Value> = Vec::with_capacity(events.len());
    for event in events {
        ids.push(event_uuid(
            &event.contract_id,
            event.ledger_sequence,
            event.event_index,
        ));
        payloads.push(serde_json::to_value(event).map_err(|e| {
            TridentError::storage(anyhow::Error::new(e).context("outbox payload serialise"))
        })?);
    }

    sqlx::query(
        r#"
        INSERT INTO event_outbox (event_id, payload)
        SELECT * FROM UNNEST($1::uuid[], $2::jsonb[])
        ON CONFLICT (event_id) DO NOTHING
        "#,
    )
    .bind(&ids)
    .bind(&payloads)
    .execute(executor)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("insert_outbox_batch")))?;

    Ok(())
}

/// Persist (or refresh) a tracked contract's parsed spec + detected
/// interfaces, keyed by `(contract_id, network)` (issues #260, #269).
/// Called only when the observed code hash differs from the last one synced,
/// so a redeploy refreshes the row instead of leaving it stale.
pub async fn upsert_contract_spec(
    pool: &PgPool,
    contract_id: &str,
    network: &str,
    spec: &crate::spec::ContractSpec,
) -> Result<(), TridentError> {
    let functions = serde_json::to_value(&spec.functions).map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("serialise spec functions"))
    })?;
    let interfaces = serde_json::to_value(&spec.interfaces).map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("serialise spec interfaces"))
    })?;

    sqlx::query(
        r#"
        INSERT INTO contract_specs
            (contract_id, network, code_hash, has_spec, functions, contract_type, interfaces)
        VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb)
        ON CONFLICT (contract_id, network) DO UPDATE SET
            code_hash     = EXCLUDED.code_hash,
            has_spec      = EXCLUDED.has_spec,
            functions     = EXCLUDED.functions,
            contract_type = EXCLUDED.contract_type,
            interfaces    = EXCLUDED.interfaces,
            updated_at    = NOW()
        "#,
    )
    .bind(contract_id)
    .bind(network)
    .bind(&spec.code_hash)
    .bind(spec.has_spec)
    .bind(functions)
    .bind(&spec.contract_type)
    .bind(interfaces)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("upsert_contract_spec")))?;

    Ok(())
}

/// Latest persisted value for a single contract-storage key, if any (issue
/// #270). Used to detect whether a freshly observed value has changed before
/// writing a new snapshot row.
pub async fn get_latest_storage_value(
    pool: &PgPool,
    contract_id: &str,
    network: &str,
    storage_key: &str,
) -> Result<Option<serde_json::Value>, TridentError> {
    let row: Option<(Option<serde_json::Value>,)> = sqlx::query_as(
        r#"
        SELECT value_json FROM contract_storage_snapshots
        WHERE contract_id = $1 AND network = $2 AND storage_key = $3
        ORDER BY ledger_sequence DESC
        LIMIT 1
        "#,
    )
    .bind(contract_id)
    .bind(network)
    .bind(storage_key)
    .fetch_optional(pool)
    .await
    .map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("get_latest_storage_value"))
    })?;

    Ok(row.and_then(|(v,)| v))
}

/// One contract-storage snapshot row, ready to persist (issue #270).
pub struct StorageSnapshotRow<'a> {
    pub contract_id: &'a str,
    pub storage_key: &'a str,
    pub key_json: &'a serde_json::Value,
    pub value_json: Option<&'a serde_json::Value>,
    pub ledger_sequence: u64,
}

/// Insert a batch of contract-storage snapshot changes (issue #270).
/// Callers are expected to have already diffed against
/// [`get_latest_storage_value`] — this only appends, it never overwrites a
/// prior snapshot, so historical values stay queryable.
pub async fn insert_storage_snapshots_batch<'e, E>(
    executor: E,
    network: &str,
    rows: &[StorageSnapshotRow<'_>],
) -> Result<(), TridentError>
where
    E: sqlx::PgExecutor<'e>,
{
    if rows.is_empty() {
        return Ok(());
    }

    let mut contract_ids = Vec::with_capacity(rows.len());
    let mut networks = Vec::with_capacity(rows.len());
    let mut storage_keys = Vec::with_capacity(rows.len());
    let mut key_jsons = Vec::with_capacity(rows.len());
    let mut value_jsons: Vec<Option<serde_json::Value>> = Vec::with_capacity(rows.len());
    let mut ledger_sequences = Vec::with_capacity(rows.len());

    for row in rows {
        contract_ids.push(row.contract_id.to_string());
        networks.push(network.to_string());
        storage_keys.push(row.storage_key.to_string());
        key_jsons.push(row.key_json.clone());
        value_jsons.push(row.value_json.cloned());
        ledger_sequences.push(row.ledger_sequence as i64);
    }

    sqlx::query(
        r#"
        INSERT INTO contract_storage_snapshots
            (contract_id, network, storage_key, key_json, value_json, ledger_sequence)
        SELECT * FROM UNNEST(
            $1::text[], $2::text[], $3::text[], $4::jsonb[], $5::jsonb[], $6::bigint[]
        )
        ON CONFLICT (contract_id, network, storage_key, ledger_sequence) DO NOTHING
        "#,
    )
    .bind(&contract_ids)
    .bind(&networks)
    .bind(&storage_keys)
    .bind(&key_jsons)
    .bind(&value_jsons)
    .bind(&ledger_sequences)
    .execute(executor)
    .await
    .map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("insert_storage_snapshots_batch"))
    })?;

    Ok(())
}

/// Persist one RPC page — events, outbox rows, cursor, and ledger metadata — in
/// a single transaction (issues #199, #200).
///
/// Events are chunked to `batch_size` so a very large page cannot produce an
/// unbounded statement, but every chunk shares the one transaction: either the
/// whole page and its cursor advance land, or none of it does — with one
/// deliberate exception (issue #208): an individual event that exhausts its
/// bounded insert retries is dead-lettered into `failed_events` rather than
/// failing the whole page, so a single poison row cannot wedge the cursor.
pub async fn commit_page(pool: &PgPool, commit: PageCommit<'_>) -> Result<(), TridentError> {
    let batch_size = commit.batch_size.max(1);

    let mut tx = pool
        .begin()
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("commit_page begin")))?;

    // Events insert with bounded-retry-then-dead-letter fallback (issue
    // #208): a poison row that survives retries is written to
    // `failed_events` instead of aborting the whole page, and its id is
    // collected so the projections below can skip it.
    let mut dead_lettered: HashSet<Uuid> = HashSet::new();
    for chunk in commit.events.chunks(batch_size) {
        let chunk_dead = insert_events_with_dead_letter(&mut tx, commit.network, chunk).await?;
        // Outbox rows ride the same transaction as the events they deliver
        // (issue #200): either both land or neither does, so a committed event
        // can never exist without a delivery record for the relay to pick up.
        // A dead-lettered event has no soroban_events row, so it must not get
        // an outbox row either — that would let the relay publish a payload
        // the DB never actually has.
        if chunk_dead.is_empty() {
            insert_outbox_batch(&mut *tx, chunk).await?;
        } else {
            let landed: Vec<SorobanEvent> = chunk
                .iter()
                .filter(|e| {
                    !chunk_dead.contains(&event_uuid(
                        &e.contract_id,
                        e.ledger_sequence,
                        e.event_index,
                    ))
                })
                .cloned()
                .collect();
            insert_outbox_batch(&mut *tx, &landed).await?;
        }
        dead_lettered.extend(chunk_dead);
    }

    // token_events.event_id logically references soroban_events(id) (the DB-level
    // FK was dropped in migration 0017 — soroban_events is partitioned, so a
    // single-column UNIQUE (id) can't be enforced globally). Referential
    // integrity is instead upheld here: projection rows must follow the event
    // insert inside the same transaction, so a token_events row can never exist
    // without its corresponding soroban_events row already committed. A
    // dead-lettered event (issue #208) never got that soroban_events row, so
    // its projection is excluded here too.
    let token_events_owned;
    let token_events: &[TokenProjection<'_>] = if dead_lettered.is_empty() {
        commit.token_events
    } else {
        token_events_owned = commit
            .token_events
            .iter()
            .filter(|p| {
                !dead_lettered.contains(&event_uuid(
                    &p.event.contract_id,
                    p.event.ledger_sequence,
                    p.event.event_index,
                ))
            })
            .map(|p| TokenProjection {
                event: p.event,
                token: p.token,
            })
            .collect::<Vec<_>>();
        &token_events_owned
    };
    for chunk in token_events.chunks(batch_size) {
        insert_token_events_batch(&mut *tx, chunk).await?;
    }

    // Invocation metrics ride the same transaction as the page they were
    // derived from (issue #266), same idempotency contract as the rest.
    for chunk in commit.invocation_metrics.chunks(batch_size) {
        insert_invocation_metrics_batch(&mut *tx, chunk).await?;
    }

    // Storage snapshot changes ride the same transaction as the page that
    // observed them (issue #270).
    for chunk in commit.storage_snapshots.chunks(batch_size) {
        insert_storage_snapshots_batch(&mut *tx, commit.network, chunk).await?;
    }

    if let Some(cursor) = commit.cursor {
        // Monotonic cursor advance (issue #418). Two indexer replicas — by
        // deliberate scale-out or an accidental double deploy mid-rollout —
        // can commit overlapping pages concurrently. An unconditional
        // `SET value = $1` lets the slower writer's older cursor land last and
        // rewind the cursor behind data that is already committed, so the
        // replicas re-poll a range they have both already indexed.
        //
        // The `WHERE ... < $1` guard makes the advance a no-op when the stored
        // cursor is already at or ahead of this page: the row is only written
        // when it genuinely moves forward. The comparison is numeric, not the
        // lexicographic ordering the TEXT column would otherwise give ("9" >
        // "10"), so it stays correct across digit-count boundaries.
        //
        // `UPDATE` takes a row lock, so concurrent advances on this single row
        // serialise: the second transaction blocks until the first commits,
        // then re-evaluates the guard against the committed value rather than
        // the snapshot it started with.
        sqlx::query(
            r#"
            UPDATE system_state
            SET value = $1, updated_at = NOW()
            WHERE key = 'latest_ledger_cursor'
              AND (value ~ '^[0-9]+$' IS NOT TRUE OR value::numeric < $2)
            "#,
        )
        .bind(cursor.to_string())
        .bind(cursor as i64)
        .execute(&mut *tx)
        .await
        .map_err(|e| {
            TridentError::storage(anyhow::Error::new(e).context("commit_page set_cursor"))
        })?;
    }

    if let Some(ledger) = commit.ledger {
        let ts: DateTime<Utc> = ledger.timestamp.parse().map_err(|e| {
            TridentError::storage(anyhow::Error::new(e).context("ledger timestamp parse"))
        })?;

        sqlx::query(
            r#"
            INSERT INTO ledger_metadata (ledger_sequence, ledger_hash, ledger_timestamp, event_count)
            VALUES ($1, $2, $3, $4)
            ON CONFLICT (ledger_sequence) DO NOTHING
            "#,
        )
        .bind(ledger.sequence as i64)
        .bind(ledger.hash)
        .bind(ts)
        .bind(ledger.event_count)
        .execute(&mut *tx)
        .await
        .map_err(|e| {
            TridentError::storage(anyhow::Error::new(e).context("commit_page ledger_metadata"))
        })?;
    }

    tx.commit()
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("commit_page commit")))?;

    Ok(())
}

/// Read the latest processed ledger cursor from system_state.
pub async fn get_cursor(pool: &PgPool) -> Result<u64, TridentError> {
    let row: (String,) =
        sqlx::query_as("SELECT value FROM system_state WHERE key = 'latest_ledger_cursor'")
            .fetch_one(pool)
            .await
            .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("get_cursor")))?;

    row.0
        .parse::<u64>()
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("cursor parse")))
}

/// Write indexer health metrics into the `system_state` health columns after
/// every successful poll cycle (issue #62).
///
/// Uses a single `UPDATE` on the known cursor row so there is never a
/// duplicate-key issue and the write is O(1) regardless of table size.
pub async fn update_health_stats(
    pool: &PgPool,
    last_ledger: i64,
    events_in_poll: i32,
    poll_duration: Duration,
) -> Result<(), TridentError> {
    let poll_ms = poll_duration.as_millis().min(i32::MAX as u128) as i32;

    sqlx::query(
        r#"
        UPDATE system_state
        SET
            last_poll_at          = NOW(),
            last_ledger_indexed   = $1,
            events_in_last_poll   = $2,
            poll_duration_ms      = $3,
            events_indexed_total  = COALESCE(events_indexed_total, 0) + $2,
            updated_at            = NOW()
        WHERE key = 'latest_ledger_cursor'
        "#,
    )
    .bind(last_ledger)
    .bind(events_in_poll)
    .bind(poll_ms)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("update_health_stats")))?;

    Ok(())
}

/// Load all contract IDs and their `index_from` values from
/// `indexed_contracts` for the given network (or network-agnostic rows where
/// `network IS NULL`).
///
/// Returns an empty map if the table has no rows — the caller treats an empty
/// map as "index all contracts" (issue #47).
///
/// `index_from` (BIGINT, default 0, added in 0001_init.sql) is the first
/// ledger from which events for that contract should be indexed. Events below
/// it are skipped client-side, because `getEvents` carries a single
/// `startLedger` for the whole request and so cannot express a per-contract
/// boundary (issue #202).
pub async fn load_indexed_contracts(
    pool: &PgPool,
    network: &str,
) -> Result<HashMap<String, i64>, TridentError> {
    let rows: Vec<(String, i64)> = sqlx::query_as(
        "SELECT contract_id, index_from FROM indexed_contracts WHERE network = $1 OR network IS NULL",
    )
    .bind(network)
    .fetch_all(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("load_indexed_contracts")))?;

    Ok(rows.into_iter().collect())
}

/// Read alert state (last_alert_at, alert_fired) from system_state (issue #75).
pub async fn get_alert_state(pool: &PgPool) -> Result<crate::alerting::AlertState, TridentError> {
    let row: (Option<chrono::DateTime<chrono::Utc>>, bool) = sqlx::query_as(
        "SELECT last_alert_at, alert_fired FROM system_state WHERE key = 'latest_ledger_cursor'",
    )
    .fetch_one(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("get_alert_state")))?;

    Ok(crate::alerting::AlertState {
        last_alert_at: row.0,
        alert_fired: row.1,
        rpc_degraded_fired: false,
        rpc_degraded_last_alert_at: None,
    })
}

/// Persist alert state back to system_state after an alerting evaluation (issue #75).
pub async fn set_alert_state(
    pool: &PgPool,
    state: &crate::alerting::AlertState,
) -> Result<(), TridentError> {
    sqlx::query(
        r#"
        UPDATE system_state
        SET last_alert_at = $1,
            alert_fired   = $2,
            updated_at    = NOW()
        WHERE key = 'latest_ledger_cursor'
        "#,
    )
    .bind(state.last_alert_at)
    .bind(state.alert_fired)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("set_alert_state")))?;

    Ok(())
}

/// Record a parse error to parse_errors table for auditing and potential replay.
pub async fn insert_parse_error(
    pool: &PgPool,
    ledger_sequence: u64,
    event_index: u32,
    raw_payload: &str,
    error_message: &str,
) -> Result<(), TridentError> {
    sqlx::query(
        r#"
        INSERT INTO parse_errors
            (ledger_sequence, event_index, raw_payload, error_message)
        VALUES ($1, $2, $3, $4)
        "#,
    )
    .bind(ledger_sequence as i64)
    .bind(event_index as i32)
    .bind(raw_payload)
    .bind(error_message)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("insert_parse_error")))?;

    Ok(())
}

/// Contracts among `contract_ids` whose `token_metadata` row is still fresh
/// (resolved or refreshed since `cutoff`), for either a positive or a cached
/// negative ("not a token") result (issue #263). Contracts absent from this
/// set need a fresh resolution attempt.
pub async fn fresh_token_metadata_contract_ids(
    pool: &PgPool,
    contract_ids: &[String],
    network: &str,
    cutoff: DateTime<Utc>,
) -> Result<HashSet<String>, TridentError> {
    if contract_ids.is_empty() {
        return Ok(HashSet::new());
    }

    let rows: Vec<(String,)> = sqlx::query_as(
        "SELECT contract_id FROM token_metadata
         WHERE contract_id = ANY($1) AND network = $2 AND updated_at > $3",
    )
    .bind(contract_ids)
    .bind(network)
    .bind(cutoff)
    .fetch_all(pool)
    .await
    .map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("fresh_token_metadata_contract_ids"))
    })?;

    Ok(rows.into_iter().map(|(id,)| id).collect())
}

/// Cache a resolved (or negative) token metadata result for one contract
/// (issue #263). Re-resolving an already-cached contract refreshes the row in
/// place rather than duplicating it.
pub async fn upsert_token_metadata(
    pool: &PgPool,
    contract_id: &str,
    network: &str,
    resolution: &crate::token_metadata::TokenMetadataResolution,
) -> Result<(), TridentError> {
    let (name, symbol, decimals, is_token) = match resolution {
        crate::token_metadata::TokenMetadataResolution::Token(meta) => (
            Some(meta.name.as_str()),
            Some(meta.symbol.as_str()),
            Some(meta.decimals as i32),
            true,
        ),
        crate::token_metadata::TokenMetadataResolution::NotAToken => (None, None, None, false),
    };

    sqlx::query(
        r#"
        INSERT INTO token_metadata (contract_id, network, name, symbol, decimals, is_token)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (contract_id, network) DO UPDATE SET
            name       = EXCLUDED.name,
            symbol     = EXCLUDED.symbol,
            decimals   = EXCLUDED.decimals,
            is_token   = EXCLUDED.is_token,
            updated_at = NOW()
        "#,
    )
    .bind(contract_id)
    .bind(network)
    .bind(name)
    .bind(symbol)
    .bind(decimals)
    .bind(is_token)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("upsert_token_metadata")))?;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use trident_common::{EventType, SorobanEvent};

    fn make_event(contract_id: &str, ledger_sequence: u64, event_index: u32) -> SorobanEvent {
        SorobanEvent {
            contract_id: contract_id.to_string(),
            ledger_sequence,
            ledger_timestamp: "2024-01-01T00:00:00Z".to_string(),
            transaction_hash: "txhash_abc123".to_string(),
            event_index,
            event_type: EventType::Contract,
            topics: vec![],
            data: json!({}),
        }
    }

    /// Deterministic UUID: same inputs must produce the same id.
    #[test]
    fn event_uuid_is_deterministic() {
        let a = event_uuid("CABC", 100, 0);
        let b = event_uuid("CABC", 100, 0);
        assert_eq!(a, b);
    }

    /// Different natural keys must produce different UUIDs.
    #[test]
    fn event_uuid_varies_with_inputs() {
        let a = event_uuid("CABC", 100, 0);
        let b = event_uuid("CABC", 100, 1);
        assert_ne!(a, b);
    }

    // -----------------------------------------------------------------------
    // Insert-failure classification (issue #208)
    // -----------------------------------------------------------------------

    #[test]
    fn connection_level_failures_are_transient() {
        assert!(is_transient_db_error(&sqlx::Error::PoolClosed));
        assert!(is_transient_db_error(&sqlx::Error::PoolTimedOut));
        assert!(is_transient_db_error(&sqlx::Error::WorkerCrashed));
    }

    #[test]
    fn non_database_non_connection_errors_are_not_transient() {
        // RowNotFound is neither a connection-level failure nor a
        // sqlx::Error::Database — it must not be retried.
        assert!(!is_transient_db_error(&sqlx::Error::RowNotFound));
    }

    #[test]
    fn encode_failures_are_never_transient() {
        // A column-encoding failure (e.g. an unparseable ledger_timestamp)
        // reproduces identically on every retry — retrying is pointless.
        let err = InsertBatchError::Encode(TridentError::storage(anyhow::anyhow!("bad input")));
        assert!(!err.is_transient());
    }

    /// Committing the same event twice must not error and the row count in
    /// `soroban_events` must remain 1.
    ///
    /// Uses the shared test database (TEST_DATABASE_URL) like the other
    /// integration tests; skips when it is not configured.
    #[tokio::test]
    async fn batch_insert_is_idempotent() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            // Hard-fail under the rust-integration CI job (REQUIRE_TEST_SERVICES)
            // so a misconfigured DB URL cannot silently skip and go green.
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();

        let event = make_event("CABC_CONTRACT_001", 42, 0);

        // Isolate from other tests sharing the database.
        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&event.contract_id)
            .execute(&pool)
            .await
            .expect("cleanup failed");

        let events = [event.clone()];
        insert_events_batch(&pool, &events)
            .await
            .expect("first insert failed");
        insert_events_batch(&pool, &events)
            .await
            .expect("second insert must not error");

        let count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE contract_id = $1")
                .bind(&event.contract_id)
                .fetch_one(&pool)
                .await
                .expect("count query failed");

        assert_eq!(count.0, 1, "duplicate insert should be silently ignored");
    }

    /// An empty batch must be a no-op rather than an invalid statement.
    #[tokio::test]
    async fn empty_batch_is_a_noop() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();
        insert_events_batch(&pool, &[])
            .await
            .expect("empty batch must succeed");
    }

    /// A page larger than `batch_size` must still land in full — chunking splits
    /// the statement, not the transaction — and a replay must insert nothing new.
    #[tokio::test]
    async fn commit_page_chunks_large_pages_and_advances_cursor_once() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();

        let contract_id = format!("CBATCH_{}", Uuid::new_v4());
        let events: Vec<SorobanEvent> = (0..25).map(|i| make_event(&contract_id, 900, i)).collect();

        fn commit(events: &[SorobanEvent]) -> PageCommit<'_> {
            PageCommit {
                events,
                token_events: &[],
                invocation_metrics: &[],
                storage_snapshots: &[],
                network: "testnet",
                cursor: Some(900),
                ledger: Some(LedgerMeta {
                    sequence: 900,
                    hash: "hash900",
                    timestamp: "2024-01-01T00:00:00Z",
                    event_count: events.len() as i32,
                }),
                // Deliberately smaller than the page so chunking is exercised.
                batch_size: 10,
            }
        }

        commit_page(&pool, commit(&events))
            .await
            .expect("commit_page failed");

        let count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(count.0, 25, "every chunk of the page must land");
        assert_eq!(get_cursor(&pool).await.unwrap(), 900);

        commit_page(&pool, commit(&events))
            .await
            .expect("replay failed");

        let recount: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(recount.0, 25, "replaying a page must insert nothing new");

        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();
    }

    /// A page containing one poison event — one with a `ledger_timestamp`
    /// that can never parse — must not abort the whole page. The poison
    /// event is dead-lettered into `failed_events`; its well-formed siblings
    /// still land in `soroban_events`; and the cursor still advances past
    /// the page (issue #208). Before this feature, `commit_page` propagated
    /// the very first `EventColumns::build` failure via `?`, failing the
    /// entire batch — including the four good events alongside it — and the
    /// next poll would hit the identical failure on the identical event,
    /// wedging the cursor forever.
    #[tokio::test]
    async fn poison_event_is_dead_lettered_and_siblings_still_commit() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();

        let contract_id = format!("CDEADLETTER_{}", Uuid::new_v4());
        // The cursor row in `system_state` is shared, monotonic, global state
        // across this whole test module — other tests in the suite may have
        // already advanced it well past any hardcoded ledger sequence. Base
        // this test's sequence on the current cursor so the "cursor
        // advanced" assertion below is meaningful regardless of test order.
        let ledger_sequence = get_cursor(&pool).await.unwrap() + 1_000;
        let mut events: Vec<SorobanEvent> = (0..5)
            .map(|i| make_event(&contract_id, ledger_sequence, i))
            .collect();
        // A permanent failure: no bounded retry count makes an invalid
        // timestamp string parse, so this must go straight to the
        // per-row-fallback path within its chunk.
        events[2].ledger_timestamp = "not-a-timestamp".to_string();

        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();
        sqlx::query("DELETE FROM failed_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();

        let commit = PageCommit {
            events: &events,
            token_events: &[],
            invocation_metrics: &[],
            storage_snapshots: &[],
            network: "testnet",
            cursor: Some(ledger_sequence),
            ledger: Some(LedgerMeta {
                sequence: ledger_sequence,
                hash: "hash_deadletter",
                timestamp: "2024-01-01T00:00:00Z",
                event_count: events.len() as i32,
            }),
            batch_size: 10,
        };

        commit_page(&pool, commit)
            .await
            .expect("commit_page must not fail because of one poison event");

        let good_count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(
            good_count.0, 4,
            "the four well-formed events must still be inserted"
        );

        let failed_count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM failed_events WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(failed_count.0, 1, "the poison event must be dead-lettered");

        let (error_message, network): (String, String) = sqlx::query_as(
            "SELECT error_message, network FROM failed_events WHERE contract_id = $1",
        )
        .bind(&contract_id)
        .fetch_one(&pool)
        .await
        .unwrap();
        assert!(
            error_message.contains("timestamp"),
            "error_message should mention the timestamp parse failure: {error_message:?}"
        );
        assert_eq!(network, "testnet");

        assert_eq!(
            get_cursor(&pool).await.unwrap(),
            ledger_sequence,
            "cursor must advance past the page despite the poison event"
        );

        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();
        sqlx::query("DELETE FROM failed_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();
    }

    /// Upserting token metadata twice for the same (contract_id, network)
    /// must update the row in place, not duplicate it, and a resolved
    /// contract must count as fresh until the refresh interval elapses
    /// (issue #263).
    #[tokio::test]
    async fn token_metadata_upsert_refreshes_in_place() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();

        let contract_id = format!("CTOKENMETA_{}", Uuid::new_v4());
        let network = "testnet";

        sqlx::query("DELETE FROM token_metadata WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();

        // Not yet resolved: absent from the fresh set.
        let fresh = fresh_token_metadata_contract_ids(
            &pool,
            std::slice::from_ref(&contract_id),
            network,
            Utc::now() - chrono::Duration::days(1),
        )
        .await
        .unwrap();
        assert!(!fresh.contains(&contract_id));

        let token = crate::token_metadata::TokenMetadataResolution::Token(
            crate::token_metadata::TokenMetadata {
                name: "Example Token".to_string(),
                symbol: "EXT".to_string(),
                decimals: 7,
            },
        );
        upsert_token_metadata(&pool, &contract_id, network, &token)
            .await
            .expect("upsert failed");

        let row: (String, String, i32, bool) = sqlx::query_as(
            "SELECT name, symbol, decimals, is_token FROM token_metadata
             WHERE contract_id = $1 AND network = $2",
        )
        .bind(&contract_id)
        .bind(network)
        .fetch_one(&pool)
        .await
        .unwrap();
        assert_eq!(
            row,
            ("Example Token".to_string(), "EXT".to_string(), 7, true)
        );

        // Fresh (updated within the last day) after resolution.
        let fresh = fresh_token_metadata_contract_ids(
            &pool,
            std::slice::from_ref(&contract_id),
            network,
            Utc::now() - chrono::Duration::days(1),
        )
        .await
        .unwrap();
        assert!(fresh.contains(&contract_id));

        // Re-resolving as "not a token" updates the same row rather than
        // inserting a second one.
        upsert_token_metadata(
            &pool,
            &contract_id,
            network,
            &crate::token_metadata::TokenMetadataResolution::NotAToken,
        )
        .await
        .expect("re-upsert failed");

        let count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM token_metadata WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(count.0, 1, "re-resolution must update, not duplicate");

        let is_token: (bool,) =
            sqlx::query_as("SELECT is_token FROM token_metadata WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&pool)
                .await
                .unwrap();
        assert!(!is_token.0);

        sqlx::query("DELETE FROM token_metadata WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();
    }

    /// Resolve the shared test database URL, honouring the same
    /// skip-vs-hard-fail contract as the other integration tests here.
    fn test_db_url(test_name: &str) -> Option<String> {
        match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => Some(url),
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: {test_name} requires TEST_DATABASE_URL");
                None
            }
        }
    }

    /// Two indexer replicas committing the SAME ledger range concurrently against
    /// one database must produce exactly one row per event — no duplicates, no
    /// lost events (issue #418).
    ///
    /// This is the deliberate-scale-out / double-deploy scenario: both writers
    /// see the same RPC page and race to persist it. Exactly-once persistence
    /// rests on the natural-key UNIQUE constraint (migration 0025) combined with
    /// the ON CONFLICT DO NOTHING insert path.
    ///
    /// Note the integration job runs with `--test-threads=1`, so the concurrency
    /// here comes from tokio tasks over independent pools inside the test, not
    /// from the test harness running tests in parallel.
    #[tokio::test]
    async fn concurrent_indexers_persist_each_event_exactly_once() {
        let Some(db_url) = test_db_url("concurrent_indexers_persist_each_event_exactly_once")
        else {
            return;
        };

        // Independent pools stand in for independent indexer processes: separate
        // connections, separate transactions, no shared in-process state.
        let pool_a = PgPool::connect(&db_url).await.unwrap();
        let pool_b = PgPool::connect(&db_url).await.unwrap();

        let contract_id = format!("CCONC_{}", Uuid::new_v4());
        const EVENT_COUNT: u32 = 50;
        // Distinct transaction hashes, as real protocol events have: separate
        // events come from separate transactions. `make_event` hardcodes one
        // hash, which is fine for a single-ledger test but would make the
        // natural key repeat across the ledgers this test spans.
        let events: Vec<SorobanEvent> = (0..EVENT_COUNT)
            .map(|i| {
                let mut event = make_event(&contract_id, 1000 + u64::from(i) / 10, i);
                event.transaction_hash = format!("txhash_conc_{i:04}");
                event
            })
            .collect();

        fn page(events: &[SorobanEvent], cursor: u64) -> PageCommit<'_> {
            PageCommit {
                events,
                token_events: &[],
                invocation_metrics: &[],
                storage_snapshots: &[],
                network: "testnet",
                cursor: Some(cursor),
                ledger: None,
                batch_size: 10,
            }
        }

        // Both replicas commit the identical page at the same time.
        let events_a = events.clone();
        let events_b = events.clone();
        let (res_a, res_b) = tokio::join!(
            tokio::spawn(async move { commit_page(&pool_a, page(&events_a, 1004)).await }),
            tokio::spawn(async move { commit_page(&pool_b, page(&events_b, 1004)).await }),
        );

        res_a
            .expect("replica A task panicked")
            .expect("replica A commit failed");
        res_b
            .expect("replica B task panicked")
            .expect("replica B commit failed");

        let verify = PgPool::connect(&db_url).await.unwrap();

        let count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&verify)
                .await
                .unwrap();
        assert_eq!(
            count.0,
            i64::from(EVENT_COUNT),
            "concurrent replicas must not lose or duplicate events"
        );

        // Explicitly assert the natural key is unique — a duplicate would show up
        // here as a group with count > 1 even if the total happened to match.
        let dupes: Vec<(i64, i32, i64)> = sqlx::query_as(
            r#"
            SELECT ledger_sequence, event_index, COUNT(*)
            FROM soroban_events
            WHERE contract_id = $1
            GROUP BY ledger_sequence, event_index
            HAVING COUNT(*) > 1
            "#,
        )
        .bind(&contract_id)
        .fetch_all(&verify)
        .await
        .unwrap();
        assert!(
            dupes.is_empty(),
            "natural key (ledger_sequence, event_index) duplicated under concurrency: {dupes:?}"
        );

        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&verify)
            .await
            .unwrap();
    }

    /// The cursor must never move backwards, even when a slower replica commits
    /// an older page after a faster one has already advanced (issue #418).
    ///
    /// Without the monotonic guard in `commit_page` this is a lost-update race:
    /// the stale writer's `SET value = $1` lands last and rewinds the cursor
    /// behind data already committed.
    #[tokio::test]
    async fn cursor_never_rewinds_under_concurrent_writers() {
        let Some(db_url) = test_db_url("cursor_never_rewinds_under_concurrent_writers") else {
            return;
        };

        let pool = PgPool::connect(&db_url).await.unwrap();

        // Establish a known starting point.
        sqlx::query("UPDATE system_state SET value = '5000' WHERE key = 'latest_ledger_cursor'")
            .execute(&pool)
            .await
            .unwrap();

        let contract_id = format!("CCURS_{}", Uuid::new_v4());
        let ahead = [make_event(&contract_id, 5100, 0)];
        let behind = [make_event(&contract_id, 5050, 1)];

        fn page(events: &[SorobanEvent], cursor: u64) -> PageCommit<'_> {
            PageCommit {
                events,
                token_events: &[],
                invocation_metrics: &[],
                storage_snapshots: &[],
                network: "testnet",
                cursor: Some(cursor),
                ledger: None,
                batch_size: 10,
            }
        }

        // The faster replica advances to 5100 first.
        commit_page(&pool, page(&ahead, 5100))
            .await
            .expect("advance commit failed");
        assert_eq!(get_cursor(&pool).await.unwrap(), 5100);

        // The slower replica now commits an older page. Its events must still
        // land, but the cursor must hold at 5100.
        commit_page(&pool, page(&behind, 5050))
            .await
            .expect("stale commit failed");

        assert_eq!(
            get_cursor(&pool).await.unwrap(),
            5100,
            "a stale replica must not rewind the cursor"
        );

        let count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE contract_id = $1")
                .bind(&contract_id)
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(count.0, 2, "a stale page's events must still be persisted");

        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&pool)
            .await
            .unwrap();
    }

    /// Many writers racing to advance the cursor must converge on the highest
    /// value, not on whichever transaction happened to commit last (issue #418).
    #[tokio::test]
    async fn concurrent_cursor_advances_converge_on_maximum() {
        let Some(db_url) = test_db_url("concurrent_cursor_advances_converge_on_maximum") else {
            return;
        };

        let setup = PgPool::connect(&db_url).await.unwrap();
        sqlx::query("UPDATE system_state SET value = '0' WHERE key = 'latest_ledger_cursor'")
            .execute(&setup)
            .await
            .unwrap();

        let contract_id = format!("CRACE_{}", Uuid::new_v4());

        // Interleave ascending and descending orders so the "last writer" is not
        // the highest cursor: a lost-update bug lands on a low value here.
        let cursors: Vec<u64> = (1..=12).map(|i| i * 100).rev().collect();

        let mut handles = Vec::new();
        for cursor in cursors {
            let url = db_url.clone();
            let contract = contract_id.clone();
            handles.push(tokio::spawn(async move {
                let pool = PgPool::connect(&url).await.unwrap();
                let events = [make_event(&contract, cursor, 0)];
                commit_page(
                    &pool,
                    PageCommit {
                        events: &events,
                        token_events: &[],
                        invocation_metrics: &[],
                        storage_snapshots: &[],
                        network: "testnet",
                        cursor: Some(cursor),
                        ledger: None,
                        batch_size: 10,
                    },
                )
                .await
            }));
        }

        for handle in handles {
            handle
                .await
                .expect("writer task panicked")
                .expect("writer commit failed");
        }

        assert_eq!(
            get_cursor(&setup).await.unwrap(),
            1200,
            "cursor must settle on the highest committed ledger, not the last writer"
        );

        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&contract_id)
            .execute(&setup)
            .await
            .unwrap();
    }
}
