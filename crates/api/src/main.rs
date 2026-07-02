use std::net::SocketAddr;
use std::str::FromStr;

use opentelemetry_otlp::WithExportConfig;
use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::EnvFilter;

pub mod trident {
    tonic::include_proto!("trident");
}

mod config;
mod services;

/// Read-heavy service with moderate concurrency, so a moderate pool is correct.
const DEFAULT_DB_POOL_SIZE: u32 = 10;

fn init_tracer() -> Option<opentelemetry_sdk::trace::Tracer> {
    let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT").ok()?;
    let sampling_ratio = std::env::var("OTEL_SAMPLING_RATIO")
        .ok()
        .and_then(|s| s.parse::<f64>().ok())
        .unwrap_or(0.1);

    opentelemetry::global::set_text_map_propagator(
        opentelemetry_sdk::propagation::TraceContextPropagator::new(),
    );

    match opentelemetry_otlp::new_pipeline()
        .tracing()
        .with_exporter(
            opentelemetry_otlp::new_exporter()
                .tonic()
                .with_endpoint(endpoint),
        )
        .with_trace_config(
            opentelemetry_sdk::trace::Config::default()
                .with_sampler(opentelemetry_sdk::trace::Sampler::TraceIdRatioBased(
                    sampling_ratio,
                ))
                .with_resource(opentelemetry_sdk::Resource::new(vec![
                    opentelemetry::KeyValue::new("service.name", "trident-grpc-api"),
                ])),
        )
        .install_batch(opentelemetry_sdk::runtime::Tokio)
    {
        Ok(tracer) => Some(tracer),
        Err(e) => {
            eprintln!("Failed to initialise OpenTelemetry tracer: {e}");
            None
        }
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let tracer = init_tracer();
    tracing_subscriber::registry()
        .with(EnvFilter::from_default_env())
        .with(tracing_subscriber::fmt::layer())
        .with(tracer.map(|t| tracing_opentelemetry::layer().with_tracer(t)))
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
    let connect_options =
        PgConnectOptions::from_str(&cfg.database_url)?.statement_cache_capacity(0);
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
