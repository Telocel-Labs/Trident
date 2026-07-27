use serde::Deserialize;
use thiserror::Error;

/// A typed error returned by the Trident API via the canonical error envelope.
///
/// Exposes the HTTP status code, machine-readable error code, human-readable
/// message, and an optional field pointer when the error originates from a
/// specific request field (issue #278).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TridentApiError {
    pub status: u16,
    pub code: String,
    pub message: String,
    pub field: Option<String>,
}

impl std::fmt::Display for TridentApiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match &self.field {
            Some(field) => write!(
                f,
                "API error {} ({}): {} [field: {}]",
                self.status, self.code, self.message, field
            ),
            None => write!(f, "API error {} ({}): {}", self.status, self.code, self.message),
        }
    }
}

#[derive(Deserialize)]
struct ApiErrorEnvelope {
    error: ApiErrorBody,
}

#[derive(Deserialize)]
struct ApiErrorBody {
    code: String,
    message: String,
    field: Option<String>,
}

/// Parse a non-2xx response body into a [`TridentApiError`].
///
/// Reads the canonical `{"error":{"code","message","field"}}` envelope. Falls
/// back to `code = "INTERNAL"` with the raw body when the body is not a valid
/// envelope (issue #278).
pub(crate) fn parse_api_error(status: u16, body: &str) -> TridentApiError {
    if let Ok(env) = serde_json::from_str::<ApiErrorEnvelope>(body) {
        TridentApiError {
            status,
            code: env.error.code,
            message: env.error.message,
            field: env.error.field,
        }
    } else {
        TridentApiError {
            status,
            code: "INTERNAL".into(),
            message: if body.is_empty() {
                format!("HTTP {status}")
            } else {
                body.to_owned()
            },
            field: None,
        }
    }
}

#[derive(Debug, Error)]
pub enum TridentError {
    /// Structured API error parsed from the server's canonical error envelope.
    /// This is the canonical error path for all non-2xx responses (issue #278).
    #[error("{0}")]
    Api(TridentApiError),

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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_canonical_envelope() {
        let body = r#"{"error":{"code":"NOT_FOUND","message":"event not found"}}"#;
        let err = parse_api_error(404, body);
        assert_eq!(err.status, 404);
        assert_eq!(err.code, "NOT_FOUND");
        assert_eq!(err.message, "event not found");
        assert!(err.field.is_none());
    }

    #[test]
    fn parses_envelope_with_field() {
        let body = r#"{"error":{"code":"INVALID_ARGUMENT","message":"must be positive","field":"limit"}}"#;
        let err = parse_api_error(400, body);
        assert_eq!(err.code, "INVALID_ARGUMENT");
        assert_eq!(err.field.as_deref(), Some("limit"));
    }

    #[test]
    fn flat_body_falls_back_to_internal() {
        let err = parse_api_error(500, "internal server error");
        assert_eq!(err.status, 500);
        assert_eq!(err.code, "INTERNAL");
        assert_eq!(err.message, "internal server error");
    }

    #[test]
    fn empty_body_uses_http_status_message() {
        let err = parse_api_error(503, "");
        assert_eq!(err.code, "INTERNAL");
        assert_eq!(err.message, "HTTP 503");
    }

    #[test]
    fn malformed_json_falls_back_to_internal() {
        let err = parse_api_error(400, "{not valid");
        assert_eq!(err.code, "INTERNAL");
    }

    #[test]
    fn display_without_field() {
        let err = TridentApiError {
            status: 429,
            code: "RATE_LIMITED".into(),
            message: "slow down".into(),
            field: None,
        };
        assert_eq!(err.to_string(), "API error 429 (RATE_LIMITED): slow down");
    }

    #[test]
    fn display_with_field() {
        let err = TridentApiError {
            status: 400,
            code: "INVALID_ARGUMENT".into(),
            message: "must be positive".into(),
            field: Some("limit".into()),
        };
        assert_eq!(
            err.to_string(),
            "API error 400 (INVALID_ARGUMENT): must be positive [field: limit]"
        );
    }

    // Cross-SDK golden payload — must decode identically in all SDK languages (issue #278).
    #[test]
    fn cross_sdk_golden_payload() {
        let golden = r#"{"error":{"code":"UNAUTHORIZED","message":"invalid or missing API key"}}"#;
        let err = parse_api_error(401, golden);
        assert_eq!(err.status, 401);
        assert_eq!(err.code, "UNAUTHORIZED");
        assert!(err.field.is_none());
    }
}
