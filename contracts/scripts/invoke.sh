#!/usr/bin/env bash
# Deterministic invocation sequence against the reference token contract:
# mint -> transfer -> approve -> transfer_from -> burn. Produces a fixed,
# known sequence of contract events for E2E tests (issue #268) to assert on.
#
# Usage: invoke.sh [local|testnet]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTRACTS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$CONTRACTS_DIR"

NETWORK="${1:-local}"
case "$NETWORK" in
  local|testnet) ;;
  *)
    echo "usage: $0 [local|testnet]" >&2
    exit 1
    ;;
esac

ADMIN_IDENTITY="${ADMIN_IDENTITY:-trident-admin}"
CONTRACT_ID="${CONTRACT_ID:-$(cat ".contract-id.$NETWORK" 2>/dev/null || true)}"
if [ -z "$CONTRACT_ID" ]; then
  echo "error: no contract id. Set CONTRACT_ID or run deploy_${NETWORK}.sh first." >&2
  exit 1
fi

for name in trident-holder-a trident-holder-b; do
  stellar keys generate --fund --network "$NETWORK" --overwrite "$name"
done
HOLDER_A=$(stellar keys address trident-holder-a)
HOLDER_B=$(stellar keys address trident-holder-b)
ADMIN_ADDR=$(stellar keys address "$ADMIN_IDENTITY")

LEDGER=$(stellar ledger latest --network "$NETWORK" --output json | jq -r '.sequence')
EXPIRATION=$((LEDGER + 1000))

echo "1/5 mint -> holder A ($HOLDER_A)"
stellar contract invoke --id "$CONTRACT_ID" --source "$ADMIN_IDENTITY" --network "$NETWORK" \
  -- mint --to "$HOLDER_A" --amount 1000000

echo "2/5 transfer holder A -> holder B"
stellar contract invoke --id "$CONTRACT_ID" --source trident-holder-a --network "$NETWORK" \
  -- transfer --from "$HOLDER_A" --to "$HOLDER_B" --amount 250000

echo "3/5 approve holder B -> admin as spender"
stellar contract invoke --id "$CONTRACT_ID" --source trident-holder-b --network "$NETWORK" \
  -- approve --from "$HOLDER_B" --spender "$ADMIN_ADDR" --amount 50000 --expiration-ledger "$EXPIRATION"

echo "4/5 transfer_from holder B -> holder A (spent by admin)"
stellar contract invoke --id "$CONTRACT_ID" --source "$ADMIN_IDENTITY" --network "$NETWORK" \
  -- transfer_from --spender "$ADMIN_ADDR" --from "$HOLDER_B" --to "$HOLDER_A" --amount 50000

echo "5/5 burn from holder A"
stellar contract invoke --id "$CONTRACT_ID" --source trident-holder-a --network "$NETWORK" \
  -- burn --from "$HOLDER_A" --amount 10000

echo "Done. Emitted events: mint, transfer, approve, transfer, burn for contract $CONTRACT_ID"
