use serde_json::{json, Value as Json};
use tracing::debug;

use super::{scaddress_to_string, scval_to_string};
use stellar_xdr::curr::ScVal;

/// The standard SEP-41 / Stellar-Asset-Contract event kinds that get a
/// first-class normalised projection (issue #211).
///
/// Administrative events (`set_admin`, `set_authorized`, `increase_supply`) are
/// still decoded into JSON but move no value, so they are not part of the
/// transfer-analytics projection.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TokenEventType {
    Transfer,
    Mint,
    Burn,
    Clawback,
    Approve,
}

impl TokenEventType {
    pub fn as_str(self) -> &'static str {
        match self {
            TokenEventType::Transfer => "transfer",
            TokenEventType::Mint => "mint",
            TokenEventType::Burn => "burn",
            TokenEventType::Clawback => "clawback",
            TokenEventType::Approve => "approve",
        }
    }
}

/// A decoded token event with named fields, ready to persist into the
/// `token_events` projection.
///
/// `amount` is a decimal string, never a number: token amounts are i128 and
/// would lose precision through any JSON or 64-bit numeric path (issue #210).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TokenEvent {
    pub event_type: TokenEventType,
    /// Source of funds: `transfer.from`, `burn.from`, `clawback.from`, `approve.from`.
    pub from: Option<String>,
    /// Destination of funds: `transfer.to`, `mint.to`.
    pub to: Option<String>,
    /// Delegated spender on `approve`.
    pub spender: Option<String>,
    /// Authorising admin on `mint` and `clawback`.
    pub admin: Option<String>,
    pub amount: Option<String>,
    /// Ledger at which an `approve` allowance expires.
    pub expiration_ledger: Option<i64>,
    /// Asset code, populated only when the emitting contract is a recognised
    /// Stellar Asset Contract (SAC) instance (issue #262).
    pub asset_code: Option<String>,
    /// Asset issuer strkey, populated alongside `asset_code`. Absent for the
    /// native XLM SAC, which has no issuer.
    pub asset_issuer: Option<String>,
}

impl TokenEvent {
    fn new(event_type: TokenEventType) -> Self {
        Self {
            event_type,
            from: None,
            to: None,
            spender: None,
            admin: None,
            amount: None,
            expiration_ledger: None,
            asset_code: None,
            asset_issuer: None,
        }
    }
}

/// Decode a standard token event into typed named fields (issue #211).
///
/// Returns `None` for anything that is not one of the five value-movement token
/// events, or whose payload does not match the token interface layout — any
/// contract is free to emit a `transfer` topic with an unrelated shape, and such
/// an event must not be projected as a token transfer.
pub fn decode_token_event(topics: &[ScVal], data: &ScVal) -> Option<TokenEvent> {
    let name = match topics.first() {
        Some(ScVal::Symbol(s)) => s.to_utf8_string_lossy(),
        _ => return None,
    };

    let decoded = match name.as_str() {
        "transfer" => typed_transfer(topics, data),
        "mint" => typed_mint(topics, data),
        "burn" => typed_burn(topics, data),
        "clawback" => typed_clawback(topics, data),
        "approve" => typed_approve(topics, data),
        _ => return None,
    };

    match decoded {
        Ok(event) => Some(event),
        Err(msg) => {
            debug!("token_event: malformed {} projection: {}", name, msg);
            None
        }
    }
}

fn typed_transfer(topics: &[ScVal], data: &ScVal) -> Result<TokenEvent, String> {
    let mut event = TokenEvent::new(TokenEventType::Transfer);
    event.from = Some(addr_topic(topics, 1, "transfer.from")?);
    event.to = Some(addr_topic(topics, 2, "transfer.to")?);
    event.amount = Some(i128_data(data, "transfer.amount")?);
    Ok(event)
}

fn typed_mint(topics: &[ScVal], data: &ScVal) -> Result<TokenEvent, String> {
    let mut event = TokenEvent::new(TokenEventType::Mint);
    event.admin = Some(addr_topic(topics, 1, "mint.admin")?);
    event.to = Some(addr_topic(topics, 2, "mint.to")?);
    event.amount = Some(i128_data(data, "mint.amount")?);
    Ok(event)
}

fn typed_burn(topics: &[ScVal], data: &ScVal) -> Result<TokenEvent, String> {
    let mut event = TokenEvent::new(TokenEventType::Burn);
    event.from = Some(addr_topic(topics, 1, "burn.from")?);
    event.amount = Some(i128_data(data, "burn.amount")?);
    Ok(event)
}

