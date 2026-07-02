//! Prometheus metrics for the indexer, served from a `GET /metrics` HTTP
//! endpoint (default port 9090, configurable via `METRICS_PORT`).
//!
//! [`install`] sets up the global recorder and starts the HTTP listener; the
//! `record_*`/`set_*` helpers below are called from the streamer at the
//! relevant points in `poll_once`.

use std::net::SocketAddr;

use metrics::{counter, describe_counter, describe_gauge, describe_histogram, gauge, histogram};
use metrics_exporter_prometheus::PrometheusBuilder;
use trident_common::TridentError;

pub const LEDGER_LAG: &str = "trident_indexer_ledger_lag";
pub const EVENTS_TOTAL: &str = "trident_indexer_events_total";
pub const EVENTS_SKIPPED_TOTAL: &str = "trident_indexer_events_skipped_total";
pub const PARSE_ERRORS_TOTAL: &str = "trident_indexer_parse_errors_total";
pub const POLL_DURATION_SECONDS: &str = "trident_indexer_poll_duration_seconds";
pub const POLL_ERRORS_TOTAL: &str = "trident_indexer_poll_errors_total";
pub const RPC_RETRIES_TOTAL: &str = "trident_indexer_rpc_retries_total";

/// Install the global Prometheus recorder and start serving `/metrics` on
/// `port`. Must be called once, before the streamer starts recording.
pub fn install(port: u16) -> Result<(), TridentError> {
    let addr: SocketAddr = ([0, 0, 0, 0], port).into();

    PrometheusBuilder::new()
        .with_http_listener(addr)
        .install()
        .map_err(|e| TridentError::ConfigError(format!("failed to start metrics exporter: {e}")))?;

    describe_gauge!(
        LEDGER_LAG,
        "Difference between chain tip and indexer cursor (ledgers)"
    );
    describe_counter!(EVENTS_TOTAL, "Total events processed since startup");
    describe_counter!(
        EVENTS_SKIPPED_TOTAL,
        "Events skipped (diagnostic, failed call, or contract filter)"
    );
    describe_counter!(PARSE_ERRORS_TOTAL, "Total events that failed XDR decoding");
    describe_histogram!(
        POLL_DURATION_SECONDS,
        "Time per poll_once cycle, in seconds"
    );
    describe_counter!(POLL_ERRORS_TOTAL, "Poll cycles that returned an error");
    describe_counter!(
        RPC_RETRIES_TOTAL,
        "Total RPC retries triggered by transient failures"
    );

    // Counters only render in the scrape output once touched at least once;
    // seed them at zero so /metrics is complete from the very first scrape.
    counter!(EVENTS_TOTAL).increment(0);
    counter!(EVENTS_SKIPPED_TOTAL).increment(0);
    counter!(PARSE_ERRORS_TOTAL).increment(0);
    counter!(POLL_ERRORS_TOTAL).increment(0);
    counter!(RPC_RETRIES_TOTAL).increment(0);
    gauge!(LEDGER_LAG).set(0.0);

    tracing::info!(port, "Metrics endpoint listening");
    Ok(())
}

pub fn set_ledger_lag(lag: i64) {
    gauge!(LEDGER_LAG).set(lag as f64);
}

pub fn record_events_processed(count: u64) {
    if count > 0 {
        counter!(EVENTS_TOTAL).increment(count);
    }
}

pub fn record_events_skipped(count: u64) {
    if count > 0 {
        counter!(EVENTS_SKIPPED_TOTAL).increment(count);
    }
}

pub fn record_parse_error() {
    counter!(PARSE_ERRORS_TOTAL).increment(1);
}

pub fn record_poll_duration(seconds: f64) {
    histogram!(POLL_DURATION_SECONDS).record(seconds);
}

pub fn record_poll_error() {
    counter!(POLL_ERRORS_TOTAL).increment(1);
}

pub fn record_rpc_retry() {
    counter!(RPC_RETRIES_TOTAL).increment(1);
}
