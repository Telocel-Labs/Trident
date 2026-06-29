use serde::{Deserialize, Serialize};
use std::time::Duration;

/// Stellar network selection.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub enum Network {
    Mainnet,
    #[default]
    Testnet,
    Futurenet,
}

/// Configuration for [`TridentClient`](crate::TridentClient).
#[derive(Debug, Clone)]
pub struct TridentConfig {
    /// Base URL of the Trident REST API.
    pub api_url: String,
    /// API key sent as `X-API-Key` on every request.
    pub api_key: String,
    /// Target Stellar network.
    pub network: Network,
    /// Per-request timeout. Defaults to 30 seconds.
    pub timeout: Duration,
}

impl Default for TridentConfig {
    fn default() -> Self {
        TridentConfig {
            api_url: "https://trident-api.fly.dev".to_string(),
            api_key: String::new(),
            network: Network::Testnet,
            timeout: Duration::from_secs(30),
        }
    }
}

/// Parameters for [`query_events`](crate::TridentClient::query_events).
#[derive(Debug, Default, Clone)]
pub struct QueryParams {
    pub contract_id: Option<String>,
    pub topic_0: Option<String>,
    pub topic_1: Option<String>,
    pub from_ledger: Option<u64>,
    pub to_ledger: Option<u64>,
    /// Pagination cursor returned by a previous call.
    pub after: Option<String>,
    /// Maximum number of events to return (default: 50).
    pub first: Option<u32>,
    pub event_type: Option<String>,
}

/// Category of a Soroban event.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum EventType {
    Contract,
    System,
    Diagnostic,
}

/// A single Soroban event returned by the Trident API.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SorobanEvent {
    pub id: String,
    pub contract_id: String,
    pub ledger_sequence: u64,
    pub ledger_timestamp: String,
    pub transaction_hash: String,
    pub event_index: u32,
    pub event_type: EventType,
    pub topics: Vec<String>,
    /// Decoded event body. Scalar XDR types are JSON primitives; maps/vecs are
    /// JSON objects/arrays.
    pub data: serde_json::Value,
    pub created_at: String,
}

/// A page of events returned by [`query_events`](crate::TridentClient::query_events).
#[derive(Debug)]
pub struct PaginatedEvents {
    pub events: Vec<SorobanEvent>,
    /// Pass as `after` in the next call to get the next page. `None` when no
    /// more pages exist.
    pub next_cursor: Option<String>,
    pub has_more: bool,
}
