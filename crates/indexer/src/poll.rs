//! Adaptive poll interval (issue #198).
//!
//! The streamer polls fast while far behind the chain tip and slows down once
//! caught up: at `lag == 0` it polls at the configured ceiling, at
//! `lag >= high_watermark` at the floor, and interpolates linearly in between.
//! A hysteresis deadband suppresses interval changes for small lag jitter so
//! the interval does not oscillate around a threshold.
//!
//! [`apply_degraded_backoff`] layers a second, independent signal on top: RPC
//! endpoint health. Lag alone says nothing about whether every configured RPC
//! endpoint is critically degraded — without this, a degraded-but-not-fully-down
//! provider set keeps getting hammered at the same request rate that likely
//! caused the degradation in the first place (plausibly provider-side rate
//! limiting).

use std::time::Duration;

/// Records the wall-clock time (seconds since the Unix epoch) of the most
/// recent *successful* poll, exported as
/// `trident_indexer_last_poll_timestamp_seconds`.
///
/// The indexer-stalled alert (issue #400) watches this metric for silence
/// rather than watching lag: if the indexer dies or the scrape target goes
/// away, lag stops updating instead of climbing, so a flat lag line is
/// indistinguishable from a healthy caught-up indexer. A heartbeat that only
/// advances on success makes "no successful poll for N intervals" observable.
///
/// A failed poll (RPC error, DB error) must **not** bump the heartbeat — that
/// is precisely what distinguishes "stalled" from "healthy but idle".
#[derive(Debug, Default)]
pub struct PollHeartbeat {
    last_success_secs: Option<u64>,
}

impl PollHeartbeat {
    pub fn new() -> Self {
        Self {
            last_success_secs: None,
        }
    }

    /// Record a successful poll at `now_secs`. Call this only after the poll
    /// has fully succeeded (RPC fetch *and* persistence); a poll that returns
    /// no new ledgers still counts as success and must bump the heartbeat.
    pub fn record_success(&mut self, now_secs: u64) {
        self.last_success_secs = Some(now_secs);
    }

    /// The last successful poll timestamp, or `None` if no poll has ever
    /// succeeded. Exported as the gauge value; `None` means the metric is
    /// absent, which the scrape-target-down alert keys on.
    pub fn last_success_secs(&self) -> Option<u64> {
        self.last_success_secs
    }
}

/// Multiplier applied to the adaptive interval when every configured RPC
/// endpoint is critically degraded (`RpcHealthScorer::all_degraded`).
/// Deliberately a flat multiplier rather than a separate set of
/// floor/ceiling/watermark knobs — degradation is binary (all endpoints
/// critically unhealthy, or not), so a single widening factor on top of
/// whatever the lag-based interval already is keeps this simple to reason
/// about and to tune.
const ALL_DEGRADED_BACKOFF_MULTIPLIER: u32 = 4;

/// Widen `interval` when every configured RPC endpoint is critically
/// degraded, so a degraded provider set is polled at a measurably reduced
/// rate instead of the same cadence as full health. A no-op when
/// `all_degraded` is `false`.
pub fn apply_degraded_backoff(interval: Duration, all_degraded: bool) -> Duration {
    if all_degraded {
        interval.saturating_mul(ALL_DEGRADED_BACKOFF_MULTIPLIER)
    } else {
        interval
    }
}

/// Bounds and watermarks controlling the lag → interval mapping.
#[derive(Debug, Clone, Copy)]
pub struct AdaptivePollConfig {
    /// Shortest interval, used when `lag >= high_watermark`.
    pub floor: Duration,
    /// Longest interval, used when `lag == 0`.
    pub ceiling: Duration,
    /// Lag at (or above) which the floor applies.
    pub high_watermark: u64,
    /// Minimum change in lag (ledgers) required to move the interval. Prevents
    /// oscillation when the lag hovers around a threshold.
    pub hysteresis_ledgers: u64,
}

