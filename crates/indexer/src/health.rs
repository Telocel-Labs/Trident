//! Minimal HTTP server providing /healthz (liveness) and /readyz (readiness)
//! endpoints on HEALTH_PORT (default 8080) (#206, #764).
//!
//! Prometheus /metrics are served separately by metrics-exporter-prometheus on
//! METRICS_PORT (default 9090). This server handles internal Kubernetes probes
//! and operational diagnostics.
//!
//! Slowloris and resource exhaustion protection (issue #764):
//! - Enforces bounded line and header length (max 4KB line, max 8KB total headers).
//! - Enforces a read timeout on HTTP headers (default 5s).
//! - Enforces a maximum concurrency ceiling via semaphores.
//! - Requires authentication (or localhost restriction) for `/admin/dead-letter`.
//! - Reuses a shared Redis connection for readiness checks.
//! - Validates poll-loop heartbeat freshness for `/healthz` and `/readyz`.

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::{Duration, Instant};

use sqlx::PgPool;
use tokio::io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::{Mutex, Semaphore};
use uuid::Uuid;

const MAX_LINE_LEN: usize = 4096;
const MAX_TOTAL_HEADERS_LEN: usize = 8192;

#[derive(Clone, Debug)]
pub struct HealthConfig {
    pub heartbeat_timeout_secs: u64,
    pub admin_api_key: Option<String>,
    pub max_concurrent_conns: usize,
    pub header_timeout: Duration,
}

impl Default for HealthConfig {
    fn default() -> Self {
        Self {
            heartbeat_timeout_secs: 120,
            admin_api_key: None,
            max_concurrent_conns: 64,
            header_timeout: Duration::from_secs(5),
        }
    }
}

pub struct SharedRedis {
    url: String,
    conn: Arc<Mutex<Option<redis::aio::MultiplexedConnection>>>,
}

impl SharedRedis {
    pub fn new(url: String, initial_conn: Option<redis::aio::MultiplexedConnection>) -> Self {
        Self {
            url,
            conn: Arc::new(Mutex::new(initial_conn)),
        }
    }

    pub async fn check_alive(&self) -> bool {
        let mut guard = self.conn.lock().await;
        if let Some(conn) = guard.as_mut() {
            if redis::cmd("PING")
                .query_async::<_, redis::Value>(conn)
                .await
                .is_ok()
            {
                return true;
            }
            // Invalidate stale connection
            *guard = None;
        }

        // Attempt single reconnect and cache
        if let Ok(client) = redis::Client::open(self.url.as_str()) {
            if let Ok(mut new_conn) = client.get_multiplexed_async_connection().await {
                if redis::cmd("PING")
                    .query_async::<_, redis::Value>(&mut new_conn)
                    .await
                    .is_ok()
                {
                    *guard = Some(new_conn);
                    return true;
                }
            }
        }
        false
    }
}

/// Backwards-compatible entrypoint.
pub async fn serve(addr: SocketAddr, db: PgPool, redis_url: String) {
    serve_with_config(addr, db, redis_url, None, HealthConfig::default()).await;
}

pub async fn serve_with_config(
    addr: SocketAddr,
    db: PgPool,
    redis_url: String,
    redis_conn: Option<redis::aio::MultiplexedConnection>,
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

    let shared_redis = Arc::new(SharedRedis::new(redis_url, redis_conn));
    let limiter = Arc::new(Semaphore::new(config.max_concurrent_conns));
    let startup_time = Instant::now();

    loop {
        let (stream, peer_addr) = match listener.accept().await {
            Ok(s) => s,
            Err(e) => {
                tracing::warn!("health server accept error: {e}");
                continue;
            }
        };

        let permit = match limiter.clone().try_acquire_owned() {
            Ok(p) => p,
            Err(_) => {
                tracing::warn!(
                    "health server reached connection capacity ({}); dropping connection from {peer_addr}",
                    config.max_concurrent_conns
                );
                continue;
            }
        };

        let db = db.clone();
        let redis = shared_redis.clone();
        let cfg = config.clone();

        tokio::spawn(async move {
            let _permit = permit;
            handle_conn(stream, peer_addr, db, redis, cfg, startup_time).await;
        });
    }
}

