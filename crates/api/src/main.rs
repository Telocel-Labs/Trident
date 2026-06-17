use std::net::SocketAddr;
use tracing_subscriber::EnvFilter;

pub mod trident {
    tonic::include_proto!("trident");
}

mod services;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL is required");

    let db_pool = sqlx::PgPool::connect(&database_url).await?;
    tracing::info!("Database connected");

    let redis_url = std::env::var("REDIS_URL").expect("REDIS_URL is required");
    let redis_manager = redis::Client::open(redis_url)?
        .get_connection_manager()
        .await?;
    tracing::info!("Redis connected");

    let addr: SocketAddr = std::env::var("GRPC_ADDR")
        .unwrap_or_else(|_| "0.0.0.0:50051".into())
        .parse()?;

    tracing::info!(%addr, "Trident gRPC server listening");

    let events_service = services::events::EventsServiceImpl::new(db_pool, redis_manager);

    tonic::transport::Server::builder()
        .add_service(trident::events_server::EventsServer::new(events_service))
        .serve(addr)
        .await?;

    Ok(())
}
