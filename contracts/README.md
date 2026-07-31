# Trident reference contracts

A minimal SEP-41 fungible token contract (`contracts/token`) used to give
Trident's tests, deploy scripts, and E2E suite a deterministic on-chain
contract to exercise. Its storage layout (`Balance(Address)`) matches what
the indexer's storage-snapshot fetcher (issue #270) expects.

This directory is its own Cargo workspace, separate from the root workspace:
Soroban contracts build for `wasm32` with `#![no_std]` and their own release
profile, which doesn't belong alongside the indexer/API services.

## Prerequisites

- [stellar CLI](https://developers.stellar.org/docs/tools/developer-tools/cli/stellar-cli) (`stellar --version`)
- Rust with a wasm target the installed CLI/toolchain supports
  (`wasm32-unknown-unknown` or `wasm32v1-none` — `stellar contract build`
  picks the right one automatically)
- Docker, for the local network

## Build

```bash
./scripts/build.sh
```

Produces `contracts/target/<wasm-target>/release/trident_reference_token.wasm`.

## Deploy + invoke — local

Start a local Stellar quickstart network (Horizon + Soroban RPC + friendbot
on `localhost:8000`):

```bash
stellar container start local
```

Deploy and initialize the reference token:

```bash
./scripts/deploy_local.sh
```

Run the deterministic invocation sequence (mint → transfer → approve →
transfer_from → burn) against it:

```bash
./scripts/invoke.sh local
```

Point the indexer at the local network by setting `STELLAR_RPC_URL` (or
`STELLAR_RPC_URLS`) to `http://localhost:8000/rpc` and adding the printed
contract id to `indexed_contracts`.

## Deploy + invoke — testnet

```bash
./scripts/deploy_testnet.sh   # funds the deploying account via friendbot
./scripts/invoke.sh testnet
```

## Deterministic event sequence

`invoke.sh` always emits, in order: `mint`, `transfer`, `approve`,
`transfer` (from the `transfer_from` call), `burn` — the fixed sequence the
E2E suite (issue #268) asserts against.
