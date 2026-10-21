//! Prometheus metrics for the gRPC API (`crates/api`), served from a `GET /metrics` HTTP
//! endpoint (default port 9092, configurable via `GRPC_METRICS_PORT` or `METRICS_PORT`).
//!
//! Exposes RED metrics per RPC method (Request count, Error count, Duration histogram),
//! active stream gauge, and Postgres DB pool gauges (issue #790).

use std::net::SocketAddr;

use metrics::{counter, describe_counter, describe_gauge, describe_histogram, gauge, histogram};
use metrics_exporter_prometheus::PrometheusBuilder;
use trident_common::TridentError;

pub const REQUESTS_TOTAL: &str = "trident_grpc_api_requests_total";
pub const ERRORS_TOTAL: &str = "trident_grpc_api_errors_total";
pub const REQUEST_DURATION_SECONDS: &str = "trident_grpc_api_request_duration_seconds";
pub const ACTIVE_STREAMS: &str = "trident_grpc_api_active_streams";
pub const DB_POOL_SIZE: &str = "trident_grpc_api_db_pool_size";
pub const DB_POOL_IDLE_CONNECTIONS: &str = "trident_grpc_api_db_pool_idle_connections";
pub const DB_POOL_ACQUIRED_CONNECTIONS: &str = "trident_grpc_api_db_pool_acquired_connections";

/// Install the global Prometheus recorder and start serving `/metrics` on
/// `port`. Must be called once before the gRPC server starts accepting calls.
pub fn install(port: u16) -> Result<(), TridentError> {
    let addr: SocketAddr = ([0, 0, 0, 0], port).into();

    PrometheusBuilder::new()
        .with_http_listener(addr)
        .install()
        .map_err(|e| {
            TridentError::config(anyhow::Error::new(e).context("failed to start gRPC metrics exporter"))
        })?;

    describe_counter!(
        REQUESTS_TOTAL,
        "Total gRPC requests served, labelled by method and status"
    );
    describe_counter!(
        ERRORS_TOTAL,
        "Total gRPC error responses, labelled by method and code"
    );
    describe_histogram!(
        REQUEST_DURATION_SECONDS,
        "gRPC call latency in seconds, labelled by method"
    );
    describe_gauge!(
        ACTIVE_STREAMS,
        "Current number of active server-streaming event subscribers"
    );
    describe_gauge!(
        DB_POOL_SIZE,
        "Total connection capacity of the gRPC API Postgres pool"
    );
    describe_gauge!(
        DB_POOL_IDLE_CONNECTIONS,
        "Idle connections in the gRPC API Postgres pool"
    );
    describe_gauge!(
        DB_POOL_ACQUIRED_CONNECTIONS,
        "Active connections in use by the gRPC API Postgres pool"
    );

    // Initialise baseline gauge values
    gauge!(ACTIVE_STREAMS).set(0.0);
    gauge!(DB_POOL_SIZE).set(0.0);
    gauge!(DB_POOL_IDLE_CONNECTIONS).set(0.0);
    gauge!(DB_POOL_ACQUIRED_CONNECTIONS).set(0.0);

    Ok(())
}

/// Helper converting tonic Code to human-readable string representation.
pub fn status_code_to_str(code: tonic::Code) -> &'static str {
    match code {
        tonic::Code::Ok => "OK",
        tonic::Code::Cancelled => "CANCELLED",
        tonic::Code::Unknown => "UNKNOWN",
        tonic::Code::InvalidArgument => "INVALID_ARGUMENT",
        tonic::Code::DeadlineExceeded => "DEADLINE_EXCEEDED",
        tonic::Code::NotFound => "NOT_FOUND",
        tonic::Code::AlreadyExists => "ALREADY_EXISTS",
        tonic::Code::PermissionDenied => "PERMISSION_DENIED",
        tonic::Code::ResourceExhausted => "RESOURCE_EXHAUSTED",
        tonic::Code::FailedPrecondition => "FAILED_PRECONDITION",
        tonic::Code::Aborted => "ABORTED",
        tonic::Code::OutOfRange => "OUT_OF_RANGE",
        tonic::Code::Unimplemented => "UNIMPLEMENTED",
        tonic::Code::Internal => "INTERNAL",
        tonic::Code::Unavailable => "UNAVAILABLE",
        tonic::Code::DataLoss => "DATA_LOSS",
        tonic::Code::Unauthenticated => "UNAUTHENTICATED",
    }
}

