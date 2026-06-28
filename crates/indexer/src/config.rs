use std::time::Duration;
use trident_common::TridentError;

#[derive(Debug)]
pub struct Config {
    pub database_url: String,
    pub db_pool_size: u32,
    pub redis_url: String,
    pub stellar_rpc_url: String,
    pub network: String,
    pub poll_interval: Duration,
    pub index_diagnostic: bool,
    pub max_events_per_poll: u32,
    pub redis_stream_maxlen: u64,
    pub metrics_port: u16,
    pub alert_webhook_url: Option<String>,
    pub alert_lag_threshold: u64,
    pub alert_cooldown_minutes: u64,
}

/// Default Postgres pool size for the indexer. It is a single writer with low
/// write concurrency, so a small pool is correct (issue #87).
const DEFAULT_DB_POOL_SIZE: u32 = 3;

impl Config {
    pub fn from_env() -> Result<Self, TridentError> {
        let mut missing: Vec<&str> = Vec::new();

        let database_url = collect_required("DATABASE_URL", &mut missing);
        let redis_url = collect_required("REDIS_URL", &mut missing);
        let stellar_rpc_url = collect_required("STELLAR_RPC_URL", &mut missing);

        if !missing.is_empty() {
            return Err(TridentError::ConfigError(format!(
                "[trident-indexer] missing required env vars:\n{}",
                missing.join("\n")
            )));
        }

        let network = std::env::var("NETWORK").unwrap_or_else(|_| "testnet".into());

        let poll_interval_ms = parse_bounded_u64("POLL_INTERVAL_MS", 1000, 100, 60_000)?;
        let max_events_per_poll = parse_bounded_u64("MAX_EVENTS_PER_POLL", 200, 1, 10_000)?;

        let index_diagnostic = std::env::var("INDEX_DIAGNOSTIC")
            .map(|v| v.eq_ignore_ascii_case("true"))
            .unwrap_or(false);

            let alert_webhook_url = std::env::var("ALERT_WEBHOOK_URL")
            .ok()
            .filter(|s| !s.is_empty());
        let alert_lag_threshold = parse_bounded_u64("ALERT_LAG_THRESHOLD", 200, 1, 1_000_000)?;
        let alert_cooldown_minutes = parse_bounded_u64("ALERT_COOLDOWN_MINUTES", 30, 1, 10_080)?;

        Ok(Self {
            database_url: database_url.unwrap(),
            db_pool_size: parse_pool_size("INDEXER_DB_POOL_SIZE", DEFAULT_DB_POOL_SIZE)?,
            redis_url: redis_url.unwrap(),
            stellar_rpc_url: stellar_rpc_url.unwrap(),
            network,
            poll_interval: Duration::from_millis(poll_interval_ms),
            index_diagnostic,
            max_events_per_poll: max_events_per_poll as u32,
            redis_stream_maxlen: std::env::var("REDIS_STREAM_MAXLEN")
                .ok()
                .and_then(|s| s.parse().ok())
                .unwrap_or(10_000),
            metrics_port: std::env::var("METRICS_PORT")
                .ok()
                .and_then(|s| s.parse().ok())
                .unwrap_or(9090),
            alert_webhook_url,
            alert_lag_threshold,
            alert_cooldown_minutes,
        })
    }
}

/// Read a required env var; on absence push its name to `missing` and return None.
fn collect_required<'a>(key: &'a str, missing: &mut Vec<&'a str>) -> Option<String> {
    match std::env::var(key) {
        Ok(v) if !v.is_empty() => Some(v),
        _ => {
            missing.push(key);
            None
        }
    }
}

/// Parse an env var as u64 with a default and inclusive [min, max] bounds.
fn parse_bounded_u64(key: &str, default: u64, min: u64, max: u64) -> Result<u64, TridentError> {
    match std::env::var(key) {
        Err(_) => Ok(default),
        Ok(raw) => {
            let v: u64 = raw.parse().map_err(|_| {
                TridentError::ConfigError(format!(
                    "[indexer] {key} must be a positive integer, got {raw:?}"
                ))
            })?;
            if v < min || v > max {
                return Err(TridentError::ConfigError(format!(
                    "[indexer] {key} must be between {min} and {max}, got {v}"
                )));
            }
            Ok(v)
        }
    }
}

