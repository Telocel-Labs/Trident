use std::collections::HashSet;
use std::str::FromStr;
use std::time::Duration;

use chrono::{DateTime, Utc};
use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use sqlx::PgPool;
use trident_common::{EventType, SorobanEvent, TridentError};
use uuid::Uuid;

/// Build a bounded Postgres connection pool sized for this service.
///
/// `statement_cache_capacity(0)` disables sqlx's named-prepared-statement cache.
/// This is mandatory when connecting through PgBouncer in transaction pooling
/// mode: a cached prepared statement is bound to one server connection, but in
/// transaction mode the next transaction may be routed to a different server
/// connection where that statement does not exist, which makes the query fail.
/// See docs/deployment.md (issue #87).
pub async fn connect_pool(database_url: &str, pool_size: u32) -> Result<PgPool, TridentError> {
    let connect_options = PgConnectOptions::from_str(database_url)
        .map_err(|e| TridentError::config(anyhow::Error::new(e).context("invalid DATABASE_URL")))?
        .statement_cache_capacity(0);

    PgPoolOptions::new()
        .max_connections(pool_size)
        .connect_with(connect_options)
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("connect_pool")))
}

// Stable namespace for deterministic event UUIDs (UUIDv5).
// Using the DNS namespace is arbitrary; what matters is that it is fixed.
const EVENT_NS: Uuid = Uuid::NAMESPACE_DNS;

/// Derive a deterministic UUID for an event from its indexer-internal composite key.
///
/// # Key composition
/// The UUID is derived from `"contract_id:ledger_sequence:event_index"`.
///
/// **Important**: this is NOT the same as the Stellar protocol's natural key
/// `(transaction_hash, event_index, network)`.  The two keys are complementary:
///
/// | Key | Fields | Purpose |
/// |-----|--------|---------|
/// | UUIDv5 id | contract_id · ledger_sequence · event_index | Stable primary key for the indexer; deduplicates replays |
/// | Natural key | transaction_hash · event_index · network | Protocol-level uniqueness; enforced by `uq_soroban_events_tx_index_network` |
///
/// Using the same inputs will always produce the same UUID, so a replayed event
/// produces the same `id` and `ON CONFLICT (id) DO NOTHING` fires.
///
/// Because `id` is not a pure function of `(transaction_hash, event_index)`, the
/// database also carries a `UNIQUE (transaction_hash, event_index, network)` constraint
/// (migration 0010) as an independent correctness guard.
fn event_uuid(contract_id: &str, ledger_sequence: u64, event_index: u32) -> Uuid {
    let key = format!("{contract_id}:{ledger_sequence}:{event_index}");
    Uuid::new_v5(&EVENT_NS, key.as_bytes())
}

/// Insert a normalised event.
///
/// Duplicate handling uses two complementary strategies:
/// - **Primary**: `ON CONFLICT (id) DO NOTHING` — deduplicates replays because `id`
///   is a deterministic UUIDv5 derived from `(contract_id, ledger_sequence, event_index)`.
/// - **Safety net**: `UNIQUE (transaction_hash, event_index, network)` at the DB layer
///   (migration 0010) catches any case where the same protocol event would be inserted
///   with a different derived `id` (e.g. due to a bug in id derivation).
///
/// The `network` argument must match the value used in `indexed_contracts` for this
/// deployment (e.g. `"mainnet"` or `"testnet"`).
pub async fn insert_event(
    pool: &PgPool,
    event: &SorobanEvent,
    network: &str,
) -> Result<(), TridentError> {
    let id = event_uuid(&event.contract_id, event.ledger_sequence, event.event_index);
    let event_type = match event.event_type {
        EventType::Contract => "contract",
        EventType::System => "system",
        EventType::Diagnostic => "diagnostic",
    };
    let topics = serde_json::to_value(&event.topics)
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("topics serialise")))?;
    let ledger_ts: DateTime<Utc> = event.ledger_timestamp.parse().map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("ledger timestamp parse"))
    })?;

    sqlx::query(
        r#"
        INSERT INTO soroban_events
            (id, contract_id, ledger_sequence, ledger_timestamp, transaction_hash,
             event_index, event_type, topics, data, network)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
        ON CONFLICT (id) DO NOTHING
        "#,
    )
    .bind(id)
    .bind(&event.contract_id)
    .bind(event.ledger_sequence as i64)
    .bind(ledger_ts)
    .bind(&event.transaction_hash)
    .bind(event.event_index as i32)
    .bind(event_type)
    .bind(&topics)
    .bind(&event.data)
    .bind(network)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("insert_event")))?;

    Ok(())
}

