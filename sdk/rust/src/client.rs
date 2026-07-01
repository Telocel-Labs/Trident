use futures::StreamExt;
use serde::Deserialize;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::http::HeaderValue;
use tokio_tungstenite::tungstenite::Message;

use crate::{
    EventType, PaginatedEvents, QueryParams, SorobanEvent, Subscription, TridentConfig,
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

async fn check_response(
    response: reqwest::Response,
) -> Result<reqwest::Response, TridentError> {
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
    pub async fn query_events(
        &self,
        params: QueryParams,
    ) -> Result<PaginatedEvents, TridentError> {
        let mut url =
            url::Url::parse(&format!("{}/v1/events", self.config.api_url)).map_err(|e| {
                TridentError::WebSocket(e.to_string())
            })?;

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
            qs.append_pair(
                "limit",
                &params.first.unwrap_or(50).to_string(),
            );
            if let Some(et) = &params.event_type {
                qs.append_pair("event_type", et);
            }
        }

        let response = self
            .http
            .get(url)
            .headers(self.headers())
            .send()
            .await?;

        let response = check_response(response).await?;
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
        let url = format!(
            "{}/v1/events/{}",
            self.config.api_url,
            url::form_urlencoded::byte_serialize(id.as_bytes()).collect::<String>()
        );

        let response = self
            .http
            .get(&url)
            .headers(self.headers())
            .send()
            .await?;

        let response = check_response(response).await?;
        let body: ApiGetResponse = response.json().await?;
        Ok(api_event_to_soroban(body.event))
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
            .mock(
                "GET",
                "/v1/events/550e8400-e29b-41d4-a716-446655440000",
            )
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

    #[tokio::test]
    async fn subscription_terminates_on_drop() {
        use futures::stream;
        let sub = Subscription::new(
            stream::empty::<Result<SorobanEvent, TridentError>>(),
        );
        drop(sub);
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
