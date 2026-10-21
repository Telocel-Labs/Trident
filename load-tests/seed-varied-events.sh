#!/usr/bin/env bash
#
# Seed representative diverse mainnet event payloads for load testing (issue #673).
# Replaces synthetic single-shape token mint loops with a realistic mix of
# SAC token transfers/mints/burns, NFTs, AMM swaps, DAO votes, and diagnostics.
#
# Usage:
#   DATABASE_URL="postgresql://trident:password@localhost:5432/trident" ./load-tests/seed-varied-events.sh [COUNT]
#
# Default COUNT is 2000 events.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COUNT="${1:-2000}"
NETWORK="${NETWORK:-mainnet}"
START_LEDGER="${START_LEDGER:-52000000}"
DB_URL="${DATABASE_URL:-}"

echo "==> Generating $COUNT varied mainnet events for network '$NETWORK' starting at ledger $START_LEDGER..."
TMP_SQL="$(mktemp -t trident_varied_events_XXXXXX.sql)"
trap 'rm -f "$TMP_SQL"' EXIT

python3 "${SCRIPT_DIR}/generate_mainnet_events.py" \
  --count "$COUNT" \
  --network "$NETWORK" \
  --start-ledger "$START_LEDGER" \
  --format sql \
  --out "$TMP_SQL"

if [ -n "$DB_URL" ]; then
  echo "==> Applying generated events to $DB_URL..."
  if command -v psql >/dev/null 2>&1; then
    psql "$DB_URL" -q -f "$TMP_SQL"
    echo "==> Successfully seeded $COUNT varied events into database."
  else
    echo "psql not found; events written to $TMP_SQL but not executed." >&2
    exit 1
  fi
else
  echo "DATABASE_URL not set. Generated SQL saved to $TMP_SQL."
fi
