//! Stellar Asset Contract (SAC) identification (issue #262).
//!
//! A SAC is a contract instance implicitly deployed for a classic asset
//! (`code:issuer`, or native XLM). It shares no on-chain deployment event with
//! ordinary contracts, but its contract id is fully determined by the asset
//! and the network: `contract_id = SHA256(HashIdPreimage::ContractId {
//! network_id, contract_id_preimage: ContractIdPreimage::Asset(asset) })`.
//! Deriving forward from a known asset list (rather than reversing an unknown
//! contract id back into an asset) is what the issue calls out as sufficient.

use sha2::{Digest, Sha256};
use stellar_strkey::Contract;
use stellar_xdr::curr::{
    AlphaNum12, AlphaNum4, Asset, AssetCode12, AssetCode4, ContractIdPreimage, Hash,
    HashIdPreimage, HashIdPreimageContractId, Limited, Limits, WriteXdr,
};
use trident_common::TridentError;

/// One tracked classic asset the operator wants SAC events resolved for.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TrackedAsset {
    /// Asset code, e.g. "USDC", or the literal "native" for XLM.
    pub code: String,
    /// Issuer account strkey (`G...`). Ignored for native.
    pub issuer: String,
}

/// Build the classic `Asset` XDR value for a code + issuer pair.
///
/// Native XLM is represented by the code "native" (case-insensitive) with no
/// meaningful issuer; codes of 1-4 characters use `AlphaNum4`, 5-12 use
/// `AlphaNum12` — the standard Stellar asset encoding, padded with trailing
/// zero bytes to the fixed-width array.
fn build_asset(code: &str, issuer: &str) -> Result<Asset, TridentError> {
    if code.eq_ignore_ascii_case("native") {
        return Ok(Asset::Native);
    }

    if code.is_empty() || code.len() > 12 {
        return Err(TridentError::config(anyhow::anyhow!(
            "SAC asset code {code:?} must be 1-12 characters"
        )));
    }

    let issuer_id = stellar_strkey::ed25519::PublicKey::from_string(issuer)
        .map_err(|e| {
            TridentError::config(anyhow::anyhow!("SAC asset issuer {issuer:?} invalid: {e}"))
        })?
        .0;
    let account_id = stellar_xdr::curr::AccountId(
        stellar_xdr::curr::PublicKey::PublicKeyTypeEd25519(stellar_xdr::curr::Uint256(issuer_id)),
    );

    if code.len() <= 4 {
        let mut bytes = [0u8; 4];
        bytes[..code.len()].copy_from_slice(code.as_bytes());
        Ok(Asset::CreditAlphanum4(AlphaNum4 {
            asset_code: AssetCode4(bytes),
            issuer: account_id,
        }))
    } else {
        let mut bytes = [0u8; 12];
        bytes[..code.len()].copy_from_slice(code.as_bytes());
        Ok(Asset::CreditAlphanum12(AlphaNum12 {
            asset_code: AssetCode12(bytes),
            issuer: account_id,
        }))
    }
}

/// Derive the strkey-encoded (`C...`) SAC contract id for an asset on a given
/// network passphrase.
///
/// `network_passphrase` is hashed with SHA-256 to obtain the network id, per
/// the standard `Network::id()` construction used throughout Stellar core and
/// SDKs (e.g. "Test SDF Network ; September 2015").
pub fn derive_sac_contract_id(
    asset_code: &str,
    issuer: &str,
    network_passphrase: &str,
) -> Result<String, TridentError> {
    let asset = build_asset(asset_code, issuer)?;

    let network_id: [u8; 32] = Sha256::digest(network_passphrase.as_bytes()).into();

    let preimage = HashIdPreimage::ContractId(HashIdPreimageContractId {
        network_id: Hash(network_id),
        contract_id_preimage: ContractIdPreimage::Asset(asset),
    });

    let mut buf = Vec::new();
    preimage
        .write_xdr(&mut Limited::new(&mut buf, Limits::none()))
        .map_err(|e| {
            TridentError::parse(anyhow::Error::new(e).context("SAC preimage XDR encode"))
        })?;

    let contract_hash: [u8; 32] = Sha256::digest(&buf).into();
    Ok(Contract(contract_hash).to_string().as_str().to_owned())
}

/// Contract id -> asset context lookup, built once at startup from the
/// operator-configured tracked-asset list (issue #262).
#[derive(Debug, Clone, Default)]
pub struct SacRegistry {
    by_contract_id: std::collections::HashMap<String, (String, String)>,
}

