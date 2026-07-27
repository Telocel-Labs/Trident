use serde_json::{json, Value as Json};
use tracing::debug;

use super::{scaddress_to_string, scval_to_string};
use stellar_xdr::curr::ScVal;

/// The event kinds emitted by the NFT reference contract (issue #275).
///
/// `mint` carries the admin address, the recipient, the token ID, and a metadata
/// URI. `transfer` carries the sender, the recipient, and the token ID.
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
/// `nft_events` projection.
///
/// Layout mirrors the token event projection (issue #211) but uses `token_id`
/// (a u64) and `uri` in place of fungible-amount fields.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NftEvent {
    pub event_type: NftEventType,
    /// Authorising admin on `mint`.
    pub admin: Option<String>,
    /// Owner / sender of an existing token on `transfer`.
    pub owner: Option<String>,
    /// Recipient address on `mint` and `transfer`.
    pub to: Option<String>,
    /// Unique token identifier.
    pub token_id: Option<u64>,
    /// Metadata URI carried in the `mint` event body.
    pub uri: Option<String>,
}

impl NftEvent {
    fn new(event_type: NftEventType) -> Self {
        Self {
            event_type,
            admin: None,
            owner: None,
            to: None,
            token_id: None,
            uri: None,
        }
    }
}

/// Decode a standard NFT event into typed named fields (issue #275).
///
/// Returns `None` for anything that is not a recognised NFT event, or whose
/// payload does not match the NFT event layout. Any contract may emit a `mint`
/// topic with an unrelated shape; such an event must not be projected.
///
/// Expected topic layouts:
/// - `mint`:     `["mint",   admin: Address, to: Address,   token_id: U64]`
/// - `transfer`: `["transfer", from: Address, to: Address, token_id: U64]`
///
/// Data field:
/// - `mint`:     `uri: String`
/// - `transfer`: `Void`
pub fn decode_nft_event(topics: &[ScVal], data: &ScVal) -> Option<NftEvent> {
    let name = match topics.first() {
        Some(ScVal::Symbol(s)) => s.to_utf8_string_lossy(),
        _ => return None,
    };

    let decoded = match name.as_str() {
        "mint" => typed_mint(topics, data),
        "transfer" => typed_transfer(topics, data),
        _ => return None,
    };

    match decoded {
        Ok(event) => Some(event),
        Err(msg) => {
            debug!("nft_event: malformed {} projection: {}", name, msg);
            None
        }
    }
}

fn typed_mint(topics: &[ScVal], data: &ScVal) -> Result<NftEvent, String> {
    let mut event = NftEvent::new(NftEventType::Mint);
    event.admin = Some(addr_topic(topics, 1, "mint.admin")?);
    event.to = Some(addr_topic(topics, 2, "mint.to")?);
    event.token_id = Some(u64_topic(topics, 3, "mint.token_id")?);
    event.uri = Some(str_data(data, "mint.uri")?);
    Ok(event)
}

fn typed_transfer(topics: &[ScVal], data: &ScVal) -> Result<NftEvent, String> {
    let mut event = NftEvent::new(NftEventType::Transfer);
    event.owner = Some(addr_topic(topics, 1, "transfer.from")?);
    event.to = Some(addr_topic(topics, 2, "transfer.to")?);
    event.token_id = Some(u64_topic(topics, 3, "transfer.token_id")?);
    // transfer data is Void; tolerate anything so foreign contracts don't panic
    let _ = data;
    Ok(event)
}

/// Attempt to decode a known NFT event from decoded topics and data.
/// Returns `Some(structured_json)` on success, `None` on any failure (logs DEBUG).
pub fn try_decode_nft_event(topics: &[ScVal], data: &ScVal) -> Option<Json> {
    let name = match topics.first() {
        Some(ScVal::Symbol(s)) => s.to_utf8_string_lossy(),
        _ => {
            debug!("nft_event: topic[0] is not a Symbol");
            return None;
        }
    };

    let result = match name.as_str() {
        "mint" => decode_mint(topics, data),
        "transfer" => decode_transfer(topics, data),
        other => {
            debug!("nft_event: unknown event name {}", other);
            return None;
        }
    };

    match result {
        Ok(v) => Some(v),
        Err(msg) => {
            debug!("nft_event: malformed {} payload: {}", name, msg);
            None
        }
    }
}

