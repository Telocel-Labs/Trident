use serde_json::{json, Value as Json};
use tracing::debug;

use super::{scaddress_to_string, scval_to_string};
use stellar_xdr::curr::ScVal;

/// The NFT event kinds emitted by the reference NFT contract (issue #275).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NftEventType {
    Mint,
    Transfer,
}

impl NftEventType {
    pub fn as_str(self) -> &'static str {
        match self {
            NftEventType::Mint => "mint",
            NftEventType::Transfer => "transfer",
        }
    }
}

/// A decoded NFT event with named fields, ready to persist into the
/// `nft_events` projection (issue #275).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NftEvent {
    pub event_type: NftEventType,
    /// Minting admin (present on `mint` only).
    pub admin: Option<String>,
    /// Source of the NFT (`transfer.from`).
    pub from: Option<String>,
    /// Destination of the NFT (`mint.to` / `transfer.to`).
    pub to: Option<String>,
    /// Numeric token identifier.
    pub token_id: u64,
    /// Metadata URI (present on `mint` only; carried in the event body).
    pub uri: Option<String>,
}

/// Decode an NFT event into typed named fields (issue #275).
///
/// Topic layout:
/// - `mint`:     `[Symbol("mint"),     Address(admin), Address(to),   U64(token_id)]`  body = `String(uri)`
/// - `transfer`: `[Symbol("transfer"), Address(from),  Address(to),   U64(token_id)]`  body = `Void`
///
/// Returns `None` for anything that is not one of the two NFT events, or
/// whose payload does not match the expected layout.
pub fn decode_nft_event(topics: &[ScVal], data: &ScVal) -> Option<NftEvent> {
    let name = match topics.first() {
        Some(ScVal::Symbol(s)) => s.to_utf8_string_lossy(),
        _ => return None,
    };

    let result = match name.as_str() {
        "mint" => typed_nft_mint(topics, data),
        "transfer" => typed_nft_transfer(topics, data),
        _ => return None,
    };

    match result {
        Ok(ev) => Some(ev),
        Err(msg) => {
            debug!("nft_event: malformed {} projection: {}", name, msg);
            None
        }
    }
}

/// Decode an NFT event and return it as a `serde_json::Value` (issue #275).
#[allow(dead_code)]
pub fn try_decode_nft_event(topics: &[ScVal], data: &ScVal) -> Option<Json> {
    let ev = decode_nft_event(topics, data)?;
    Some(json!({
        "event": ev.event_type.as_str(),
        "admin": ev.admin,
        "from": ev.from,
        "to": ev.to,
        "token_id": ev.token_id,
        "uri": ev.uri,
    }))
}

fn typed_nft_mint(topics: &[ScVal], data: &ScVal) -> Result<NftEvent, String> {
    let admin = addr_topic(topics, 1, "mint.admin")?;
    let to = addr_topic(topics, 2, "mint.to")?;
    let token_id = u64_topic(topics, 3, "mint.token_id")?;
    let uri = string_data(data, "mint.uri")?;
    Ok(NftEvent {
        event_type: NftEventType::Mint,
        admin: Some(admin),
        from: None,
        to: Some(to),
        token_id,
        uri: Some(uri),
    })
}

fn typed_nft_transfer(topics: &[ScVal], data: &ScVal) -> Result<NftEvent, String> {
    let from = addr_topic(topics, 1, "transfer.from")?;
    let to = addr_topic(topics, 2, "transfer.to")?;
    let token_id = u64_topic(topics, 3, "transfer.token_id")?;
    // transfer body is Void — data field carries no payload
    let _ = data;
    Ok(NftEvent {
        event_type: NftEventType::Transfer,
        admin: None,
        from: Some(from),
        to: Some(to),
        token_id,
        uri: None,
    })
}

fn addr_topic(topics: &[ScVal], index: usize, field: &str) -> Result<String, String> {
    match topics.get(index) {
        Some(ScVal::Address(addr)) => Ok(scaddress_to_string(addr)),
        Some(other) => Err(format!(
            "{field}: expected Address, got {}",
            scval_to_string(other)
        )),
        None => Err(format!("{field}: topic[{index}] missing")),
    }
}

