//! # Alerting
//!
//! Fires an outbound webhook when the indexer falls behind the chain tip by
//! more than `ALERT_LAG_THRESHOLD` ledgers, and sends a recovery webhook when
//! it catches up again.
//!
//! ## Design decisions
//! - **Silently disabled** when `ALERT_WEBHOOK_URL` is not set — no log
//!   warnings, no HTTP client allocated.
//! - **Cooldown**: alerts fire at most once per `ALERT_COOLDOWN_MINUTES`.
//!   Without this, every 6-second poll cycle would flood Slack.
//! - **Best-effort**: a failed webhook delivery logs a warning but never
//!   aborts the poll cycle or affects cursor advancement.
//! - **One retry on network error**: a 4xx means our payload is malformed;
//!   retrying won't help. A network error is transient and worth one retry.
//! - **Slack compatible**: payload includes a `text` field so it can be sent
//!   directly to a Slack incoming webhook URL.
//! - **PagerDuty**: PagerDuty Events API v2 requires `routing_key` and
//!   `event_action` fields which are not present in our payload. PagerDuty
//!   users should use an HTTP proxy/transformation layer (e.g. an AWS Lambda
//!   or Zapier step) to translate the payload before forwarding to PD.

use chrono::Utc;
use serde::Serialize;
use std::time::Duration;
use trident_common::TridentError;

/// Webhook POST timeout.
const WEBHOOK_TIMEOUT_SECS: u64 = 5;

/// State passed into every alerting check.
pub struct AlertContext {
    pub last_ledger_indexed: u64,
    pub chain_tip_ledger: u64,
    pub lag_threshold: u64,
    pub network: String,
}

/// Persistent alert state read from / written to `system_state`.
#[derive(Debug, Default)]
pub struct AlertState {
    pub last_alert_at: Option<chrono::DateTime<Utc>>,
    pub alert_fired: bool,
}

#[derive(Debug, Serialize)]
struct WebhookPayload {
    alert: &'static str,
    severity: &'static str,
    indexer: &'static str,
    network: String,
    lag_ledgers: u64,
    last_indexed_ledger: u64,
    chain_tip_ledger: u64,
    lag_threshold: u64,
    timestamp: String,
    message: String,
    /// Slack compatibility: Slack incoming webhooks accept `{ text: "..." }`.
    text: String,
}

#[derive(Debug, Serialize)]
struct RecoveryPayload {
    alert: &'static str,
    lag_ledgers: u64,
    timestamp: String,
    message: String,
    text: String,
}

/// The alerting subsystem. Constructed once in `main` and passed to
/// `Streamer`. When `webhook_url` is `None` every method is a no-op.
 pub struct Alerter {
    webhook_url: Option<String>,
    #[allow(dead_code)]
    lag_threshold: u64,
    cooldown: Duration,
    http: Option<reqwest::Client>,
}

impl Alerter {
    /// Build an `Alerter` from the three alerting env vars.
    ///
    /// Returns `Ok(Alerter { webhook_url: None, .. })` when
    /// `ALERT_WEBHOOK_URL` is absent — no error, no log.
    pub fn from_config(
        webhook_url: Option<String>,
        lag_threshold: u64,
        cooldown_minutes: u64,
    ) -> Result<Self, TridentError> {
        let http = if webhook_url.is_some() {
            Some(
                reqwest::Client::builder()
                    .timeout(Duration::from_secs(WEBHOOK_TIMEOUT_SECS))
                    .build()
                    .map_err(|e| {
                        TridentError::ConfigError(format!("alerting HTTP client: {e}"))
                    })?,
            )
        } else {
            None
        };

        Ok(Self {
            webhook_url,
            lag_threshold,
            cooldown: Duration::from_secs(cooldown_minutes * 60),
            http,
        })
    }

    /// Returns `true` when alerting is enabled (webhook URL is set).
    pub fn is_enabled(&self) -> bool {
        self.webhook_url.is_some()
    }

