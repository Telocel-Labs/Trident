use std::collections::VecDeque;
use std::pin::Pin;
use std::time::Duration;

use futures::{stream, Stream, StreamExt};
use serde::Deserialize;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::http::HeaderValue;
use tokio_tungstenite::tungstenite::Message;

use crate::retry::{compute_backoff, is_retryable_status, parse_retry_after_seconds};
use crate::{
    ContractStatsQuery, ContractStatsResponse, EventType, HealthResponse, IndexerStatsResponse,
    Network, PaginatedEvents, QueryParams, RetryConfig, SorobanEvent, Subscription, TridentConfig,
    TridentError,
};

// ---------------------------------------------------------------------------
// Internal API response types (snake_case, as returned by the Go API)
// ---------------------------------------------------------------------------

#[derive(Deserialize)]
struct ApiEvent {
    id: String,
    contract_id: String,
    ledger_sequence: u64,
    ledger_timestamp: String,
    transaction_hash: String,
    event_index: u32,
    event_type: String,
    topics: Vec<String>,
    data: String,
    created_at: String,
}

#[derive(Deserialize)]
struct ApiListResponse {
    events: Vec<ApiEvent>,
    next_cursor: Option<String>,
    has_more: bool,
}

#[derive(Deserialize)]
struct ApiGetResponse {
    event: ApiEvent,
}

