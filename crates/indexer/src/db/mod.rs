use std::collections::HashSet;
use std::time::Duration;

use chrono::{DateTime, Utc};
use sqlx::PgPool;
use trident_common::{EventType, SorobanEvent, TridentError};
use uuid::Uuid;

/// Insert a normalised event. Silently ignores duplicates (same tx_hash + event_index)
/// because the streamer may replay events during cursor recovery.
pub async fn insert_event(pool: &PgPool, event: &SorobanEvent) -> Result<(), TridentError> {
    let id = Uuid::new_v4();
    let event_type = match event.event_type {
        EventType::Contract => "contract",
        EventType::System => "system",
        EventType::Diagnostic => "diagnostic",
    };
    let topics = serde_json::to_value(&event.topics)
        .map_err(|e| TridentError::StorageError(format!("topics serialise: {e}")))?;
    let ledger_ts: DateTime<Utc> = event
        .ledger_timestamp
        .parse()
        .map_err(|e| TridentError::StorageError(format!("ledger timestamp parse: {e}")))?;

    sqlx::query(
        r#"
        INSERT INTO soroban_events
            (id, contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
             event_index, event_type, topics, data)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT (transaction_hash, event_index) DO NOTHING
        "#,
    )
    .bind(id)
    .bind(&event.contract_id)
    .bind(event.ledger_sequence as i64)
    .bind(ledger_ts)
    .bind(&event.transaction_hash)
    .bind(event.event_index as i32)
    .bind(event_type)
    .bind(&topics)
    .bind(&event.data)
    .execute(pool)
    .await
    .map_err(|e| TridentError::StorageError(format!("insert_event: {e}")))?;

    Ok(())
}

/// Read the latest processed ledger cursor from system_state.
pub async fn get_cursor(pool: &PgPool) -> Result<u64, TridentError> {
    let row: (String,) =
        sqlx::query_as("SELECT value FROM system_state WHERE key = 'latest_ledger_cursor'")
            .fetch_one(pool)
            .await
            .map_err(|e| TridentError::StorageError(format!("get_cursor: {e}")))?;

    row.0
        .parse::<u64>()
        .map_err(|e| TridentError::StorageError(format!("cursor parse: {e}")))
}

/// Persist the latest processed ledger sequence so the streamer can resume
/// from the correct position after a restart.
pub async fn set_cursor(pool: &PgPool, ledger: u64) -> Result<(), TridentError> {
    sqlx::query(
        "UPDATE system_state SET value = $1, updated_at = NOW() WHERE key = 'latest_ledger_cursor'",
    )
    .bind(ledger.to_string())
    .execute(pool)
    .await
    .map_err(|e| TridentError::StorageError(format!("set_cursor: {e}")))?;

    Ok(())
}

/// Record a processed ledger in ledger_metadata for gap detection.
pub async fn insert_ledger_metadata(
    pool: &PgPool,
    ledger_sequence: u64,
    ledger_hash: &str,
    ledger_timestamp: &str,
    event_count: i32,
) -> Result<(), TridentError> {
    let ts: DateTime<Utc> = ledger_timestamp
        .parse()
        .map_err(|e| TridentError::StorageError(format!("ledger timestamp parse: {e}")))?;

    sqlx::query(
        r#"
        INSERT INTO ledger_metadata (ledger_sequence, ledger_hash, ledger_timestamp, event_count)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (ledger_sequence) DO NOTHING
        "#,
    )
    .bind(ledger_sequence as i64)
    .bind(ledger_hash)
    .bind(ts)
    .bind(event_count)
    .execute(pool)
    .await
    .map_err(|e| TridentError::StorageError(format!("insert_ledger_metadata: {e}")))?;

    Ok(())
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
    .map_err(|e| TridentError::StorageError(format!("update_health_stats: {e}")))?;

    Ok(())
}

/// Load all contract IDs from `indexed_contracts` for the given network (or
/// network-agnostic rows where `network IS NULL`).
///
/// Returns an empty set if the table has no rows — the caller treats an empty
/// set as "index all contracts" (issue #47).
pub async fn load_indexed_contracts(
    pool: &PgPool,
    network: &str,
) -> Result<HashSet<String>, TridentError> {
    let rows: Vec<(String,)> = sqlx::query_as(
        "SELECT contract_id FROM indexed_contracts WHERE network = $1 OR network IS NULL",
    )
    .bind(network)
    .fetch_all(pool)
    .await
    .map_err(|e| TridentError::StorageError(format!("load_indexed_contracts: {e}")))?;

    Ok(rows.into_iter().map(|(id,)| id).collect())
}