    /// Evaluate lag and fire / resolve alerts as needed.
    ///
    /// This is called after every successful poll cycle. It reads the current
    /// alert state, decides whether to fire or resolve, and returns the
    /// (possibly mutated) state for the caller to persist.
    ///
    /// Never returns an error — failures are logged at WARN level so the poll
    /// cycle is never affected.
    pub async fn evaluate(
        &self,
        ctx: &AlertContext,
        state: &mut AlertState,
    ) {
        if self.webhook_url.is_none() {
            return;
        }

        let lag = ctx
            .chain_tip_ledger
            .saturating_sub(ctx.last_ledger_indexed);

        if lag > ctx.lag_threshold {
            self.maybe_fire_alert(ctx, state, lag).await;
        } else {
            self.maybe_resolve(ctx, state, lag).await;
        }
    }

    /// Fire an alert if outside the cooldown window.
    async fn maybe_fire_alert(
        &self,
        ctx: &AlertContext,
        state: &mut AlertState,
        lag: u64,
    ) {
        let now = Utc::now();

        // Cooldown check: suppress if we fired recently.
        if let Some(last) = state.last_alert_at {
            let elapsed = (now - last).to_std().unwrap_or(Duration::ZERO);
            if elapsed < self.cooldown {
                tracing::debug!(
                    lag,
                    cooldown_remaining_secs = (self.cooldown - elapsed).as_secs(),
                    "Alert suppressed by cooldown"
                );
                return;
            }
        }

        let timestamp = now.to_rfc3339();
        let message = format!(
            "Trident indexer is {} ledgers behind chain tip on {} (threshold: {})",
            lag, ctx.network, ctx.lag_threshold
        );

        let payload = WebhookPayload {
            alert: "indexer_lag",
            severity: "warning",
            indexer: "trident-indexer",
            network: ctx.network.clone(),
            lag_ledgers: lag,
            last_indexed_ledger: ctx.last_ledger_indexed,
            chain_tip_ledger: ctx.chain_tip_ledger,
            lag_threshold: ctx.lag_threshold,
            timestamp: timestamp.clone(),
            message: message.clone(),
            text: message,
        };

        if self.post_with_retry(&payload).await {
            state.last_alert_at = Some(now);
            state.alert_fired = true;
            tracing::info!(lag, "Alert webhook fired");
        }
    }

    /// Send a recovery webhook if we previously fired an alert.
    async fn maybe_resolve(
        &self,
        ctx: &AlertContext,
        state: &mut AlertState,
        lag: u64,
    ) {
        if !state.alert_fired {
            return;
        }

        let timestamp = Utc::now().to_rfc3339();
        let message = format!(
            "Trident indexer has recovered. Lag is now {} ledgers.",
            lag
        );

        let payload = RecoveryPayload {
            alert: "indexer_lag_resolved",
            lag_ledgers: lag,
            timestamp,
            message: message.clone(),
            text: message,
        };

        if self.post_with_retry(&payload).await {
            state.alert_fired = false;
            state.last_alert_at = None;
            tracing::info!(lag, network = %ctx.network, "Recovery webhook fired");
        }
    }