fn typed_clawback(topics: &[ScVal], data: &ScVal) -> Result<TokenEvent, String> {
    let mut event = TokenEvent::new(TokenEventType::Clawback);
    event.admin = Some(addr_topic(topics, 1, "clawback.admin")?);
    event.from = Some(addr_topic(topics, 2, "clawback.from")?);
    event.amount = Some(i128_data(data, "clawback.amount")?);
    Ok(event)
}

/// `approve` carries two values, so its body is a two-element vector of
/// `[amount, expiration_ledger]` rather than a bare i128.
fn typed_approve(topics: &[ScVal], data: &ScVal) -> Result<TokenEvent, String> {
    let mut event = TokenEvent::new(TokenEventType::Approve);
    event.from = Some(addr_topic(topics, 1, "approve.from")?);
    event.spender = Some(addr_topic(topics, 2, "approve.spender")?);

    let items = match data {
        ScVal::Vec(Some(items)) => items,
        other => {
            return Err(format!(
                "approve body expected Vec[amount, expiration_ledger], got {}",
                scval_to_string(other)
            ))
        }
    };
    if items.len() != 2 {
        return Err(format!(
            "approve body expected 2 elements, got {}",
            items.len()
        ));
    }

    event.amount = Some(i128_data(&items[0], "approve.amount")?);
    event.expiration_ledger = Some(match &items[1] {
        ScVal::U32(n) => *n as i64,
        ScVal::U64(n) => *n as i64,
        other => {
            return Err(format!(
                "approve.expiration_ledger expected U32, got {}",
                scval_to_string(other)
            ))
        }
    });

    Ok(event)
}

/// Attempt to decode a known SEP-41 token event from decoded topics and data.
/// Returns Some(structured_json) on success, None on any failure (logs DEBUG).
#[allow(dead_code)] // Functions are used in tests
pub fn try_decode_token_event(topics: &[ScVal], data: &ScVal) -> Option<Json> {
    let name = match topics.first() {
        Some(ScVal::Symbol(s)) => s.to_utf8_string_lossy(),
        _ => {
            debug!("token_event: topic[0] is not a Symbol");
            return None;
        }
    };

    let result = match name.as_str() {
        "transfer" => decode_transfer(topics, data),
        "mint" => decode_mint(topics, data),
        "burn" => decode_burn(topics, data),
        "clawback" => decode_clawback(topics, data),
        "approve" => decode_approve(topics, data),
        "set_admin" => decode_set_admin(topics, data),
        "set_authorized" => decode_set_authorized(topics, data),
        "increase_supply" => decode_increase_supply(topics, data),
        other => {
            debug!("token_event: unknown event name {}", other);
            return None;
        }
    };

    match result {
        Ok(v) => Some(v),
        Err(msg) => {
            debug!("token_event: malformed {} payload: {}", name, msg);
            None
        }
    }
}

fn decode_transfer(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let from = addr_topic(topics, 1, "transfer.from")?;
    let to = addr_topic(topics, 2, "transfer.to")?;
    let amount = i128_data(data, "transfer.amount")?;
    Ok(json!({ "event": "transfer", "from": from, "to": to, "amount": amount }))
}

#[allow(dead_code)]
fn decode_mint(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "mint.admin")?;
    let to = addr_topic(topics, 2, "mint.to")?;
    let amount = i128_data(data, "mint.amount")?;
    Ok(json!({ "event": "mint", "admin": admin, "to": to, "amount": amount }))
}

#[allow(dead_code)]
fn decode_burn(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let from = addr_topic(topics, 1, "burn.from")?;
    let amount = i128_data(data, "burn.amount")?;
    Ok(json!({ "event": "burn", "from": from, "amount": amount }))
}

#[allow(dead_code)]
fn decode_clawback(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "clawback.admin")?;
    let from = addr_topic(topics, 2, "clawback.from")?;
    let amount = i128_data(data, "clawback.amount")?;
    Ok(json!({ "event": "clawback", "admin": admin, "from": from, "amount": amount }))
}

#[allow(dead_code)]
fn decode_approve(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let event = typed_approve(topics, data)?;
    Ok(json!({
        "event": "approve",
        "from": event.from,
        "spender": event.spender,
        "amount": event.amount,
        "expiration_ledger": event.expiration_ledger,
    }))
}

