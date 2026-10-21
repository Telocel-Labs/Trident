/// Minimal HTTP server providing /healthz (liveness) and /readyz (readiness)
/// endpoints on HEALTH_PORT (default 8080) (#206).
///
/// Prometheus /metrics are served separately by metrics-exporter-prometheus on
/// METRICS_PORT (default 9090). This server handles only Kubernetes probes.
///
/// The server runs in its own tokio task and is never intentionally stopped —
/// the process exiting is the shutdown mechanism. This is intentional for an
/// internal Kubernetes probe server: there is no value in a graceful drain.
use std::net::SocketAddr;

use sqlx::PgPool;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpListener;
use uuid::Uuid;

pub async fn serve(addr: SocketAddr, db: PgPool, redis_url: String) {
    let listener = match TcpListener::bind(addr).await {
        Ok(l) => l,
        Err(e) => {
            tracing::error!("health server failed to bind {addr}: {e}");
            return;
        }
    };
    tracing::info!("health server listening on {addr}");

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
        tokio::spawn(handle_conn(stream, db, redis_url));
    }
}

async fn handle_conn(stream: tokio::net::TcpStream, db: PgPool, redis_url: String) {
    let (read_half, mut write_half) = stream.into_split();
    let mut reader = BufReader::new(read_half);

    let mut first_line = String::new();
    if reader.read_line(&mut first_line).await.is_err() {
        return;
    }

    // Drain the remaining HTTP headers so the client doesn't get a RST.
    loop {
        let mut line = String::new();
        match reader.read_line(&mut line).await {
            Ok(0) | Err(_) => break,
            _ if line == "\r\n" || line.trim().is_empty() => break,
            _ => {}
        }
    }

    let raw_target = first_line.split_whitespace().nth(1).unwrap_or("/");

    let (path, query_str) = match raw_target.split_once('?') {
        Some((p, q)) => (p, q),
        None => (raw_target, ""),
    };

    let (code, phrase, content_type, body): (u16, &str, &str, String) = match path {
        "/healthz" => (200, "OK", "text/plain", "ok\n".into()),
        "/readyz" => {
            let (code, phrase, body) = readyz(&db, &redis_url).await;
            (code, phrase, "text/plain", body)
        }
        // Admin endpoint: return recent dead-lettered (parse error) events
        // for operational inspection (issue #414, #679).
        "/admin/dead-letter" => match parse_pagination(query_str) {
            Ok((limit, offset)) => {
                let (code, phrase, body) = dead_letter_summary(&db, limit, offset).await;
                (code, phrase, "application/json", body)
            }
            Err(err) => (
                400,
                "Bad Request",
                "application/json",
                serde_json::json!({ "error": err }).to_string(),
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

async fn readyz(db: &PgPool, redis_url: &str) -> (u16, &'static str, String) {
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
        (503, "Service Unavailable", "not ready: db\n".into())
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

pub const DEFAULT_DEAD_LETTER_LIMIT: i64 = 20;
pub const MAX_DEAD_LETTER_LIMIT: i64 = 100;

/// Parse pagination query parameters (`limit` and `offset`) for `/admin/dead-letter`.
pub fn parse_pagination(query: &str) -> Result<(i64, i64), String> {
    let mut limit = DEFAULT_DEAD_LETTER_LIMIT;
    let mut offset = 0i64;

    if query.is_empty() {
        return Ok((limit, offset));
    }

    for pair in query.split('&') {
        if pair.is_empty() {
            continue;
        }
        let (key, val) = match pair.split_once('=') {
            Some((k, v)) => (k, v),
            None => (pair, ""),
        };

        match key {
            "limit" => match val.parse::<i64>() {
                Ok(v) if v > 0 => {
                    limit = v.min(MAX_DEAD_LETTER_LIMIT);
                }
                _ => return Err("limit must be a positive integer".into()),
            },
            "offset" => match val.parse::<i64>() {
                Ok(v) if v >= 0 => {
                    offset = v;
                }
                _ => return Err("offset must be a non-negative integer".into()),
            },
            _ => {}
        }
    }

    Ok((limit, offset))
}

/// Return dead-lettered events as a JSON array with offset/limit pagination (#679).
///
/// Generic over the executor (rather than `&PgPool`) so tests can drive it
/// against a transaction they always roll back, instead of mutating the
/// shared test database directly.
async fn dead_letter_summary<'e, E>(db: E, limit: i64, offset: i64) -> (u16, &'static str, String)
where
    E: sqlx::PgExecutor<'e>,
{
    let limit = limit.clamp(1, MAX_DEAD_LETTER_LIMIT);
    let offset = offset.max(0);

    match sqlx::query_as::<_, DeadLetterRow>(
        r#"SELECT id, ledger_sequence, event_index, error_message, occurred_at
           FROM parse_errors
           ORDER BY occurred_at DESC, id DESC
           LIMIT $1 OFFSET $2"#,
    )
    .bind(limit)
    .bind(offset)
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
            "INSERT INTO parse_errors (ledger_sequence, event_index, raw_payload, error_message)
             VALUES ($1, 0, 'raw-payload', 'boom')",
        )
        .bind(ledger_sequence)
        .execute(&pool)
        .await
        .expect("seed parse_errors row");

        let (code, phrase, body) = dead_letter_summary(&pool, DEFAULT_DEAD_LETTER_LIMIT, 0).await;

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

        let (code, _, body) = dead_letter_summary(&mut *tx, DEFAULT_DEAD_LETTER_LIMIT, 0).await;

        assert_eq!(code, 200);
        assert_eq!(body, "[]");

        // Never commit: this DELETE must not outlive the test.
        tx.rollback().await.unwrap();
    }

    #[test]
    fn parse_pagination_defaults_when_empty() {
        let (limit, offset) = parse_pagination("").expect("empty query should yield defaults");
        assert_eq!(limit, DEFAULT_DEAD_LETTER_LIMIT);
        assert_eq!(offset, 0);
    }

    #[test]
    fn parse_pagination_valid_custom_values() {
        let (limit, offset) =
            parse_pagination("limit=10&offset=30").expect("valid pagination query");
        assert_eq!(limit, 10);
        assert_eq!(offset, 30);

        let (limit, offset) =
            parse_pagination("offset=50&limit=5").expect("valid reversed order query");
        assert_eq!(limit, 5);
        assert_eq!(offset, 50);

        let (limit, offset) = parse_pagination("limit=42").expect("valid limit only");
        assert_eq!(limit, 42);
        assert_eq!(offset, 0);

        let (limit, offset) = parse_pagination("offset=15").expect("valid offset only");
        assert_eq!(limit, DEFAULT_DEAD_LETTER_LIMIT);
        assert_eq!(offset, 15);
    }

    #[test]
    fn parse_pagination_clamps_to_max_limit() {
        let (limit, offset) =
            parse_pagination("limit=500&offset=10").expect("query above max limit");
        assert_eq!(limit, MAX_DEAD_LETTER_LIMIT);
        assert_eq!(offset, 10);
    }

    #[test]
    fn parse_pagination_rejects_invalid_values() {
        assert!(
            parse_pagination("limit=0").is_err(),
            "limit 0 must be rejected"
        );
        assert!(
            parse_pagination("limit=-10").is_err(),
            "negative limit must be rejected"
        );
        assert!(
            parse_pagination("limit=foo").is_err(),
            "non-numeric limit must be rejected"
        );
        assert!(
            parse_pagination("offset=-1").is_err(),
            "negative offset must be rejected"
        );
        assert!(
            parse_pagination("offset=bar").is_err(),
            "non-numeric offset must be rejected"
        );
    }

    #[tokio::test]
    async fn dead_letter_summary_pagination_pages_beyond_limit() {
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

        // Seed 5 parse error rows with distinctive ledger sequences
        for i in 1..=5 {
            sqlx::query(
                "INSERT INTO parse_errors (ledger_sequence, event_index, raw_payload, error_message, occurred_at)
                 VALUES ($1, 0, 'raw-payload', 'pagination-test', NOW() - ($2 || ' seconds')::interval)",
            )
            .bind(9_998_000i64 + i)
            .bind(5 - i)
            .execute(&mut *tx)
            .await
            .expect("seed parse_errors row");
        }

        // Fetch page 1: limit 2, offset 0
        let (code, phrase, body1) = dead_letter_summary(&mut *tx, 2, 0).await;
        assert_eq!(code, 200);
        assert_eq!(phrase, "OK");
        let entries1: Vec<serde_json::Value> = serde_json::from_str(&body1).unwrap();
        assert_eq!(entries1.len(), 2, "first page must return exactly 2 items");

        // Fetch page 2: limit 2, offset 2
        let (code, phrase, body2) = dead_letter_summary(&mut *tx, 2, 2).await;
        assert_eq!(code, 200);
        assert_eq!(phrase, "OK");
        let entries2: Vec<serde_json::Value> = serde_json::from_str(&body2).unwrap();
        assert_eq!(entries2.len(), 2, "second page must return exactly 2 items");

        // Verify page 1 and page 2 do not overlap
        let ids1: Vec<&str> = entries1.iter().map(|e| e["id"].as_str().unwrap()).collect();
        let ids2: Vec<&str> = entries2.iter().map(|e| e["id"].as_str().unwrap()).collect();
        for id in &ids1 {
            assert!(
                !ids2.contains(id),
                "page 1 and page 2 must not contain the same row id"
            );
        }

        // Fetch beyond available rows: offset 100
        let (code, phrase, body_empty) = dead_letter_summary(&mut *tx, 2, 100).await;
        assert_eq!(code, 200);
        assert_eq!(phrase, "OK");
        let entries_empty: Vec<serde_json::Value> = serde_json::from_str(&body_empty).unwrap();
        assert_eq!(
            entries_empty.len(),
            0,
            "offset beyond records must return empty array"
        );

        tx.rollback().await.unwrap();
    }
}
