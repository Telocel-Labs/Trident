# Soroban Event Model and Decoding Guarantees

This document describes how Trident interprets and decodes Soroban contract events, the guarantees it makes about ordering and idempotency, and how to map raw on-chain data to what the API and SDKs expose.

## Table of Contents

- [Event Data Model](#event-data-model)
- [XDR-to-JSON Conventions](#xdr-to-json-conventions)
- [Big-Integer Encoding](#big-integer-encoding)
- [Spec-Aware vs Positional Decoding](#spec-aware-vs-positional-decoding)
- [Indexer Guarantees](#indexer-guarantees)
- [Worked Examples](#worked-examples)
  - [Token Transfer (SAC)](#token-transfer-sac)
  - [NFT Mint](#nft-mint)
  - [Custom Contract (Escrow)](#custom-contract-escrow)
- [Linking to Source](#linking-to-source)

---

## Event Data Model

A Soroban contract event has two structural parts on-chain:

| Part     | Description |
|----------|-------------|
| `topics` | An ordered list of XDR `ScVal` entries that categorise the event. By convention the first topic is a `Symbol` naming the operation (e.g. `"transfer"`, `"mint"`). Topics are used for server-side filtering. |
| `data`   | A single XDR `ScVal` carrying the payload. This is typically a `Vec`, `Map`, or primitive holding the event's business data (amounts, addresses, metadata). |

Trident adds provenance fields that are not present on-chain:

| Field               | Type    | Description |
|---------------------|---------|-------------|
| `id`                | UUID v5 | Deterministic UUID derived from `(contract_id, ledger_sequence, event_index)`. Stable across re-indexes. |
| `contract_id`       | String  | Strkey-encoded contract address (starts with `C`). |
| `ledger_sequence`   | Integer | Ledger number the transaction was included in. |
| `ledger_timestamp`  | ISO8601 | Timestamp of the ledger close. |
| `transaction_hash`  | String  | Transaction hash (hex). |
| `event_index`       | Integer | Zero-based position of the event within the transaction's event list. |
| `event_type`        | String  | `"contract"`, `"system"`, or `"diagnostic"`. |
| `topics`            | Array   | Decoded topic values as JSON (see below). |
| `data`              | Any     | Decoded payload as JSON (see below). |
| `created_at`        | ISO8601 | When the row was written to Trident's database (≈ `ledger_timestamp`). |

---

## XDR-to-JSON Conventions

Trident decodes XDR `ScVal` variants to JSON using the following mapping:

| XDR type         | JSON representation                                     |
|------------------|---------------------------------------------------------|
| `Symbol`         | `string` — the symbol's UTF-8 value (e.g. `"transfer"`) |
| `String`         | `string`                                                |
| `I128` / `U128`  | `string` — decimal representation (see [Big-Integer Encoding](#big-integer-encoding)) |
| `I64` / `U64`    | `number` (JSON number, safe up to 2^53)                 |
| `I32` / `U32`    | `number`                                                |
| `Bool`           | `true` / `false`                                        |
| `Address`        | `string` — Strkey encoding (`G…` for accounts, `C…` for contracts) |
| `Bytes`          | `string` — hex-encoded                                  |
| `Vec`            | `array` — each element recursively decoded              |
| `Map`            | `object` — keys decoded as strings, values recursively decoded |
| `Void`           | `null`                                                  |

---

## Big-Integer Encoding

Soroban's `i128` and `u128` types exceed the safe integer range of IEEE 754 doubles (2^53 − 1). Trident encodes them as **decimal strings** to prevent silent precision loss.

```json
{
  "amount": "123456789012345678901234567890"
}
```

> **SDK note**: All SDKs expose the raw JSON value. Parse `i128`/`u128` amounts with a big-integer library (e.g. `BigInt` in JavaScript, `decimal.Decimal` in Python, `big.Int` in Go) before doing arithmetic.

---

## Spec-Aware vs Positional Decoding

Trident supports two decoding strategies, selected per-contract:

### Positional decoding (default)

Topics and data are decoded in order. The topic array maps to the raw on-chain topic list; `data` is the decoded root `ScVal`. No contract spec is required.

```json
{
  "topics": ["transfer", "GABC…", "GDEF…"],
  "data": { "amount": "1000000000" }
}
```

### Spec-aware decoding (opt-in, issue #261)

When a contract spec (WASM interface) is available, Trident can label fields by name instead of position. This yields richer, self-describing payloads:

```json
{
  "topics": ["transfer"],
  "data": {
    "from": "GABC…",
    "to":   "GDEF…",
    "amount": "1000000000"
  }
}
```

Spec-aware decoding is enabled per-contract in the admin API. Positional decoding is always available as a fallback.

---

## Indexer Guarantees

### Idempotency

Events are identified by a deterministic UUID v5 derived from `(contract_id, ledger_sequence, event_index)`. Duplicate ingestion (e.g. from ledger re-polling after a restart) produces the same UUID and is silently ignored by `ON CONFLICT (id) DO NOTHING`. Consumers will never see duplicate event rows.

### Ordering

Events within the API response are ordered by `(ledger_sequence ASC, event_index ASC)`. This matches the canonical on-chain ordering within a transaction.

### Provenance

Every event row includes `ledger_sequence`, `transaction_hash`, and `event_index`, giving a complete chain reference to the on-chain source. `ledger_timestamp` is the Stellar network's close time for that ledger.

### Retention window and resume

The streaming API (`/ws`) maintains a Redis-backed retention window (configurable via `REDIS_STREAM_MAXLEN`). Clients that reconnect with a `cursor` query parameter (set to the last received event id) will receive all events emitted after that cursor, up to the retention window boundary. Events older than the retention window are not replayed; consumers should fall back to the REST API for historical backfill.

---

## Worked Examples

### Token Transfer (SAC)

The Stellar Asset Contract emits `transfer` events conforming to the SEP-41 token interface.

**On-chain topics**: `[Symbol("transfer"), Address(from), Address(to)]`  
**On-chain data**: `i128(amount)`

**Trident JSON**:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "contract_id": "CCW67TSZV3SSS2HXMBQ5JFGCKJNXKZM7UQUWUZPUTHXSTZLEO7SJMI75",
  "ledger_sequence": 50000000,
  "ledger_timestamp": "2024-06-01T12:00:00Z",
  "transaction_hash": "abc123def456…",
  "event_index": 0,
  "event_type": "contract",
  "topics": ["transfer", "GABC…", "GDEF…"],
  "data": "1000000000"
}
```

> Note: The amount is a decimal string because `i128` exceeds the safe JSON integer range.

---

### NFT Mint

A hypothetical NFT contract emits a `mint` event with a `Vec` data payload.

**On-chain topics**: `[Symbol("mint"), Address(recipient)]`  
**On-chain data**: `Map { token_id: u64, uri: String }`

**Trident JSON**:

```json
{
  "contract_id": "CNFTCONTRACT…",
  "ledger_sequence": 50100000,
  "event_type": "contract",
  "topics": ["mint", "GRECIPIENT…"],
  "data": {
    "token_id": 42,
    "uri": "ipfs://QmXyz…"
  }
}
```

---

### Custom Contract (Escrow)

The Trident reference escrow contract (`contracts/escrow/`) emits one event per state transition, making it useful for testing the indexer against a realistic, multi-step flow.

**Happy path**: deposit → release

```json
[
  {
    "event_index": 0,
    "topics": ["deposit"],
    "data": ["GDEPOSITOR…", "GBENEFICIARY…", "5000000000"]
  },
  {
    "event_index": 0,
    "topics": ["release"],
    "data": ["GBENEFICIARY…", "5000000000"]
  }
]
```

**Refund path**: deposit → refund

```json
[
  {
    "event_index": 0,
    "topics": ["deposit"],
    "data": ["GDEPOSITOR…", "GBENEFICIARY…", "5000000000"]
  },
  {
    "event_index": 0,
    "topics": ["refund"],
    "data": ["GDEPOSITOR…", "5000000000"]
  }
]
```

Events are guaranteed to appear in ledger order. Filtering by `contractId` returns only events from the escrow contract. See `crates/indexer/src/escrow_integration_test.rs` for the integration tests that validate this ordering.

---

## Linking to Source

- REST API reference: see `docs/SPECIFICATION.md`
- Streaming / reconnect behaviour: see `docs/stream-events.md`
- Spec-aware decoding implementation: `crates/indexer/src/parser/`
- Escrow reference contract: `contracts/escrow/src/lib.rs`
- SAC bootstrap (well-known contracts): `crates/indexer/src/sac_bootstrap.rs`
