#!/usr/bin/env bash
# Build the reference contracts to WASM via the stellar CLI (issue #267).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTRACTS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$CONTRACTS_DIR"

stellar contract build

WASM="target/wasm32v1-none/release/trident_reference_token.wasm"
if [ ! -f "$WASM" ]; then
  echo "error: expected build output at $WASM" >&2
  exit 1
fi
echo "Built: $CONTRACTS_DIR/$WASM"
