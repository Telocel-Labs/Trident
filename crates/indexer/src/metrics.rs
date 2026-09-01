use lazy_static::lazy_static;
use prometheus::{Counter, register_counter};

lazy_static! {
    pub static ref RETENTION_JOB_SUCCESS: Counter = register_counter!("trident_retention_job_success_total", "Total successful retention runs").unwrap();
    pub static ref RETENTION_JOB_FAILURE: Counter = register_counter!("trident_retention_job_failure_total", "Total failed retention runs").unwrap();
}