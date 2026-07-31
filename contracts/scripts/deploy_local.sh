#!/usr/bin/env bash
# Deploy + initialize the reference SEP-41 token contract against a local
# Stellar quickstart network (issue #267).
#
# Start the local network first (requires Docker):
#   stellar container start local
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTRACTS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$CONTRACTS_DIR"

IDENTITY="${IDENTITY:-trident-admin}"

stellar keys generate --fund --network local --overwrite "$IDENTITY"
"$SCRIPT_DIR/build.sh"

ADMIN_ADDR=$(stellar keys address "$IDENTITY")

CONTRACT_ID=$(stellar contract deploy \
  --wasm target/wasm32v1-none/release/trident_reference_token.wasm \
  --source "$IDENTITY" \
  --network local)
echo "Deployed contract: $CONTRACT_ID"

stellar contract invoke \
  --id "$CONTRACT_ID" --source "$IDENTITY" --network local \
  -- initialize --admin "$ADMIN_ADDR" --decimal 7 \
  --name "Trident Reference Token" --symbol TRT

echo "$CONTRACT_ID" > .contract-id.local
echo "Initialized. Contract ID saved to $CONTRACTS_DIR/.contract-id.local"
