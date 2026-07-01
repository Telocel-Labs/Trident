use thiserror::Error;

#[derive(Debug, Error)]
pub enum TridentError {
    #[error("HTTP error: {status} {message}")]
    Http { status: u16, message: String },

    #[error("Unauthorized: invalid or missing API key")]
    Unauthorized,

    #[error("Not found")]
    NotFound,

    #[error("Rate limited: retry after {retry_after_seconds}s")]
    RateLimited { retry_after_seconds: u64 },

    #[error("Network error: {0}")]
    Network(#[from] reqwest::Error),

    #[error("WebSocket error: {0}")]
    WebSocket(String),

    #[error("Deserialization error: {0}")]
    Deserialize(#[from] serde_json::Error),
}
