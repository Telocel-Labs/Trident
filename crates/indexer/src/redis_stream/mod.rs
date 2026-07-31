pub mod relay;

use redis::{streams::StreamMaxlen, AsyncCommands};
use trident_common::{SorobanEvent, TridentError};

const STREAM_KEY: &str = "trident:events";

/// Publish a normalised event onto the Redis Stream, trimming it to at most
/// `maxlen` entries (approximate trim — `MAXLEN ~`). The Go API layer
/// consumes this stream to fan out to WebSocket subscribers.
///
/// `event_id` carries the deterministic event id when the caller knows it. The
/// outbox relay delivers at least once, so the same event can appear twice on
/// the stream after a crash between `XADD` and the outbox update; that field is
/// what lets a consumer discard the repeat (issue #200).
pub async fn publish_event(
    conn: &mut redis::aio::MultiplexedConnection,
    event: &SorobanEvent,
    maxlen: u64,
    event_id: Option<&str>,
) -> Result<(), TridentError> {
    let topics = serde_json::to_string(&event.topics)
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("topics serialise")))?;
    let data = event.data.to_string();
    let event_type = format!("{:?}", event.event_type).to_lowercase();

    let ledger_sequence = event.ledger_sequence.to_string();
    let event_index = event.event_index.to_string();
    let mut fields: Vec<(&str, &str)> = vec![
        ("contract_id", event.contract_id.as_str()),
        ("ledger_sequence", &ledger_sequence),
        ("ledger_timestamp", event.ledger_timestamp.as_str()),
        ("transaction_hash", event.transaction_hash.as_str()),
        ("event_index", &event_index),
        ("event_type", &event_type),
        ("topics", &topics),
        ("data", &data),
    ];
    if let Some(id) = event_id {
        fields.push(("event_id", id));
    }

    let _: String = conn
        .xadd_maxlen(
            STREAM_KEY,
            StreamMaxlen::Approx(maxlen as usize),
            "*",
            &fields,
        )
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("redis xadd")))?;

    Ok(())
}
