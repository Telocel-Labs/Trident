use serde::{Deserialize, Serialize};

// ---------------------------------------------------------------------------
// Contract liveness / archival tracking (issue #271)
// ---------------------------------------------------------------------------

/// Whether a Soroban contract entry is currently live or has been archived by
/// the ledger (TTL expired). Archived entries can be restored via
/// `restoreFootprint` but are not readable until then.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LivenessStatus {
    Live,
    Archived,
}

/// TTL and archival state for a tracked contract entry.
///
/// Populated by polling `getLedgerEntries` for the contract's instance key and
/// comparing `liveUntilLedger` against the current ledger (issue #271).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ContractLiveness {
    /// Strkey-encoded contract ID (C...) this record belongs to.
    pub contract_id: String,
    /// Whether the contract instance is currently live or archived.
    pub status: LivenessStatus,
    /// The ledger sequence at which the instance entry expires (inclusive).
    /// `None` if the entry has already been archived and the ledger has
    /// advanced past the expiry point.
    pub live_until_ledger: Option<u32>,
    /// Ledgers remaining before archival (current_ledger − live_until_ledger).
    /// Negative values (clamped to 0) indicate the entry is already archived.
    /// `None` when `live_until_ledger` is unknown.
    pub ledgers_until_archive: Option<i64>,
    /// Ledger sequence at which this record was last updated.
    pub last_checked_ledger: u32,
}

impl ContractLiveness {
    /// Compute liveness given the current ledger and the `liveUntilLedger`
    /// value returned by `getLedgerEntries`.
    pub fn from_rpc(
        contract_id: String,
        current_ledger: u32,
        live_until_ledger: Option<u32>,
        last_checked_ledger: u32,
    ) -> Self {
        let (status, ledgers_until_archive) = match live_until_ledger {
            Some(lul) if lul >= current_ledger => {
                let remaining = lul as i64 - current_ledger as i64;
                (LivenessStatus::Live, Some(remaining))
            }
            Some(_) => (LivenessStatus::Archived, Some(0)),
            None => (LivenessStatus::Archived, None),
        };

        ContractLiveness {
            contract_id,
            status,
            live_until_ledger,
            ledgers_until_archive,
            last_checked_ledger,
        }
    }

    /// Returns `true` when the contract is nearing archival (within `threshold`
    /// ledgers). Used to generate alerting metrics (issue #271).
    pub fn is_near_archival(&self, threshold: u32) -> bool {
        matches!(
            self.ledgers_until_archive,
            Some(remaining) if remaining >= 0 && remaining < threshold as i64
        )
    }
}

// ---------------------------------------------------------------------------
// Contract source verification (issue #273)
// ---------------------------------------------------------------------------

/// Whether a deployed contract's on-chain WASM code hash has been matched to a
/// submitted source build (issue #273).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum VerificationStatus {
    /// Not yet submitted for verification.
    Unverified,
    /// Source submitted; verification in progress.
    Pending,
    /// WASM hash from source build matches on-chain code hash.
    Verified,
    /// WASM hash from source build does NOT match the deployed hash.
    Mismatch,
    /// Build or verification attempt failed (e.g., toolchain error).
    Failed,
}

/// Source build metadata submitted alongside a verification request (issue #273).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SourceBuildMetadata {
    /// Git repository URL containing the contract source.
    pub repository_url: String,
    /// Git commit SHA pinning the exact source revision.
    pub commit_sha: String,
    /// Rust toolchain version used to build (e.g., "1.81.0").
    pub toolchain_version: String,
    /// Build command relative to the repo root (e.g., "cargo build --release --target wasm32-unknown-unknown").
    pub build_command: String,
    /// Path to the produced WASM within the repo (e.g., "target/wasm32-unknown-unknown/release/contract.wasm").
    pub wasm_path: String,
}

/// Verification record for a deployed contract (issue #273).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ContractVerification {
    /// Strkey-encoded contract ID (C...).
    pub contract_id: String,
    /// Current verification status.
    pub status: VerificationStatus,
    /// SHA-256 hash of the deployed WASM code, as reported by the ledger.
    pub on_chain_hash: String,
    /// SHA-256 hash of the WASM produced from the submitted source.
    /// `None` until a verification attempt completes.
    pub source_hash: Option<String>,
    /// Source build metadata. `None` for unverified contracts.
    pub build_metadata: Option<SourceBuildMetadata>,
    /// ISO 8601 timestamp of the most recent verification attempt.
    pub verified_at: Option<String>,
}

impl ContractVerification {
    /// Compare the on-chain WASM hash to a locally-produced WASM hash and
    /// return the resulting `VerificationStatus`.
    ///
    /// The comparison is case-insensitive to tolerate hex-case differences
    /// between toolchains (issue #273).
    pub fn compare_hashes(on_chain: &str, produced: &str) -> VerificationStatus {
        if on_chain.to_lowercase() == produced.to_lowercase() {
            VerificationStatus::Verified
        } else {
            VerificationStatus::Mismatch
        }
    }
}

/// Distinguishes the three event categories emitted by the Soroban runtime.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum EventType {
    /// Emitted explicitly by contract code via `env.events().publish(...)`.
    Contract,
    /// Emitted by the Soroban host itself (e.g. fee events).
    System,
    /// Emitted only when diagnostic mode is enabled; never stored by default.
    Diagnostic,
}

/// Normalised representation of a single Soroban event as stored in PostgreSQL
/// and published onto Redis Streams.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SorobanEvent {
    /// Strkey-encoded contract address (C...).
    pub contract_id: String,
    /// Ordered list of topic values, XDR-decoded to their string representations.
    pub topics: Vec<String>,
    /// Decoded event body. Scalar XDR types are coerced to JSON primitives;
    /// map/vec types become JSON objects/arrays.
    pub data: serde_json::Value,
    /// Ledger sequence number in which this event was emitted.
    pub ledger_sequence: u64,
    /// ISO 8601 UTC timestamp of the ledger close.
    pub ledger_timestamp: String,
    /// Hash of the transaction that emitted this event.
    pub transaction_hash: String,
    /// Zero-based index of this event within its transaction.
    pub event_index: u32,
    /// Category of event as reported by the Soroban host.
    pub event_type: EventType,
}
