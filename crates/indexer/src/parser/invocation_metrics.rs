//! Decode per-invocation resource + fee metering from a `getTransaction`
//! response (issue #266).
//!
//! `feeCharged` on the transaction result is the actual amount charged and is
//! always present for a processed transaction. CPU instructions and ledger
//! read/write byte budgets come from the *declared* `SorobanTransactionData`
//! resources on the transaction envelope — the limits the submitter requested
//! (and simulation computed) — not host-measured actual consumption; the RPC
//! only reports true metered usage via diagnostic events, which most public
//! nodes do not enable. See docs/contract-invocation-metering.md. This
//! distinction is recorded in `PROVENANCE_DECLARED` so consumers do not
//! mistake declared limits for measured usage.

use base64::{engine::general_purpose::STANDARD, Engine};
use stellar_xdr::curr::{
    FeeBumpTransactionInnerTx, Limited, Limits, ReadXdr, TransactionEnvelope, TransactionExt,
    TransactionResult, TransactionResultResult,
};
use trident_common::TridentError;

/// The only provenance value emitted today: resource fields reflect the
/// transaction's declared/simulated budget, not host-measured usage.
pub const PROVENANCE_DECLARED: &str = "declared_resources";

/// Decoded fee + resource metering for one transaction, ready to be
/// attributed to every tracked contract it invoked.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct InvocationMetrics {
    /// Actual total fee charged, in stroops (`TransactionResult.feeCharged`).
    pub fee_charged: i64,
    /// Declared resource-fee portion of `fee_charged`, when the transaction
    /// carries Soroban resource data.
    pub resource_fee: Option<i64>,
    pub cpu_instructions: Option<i64>,
    pub read_bytes: Option<i64>,
    pub write_bytes: Option<i64>,
    pub provenance: &'static str,
}

/// Decode a `getTransaction` response's `envelopeXdr` + `resultXdr` into
/// [`InvocationMetrics`].
///
/// A transaction that did not succeed still charged a fee but never
/// consumed its declared resource budget, so the CPU/read/write fields are
/// `None` for those.
pub fn decode_invocation_metrics(
    envelope_xdr_b64: &str,
    result_xdr_b64: &str,
) -> Result<InvocationMetrics, TridentError> {
    let result: TransactionResult = decode_xdr(result_xdr_b64, "resultXdr")?;

    let succeeded = matches!(
        result.result,
        TransactionResultResult::TxSuccess(_) | TransactionResultResult::TxFeeBumpInnerSuccess(_)
    );

    if !succeeded {
        return Ok(InvocationMetrics {
            fee_charged: result.fee_charged,
            resource_fee: None,
            cpu_instructions: None,
            read_bytes: None,
            write_bytes: None,
            provenance: PROVENANCE_DECLARED,
        });
    }

    let envelope: TransactionEnvelope = decode_xdr(envelope_xdr_b64, "envelopeXdr")?;
    let soroban_ext = match envelope {
        TransactionEnvelope::Tx(v1) => v1.tx.ext,
        TransactionEnvelope::TxFeeBump(fee_bump) => match fee_bump.tx.inner_tx {
            FeeBumpTransactionInnerTx::Tx(inner) => inner.tx.ext,
        },
        // Pre-Soroban v0 envelopes never carry resource data.
        TransactionEnvelope::TxV0(_) => TransactionExt::V0,
    };

    let (resource_fee, cpu_instructions, read_bytes, write_bytes) = match soroban_ext {
        TransactionExt::V1(data) => (
            Some(data.resource_fee),
            Some(data.resources.instructions as i64),
            Some(data.resources.disk_read_bytes as i64),
            Some(data.resources.write_bytes as i64),
        ),
        TransactionExt::V0 => (None, None, None, None),
    };

    Ok(InvocationMetrics {
        fee_charged: result.fee_charged,
        resource_fee,
        cpu_instructions,
        read_bytes,
        write_bytes,
        provenance: PROVENANCE_DECLARED,
    })
}

