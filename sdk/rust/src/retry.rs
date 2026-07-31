use std::time::Duration;

use rand::Rng;

/// Retry policy for idempotent HTTP requests (GET). Honours `Retry-After` on
/// 429/503 responses, falling back to exponential backoff with jitter
/// otherwise.
///
/// `None` in [`TridentConfig::retry`](crate::TridentConfig::retry) (or in a
/// `*_with_retry` call) disables retries entirely — the default.
#[derive(Debug, Clone)]
pub struct RetryConfig {
    /// Total number of attempts, including the first.
    pub max_attempts: u32,
    /// Base delay used for exponential backoff.
    pub base_delay: Duration,
    /// Upper bound for a single computed backoff delay.
    pub max_delay: Duration,
    /// Upper bound on total time spent waiting across all retries
    /// (including any honoured `Retry-After`).
    pub max_total_wait: Duration,
    /// Randomize each computed delay in `[0, delay]`.
    pub jitter: bool,
}

impl Default for RetryConfig {
    fn default() -> Self {
        RetryConfig {
            max_attempts: 3,
            base_delay: Duration::from_millis(100),
            max_delay: Duration::from_secs(2),
            max_total_wait: Duration::from_secs(10),
            jitter: true,
        }
    }
}

/// Only 429 (rate limited) and 503 (service unavailable) are retried.
pub(crate) fn is_retryable_status(status: u16) -> bool {
    status == 429 || status == 503
}

/// Parse a `Retry-After` header value expressed as a number of seconds (the
/// delta-seconds form from RFC 9110; the HTTP-date form is not supported).
pub(crate) fn parse_retry_after_seconds(
    header: Option<&reqwest::header::HeaderValue>,
) -> Option<Duration> {
    header
        .and_then(|v| v.to_str().ok())
        .map(str::trim)
        .and_then(|s| s.parse::<u64>().ok())
        .map(Duration::from_secs)
}

/// Exponential backoff with optional full jitter, capped at `max_delay`.
pub(crate) fn compute_backoff(attempt: u32, cfg: &RetryConfig) -> Duration {
    let shift = attempt.saturating_sub(1).min(31);
    let exp = cfg
        .base_delay
        .saturating_mul(1u32.checked_shl(shift).unwrap_or(u32::MAX));
    let capped = exp.min(cfg.max_delay);
    if !cfg.jitter {
        return capped;
    }
    let millis = capped.as_millis() as u64;
    if millis == 0 {
        return Duration::from_millis(0);
    }
    Duration::from_millis(rand::thread_rng().gen_range(0..=millis))
}
