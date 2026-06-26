use std::time::Duration;
use trident_common::TridentError;

pub struct Config {
    pub database_url: String,
    pub db_pool_size: u32,
    pub redis_url: String,
    pub stellar_rpc_url: String,
    pub network: String,
    pub poll_interval: Duration,
    pub index_diagnostic: bool,
    pub redis_stream_maxlen: u64,
}

/// Default Postgres pool size for the indexer. It is a single writer with low
/// write concurrency, so a small pool is correct (issue #87).
const DEFAULT_DB_POOL_SIZE: u32 = 3;

impl Config {
    pub fn from_env() -> Result<Self, TridentError> {
        Ok(Self {
            database_url: require_env("DATABASE_URL")?,
            db_pool_size: parse_pool_size("INDEXER_DB_POOL_SIZE", DEFAULT_DB_POOL_SIZE)?,
            redis_url: require_env("REDIS_URL")?,
            stellar_rpc_url: require_env("STELLAR_RPC_URL")?,
            network: std::env::var("NETWORK").unwrap_or_else(|_| "testnet".into()),
            poll_interval: Duration::from_millis(
                std::env::var("POLL_INTERVAL_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(5000),
            ),
            index_diagnostic: std::env::var("INDEX_DIAGNOSTIC")
                .map(|v| v.eq_ignore_ascii_case("true"))
                .unwrap_or(false),
            redis_stream_maxlen: std::env::var("REDIS_STREAM_MAXLEN")
                .ok()
                .and_then(|s| s.parse().ok())
                .unwrap_or(10_000),
        })
    }
}

fn require_env(key: &str) -> Result<String, TridentError> {
    std::env::var(key)
        .map_err(|_| TridentError::ConfigError(format!("{key} is required but not set")))
}

/// Parse an optional positive pool-size env var, falling back to `default`.
/// A present-but-invalid value (non-numeric or zero) is a hard configuration
/// error rather than a silent fallback.
fn parse_pool_size(key: &str, default: u32) -> Result<u32, TridentError> {
    match std::env::var(key) {
        Err(_) => Ok(default),
        Ok(raw) => raw
            .parse::<u32>()
            .ok()
            .filter(|&n| n > 0)
            .ok_or_else(|| TridentError::ConfigError(format!("{key} must be a positive integer"))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_pool_size_uses_default_when_unset() {
        std::env::remove_var("TEST_POOL_UNSET");
        assert_eq!(parse_pool_size("TEST_POOL_UNSET", 7).unwrap(), 7);
    }

    #[test]
    fn parse_pool_size_reads_valid_value() {
        std::env::set_var("TEST_POOL_VALID", "12");
        assert_eq!(parse_pool_size("TEST_POOL_VALID", 3).unwrap(), 12);
        std::env::remove_var("TEST_POOL_VALID");
    }

    #[test]
    fn parse_pool_size_rejects_zero_and_garbage() {
        std::env::set_var("TEST_POOL_BAD", "0");
        assert!(parse_pool_size("TEST_POOL_BAD", 3).is_err());
        std::env::set_var("TEST_POOL_BAD", "abc");
        assert!(parse_pool_size("TEST_POOL_BAD", 3).is_err());
        std::env::remove_var("TEST_POOL_BAD");
    }
}