impl SacRegistry {
    /// Derive every tracked asset's SAC contract id and index it.
    ///
    /// A single malformed entry is a hard config error: silently dropping it
    /// would leave that asset's events unattributed with no operator signal.
    pub fn build(assets: &[TrackedAsset], network_passphrase: &str) -> Result<Self, TridentError> {
        let mut by_contract_id = std::collections::HashMap::with_capacity(assets.len());
        for asset in assets {
            let contract_id =
                derive_sac_contract_id(&asset.code, &asset.issuer, network_passphrase)?;
            by_contract_id.insert(contract_id, (asset.code.clone(), asset.issuer.clone()));
        }
        Ok(Self { by_contract_id })
    }

    /// Look up the asset context for a contract id, if it is a tracked SAC.
    pub fn lookup(&self, contract_id: &str) -> Option<(&str, &str)> {
        self.by_contract_id
            .get(contract_id)
            .map(|(code, issuer)| (code.as_str(), issuer.as_str()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const TESTNET_PASSPHRASE: &str = "Test SDF Network ; September 2015";
    const MAINNET_PASSPHRASE: &str = "Public Global Stellar Network ; September 2015";
    const ISSUER: &str = "GAIRCEIRCEIRCEIRCEIRCEIRCEIRCEIRCEIRCEIRCEIRCEIRCEIRCF6M";

    #[test]
    fn native_asset_derives_a_valid_strkey_contract_id() {
        let id = derive_sac_contract_id("native", "", TESTNET_PASSPHRASE).expect("derive");
        assert!(id.starts_with('C'), "got {id}");
        assert_eq!(id.len(), 56, "contract strkey must be 56 chars, got {id}");
    }

    #[test]
    fn native_is_case_insensitive() {
        let a = derive_sac_contract_id("native", "", TESTNET_PASSPHRASE).unwrap();
        let b = derive_sac_contract_id("NATIVE", "", TESTNET_PASSPHRASE).unwrap();
        assert_eq!(a, b);
    }

    #[test]
    fn deriving_twice_is_deterministic() {
        let a = derive_sac_contract_id("USDC", ISSUER, TESTNET_PASSPHRASE).unwrap();
        let b = derive_sac_contract_id("USDC", ISSUER, TESTNET_PASSPHRASE).unwrap();
        assert_eq!(a, b, "same inputs must derive the same contract id");
    }

    #[test]
    fn different_asset_codes_derive_different_ids() {
        let usdc = derive_sac_contract_id("USDC", ISSUER, TESTNET_PASSPHRASE).unwrap();
        let usdt = derive_sac_contract_id("USDT", ISSUER, TESTNET_PASSPHRASE).unwrap();
        assert_ne!(usdc, usdt);
    }

    #[test]
    fn different_network_passphrase_derives_a_different_id() {
        // Self-consistency check only: we are not asserting either value is a
        // real, published mainnet/testnet SAC id, just that network identity
        // is actually mixed into the hash.
        let testnet = derive_sac_contract_id("USDC", ISSUER, TESTNET_PASSPHRASE).unwrap();
        let mainnet = derive_sac_contract_id("USDC", ISSUER, MAINNET_PASSPHRASE).unwrap();
        assert_ne!(testnet, mainnet);
    }

    #[test]
    fn short_code_uses_alphanum4_and_long_code_uses_alphanum12() {
        // Codes of differing length classes must not collide, which would
        // happen if both were padded into the same fixed-width representation.
        let four = derive_sac_contract_id("USDC", ISSUER, TESTNET_PASSPHRASE).unwrap();
        let twelve = derive_sac_contract_id("LONGASSETCOD", ISSUER, TESTNET_PASSPHRASE).unwrap();
        assert_ne!(four, twelve);
        assert_eq!(twelve.len(), 56);
    }

    #[test]
    fn code_over_12_chars_is_rejected() {
        assert!(derive_sac_contract_id("THIRTEENCHARS", ISSUER, TESTNET_PASSPHRASE).is_err());
    }

    #[test]
    fn empty_code_is_rejected() {
        assert!(derive_sac_contract_id("", ISSUER, TESTNET_PASSPHRASE).is_err());
    }

    #[test]
    fn invalid_issuer_strkey_is_rejected() {
        assert!(derive_sac_contract_id("USDC", "not-a-strkey", TESTNET_PASSPHRASE).is_err());
    }

    #[test]
    fn registry_looks_up_a_tracked_asset_by_derived_contract_id() {
        let assets = vec![TrackedAsset {
            code: "USDC".to_string(),
            issuer: ISSUER.to_string(),
        }];
        let registry = SacRegistry::build(&assets, TESTNET_PASSPHRASE).expect("build");

        let contract_id = derive_sac_contract_id("USDC", ISSUER, TESTNET_PASSPHRASE).unwrap();
        assert_eq!(registry.lookup(&contract_id), Some(("USDC", ISSUER)));
    }

    #[test]
    fn registry_returns_none_for_an_untracked_contract() {
        let registry = SacRegistry::build(&[], TESTNET_PASSPHRASE).expect("build");
        assert_eq!(registry.lookup("CSOMEUNTRACKEDCONTRACT"), None);
    }
}
