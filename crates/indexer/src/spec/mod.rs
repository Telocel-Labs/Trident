//! # Spec (issue #260)
//!
//! Fetches a tracked contract's deployed WASM via `getLedgerEntries`
//! (instance entry -> code hash -> code entry) and parses the embedded
//! `contractspecv0` custom section (contractmeta / SEP-48) into its exported
//! functions. Parsed specs are keyed by WASM code hash and cached in memory
//! so contracts sharing the same deployed code (e.g. many token instances)
//! only pay the parse cost once.
//!
//! Interface classification from the parsed functions (issue #269) lives in
//! [`interfaces`].

use std::collections::HashMap;
use std::sync::Mutex;

use base64::{engine::general_purpose::STANDARD, Engine};
use stellar_xdr::curr::{
    ContractDataDurability, ContractExecutable, ContractId, Hash, LedgerEntryData, LedgerKey,
    LedgerKeyContractCode, LedgerKeyContractData, Limited, Limits, ReadXdr, ScAddress,
    ScContractInstance, ScSpecEntry, ScVal, WriteXdr,
};
use trident_common::TridentError;

use crate::rpc::RpcClient;

pub mod interfaces;

/// One function exported by a contract, as captured from its spec.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct SpecFunction {
    pub name: String,
}

/// Parsed + classified spec for a contract at its currently-deployed code
/// hash.
#[derive(Debug, Clone)]
pub struct ContractSpec {
    pub code_hash: String,
    pub has_spec: bool,
    pub functions: Vec<SpecFunction>,
    pub contract_type: String,
    pub interfaces: Vec<String>,
}

/// In-memory cache of parsed spec functions keyed by WASM code hash (hex).
/// Avoids re-fetching and re-parsing the same WASM for every contract that
/// happens to share it.
#[derive(Default)]
pub struct SpecCache {
    by_code_hash: Mutex<HashMap<String, Vec<SpecFunction>>>,
}

impl SpecCache {
    pub fn new() -> Self {
        Self::default()
    }
}

/// Build the `ScAddress` for a contract strkey (`C...`).
pub(crate) fn contract_address(contract_id: &str) -> Result<ScAddress, TridentError> {
    let strkey = stellar_strkey::Contract::from_string(contract_id).map_err(|e| {
        TridentError::parse(anyhow::anyhow!("invalid contract id {contract_id}: {e}"))
    })?;
    Ok(ScAddress::Contract(ContractId(Hash(strkey.0))))
}

fn encode_key(key: &LedgerKey) -> Result<String, TridentError> {
    let mut buf = Vec::new();
    key.write_xdr(&mut Limited::new(&mut buf, Limits::none()))
        .map_err(|e| TridentError::parse(anyhow::Error::new(e).context("encode LedgerKey")))?;
    Ok(STANDARD.encode(buf))
}

fn decode_entry(xdr_b64: &str) -> Result<LedgerEntryData, TridentError> {
    let bytes = STANDARD.decode(xdr_b64).map_err(|e| {
        TridentError::parse(anyhow::Error::new(e).context("base64 decode ledger entry"))
    })?;
    let mut cursor = std::io::Cursor::new(bytes);
    LedgerEntryData::read_xdr(&mut Limited::new(&mut cursor, Limits::none())).map_err(|e| {
        TridentError::parse(anyhow::Error::new(e).context("XDR decode LedgerEntryData"))
    })
}

/// Fetch the WASM code hash currently deployed at `contract_id` via its
/// contract-instance ledger entry. `Ok(None)` covers every "no spec
/// available" case: no instance entry (not a Soroban contract / archived),
/// or a Stellar Asset Contract (built-in, no WASM).
async fn fetch_code_hash(rpc: &RpcClient, contract_id: &str) -> Result<Option<Hash>, TridentError> {
    let instance_key = LedgerKey::ContractData(LedgerKeyContractData {
        contract: contract_address(contract_id)?,
        key: ScVal::LedgerKeyContractInstance,
        durability: ContractDataDurability::Persistent,
    });

    let entries = rpc
        .get_ledger_entries(&[encode_key(&instance_key)?])
        .await?;
    let Some(entry) = entries.first() else {
        return Ok(None);
    };

    let LedgerEntryData::ContractData(contract_data) = decode_entry(&entry.xdr)? else {
        return Ok(None);
    };
    let ScVal::ContractInstance(ScContractInstance { executable, .. }) = contract_data.val else {
        return Ok(None);
    };

    match executable {
        ContractExecutable::Wasm(hash) => Ok(Some(hash)),
        ContractExecutable::StellarAsset => Ok(None),
    }
}

