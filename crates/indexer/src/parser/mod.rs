//! # Parser
//!
//! Owns XDR decoding and event normalisation. Responsibilities:
//!
//! - Decoding raw base64-encoded XDR strings as returned by the Soroban RPC
//!   `getEvents` method into typed Rust values via the `stellar-xdr` crate.
//! - Normalising decoded `ScVal` topics into human-readable string representations
//!   and the event body into a `serde_json::Value` for storage and forwarding.
//! - Type coercion: Symbol/String → plain string, Address → strkey, I128/U128 →
//!   decimal string, Bool → "true"/"false", Bytes → hex, Map/Vec → JSON object/array.
//! - Returning `TridentError::ParseError` for any input that cannot be decoded so
//!   the caller (Streamer) can decide whether to skip or halt.

use base64::{engine::general_purpose::STANDARD, Engine};
use serde_json::Value as Json;
use stellar_strkey::{ed25519, Contract};
use stellar_xdr::curr::{
    AccountId, ContractId, Limited, Limits, PublicKey, ReadXdr, ScAddress, ScVal,
};
use trident_common::{EventType, SorobanEvent, TridentError};

use crate::rpc::RawEvent;

pub mod invocation_metrics;
pub mod nft_events;
pub mod token_events;

use nft_events::NftEvent;
use token_events::TokenEvent;

/// A normalised event together with its optional typed projections.
///
/// `token` is populated for standard SEP-41 / SAC value-movement events
/// (issue #211). `nft` is populated for NFT mint/transfer events (issue #275).
pub struct ParsedEvent {
    pub event: SorobanEvent,
    /// `Some` only for standard SEP-41 / SAC value-movement events whose payload
    /// matches the token interface layout.
    pub token: Option<TokenEvent>,
    /// `Some` only for NFT mint/transfer events whose payload matches the NFT
    /// contract event layout (issue #275).
    pub nft: Option<NftEvent>,
}

pub struct Parser {
    pub index_diagnostic: bool,
}

impl Parser {
    pub fn new(index_diagnostic: bool) -> Self {
        Self { index_diagnostic }
    }

