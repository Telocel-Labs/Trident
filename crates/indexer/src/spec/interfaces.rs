//! # Interface detection (issue #269)
//!
//! Classifies a contract by matching the function names captured in its
//! parsed spec (issue #260) against known interface fingerprints. A contract
//! can match more than one fingerprint; one matching none is "custom".

/// Required function surface for a SEP-41 fungible token. Admin/issuer-only
/// extras (`mint`, `clawback`, `set_admin`, ...) vary by implementation and
/// are deliberately excluded from the fingerprint — only the standard's
/// mandatory client-facing interface is checked.
const SEP41_TOKEN_FUNCTIONS: &[&str] = &[
    "allowance",
    "approve",
    "balance",
    "transfer",
    "transfer_from",
    "burn",
    "decimals",
    "name",
    "symbol",
];

/// Required function surface for a non-fungible token: ownership, transfer,
/// and per-token approval, without the fungible `decimals` surface.
const NFT_FUNCTIONS: &[&str] = &[
    "balance",
    "owner_of",
    "transfer",
    "approve",
    "get_approved",
    "set_approval_for_all",
];

/// Result of matching a contract's spec functions against known fingerprints.
pub struct Detected {
    /// Every interface tag the contract matches (a contract may match more
    /// than one).
    pub interfaces: Vec<String>,
    /// Single label for display/decoder-selection purposes: the first match,
    /// or "custom" when nothing matched.
    pub contract_type: &'static str,
}

fn implements_all(functions: &[&str], required: &[&str]) -> bool {
    required.iter().all(|f| functions.contains(f))
}

/// Match a contract's exported function names against the known interface
/// fingerprints (issue #269).
pub fn detect_interfaces(functions: &[&str]) -> Detected {
    let mut interfaces = Vec::new();

    if implements_all(functions, SEP41_TOKEN_FUNCTIONS) {
        interfaces.push("sep41_token".to_string());
    }
    if implements_all(functions, NFT_FUNCTIONS) {
        interfaces.push("nft".to_string());
    }

    let contract_type = match interfaces.first().map(String::as_str) {
        Some("sep41_token") => "token",
        Some("nft") => "nft",
        _ => "custom",
    };

    Detected {
        interfaces,
        contract_type,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Full function surface of the reference SEP-41 token contract
    /// (contracts/token, issue #267).
    const SEP41_REFERENCE: &[&str] = &[
        "initialize",
        "mint",
        "set_admin",
        "admin",
        "allowance",
        "approve",
        "balance",
        "transfer",
        "transfer_from",
        "burn",
        "burn_from",
        "decimals",
        "name",
        "symbol",
    ];

    const NFT_REFERENCE: &[&str] = &[
        "balance",
        "owner_of",
        "transfer",
        "approve",
        "get_approved",
        "set_approval_for_all",
        "mint",
    ];

    #[test]
    fn classifies_a_sep41_token_contract() {
        let detected = detect_interfaces(SEP41_REFERENCE);
        assert_eq!(detected.contract_type, "token");
        assert!(detected.interfaces.contains(&"sep41_token".to_string()));
        assert!(!detected.interfaces.contains(&"nft".to_string()));
    }

    #[test]
    fn classifies_an_nft_contract() {
        let detected = detect_interfaces(NFT_REFERENCE);
        assert_eq!(detected.contract_type, "nft");
        assert!(detected.interfaces.contains(&"nft".to_string()));
        assert!(!detected.interfaces.contains(&"sep41_token".to_string()));
    }

    #[test]
    fn unknown_functions_classify_as_custom() {
        let detected = detect_interfaces(&["swap", "add_liquidity", "remove_liquidity"]);
        assert_eq!(detected.contract_type, "custom");
        assert!(detected.interfaces.is_empty());
    }

    #[test]
    fn partial_token_surface_does_not_match() {
        // Missing `transfer_from`/`burn` — not the full mandatory SEP-41 surface.
        let functions = &["balance", "transfer", "name", "symbol", "decimals"];
        let detected = detect_interfaces(functions);
        assert_eq!(detected.contract_type, "custom");
        assert!(detected.interfaces.is_empty());
    }

    #[test]
    fn empty_spec_classifies_as_custom() {
        let detected = detect_interfaces(&[]);
        assert_eq!(detected.contract_type, "custom");
        assert!(detected.interfaces.is_empty());
    }
}