/// Read the latest processed ledger cursor from system_state.
pub async fn get_cursor(pool: &PgPool) -> Result<u64, TridentError> {
    let row: (String,) =
        sqlx::query_as("SELECT value FROM system_state WHERE key = 'latest_ledger_cursor'")
            .fetch_one(pool)
            .await
            .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("get_cursor")))?;

    row.0
        .parse::<u64>()
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("cursor parse")))
}

/// Persist the latest processed ledger sequence so the streamer can resume
/// from the correct position after a restart.
pub async fn set_cursor(pool: &PgPool, ledger: u64) -> Result<(), TridentError> {
    sqlx::query(
        "UPDATE system_state SET value = $1, updated_at = NOW() WHERE key = 'latest_ledger_cursor'",
    )
    .bind(ledger.to_string())
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("set_cursor")))?;

    Ok(())
}

/// Record a processed ledger in ledger_metadata for gap detection.
pub async fn insert_ledger_metadata(
    pool: &PgPool,
    ledger_sequence: u64,
    ledger_hash: &str,
    ledger_timestamp: &str,
    event_count: i32,
) -> Result<(), TridentError> {
    let ts: DateTime<Utc> = ledger_timestamp.parse().map_err(|e| {
        TridentError::storage(anyhow::Error::new(e).context("ledger timestamp parse"))
    })?;

    sqlx::query(
        r#"
        INSERT INTO ledger_metadata (ledger_sequence, ledger_hash, ledger_timestamp, event_count)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (ledger_sequence) DO NOTHING
        "#,
    )
    .bind(ledger_sequence as i64)
    .bind(ledger_hash)
    .bind(ts)
    .bind(event_count)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("insert_ledger_metadata")))?;

    Ok(())
}

/// Write indexer health metrics into the `system_state` health columns after
/// every successful poll cycle (issue #62).
///
/// Uses a single `UPDATE` on the known cursor row so there is never a
/// duplicate-key issue and the write is O(1) regardless of table size.
pub async fn update_health_stats(
    pool: &PgPool,
    last_ledger: i64,
    events_in_poll: i32,
    poll_duration: Duration,
) -> Result<(), TridentError> {
    let poll_ms = poll_duration.as_millis().min(i32::MAX as u128) as i32;

    sqlx::query(
        r#"
        UPDATE system_state
        SET
            last_poll_at          = NOW(),
            last_ledger_indexed   = $1,
            events_in_last_poll   = $2,
            poll_duration_ms      = $3,
            events_indexed_total  = COALESCE(events_indexed_total, 0) + $2,
            updated_at            = NOW()
        WHERE key = 'latest_ledger_cursor'
        "#,
    )
    .bind(last_ledger)
    .bind(events_in_poll)
    .bind(poll_ms)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("update_health_stats")))?;

    Ok(())
}

/// Load all contract IDs from `indexed_contracts` for the given network (or
/// network-agnostic rows where `network IS NULL`).
///
/// Returns an empty set if the table has no rows — the caller treats an empty
/// set as "index all contracts" (issue #47).
pub async fn load_indexed_contracts(
    pool: &PgPool,
    network: &str,
) -> Result<HashSet<String>, TridentError> {
    let rows: Vec<(String,)> = sqlx::query_as(
        "SELECT contract_id FROM indexed_contracts WHERE network = $1 OR network IS NULL",
    )
    .bind(network)
    .fetch_all(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("load_indexed_contracts")))?;

    Ok(rows.into_iter().map(|(id,)| id).collect())
}

