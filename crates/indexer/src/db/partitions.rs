use sqlx::PgPool;

/// Ensures future partitions are created ahead of time for events partitioning.
/// Calls the stored procedure to manage and provision partition tables.
pub async fn ensure_future_partitions(pool: &PgPool, months_ahead: i32) -> Result<(), sqlx::Error> {
    sqlx::query("SELECT ensure_future_event_partitions($1)")
        .bind(months_ahead)
        .execute(pool)
        .await?;
    Ok(())
}
