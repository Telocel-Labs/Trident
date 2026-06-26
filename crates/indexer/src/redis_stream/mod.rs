use redis::{AsyncCommands, streams::StreamMaxlen};
use trident_common::{SorobanEvent, TridentError};

const STREAM_KEY: &str = "trident:events";

/// Publish a normalised event onto the Redis Stream, trimming it to at most
/// `maxlen` entries (approximate trim — `MAXLEN ~`). The Go API layer
/// consumes this stream to fan out to WebSocket subscribers.
pub async fn publish_event(
    conn: &mut redis::aio::MultiplexedConnection,
    event: &SorobanEvent,
    maxlen: u64,
) -> Result<(), TridentError> {
    let topics = serde_json::to_string(&event.topics)
        .map_err(|e| TridentError::StorageError(format!("topics serialise: {e}")))?;
    let data = event.data.to_string();
    let event_type = format!("{:?}", event.event_type).to_lowercase();

    let _: String = conn
        .xadd_maxlen(
            STREAM_KEY,
            StreamMaxlen::Approx(maxlen as usize),
            "*",
            &[
                ("contract_id", event.contract_id.as_str()),
                ("ledger_sequence", &event.ledger_sequence.to_string()),
                ("ledger_timestamp", event.ledger_timestamp.as_str()),
                ("transaction_hash", event.transaction_hash.as_str()),
                ("event_index", &event.event_index.to_string()),
                ("event_type", &event_type),
                ("topics", &topics),
                ("data", &data),
            ],
        )
        .await
        .map_err(|e| TridentError::StorageError(format!("redis xadd: {e}")))?;

    Ok(())
}
