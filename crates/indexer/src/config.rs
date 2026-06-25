use std::time::Duration;
use trident_common::TridentError;

pub struct Config {
    pub database_url: String,
    pub redis_url: String,
    pub stellar_rpc_url: String,
    pub network: String,
    pub poll_interval: Duration,
    pub index_diagnostic: bool,
    pub metrics_port: u16,
}

impl Config {
    pub fn from_env() -> Result<Self, TridentError> {
        Ok(Self {
            database_url: require_env("DATABASE_URL")?,
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
            metrics_port: std::env::var("METRICS_PORT")
                .ok()
                .and_then(|s| s.parse().ok())
                .unwrap_or(9090),
        })
    }
}

fn require_env(key: &str) -> Result<String, TridentError> {
    std::env::var(key)
        .map_err(|_| TridentError::ConfigError(format!("{key} is required but not set")))
}
