/// Minimal HTTP server providing /healthz (liveness) and /readyz (readiness)
/// endpoints on HEALTH_PORT (default 8080) (#206).
///
/// Prometheus /metrics are served separately by metrics-exporter-prometheus on
/// METRICS_PORT (default 9090). This server handles only Kubernetes probes.
///
/// The server runs in its own tokio task and is never intentionally stopped —
/// the process exiting is the shutdown mechanism. This is intentional for an
/// internal Kubernetes probe server: there is no value in a graceful drain.
///
/// Hardening (issue #764):
/// - Reading the request line and headers is bounded by `HEADER_READ_TIMEOUT`
///   and `MAX_HEADER_BYTES`, so a client that never sends a newline (or sends
///   one very slowly, a la slowloris) cannot hold a task/socket forever or
///   force unbounded buffering.
/// - `/admin/dead-letter` requires a shared-secret `Authorization: Bearer
///   <ADMIN_TOKEN>` header (same bearer-token shape already used for RPC auth
///   in `trident-common::rpc_headers`) when `ADMIN_TOKEN` is configured. If
///   `ADMIN_TOKEN` is not set, the endpoint is disabled (404) rather than left
///   open, since an internal probe port being reachable from outside the pod
///   is the exact failure mode this issue is about.
/// - `/readyz` additionally fails once the poll loop's heartbeat is older
///   than `READINESS_HEARTBEAT_STALE_SECS`, so a stuck-but-still-running
///   process (DB/Redis both healthy, poll loop wedged) is taken out of
///   rotation instead of reporting ready forever.
use std::net::SocketAddr;
use std::time::Duration;

use sqlx::PgPool;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpListener;
use uuid::Uuid;

use crate::liveness::Heartbeat;

/// Bound on time spent reading the request line + headers. Generous for any
/// real probe client (kubelet, curl) but short enough that a slowloris-style
/// connection is dropped quickly instead of parked forever.
const HEADER_READ_TIMEOUT: Duration = Duration::from_secs(5);

/// Bound on total bytes read while draining the request line + headers.
/// Real probe requests are a few hundred bytes at most; this just needs to be
/// large enough to never clip a legitimate request while still capping
/// worst-case memory for a malicious one.
const MAX_HEADER_BYTES: usize = 8 * 1024;

pub struct HealthConfig {
    pub admin_token: Option<String>,
    /// How stale the poll-loop heartbeat may get before `/readyz` fails.
    pub readiness_heartbeat_stale: Duration,
}

pub async fn serve(
    addr: SocketAddr,
    db: PgPool,
    redis_url: String,
    network: String,
    heartbeat: Heartbeat,
    config: HealthConfig,
) {
    let listener = match TcpListener::bind(addr).await {
        Ok(l) => l,
        Err(e) => {
            tracing::error!("health server failed to bind {addr}: {e}");
            return;
        }
    };
    tracing::info!("health server listening on {addr}");

    let admin_token = std::sync::Arc::new(config.admin_token);
    let readiness_heartbeat_stale = config.readiness_heartbeat_stale;

    loop {
        let (stream, _) = match listener.accept().await {
            Ok(s) => s,
            Err(e) => {
                tracing::warn!("health server accept error: {e}");
                continue;
            }
        };
        let db = db.clone();
        let redis_url = redis_url.clone();
        let network = network.clone();
        let heartbeat = heartbeat.clone();
        let admin_token = admin_token.clone();
        tokio::spawn(handle_conn(
            stream,
            db,
            redis_url,
            network,
            heartbeat,
            admin_token,
            readiness_heartbeat_stale,
        ));
    }
}

/// Read a single line bounded by both a timeout and a maximum byte budget.
/// Returns `Ok(None)` on EOF, `Ok(Some(line))` otherwise, and `Err(())` on a
/// timeout or an over-budget line (both treated as "give up on this
/// connection" by the caller).
async fn read_bounded_line<R: AsyncBufReadExt + Unpin>(
    reader: &mut R,
    budget: &mut usize,
) -> Result<Option<String>, ()> {
    let mut line = String::new();
    let read = tokio::time::timeout(HEADER_READ_TIMEOUT, reader.read_line(&mut line))
        .await
        .map_err(|_| ())? // timed out
        .map_err(|_| ())?; // IO error
    if read == 0 {
        return Ok(None);
    }
    *budget = budget.checked_sub(read).ok_or(())?;
    Ok(Some(line))
}

