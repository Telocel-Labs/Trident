use trident_common::TridentError;

#[derive(Debug)]
pub struct Config {
    pub database_url: String,
    pub grpc_addr: String,
}

impl Config {
    pub fn from_env() -> Result<Self, TridentError> {
        let mut missing: Vec<&str> = Vec::new();

        let database_url = collect_required("DATABASE_URL", &mut missing);
        let grpc_addr = collect_required("GRPC_ADDR", &mut missing);

        if !missing.is_empty() {
            return Err(TridentError::ConfigError(format!(
                "[trident-api] missing required env vars:\n{}",
                missing.join("\n")
            )));
        }

        Ok(Self {
            database_url: database_url.unwrap(),
            grpc_addr: grpc_addr.unwrap(),
        })
    }
}

fn collect_required<'a>(key: &'a str, missing: &mut Vec<&'a str>) -> Option<String> {
    match std::env::var(key) {
        Ok(v) if !v.is_empty() => Some(v),
        _ => {
            missing.push(key);
            None
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

    #[test]
    fn missing_both_required_vars_lists_both() {
        env::remove_var("DATABASE_URL");
        env::remove_var("GRPC_ADDR");

        let err = Config::from_env().unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("DATABASE_URL"));
        assert!(msg.contains("GRPC_ADDR"));
    }

    #[test]
    fn missing_database_url_only() {
        env::remove_var("DATABASE_URL");
        env::set_var("GRPC_ADDR", "0.0.0.0:50051");

        let err = Config::from_env().unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("DATABASE_URL"));
        assert!(!msg.contains("GRPC_ADDR"));

        env::remove_var("GRPC_ADDR");
    }

    #[test]
    fn missing_grpc_addr_only() {
        env::set_var("DATABASE_URL", "postgres://localhost/test");
        env::remove_var("GRPC_ADDR");

        let err = Config::from_env().unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("GRPC_ADDR"));
        assert!(!msg.contains("DATABASE_URL"));

        env::remove_var("DATABASE_URL");
    }

    #[test]
    fn all_vars_set_returns_config() {
        with_env(
            &[
                ("DATABASE_URL", "postgres://localhost/trident"),
                ("GRPC_ADDR", "0.0.0.0:50051"),
            ],
            || {
                let cfg = Config::from_env().unwrap();
                assert_eq!(cfg.database_url, "postgres://localhost/trident");
                assert_eq!(cfg.grpc_addr, "0.0.0.0:50051");
            },
        );
    }
}
