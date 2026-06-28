use serde_json::{json, Value as Json};
use tracing::debug;

use super::{scaddress_to_string, scval_to_string};
use stellar_xdr::curr::ScVal;

/// Attempt to decode a known SEP-41 token event from decoded topics and data.
/// Returns Some(structured_json) on success, None on any failure (logs DEBUG).
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

fn decode_mint(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "mint.admin")?;
    let to = addr_topic(topics, 2, "mint.to")?;
    let amount = i128_data(data, "mint.amount")?;
    Ok(json!({ "event": "mint", "admin": admin, "to": to, "amount": amount }))
}

fn decode_burn(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let from = addr_topic(topics, 1, "burn.from")?;
    let amount = i128_data(data, "burn.amount")?;
    Ok(json!({ "event": "burn", "from": from, "amount": amount }))
}

fn decode_clawback(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "clawback.admin")?;
    let from = addr_topic(topics, 2, "clawback.from")?;
    let amount = i128_data(data, "clawback.amount")?;
    Ok(json!({ "event": "clawback", "admin": admin, "from": from, "amount": amount }))
}

fn decode_set_admin(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "set_admin.admin")?;
    let new_admin = addr_scval(data, "set_admin.new_admin")?;
    Ok(json!({ "event": "set_admin", "admin": admin, "new_admin": new_admin }))
}

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

fn decode_increase_supply(topics: &[ScVal], data: &ScVal) -> Result<Json, String> {
    let admin = addr_topic(topics, 1, "increase_supply.admin")?;
    let amount = i128_data(data, "increase_supply.amount")?;
    Ok(json!({ "event": "increase_supply", "admin": admin, "amount": amount }))
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

    #[test]
    fn clawback_negative_amount() {
        let topics = vec![sym("clawback"), account_addr(0xAA), account_addr(0xBB)];
        let out = try_decode_token_event(&topics, &i128_val(-500)).expect("decode");
        assert_eq!(out["amount"], "-500");
    }
}
