//! Bootstrap the indexer's contract allow-list with well-known SAC (Stellar
//! Asset Contract) contract ids for the active network.
//!
//! Enabled via `SEED_WELL_KNOWN_CONTRACTS=true`. The list is sourced from the
//! optional env var `WELL_KNOWN_SAC_CONTRACTS` (comma-separated
//! `LABEL:CONTRACT_ID` pairs), which falls back to a small built-in set for
//! `mainnet` and `testnet` when not provided.
//!
//! The operation is **idempotent**: rows already present in
//! `indexed_contracts` are skipped via `ON CONFLICT DO NOTHING`, so it is
//! safe to run on every startup.

use sqlx::PgPool;
use trident_common::TridentError;

/// One well-known SAC entry to register.
pub struct SacEntry {
    pub label: &'static str,
    pub contract_id: &'static str,
}

// ---------------------------------------------------------------------------
// Built-in lists
// Provenance: Stellar expert / StellarX public contract registries.
// Update by adding rows and bumping the comment date.
// Last updated: 2026-07-27
// ---------------------------------------------------------------------------

const MAINNET_SACS: &[SacEntry] = &[
    SacEntry {
        label: "USDC (mainnet)",
        contract_id: "CCW67TSZV3SSS2HXMBQ5JFGCKJNXKZM7UQUWUZPUTHXSTZLEO7SJMI75",
    },
    SacEntry {
        label: "XLM native SAC (mainnet)",
        contract_id: "CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA",
    },
    SacEntry {
        label: "AQUA (mainnet)",
        contract_id: "CBDGDGC4AZEWDXAJVZWMQIUGJHH5AJ2VC7TLBKM7K2AWFH5QJKTBAHEF",
    },
];

