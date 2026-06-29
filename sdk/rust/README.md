# trident-sdk

Rust client for the [Trident](https://github.com/Telocel-Labs/Trident) Soroban event indexer.

## Installation

Add to `Cargo.toml`:

```toml
[dependencies]
trident-sdk = "0.1"
tokio = { version = "1", features = ["full"] }
futures = "0.3"
```

## Quick start

```rust
use trident_sdk::{TridentClient, TridentConfig, QueryParams, Network};
use futures::StreamExt;

#[tokio::main]
async fn main() -> Result<(), trident_sdk::TridentError> {
    let client = TridentClient::new(TridentConfig {
        api_url: "https://trident-api.fly.dev".into(),
        api_key: "tk_your_key".into(),
        network: Network::Testnet,
        ..Default::default()
    })?;

    // Query historical events
    let page = client.query_events(QueryParams {
        contract_id: Some("CAAAA...".into()),
        topic_0: Some("transfer".into()),
        first: Some(50),
        ..Default::default()
    }).await?;

    println!("Found {} events", page.events.len());

    // Paginate
    if let Some(cursor) = page.next_cursor {
        let next_page = client.query_events(QueryParams {
            after: Some(cursor),
            ..Default::default()
        }).await?;
        println!("{} more events", next_page.events.len());
    }

    // Fetch single event
    let event = client.get_event_by_id("550e8400-e29b-41d4-a716-446655440000").await?;
    println!("Event: {:?}", event);

    // Real-time subscription
    let mut sub = client.subscribe_to_contract("CAAAA...", Some("transfer")).await?;
    while let Some(result) = sub.next().await {
        println!("{:?}", result?);
    }

    Ok(())
}
```

## Publishing

```bash
cargo publish --package trident-sdk
```

Dry-run check (runs in CI):

```bash
cargo publish --dry-run --package trident-sdk
```
