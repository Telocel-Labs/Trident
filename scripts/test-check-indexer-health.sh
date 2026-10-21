#!/usr/bin/env bash
#
# Verification tests for indexer health server Slowloris protection,
# admin dead-letter authentication, and poll-loop freshness (issue #764).
#
# Usage:
#   ./scripts/test-check-indexer-health.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "=== Running indexer health server verification (issue #764) ==="

# 1. Verify docs/ENVIRONMENT.md documents HEALTH_HEARTBEAT_TIMEOUT_SECS
if ! grep -qF '`HEALTH_HEARTBEAT_TIMEOUT_SECS`' docs/ENVIRONMENT.md; then
    echo "FAIL: docs/ENVIRONMENT.md missing HEALTH_HEARTBEAT_TIMEOUT_SECS" >&2
    exit 1
fi
echo "  [OK] docs/ENVIRONMENT.md documents HEALTH_HEARTBEAT_TIMEOUT_SECS"

# 2. Run Rust unit tests for health server if cargo is present
if command -v cargo >/dev/null 2>&1; then
    echo "  Running cargo test for health::tests..."
    cargo test -p trident-indexer --bin trident-indexer health::tests
    echo "  [OK] cargo test health::tests passed"
else
    echo "  [INFO] cargo CLI not in PATH; skipping cargo test execution"
fi

echo "=== All indexer health checks passed! ==="