const TESTNET_SACS: &[SacEntry] = &[
    SacEntry {
        label: "XLM native SAC (testnet)",
        contract_id: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
    },
    SacEntry {
        label: "USDC (testnet)",
        contract_id: "CBIELTK6YBZJU5UP2WWQEUCYKLPU6AUNZ2BQ4WWFEIE3USCIHMXQDAMA",
    },
];

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Parse the `WELL_KNOWN_SAC_CONTRACTS` env var (format:
/// `"LABEL1:CONTRACT_ID1,LABEL2:CONTRACT_ID2"`).
/// Returns `None` when the var is absent; `Some(vec)` with parsed pairs otherwise.
fn parse_override_list(raw: &str) -> Vec<(String, String)> {
    raw.split(',')
        .filter_map(|entry| {
            let mut parts = entry.splitn(2, ':');
            let label = parts.next()?.trim().to_string();
            let contract_id = parts.next()?.trim().to_string();
            if label.is_empty() || contract_id.is_empty() {
                return None;
            }
            Some((label, contract_id))
        })
        .collect()
}

/// Idempotently insert well-known SAC contract ids into `indexed_contracts` for
/// `network`. Skips entries already registered. Logs each insertion or skip.
///
/// The seed list is taken from the `WELL_KNOWN_SAC_CONTRACTS` env var when
/// set, otherwise falls back to the built-in list for the network.
pub async fn seed_well_known_contracts(pool: &PgPool, network: &str) -> Result<(), TridentError> {
    let entries: Vec<(String, String)> = match std::env::var("WELL_KNOWN_SAC_CONTRACTS").ok() {
        Some(raw) if !raw.is_empty() => parse_override_list(&raw),
        _ => match network {
            "mainnet" => MAINNET_SACS
                .iter()
                .map(|e| (e.label.to_string(), e.contract_id.to_string()))
                .collect(),
            "testnet" => TESTNET_SACS
                .iter()
                .map(|e| (e.label.to_string(), e.contract_id.to_string()))
                .collect(),
            other => {
                tracing::info!(
                    network = other,
                    "no built-in SAC list for this network; set WELL_KNOWN_SAC_CONTRACTS to seed"
                );
                return Ok(());
            }
        },
    };

    for (label, contract_id) in &entries {
        let rows_affected = sqlx::query(
            "INSERT INTO indexed_contracts (contract_id, network, label, index_from)
             VALUES ($1, $2, $3, 0)
             ON CONFLICT (contract_id, network) DO NOTHING",
        )
        .bind(contract_id)
        .bind(network)
        .bind(label)
        .execute(pool)
        .await
        .map_err(|e| TridentError::storage(anyhow::Error::new(e).context("sac_bootstrap insert")))?
        .rows_affected();

        if rows_affected > 0 {
            tracing::info!(contract_id, network, label, "seeded well-known SAC contract");
        } else {
            tracing::debug!(contract_id, network, label, "SAC contract already registered, skipped");
        }

        // Audit log — non-fatal if it fails (e.g. migration not yet applied).
        let _ = sqlx::query(
            "INSERT INTO sac_seed_log (contract_id, network, label)
             VALUES ($1, $2, $3)
             ON CONFLICT (contract_id, network) DO NOTHING",
        )
        .bind(contract_id)
        .bind(network)
        .bind(label)
        .execute(pool)
        .await;
    }

    tracing::info!(
        network,
        count = entries.len(),
        "SAC bootstrap complete"
    );
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_override_list_parses_valid_pairs() {
        let raw = "XLM:CABC123,USDC:CDEF456";
        let result = parse_override_list(raw);
        assert_eq!(result.len(), 2);
        assert_eq!(result[0], ("XLM".into(), "CABC123".into()));
        assert_eq!(result[1], ("USDC".into(), "CDEF456".into()));
    }

    #[test]
    fn parse_override_list_skips_malformed_entries() {
        let raw = "ONLYLABEL,GOOD:CABC,";
        let result = parse_override_list(raw);
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].0, "GOOD");
    }

    #[test]
    fn parse_override_list_handles_empty_string() {
        let result = parse_override_list("");
        assert!(result.is_empty());
    }

    #[test]
    fn built_in_mainnet_list_is_non_empty() {
        assert!(!MAINNET_SACS.is_empty());
        for e in MAINNET_SACS {
            assert!(!e.contract_id.is_empty());
            assert!(!e.label.is_empty());
        }
    }

    #[test]
    fn built_in_testnet_list_is_non_empty() {
        assert!(!TESTNET_SACS.is_empty());
    }

    /// Integration test: seed contracts into a real database and assert they appear.
    #[tokio::test]
    async fn seed_inserts_into_indexed_contracts() {
        let db_url = match std::env::var("TEST_DATABASE_URL") {
            Ok(u) => u,
            Err(_) if std::env::var("REQUIRE_TEST_SERVICES").is_ok() => {
                panic!("TEST_DATABASE_URL required");
            }
            Err(_) => {
                eprintln!("SKIP: TEST_DATABASE_URL not set");
                return;
            }
        };

        let pool = sqlx::PgPool::connect(&db_url).await.unwrap();

        // Run with a known override list so we don't depend on network state.
        std::env::set_var(
            "WELL_KNOWN_SAC_CONTRACTS",
            "TestToken:CTEST_SAC_SEED_000000000000000000000000000000000000000000",
        );

        seed_well_known_contracts(&pool, "testnet")
            .await
            .expect("seed should succeed");

        let rows: Vec<(String,)> = sqlx::query_as(
            "SELECT contract_id FROM indexed_contracts WHERE contract_id = $1",
        )
        .bind("CTEST_SAC_SEED_000000000000000000000000000000000000000000")
        .fetch_all(&pool)
        .await
        .unwrap();

        assert_eq!(rows.len(), 1, "seeded contract must be in indexed_contracts");

        // Cleanup
        sqlx::query("DELETE FROM indexed_contracts WHERE contract_id = $1")
            .bind("CTEST_SAC_SEED_000000000000000000000000000000000000000000")
            .execute(&pool)
            .await
            .unwrap();

        std::env::remove_var("WELL_KNOWN_SAC_CONTRACTS");
    }
}
