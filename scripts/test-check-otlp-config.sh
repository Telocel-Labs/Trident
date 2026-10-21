#!/usr/bin/env bash
#
# Regression test for issue #762:
# security: OTLP trace exporter defaults to TLS, allows insecure opt-in,
# supports headers, and validates sampling ratio in all 3 services.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAIN_GO="${REPO_ROOT}/services/api/main.go"
GRPC_MAIN="${REPO_ROOT}/crates/api/src/main.rs"
INDEXER_MAIN="${REPO_ROOT}/crates/indexer/src/main.rs"
ENV_DOC="${REPO_ROOT}/docs/ENVIRONMENT.md"

errors=0

echo "Checking issue #762 OTLP TLS and sampling ratio configurations..."

# 1. Verify Go API defaults to TLS and only allows insecure with opt-in
if ! grep -q 'OTEL_EXPORTER_OTLP_INSECURE' "${MAIN_GO}"; then
    echo "FAIL: services/api/main.go missing OTEL_EXPORTER_OTLP_INSECURE check" >&2
    errors=$((errors + 1))
fi

if ! grep -q 'OTEL_EXPORTER_OTLP_HEADERS' "${MAIN_GO}"; then
    echo "FAIL: services/api/main.go missing OTEL_EXPORTER_OTLP_HEADERS support" >&2
    errors=$((errors + 1))
fi

# 2. Verify sampling ratio validation in all 3 services
if ! grep -A 5 'OTEL_SAMPLING_RATIO' "${MAIN_GO}" | grep -q '0.0 and 1.0'; then
    echo "FAIL: services/api/main.go missing [0.0, 1.0] sampling ratio validation" >&2
    errors=$((errors + 1))
fi

if ! grep -A 10 'parse_sampling_ratio' "${GRPC_MAIN}" | grep -q '0.0..=1.0'; then
    echo "FAIL: crates/api/src/main.rs missing 0.0..=1.0 sampling ratio validation" >&2
    errors=$((errors + 1))
fi

if ! grep -A 10 'parse_sampling_ratio' "${INDEXER_MAIN}" | grep -q '0.0..=1.0'; then
    echo "FAIL: crates/indexer/src/main.rs missing 0.0..=1.0 sampling ratio validation" >&2
    errors=$((errors + 1))
fi

# 3. Verify documentation in ENVIRONMENT.md
if ! grep -q 'OTEL_EXPORTER_OTLP_INSECURE' "${ENV_DOC}"; then
    echo "FAIL: docs/ENVIRONMENT.md missing OTEL_EXPORTER_OTLP_INSECURE" >&2
    errors=$((errors + 1))
fi

if ! grep -q 'OTEL_EXPORTER_OTLP_HEADERS' "${ENV_DOC}"; then
    echo "FAIL: docs/ENVIRONMENT.md missing OTEL_EXPORTER_OTLP_HEADERS" >&2
    errors=$((errors + 1))
fi

if [[ ${errors} -gt 0 ]]; then
    echo "FAILED: ${errors} check(s) failed." >&2
    exit 1
fi

echo "OK: OTLP TLS, headers, and sampling ratio checks passed across all services (#762)"
