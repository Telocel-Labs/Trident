#!/usr/bin/env bash
#
# Unit tests for scripts/check-internal-ports.sh (issue #778).
#
# Asserts that:
#   1. The checker succeeds against current repository Fly configs.
#   2. The checker fails when an internal config introduces [[services.ports]].
#   3. The checker fails when an internal config introduces [http_service].
#   4. The checker fails when an internal config omits [checks].
#
# Usage:
#   ./scripts/test-check-internal-ports.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECKER="$REPO_ROOT/scripts/check-internal-ports.sh"

echo "=== Running check-internal-ports unit test suite (issue #778) ==="

# 1. Baseline verification on repo files
bash "$CHECKER"

# 2. Regression tests with synthetic fixtures
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/fly" "$TMP_DIR/scripts"
cp "$CHECKER" "$TMP_DIR/scripts/check-internal-ports.sh"
cp "$REPO_ROOT/fly/api.toml" "$TMP_DIR/fly/api.toml"
cp "$REPO_ROOT/fly/grpc-api.toml" "$TMP_DIR/fly/grpc-api.toml"
cp "$REPO_ROOT/fly/indexer.toml" "$TMP_DIR/fly/indexer.toml"

# Test A: Adding [[services.ports]] to grpc-api.toml must trigger failure
cat <<'EOF' >> "$TMP_DIR/fly/grpc-api.toml"
[[services]]
  internal_port = 50051
  [[services.ports]]
    port = 50051
EOF

if (cd "$TMP_DIR" && bash scripts/check-internal-ports.sh >/dev/null 2>&1); then
    echo "FAIL: Checker should have failed on [[services.ports]] in grpc-api.toml" >&2
    exit 1
fi
echo "  [OK] Caught illegal [[services.ports]] in internal service."

# Restore clean grpc-api.toml
cp "$REPO_ROOT/fly/grpc-api.toml" "$TMP_DIR/fly/grpc-api.toml"

# Test B: Adding [http_service] to indexer.toml must trigger failure
cat <<'EOF' >> "$TMP_DIR/fly/indexer.toml"
[http_service]
  internal_port = 8080
EOF

if (cd "$TMP_DIR" && bash scripts/check-internal-ports.sh >/dev/null 2>&1); then
    echo "FAIL: Checker should have failed on [http_service] in indexer.toml" >&2
    exit 1
fi
echo "  [OK] Caught illegal [http_service] in internal service."

# Restore clean indexer.toml
cp "$REPO_ROOT/fly/indexer.toml" "$TMP_DIR/fly/indexer.toml"

# Test C: Omitting [checks] must trigger failure
grep -v '\[checks' "$REPO_ROOT/fly/grpc-api.toml" > "$TMP_DIR/fly/grpc-api.toml"

if (cd "$TMP_DIR" && bash scripts/check-internal-ports.sh >/dev/null 2>&1); then
    echo "FAIL: Checker should have failed on missing [checks] in grpc-api.toml" >&2
    exit 1
fi
echo "  [OK] Caught missing [checks] in internal service."

echo "=== All test-check-internal-ports unit tests passed! ==="