#[allow(dead_code)]
fn decode_set_admin(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "set_admin.admin")?;
    let new_admin = addr_scval(data, "set_admin.new_admin")?;
    Ok(json!({ "event": "set_admin", "admin": admin, "new_admin": new_admin }))
}

#[allow(dead_code)]
fn decode_set_authorized(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "set_authorized.admin")?;
    let id = addr_topic(topics, 2, "set_authorized.id")?;
    let authorize = match data {
        ScVal::Bool(b) => *b,
        other => {
            return Err(format!(
                "set_authorized.authorize expected Bool, got {}",
                scval_to_string(other)
            ))
        }
    };
    Ok(json!({ "event": "set_authorized", "admin": admin, "id": id, "authorize": authorize }))
}

#[allow(dead_code)]
fn decode_increase_supply(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "increase_supply.admin")?;
    let amount = i128_data(data, "increase_supply.amount")?;
    Ok(json!({ "event": "increase_supply", "admin": admin, "amount": amount }))
}

#[allow(dead_code)]
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

#[allow(dead_code)]
fn addr_scval(val: &ScVal, field: &str) -> Result<String, String> {
    match val {
        ScVal::Address(addr) => Ok(scaddress_to_string(addr)),
        other => Err(format!(
            "{field}: expected Address, got {}",
            scval_to_string(other)
        )),
    }
}

