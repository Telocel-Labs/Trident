use futures::StreamExt;
use trident_sdk::{ContractStatsQuery, QueryParams, TridentClient, TridentConfig, TridentError};

#[tokio::main]
async fn main() -> Result<(), TridentError> {
    let api_url =
        std::env::var("TRIDENT_API_URL").unwrap_or_else(|_| "https://trident-api.fly.dev".into());
    let api_key = std::env::var("TRIDENT_API_KEY").unwrap_or_default();
    let contract_id = std::env::var("TRIDENT_CONTRACT_ID")
        .unwrap_or_else(|_| "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4".into());

    let client = TridentClient::new(TridentConfig {
        api_url,
        api_key,
        ..Default::default()
    })?;

    let health = client.get_health().await?;
    println!("health: {}", health.status);

    let indexer = client.get_indexer_stats().await?;
    println!("indexer: {}", indexer.status);

    let contract_stats = client
        .get_contract_stats(ContractStatsQuery {
            limit: Some(5),
            ..Default::default()
        })
        .await?;
    println!("contracts returned: {}", contract_stats.contracts.len());

    let mut history = client.iter_events(QueryParams {
        contract_id: Some(contract_id.clone()),
        first: Some(10),
        ..Default::default()
    });
    if let Some(event) = history.next().await {
        println!("historical event: {}", event?.id);
    }

    let mut live = client.subscribe_to_contract(&contract_id, None).await?;
    if let Some(event) = live.next().await {
        println!("live event: {}", event?.id);
    }

    Ok(())
}