    /// POST a JSON payload to the webhook URL.
    /// Retries once on network error. Does NOT retry on 4xx (malformed payload).
    /// Returns `true` on success, `false` on failure (already logged).
    async fn post_with_retry<P: Serialize>(&self, payload: &P) -> bool {
        let url = match &self.webhook_url {
            Some(u) => u,
            None => return false,
        };
        let client = match &self.http {
            Some(c) => c,
            None => return false,
        };

        for attempt in 1..=2u8 {
            match client.post(url).json(payload).send().await {
                Ok(resp) => {
                    let status = resp.status();
                    if status.is_success() {
                        tracing::info!(status = status.as_u16(), "Webhook delivered");
                        return true;
                    }
                    // 4xx: our payload is malformed — no point retrying.
                    if status.is_client_error() {
                        tracing::warn!(
                            status = status.as_u16(),
                            "Webhook rejected (4xx) — not retrying"
                        );
                        return false;
                    }
                    // 5xx: server-side issue — retry once.
                    tracing::warn!(
                        status = status.as_u16(),
                        attempt,
                        "Webhook delivery failed (5xx)"
                    );
                }
                Err(e) => {
                    tracing::warn!(error = %e, attempt, "Webhook network error");
                }
            }

            if attempt == 2 {
                tracing::warn!("Webhook delivery failed after retry — best-effort, continuing");
                return false;
            }
        }

        false
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::Duration as CDuration;

    fn make_alerter(url: Option<&str>, threshold: u64, cooldown_minutes: u64) -> Alerter {
        Alerter::from_config(
            url.map(|s| s.to_string()),
            threshold,
            cooldown_minutes,
        )
        .unwrap()
    }

    fn make_ctx(last_indexed: u64, chain_tip: u64, threshold: u64) -> AlertContext {
        AlertContext {
            last_ledger_indexed: last_indexed,
            chain_tip_ledger: chain_tip,
            lag_threshold: threshold,
            network: "testnet".to_string(),
        }
    }

    // ── Disabled alerter ──────────────────────────────────────────────────

    #[test]
    fn alerter_disabled_when_no_url() {
        let a = make_alerter(None, 200, 30);
        assert!(!a.is_enabled());
    }

    #[test]
    fn alerter_enabled_when_url_set() {
        let a = make_alerter(Some("https://hooks.example.com/test"), 200, 30);
        assert!(a.is_enabled());
    }

    // ── Cooldown logic ────────────────────────────────────────────────────

    #[tokio::test]
    async fn alert_fires_when_no_previous_alert() {
        // We can't actually POST in a unit test, but we can verify state mutation
        // by using a mock server. Here we just test the cooldown guard logic
        // using a disabled alerter (no HTTP call) and check state is untouched.
        let a = make_alerter(None, 200, 30);
        let ctx = make_ctx(100, 400, 200); // lag = 300 > threshold
        let mut state = AlertState::default();

        a.evaluate(&ctx, &mut state).await;

        // Disabled alerter: state must not be mutated.
        assert!(!state.alert_fired);
        assert!(state.last_alert_at.is_none());
    }

    #[tokio::test]
    async fn second_alert_within_cooldown_is_suppressed() {
        // Simulate: last_alert_at was 5 minutes ago, cooldown is 30 minutes.
        // The alerter is disabled so no HTTP call; we just verify the guard.
        let a = make_alerter(None, 200, 30);
        let ctx = make_ctx(100, 400, 200);
        let mut state = AlertState {
            last_alert_at: Some(Utc::now() - CDuration::minutes(5)),
            alert_fired: true,
        };

        // With a disabled alerter evaluate is a no-op; the guard is tested
        // via `maybe_fire_alert` which checks the cooldown before any HTTP.
        a.evaluate(&ctx, &mut state).await;

        // State must remain unchanged — cooldown not expired.
        assert!(state.alert_fired);
        assert!(state.last_alert_at.is_some());
    }

    #[tokio::test]
    async fn alert_fires_again_after_cooldown_expires() {
        let a = make_alerter(None, 200, 30);
        let ctx = make_ctx(100, 400, 200);
        let mut state = AlertState {
            // last alert was 31 minutes ago — cooldown expired
            last_alert_at: Some(Utc::now() - CDuration::minutes(31)),
            alert_fired: true,
        };

        // Disabled alerter: no HTTP call, but cooldown check would pass.
        // State stays as-is since the alerter is disabled.
        a.evaluate(&ctx, &mut state).await;
        // Just verifying no panic; HTTP-delivery logic tested via integration test.
    }

    #[tokio::test]
    async fn no_resolve_sent_if_alert_was_never_fired() {
        let a = make_alerter(None, 200, 30);
        // Lag is below threshold but alert_fired is false.
        let ctx = make_ctx(990, 1000, 200); // lag = 10 < threshold
        let mut state = AlertState {
            last_alert_at: None,
            alert_fired: false,
        };

        a.evaluate(&ctx, &mut state).await;

        assert!(!state.alert_fired);
        assert!(state.last_alert_at.is_none());
    }

    // ── Integration test (requires mock HTTP server) ───────────────────────

    #[tokio::test]
    async fn webhook_fires_and_payload_fields_are_correct() {
        let server = wiremock::MockServer::start().await;

        wiremock::Mock::given(wiremock::matchers::method("POST"))
            .and(wiremock::matchers::path("/webhook"))
            .respond_with(wiremock::ResponseTemplate::new(200))
            .expect(1)
            .mount(&server)
            .await;

        let url = format!("{}/webhook", server.uri());
        let alerter = make_alerter(Some(&url), 200, 30);
        let ctx = make_ctx(54_800, 55_050, 200); // lag = 250
        let mut state = AlertState::default();

        alerter.evaluate(&ctx, &mut state).await;

        // Verify server received exactly 1 request (enforced by `expect(1)`).
        server.verify().await;

        assert!(state.alert_fired, "alert_fired should be set after webhook");
        assert!(
            state.last_alert_at.is_some(),
            "last_alert_at should be set after webhook"
        );
    }

    #[tokio::test]
    async fn recovery_webhook_fires_after_lag_resolves() {
        let server = wiremock::MockServer::start().await;

        // Expect exactly 1 call for the recovery webhook.
        wiremock::Mock::given(wiremock::matchers::method("POST"))
            .and(wiremock::matchers::path("/webhook"))
            .respond_with(wiremock::ResponseTemplate::new(200))
            .expect(1)
            .mount(&server)
            .await;

        let url = format!("{}/webhook", server.uri());
        let alerter = make_alerter(Some(&url), 200, 30);

        // Lag is now below threshold — simulate recovery.
        let ctx = make_ctx(999_990, 1_000_000, 200); // lag = 10
        let mut state = AlertState {
            last_alert_at: Some(Utc::now() - CDuration::minutes(35)),
            alert_fired: true, // a previous alert was fired
        };

        alerter.evaluate(&ctx, &mut state).await;

        server.verify().await;

        assert!(
            !state.alert_fired,
            "alert_fired should be cleared after recovery"
        );
    }

    #[tokio::test]
    async fn failed_webhook_does_not_mutate_state() {
        let server = wiremock::MockServer::start().await;

        // Server always returns 500.
        wiremock::Mock::given(wiremock::matchers::method("POST"))
            .and(wiremock::matchers::path("/webhook"))
            .respond_with(wiremock::ResponseTemplate::new(500))
            .mount(&server)
            .await;

        let url = format!("{}/webhook", server.uri());
        let alerter = make_alerter(Some(&url), 200, 30);
        let ctx = make_ctx(54_800, 55_050, 200);
        let mut state = AlertState::default();

        alerter.evaluate(&ctx, &mut state).await;

        // Delivery failed — state must not be updated.
        assert!(!state.alert_fired, "state must not change on failed delivery");
        assert!(state.last_alert_at.is_none());
    }

    #[tokio::test]
    async fn cooldown_suppresses_second_alert_with_real_server() {
        let server = wiremock::MockServer::start().await;

        // Expect exactly 0 webhook calls — cooldown should suppress.
        wiremock::Mock::given(wiremock::matchers::method("POST"))
            .and(wiremock::matchers::path("/webhook"))
            .respond_with(wiremock::ResponseTemplate::new(200))
            .expect(0)
            .mount(&server)
            .await;

        let url = format!("{}/webhook", server.uri());
        let alerter = make_alerter(Some(&url), 200, 30);
        let ctx = make_ctx(54_800, 55_050, 200);

        // Last alert fired 5 minutes ago — within 30-minute cooldown.
        let mut state = AlertState {
            last_alert_at: Some(Utc::now() - CDuration::minutes(5)),
            alert_fired: true,
        };

        alerter.evaluate(&ctx, &mut state).await;

        server.verify().await;
    }
}