/// Fetch the WASM bytecode for a given code hash via its contract-code
/// ledger entry.
async fn fetch_wasm(rpc: &RpcClient, hash: &Hash) -> Result<Vec<u8>, TridentError> {
    let code_key = LedgerKey::ContractCode(LedgerKeyContractCode { hash: hash.clone() });
    let entries = rpc.get_ledger_entries(&[encode_key(&code_key)?]).await?;
    let Some(entry) = entries.first() else {
        return Ok(Vec::new());
    };
    let LedgerEntryData::ContractCode(code_entry) = decode_entry(&entry.xdr)? else {
        return Ok(Vec::new());
    };
    Ok(code_entry.code.to_vec())
}

// ---------------------------------------------------------------------------
// WASM `contractspecv0` custom-section parsing
// ---------------------------------------------------------------------------

fn read_leb128_u32(buf: &[u8], pos: &mut usize) -> Option<u32> {
    let mut result: u32 = 0;
    let mut shift: u32 = 0;
    loop {
        let byte = *buf.get(*pos)?;
        *pos += 1;
        result |= ((byte & 0x7f) as u32) << shift;
        if byte & 0x80 == 0 {
            break;
        }
        shift += 7;
        if shift >= 35 {
            return None;
        }
    }
    Some(result)
}

/// Locate a named custom section inside a WASM binary. This is a minimal
/// walk of the module header just to find `contractspecv0` — not a general
/// WASM parser.
fn extract_custom_section<'a>(wasm: &'a [u8], name: &str) -> Option<&'a [u8]> {
    if wasm.len() < 8 || &wasm[0..4] != b"\0asm" {
        return None;
    }
    let mut pos = 8usize;
    while pos < wasm.len() {
        let id = *wasm.get(pos)?;
        pos += 1;
        let size = read_leb128_u32(wasm, &mut pos)? as usize;
        let section_start = pos;
        let section_end = section_start.checked_add(size)?;
        if section_end > wasm.len() {
            return None;
        }
        if id == 0 {
            let mut p = section_start;
            let name_len = read_leb128_u32(wasm, &mut p)? as usize;
            if let Some(name_end) = p.checked_add(name_len) {
                if name_end <= section_end {
                    if let Ok(section_name) = std::str::from_utf8(&wasm[p..name_end]) {
                        if section_name == name {
                            return Some(&wasm[name_end..section_end]);
                        }
                    }
                }
            }
        }
        pos = section_end;
    }
    None
}

/// Decode the sequence of XDR `ScSpecEntry` values packed into the
/// `contractspecv0` custom section. Stops at the first entry that fails to
/// decode rather than erroring — a contract with no embedded spec, or one
/// this parser can't fully make sense of, degrades to an empty function list
/// instead of failing the fetch (issue #260: "handle contracts without an
/// embedded spec gracefully").
fn parse_spec_entries(wasm: &[u8]) -> Vec<ScSpecEntry> {
    let Some(section) = extract_custom_section(wasm, "contractspecv0") else {
        return Vec::new();
    };
    let mut reader = section;
    let mut entries = Vec::new();
    while !reader.is_empty() {
        let mut limited = Limited::new(&mut reader, Limits::none());
        match ScSpecEntry::read_xdr(&mut limited) {
            Ok(entry) => entries.push(entry),
            Err(_) => break,
        }
    }
    entries
}

fn spec_functions(entries: &[ScSpecEntry]) -> Vec<SpecFunction> {
    entries
        .iter()
        .filter_map(|entry| match entry {
            ScSpecEntry::FunctionV0(f) => Some(SpecFunction {
                name: f.name.to_utf8_string_lossy(),
            }),
            _ => None,
        })
        .collect()
}