fn decode_mint(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "mint.admin")?;
    let to = addr_topic(topics, 2, "mint.to")?;
    let token_id = u64_topic(topics, 3, "mint.token_id")?;
    let uri = str_data(data, "mint.uri")?;
    Ok(json!({ "event": "mint", "admin": admin, "to": to, "token_id": token_id, "uri": uri }))
}

fn decode_transfer(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let from = addr_topic(topics, 1, "transfer.from")?;
    let to = addr_topic(topics, 2, "transfer.to")?;
    let token_id = u64_topic(topics, 3, "transfer.token_id")?;
    let _ = data;
    Ok(json!({ "event": "transfer", "from": from, "to": to, "token_id": token_id }))
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
            "{field}: expected U64/U32, got {}",
            scval_to_string(other)
        )),
        None => Err(format!("{field}: topic[{index}] missing")),
    }
}

fn str_data(val: &ScVal, field: &str) -> Result<String, String> {
    match val {
        ScVal::String(s) => Ok(s.to_utf8_string_lossy()),
        ScVal::Bytes(b) => String::from_utf8(b.0.to_vec())
            .map_err(|_| format!("{field}: bytes are not valid UTF-8")),
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
        AccountId, ContractId, Hash, Limited, Limits, PublicKey, ScAddress, ScString, ScSymbol,
        ScVal, Uint256, WriteXdr,
    };

    fn xdr_b64(val: &ScVal) -> String {
        use base64::{engine::general_purpose::STANDARD, Engine};
        let mut buf = Vec::new();
        val.write_xdr(&mut Limited::new(&mut buf, Limits::none()))
            .expect("XDR encode");
        STANDARD.encode(buf)
    }

    fn sym(s: &str) -> ScVal {
        ScVal::Symbol(ScSymbol::try_from(s.to_string()).expect("symbol"))
    }

    fn account_addr(seed: u8) -> ScVal {
        ScVal::Address(ScAddress::Account(AccountId(
            PublicKey::PublicKeyTypeEd25519(Uint256([seed; 32])),
        )))
    }

    fn contract_addr(seed: u8) -> ScVal {
        ScVal::Address(ScAddress::Contract(ContractId(Hash([seed; 32]))))
    }

    fn u64_val(v: u64) -> ScVal {
        ScVal::U64(v)
    }

    fn str_val(s: &str) -> ScVal {
        ScVal::String(ScString::try_from(s.to_string()).expect("string"))
    }

    #[test]
    fn mint_happy_path() {
        let topics = vec![
            sym("mint"),
            account_addr(0xAA),
            account_addr(0xBB),
            u64_val(42),
        ];
        let out = try_decode_nft_event(&topics, &str_val("ipfs://QmFoo")).expect("decode");
        assert_eq!(out["event"], "mint");
        assert_eq!(out["token_id"], 42u64);
        assert_eq!(out["uri"], "ipfs://QmFoo");
        assert!(out["admin"].as_str().unwrap().len() > 10);
        assert!(out["to"].as_str().unwrap().len() > 10);
    }

    #[test]
    fn transfer_happy_path() {
        let topics = vec![
            sym("transfer"),
            account_addr(0x01),
            account_addr(0x02),
            u64_val(7),
        ];
        let out = try_decode_nft_event(&topics, &ScVal::Void).expect("decode");
        assert_eq!(out["event"], "transfer");
        assert_eq!(out["token_id"], 7u64);
        assert!(out["from"].as_str().unwrap().len() > 10);
        assert!(out["to"].as_str().unwrap().len() > 10);
    }

    #[test]
    fn transfer_accepts_contract_addresses() {
        let topics = vec![
            sym("transfer"),
            contract_addr(0xCC),
            account_addr(0xDD),
            u64_val(100),
        ];
        let out = try_decode_nft_event(&topics, &ScVal::Void).expect("decode");
        assert_eq!(out["event"], "transfer");
        assert!(out["from"].as_str().unwrap().starts_with('C'));
    }

    #[test]
    fn unknown_event_name_returns_none() {
        let topics = vec![sym("approve"), account_addr(1)];
        assert!(try_decode_nft_event(&topics, &ScVal::Void).is_none());
    }

    #[test]
    fn non_symbol_first_topic_returns_none() {
        assert!(try_decode_nft_event(&[ScVal::Bool(true)], &ScVal::Void).is_none());
    }

    #[test]
    fn mint_missing_token_id_returns_none() {
        let topics = vec![sym("mint"), account_addr(1), account_addr(2)];
        assert!(try_decode_nft_event(&topics, &str_val("ipfs://x")).is_none());
    }

    #[test]
    fn mint_wrong_data_type_returns_none() {
        let topics = vec![sym("mint"), account_addr(1), account_addr(2), u64_val(1)];
        assert!(try_decode_nft_event(&topics, &ScVal::Void).is_none());
    }

    #[test]
    fn typed_mint_populates_all_fields() {
        let topics = vec![
            sym("mint"),
            account_addr(0xAA),
            account_addr(0xBB),
            u64_val(999),
        ];
        let event = decode_nft_event(&topics, &str_val("ipfs://QmBar")).expect("decode");
        assert_eq!(event.event_type, NftEventType::Mint);
        assert!(event.admin.is_some());
        assert!(event.to.is_some());
        assert_eq!(event.token_id, Some(999));
        assert_eq!(event.uri.as_deref(), Some("ipfs://QmBar"));
        assert!(event.owner.is_none());
    }

    #[test]
    fn typed_transfer_populates_all_fields() {
        let topics = vec![
            sym("transfer"),
            account_addr(0x01),
            account_addr(0x02),
            u64_val(5),
        ];
        let event = decode_nft_event(&topics, &ScVal::Void).expect("decode");
        assert_eq!(event.event_type, NftEventType::Transfer);
        assert!(event.owner.is_some());
        assert!(event.to.is_some());
        assert_eq!(event.token_id, Some(5));
        assert!(event.uri.is_none());
        assert!(event.admin.is_none());
    }

    #[test]
    fn u32_token_id_accepted() {
        let topics = vec![
            sym("transfer"),
            account_addr(1),
            account_addr(2),
            ScVal::U32(77),
        ];
        let event = decode_nft_event(&topics, &ScVal::Void).expect("decode");
        assert_eq!(event.token_id, Some(77));
    }

    // XDR round-trip: encode a U64 token_id then decode it back through the
    // production parse path to confirm the full XDR → strkey pipeline is intact.
    #[test]
    fn xdr_round_trip_token_id() {
        let b64 = xdr_b64(&u64_val(12345));
        let decoded = super::super::decode_scval(&b64).expect("decode");
        let topics = vec![
            sym("transfer"),
            account_addr(1),
            account_addr(2),
            decoded,
        ];
        let event = decode_nft_event(&topics, &ScVal::Void).expect("decode");
        assert_eq!(event.token_id, Some(12345));
    }

    // XDR round-trip for the String URI field.
    #[test]
    fn xdr_round_trip_uri() {
        let uri = "ipfs://QmTestUri";
        let b64 = xdr_b64(&str_val(uri));
        let decoded_data = super::super::decode_scval(&b64).expect("decode");
        let topics = vec![
            sym("mint"),
            account_addr(0xAA),
            account_addr(0xBB),
            u64_val(1),
        ];
        let event = decode_nft_event(&topics, &decoded_data).expect("decode");
        assert_eq!(event.uri.as_deref(), Some(uri));
    }
}