/// Parse an optional positive pool-size env var, falling back to `default`.
/// A present-but-invalid value (non-numeric or zero) is a hard configuration
/// error rather than a silent fallback.
fn parse_pool_size(key: &str, default: u32) -> Result<u32, TridentError> {
    match std::env::var(key) {
        Err(_) => Ok(default),
        Ok(raw) => {
            raw.parse::<u32>().ok().filter(|&n| n > 0).ok_or_else(|| {
                TridentError::ConfigError(format!("{key} must be a positive integer"))
            })
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::env;

    fn with_env<F: FnOnce()>(pairs: &[(&str, &str)], f: F) {
        for (k, v) in pairs {
            env::set_var(k, v);
        }
        f();
        for (k, _) in pairs {
            env::remove_var(k);
        }
    }

    fn required_vars() -> Vec<(&'static str, &'static str)> {
        vec![
            ("DATABASE_URL", "postgres://localhost/test"),
            ("REDIS_URL", "redis://localhost:6379"),
            ("STELLAR_RPC_URL", "https://soroban-testnet.stellar.org"),
        ]
    }

    #[test]
    fn missing_all_required_vars_lists_all_in_error() {
        env::remove_var("DATABASE_URL");
        env::remove_var("REDIS_URL");
        env::remove_var("STELLAR_RPC_URL");
        env::remove_var("POLL_INTERVAL_MS");
        env::remove_var("MAX_EVENTS_PER_POLL");

        let err = Config::from_env().unwrap_err();
        let msg = err.to_string();
        assert!(
            msg.contains("DATABASE_URL"),
            "error should mention DATABASE_URL"
        );
        assert!(msg.contains("REDIS_URL"), "error should mention REDIS_URL");
        assert!(
            msg.contains("STELLAR_RPC_URL"),
            "error should mention STELLAR_RPC_URL"
        );
    }

    #[test]
    fn missing_single_required_var_names_it() {
        env::set_var("DATABASE_URL", "postgres://localhost/test");
        env::set_var("STELLAR_RPC_URL", "https://soroban-testnet.stellar.org");
        env::remove_var("REDIS_URL");
        env::remove_var("POLL_INTERVAL_MS");
        env::remove_var("MAX_EVENTS_PER_POLL");

        let err = Config::from_env().unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("REDIS_URL"));
        assert!(
            !msg.contains("DATABASE_URL"),
            "DATABASE_URL should not appear"
        );

        env::remove_var("DATABASE_URL");
        env::remove_var("STELLAR_RPC_URL");
    }

    #[test]
    fn poll_interval_default_is_1000ms() {
        let vars = required_vars();
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            env::remove_var("MAX_EVENTS_PER_POLL");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.poll_interval.as_millis(), 1000);
        });
    }

    #[test]
    fn poll_interval_custom_value() {
        let mut vars = required_vars();
        vars.push(("POLL_INTERVAL_MS", "500"));
        with_env(&vars, || {
            env::remove_var("MAX_EVENTS_PER_POLL");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.poll_interval.as_millis(), 500);
        });
    }

    #[test]
    fn poll_interval_below_minimum_is_rejected() {
        let mut vars = required_vars();
        vars.push(("POLL_INTERVAL_MS", "50"));
        with_env(&vars, || {
            env::remove_var("MAX_EVENTS_PER_POLL");
            let err = Config::from_env().unwrap_err();
            assert!(err.to_string().contains("POLL_INTERVAL_MS"));
        });
    }

    #[test]
    fn poll_interval_above_maximum_is_rejected() {
        let mut vars = required_vars();
        vars.push(("POLL_INTERVAL_MS", "90000"));
        with_env(&vars, || {
            env::remove_var("MAX_EVENTS_PER_POLL");
            let err = Config::from_env().unwrap_err();
            assert!(err.to_string().contains("POLL_INTERVAL_MS"));
        });
    }

    #[test]
    fn poll_interval_non_integer_is_rejected() {
        let mut vars = required_vars();
        vars.push(("POLL_INTERVAL_MS", "abc"));
        with_env(&vars, || {
            env::remove_var("MAX_EVENTS_PER_POLL");
            let err = Config::from_env().unwrap_err();
            assert!(err.to_string().contains("POLL_INTERVAL_MS"));
        });
    }

    #[test]
    fn poll_interval_boundary_min_accepted() {
        let mut vars = required_vars();
        vars.push(("POLL_INTERVAL_MS", "100"));
        with_env(&vars, || {
            env::remove_var("MAX_EVENTS_PER_POLL");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.poll_interval.as_millis(), 100);
        });
    }

    #[test]
    fn poll_interval_boundary_max_accepted() {
        let mut vars = required_vars();
        vars.push(("POLL_INTERVAL_MS", "60000"));
        with_env(&vars, || {
            env::remove_var("MAX_EVENTS_PER_POLL");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.poll_interval.as_millis(), 60000);
        });
    }

    #[test]
    fn max_events_per_poll_default_is_200() {
        let vars = required_vars();
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            env::remove_var("MAX_EVENTS_PER_POLL");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.max_events_per_poll, 200);
        });
    }

    #[test]
    fn max_events_per_poll_custom_value() {
        let mut vars = required_vars();
        vars.push(("MAX_EVENTS_PER_POLL", "500"));
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.max_events_per_poll, 500);
        });
    }

    #[test]
    fn max_events_per_poll_below_minimum_is_rejected() {
        let mut vars = required_vars();
        vars.push(("MAX_EVENTS_PER_POLL", "0"));
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            let err = Config::from_env().unwrap_err();
            assert!(err.to_string().contains("MAX_EVENTS_PER_POLL"));
        });
    }

    #[test]
    fn max_events_per_poll_above_maximum_is_rejected() {
        let mut vars = required_vars();
        vars.push(("MAX_EVENTS_PER_POLL", "10001"));
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            let err = Config::from_env().unwrap_err();
            assert!(err.to_string().contains("MAX_EVENTS_PER_POLL"));
        });
    }

    #[test]
    fn max_events_per_poll_invalid_string_is_rejected() {
        let mut vars = required_vars();
        vars.push(("MAX_EVENTS_PER_POLL", "not-a-number"));
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            let err = Config::from_env().unwrap_err();
            assert!(err.to_string().contains("MAX_EVENTS_PER_POLL"));
        });
    }

    #[test]
    fn max_events_per_poll_boundary_min_accepted() {
        let mut vars = required_vars();
        vars.push(("MAX_EVENTS_PER_POLL", "1"));
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.max_events_per_poll, 1);
        });
    }

    #[test]
    fn max_events_per_poll_boundary_max_accepted() {
        let mut vars = required_vars();
        vars.push(("MAX_EVENTS_PER_POLL", "10000"));
        with_env(&vars, || {
            env::remove_var("POLL_INTERVAL_MS");
            let cfg = Config::from_env().unwrap();
            assert_eq!(cfg.max_events_per_poll, 10000);
        });
    }

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