async fn read_bounded_line<R: AsyncBufReadExt + Unpin>(
    reader: &mut R,
    out: &mut String,
    max_len: usize,
) -> Result<bool, &'static str> {
    out.clear();
    let mut total_read = 0;
    while total_read < max_len {
        let mut byte = [0u8; 1];
        match reader.read(&mut byte).await {
            Ok(0) => return Ok(false), // EOF
            Ok(_) => {
                let c = byte[0] as char;
                out.push(c);
                total_read += 1;
                if c == '\n' {
                    return Ok(true);
                }
            }
            Err(_) => return Err("io_error"),
        }
    }
    Err("line_too_long")
}

async fn handle_conn(
    stream: TcpStream,
    peer_addr: SocketAddr,
    db: PgPool,
    redis: Arc<SharedRedis>,
    config: HealthConfig,
    startup_time: Instant,
) {
    let (read_half, mut write_half) = stream.into_split();
    let mut reader = BufReader::new(read_half);

    // Bounded header parsing with timeout (Slowloris protection — issue #764)
    let parse_result = tokio::time::timeout(config.header_timeout, async {
        let mut first_line = String::new();
        match read_bounded_line(&mut reader, &mut first_line, MAX_LINE_LEN).await {
            Ok(true) => {}
            Ok(false) => return Err("eof"),
            Err(e) => return Err(e),
        }

        let mut headers = Vec::new();
        let mut total_headers_len = first_line.len();

        loop {
            let mut line = String::new();
            match read_bounded_line(&mut reader, &mut line, MAX_LINE_LEN).await {
                Ok(true) => {
                    total_headers_len += line.len();
                    if total_headers_len > MAX_TOTAL_HEADERS_LEN {
                        return Err("headers_too_large");
                    }
                    if line == "\r\n" || line == "\n" {
                        break;
                    }
                    headers.push(line);
                }
                Ok(false) => break,
                Err(e) => return Err(e),
            }
        }
        Ok((first_line, headers))
    })
    .await;

    let (first_line, headers) = match parse_result {
        Ok(Ok(parsed)) => parsed,
        Ok(Err("line_too_long")) | Ok(Err("headers_too_large")) => {
            let resp = "HTTP/1.1 431 Request Header Fields Too Large\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\nheader too large\n";
            let _ = write_half.write_all(resp.as_bytes()).await;
            return;
        }
        _ => {
            // Timeout or read failure -> close connection
            return;
        }
    };

    let path = first_line
        .split_whitespace()
        .nth(1)
        .unwrap_or("/")
        .split('?')
        .next()
        .unwrap_or("/");

    let (code, phrase, content_type, body): (u16, &str, &str, String) = match path {
        "/healthz" => {
            let (code, phrase, body) = healthz(config.heartbeat_timeout_secs, startup_time).await;
            (code, phrase, "text/plain", body)
        }
        "/readyz" => {
            let (code, phrase, body) =
                readyz(&db, &redis, config.heartbeat_timeout_secs, startup_time).await;
            (code, phrase, "text/plain", body)
        }
        "/admin/dead-letter" => {
            // Check authorization (issue #764)
            let mut admin_header: Option<String> = None;
            for h in &headers {
                if let Some((k, v)) = h.split_once(':') {
                    let k = k.trim();
                    let v = v.trim();
                    if k.eq_ignore_ascii_case("x-admin-key") {
                        admin_header = Some(v.to_string());
                    } else if k.eq_ignore_ascii_case("authorization") {
                        if let Some(bearer) = v
                            .strip_prefix("Bearer ")
                            .or_else(|| v.strip_prefix("bearer "))
                        {
                            admin_header = Some(bearer.trim().to_string());
                        }
                    }
                }
            }

            let is_authorized = match &config.admin_api_key {
                Some(expected) => admin_header
                    .as_deref()
                    .map(|k| k == expected)
                    .unwrap_or(false),
                None => peer_addr.ip().is_loopback(),
            };

            if !is_authorized {
                (
                    401,
                    "Unauthorized",
                    "application/json",
                    "{\"error\":\"unauthorized: missing or invalid admin key\"}\n".into(),
                )
            } else {
                let (code, phrase, body) = dead_letter_summary(&db).await;
                (code, phrase, "application/json", body)
            }
        }
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

pub async fn healthz(
    heartbeat_timeout_secs: u64,
    startup_time: Instant,
) -> (u16, &'static str, String) {
    if let Some(age) = crate::metrics::last_heartbeat_age_secs() {
        if age > heartbeat_timeout_secs {
            return (
                503,
                "Service Unavailable",
                format!("stalled: last heartbeat was {age}s ago (threshold: {heartbeat_timeout_secs}s)\n"),
            );
        }
    } else if startup_time.elapsed().as_secs() > heartbeat_timeout_secs {
        return (
            503,
            "Service Unavailable",
            format!("unhealthy: no heartbeat recorded within startup period ({heartbeat_timeout_secs}s)\n"),
        );
    }
    (200, "OK", "ok\n".into())
}

async fn readyz(
    db: &PgPool,
    redis: &SharedRedis,
    heartbeat_timeout_secs: u64,
    startup_time: Instant,
) -> (u16, &'static str, String) {
    // Check heartbeat freshness first — a stalled poll loop is not ready
    let (health_code, _, health_msg) = healthz(heartbeat_timeout_secs, startup_time).await;
    if health_code != 200 {
        return (
            503,
            "Service Unavailable",
            format!("not ready: {health_msg}"),
        );
    }

    let db_ok = sqlx::query("SELECT 1").execute(db).await.is_ok();
    let redis_ok = redis.check_alive().await;

    if db_ok {
        if redis_ok {
            (200, "OK", "ready\n".into())
        } else {
            // Redis is not required for ingestion (issue #732) - report degraded but ready
            (200, "OK", "ready (redis degraded)\n".into())
        }
    } else {
        (503, "Service Unavailable", "not ready: db\n".into())
    }
}

/// Return the 20 most recent dead-lettered events as a JSON array.
///
/// Generic over the executor (rather than `&PgPool`) so tests can drive it
/// against a transaction they always roll back, instead of mutating the
/// shared test database directly.
pub async fn dead_letter_summary<'e, E>(db: E) -> (u16, &'static str, String)
where
    E: sqlx::PgExecutor<'e>,
{
    match sqlx::query_as::<_, DeadLetterRow>(
        r#"SELECT id, ledger_sequence, event_index, error_message, occurred_at
           FROM parse_errors
           ORDER BY occurred_at DESC
           LIMIT 20"#,
    )
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
            "INSERT INTO parse_errors (ledger_sequence, event_index, raw_payload, error_message)
             VALUES ($1, 0, 'raw-payload', 'boom')",
        )
        .bind(ledger_sequence)
        .execute(&pool)
        .await
        .expect("seed parse_errors row");

        let (code, phrase, body) = dead_letter_summary(&pool).await;

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

        let (code, _, body) = dead_letter_summary(&mut *tx).await;

        assert_eq!(code, 200);
        assert_eq!(body, "[]");

        tx.rollback().await.unwrap();
    }

    #[tokio::test]
    async fn test_healthz_stalled_poll_loop_reports_503() {
        let now_secs = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs_f64();

        // 300 seconds in past
        crate::metrics::set_heartbeat_timestamp(now_secs - 300.0);

        let (code, phrase, body) = healthz(120, Instant::now()).await;
        assert_eq!(code, 503);
        assert_eq!(phrase, "Service Unavailable");
        assert!(body.contains("stalled: last heartbeat was"));

        // Stamp fresh heartbeat
        crate::metrics::set_heartbeat_timestamp(now_secs);
        let (code, phrase, body) = healthz(120, Instant::now()).await;
        assert_eq!(code, 200);
        assert_eq!(phrase, "OK");
        assert_eq!(body, "ok\n");
    }

    #[tokio::test]
    async fn test_slowloris_timeout_closes_connection() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let cfg = HealthConfig {
            header_timeout: Duration::from_millis(150),
            ..Default::default()
        };

        let db_dummy = PgPool::connect_lazy("postgres://dummy:dummy@localhost/dummy").unwrap();
        let redis = Arc::new(SharedRedis::new("redis://localhost:6379".into(), None));

        tokio::spawn(async move {
            if let Ok((stream, peer)) = listener.accept().await {
                handle_conn(stream, peer, db_dummy, redis, cfg, Instant::now()).await;
            }
        });

        let mut client = TcpStream::connect(addr).await.unwrap();
        // Send partial header without newline
        client
            .write_all(b"GET /healthz HTTP/1.1\r\nHost: ")
            .await
            .unwrap();

        // Wait past the 150ms timeout
        tokio::time::sleep(Duration::from_millis(250)).await;

        // Next read or write on client should see EOF / closed connection
        let mut buf = [0u8; 16];
        let n = client.read(&mut buf).await.unwrap_or(0);
        assert_eq!(n, 0, "server must have closed slowloris connection");
    }

    #[tokio::test]
    async fn test_admin_dead_letter_auth_rejected_and_accepted() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let cfg = HealthConfig {
            admin_api_key: Some("secret123".into()),
            ..Default::default()
        };

        let db_dummy = PgPool::connect_lazy("postgres://dummy:dummy@localhost/dummy").unwrap();
        let redis = Arc::new(SharedRedis::new("redis://localhost:6379".into(), None));

        let srv_cfg = cfg.clone();
        let srv_db = db_dummy.clone();
        let srv_redis = redis.clone();
        tokio::spawn(async move {
            while let Ok((stream, peer)) = listener.accept().await {
                let db = srv_db.clone();
                let r = srv_redis.clone();
                let c = srv_cfg.clone();
                tokio::spawn(async move {
                    handle_conn(stream, peer, db, r, c, Instant::now()).await;
                });
            }
        });

        // 1. Unauthenticated request -> 401
        let mut client1 = TcpStream::connect(addr).await.unwrap();
        client1
            .write_all(b"GET /admin/dead-letter HTTP/1.1\r\nHost: localhost\r\n\r\n")
            .await
            .unwrap();
        let mut resp1 = String::new();
        let mut reader1 = BufReader::new(client1);
        reader1.read_line(&mut resp1).await.unwrap();
        assert!(resp1.contains("401 Unauthorized"), "got {resp1}");

        // 2. Authenticated with wrong key -> 401
        let mut client2 = TcpStream::connect(addr).await.unwrap();
        client2
            .write_all(b"GET /admin/dead-letter HTTP/1.1\r\nX-Admin-Key: wrong\r\n\r\n")
            .await
            .unwrap();
        let mut resp2 = String::new();
        let mut reader2 = BufReader::new(client2);
        reader2.read_line(&mut resp2).await.unwrap();
        assert!(resp2.contains("401 Unauthorized"), "got {resp2}");

        // 3. Authenticated with correct key -> enters handler (returns 500 or 200 depending on lazy db, but NOT 401)
        let mut client3 = TcpStream::connect(addr).await.unwrap();
        client3
            .write_all(b"GET /admin/dead-letter HTTP/1.1\r\nX-Admin-Key: secret123\r\n\r\n")
            .await
            .unwrap();
        let mut resp3 = String::new();
        let mut reader3 = BufReader::new(client3);
        reader3.read_line(&mut resp3).await.unwrap();
        assert!(
            !resp3.contains("401 Unauthorized"),
            "must not be 401: got {resp3}"
        );
    }
}
