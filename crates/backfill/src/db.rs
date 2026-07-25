use sqlx::PgPool;
use trident_common::{SorobanEvent, TridentError};
use uuid::Uuid;

const EVENT_NS: Uuid = Uuid::NAMESPACE_DNS;

/// Derive a deterministic UUID for an event from its indexer-internal composite key.
///
/// The UUID is derived from `(contract_id, ledger_sequence, event_index)`.
/// See `crates/indexer/src/db/mod.rs` for the full rationale and the relationship
/// to the natural key `(transaction_hash, event_index, network)`.
fn event_uuid(contract_id: &str, ledger_sequence: u64, event_index: u32) -> Uuid {
    let key = format!("{contract_id}:{ledger_sequence}:{event_index}");
    Uuid::new_v5(&EVENT_NS, key.as_bytes())
}

/// Insert a backfilled event.  Duplicate handling is identical to the indexer:
/// - `ON CONFLICT (id) DO NOTHING` deduplicates replays.
/// - The DB-level `UNIQUE (transaction_hash, event_index, network)` constraint
///   (migration 0010) provides an independent protocol-level guard.
pub async fn insert_event(
    pool: &PgPool,
    event: &SorobanEvent,
    network: &str,
) -> Result<(), TridentError> {
    let id = event_uuid(&event.contract_id, event.ledger_sequence, event.event_index);
    let event_type = match event.event_type {
        trident_common::EventType::Contract => "contract",
        trident_common::EventType::System => "system",
        trident_common::EventType::Diagnostic => "diagnostic",
    };
    let topics = serde_json::to_value(&event.topics)
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("topics serialise")))?;
    let ledger_ts: chrono::DateTime<chrono::Utc> = event.ledger_timestamp.parse().map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("ledger timestamp parse"))
    })?;

    sqlx::query(
        r#"
        INSERT INTO soroban_events
            (id, contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
             event_index, event_type, topics, data, network)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
        ON CONFLICT (id) DO NOTHING
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
    .bind(network)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("insert_event")))?;

    Ok(())
}
