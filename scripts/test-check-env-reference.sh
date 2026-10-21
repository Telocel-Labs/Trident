#!/usr/bin/env bash
#
# Unit tests for Fly and Helm env validation in scripts/check-env-reference.sh (issue #777).
#
# Asserts that:
#   1. Current repository Fly and Helm configs pass validation.
#   2. Fly configs setting rejected variables (e.g. POLL_INTERVAL_MS, MAX_REORG_REWIND_DEPTH) fail.
#   3. Fly configs setting undocumented variables fail.
#   4. Helm values setting rejected variables fail.
#
# Usage:
#   scripts/test-check-env-reference.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECKER="$REPO_ROOT/scripts/check-env-reference.sh"

if [ ! -f "$CHECKER" ]; then
    echo "error: $CHECKER not found" >&2
    exit 2
fi

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

failures=0

assert_pass() {
    local desc="$1"
    shift
    if out=$(bash "$CHECKER" "$@" 2>&1); then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — expected checker to pass, got: $out"
        failures=$((failures + 1))
    fi
}

assert_fail() {
    local desc="$1"
    local want_snippet="$2"
    shift 2
    if out=$(bash "$CHECKER" "$@" 2>&1); then
        echo "FAIL: $desc — expected checker to fail, but it passed"
        failures=$((failures + 1))
    elif [[ "$out" == *"$want_snippet"* ]]; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — failed, but not for expected reason ($want_snippet): $out"
        failures=$((failures + 1))
    fi
}

# --- Case 1: Real repository Fly and Helm configs pass ---
assert_pass "real repo configs pass validation"

# Setup scratch test environment
MOCK_FLY="$WORKDIR/fly"
MOCK_HELM="$WORKDIR/helm"
mkdir -p "$MOCK_FLY" "$MOCK_HELM/templates"

cat > "$MOCK_FLY/indexer.toml" <<'EOF'
[env]
  POLL_INTERVAL_FLOOR_MS   = "250"
  POLL_INTERVAL_CEILING_MS = "5000"
  NETWORK                  = "testnet"
  METRICS_PORT             = "9090"
EOF

cat > "$MOCK_HELM/values.yaml" <<'EOF'
indexer:
  env:
    DB_BATCH_SIZE: "1000"
EOF

# --- Case 2: Mock valid Fly and Helm pass ---
assert_pass "mock valid deploy configs pass" "$MOCK_FLY" "$MOCK_HELM"

# --- Case 3: Fly config with rejected POLL_INTERVAL_MS fails ---
cat > "$MOCK_FLY/bad_indexer.toml" <<'EOF'
[env]
  POLL_INTERVAL_MS = "5000"
EOF
assert_fail "Fly config with rejected POLL_INTERVAL_MS fails" "sets rejected env var \`POLL_INTERVAL_MS\`" "$MOCK_FLY" "$MOCK_HELM"
rm -f "$MOCK_FLY/bad_indexer.toml"

# --- Case 4: Fly config with rejected MAX_REORG_REWIND_DEPTH fails ---
cat > "$MOCK_FLY/bad_rewind.toml" <<'EOF'
[env]
  MAX_REORG_REWIND_DEPTH = "10"
EOF
assert_fail "Fly config with rejected MAX_REORG_REWIND_DEPTH fails" "sets rejected env var \`MAX_REORG_REWIND_DEPTH\`" "$MOCK_FLY" "$MOCK_HELM"
rm -f "$MOCK_FLY/bad_rewind.toml"

# --- Case 5: Fly config with undocumented variable fails ---
cat > "$MOCK_FLY/undocumented.toml" <<'EOF'
[env]
  COMPLETELY_UNKNOWN_CUSTOM_VAR_XYZ = "123"
EOF
assert_fail "Fly config with undocumented variable fails" "sets env var \`COMPLETELY_UNKNOWN_CUSTOM_VAR_XYZ\` which is not documented" "$MOCK_FLY" "$MOCK_HELM"
rm -f "$MOCK_FLY/undocumented.toml"

# --- Case 6: Helm values with rejected variable fails ---
cat > "$MOCK_HELM/values-bad.yaml" <<'EOF'
indexer:
  env:
    RPC_FAILOVER_THRESHOLD: "5"
EOF
assert_fail "Helm values with rejected variable fails" "sets rejected env var \`RPC_FAILOVER_THRESHOLD\`" "$MOCK_FLY" "$MOCK_HELM"
rm -f "$MOCK_HELM/values-bad.yaml"

echo
if [ "$failures" -gt 0 ]; then
    echo "$failures test(s) failed"
    exit 1
fi
echo "All check-env-reference.sh deployment validation tests passed"