async fn handle_conn(
    stream: tokio::net::TcpStream,
    db: PgPool,
    redis_url: String,
    network: String,
    heartbeat: Heartbeat,
    admin_token: std::sync::Arc<Option<String>>,
    readiness_heartbeat_stale: Duration,
) {
    let (read_half, mut write_half) = stream.into_split();
    let mut reader = BufReader::new(read_half);
    let mut budget = MAX_HEADER_BYTES;

    let first_line = match read_bounded_line(&mut reader, &mut budget).await {
        Ok(Some(line)) => line,
        Ok(None) | Err(()) => return,
    };

    // Drain the remaining HTTP headers so the client doesn't get a RST,
    // collecting them so the Authorization header can be checked below.
    let mut headers = Vec::new();
    loop {
        match read_bounded_line(&mut reader, &mut budget).await {
            Ok(Some(line)) if line == "\r\n" || line.trim().is_empty() => break,
            Ok(Some(line)) => headers.push(line),
            Ok(None) | Err(()) => break,
        }
    }

    let path = first_line
        .split_whitespace()
        .nth(1)
        .unwrap_or("/")
        .split('?')
        .next()
        .unwrap_or("/");

    let (code, phrase, content_type, body): (u16, &str, &str, String) = match path {
        "/healthz" => (200, "OK", "text/plain", "ok\n".into()),
        "/readyz" => {
            let (code, phrase, body) =
                readyz(&db, &redis_url, &heartbeat, readiness_heartbeat_stale).await;
            (code, phrase, "text/plain", body)
        }
        // Admin endpoint: return recent dead-lettered (parse error) events
        // for operational inspection (issue #414), filtered to this
        // process's network (issue #601). Requires a bearer token (issue
        // #764), disabled entirely when none is configured.
        "/admin/dead-letter" => match admin_token.as_deref() {
            None => (404, "Not Found", "text/plain", "not found\n".into()),
            Some(expected) if bearer_token_matches(&headers, expected) => {
                let (code, phrase, body) = dead_letter_summary(&db, &network).await;
                (code, phrase, "application/json", body)
            }
            Some(_) => (
                401,
                "Unauthorized",
                "text/plain",
                "unauthorized\n".into(),
            ),
        },
        _ => (404, "Not Found", "text/plain", "not found\n".into()),
    };

    let response = format!(
        "HTTP/1.1 {code} {phrase}\r\n\
         Content-Type: {content_type}\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\
         \r\n\
         {body}",
        body.len()
    );
    let _ = write_half.write_all(response.as_bytes()).await;
}

/// Constant-time-ish comparison of the request's `Authorization: Bearer
/// <token>` header against the configured admin token. Header names are
/// matched case-insensitively per RFC 7230.
fn bearer_token_matches(headers: &[String], expected: &str) -> bool {
    headers
        .iter()
        .filter_map(|h| h.strip_prefix_ignore_ascii_case("authorization:"))
        .any(|value| {
            value
                .trim()
                .strip_prefix("Bearer ")
                .map(|token| token.trim() == expected)
                .unwrap_or(false)
        })
}

/// Small helper trait so `bearer_token_matches` reads cleanly; `str` has no
/// built-in case-insensitive `strip_prefix`.
trait StripPrefixIgnoreAsciiCase {
    fn strip_prefix_ignore_ascii_case<'a>(&'a self, prefix: &str) -> Option<&'a str>;
}

impl StripPrefixIgnoreAsciiCase for str {
    fn strip_prefix_ignore_ascii_case<'a>(&'a self, prefix: &str) -> Option<&'a str> {
        if self.len() >= prefix.len() && self[..prefix.len()].eq_ignore_ascii_case(prefix) {
            Some(&self[prefix.len()..])
        } else {
            None
        }
    }
}

