use trident_common::TridentError;

#[derive(Debug)]
pub struct Config {
    pub database_url: String,
    pub grpc_addr: String,
    pub mtls: Option<MtlsConfig>,
}

/// Internal mTLS between the Go API and this gRPC service (issue #320).
/// Off by default (`GRPC_MTLS_ENABLED` unset/false) — TLS is terminated at
/// the edge (nginx/ingress) and this hop stays inside the cluster network.
/// When enabled, cert/key/CA paths point at files mounted from a Kubernetes
/// Secret (see helm/trident/templates/grpc-api-deployment.yaml and
/// `internalMTLS` in values.yaml) — never baked into the image.
#[derive(Debug)]
pub struct MtlsConfig {
    pub ca_cert_path: String,
    pub server_cert_path: String,
    pub server_key_path: String,
}

impl Config {
    pub fn from_env() -> Result<Self, TridentError> {
        let mut missing: Vec<&str> = Vec::new();

        let database_url = collect_required("DATABASE_URL", &mut missing);
        let grpc_addr = collect_required("GRPC_ADDR", &mut missing);

        let mtls_enabled = std::env::var("GRPC_MTLS_ENABLED")
            .map(|v| v == "true")
            .unwrap_or(false);

        let mtls = if mtls_enabled {
            let ca_cert_path = collect_required("GRPC_MTLS_CA_CERT", &mut missing);
            let server_cert_path = collect_required("GRPC_MTLS_SERVER_CERT", &mut missing);
            let server_key_path = collect_required("GRPC_MTLS_SERVER_KEY", &mut missing);
            if missing.is_empty() {
                Some(MtlsConfig {
                    ca_cert_path: ca_cert_path.unwrap(),
                    server_cert_path: server_cert_path.unwrap(),
                    server_key_path: server_key_path.unwrap(),
                })
            } else {
                None
            }
        } else {
            None
        };

        if !missing.is_empty() {
            return Err(TridentError::config(anyhow::anyhow!(
                "[trident-api] missing required env vars:\n{}",
                missing.join("\n")
            )));
        }

        Ok(Self {
            database_url: database_url.unwrap(),
            grpc_addr: grpc_addr.unwrap(),
            mtls,
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

    /// Process environment is global state shared by every test thread, so all
    /// env-mutating tests serialise on this lock. Without it one test clearing
    /// `DATABASE_URL` fails another mid-`from_env`.
    static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

    fn env_guard() -> std::sync::MutexGuard<'static, ()> {
        // A panicking test must not poison the lock for the rest of the suite.
        ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn with_env<F: FnOnce()>(pairs: &[(&str, &str)], f: F) {
        let _guard = env_guard();
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
        let _guard = env_guard();
        env::remove_var("DATABASE_URL");
        env::remove_var("GRPC_ADDR");

        let err = Config::from_env().unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("DATABASE_URL"));
        assert!(msg.contains("GRPC_ADDR"));
    }

    #[test]
    fn missing_database_url_only() {
        let _guard = env_guard();
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
        let _guard = env_guard();
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
                assert!(cfg.mtls.is_none());
            },
        );
    }

    #[test]
    fn mtls_disabled_by_default() {
        // Note: do not also take env_guard() here — with_env() below takes
        // it internally, and the guard mutex is not reentrant.
        env::remove_var("GRPC_MTLS_ENABLED");
        with_env(
            &[
                ("DATABASE_URL", "postgres://localhost/trident"),
                ("GRPC_ADDR", "0.0.0.0:50051"),
            ],
            || {
                let cfg = Config::from_env().unwrap();
                assert!(cfg.mtls.is_none());
            },
        );
    }

    #[test]
    fn mtls_enabled_populates_cert_paths() {
        with_env(
            &[
                ("DATABASE_URL", "postgres://localhost/trident"),
                ("GRPC_ADDR", "0.0.0.0:50051"),
                ("GRPC_MTLS_ENABLED", "true"),
                ("GRPC_MTLS_CA_CERT", "/certs/ca.crt"),
                ("GRPC_MTLS_SERVER_CERT", "/certs/server.crt"),
                ("GRPC_MTLS_SERVER_KEY", "/certs/server.key"),
            ],
            || {
                let cfg = Config::from_env().unwrap();
                let mtls = cfg.mtls.expect("mtls config should be present");
                assert_eq!(mtls.ca_cert_path, "/certs/ca.crt");
                assert_eq!(mtls.server_cert_path, "/certs/server.crt");
                assert_eq!(mtls.server_key_path, "/certs/server.key");
            },
        );
    }

    #[test]
    fn mtls_enabled_without_cert_paths_errors() {
        let _guard = env_guard();
        env::set_var("DATABASE_URL", "postgres://localhost/trident");
        env::set_var("GRPC_ADDR", "0.0.0.0:50051");
        env::set_var("GRPC_MTLS_ENABLED", "true");
        env::remove_var("GRPC_MTLS_CA_CERT");
        env::remove_var("GRPC_MTLS_SERVER_CERT");
        env::remove_var("GRPC_MTLS_SERVER_KEY");

        let err = Config::from_env().unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("GRPC_MTLS_CA_CERT"));
        assert!(msg.contains("GRPC_MTLS_SERVER_CERT"));
        assert!(msg.contains("GRPC_MTLS_SERVER_KEY"));

        env::remove_var("DATABASE_URL");
        env::remove_var("GRPC_ADDR");
        env::remove_var("GRPC_MTLS_ENABLED");
    }
}