fn u64_topic(topics: &[ScVal], index: usize, field: &str) -> Result<u64, String> {
    match topics.get(index) {
        Some(ScVal::U64(n)) => Ok(*n),
        Some(ScVal::U32(n)) => Ok(*n as u64),
        Some(other) => Err(format!(
            "{field}: expected U64, got {}",
            scval_to_string(other)
        )),
        None => Err(format!("{field}: topic[{index}] missing")),
    }
}

fn string_data(val: &ScVal, field: &str) -> Result<String, String> {
    match val {
        ScVal::String(s) => Ok(s.to_utf8_string_lossy()),
        other => Err(format!(
            "{field}: expected String, got {}",
            scval_to_string(other)
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use stellar_xdr::curr::{
        AccountId, ContractId, Hash, PublicKey, ScAddress, ScString, ScSymbol, ScVal, StringM,
        Uint256,
    };

    fn sym(s: &str) -> ScVal {
        ScVal::Symbol(ScSymbol::try_from(s.to_string()).unwrap())
    }

    fn account_addr(seed: u8) -> ScVal {
        ScVal::Address(ScAddress::Account(AccountId(
            PublicKey::PublicKeyTypeEd25519(Uint256([seed; 32])),
        )))
    }

    fn contract_addr(seed: u8) -> ScVal {
        ScVal::Address(ScAddress::Contract(ContractId(Hash([seed; 32]))))
    }

    fn sc_string(s: &str) -> ScVal {
        // ScString wraps StringM<{u32::MAX}>; there is no From<String> impl,
        // so convert through the underlying byte buffer.
        ScVal::String(ScString(StringM::try_from(s.as_bytes().to_vec()).unwrap()))
    }

    #[test]
    fn mint_happy_path() {
        let topics = vec![
            sym("mint"),
            contract_addr(1),
            account_addr(2),
            ScVal::U64(42),
        ];
        let data = sc_string("ipfs://Qm123");
        let ev = decode_nft_event(&topics, &data).expect("decode");
        assert_eq!(ev.event_type, NftEventType::Mint);
        assert!(ev.admin.is_some());
        assert!(ev.to.is_some());
        assert_eq!(ev.token_id, 42);
        assert_eq!(ev.uri.as_deref(), Some("ipfs://Qm123"));
        assert!(ev.from.is_none());
    }

    #[test]
    fn transfer_happy_path() {
        let topics = vec![
            sym("transfer"),
            account_addr(1),
            account_addr(2),
            ScVal::U64(7),
        ];
        let ev = decode_nft_event(&topics, &ScVal::Void).expect("decode");
        assert_eq!(ev.event_type, NftEventType::Transfer);
        assert!(ev.from.is_some());
        assert!(ev.to.is_some());
        assert_eq!(ev.token_id, 7);
        assert!(ev.uri.is_none());
        assert!(ev.admin.is_none());
    }

    #[test]
    fn u32_token_id_accepted() {
        let topics = vec![
            sym("transfer"),
            account_addr(1),
            account_addr(2),
            ScVal::U32(99),
        ];
        let ev = decode_nft_event(&topics, &ScVal::Void).expect("decode");
        assert_eq!(ev.token_id, 99);
    }

    #[test]
    fn unknown_event_name_returns_none() {
        let topics = vec![sym("swap"), account_addr(1)];
        assert!(decode_nft_event(&topics, &ScVal::Void).is_none());
    }

    #[test]
    fn missing_token_id_returns_none() {
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        assert!(decode_nft_event(&topics, &ScVal::Void).is_none());
    }

    #[test]
    fn json_output_contains_all_fields() {
        let topics = vec![
            sym("mint"),
            contract_addr(1),
            account_addr(2),
            ScVal::U64(1),
        ];
        let out = try_decode_nft_event(&topics, &sc_string("ipfs://abc")).expect("decode");
        assert_eq!(out["event"], "mint");
        assert_eq!(out["token_id"], 1u64);
        assert_eq!(out["uri"], "ipfs://abc");
    }
}