/// Fetch, parse, and classify a contract's spec (issues #260, #269).
///
/// Returns `Ok(None)` when the contract has no accessible instance/WASM —
/// the caller treats that as "nothing to persist" rather than an error,
/// matching the best-effort convention already used for invocation metrics.
pub async fn fetch_contract_spec(
    rpc: &RpcClient,
    cache: &SpecCache,
    contract_id: &str,
) -> Result<Option<ContractSpec>, TridentError> {
    let Some(hash) = fetch_code_hash(rpc, contract_id).await? else {
        return Ok(None);
    };
    let code_hash_hex = hex::encode(hash.0);

    let cached = cache
        .by_code_hash
        .lock()
        .expect("spec cache mutex poisoned")
        .get(&code_hash_hex)
        .cloned();

    let functions = match cached {
        Some(functions) => functions,
        None => {
            let wasm = fetch_wasm(rpc, &hash).await?;
            let functions = spec_functions(&parse_spec_entries(&wasm));
            cache
                .by_code_hash
                .lock()
                .expect("spec cache mutex poisoned")
                .insert(code_hash_hex.clone(), functions.clone());
            functions
        }
    };

    let function_names: Vec<&str> = functions.iter().map(|f| f.name.as_str()).collect();
    let detected = interfaces::detect_interfaces(&function_names);

    Ok(Some(ContractSpec {
        code_hash: code_hash_hex,
        has_spec: !functions.is_empty(),
        functions,
        contract_type: detected.contract_type.to_string(),
        interfaces: detected.interfaces,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Build a minimal valid WASM module (magic + version, no sections).
    fn empty_wasm() -> Vec<u8> {
        vec![0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]
    }

    #[test]
    fn extract_custom_section_returns_none_when_absent() {
        assert!(extract_custom_section(&empty_wasm(), "contractspecv0").is_none());
    }

    #[test]
    fn extract_custom_section_returns_none_for_non_wasm_bytes() {
        assert!(extract_custom_section(b"not wasm", "contractspecv0").is_none());
    }

    /// Encode a WASM custom section (id 0) with the given name and payload,
    /// appended after a minimal module header.
    fn wasm_with_custom_section(name: &str, payload: &[u8]) -> Vec<u8> {
        let mut section = Vec::new();
        write_leb128_u32(&mut section, name.len() as u32);
        section.extend_from_slice(name.as_bytes());
        section.extend_from_slice(payload);

        let mut wasm = empty_wasm();
        wasm.push(0); // custom section id
        write_leb128_u32(&mut wasm, section.len() as u32);
        wasm.extend_from_slice(&section);
        wasm
    }

    fn write_leb128_u32(buf: &mut Vec<u8>, mut value: u32) {
        loop {
            let byte = (value & 0x7f) as u8;
            value >>= 7;
            if value == 0 {
                buf.push(byte);
                break;
            }
            buf.push(byte | 0x80);
        }
    }

    #[test]
    fn extract_custom_section_finds_named_payload() {
        let wasm = wasm_with_custom_section("contractspecv0", &[1, 2, 3]);
        let section = extract_custom_section(&wasm, "contractspecv0").expect("section present");
        assert_eq!(section, &[1, 2, 3]);
    }

    #[test]
    fn extract_custom_section_ignores_other_names() {
        let wasm = wasm_with_custom_section("name", &[9, 9]);
        assert!(extract_custom_section(&wasm, "contractspecv0").is_none());
    }

    #[test]
    fn parse_spec_entries_decodes_a_function_entry() {
        use stellar_xdr::curr::{ScSpecFunctionV0, ScSymbol, StringM, VecM};

        let entry = ScSpecEntry::FunctionV0(ScSpecFunctionV0 {
            doc: StringM::default(),
            name: ScSymbol::try_from("transfer".to_string()).unwrap(),
            inputs: VecM::default(),
            outputs: VecM::default(),
        });
        let mut buf = Vec::new();
        entry
            .write_xdr(&mut Limited::new(&mut buf, Limits::none()))
            .unwrap();

        let wasm = wasm_with_custom_section("contractspecv0", &buf);
        let entries = parse_spec_entries(&wasm);
        let functions = spec_functions(&entries);

        assert_eq!(functions.len(), 1);
        assert_eq!(functions[0].name, "transfer");
    }

    #[test]
    fn parse_spec_entries_empty_when_no_section() {
        assert!(parse_spec_entries(&empty_wasm()).is_empty());
    }
}
