use std::net::SocketAddr;
use std::str::FromStr;

use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use tracing_subscriber::EnvFilter;

pub mod trident {
    tonic::include_proto!("trident");
}

mod config;
mod services;

/// Read-heavy service with moderate concurrency, so a moderate pool is correct.
const DEFAULT_DB_POOL_SIZE: u32 = 10;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let cfg = config::Config::from_env().unwrap_or_else(|e| {
        eprintln!("{e}");
        std::process::exit(1);
    });

    let pool_size = std::env::var("GRPC_API_DB_POOL_SIZE")
        .ok()
        .and_then(|s| s.parse::<u32>().ok())
        .filter(|&n| n > 0)
        .unwrap_or(DEFAULT_DB_POOL_SIZE);

    // statement_cache_capacity(0) disables named prepared statements so the pool
    // is safe behind PgBouncer in transaction mode. See docs/deployment.md (#87).
    let connect_options = PgConnectOptions::from_str(&cfg.database_url)?.statement_cache_capacity(0);
    let db_pool = PgPoolOptions::new()
        .max_connections(pool_size)
        .connect_with(connect_options)
        .await?;
    tracing::info!(pool_size, "Database connected via pool");

    let redis_url = std::env::var("REDIS_URL").expect("REDIS_URL is required");
    let redis_manager = redis::Client::open(redis_url)?
        .get_connection_manager()
        .await?;
    tracing::info!("Redis connected");

    let addr: SocketAddr = cfg.grpc_addr.parse()?;

    tracing::info!(%addr, "Trident gRPC server listening");

    let events_service = services::events::EventsServiceImpl::new(db_pool, redis_manager);

    tonic::transport::Server::builder()
        .add_service(trident::events_server::EventsServer::new(events_service))
        .serve(addr)
        .await?;

    Ok(())
}
