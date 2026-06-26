use crate::parser::RawEvent;
use serde::{Deserialize, Serialize};
use trident_common::TridentError;

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

    pub async fn get_events(
        &self,
        start_ledger: Option<u64>,
        cursor: Option<String>,
    ) -> Result<crate::parser::EventsPage, TridentError> {
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

        Ok(crate::parser::EventsPage {
            events: result.events,
            latest_ledger: result.latest_ledger,
        })
    }
}