/// Record successful completion of an RPC method call.
pub fn record_rpc_success(method: &'static str, duration_secs: f64) {
    counter!(REQUESTS_TOTAL, "method" => method, "status" => "OK").increment(1);
    histogram!(REQUEST_DURATION_SECONDS, "method" => method).record(duration_secs);
}

/// Record completion of an RPC method call that resulted in a tonic status error.
pub fn record_rpc_error(method: &'static str, status: &tonic::Status, duration_secs: f64) {
    let code_str = status_code_to_str(status.code());
    counter!(REQUESTS_TOTAL, "method" => method, "status" => code_str).increment(1);
    counter!(ERRORS_TOTAL, "method" => method, "code" => code_str).increment(1);
    histogram!(REQUEST_DURATION_SECONDS, "method" => method).record(duration_secs);
}

/// Increment active stream subscriber gauge.
pub fn increment_active_streams() {
    gauge!(ACTIVE_STREAMS).increment(1.0);
}

/// Decrement active stream subscriber gauge.
pub fn decrement_active_streams() {
    gauge!(ACTIVE_STREAMS).decrement(1.0);
}

/// Set Postgres connection pool utilisation metrics.
pub fn set_db_pool_stats(size: u32, idle: u32) {
    gauge!(DB_POOL_SIZE).set(size as f64);
    gauge!(DB_POOL_IDLE_CONNECTIONS).set(idle as f64);
    let acquired = size.saturating_sub(idle);
    gauge!(DB_POOL_ACQUIRED_CONNECTIONS).set(acquired as f64);
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;

    #[test]
    fn test_status_code_mapping() {
        assert_eq!(status_code_to_str(tonic::Code::Ok), "OK");
        assert_eq!(status_code_to_str(tonic::Code::NotFound), "NOT_FOUND");
        assert_eq!(status_code_to_str(tonic::Code::Internal), "INTERNAL");
        assert_eq!(status_code_to_str(tonic::Code::InvalidArgument), "INVALID_ARGUMENT");
        assert_eq!(status_code_to_str(tonic::Code::DeadlineExceeded), "DEADLINE_EXCEEDED");
        assert_eq!(status_code_to_str(tonic::Code::Unavailable), "UNAVAILABLE");
    }

    #[test]
    fn test_metrics_installation_and_exposition() {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind free port");
        let port = listener.local_addr().unwrap().port();
        drop(listener);

        // Install metrics exporter
        let _ = install(port);

        // Record metrics across methods
        record_rpc_success("list_events", 0.042);
        record_rpc_error("get_event", &tonic::Status::not_found("not found"), 0.015);
        increment_active_streams();
        set_db_pool_stats(10, 8);

        // Connect and scrape /metrics
        if let Ok(mut stream) = std::net::TcpStream::connect(format!("127.0.0.1:{port}")) {
            stream
                .write_all(b"GET /metrics HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
                .unwrap();
            let mut response = String::new();
            stream.read_to_string(&mut response).unwrap();

            assert!(response.contains("trident_grpc_api_requests_total"));
            assert!(response.contains("trident_grpc_api_errors_total"));
            assert!(response.contains("trident_grpc_api_active_streams"));
            assert!(response.contains("trident_grpc_api_db_pool_size"));
            assert!(response.contains("trident_grpc_api_db_pool_idle_connections"));
            assert!(response.contains("trident_grpc_api_db_pool_acquired_connections"));
        }
    }
}
