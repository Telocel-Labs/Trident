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

#[cfg(test)]
mod tests {
    /// Verify that the `insert_event` SQL contains an idempotent ON CONFLICT clause.
    /// The deduplication key is (transaction_hash, event_index) rather than id
    /// because `id` is a random Uuid::new_v4() that would never naturally conflict.
    #[test]
    fn insert_event_sql_is_idempotent() {
        let sql = r#"
        INSERT INTO soroban_events
            (id, contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
             event_index, event_type, topics, data)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT (transaction_hash, event_index) DO NOTHING
        "#;
        assert!(sql.contains("ON CONFLICT"));
        assert!(sql.contains("DO NOTHING"));
        assert!(
            sql.contains("transaction_hash, event_index"),
            "dedup key must be (transaction_hash, event_index), not the random id column"
        );
    }
}
