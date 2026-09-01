use crate::config::Config;
use sqlx::PgPool;
use tokio::time::{sleep, Duration};

mod config;
mod db;
mod metrics;
// ... other imports

async fn run_retention_job(pool: PgPool, days: u64) {
    let mut interval = tokio::time::interval(Duration::from_secs(86400)); // Run daily
    loop {
        interval.tick().await;
        let result = sqlx::query("SELECT prune_soroban_events($1)")
            .bind(days as i32)
            .execute(&pool)
            .await;
        
        match result {
            Ok(_) => metrics::RETENTION_JOB_SUCCESS.inc(),
            Err(e) => {
                tracing::error!("Retention job failed: {:?}", e);
                metrics::RETENTION_JOB_FAILURE.inc();
            }
        }
    }
}

#[tokio::main]
async fn main() {
    let cfg = Config::from_env();
    let pool = PgPool::connect(&cfg.database_url).await.unwrap();

    if let Some(days) = cfg.event_retention_days {
        tokio::spawn(run_retention_job(pool.clone(), days));
    }

    // ... existing startup logic
}