/// Read alert state (last_alert_at, alert_fired) from system_state (issue #75).
pub async fn get_alert_state(pool: &PgPool) -> Result<crate::alerting::AlertState, TridentError> {
    let row: (Option<chrono::DateTime<chrono::Utc>>, bool) = sqlx::query_as(
        "SELECT last_alert_at, alert_fired FROM system_state WHERE key = 'latest_ledger_cursor'",
    )
    .fetch_one(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("get_alert_state")))?;

    Ok(crate::alerting::AlertState {
        last_alert_at: row.0,
        alert_fired: row.1,
    })
}

/// Persist alert state back to system_state after an alerting evaluation (issue #75).
pub async fn set_alert_state(
    pool: &PgPool,
    state: &crate::alerting::AlertState,
) -> Result<(), TridentError> {
    sqlx::query(
        r#"
        UPDATE system_state
        SET last_alert_at = $1,
            alert_fired   = $2,
            updated_at    = NOW()
        WHERE key = 'latest_ledger_cursor'
        "#,
    )
    .bind(state.last_alert_at)
    .bind(state.alert_fired)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("set_alert_state")))?;

    Ok(())
}

/// Record a parse error to parse_errors table for auditing and potential replay.
pub async fn insert_parse_error(
    pool: &PgPool,
    ledger_sequence: u64,
    event_index: u32,
    raw_payload: &str,
    error_message: &str,
) -> Result<(), TridentError> {
    sqlx::query(
        r#"
        INSERT INTO parse_errors
            (ledger_sequence, event_index, raw_payload, error_message)
        VALUES ($1, $2, $3, $4)
        "#,
    )
    .bind(ledger_sequence as i64)
    .bind(event_index as i32)
    .bind(raw_payload)
    .bind(error_message)
    .execute(pool)
    .await
    .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("insert_parse_error")))?;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use trident_common::{EventType, SorobanEvent};

    fn make_event(contract_id: &str, ledger_sequence: u64, event_index: u32) -> SorobanEvent {
        SorobanEvent {
            contract_id: contract_id.to_string(),
            ledger_sequence,
            ledger_timestamp: "2024-01-01T00:00:00Z".to_string(),
            transaction_hash: "txhash_abc123".to_string(),
            event_index,
            event_type: EventType::Contract,
            topics: vec![],
            data: json!({}),
        }
    }

    /// Deterministic UUID: same inputs must produce the same id.
    #[test]
    fn event_uuid_is_deterministic() {
        let a = event_uuid("CABC", 100, 0);
        let b = event_uuid("CABC", 100, 0);
        assert_eq!(a, b);
    }

    /// Different natural keys must produce different UUIDs.
    #[test]
    fn event_uuid_varies_with_inputs() {
        let a = event_uuid("CABC", 100, 0);
        let b = event_uuid("CABC", 100, 1);
        assert_ne!(a, b);
    }

    /// The UUID id is NOT derived from transaction_hash, so two events with
    /// different contract_ids but the same (transaction_hash, event_index) would
    /// produce different UUIDs. This test documents that distinction — the
    /// natural-key constraint (uq_soroban_events_tx_index_network) is the guard
    /// for that case.
    #[test]
    fn event_uuid_does_not_include_transaction_hash() {
        // Same (contract_id, ledger, event_index) → same UUID regardless of tx_hash.
        let uuid_a = event_uuid("CABC", 100, 0);
        let uuid_b = event_uuid("CABC", 100, 0); // identical inputs, different tx_hash in the event struct
        assert_eq!(
            uuid_a, uuid_b,
            "UUID must be stable across calls with the same indexer key"
        );

        // Different contract_id → different UUID even if tx+index were the same.
        let uuid_c = event_uuid("CXYZ", 100, 0);
        assert_ne!(
            uuid_a, uuid_c,
            "different contract_id must produce a different UUID"
        );
    }

    /// Calling `insert_event` twice with the same event must not error and
    /// the row count in `soroban_events` must remain 1.
    ///
    /// Uses the shared test database (TEST_DATABASE_URL) like the other
    /// integration tests; skips when it is not configured.
    #[tokio::test]
    async fn insert_event_is_idempotent() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(url) => url,
            // Hard-fail under the rust-integration CI job (REQUIRE_TEST_SERVICES)
            // so a misconfigured DB URL cannot silently skip and go green.
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };
        let pool = PgPool::connect(&db_url).await.unwrap();

        let event = make_event("CABC_CONTRACT_001", 42, 0);

        // Isolate from other tests sharing the database.
        sqlx::query("DELETE FROM soroban_events WHERE contract_id = $1")
            .bind(&event.contract_id)
            .execute(&pool)
            .await
            .expect("cleanup failed");

        insert_event(&pool, &event, "testnet")
            .await
            .expect("first insert failed");
        insert_event(&pool, &event, "testnet")
            .await
            .expect("second insert must not error");

        let count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE contract_id = $1")
                .bind(&event.contract_id)
                .fetch_one(&pool)
                .await
                .expect("count query failed");

        assert_eq!(count.0, 1, "duplicate insert should be silently ignored");
    }

    /// Inserting two events with the same (transaction_hash, event_index, network) but
    /// different contract_ids (which would produce different UUIDs) must be rejected
    /// by the natural-key constraint `uq_soroban_events_tx_index_network`.
    ///
    /// This validates that the DB-level guard works independently of the id scheme.
    #[tokio::test]
    async fn natural_key_constraint_rejects_duplicate_tx_event_index() {
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

        // Shared (transaction_hash, event_index) — this is the natural key.
        let shared_tx_hash = "txhash_natural_key_test_001";
        let shared_event_index: u32 = 0;
        let network = "testnet";

        // Clean up any leftovers from previous runs.
        sqlx::query("DELETE FROM soroban_events WHERE transaction_hash = $1")
            .bind(shared_tx_hash)
            .execute(&pool)
            .await
            .expect("cleanup failed");

        // First event: contract A, same tx+index.
        let event_a = SorobanEvent {
            contract_id: "CONTRACT_A_NATURAL_KEY_TEST".to_string(),
            ledger_sequence: 999,
            ledger_timestamp: "2024-01-01T00:00:00Z".to_string(),
            transaction_hash: shared_tx_hash.to_string(),
            event_index: shared_event_index,
            event_type: EventType::Contract,
            topics: vec![],
            data: json!({}),
        };
        insert_event(&pool, &event_a, network)
            .await
            .expect("first insert (contract A) must succeed");

        // Second event: DIFFERENT contract_id → DIFFERENT UUID, but SAME (tx_hash, event_index, network).
        // The natural-key constraint must reject this.
        let event_b = SorobanEvent {
            contract_id: "CONTRACT_B_NATURAL_KEY_TEST".to_string(),
            ledger_sequence: 999,
            ledger_timestamp: "2024-01-01T00:00:00Z".to_string(),
            transaction_hash: shared_tx_hash.to_string(),
            event_index: shared_event_index,
            event_type: EventType::Contract,
            topics: vec![],
            data: json!({}),
        };
        let result = insert_event(&pool, &event_b, network).await;
        assert!(
            result.is_err(),
            "inserting a duplicate (transaction_hash, event_index, network) with a different \
             contract_id must be rejected by uq_soroban_events_tx_index_network"
        );

        // Verify exactly one row persisted.
        let count: (i64,) =
            sqlx::query_as("SELECT COUNT(*) FROM soroban_events WHERE transaction_hash = $1")
                .bind(shared_tx_hash)
                .fetch_one(&pool)
                .await
                .expect("count query failed");
        assert_eq!(count.0, 1, "only the first event should be stored");

        // Cleanup.
        sqlx::query("DELETE FROM soroban_events WHERE transaction_hash = $1")
            .bind(shared_tx_hash)
            .execute(&pool)
            .await
            .expect("post-test cleanup failed");
    }
}
