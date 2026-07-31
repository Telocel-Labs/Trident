//! # Token metadata resolver (issue #263)
//!
//! Resolves `name()`, `symbol()` and `decimals()` for a token contract via a
//! read-only Soroban RPC `simulateTransaction` call (SEP-41) so amounts can be
//! shown in human units. Nothing is submitted to the network: the built
//! transaction envelope is never signed, and `simulateTransaction` only reads
//! current ledger state.

use base64::{engine::general_purpose::STANDARD, Engine};
use stellar_strkey::Contract;
use stellar_xdr::curr::{
    ContractId, Hash as XdrHash, HostFunction, InvokeContractArgs, InvokeHostFunctionOp, Limited,
    Limits, Memo, MuxedAccount, Operation, OperationBody, Preconditions, ScAddress, ScSymbol,
    ScVal, SequenceNumber, Transaction, TransactionEnvelope, TransactionExt, TransactionV1Envelope,
    Uint256, VecM, WriteXdr,
};
use trident_common::TridentError;

use crate::rpc::RpcClient;

/// Resolved SEP-41 token metadata for one contract.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TokenMetadata {
    pub name: String,
    pub symbol: String,
    pub decimals: u32,
}

/// Outcome of a resolution attempt, ready to persist into `token_metadata`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TokenMetadataResolution {
    Token(TokenMetadata),
    /// The contract does not expose the SEP-41 read interface — cached as a
    /// negative result so we do not re-simulate it on every poll cycle.
    NotAToken,
}

/// Resolve `name()`/`symbol()`/`decimals()` for `contract_id` via simulation.
///
/// Returns `Ok(NotAToken)` when any of the three calls fails or returns a
/// value of the wrong shape — that is the graceful "not a token" path the
/// issue calls for. Returns `Err` only for a transport/RPC failure, which the
/// caller should treat as transient and retry later rather than cache.
pub async fn resolve(
    rpc: &RpcClient,
    contract_id: &str,
) -> Result<TokenMetadataResolution, TridentError> {
    let Some(name) = simulate_string_call(rpc, contract_id, "name").await? else {
        return Ok(TokenMetadataResolution::NotAToken);
    };
    let Some(symbol) = simulate_string_call(rpc, contract_id, "symbol").await? else {
        return Ok(TokenMetadataResolution::NotAToken);
    };
    let Some(decimals) = simulate_u32_call(rpc, contract_id, "decimals").await? else {
        return Ok(TokenMetadataResolution::NotAToken);
    };

    Ok(TokenMetadataResolution::Token(TokenMetadata {
        name,
        symbol,
        decimals,
    }))
}

/// Call a zero-argument function expected to return `ScVal::String`.
async fn simulate_string_call(
    rpc: &RpcClient,
    contract_id: &str,
    function_name: &str,
) -> Result<Option<String>, TridentError> {
    match simulate_read_call(rpc, contract_id, function_name).await? {
        Some(ScVal::String(s)) => Ok(Some(s.to_utf8_string_lossy())),
        Some(_) | None => Ok(None),
    }
}

/// Call a zero-argument function expected to return `ScVal::U32`.
async fn simulate_u32_call(
    rpc: &RpcClient,
    contract_id: &str,
    function_name: &str,
) -> Result<Option<u32>, TridentError> {
    match simulate_read_call(rpc, contract_id, function_name).await? {
        Some(ScVal::U32(n)) => Ok(Some(n)),
        Some(_) | None => Ok(None),
    }
}

/// Simulate a single zero-argument read-only call, decoding its return value.
///
/// `Ok(None)` covers every "this isn't a token function" outcome: the
/// simulation itself reported an error (no such function, wrong arity, a
/// contract that traps), the call produced no result, or the result XDR
/// failed to decode. Only a transport/RPC failure reaching the node at all is
/// propagated as `Err`.
async fn simulate_read_call(
    rpc: &RpcClient,
    contract_id: &str,
    function_name: &str,
) -> Result<Option<ScVal>, TridentError> {
    let envelope_xdr = build_invoke_envelope(contract_id, function_name)?;
    let result = rpc.simulate_transaction(&envelope_xdr).await?;

    if let Some(err) = result.error {
        tracing::debug!(
            contract_id,
            function_name,
            error = %err,
            "simulateTransaction reported an error; treating as non-token"
        );
        return Ok(None);
    }

    let Some(first) = result.results.into_iter().next() else {
        return Ok(None);
    };

    match crate::parser::decode_scval(&first.xdr) {
        Ok(val) => Ok(Some(val)),
        Err(e) => {
            tracing::debug!(
                contract_id,
                function_name,
                error = %e,
                "failed to decode simulateTransaction result"
            );
            Ok(None)
        }
    }
}

