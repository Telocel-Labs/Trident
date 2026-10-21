//! Shared poll-loop liveness heartbeat (issue #764).
//!
//! The Prometheus gauge recorded by `metrics::record_heartbeat` is scraped
//! externally and is not something the in-process health server can cheaply
//! read back. This module keeps a second, process-local copy of the same
//! heartbeat in an `AtomicU64` (Unix seconds) so `/readyz` can fail when the
//! poll loop has stopped making progress, without round-tripping through
//! Prometheus.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

/// Handle shared between the poll loop (writer) and the health server
/// (reader). Cheap to clone — just an `Arc` around one atomic.
#[derive(Clone)]
pub struct Heartbeat(Arc<AtomicU64>);

impl Heartbeat {
    /// A fresh heartbeat stamped to "now", so a health check performed before
    /// the poll loop's first iteration does not spuriously report staleness.
    pub fn new() -> Self {
        let beat = Self(Arc::new(AtomicU64::new(0)));
        beat.record();
        beat
    }

    /// Stamp the heartbeat to the current Unix time. Called once per poll
    /// loop iteration, success or failure, mirroring `metrics::record_heartbeat`.
    pub fn record(&self) {
        let secs = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        self.0.store(secs, Ordering::Relaxed);
    }

    /// Seconds since the last `record()` call, using the current time. Never
    /// negative: a clock that moved backwards clamps to 0 rather than
    /// underflowing.
    pub fn age_secs(&self) -> u64 {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        let last = self.0.load(Ordering::Relaxed);
        now.saturating_sub(last)
    }

    /// Backdate the heartbeat by `age_secs_ago` seconds, for deterministic
    /// staleness tests elsewhere in this crate (`health::tests`) — avoids
    /// flaking on `age_secs`'s 1-second resolution via a real sleep.
    #[cfg(test)]
    pub fn set_for_test(&self, age_secs_ago: u64) {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        self.0
            .store(now.saturating_sub(age_secs_ago), Ordering::Relaxed);
    }
}

impl Default for Heartbeat {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fresh_heartbeat_has_zero_age() {
        let hb = Heartbeat::new();
        assert_eq!(hb.age_secs(), 0);
    }

    #[test]
    fn record_resets_age_to_zero() {
        let hb = Heartbeat::new();
        hb.set_for_test(300);
        assert!(hb.age_secs() > 0);
        hb.record();
        assert_eq!(hb.age_secs(), 0);
    }

    #[test]
    fn clones_share_the_same_underlying_timestamp() {
        let hb = Heartbeat::new();
        let clone = hb.clone();
        clone.set_for_test(300);
        assert!(
            hb.age_secs() > 0,
            "writes through a clone must be visible through the original"
        );
    }
}
