//! Integration tests asserting that the escrow reference contract's event
//! sequence is indexed in the correct order and is filterable by contract.
//!
//! These tests require TEST_DATABASE_URL to be set and run with a real
//! Postgres instance. They are skipped silently when the env var is absent
//! (or hard-failed when REQUIRE_TEST_SERVICES is set).
//!
//! The escrow flow under test:
//!   1. deposit  → topics = ["deposit"]
//!   2. release  → topics = ["release"]
//!
//! And the refund path:
//!   1. deposit  → topics = ["deposit"]
//!   2. refund   → topics = ["refund"]

#[cfg(test)]
mod escrow_indexing {
    use serde_json::json;
    use sqlx::PgPool;
    use trident_common::{EventType, SorobanEvent};

    use crate::db::{self, LedgerMeta};

    const ESCROW_CONTRACT: &str = "CESCROW_TEST_CONTRACT_0000000000000000000000000000000000";

    fn make_escrow_event(topic: &str, ledger: u64, idx: u32) -> SorobanEvent {
        SorobanEvent {
            contract_id: ESCROW_CONTRACT.to_string(),
            ledger_sequence: ledger,
            ledger_timestamp: "2024-06-01T00:00:00Z".to_string(),
            transaction_hash: format!("txhash_{topic}_{ledger}_{idx}"),
            event_index: idx,
            event_type: EventType::Contract,
            topics: vec![topic.to_string()],
            data: json!({ "topic": topic }),
        }
    }

    async fn get_test_pool() -> Option<PgPool> {
        let url = match std::env::var("TEST_DATABASE_URL") {
            Ok(u) => u,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set — skipping escrow integration tests");
                return None;
            }
        };
        Some(PgPool::connect(&url).await.unwrap())
    }

    /// Insert the happy-path escrow sequence (deposit → release) and assert that
    /// the events are retrievable in ledger order when filtered to the escrow contract.
    #[tokio::test]
    async fn escrow_happy_path_indexed_in_order() {
        let pool = match get_test_pool().await {
            Some(p) => p,
            None => return,
        };

        let deposit_event = make_escrow_event("deposit", 1_000, 0);
        let release_event = make_escrow_event("release", 1_001, 0);

        let meta_deposit = LedgerMeta {
            sequence: 1_000,
            hash: "hash_deposit",
            timestamp: "2024-06-01T00:00:00Z",
            event_count: 1,
        };
        let meta_release = LedgerMeta {
            sequence: 1_001,
            hash: "hash_release",
            timestamp: "2024-06-01T00:00:01Z",
            event_count: 1,
        };

        db::commit_page(&pool, &[deposit_event], &meta_deposit, &[], 1_000)
            .await
            .expect("commit deposit page");
        db::commit_page(&pool, &[release_event], &meta_release, &[], 1_001)
            .await
            .expect("commit release page");

        // Query events for the escrow contract and assert ordering.
        let rows: Vec<(String, i64, i32)> = sqlx::query_as(
            "SELECT topics[1], ledger_sequence, event_index
             FROM soroban_events
             WHERE contract_id = $1
             ORDER BY ledger_sequence ASC, event_index ASC",
        )
        .bind(ESCROW_CONTRACT)
        .fetch_all(&pool)
        .await
        .expect("query escrow events");

        // Filter to our specific run (in case previous runs left rows).
        let our_rows: Vec<_> = rows
            .iter()
            .filter(|(_, ls, _)| *ls == 1_000 || *ls == 1_001)
            .collect();

        assert!(our_rows.len() >= 2, "expected at least 2 events, got {}", our_rows.len());

        let first_topic = &our_rows[our_rows.len() - 2].0;
        let second_topic = &our_rows[our_rows.len() - 1].0;
        assert_eq!(first_topic, "deposit", "first event must be deposit");
        assert_eq!(second_topic, "release", "second event must be release");
    }

    /// Insert the refund path (deposit → refund) and assert ordering.
    #[tokio::test]
    async fn escrow_refund_path_indexed_in_order() {
        let pool = match get_test_pool().await {
            Some(p) => p,
            None => return,
        };

        let deposit_event = make_escrow_event("deposit", 2_000, 0);
        let refund_event = make_escrow_event("refund", 2_001, 0);

        let meta_deposit = LedgerMeta {
            sequence: 2_000,
            hash: "hash2_deposit",
            timestamp: "2024-06-02T00:00:00Z",
            event_count: 1,
        };
        let meta_refund = LedgerMeta {
            sequence: 2_001,
            hash: "hash2_refund",
            timestamp: "2024-06-02T00:00:01Z",
            event_count: 1,
        };

        db::commit_page(&pool, &[deposit_event], &meta_deposit, &[], 2_000)
            .await
            .expect("commit deposit page");
        db::commit_page(&pool, &[refund_event], &meta_refund, &[], 2_001)
            .await
            .expect("commit refund page");

        let rows: Vec<(String, i64, i32)> = sqlx::query_as(
            "SELECT topics[1], ledger_sequence, event_index
             FROM soroban_events
             WHERE contract_id = $1 AND ledger_sequence IN (2000, 2001)
             ORDER BY ledger_sequence ASC, event_index ASC",
        )
        .bind(ESCROW_CONTRACT)
        .fetch_all(&pool)
        .await
        .expect("query escrow refund events");

        assert_eq!(rows.len(), 2, "expected exactly 2 events");
        assert_eq!(rows[0].0, "deposit", "first event must be deposit");
        assert_eq!(rows[1].0, "refund", "second event must be refund");
    }

    /// Per-contract filtering: events from the escrow contract must not appear
    /// when querying a different contract id.
    #[tokio::test]
    async fn escrow_per_contract_filtering() {
        let pool = match get_test_pool().await {
            Some(p) => p,
            None => return,
        };

        let deposit_event = make_escrow_event("deposit", 3_000, 0);
        let meta = LedgerMeta {
            sequence: 3_000,
            hash: "hash3",
            timestamp: "2024-06-03T00:00:00Z",
            event_count: 1,
        };

        db::commit_page(&pool, &[deposit_event], &meta, &[], 3_000)
            .await
            .expect("commit deposit page");

        let rows: Vec<(String,)> = sqlx::query_as(
            "SELECT contract_id FROM soroban_events
             WHERE contract_id = 'CDIFFERENT_CONTRACT_NOT_ESCROW' AND ledger_sequence = 3000",
        )
        .fetch_all(&pool)
        .await
        .expect("query different contract");

        assert!(
            rows.is_empty(),
            "escrow events must not appear under a different contract id"
        );
    }
}