fn decode_xdr<T: ReadXdr>(b64: &str, context: &'static str) -> Result<T, TridentError> {
    let bytes = STANDARD.decode(b64).map_err(|e| {
        TridentError::parse(anyhow::Error::new(e).context(format!("{context} base64 decode")))
    })?;
    let mut cursor = std::io::Cursor::new(bytes);
    T::read_xdr(&mut Limited::new(&mut cursor, Limits::none())).map_err(|e| {
        TridentError::parse(anyhow::Error::new(e).context(format!("{context} XDR decode")))
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use stellar_xdr::curr::{
        LedgerFootprint, Memo, MuxedAccount, Operation, OperationBody, Preconditions,
        SequenceNumber, SorobanResources, SorobanTransactionData, SorobanTransactionDataExt,
        Transaction, TransactionExt, TransactionResultExt, TransactionV1Envelope, Uint256, VecM,
        WriteXdr,
    };

    fn xdr_b64<T: WriteXdr>(val: &T) -> String {
        let mut buf = Vec::new();
        val.write_xdr(&mut Limited::new(&mut buf, Limits::none()))
            .expect("XDR encode");
        STANDARD.encode(buf)
    }

    fn success_result(fee_charged: i64) -> TransactionResult {
        TransactionResult {
            fee_charged,
            result: TransactionResultResult::TxSuccess(VecM::default()),
            ext: TransactionResultExt::V0,
        }
    }

    fn failed_result(fee_charged: i64) -> TransactionResult {
        TransactionResult {
            fee_charged,
            result: TransactionResultResult::TxFailed(VecM::default()),
            ext: TransactionResultExt::V0,
        }
    }

    fn soroban_invoke_transaction(
        instructions: u32,
        disk_read_bytes: u32,
        write_bytes: u32,
        resource_fee: i64,
    ) -> Transaction {
        // The operation body is irrelevant to `decode_invocation_metrics` — it
        // only reads `tx.ext` — so a no-argument variant keeps the fixture
        // builder minimal.
        let op = Operation {
            source_account: None,
            body: OperationBody::Inflation,
        };

        let resources = SorobanResources {
            footprint: LedgerFootprint {
                read_only: VecM::default(),
                read_write: VecM::default(),
            },
            instructions,
            disk_read_bytes,
            write_bytes,
        };
        let soroban_data = SorobanTransactionData {
            ext: SorobanTransactionDataExt::V0,
            resources,
            resource_fee,
        };

        Transaction {
            source_account: MuxedAccount::Ed25519(Uint256([1u8; 32])),
            fee: 1_000_000,
            seq_num: SequenceNumber(1),
            cond: Preconditions::None,
            memo: Memo::None,
            operations: VecM::try_from(vec![op]).unwrap(),
            ext: TransactionExt::V1(soroban_data),
        }
    }

    fn envelope_for(tx: Transaction) -> TransactionEnvelope {
        TransactionEnvelope::Tx(TransactionV1Envelope {
            tx,
            signatures: VecM::default(),
        })
    }

    #[test]
    fn successful_soroban_invocation_reports_declared_resources() {
        let tx = soroban_invoke_transaction(5_000_000, 2_048, 512, 12_345);
        let envelope_xdr = xdr_b64(&envelope_for(tx));
        let result_xdr = xdr_b64(&success_result(1_012_345));

        let metrics = decode_invocation_metrics(&envelope_xdr, &result_xdr).expect("decode");

        assert_eq!(metrics.fee_charged, 1_012_345);
        assert_eq!(metrics.resource_fee, Some(12_345));
        assert_eq!(metrics.cpu_instructions, Some(5_000_000));
        assert_eq!(metrics.read_bytes, Some(2_048));
        assert_eq!(metrics.write_bytes, Some(512));
        assert_eq!(metrics.provenance, PROVENANCE_DECLARED);
    }

    #[test]
    fn failed_transaction_still_reports_fee_charged_but_no_resources() {
        let tx = soroban_invoke_transaction(5_000_000, 2_048, 512, 12_345);
        let envelope_xdr = xdr_b64(&envelope_for(tx));
        let result_xdr = xdr_b64(&failed_result(100_000));

        let metrics = decode_invocation_metrics(&envelope_xdr, &result_xdr).expect("decode");

        assert_eq!(metrics.fee_charged, 100_000);
        assert_eq!(metrics.resource_fee, None);
        assert_eq!(metrics.cpu_instructions, None);
        assert_eq!(metrics.read_bytes, None);
        assert_eq!(metrics.write_bytes, None);
    }

    #[test]
    fn non_soroban_v0_extension_reports_fee_only() {
        let mut tx = soroban_invoke_transaction(1, 1, 1, 1);
        tx.ext = TransactionExt::V0;
        let envelope_xdr = xdr_b64(&envelope_for(tx));
        let result_xdr = xdr_b64(&success_result(500));

        let metrics = decode_invocation_metrics(&envelope_xdr, &result_xdr).expect("decode");

        assert_eq!(metrics.fee_charged, 500);
        assert_eq!(metrics.cpu_instructions, None);
    }

    #[test]
    fn malformed_result_xdr_is_a_parse_error() {
        let err = decode_invocation_metrics("AAAA", "not-valid-base64-xdr!!").unwrap_err();
        assert_eq!(err.severity(), trident_common::Severity::Skip);
    }

    // -----------------------------------------------------------------------
    // Golden fixture (issue #266)
    // -----------------------------------------------------------------------

    #[test]
    fn transaction_meta_fixture_decodes() {
        let fixture: serde_json::Value = serde_json::from_str(include_str!(
            "../../fixtures/invocation_metrics/successful_invocation.json"
        ))
        .expect("fixture JSON");

        let metrics = decode_invocation_metrics(
            fixture["envelopeXdr"].as_str().unwrap(),
            fixture["resultXdr"].as_str().unwrap(),
        )
        .expect("fixture must decode");

        let expected = &fixture["expected"];
        assert_eq!(
            metrics.fee_charged,
            expected["fee_charged"].as_i64().unwrap()
        );
        assert_eq!(metrics.resource_fee, expected["resource_fee"].as_i64());
        assert_eq!(
            metrics.cpu_instructions,
            expected["cpu_instructions"].as_i64()
        );
        assert_eq!(metrics.read_bytes, expected["read_bytes"].as_i64());
        assert_eq!(metrics.write_bytes, expected["write_bytes"].as_i64());
        assert_eq!(metrics.provenance, expected["provenance"].as_str().unwrap());
    }
}