/// Pure mapping from chain-tip lag to a poll interval, before hysteresis.
///
/// Guarantees: returns `ceiling` at `lag == 0`, `floor` at
/// `lag >= high_watermark`, and a linearly interpolated value in between. If
/// the configuration is degenerate (`ceiling <= floor` or `high_watermark == 0`)
/// the floor is returned.
pub fn target_interval(lag: u64, cfg: &AdaptivePollConfig) -> Duration {
    let floor_ms = cfg.floor.as_millis() as u64;
    let ceiling_ms = cfg.ceiling.as_millis() as u64;

    if ceiling_ms <= floor_ms || cfg.high_watermark == 0 {
        return Duration::from_millis(floor_ms);
    }
    if lag == 0 {
        return Duration::from_millis(ceiling_ms);
    }
    if lag >= cfg.high_watermark {
        return Duration::from_millis(floor_ms);
    }

    // Linear interpolation: ceiling at lag=0 down to floor at lag=high_watermark.
    let span = (ceiling_ms - floor_ms) as u128;
    let reduction = (span * lag as u128 / cfg.high_watermark as u128) as u64;
    Duration::from_millis(ceiling_ms - reduction)
}

/// Stateful adaptive poller: applies [`target_interval`] with a hysteresis
/// deadband so small lag fluctuations don't churn the interval.
#[derive(Debug)]
pub struct AdaptivePoll {
    cfg: AdaptivePollConfig,
    last_lag: Option<u64>,
    last_interval: Duration,
}

impl AdaptivePoll {
    pub fn new(cfg: AdaptivePollConfig) -> Self {
        // Start at the ceiling (assume caught up until proven otherwise).
        let last_interval = cfg.ceiling;
        Self {
            cfg,
            last_lag: None,
            last_interval,
        }
    }