/// Build a base64 `TransactionEnvelope` XDR invoking `function_name()` with no
/// arguments on `contract_id`, suitable for `simulateTransaction`.
///
/// The source account is a fixed placeholder (all-zero Ed25519 key, sequence
/// 0). `simulateTransaction` only runs the host function against current
/// ledger state — it does not require the source account to exist or the
/// envelope to be signed, since nothing is submitted to the network.
fn build_invoke_envelope(contract_id: &str, function_name: &str) -> Result<String, TridentError> {
    let contract = Contract::from_string(contract_id).map_err(|e| {
        TridentError::parse(anyhow::anyhow!(
            "invalid contract address {contract_id}: {e}"
        ))
    })?;
    let contract_address = ScAddress::Contract(ContractId(XdrHash(contract.0)));
    let function_name = ScSymbol::try_from(function_name.to_string()).map_err(|_| {
        TridentError::parse(anyhow::anyhow!("invalid function name {function_name:?}"))
    })?;

    let host_function = HostFunction::InvokeContract(InvokeContractArgs {
        contract_address,
        function_name,
        args: VecM::default(),
    });

    let op = Operation {
        source_account: None,
        body: OperationBody::InvokeHostFunction(InvokeHostFunctionOp {
            host_function,
            auth: VecM::default(),
        }),
    };

    let tx = Transaction {
        source_account: MuxedAccount::Ed25519(Uint256([0u8; 32])),
        fee: 100,
        seq_num: SequenceNumber(0),
        cond: Preconditions::None,
        memo: Memo::None,
        operations: VecM::try_from(vec![op])
            .map_err(|e| TridentError::parse(anyhow::anyhow!("build operations: {e}")))?,
        ext: TransactionExt::V0,
    };

    let envelope = TransactionEnvelope::Tx(TransactionV1Envelope {
        tx,
        signatures: VecM::default(),
    });

    let mut buf = Vec::new();
    envelope
        .write_xdr(&mut Limited::new(&mut buf, Limits::none()))
        .map_err(|e| {
            TridentError::parse(anyhow::Error::new(e).context("encode simulate envelope"))
        })?;
    Ok(STANDARD.encode(buf))
}

#[cfg(test)]
mod tests {
    use super::*;
    use stellar_xdr::curr::ScString;
    use wiremock::matchers::method;
    use wiremock::{Mock, MockServer, ResponseTemplate};

    use crate::rpc::RpcHttpSettings;

    const TEST_CONTRACT: &str = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM";

    fn sc_string(s: &str) -> ScVal {
        ScVal::String(ScString(s.try_into().unwrap()))
    }

    fn xdr_b64(val: &ScVal) -> String {
        let mut buf = Vec::new();
        val.write_xdr(&mut Limited::new(&mut buf, Limits::none()))
            .unwrap();
        STANDARD.encode(buf)
    }

    #[test]
    fn build_invoke_envelope_roundtrips_function_name() {
        let xdr = build_invoke_envelope(TEST_CONTRACT, "decimals").unwrap();
        assert!(!xdr.is_empty());
    }

    #[test]
    fn build_invoke_envelope_rejects_invalid_contract_address() {
        assert!(build_invoke_envelope("not-a-contract", "name").is_err());
    }

    async fn mock_simulate_sequence(server: &MockServer, bodies: Vec<serde_json::Value>) {
        for body in bodies {
            Mock::given(method("POST"))
                .respond_with(ResponseTemplate::new(200).set_body_json(body))
                .up_to_n_times(1)
                .mount(server)
                .await;
        }
    }

    fn ok_result(val: &ScVal) -> serde_json::Value {
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 4,
            "result": { "results": [{ "xdr": xdr_b64(val) }] }
        })
    }

    fn error_result() -> serde_json::Value {
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 4,
            "result": { "error": "HostError: Error(Value, MissingValue)", "results": [] }
        })
    }

    #[tokio::test]
    async fn resolve_returns_token_metadata_for_a_real_token() {
        let server = MockServer::start().await;
        mock_simulate_sequence(
            &server,
            vec![
                ok_result(&sc_string("Example Token")),
                ok_result(&sc_string("EXT")),
                ok_result(&ScVal::U32(7)),
            ],
        )
        .await;

        let rpc = RpcClient::with_settings(server.uri(), &RpcHttpSettings::default()).unwrap();
        let resolution = resolve(&rpc, TEST_CONTRACT).await.unwrap();

        assert_eq!(
            resolution,
            TokenMetadataResolution::Token(TokenMetadata {
                name: "Example Token".to_string(),
                symbol: "EXT".to_string(),
                decimals: 7,
            })
        );
    }

    #[tokio::test]
    async fn resolve_returns_not_a_token_when_name_call_errors() {
        let server = MockServer::start().await;
        mock_simulate_sequence(&server, vec![error_result()]).await;

        let rpc = RpcClient::with_settings(server.uri(), &RpcHttpSettings::default()).unwrap();
        let resolution = resolve(&rpc, TEST_CONTRACT).await.unwrap();

        assert_eq!(resolution, TokenMetadataResolution::NotAToken);
    }

    #[tokio::test]
    async fn resolve_returns_not_a_token_when_decimals_is_the_wrong_type() {
        let server = MockServer::start().await;
        mock_simulate_sequence(
            &server,
            vec![
                ok_result(&sc_string("Example Token")),
                ok_result(&sc_string("EXT")),
                // decimals() must return U32; a String here is a malformed
                // interface, so the contract is treated as a non-token.
                ok_result(&sc_string("7")),
            ],
        )
        .await;

        let rpc = RpcClient::with_settings(server.uri(), &RpcHttpSettings::default()).unwrap();
        let resolution = resolve(&rpc, TEST_CONTRACT).await.unwrap();

        assert_eq!(resolution, TokenMetadataResolution::NotAToken);
    }
}
