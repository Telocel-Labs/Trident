use std::net::SocketAddr;

use metrics::{counter, describe_counter, describe_gauge, gauge};
use metrics_exporter_prometheus::PrometheusBuilder;
use trident_common::TridentError;

pub const BACKFILL_PENDING_JOBS: &str = "trident_backfill_pending_jobs";
pub const BACKFILL_OLDEST_PENDING_AGE_SECONDS: &str = "trident_backfill_oldest_pending_age_seconds";
pub const BACKFILL_STALE_RUNNING_JOBS: &str = "trident_backfill_stale_running_jobs";
pub const BACKFILL_FAILED_JOBS: &str = "trident_backfill_failed_jobs";
pub const BACKFILL_JOBS_COMPLETED_TOTAL: &str = "trident_backfill_jobs_completed_total";
pub const BACKFILL_JOBS_FAILED_TOTAL: &str = "trident_backfill_jobs_failed_total";
pub const BACKFILL_EVENTS_INDEXED_TOTAL: &str = "trident_backfill_events_indexed_total";

pub fn install(port: u16) -> Result<(), TridentError> {
    let addr: SocketAddr = ([0, 0, 0, 0], port).into();
    PrometheusBuilder::new()
        .with_http_listener(addr)
        .install()
        .map_err(|e| {
            TridentError::config(anyhow::Error::new(e).context("failed to start backfill metrics exporter"))
        })?;

    describe_gauge!(BACKFILL_PENDING_JOBS, "Number of pending backfill jobs in the queue");
    describe_gauge!(
        BACKFILL_OLDEST_PENDING_AGE_SECONDS,
        "Age in seconds of the oldest pending backfill job"
    );
    describe_gauge!(
        BACKFILL_STALE_RUNNING_JOBS,
        "Number of stale running backfill jobs exceeding timeout threshold"
    );
    describe_gauge!(BACKFILL_FAILED_JOBS, "Number of backfill jobs marked as failed");
    describe_counter!(BACKFILL_JOBS_COMPLETED_TOTAL, "Total backfill jobs successfully completed");
    describe_counter!(BACKFILL_JOBS_FAILED_TOTAL, "Total backfill jobs that failed");
    describe_counter!(BACKFILL_EVENTS_INDEXED_TOTAL, "Total events indexed during backfill");

    gauge!(BACKFILL_PENDING_JOBS).set(0.0);
    gauge!(BACKFILL_OLDEST_PENDING_AGE_SECONDS).set(0.0);
    gauge!(BACKFILL_STALE_RUNNING_JOBS).set(0.0);
    gauge!(BACKFILL_FAILED_JOBS).set(0.0);
    counter!(BACKFILL_JOBS_COMPLETED_TOTAL).absolute(0);
    counter!(BACKFILL_JOBS_FAILED_TOTAL).absolute(0);
    counter!(BACKFILL_EVENTS_INDEXED_TOTAL).absolute(0);

    Ok(())
}

pub fn set_queue_metrics(pending: i64, oldest_age_secs: f64, stale_running: i64, failed: i64) {
    gauge!(BACKFILL_PENDING_JOBS).set(pending as f64);
    gauge!(BACKFILL_OLDEST_PENDING_AGE_SECONDS).set(oldest_age_secs);
    gauge!(BACKFILL_STALE_RUNNING_JOBS).set(stale_running as f64);
    gauge!(BACKFILL_FAILED_JOBS).set(failed as f64);
}

pub fn record_job_completed() {
    counter!(BACKFILL_JOBS_COMPLETED_TOTAL).increment(1);
}

pub fn record_job_failed() {
    counter!(BACKFILL_JOBS_FAILED_TOTAL).increment(1);
}

pub fn record_events_indexed(count: u64) {
    counter!(BACKFILL_EVENTS_INDEXED_TOTAL).increment(count);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn set_queue_metrics_does_not_panic() {
        set_queue_metrics(5, 120.5, 1, 2);
        record_job_completed();
        record_job_failed();
        record_events_indexed(42);
    }
}
