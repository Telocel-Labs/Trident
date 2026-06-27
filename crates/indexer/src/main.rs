use tokio_util::sync::CancellationToken;
use tracing_subscriber::EnvFilter;

mod config;
mod db;
mod metrics;
mod parser;
mod redis_stream;
mod rpc;
mod streamer;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    tracing::info!("Trident indexer starting");

    let cfg = config::Config::from_env().unwrap_or_else(|e| {
        eprintln!("{e}");
        std::process::exit(1);
    });

    metrics::install(cfg.metrics_port)?;

    let db_pool = db::connect_pool(&cfg.database_url, cfg.db_pool_size).await?;
    tracing::info!(pool_size = cfg.db_pool_size, "Database connected via pool");

    let redis_client = redis::Client::open(cfg.redis_url.as_str())?;
    let redis_conn = redis_client.get_multiplexed_async_connection().await?;
    tracing::info!("Redis connected");

    let shutdown = CancellationToken::new();

    // Spawn signal watcher — cancels the token on SIGTERM or SIGINT.
    let shutdown_trigger = shutdown.clone();
    tokio::spawn(async move {
        #[cfg(unix)]
        {
            use tokio::signal::unix::{signal, SignalKind};
            let mut sigterm =
                signal(SignalKind::terminate()).expect("failed to register SIGTERM handler");
            tokio::select! {
                _ = tokio::signal::ctrl_c() => {
                    tracing::info!("Received SIGINT, initiating graceful shutdown");
                }
                _ = sigterm.recv() => {
                    tracing::info!("Received SIGTERM, initiating graceful shutdown");
                }
            }
        }
        #[cfg(not(unix))]
        {
            let _ = tokio::signal::ctrl_c().await;
            tracing::info!("Received SIGINT, initiating graceful shutdown");
        }

        shutdown_trigger.cancel();
    });

    let mut s = streamer::Streamer::new(cfg, db_pool, redis_conn).await?;
    s.run(shutdown).await?;

    tracing::info!("Trident indexer stopped");
    Ok(())
}