/// i128 values are always stored as JSON strings to preserve full precision.
#[allow(dead_code)]
fn i128_data(val: &ScVal, field: &str) -> Result<String, String> {
    match val {
        ScVal::I128(parts) => {
            let v = ((parts.hi as i128) << 64) | (parts.lo as i128);
            Ok(v.to_string())
        }
        other => Err(format!(
            "{field}: expected I128, got {}",
            scval_to_string(other)
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::{engine::general_purpose::STANDARD, Engine};
    use stellar_xdr::curr::{
        AccountId, ContractId, Hash, Int128Parts, Limited, Limits, PublicKey, ScAddress, ScSymbol,
        ScVal, Uint256, WriteXdr,
    };

    fn xdr_b64(val: &ScVal) -> String {
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

    fn i128_val(v: i128) -> ScVal {
        ScVal::I128(Int128Parts {
            hi: (v >> 64) as i64,
            lo: v as u64,
        })
    }

    #[test]
    fn transfer_happy_path() {
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        let out = try_decode_token_event(&topics, &i128_val(1_000_000)).expect("decode");
        assert_eq!(out["event"], "transfer");
        assert_eq!(out["amount"], "1000000");
        assert!(out["from"].as_str().unwrap().len() > 10);
        assert!(out["to"].as_str().unwrap().len() > 10);
    }

    #[test]
    fn transfer_large_i128_as_string() {
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        let out = try_decode_token_event(&topics, &i128_val(i128::MAX)).expect("decode");
        assert_eq!(out["amount"], i128::MAX.to_string());
    }

    #[test]
    fn mint_happy_path() {
        let topics = vec![sym("mint"), account_addr(0xAA), account_addr(0xBB)];
        let out = try_decode_token_event(&topics, &i128_val(5_000)).expect("decode");
        assert_eq!(out["event"], "mint");
        assert_eq!(out["amount"], "5000");
    }

    #[test]
    fn burn_happy_path() {
        let topics = vec![sym("burn"), account_addr(1)];
        let out = try_decode_token_event(&topics, &i128_val(250)).expect("decode");
        assert_eq!(out["event"], "burn");
        assert_eq!(out["amount"], "250");
    }

    #[test]
    fn clawback_happy_path() {
        let topics = vec![sym("clawback"), account_addr(0xAA), account_addr(0xBB)];
        let out = try_decode_token_event(&topics, &i128_val(999)).expect("decode");
        assert_eq!(out["event"], "clawback");
        assert_eq!(out["amount"], "999");
    }

    #[test]
    fn set_admin_happy_path() {
        let topics = vec![sym("set_admin"), account_addr(1)];
        let new_admin = contract_addr(0xFF);
        let out = try_decode_token_event(&topics, &new_admin).expect("decode");
        assert_eq!(out["event"], "set_admin");
        assert!(out["new_admin"].as_str().unwrap().starts_with("C"));
    }

    #[test]
    fn set_authorized_happy_path() {
        let topics = vec![sym("set_authorized"), account_addr(1), account_addr(2)];
        let out = try_decode_token_event(&topics, &ScVal::Bool(true)).expect("decode");
        assert_eq!(out["event"], "set_authorized");
        assert_eq!(out["authorize"], true);
    }

    #[test]
    fn increase_supply_happy_path() {
        let topics = vec![sym("increase_supply"), account_addr(0xCC)];
        let out = try_decode_token_event(&topics, &i128_val(100_000_000)).expect("decode");
        assert_eq!(out["event"], "increase_supply");
        assert_eq!(out["amount"], "100000000");
    }

    #[test]
    fn malformed_transfer_missing_to_returns_none() {
        let topics = vec![sym("transfer"), account_addr(1)];
        assert!(try_decode_token_event(&topics, &i128_val(100)).is_none());
    }

    #[test]
    fn malformed_transfer_wrong_data_type_returns_none() {
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        assert!(try_decode_token_event(&topics, &ScVal::Bool(true)).is_none());
    }

    #[test]
    fn unknown_event_name_returns_none() {
        let topics = vec![sym("custom_event"), account_addr(1)];
        assert!(try_decode_token_event(&topics, &ScVal::Void).is_none());
    }

    #[test]
    fn non_symbol_first_topic_returns_none() {
        assert!(try_decode_token_event(&[ScVal::Bool(true)], &ScVal::Void).is_none());
    }

    #[test]
    fn empty_topics_returns_none() {
        assert!(try_decode_token_event(&[], &ScVal::Void).is_none());
    }

    #[test]
    fn negative_i128_transfer_amount() {
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        let out = try_decode_token_event(&topics, &i128_val(-1)).expect("decode");
        assert_eq!(out["amount"], "-1");
    }

    #[test]
    fn xdr_round_trip_via_parser_decode() {
        use super::super::decode_scval;
        let b64 = xdr_b64(&i128_val(42_000_000));
        let decoded = decode_scval(&b64).expect("decode");
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        let out = try_decode_token_event(&topics, &decoded).expect("typed decode");
        assert_eq!(out["amount"], "42000000");
    }

    // -----------------------------------------------------------------------
    // Golden fixtures (issue #211)
    //
    // Each fixture is a wire-format event — base64 XDR topics and body, exactly
    // as getEvents returns them — decoded through the production path.
    // -----------------------------------------------------------------------

    /// Decode a fixture and assert every expected field, including that fields
    /// the event should not carry are absent.
    fn assert_fixture(raw: &str) {
        let fixture: serde_json::Value = serde_json::from_str(raw).expect("fixture JSON");

        let topics: Vec<ScVal> = fixture["topics"]
            .as_array()
            .expect("topics array")
            .iter()
            .map(|t| super::super::decode_scval(t.as_str().unwrap()).expect("topic XDR"))
            .collect();
        let data = super::super::decode_scval(fixture["data"].as_str().unwrap()).expect("body XDR");

        let expected = &fixture["expected"];
        let decoded = decode_token_event(&topics, &data)
            .unwrap_or_else(|| panic!("fixture {} must decode", fixture["name"]));

        assert_eq!(
            decoded.event_type.as_str(),
            expected["event_type"].as_str().unwrap()
        );

        let expect_field = |actual: &Option<String>, key: &str| {
            let want = expected.get(key).and_then(|v| v.as_str());
            assert_eq!(
                actual.as_deref(),
                want,
                "field {key} mismatch in fixture {}",
                fixture["name"]
            );
        };

        expect_field(&decoded.from, "from");
        expect_field(&decoded.to, "to");
        expect_field(&decoded.spender, "spender");
        expect_field(&decoded.admin, "admin");
        expect_field(&decoded.amount, "amount");

        assert_eq!(
            decoded.expiration_ledger,
            expected.get("expiration_ledger").and_then(|v| v.as_i64())
        );
    }

    #[test]
    fn transfer_fixture_decodes() {
        assert_fixture(include_str!("../../fixtures/token_events/transfer.json"));
    }

    #[test]
    fn mint_fixture_decodes() {
        assert_fixture(include_str!("../../fixtures/token_events/mint.json"));
    }

    #[test]
    fn burn_fixture_decodes() {
        assert_fixture(include_str!("../../fixtures/token_events/burn.json"));
    }

    #[test]
    fn clawback_fixture_decodes() {
        assert_fixture(include_str!("../../fixtures/token_events/clawback.json"));
    }

    #[test]
    fn approve_fixture_decodes() {
        assert_fixture(include_str!("../../fixtures/token_events/approve.json"));
    }

    // -----------------------------------------------------------------------
    // Typed projection decoding (issue #211)
    // -----------------------------------------------------------------------

    fn vec_val(items: Vec<ScVal>) -> ScVal {
        ScVal::Vec(Some(stellar_xdr::curr::ScVec(
            stellar_xdr::curr::VecM::try_from(items).unwrap(),
        )))
    }

    #[test]
    fn typed_transfer_populates_from_to_and_amount() {
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        let event = decode_token_event(&topics, &i128_val(1_000_000)).expect("decode");
        assert_eq!(event.event_type, TokenEventType::Transfer);
        assert!(event.from.is_some());
        assert!(event.to.is_some());
        assert_eq!(event.amount.as_deref(), Some("1000000"));
        assert!(event.admin.is_none());
        assert!(event.spender.is_none());
    }

    #[test]
    fn typed_mint_records_admin_and_recipient() {
        let topics = vec![sym("mint"), account_addr(0xAA), account_addr(0xBB)];
        let event = decode_token_event(&topics, &i128_val(5_000)).expect("decode");
        assert_eq!(event.event_type, TokenEventType::Mint);
        assert!(event.admin.is_some());
        assert!(event.to.is_some());
        assert_eq!(event.amount.as_deref(), Some("5000"));
    }

    #[test]
    fn typed_burn_records_only_source_and_amount() {
        let topics = vec![sym("burn"), account_addr(1)];
        let event = decode_token_event(&topics, &i128_val(250)).expect("decode");
        assert_eq!(event.event_type, TokenEventType::Burn);
        assert!(event.from.is_some());
        assert!(event.to.is_none());
        assert_eq!(event.amount.as_deref(), Some("250"));
    }

    #[test]
    fn typed_clawback_records_admin_and_source() {
        let topics = vec![sym("clawback"), account_addr(0xAA), account_addr(0xBB)];
        let event = decode_token_event(&topics, &i128_val(999)).expect("decode");
        assert_eq!(event.event_type, TokenEventType::Clawback);
        assert!(event.admin.is_some());
        assert!(event.from.is_some());
    }

    #[test]
    fn typed_approve_records_spender_and_expiration() {
        let topics = vec![sym("approve"), account_addr(1), account_addr(2)];
        let body = vec_val(vec![i128_val(42), ScVal::U32(1_234)]);
        let event = decode_token_event(&topics, &body).expect("decode");
        assert_eq!(event.event_type, TokenEventType::Approve);
        assert!(event.spender.is_some());
        assert_eq!(event.amount.as_deref(), Some("42"));
        assert_eq!(event.expiration_ledger, Some(1_234));
    }

    #[test]
    fn typed_approve_rejects_a_bare_amount_body() {
        // A two-value event whose body is a single i128 is not the token
        // interface shape and must not be projected.
        let topics = vec![sym("approve"), account_addr(1), account_addr(2)];
        assert!(decode_token_event(&topics, &i128_val(42)).is_none());
    }

    #[test]
    fn typed_amounts_stay_strings_at_i128_extremes() {
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        let event = decode_token_event(&topics, &i128_val(i128::MAX)).expect("decode");
        assert_eq!(
            event.amount.as_deref(),
            Some(i128::MAX.to_string().as_str())
        );
    }

    #[test]
    fn typed_decoder_ignores_administrative_events() {
        // set_admin decodes to JSON but moves no value, so it is not projected.
        let topics = vec![sym("set_admin"), account_addr(1)];
        assert!(decode_token_event(&topics, &contract_addr(0xFF)).is_none());
    }

    #[test]
    fn typed_decoder_ignores_a_transfer_topic_with_a_foreign_shape() {
        // Any contract may emit "transfer"; only the token layout is projected.
        let topics = vec![sym("transfer"), account_addr(1), account_addr(2)];
        assert!(decode_token_event(&topics, &ScVal::Bool(true)).is_none());
    }

    #[test]
    fn typed_decoder_ignores_unknown_event_names() {
        let topics = vec![sym("swap"), account_addr(1)];
        assert!(decode_token_event(&topics, &i128_val(1)).is_none());
    }

    #[test]
    fn clawback_negative_amount() {
        let topics = vec![sym("clawback"), account_addr(0xAA), account_addr(0xBB)];
        let out = try_decode_token_event(&topics, &i128_val(-500)).expect("decode");
        assert_eq!(out["amount"], "-500");
    }
}