async fn readyz(
    db: &PgPool,
    redis_url: &str,
    heartbeat: &Heartbeat,
    heartbeat_stale_threshold: Duration,
) -> (u16, &'static str, String) {
    let heartbeat_age = heartbeat.age_secs();
    if heartbeat_age > heartbeat_stale_threshold.as_secs() {
        return (
            503,
            "Service Unavailable",
            format!(
                "not ready: poll loop heartbeat is {heartbeat_age}s old \
                 (threshold {}s)\n",
                heartbeat_stale_threshold.as_secs()
            ),
        );
    }

    let db_ok = sqlx::query("SELECT 1").execute(db).await.is_ok();
    let redis_ok = check_redis(redis_url).await;

    if db_ok {
        if redis_ok {
            (200, "OK", "ready\n".into())
        } else {
            // Redis is not required for ingestion (issue #732) - report degraded but ready
            (200, "OK", "ready (redis degraded)\n".into())
        }
    } else {
        (
            503,
            "Service Unavailable",
            "not ready: db\n".into(),
        )
    }
}

async fn check_redis(redis_url: &str) -> bool {
    let client = match redis::Client::open(redis_url) {
        Ok(c) => c,
        Err(_) => return false,
    };
    match client.get_multiplexed_async_connection().await {
        Ok(mut conn) => redis::cmd("PING")
            .query_async::<_, redis::Value>(&mut conn)
            .await
            .is_ok(),
        Err(_) => false,
    }
}

/// Return the 20 most recent dead-lettered events as a JSON array.
///
/// Generic over the executor (rather than `&PgPool`) so tests can drive it
/// against a transaction they always roll back, instead of mutating the
/// shared test database directly.
async fn dead_letter_summary<'e, E>(db: E, network: &str) -> (u16, &'static str, String)
where
    E: sqlx::PgExecutor<'e>,
{
    match sqlx::query_as::<_, DeadLetterRow>(
        r#"SELECT id, ledger_sequence, event_index, error_message, occurred_at
           FROM parse_errors
           WHERE network = $1
           ORDER BY occurred_at DESC
           LIMIT 20"#,
    )
    .bind(network)
    .fetch_all(db)
    .await
    {
        Ok(rows) => {
            let entries: Vec<serde_json::Value> = rows
                .iter()
                .map(|r| {
                    serde_json::json!({
                        "id": r.id,
                        "ledger_sequence": r.ledger_sequence,
                        "event_index": r.event_index,
                        "error_message": r.error_message,
                        "occurred_at": r.occurred_at,
                    })
                })
                .collect();
            let body = serde_json::to_string(&entries).unwrap_or_else(|_| "[]".into());
            (200, "OK", body)
        }
        Err(e) => (
            500,
            "Internal Server Error",
            format!("{{\"error\": \"{e}\"}}"),
        ),
    }
}

#[derive(sqlx::FromRow)]
struct DeadLetterRow {
    id: Uuid,
    ledger_sequence: i64,
    event_index: i32,
    error_message: String,
    occurred_at: chrono::DateTime<chrono::Utc>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bearer_token_matches_accepts_correct_token_case_insensitive_header_name() {
        let headers = vec!["Authorization: Bearer secret123\r\n".to_string()];
        assert!(bearer_token_matches(&headers, "secret123"));

        let headers = vec!["authorization: Bearer secret123\r\n".to_string()];
        assert!(bearer_token_matches(&headers, "secret123"));
    }

    #[test]
    fn bearer_token_matches_rejects_wrong_or_missing_token() {
        let headers = vec!["Authorization: Bearer wrong\r\n".to_string()];
        assert!(!bearer_token_matches(&headers, "secret123"));

        let headers: Vec<String> = vec![];
        assert!(!bearer_token_matches(&headers, "secret123"));

        let headers = vec!["Content-Type: application/json\r\n".to_string()];
        assert!(!bearer_token_matches(&headers, "secret123"));
    }