    /// Compute the interval for the current `lag`, honouring the hysteresis
    /// deadband: if the lag hasn't moved by at least `hysteresis_ledgers` since
    /// the last applied value, the previous interval is kept. The floor and
    /// ceiling extremes always apply immediately so the streamer reacts
    /// promptly at the boundaries.
    pub fn next_interval(&mut self, lag: u64) -> Duration {
        let at_extreme = lag == 0 || lag >= self.cfg.high_watermark;
        if let Some(prev) = self.last_lag {
            let delta = lag.abs_diff(prev);
            if !at_extreme && delta < self.cfg.hysteresis_ledgers {
                return self.last_interval;
            }
        }
        let interval = target_interval(lag, &self.cfg);
        self.last_lag = Some(lag);
        self.last_interval = interval;
        interval
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn cfg() -> AdaptivePollConfig {
        AdaptivePollConfig {
            floor: Duration::from_millis(250),
            ceiling: Duration::from_millis(5000),
            high_watermark: 100,
            hysteresis_ledgers: 10,
        }
    }

    #[test]
    fn caught_up_uses_ceiling() {
        assert_eq!(target_interval(0, &cfg()), Duration::from_millis(5000));
    }

    #[test]
    fn at_or_above_high_watermark_uses_floor() {
        assert_eq!(target_interval(100, &cfg()), Duration::from_millis(250));
        assert_eq!(target_interval(5000, &cfg()), Duration::from_millis(250));
    }

    #[test]
    fn interpolates_between_bounds() {
        // Halfway (lag 50 of 100): reduction = 4750 * 50 / 100 = 2375 -> 2625ms.
        assert_eq!(target_interval(50, &cfg()), Duration::from_millis(2625));
        // Shorter interval as lag grows.
        assert!(target_interval(75, &cfg()) < target_interval(25, &cfg()));
    }

    #[test]
    fn interval_monotonically_decreases_with_lag() {
        let c = cfg();
        let mut prev = target_interval(0, &c);
        for lag in [1, 10, 25, 50, 75, 99, 100] {
            let cur = target_interval(lag, &c);
            assert!(cur <= prev, "interval should not grow as lag grows");
            prev = cur;
        }
    }

    #[test]
    fn degenerate_config_returns_floor() {
        let bad = AdaptivePollConfig {
            floor: Duration::from_millis(1000),
            ceiling: Duration::from_millis(500), // ceiling < floor
            high_watermark: 100,
            hysteresis_ledgers: 10,
        };
        assert_eq!(target_interval(0, &bad), Duration::from_millis(1000));
    }

    #[test]
    fn hysteresis_holds_interval_for_small_lag_jitter() {
        let mut ap = AdaptivePoll::new(cfg());
        let first = ap.next_interval(50); // establishes baseline
                                          // A jitter of < 10 ledgers must not change the interval.
        assert_eq!(ap.next_interval(53), first);
        assert_eq!(ap.next_interval(45), first);
    }

    #[test]
    fn hysteresis_yields_once_lag_moves_enough() {
        let mut ap = AdaptivePoll::new(cfg());
        let first = ap.next_interval(50);
        // A jump beyond the deadband recomputes.
        let moved = ap.next_interval(80);
        assert_ne!(moved, first);
        assert!(moved < first, "more lag -> shorter interval");
    }

    #[test]
    fn degraded_backoff_is_a_noop_when_healthy() {
        assert_eq!(
            apply_degraded_backoff(Duration::from_millis(250), false),
            Duration::from_millis(250)
        );
    }

    #[test]
    fn degraded_backoff_widens_interval_when_all_degraded() {
        let widened = apply_degraded_backoff(Duration::from_millis(250), true);
        assert!(
            widened > Duration::from_millis(250),
            "a fully degraded endpoint set must poll slower than full health"
        );
        assert_eq!(widened, Duration::from_millis(1000));
    }

    #[test]
    fn degraded_backoff_applies_on_top_of_whatever_interval_lag_already_chose() {
        // The widening is independent of where in the floor/ceiling range the
        // lag-based interval landed.
        let at_floor = apply_degraded_backoff(Duration::from_millis(250), true);
        let at_ceiling = apply_degraded_backoff(Duration::from_millis(5000), true);
        assert_eq!(at_floor, Duration::from_millis(1000));
        assert_eq!(at_ceiling, Duration::from_millis(20_000));
    }

    #[test]
    fn extremes_apply_immediately_despite_hysteresis() {
        let mut ap = AdaptivePoll::new(cfg());
        ap.next_interval(5); // small lag near ceiling
                             // Reaching the high watermark must snap to floor even within the deadband window.
        assert_eq!(ap.next_interval(100), Duration::from_millis(250));
        // Caught up snaps to ceiling immediately.
        assert_eq!(ap.next_interval(0), Duration::from_millis(5000));
    }

    #[test]
    fn heartbeat_starts_absent() {
        let hb = PollHeartbeat::new();
        assert_eq!(hb.last_success_secs(), None);
    }

    #[test]
    fn heartbeat_flat_line_still_bumps_on_success() {
        // A poll that returns no new ledgers (lag unchanged, flat line) is
        // still a successful poll and must advance the heartbeat.
        let mut hb = PollHeartbeat::new();
        hb.record_success(1_000);
        assert_eq!(hb.last_success_secs(), Some(1_000));
        // Next poll: no new ledgers, but it succeeded.
        hb.record_success(1_005);
        assert_eq!(hb.last_success_secs(), Some(1_005));
    }

    #[test]
    fn heartbeat_unchanged_on_failed_poll() {
        // A failed poll (RPC or DB error) must not bump the heartbeat: the
        // caller simply does not call `record_success`. The timestamp stays
        // frozen, which is what the silence alert fires on.
        let mut hb = PollHeartbeat::new();
        hb.record_success(1_000);
        // Failed poll at t=1_005: no `record_success` call.
        assert_eq!(hb.last_success_secs(), Some(1_000));
        // Still frozen after several consecutive failures.
        assert_eq!(hb.last_success_secs(), Some(1_000));
    }

    #[test]
    fn heartbeat_never_advances_if_all_polls_fail() {
        let hb = PollHeartbeat::new();
        // No successful poll ever: metric stays absent so the
        // scrape-target-down alert fires.
        assert_eq!(hb.last_success_secs(), None);
    }
}
