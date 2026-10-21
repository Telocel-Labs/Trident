use std::net::SocketAddr;

use sqlx::PgPool;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpListener;

pub async fn serve(addr: SocketAddr, db: PgPool) {
    let listener = match TcpListener::bind(addr).await {
        Ok(l) => l,
        Err(e) => {
            tracing::error!("backfill health server failed to bind {addr}: {e}");
            return;
        }
    };
    tracing::info!("backfill health server listening on {addr}");

    loop {
        let (stream, _) = match listener.accept().await {
            Ok(s) => s,
            Err(e) => {
                tracing::warn!("backfill health server accept error: {e}");
                continue;
            }
        };
        let db = db.clone();
        tokio::spawn(handle_conn(stream, db));
    }
}

async fn handle_conn(stream: tokio::net::TcpStream, db: PgPool) {
    let (read_half, mut write_half) = stream.into_split();
    let mut reader = BufReader::new(read_half);

    let mut first_line = String::new();
    if reader.read_line(&mut first_line).await.is_err() {
        return;
    }

    loop {
        let mut line = String::new();
        match reader.read_line(&mut line).await {
            Ok(0) | Err(_) => break,
            _ if line == "\r\n" || line.trim().is_empty() => break,
            _ => {}
        }
    }

    let path = first_line.split_whitespace().nth(1).unwrap_or("");
    let (status_code, status_text, body) = match path {
        "/healthz" => (200, "OK", "ok\n".to_string()),
        "/readyz" => {
            match sqlx::query("SELECT 1").execute(&db).await {
                Ok(_) => (200, "OK", "ready\n".to_string()),
                Err(e) => (503, "Service Unavailable", format!("database error: {e}\n")),
            }
        }
        _ => (404, "Not Found", "not found\n".to_string()),
    };

    let response = format!(
        "HTTP/1.1 {status_code} {status_text}\r\nContent-Type: text/plain\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    let _ = write_half.write_all(response.as_bytes()).await;
}
