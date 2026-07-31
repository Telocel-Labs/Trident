// Example code that deserializes and serializes the model.
// extern crate serde;
// #[macro_use]
// extern crate serde_derive;
// extern crate serde_json;
//
// use generated_module::OpenAPIModels;
//
// fn main() {
//     let json = r#"{"answer": 42}"#;
//     let model: OpenAPIModels = serde_json::from_str(&json).unwrap();
// }

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct OpenApiModels {
    pub contract_stats: Option<ContractStats>,

    pub contract_stats_response: Option<ContractStatsResponse>,

    pub error_response: Option<ErrorResponse>,

    pub event_list_response: Option<EventListResponse>,

    pub health_response: Option<HealthResponse>,

    pub indexer_stats_response: Option<IndexerStatsResponse>,

    pub soroban_event: Option<SorobanEvent>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ContractStats {
    /// Soroban contract address
    pub contract_id: String,

    /// Total events for this contract in range
    pub event_count: i64,

    /// Timestamp of last event for this contract
    pub last_seen_at: String,

    /// Latest ledger sequence for this contract
    pub last_seen_ledger: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ContractStatsResponse {
    /// Contracts sorted by event count (descending)
    pub contracts: Vec<ContractStats>,

    /// Lower bound of queried ledger range
    pub from_ledger: i64,

    /// Timestamp when response was generated
    pub generated_at: String,

    /// Network queried
    pub network: Network,

    /// Upper bound of queried ledger range
    pub to_ledger: i64,
}

/// Network queried
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Network {
    Mainnet,

    Testnet,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ErrorResponse {
    pub error: Error,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Error {
    /// Error code (e.g., INVALID_ARGUMENT, INTERNAL, UNAVAILABLE)
    pub code: String,

    /// Human-readable error message
    pub message: String,

    /// Request ID for debugging
    pub request_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EventListResponse {
    /// List of events
    pub events: Vec<SorobanEvent>,

    /// Whether more results are available
    pub has_more: bool,

    /// Opaque cursor for next page (null if has_more is false)
    pub next_cursor: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SorobanEvent {
    /// Soroban contract address
    pub contract_id: String,

    /// Timestamp when event was indexed
    pub created_at: String,

    /// Event data (XDR-encoded)
    pub data: String,

    /// Event index within transaction
    pub event_index: i64,

    /// Type of event
    pub event_type: EventType,

    /// Unique event identifier
    pub id: String,

    /// Ledger sequence number
    pub ledger_sequence: i64,

    /// Ledger timestamp in ISO 8601 format
    pub ledger_timestamp: String,

    /// Event topics (XDR-encoded)
    pub topics: Vec<String>,

    /// Transaction hash (XDR-encoded)
    pub transaction_hash: String,
}

/// Type of event
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum EventType {
    Contract,

    Diagnostic,

    System,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthResponse {
    pub indexer: Indexer,

    /// Overall system status
    pub status: HealthResponseStatus,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Indexer {
    /// Latest indexed ledger sequence
    pub last_ledger_indexed: i64,

    /// Timestamp of last successful indexer poll
    pub last_poll_at: Option<String>,
}

/// Overall system status
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum HealthResponseStatus {
    Degraded,

    #[serde(rename = "ok")]
    StatusOk,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IndexerStatsResponse {
    /// Average poll duration in milliseconds
    pub avg_poll_duration_ms: Option<i64>,

    /// Current chain tip ledger (from RPC)
    pub chain_tip_ledger: Option<i64>,

    /// Cumulative events indexed
    pub events_indexed_total: Option<i64>,

    /// Events processed in last poll
    pub events_last_poll: Option<i64>,

    /// Number of ledgers behind chain tip
    pub lag_ledgers: Option<i64>,

    /// Latest indexed ledger sequence
    pub last_ledger_indexed: Option<i64>,

    /// Timestamp of last successful poll
    pub last_poll_at: Option<String>,

    /// Network name from NETWORK environment variable
    pub network: String,

    /// Indexer health status
    pub status: IndexerStatsResponseStatus,
}

/// Indexer health status
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum IndexerStatsResponseStatus {
    Healthy,

    Lagging,

    Stalled,
}