    /// Decode a raw RPC event into a normalised `SorobanEvent` plus, when the
    /// payload matches the standard token interface, a typed token projection
    /// (issue #211).
    ///
    /// The topic and body `ScVal`s are decoded once and reused for both outputs,
    /// so the projection costs no extra XDR work.
    pub fn parse_event_with_projection(
        &self,
        raw: &RawEvent,
    ) -> Result<Option<ParsedEvent>, TridentError> {
        let event_type = parse_event_type(&raw.event_type)?;

        if event_type == EventType::Diagnostic && !self.index_diagnostic {
            return Ok(None);
        }

        // Skip events emitted by failed contract calls — they have no observable effect.
        if !raw.in_successful_contract_call {
            return Ok(None);
        }

        let contract_id = raw.contract_id.clone().unwrap_or_default();

        let topic_vals: Vec<ScVal> = raw
            .topic
            .iter()
            .map(|xdr| decode_scval(xdr))
            .collect::<Result<_, _>>()?;
        let topics: Vec<String> = topic_vals.iter().map(scval_to_string).collect();

        let (data, data_val) = if raw.value.is_empty() {
            (Json::Null, ScVal::Void)
        } else {
            let val = decode_scval(&raw.value)?;
            (scval_to_json(&val), val)
        };

        let token = token_events::decode_token_event(&topic_vals, &data_val);
        let nft = nft_events::decode_nft_event(&topic_vals, &data_val);

        let ledger_sequence: u64 = raw
            .ledger
            .parse()
            .map_err(|_| TridentError::parse(anyhow::anyhow!("invalid ledger: {}", raw.ledger)))?;

        // event_index is the second component of the opaque id string: "{encoded}-{index}"
        let event_index: u32 = raw
            .id
            .split('-')
            .next_back()
            .and_then(|s| s.parse().ok())
            .unwrap_or(0);

        Ok(Some(ParsedEvent {
            event: SorobanEvent {
                contract_id,
                topics,
                data,
                ledger_sequence,
                ledger_timestamp: raw.ledger_closed_at.clone(),
                transaction_hash: raw.tx_hash.clone(),
                event_index,
                event_type,
            },
            token,
            nft,
        }))
    }
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

fn parse_event_type(raw: &str) -> Result<EventType, TridentError> {
    match raw {
        "contract" => Ok(EventType::Contract),
        "system" => Ok(EventType::System),
        "diagnostic" => Ok(EventType::Diagnostic),
        other => Err(TridentError::parse(anyhow::anyhow!(
            "unknown event type: {other}"
        ))),
    }
}

pub(crate) fn decode_scval(b64: &str) -> Result<ScVal, TridentError> {
    let bytes = STANDARD
        .decode(b64)
        .map_err(|e| TridentError::parse(anyhow::Error::new(e).context("base64 decode")))?;
    let mut cursor = std::io::Cursor::new(bytes);
    ScVal::read_xdr(&mut Limited::new(&mut cursor, Limits::none()))
        .map_err(|e| TridentError::parse(anyhow::Error::new(e).context("XDR decode ScVal")))
}

/// Convert a topic `ScVal` to a compact string representation.
pub fn scval_to_string(val: &ScVal) -> String {
    match val {
        ScVal::Symbol(s) => s.to_utf8_string_lossy(),
        ScVal::String(s) => s.to_utf8_string_lossy(),
        ScVal::Bool(b) => b.to_string(),
        ScVal::Void => "void".into(),
        ScVal::U32(n) => n.to_string(),
        ScVal::I32(n) => n.to_string(),
        ScVal::U64(n) => n.to_string(),
        ScVal::I64(n) => n.to_string(),
        ScVal::U128(parts) => {
            let val = ((parts.hi as u128) << 64) | (parts.lo as u128);
            val.to_string()
        }
        ScVal::I128(parts) => {
            let val = ((parts.hi as i128) << 64) | (parts.lo as i128);
            val.to_string()
        }
        ScVal::U256(parts) => format!(
            "u256({:x}{:x}{:x}{:x})",
            parts.hi_hi, parts.hi_lo, parts.lo_hi, parts.lo_lo
        ),
        ScVal::I256(parts) => format!(
            "i256({:x}{:x}{:x}{:x})",
            parts.hi_hi, parts.hi_lo, parts.lo_hi, parts.lo_lo
        ),
        ScVal::Bytes(b) => hex::encode(b.as_slice()),
        ScVal::Address(addr) => scaddress_to_string(addr),
        // For complex types in topic position, fall back to debug representation
        other => format!("{other:?}"),
    }
}

/// Recursively convert a `ScVal` to a `serde_json::Value` for the event body.
pub fn scval_to_json(val: &ScVal) -> Json {
    match val {
        ScVal::Void => Json::Null,
        ScVal::Bool(b) => Json::Bool(*b),
        ScVal::Symbol(s) => Json::String(s.to_utf8_string_lossy()),
        ScVal::String(s) => Json::String(s.to_utf8_string_lossy()),
        ScVal::U32(n) => Json::from(*n),
        ScVal::I32(n) => Json::from(*n),
        ScVal::U64(n) => Json::from(*n),
        ScVal::I64(n) => Json::from(*n),
        ScVal::U128(parts) => {
            let v = ((parts.hi as u128) << 64) | (parts.lo as u128);
            // Use string for values that overflow JSON's safe integer range
            if v <= u64::MAX as u128 {
                Json::from(v as u64)
            } else {
                Json::String(v.to_string())
            }
        }
        ScVal::I128(parts) => {
            let v = ((parts.hi as i128) << 64) | (parts.lo as i128);
            if v >= i64::MIN as i128 && v <= i64::MAX as i128 {
                Json::from(v as i64)
            } else {
                Json::String(v.to_string())
            }
        }
        ScVal::Bytes(b) => Json::String(hex::encode(b.as_slice())),
        ScVal::Address(addr) => Json::String(scaddress_to_string(addr)),
        ScVal::Vec(Some(items)) => Json::Array(items.iter().map(scval_to_json).collect()),
        ScVal::Vec(None) => Json::Array(vec![]),
        ScVal::Map(Some(entries)) => {
            let obj: serde_json::Map<String, Json> = entries
                .iter()
                .map(|e| (scval_to_string(&e.key), scval_to_json(&e.val)))
                .collect();
            Json::Object(obj)
        }
        ScVal::Map(None) => Json::Object(serde_json::Map::new()),
        other => Json::String(format!("{other:?}")),
    }
}

pub(crate) fn scaddress_to_string(addr: &ScAddress) -> String {
    match addr {
        ScAddress::Account(AccountId(PublicKey::PublicKeyTypeEd25519(bytes))) => {
            // stellar-strkey 0.0.16+ returns heapless::String — convert to std::String
            ed25519::PublicKey(bytes.0).to_string().as_str().to_owned()
        }
        // stellar-xdr 26.x wraps the hash in ContractId; the inner Hash holds [u8; 32]
        ScAddress::Contract(ContractId(hash)) => Contract(hash.0).to_string().as_str().to_owned(),
        // stellar-xdr 26.x added MuxedAccount, ClaimableBalance, LiquidityPool variants;
        // these do not appear in Soroban contract events but the match must be exhaustive.
        other => format!("{other:?}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::{engine::general_purpose::STANDARD, Engine};
    use stellar_xdr::curr::{
        AccountId, ContractId, Hash, Int128Parts, Limited, Limits, PublicKey, ScAddress, ScMap,
        ScMapEntry, ScSymbol, ScVal, Uint256, VecM, WriteXdr,
    };

    use crate::rpc::RawEvent;

    fn xdr_b64(val: &ScVal) -> String {
        let mut buf = Vec::new();
        val.write_xdr(&mut Limited::new(&mut buf, Limits::none()))
            .expect("XDR encode failed");
        STANDARD.encode(buf)
    }

    fn sym(s: &str) -> ScVal {
        ScVal::Symbol(ScSymbol::try_from(s.to_string()).expect("symbol too long"))
    }

    fn make_event(
        event_type: &str,
        contract_id: Option<&str>,
        topics: Vec<ScVal>,
        value: ScVal,
        successful: bool,
    ) -> RawEvent {
        RawEvent {
            event_type: event_type.to_string(),
            ledger: "500".to_string(),
            ledger_closed_at: "2024-06-01T00:00:00Z".to_string(),
            contract_id: contract_id.map(str::to_string),
            id: "0000000000500000-0".to_string(),
            paging_token: "token1".to_string(),
            tx_hash: "deadbeefdeadbeef".to_string(),
            topic: topics.iter().map(xdr_b64).collect(),
            value: xdr_b64(&value),
            in_successful_contract_call: successful,
        }
    }

    #[test]
    fn token_transfer_topics_and_amount_decoded() {
        // Standard SEP-41 transfer: topics=[Symbol("transfer"), Addr(from), Addr(to)], data=I128(amount)
        let from = ScVal::Address(ScAddress::Account(AccountId(
            PublicKey::PublicKeyTypeEd25519(Uint256([1u8; 32])),
        )));
        let to = ScVal::Address(ScAddress::Account(AccountId(
            PublicKey::PublicKeyTypeEd25519(Uint256([2u8; 32])),
        )));
        let amount = ScVal::I128(Int128Parts {
            hi: 0,
            lo: 1_000_000,
        });

        let contract_id = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM";
        let raw = make_event(
            "contract",
            Some(contract_id),
            vec![sym("transfer"), from, to],
            amount,
            true,
        );

        let parser = Parser::new(false);
        let event = parser
            .parse_event_with_projection(&raw)
            .unwrap()
            .unwrap()
            .event;

        assert_eq!(event.contract_id, contract_id);
        assert_eq!(event.topics[0], "transfer");
        assert_eq!(event.data, serde_json::json!(1_000_000u64));
        assert_eq!(event.ledger_sequence, 500);
        assert_eq!(event.transaction_hash, "deadbeefdeadbeef");
    }

    #[test]
    fn mint_event_symbol_and_address_topics() {
        let to = ScVal::Address(ScAddress::Account(AccountId(
            PublicKey::PublicKeyTypeEd25519(Uint256([3u8; 32])),
        )));
        let amount = ScVal::I128(Int128Parts { hi: 0, lo: 5_000 });

        let raw = make_event(
            "contract",
            Some("CONTRACT"),
            vec![sym("mint"), to],
            amount,
            true,
        );

        let parser = Parser::new(false);
        let event = parser
            .parse_event_with_projection(&raw)
            .unwrap()
            .unwrap()
            .event;

        assert_eq!(event.topics[0], "mint");
        assert_eq!(event.data, serde_json::json!(5_000u64));
    }

    #[test]
    fn transfer_event_yields_a_token_projection() {
        let from = ScVal::Address(ScAddress::Account(AccountId(
            PublicKey::PublicKeyTypeEd25519(Uint256([1u8; 32])),
        )));
        let to = ScVal::Address(ScAddress::Account(AccountId(
            PublicKey::PublicKeyTypeEd25519(Uint256([2u8; 32])),
        )));
        let amount = ScVal::I128(Int128Parts { hi: 0, lo: 7_500 });

        let raw = make_event(
            "contract",
            Some("CTOKEN"),
            vec![sym("transfer"), from, to],
            amount,
            true,
        );

        let parsed = Parser::new(false)
            .parse_event_with_projection(&raw)
            .unwrap()
            .unwrap();
        let token = parsed.token.expect("transfer must produce a projection");
        assert_eq!(token.amount.as_deref(), Some("7500"));
        assert_eq!(parsed.event.contract_id, "CTOKEN");
    }

    #[test]
    fn non_token_event_yields_no_projection() {
        let raw = make_event(
            "contract",
            Some("CAPP"),
            vec![sym("swap")],
            ScVal::Void,
            true,
        );
        let parsed = Parser::new(false)
            .parse_event_with_projection(&raw)
            .unwrap()
            .unwrap();
        assert!(
            parsed.token.is_none(),
            "a non-token event must not be projected"
        );
    }

    #[test]
    fn map_event_data_decodes_to_json_object() {
        let entries: Vec<ScMapEntry> = vec![
            ScMapEntry {
                key: ScVal::Symbol(ScSymbol::try_from("amount".to_string()).unwrap()),
                val: ScVal::I128(Int128Parts { hi: 0, lo: 100 }),
            },
            ScMapEntry {
                key: ScVal::Symbol(ScSymbol::try_from("fee".to_string()).unwrap()),
                val: ScVal::I128(Int128Parts { hi: 0, lo: 1 }),
            },
        ];
        let map_val = ScVal::Map(Some(ScMap(VecM::try_from(entries).unwrap())));
        let raw = make_event("contract", None, vec![sym("custom")], map_val, true);

        let parser = Parser::new(false);
        let event = parser
            .parse_event_with_projection(&raw)
            .unwrap()
            .unwrap()
            .event;

        let obj = event
            .data
            .as_object()
            .expect("data should be a JSON object");
        assert_eq!(obj["amount"], serde_json::json!(100u64));
        assert_eq!(obj["fee"], serde_json::json!(1u64));
    }

    #[test]
    fn diagnostic_event_skipped_when_index_diagnostic_false() {
        let raw = make_event("diagnostic", None, vec![sym("debug")], ScVal::Void, true);
        let parser = Parser::new(false);
        assert!(
            parser.parse_event_with_projection(&raw).unwrap().is_none(),
            "diagnostic events must be skipped when index_diagnostic=false"
        );
    }

    #[test]
    fn diagnostic_event_included_when_index_diagnostic_true() {
        let raw = make_event("diagnostic", None, vec![sym("debug")], ScVal::Void, true);
        let parser = Parser::new(true);
        assert!(
            parser.parse_event_with_projection(&raw).unwrap().is_some(),
            "diagnostic events must be indexed when index_diagnostic=true"
        );
    }

    #[test]
    fn failed_contract_call_filtered() {
        let raw = make_event("contract", None, vec![sym("transfer")], ScVal::Void, false);
        let parser = Parser::new(false);
        assert!(
            parser.parse_event_with_projection(&raw).unwrap().is_none(),
            "events from failed contract calls must be filtered out"
        );
    }

    #[test]
    fn contract_address_topic_decoded_to_strkey() {
        let contract_hash = [0xABu8; 32];
        let addr = ScVal::Address(ScAddress::Contract(ContractId(Hash(contract_hash))));
        let raw = make_event(
            "contract",
            None,
            vec![sym("event"), addr],
            ScVal::Void,
            true,
        );

        let parser = Parser::new(false);
        let event = parser
            .parse_event_with_projection(&raw)
            .unwrap()
            .unwrap()
            .event;

        assert!(
            event.topics[1].starts_with('C'),
            "contract strkey must start with C, got: {}",
            event.topics[1]
        );
        assert_eq!(
            event.topics[1].len(),
            56,
            "contract strkey must be 56 chars"
        );
    }
}
