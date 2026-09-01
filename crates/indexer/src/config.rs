use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Config {
    pub database_url: String,
    pub redis_url: String,
    pub event_retention_days: Option<u64>,
}

impl Config {
    pub fn from_env() -> Self {
        Self {
            database_url: std::env::var("DATABASE_URL").expect("DATABASE_URL must be set"),
            redis_url: std::env::var("REDIS_URL").expect("REDIS_URL must be set"),
            event_retention_days: std::env::var("EVENT_RETENTION_DAYS")
                .ok()
                .and_then(|v| v.parse().ok()),
        }
    }
}