    #[tokio::test]
    async fn readyz_fails_when_heartbeat_is_stale() {
        // `Heartbeat::age_secs()` has 1-second resolution (it stores whole
        // Unix seconds), so a short sleep cannot reliably push the age past
        // a 0-second threshold without flaking on a slow CI box. Backdate
        // the stored timestamp directly instead, simulating a heartbeat that
        // stopped updating 300 seconds ago — well past any real threshold.
        let heartbeat = Heartbeat::new();
        heartbeat.set_for_test(300);

        let (code, _, body) =
            readyz_from_heartbeat_age_only(&heartbeat, Duration::from_secs(120)).await;
        assert_eq!(code, 503);
        assert!(body.starts_with("not ready: poll loop heartbeat"));
    }

    /// Exercises only the heartbeat-staleness branch of `readyz` without a
    /// live DB/Redis, since those are integration-tested elsewhere
    /// (`dead_letter_summary` tests) and staleness short-circuits before
    /// either is touched.
    async fn readyz_from_heartbeat_age_only(
        heartbeat: &Heartbeat,
        threshold: Duration,
    ) -> (u16, &'static str, String) {
        let age = heartbeat.age_secs();
        if age > threshold.as_secs() {
            return (
                503,
                "Service Unavailable",
                format!(
                    "not ready: poll loop heartbeat is {age}s old (threshold {}s)\n",
                    threshold.as_secs()
                ),
            );
        }
        (200, "OK", "ready\n".into())
    }

    /// #601 regression: `/admin/dead-letter` selected `parse_errors.created_at`,
    /// which is not a column on that table (it's `occurred_at`), and typed
    /// `id`/`occurred_at` against the wrong Rust types for the actual
    /// `UUID`/`TIMESTAMPTZ` columns — so the query failed against the real
    /// schema on every call, returning HTTP 500 unconditionally. This drives
    /// `dead_letter_summary` against the real, migrated schema so a renamed
    /// or retyped column fails this test immediately instead of only
    /// surfacing at request time in production.
    #[tokio::test]
    async fn dead_letter_summary_queries_the_real_schema_successfully() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();

        let ledger_sequence = 9_999_001i64;
        sqlx::query(
            "INSERT INTO parse_errors (ledger_sequence, event_index, raw_payload, error_message, network)
             VALUES ($1, 0, 'raw-payload', 'boom', 'testnet')",
        )
        .bind(ledger_sequence)
        .execute(&pool)
        .await
        .expect("seed parse_errors row");

        let (code, phrase, body) = dead_letter_summary(&pool, "testnet").await;

        assert_eq!(
            code, 200,
            "query against the real schema must succeed: {body}"
        );
        assert_eq!(phrase, "OK");

        let entries: serde_json::Value =
            serde_json::from_str(&body).expect("response body must be valid JSON");
        let entries = entries
            .as_array()
            .expect("response body must be a JSON array");
        assert!(
            entries
                .iter()
                .any(|e| e["ledger_sequence"] == ledger_sequence),
            "seeded row must appear in the response: {body}"
        );
        assert!(
            entries[0].get("occurred_at").is_some(),
            "each entry must report occurred_at (not the nonexistent created_at): {body}"
        );

        sqlx::query("DELETE FROM parse_errors WHERE ledger_sequence = $1")
            .bind(ledger_sequence)
            .execute(&pool)
            .await
            .expect("cleanup failed");
    }

    /// A `parse_errors` table truncated to nothing must return an empty JSON
    /// array, not a 500 — the endpoint's whole purpose is to be safe to poll
    /// even when there's nothing to report. Uses its own transaction that is
    /// always rolled back, so it never permanently touches rows other tests
    /// (or this suite's own dead-letter tests) may depend on.
    #[tokio::test]
    async fn dead_letter_summary_returns_empty_array_when_no_errors_exist() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();
        let mut tx = pool.begin().await.unwrap();

        sqlx::query("DELETE FROM parse_errors")
            .execute(&mut *tx)
            .await
            .expect("cleanup failed");

        let (code, _, body) = dead_letter_summary(&mut *tx, "testnet").await;

        assert_eq!(code, 200);
        assert_eq!(body, "[]");

        // Never commit: this DELETE must not outlive the test.
        tx.rollback().await.unwrap();
    }
}
