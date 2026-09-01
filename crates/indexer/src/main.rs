use std::time::Duration;
use tokio::time;
use sqlx::PgPool;

async fn maintain_partitions(pool: PgPool) {
    let mut interval = time::interval(Duration::from_secs(86400)); // Once per day
    loop {
        interval.tick().await;
        if let Err(e) = sqlx::query("SELECT ensure_future_event_partitions(3)")
            .execute(&pool)
            .await
        {
            tracing::error!(error = %e, "failed to run ensure_future_event_partitions");
        }
    }
}

// ... existing main implementation ...
#[tokio::main]
async fn main() -> anyhow::Result<()> {
    // ... existing setup ...
    let pool = PgPool::connect(&std::env::var("DATABASE_URL")?).await?;
    tokio::spawn(maintain_partitions(pool));
    // ... rest of main ...