// Hub sends all numeric fields as strings and topics as a JSON-encoded string.
#[derive(Deserialize)]
struct WsEvent {
    contract_id: String,
    ledger_sequence: String,
    ledger_timestamp: String,
    transaction_hash: String,
    event_index: String,
    event_type: String,
    topics: String,
    data: String,
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

fn api_event_to_soroban(e: ApiEvent) -> SorobanEvent {
    let data_str = e.data;
    let data = serde_json::from_str::<serde_json::Value>(&data_str)
        .unwrap_or(serde_json::Value::String(data_str));

    let event_type = match e.event_type.as_str() {
        "system" => EventType::System,
        "diagnostic" => EventType::Diagnostic,
        _ => EventType::Contract,
    };

    SorobanEvent {
        id: e.id,
        contract_id: e.contract_id,
        ledger_sequence: e.ledger_sequence,
        ledger_timestamp: e.ledger_timestamp,
        transaction_hash: e.transaction_hash,
        event_index: e.event_index,
        event_type,
        topics: e.topics,
        data,
        created_at: e.created_at,
    }
}

fn ws_event_to_soroban(e: WsEvent) -> SorobanEvent {
    let ledger_sequence = e.ledger_sequence.parse::<u64>().unwrap_or(0);
    let event_index = e.event_index.parse::<u32>().unwrap_or(0);
    let topics = serde_json::from_str::<Vec<String>>(&e.topics).unwrap_or_default();
    let data_str = e.data;
    let data = serde_json::from_str::<serde_json::Value>(&data_str)
        .unwrap_or(serde_json::Value::String(data_str));

    let event_type = match e.event_type.as_str() {
        "system" => EventType::System,
        "diagnostic" => EventType::Diagnostic,
        _ => EventType::Contract,
    };

    SorobanEvent {
        id: String::new(),
        contract_id: e.contract_id,
        ledger_sequence,
        ledger_timestamp: e.ledger_timestamp.clone(),
        transaction_hash: e.transaction_hash,
        event_index,
        event_type,
        topics,
        data,
        created_at: e.ledger_timestamp,
    }
}

fn ws_url_from_api_url(api_url: &str) -> String {
    api_url
        .replacen("https://", "wss://", 1)
        .replacen("http://", "ws://", 1)
}

// ---------------------------------------------------------------------------
// HTTP response error mapping
// ---------------------------------------------------------------------------

async fn check_response(response: reqwest::Response) -> Result<reqwest::Response, TridentError> {
    let status = response.status();
    if status.is_success() {
        return Ok(response);
    }
    match status.as_u16() {
        401 => Err(TridentError::Unauthorized),
        404 => Err(TridentError::NotFound),
        429 => {
            let retry_after_seconds = response
                .headers()
                .get("retry-after")
                .and_then(|v| v.to_str().ok())
                .and_then(|s| s.parse::<u64>().ok())
                .unwrap_or(60);
            Err(TridentError::RateLimited {
                retry_after_seconds,
            })
        }
        code => {
            let message = response.text().await.unwrap_or_default();
            Err(TridentError::Http {
                status: code,
                message,
            })
        }
    }
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

/// Async HTTP + WebSocket client for the Trident Soroban event indexer.
#[derive(Clone)]
pub struct TridentClient {
    config: TridentConfig,
    http: reqwest::Client,
}

impl TridentClient {
    /// Create a new client from the given configuration.
    ///
    /// Returns an error if the underlying HTTP client cannot be built (e.g.
    /// TLS initialisation failure).
    ///
    /// # Example
    ///
    /// ```no_run
    /// # tokio_test::block_on(async {
    /// let client = trident_sdk::TridentClient::new(trident_sdk::TridentConfig {
    ///     api_url: "https://trident-api.fly.dev".into(),
    ///     api_key: "tk_your_key".into(),
    ///     ..Default::default()
    /// })?;
    /// # Ok::<(), trident_sdk::TridentError>(())
    /// # });
    /// ```
    pub fn new(config: TridentConfig) -> Result<Self, TridentError> {
        let config = config.resolved();
        if config.api_key.is_empty() {
            return Err(TridentError::MissingApiKey);
        }

        let http = reqwest::Client::builder()
            .timeout(config.timeout)
            .build()
            .map_err(TridentError::Network)?;
        Ok(TridentClient { config, http })
    }

    fn headers(&self) -> reqwest::header::HeaderMap {
        let mut map = reqwest::header::HeaderMap::new();
        if let Ok(v) = reqwest::header::HeaderValue::from_str(&self.config.api_key) {
            map.insert("X-API-Key", v);
        }
        map
    }

    /// Issue a GET request, retrying according to `retry` (`None` disables
    /// retries — a single attempt). Honours `Retry-After` on 429/503,
    /// falling back to exponential backoff with jitter otherwise. Once
    /// retries are exhausted, wraps the last error in
    /// [`TridentError::RetryExhausted`].
    async fn send_get(
        &self,
        url: url::Url,
        retry: Option<RetryConfig>,
    ) -> Result<reqwest::Response, TridentError> {
        let mut attempt: u32 = 1;
        let mut total_waited = Duration::from_millis(0);

        loop {
            let send_result = self
                .http
                .get(url.clone())
                .headers(self.headers())
                .send()
                .await;

            let response = match send_result {
                Ok(r) => r,
                Err(e) => {
                    let network_err = TridentError::Network(e);
                    if let Some(cfg) = &retry {
                        if attempt < cfg.max_attempts {
                            let wait = compute_backoff(attempt, cfg);
                            if total_waited + wait <= cfg.max_total_wait {
                                total_waited += wait;
                                tokio::time::sleep(wait).await;
                                attempt += 1;
                                continue;
                            }
                        }
                    }
                    return Err(if attempt > 1 {
                        TridentError::RetryExhausted {
                            attempts: attempt,
                            last_error: Box::new(network_err),
                        }
                    } else {
                        network_err
                    });
                }
            };

            let status = response.status();
            if status.is_success() {
                return Ok(response);
            }

            if let Some(cfg) = &retry {
                if is_retryable_status(status.as_u16()) && attempt < cfg.max_attempts {
                    let wait = parse_retry_after_seconds(response.headers().get("retry-after"))
                        .unwrap_or_else(|| compute_backoff(attempt, cfg));
                    if total_waited + wait <= cfg.max_total_wait {
                        total_waited += wait;
                        tokio::time::sleep(wait).await;
                        attempt += 1;
                        continue;
                    }
                }
            }

            // Status already confirmed non-success above, so check_response
            // always returns Err here.
            let err = check_response(response).await.unwrap_err();
            return Err(if attempt > 1 {
                TridentError::RetryExhausted {
                    attempts: attempt,
                    last_error: Box::new(err),
                }
            } else {
                err
            });
        }
    }

    /// Query historical Soroban events with optional filtering.
    ///
    /// Results are cursor-paginated. Pass `result.next_cursor` as
    /// `params.after` on the next call to fetch the next page.
    ///
    /// # Example
    ///
    /// ```no_run
    /// # tokio_test::block_on(async {
    /// # let client = trident_sdk::TridentClient::new(trident_sdk::TridentConfig {
    /// #     api_url: "https://trident-api.fly.dev".into(),
    /// #     api_key: "tk_your_key".into(),
    /// #     ..Default::default()
    /// # })?;
    /// let page = client.query_events(trident_sdk::QueryParams {
    ///     contract_id: Some("CAAAA...".into()),
    ///     first: Some(50),
    ///     ..Default::default()
    /// }).await?;
    /// println!("Found {} events", page.events.len());
    /// # Ok::<(), trident_sdk::TridentError>(())
    /// # });
    /// ```
    pub async fn query_events(&self, params: QueryParams) -> Result<PaginatedEvents, TridentError> {
        self.query_events_with_retry(params, self.config.retry.clone())
            .await
    }

    /// Same as [`query_events`](Self::query_events), overriding the
    /// client-level retry policy for this call only. Pass `None` to disable
    /// retries regardless of [`TridentConfig::retry`].
    pub async fn query_events_with_retry(
        &self,
        params: QueryParams,
        retry: Option<RetryConfig>,
    ) -> Result<PaginatedEvents, TridentError> {
        let mut url = url::Url::parse(&format!("{}/v1/events", self.config.api_url))
            .map_err(|e| TridentError::WebSocket(e.to_string()))?;

        {
            let mut qs = url.query_pairs_mut();
            if let Some(c) = &params.contract_id {
                qs.append_pair("contractId", c);
            }
            if let Some(t) = &params.topic_0 {
                qs.append_pair("topic0", t);
            }
            if let Some(t) = &params.topic_1 {
                qs.append_pair("topic1", t);
            }
            if let Some(l) = params.from_ledger {
                qs.append_pair("ledgerFrom", &l.to_string());
            }
            if let Some(l) = params.to_ledger {
                qs.append_pair("ledgerTo", &l.to_string());
            }
            if let Some(a) = &params.after {
                qs.append_pair("cursor", a);
            }
            qs.append_pair("limit", &params.first.unwrap_or(50).to_string());
            if let Some(et) = &params.event_type {
                qs.append_pair("event_type", et);
            }
        }

        let response = self.send_get(url, retry).await?;
        let body: ApiListResponse = response.json().await?;

        Ok(PaginatedEvents {
            events: body.events.into_iter().map(api_event_to_soroban).collect(),
            next_cursor: body.next_cursor,
            has_more: body.has_more,
        })
    }

    /// Query a page of events and return `(events, next_cursor)`.
    ///
    /// Convenience wrapper around [`query_events`](Self::query_events) for
    /// callers that want to destructure the result directly.
    ///
    /// # Example
    ///
    /// ```no_run
    /// # tokio_test::block_on(async {
    /// # let client = trident_sdk::TridentClient::new(trident_sdk::TridentConfig {
    /// #     api_url: "https://trident-api.fly.dev".into(),
    /// #     api_key: "tk_your_key".into(),
    /// #     ..Default::default()
    /// # })?;
    /// let (events, cursor) = client
    ///     .query_events_page(Default::default())
    ///     .await?;
    /// println!("Got {} events, cursor: {:?}", events.len(), cursor);
    /// # Ok::<(), trident_sdk::TridentError>(())
    /// # });
    /// ```
    pub async fn query_events_page(
        &self,
        params: QueryParams,
    ) -> Result<(Vec<SorobanEvent>, Option<String>), TridentError> {
        let page = self.query_events(params).await?;
        Ok((page.events, page.next_cursor))
    }

    /// Auto-paginating event stream backed by cursor-based HTTP pagination.
    ///
    /// Each poll fetches the next page only when the current page has been
    /// drained, so cancellation is immediate when the stream is dropped.
    pub fn iter_events(
        &self,
        params: QueryParams,
    ) -> Pin<Box<dyn Stream<Item = Result<SorobanEvent, TridentError>> + Send>> {
        #[derive(Clone)]
        struct IterState {
            client: TridentClient,
            params: QueryParams,
            buffer: VecDeque<SorobanEvent>,
            exhausted: bool,
        }

        let state = IterState {
            client: self.clone(),
            params,
            buffer: VecDeque::new(),
            exhausted: false,
        };

        // stream::unfold's returned Stream isn't Unpin (its inner future
        // holds a self-reference across .await points), which makes
        // StreamExt::next() unusable without the caller manually pinning it
        // first. Boxing and pinning here makes the stream Unpin so it can be
        // used the same way Subscription is (see subscription.rs).
        Box::pin(stream::unfold(state, |mut state| async move {
            loop {
                if let Some(event) = state.buffer.pop_front() {
                    return Some((Ok(event), state));
                }
                if state.exhausted {
                    return None;
                }

                match state.client.query_events(state.params.clone()).await {
                    Ok(page) => {
                        state.params.after = page.next_cursor.clone();
                        state.exhausted = !page.has_more || page.next_cursor.is_none();
                        state.buffer = VecDeque::from(page.events);
                    }
                    Err(err) => {
                        state.exhausted = true;
                        return Some((Err(err), state));
                    }
                }
            }
        }))
    }

    /// Fetch a single event by its UUID.
    ///
    /// Returns `Err(TridentError::NotFound)` if no event with that ID exists.
    ///
    /// # Example
    ///
    /// ```no_run
    /// # tokio_test::block_on(async {
    /// # let client = trident_sdk::TridentClient::new(trident_sdk::TridentConfig {
    /// #     api_url: "https://trident-api.fly.dev".into(),
    /// #     api_key: "tk_your_key".into(),
    /// #     ..Default::default()
    /// # })?;
    /// let event = client.get_event_by_id("550e8400-e29b-41d4-a716-446655440000").await?;
    /// println!("Event: {:?}", event);
    /// # Ok::<(), trident_sdk::TridentError>(())
    /// # });
    /// ```
    pub async fn get_event_by_id(&self, id: &str) -> Result<SorobanEvent, TridentError> {
        self.get_event_by_id_with_retry(id, self.config.retry.clone())
            .await
    }

    /// Same as [`get_event_by_id`](Self::get_event_by_id), overriding the
    /// client-level retry policy for this call only. Pass `None` to disable
    /// retries regardless of [`TridentConfig::retry`].
    pub async fn get_event_by_id_with_retry(
        &self,
        id: &str,
        retry: Option<RetryConfig>,
    ) -> Result<SorobanEvent, TridentError> {
        let url = format!(
            "{}/v1/events/{}",
            self.config.api_url,
            url::form_urlencoded::byte_serialize(id.as_bytes()).collect::<String>()
        );
        let url = url::Url::parse(&url).map_err(|e| TridentError::WebSocket(e.to_string()))?;

        let response = self.send_get(url, retry).await?;
        let body: ApiGetResponse = response.json().await?;
        Ok(api_event_to_soroban(body.event))
    }

    /// Fetch the service-wide health status.
    pub async fn get_health(&self) -> Result<HealthResponse, TridentError> {
        let url = format!("{}/v1/health", self.config.api_url);
        let response = self.http.get(&url).send().await?;
        let response = check_response(response).await?;
        Ok(response.json().await?)
    }

    /// Fetch indexer health and throughput statistics.
    pub async fn get_indexer_stats(&self) -> Result<IndexerStatsResponse, TridentError> {
        let url = format!("{}/v1/stats/indexer", self.config.api_url);
        let response = self.http.get(&url).headers(self.headers()).send().await?;
        let response = check_response(response).await?;
        Ok(response.json().await?)
    }

    /// Fetch aggregated per-contract statistics for the selected ledger range.
    pub async fn get_contract_stats(
        &self,
        params: ContractStatsQuery,
    ) -> Result<ContractStatsResponse, TridentError> {
        let mut url = url::Url::parse(&format!("{}/v1/stats/contracts", self.config.api_url))
            .map_err(|e| TridentError::WebSocket(e.to_string()))?;

        {
            let mut qs = url.query_pairs_mut();
            if let Some(from_ledger) = params.from_ledger {
                qs.append_pair("from_ledger", &from_ledger.to_string());
            }
            if let Some(to_ledger) = params.to_ledger {
                qs.append_pair("to_ledger", &to_ledger.to_string());
            }
            if let Some(limit) = params.limit {
                qs.append_pair("limit", &limit.to_string());
            }
            let network = params
                .network
                .unwrap_or_else(|| self.config.network.clone());
            if !matches!(network, Network::Futurenet) {
                qs.append_pair("network", network.as_str());
            }
        }

        let response = self.http.get(url).headers(self.headers()).send().await?;
        let response = check_response(response).await?;
        Ok(response.json().await?)
    }

    /// Open a real-time WebSocket subscription to events emitted by a contract.
    ///
    /// Returns a [`Subscription`] that implements
    /// [`futures::Stream`](futures::stream::Stream). Call
    /// [`StreamExt::next`](futures::StreamExt::next) to iterate over incoming
    /// events. Drop the `Subscription` to close the connection.
    ///
    /// # Example
    ///
    /// ```no_run
    /// # tokio_test::block_on(async {
    /// use futures::StreamExt;
    /// # let client = trident_sdk::TridentClient::new(trident_sdk::TridentConfig {
    /// #     api_url: "https://trident-api.fly.dev".into(),
    /// #     api_key: "tk_your_key".into(),
    /// #     ..Default::default()
    /// # })?;
    /// let mut sub = client
    ///     .subscribe_to_contract("CAAAA...", Some("transfer"))
    ///     .await?;
    /// while let Some(event) = sub.next().await {
    ///     println!("{:?}", event?);
    /// }
    /// # Ok::<(), trident_sdk::TridentError>(())
    /// # });
    /// ```
    pub async fn subscribe_to_contract(
        &self,
        contract_id: &str,
        topic_0: Option<&str>,
    ) -> Result<Subscription, TridentError> {
        let ws_base = ws_url_from_api_url(&self.config.api_url);

        let mut ws_url = url::Url::parse(&format!("{}/ws", ws_base))
            .map_err(|e| TridentError::WebSocket(e.to_string()))?;
        {
            let mut qs = ws_url.query_pairs_mut();
            qs.append_pair("contractId", contract_id);
            if let Some(t) = topic_0 {
                qs.append_pair("topic0", t);
            }
        }

        let mut request = ws_url
            .as_str()
            .into_client_request()
            .map_err(|e| TridentError::WebSocket(e.to_string()))?;

        if let Ok(v) = HeaderValue::from_str(&self.config.api_key) {
            request.headers_mut().insert("X-API-Key", v);
        }

        let (ws_stream, _) = tokio_tungstenite::connect_async(request)
            .await
            .map_err(|e| TridentError::WebSocket(e.to_string()))?;

        let event_stream = ws_stream.filter_map(|msg| async move {
            match msg {
                Ok(Message::Text(text)) => {
                    let result = serde_json::from_str::<WsEvent>(&text)
                        .map(ws_event_to_soroban)
                        .map_err(TridentError::Deserialize);
                    Some(result)
                }
                Ok(Message::Close(_)) | Ok(_) => None,
                Err(e) => Some(Err(TridentError::WebSocket(e.to_string()))),
            }
        });

        Ok(Subscription::new(event_stream))
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use futures::StreamExt;
    use mockito::Server;

    fn make_client(base_url: &str) -> TridentClient {
        TridentClient::new(TridentConfig {
            api_url: base_url.to_string(),
            api_key: "test-key".to_string(),
            ..Default::default()
        })
        .unwrap()
    }

    fn event_body() -> serde_json::Value {
        serde_json::json!({
            "id": "550e8400-e29b-41d4-a716-446655440000",
            "contract_id": "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
            "ledger_sequence": 50000,
            "ledger_timestamp": "2024-01-01T00:00:00Z",
            "transaction_hash": "abc123",
            "event_index": 0,
            "event_type": "contract",
            "topics": ["transfer"],
            "data": "\"hello\"",
            "created_at": "2024-01-01T00:00:00Z"
        })
    }

    #[tokio::test]
    async fn query_events_parses_response() {
        let mut server = Server::new_async().await;

        let body = serde_json::json!({
            "events": [event_body()],
            "next_cursor": null,
            "has_more": false
        });

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client.query_events(QueryParams::default()).await.unwrap();

        assert_eq!(result.events.len(), 1);
        assert_eq!(result.events[0].id, "550e8400-e29b-41d4-a716-446655440000");
        assert_eq!(result.events[0].event_type, EventType::Contract);
        assert_eq!(
            result.events[0].data,
            serde_json::Value::String("hello".to_string())
        );
        assert!(!result.has_more);
        assert!(result.next_cursor.is_none());

        mock.assert_async().await;
    }

    #[tokio::test]
    async fn query_events_with_filter_sends_params() {
        let mut server = Server::new_async().await;

        let body = serde_json::json!({
            "events": [],
            "next_cursor": null,
            "has_more": false
        });

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .match_query(mockito::Matcher::AllOf(vec![
                mockito::Matcher::UrlEncoded("contractId".into(), "CAAAA".into()),
                mockito::Matcher::UrlEncoded("topic0".into(), "transfer".into()),
            ]))
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .create_async()
            .await;

        let client = make_client(&server.url());
        let _ = client
            .query_events(QueryParams {
                contract_id: Some("CAAAA".into()),
                topic_0: Some("transfer".into()),
                ..Default::default()
            })
            .await
            .unwrap();

        mock.assert_async().await;
    }

    #[tokio::test]
    async fn query_events_unauthorized() {
        let mut server = Server::new_async().await;

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(401)
            .with_body("Unauthorized")
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client.query_events(QueryParams::default()).await;

        assert!(matches!(result, Err(TridentError::Unauthorized)));
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn query_events_rate_limited() {
        let mut server = Server::new_async().await;

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(429)
            .with_header("retry-after", "30")
            .with_body("Too Many Requests")
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client.query_events(QueryParams::default()).await;

        assert!(matches!(
            result,
            Err(TridentError::RateLimited {
                retry_after_seconds: 30
            })
        ));
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn get_event_by_id_returns_event() {
        let mut server = Server::new_async().await;

        let body = serde_json::json!({ "event": event_body() });

        let mock = server
            .mock("GET", "/v1/events/550e8400-e29b-41d4-a716-446655440000")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .create_async()
            .await;

        let client = make_client(&server.url());
        let event = client
            .get_event_by_id("550e8400-e29b-41d4-a716-446655440000")
            .await
            .unwrap();

        assert_eq!(event.id, "550e8400-e29b-41d4-a716-446655440000");
        assert_eq!(event.ledger_sequence, 50000);
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn get_event_by_id_not_found() {
        let mut server = Server::new_async().await;

        let mock = server
            .mock("GET", "/v1/events/nonexistent-id")
            .with_status(404)
            .with_body("Not found")
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client.get_event_by_id("nonexistent-id").await;

        assert!(matches!(result, Err(TridentError::NotFound)));
        mock.assert_async().await;
    }

    // ── Retry with backoff (#279) ─────────────────────────────────────────

    fn fast_retry_config(max_attempts: u32) -> RetryConfig {
        RetryConfig {
            max_attempts,
            base_delay: Duration::from_millis(1),
            max_delay: Duration::from_millis(20),
            max_total_wait: Duration::from_secs(1),
            jitter: false,
        }
    }

    #[tokio::test]
    async fn retry_succeeds_after_n_transient_503s() {
        let mut server = Server::new_async().await;

        let body = serde_json::json!({ "events": [], "next_cursor": null, "has_more": false });

        let unavailable = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(503)
            .with_body("temporarily unavailable")
            .expect(2)
            .create_async()
            .await;
        let ok = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .expect(1)
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client
            .query_events_with_retry(QueryParams::default(), Some(fast_retry_config(3)))
            .await;

        assert!(
            result.is_ok(),
            "expected success after retries: {:?}",
            result.err()
        );
        unavailable.assert_async().await;
        ok.assert_async().await;
    }

    #[tokio::test]
    async fn retry_honours_retry_after_header_on_429() {
        let mut server = Server::new_async().await;
        let body = serde_json::json!({ "events": [], "next_cursor": null, "has_more": false });

        let limited = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(429)
            .with_header("retry-after", "0")
            .with_body("slow down")
            .expect(1)
            .create_async()
            .await;
        let ok = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .expect(1)
            .create_async()
            .await;

        let client = make_client(&server.url());
        // Base delay is large; Retry-After: 0 must be honoured instead, so
        // this must complete well within the timeout below.
        let large_backoff_cfg = RetryConfig {
            max_attempts: 3,
            base_delay: Duration::from_secs(5),
            max_delay: Duration::from_secs(5),
            max_total_wait: Duration::from_secs(30),
            jitter: false,
        };

        let result = tokio::time::timeout(
            Duration::from_secs(2),
            client.query_events_with_retry(QueryParams::default(), Some(large_backoff_cfg)),
        )
        .await;

        assert!(
            result.is_ok(),
            "timed out — Retry-After was not honoured over base backoff"
        );
        assert!(result.unwrap().is_ok());
        limited.assert_async().await;
        ok.assert_async().await;
    }

    #[tokio::test]
    async fn retry_gives_up_after_max_attempts_and_surfaces_typed_error() {
        let mut server = Server::new_async().await;

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(503)
            .with_body("still down")
            .expect(3)
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client
            .query_events_with_retry(QueryParams::default(), Some(fast_retry_config(3)))
            .await;

        match result {
            Err(TridentError::RetryExhausted {
                attempts,
                last_error,
            }) => {
                assert_eq!(attempts, 3);
                assert!(matches!(
                    *last_error,
                    TridentError::Http { status: 503, .. }
                ));
            }
            other => panic!("expected RetryExhausted, got {:?}", other),
        }
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn retry_does_not_retry_non_retryable_status() {
        let mut server = Server::new_async().await;

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(401)
            .with_body("bad key")
            .expect(1)
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client
            .query_events_with_retry(QueryParams::default(), Some(fast_retry_config(5)))
            .await;

        assert!(matches!(result, Err(TridentError::Unauthorized)));
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn retry_disabled_by_default_on_client_config() {
        let mut server = Server::new_async().await;

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(503)
            .with_body("down")
            .expect(1)
            .create_async()
            .await;

        // Default TridentConfig::retry is None — a single attempt, no retry.
        let client = make_client(&server.url());
        let result = client.query_events(QueryParams::default()).await;

        assert!(matches!(
            result,
            Err(TridentError::Http { status: 503, .. })
        ));
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn retry_per_call_override_disables_retries() {
        let mut server = Server::new_async().await;

        let mock = server
            .mock("GET", mockito::Matcher::Regex(r"^/v1/events".to_string()))
            .with_status(503)
            .with_body("down")
            .expect(1)
            .create_async()
            .await;

        let mut config = TridentConfig {
            api_url: server.url(),
            api_key: "test-key".to_string(),
            ..Default::default()
        };
        config.retry = Some(fast_retry_config(5));
        let client = TridentClient::new(config).unwrap();

        // Explicitly disable retries for this call only.
        let result = client
            .query_events_with_retry(QueryParams::default(), None)
            .await;

        assert!(matches!(
            result,
            Err(TridentError::Http { status: 503, .. })
        ));
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn retry_applies_to_get_event_by_id() {
        let mut server = Server::new_async().await;
        let body = serde_json::json!({ "event": event_body() });

        let unavailable = server
            .mock("GET", "/v1/events/550e8400-e29b-41d4-a716-446655440000")
            .with_status(503)
            .with_body("down")
            .expect(1)
            .create_async()
            .await;
        let ok = server
            .mock("GET", "/v1/events/550e8400-e29b-41d4-a716-446655440000")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .expect(1)
            .create_async()
            .await;

        let client = make_client(&server.url());
        let result = client
            .get_event_by_id_with_retry(
                "550e8400-e29b-41d4-a716-446655440000",
                Some(fast_retry_config(3)),
            )
            .await;

        assert!(
            result.is_ok(),
            "expected success after retry: {:?}",
            result.err()
        );
        unavailable.assert_async().await;
        ok.assert_async().await;
    }

    #[test]
    fn compute_backoff_exponential_growth_capped_at_max_delay() {
        let cfg = RetryConfig {
            base_delay: Duration::from_millis(10),
            max_delay: Duration::from_millis(100),
            jitter: false,
            ..Default::default()
        };
        assert_eq!(compute_backoff(1, &cfg), Duration::from_millis(10));
        assert_eq!(compute_backoff(2, &cfg), Duration::from_millis(20));
        assert_eq!(compute_backoff(3, &cfg), Duration::from_millis(40));
        assert_eq!(compute_backoff(4, &cfg), Duration::from_millis(80));
        assert_eq!(compute_backoff(5, &cfg), Duration::from_millis(100)); // capped
    }

    #[tokio::test]
    async fn get_health_parses_response() {
        let mut server = Server::new_async().await;
        let body = serde_json::json!({
            "status": "ok",
            "indexer_lag": 4,
            "checks": {
                "postgres": "ok",
                "redis": "ok",
                "grpc_api": "ok"
            }
        });

        let mock = server
            .mock("GET", "/v1/health")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .create_async()
            .await;

        let client = make_client(&server.url());
        let health = client.get_health().await.unwrap();

        assert_eq!(health.status, "ok");
        assert_eq!(health.indexer_lag, Some(4));
        assert_eq!(health.checks.grpc_api, "ok");
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn get_indexer_stats_parses_response() {
        let mut server = Server::new_async().await;
        let body = serde_json::json!({
            "last_ledger_indexed": 100,
            "chain_tip_ledger": 105,
            "lag_ledgers": 5,
            "events_indexed_total": 9000,
            "events_last_poll": 50,
            "avg_poll_duration_ms": 120,
            "last_poll_at": "2024-01-01T00:00:00Z",
            "status": "healthy",
            "network": "testnet"
        });

        let mock = server
            .mock("GET", "/v1/stats/indexer")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .create_async()
            .await;

        let client = make_client(&server.url());
        let stats = client.get_indexer_stats().await.unwrap();

        assert_eq!(stats.status, "healthy");
        assert_eq!(stats.lag_ledgers, Some(5));
        assert_eq!(stats.network, "testnet");
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn get_contract_stats_sends_query_params() {
        let mut server = Server::new_async().await;
        let body = serde_json::json!({
            "contracts": [{
                "contract_id": "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
                "event_count": 20,
                "last_seen_ledger": 50001,
                "last_seen_at": "2024-01-01T00:00:00Z",
                "invocation_count": 2,
                "total_fee_charged": 123,
                "avg_fee_charged": 61.5,
                "avg_cpu_instructions": 100.0,
                "avg_read_bytes": 12.0,
                "avg_write_bytes": 6.0
            }],
            "from_ledger": 10,
            "to_ledger": 99,
            "network": "mainnet",
            "generated_at": "2024-01-01T00:00:00Z"
        });

        let mock = server
            .mock("GET", "/v1/stats/contracts")
            .match_query(mockito::Matcher::AllOf(vec![
                mockito::Matcher::UrlEncoded("from_ledger".into(), "10".into()),
                mockito::Matcher::UrlEncoded("to_ledger".into(), "99".into()),
                mockito::Matcher::UrlEncoded("limit".into(), "5".into()),
                mockito::Matcher::UrlEncoded("network".into(), "mainnet".into()),
            ]))
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(body.to_string())
            .create_async()
            .await;

        let client = make_client(&server.url());
        let stats = client
            .get_contract_stats(ContractStatsQuery {
                from_ledger: Some(10),
                to_ledger: Some(99),
                network: Some(Network::Mainnet),
                limit: Some(5),
            })
            .await
            .unwrap();

        assert_eq!(stats.contracts.len(), 1);
        assert_eq!(
            stats.contracts[0].contract_id,
            "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"
        );
        mock.assert_async().await;
    }

    #[tokio::test]
    async fn iter_events_streams_across_pages() {
        let mut server = Server::new_async().await;

        let page_one = serde_json::json!({
            "events": [event_body()],
            "next_cursor": "cursor-2",
            "has_more": true
        });
        let page_two = serde_json::json!({
            "events": [{
                "id": "550e8400-e29b-41d4-a716-446655440001",
                "contract_id": "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
                "ledger_sequence": 50001,
                "ledger_timestamp": "2024-01-01T00:00:01Z",
                "transaction_hash": "def456",
                "event_index": 1,
                "event_type": "contract",
                "topics": ["transfer"],
                "data": "\"goodbye\"",
                "created_at": "2024-01-01T00:00:01Z"
            }],
            "next_cursor": null,
            "has_more": false
        });

        // query_events always appends `limit=...`, so Matcher::Missing here
        // never actually matched (every request has a non-empty query
        // string) — mockito served its unmatched-request fallback (501)
        // instead. Differentiate the two requests by the query params that
        // actually distinguish them.
        let mock_one = server
            .mock("GET", "/v1/events")
            .match_query(mockito::Matcher::UrlEncoded("limit".into(), "50".into()))
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(page_one.to_string())
            .create_async()
            .await;

        let mock_two = server
            .mock("GET", "/v1/events")
            .match_query(mockito::Matcher::UrlEncoded(
                "cursor".into(),
                "cursor-2".into(),
            ))
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(page_two.to_string())
            .create_async()
            .await;

        let client = make_client(&server.url());
        let mut stream = client.iter_events(QueryParams::default());

        let first = stream.next().await.unwrap().unwrap();
        let second = stream.next().await.unwrap().unwrap();
        assert!(stream.next().await.is_none());

        assert_eq!(first.id, "550e8400-e29b-41d4-a716-446655440000");
        assert_eq!(second.id, "550e8400-e29b-41d4-a716-446655440001");
        mock_one.assert_async().await;
        mock_two.assert_async().await;
    }

    #[tokio::test]
    async fn subscription_terminates_on_drop() {
        use futures::stream;
        let sub = Subscription::new(stream::empty::<Result<SorobanEvent, TridentError>>());
        drop(sub);
    }

    #[test]
    fn new_returns_missing_api_key_error_when_unset() {
        let result = TridentClient::new(TridentConfig {
            api_key: String::new(),
            ..Default::default()
        });
        assert!(matches!(result, Err(TridentError::MissingApiKey)));
    }

    #[tokio::test]
    async fn subscription_yields_items_from_stream() {
        use futures::stream;
        let events = vec![Ok(SorobanEvent {
            id: "test".into(),
            contract_id: "C123".into(),
            ledger_sequence: 1,
            ledger_timestamp: "2024-01-01T00:00:00Z".into(),
            transaction_hash: "hash".into(),
            event_index: 0,
            event_type: EventType::Contract,
            topics: vec!["transfer".into()],
            data: serde_json::Value::Null,
            created_at: "2024-01-01T00:00:00Z".into(),
        })];

        let mut sub = Subscription::new(stream::iter(events));
        let item = sub.next().await.unwrap().unwrap();
        assert_eq!(item.id, "test");
        assert!(sub.next().await.is_none());
    }
}
