use opentelemetry_otlp::WithExportConfig;
use tokio_util::sync::CancellationToken;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::EnvFilter;

mod alerting;
mod config;
mod db;
mod metrics;
mod parser;
mod redis_stream;
mod rpc;
mod streamer;

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
        .with_exporter(opentelemetry_otlp::new_exporter().tonic().with_endpoint(endpoint))
        .with_trace_config(
            opentelemetry_sdk::trace::Config::default()
                .with_sampler(opentelemetry_sdk::trace::Sampler::TraceIdRatioBased(sampling_ratio))
                .with_resource(opentelemetry_sdk::Resource::new(vec![
                    opentelemetry::KeyValue::new("service.name", "trident-indexer"),
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
