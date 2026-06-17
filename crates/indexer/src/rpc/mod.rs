use serde::{Deserialize, Serialize};
use trident_common::TridentError;

/// A single raw event as returned by the Stellar RPC `getEvents` method.
/// Topics and data are base64-encoded XDR strings; the parser decodes them.
#[derive(Debug, Deserialize)]
pub struct RawEvent {
    #[serde(rename = "type")]
    pub event_type: String,
    /// Ledger sequence number as a numeric string.
    pub ledger: String,
    #[serde(rename = "ledgerClosedAt")]
    pub ledger_closed_at: String,
    #[serde(rename = "contractId")]
    pub contract_id: Option<String>,
    pub id: String,
    #[serde(rename = "pagingToken")]
    pub paging_token: String,
    #[serde(rename = "txHash")]
    pub tx_hash: String,
    /// Ordered list of base64 XDR-encoded ScVal topic values.
    pub topic: Vec<String>,
    /// Base64 XDR-encoded ScVal event body.
    pub value: String,
    #[serde(rename = "inSuccessfulContractCall")]
    pub in_successful_contract_call: bool,
}

#[derive(Debug)]
pub struct EventsPage {
    pub events: Vec<RawEvent>,
    pub latest_ledger: u64,
}

// ---------------------------------------------------------------------------
// JSON-RPC wire types
// ---------------------------------------------------------------------------

#[derive(Serialize)]
struct JsonRpcRequest<'a, P: Serialize> {
    jsonrpc: &'static str,
    id: u64,
    method: &'a str,
    params: P,
}

#[derive(Deserialize)]
struct JsonRpcResponse<R> {
    result: Option<R>,
    error: Option<JsonRpcError>,
}

#[derive(Deserialize)]
struct JsonRpcError {
    code: i64,
    message: String,
}

#[derive(Serialize)]
struct GetLedgersParams {
    #[serde(rename = "startLedger")]
    start_ledger: u64,
    pagination: LedgerPagination,
}

#[derive(Serialize)]
struct LedgerPagination {
    limit: u32,
}

#[derive(Deserialize)]
struct GetLedgersResult {
    ledgers: Vec<LedgerSummary>,
}

#[derive(Deserialize)]
struct LedgerSummary {
    hash: String,
}

#[derive(Serialize)]
struct GetEventsParams {
    #[serde(rename = "startLedger", skip_serializing_if = "Option::is_none")]
    start_ledger: Option<u64>,
    filters: Vec<serde_json::Value>,
    pagination: Pagination,
}

#[derive(Serialize)]
struct Pagination {
    limit: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    cursor: Option<String>,
}

#[derive(Deserialize)]
struct GetEventsResult {
    events: Vec<RawEvent>,
    #[serde(rename = "latestLedger")]
    latest_ledger: u64,
}

// ---------------------------------------------------------------------------
// RPC client
// ---------------------------------------------------------------------------

pub struct RpcClient {
    http: reqwest::Client,
    url: String,
}

impl RpcClient {
    pub fn new(url: String) -> Self {
        Self {
            http: reqwest::Client::new(),
            url,
        }
    }

    /// Fetch the ledger hash for a given sequence number via `getLedgers`.
    /// Returns `None` if the RPC does not know about that ledger yet.
    pub async fn get_ledger(&self, sequence: u64) -> Result<Option<String>, TridentError> {
        let params = GetLedgersParams {
            start_ledger: sequence,
            pagination: LedgerPagination { limit: 1 },
        };
        let req = JsonRpcRequest {
            jsonrpc: "2.0",
            id: 2,
            method: "getLedgers",
            params,
        };

        let resp = self
            .http
            .post(&self.url)
            .json(&req)
            .send()
            .await
            .map_err(|e| TridentError::RpcError(format!("getLedgers HTTP failed: {e}")))?;

        let body: JsonRpcResponse<GetLedgersResult> = resp
            .json()
            .await
            .map_err(|e| TridentError::RpcError(format!("getLedgers decode failed: {e}")))?;

        if let Some(err) = body.error {
            return Err(TridentError::RpcError(format!(
                "getLedgers RPC error {}: {}",
                err.code, err.message
            )));
        }

        let hash = body
            .result
            .and_then(|r| r.ledgers.into_iter().next())
            .map(|l| l.hash);

        Ok(hash)
    }

    /// Fetch a page of events from the Stellar RPC node.
    ///
    /// Pass `start_ledger` on the first call to anchor the scan position.
    /// On subsequent calls pass `cursor` (the `paging_token` from the last
    /// event received) to continue pagination. Only one of the two should be
    /// set at a time — the RPC rejects requests that supply both.
    pub async fn get_events(
        &self,
        start_ledger: Option<u64>,
        cursor: Option<String>,
    ) -> Result<EventsPage, TridentError> {
        let params = GetEventsParams {
            start_ledger,
            filters: vec![],
            pagination: Pagination { limit: 200, cursor },
        };

        let req = JsonRpcRequest {
            jsonrpc: "2.0",
            id: 1,
            method: "getEvents",
            params,
        };

        let resp = self
            .http
            .post(&self.url)
            .json(&req)
            .send()
            .await
            .map_err(|e| TridentError::RpcError(format!("HTTP request failed: {e}")))?;

        let body: JsonRpcResponse<GetEventsResult> = resp
            .json()
            .await
            .map_err(|e| TridentError::RpcError(format!("Failed to decode RPC response: {e}")))?;

        if let Some(err) = body.error {
            return Err(TridentError::RpcError(format!(
                "RPC error {}: {}",
                err.code, err.message
            )));
        }

        let result = body
            .result
            .ok_or_else(|| TridentError::RpcError("Empty result in RPC response".into()))?;

        Ok(EventsPage {
            events: result.events,
            latest_ledger: result.latest_ledger,
        })
    }
}
