#!/usr/bin/env bash
#
# check-internal-ports.sh — Ensures internal-only Fly configs do not declare public ports (issue #778).
#
# Internal services (such as the unauthenticated gRPC API and the indexer metrics/health server)
# must only be reachable over Fly's private WireGuard (6PN) network.
# Declaring [[services.ports]] or [http_service] allocates a public IP and exposes internal
# services to the internet.
#
# Usage:
#   scripts/check-internal-ports.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

failures=0

check_internal_fly_config() {
    local file="$1"
    local name="$2"

    if [ ! -f "$file" ]; then
        echo "ERROR: Fly config file $file not found!" >&2
        failures=$((failures + 1))
        return
    fi

    # Check for un-commented public port declarations
    if grep -qE '^[[:space:]]*(\[\[services\.ports\]\]|\[http_service\])' "$file"; then
        echo "FAIL: $file ($name) exposes public ports! Internal-only services must omit public [[services.ports]] and [http_service]." >&2
        failures=$((failures + 1))
    else
        echo "  [OK] $file has no public port declarations."
    fi

    # Check that internal health check is present
    if ! grep -qE '^[[:space:]]*\[checks\]' "$file"; then
        echo "FAIL: $file ($name) must declare [checks] for private health monitoring." >&2
        failures=$((failures + 1))
    else
        echo "  [OK] $file defines [checks] for private monitoring."
    fi
}

echo "=== Verifying internal-only Fly services have no public ports (issue #778) ==="

check_internal_fly_config "fly/grpc-api.toml" "gRPC API"
check_internal_fly_config "fly/indexer.toml" "Indexer"

# Also verify that fly/api.toml legitimately retains [http_service] as the public gateway
if ! grep -qE '^[[:space:]]*\[http_service\]' "fly/api.toml"; then
    echo "FAIL: fly/api.toml should retain [http_service] as the public API gateway." >&2
    failures=$((failures + 1))
else
    echo "  [OK] fly/api.toml correctly configured as public HTTP gateway."
fi

if [ "$failures" -gt 0 ]; then
    echo "ERROR: $failures check(s) failed." >&2
    exit 1
fi

echo "=== All Fly internal port checks passed! ==="
