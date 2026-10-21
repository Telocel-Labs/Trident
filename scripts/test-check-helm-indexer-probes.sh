#!/usr/bin/env bash
#
# Regression test for issue #763:
# Ensure Helm indexer liveness and readiness probes point to dedicated /healthz
# and /readyz endpoints on healthPort (8080) instead of Prometheus /metrics (9090).
# Also verifies that containerPort 8080 (health) is exposed in indexer-deployment.yaml.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VALUES_FILE="${REPO_ROOT}/helm/trident/values.yaml"
DEPLOY_FILE="${REPO_ROOT}/helm/trident/templates/indexer-deployment.yaml"

errors=0

# 1. Verify values.yaml has healthPort
if ! grep -q 'healthPort: 8080' "${VALUES_FILE}"; then
    echo "FAIL: helm/trident/values.yaml missing 'healthPort: 8080'" >&2
    errors=$((errors + 1))
fi

# 2. Verify indexer livenessProbe points to /healthz and port 8080
if ! grep -A 4 'livenessProbe:' "${VALUES_FILE}" | grep -q 'path: /healthz'; then
    echo "FAIL: indexer livenessProbe in values.yaml does not target /healthz" >&2
    errors=$((errors + 1))
fi

if ! grep -A 4 'livenessProbe:' "${VALUES_FILE}" | grep -q 'port: 8080'; then
    echo "FAIL: indexer livenessProbe in values.yaml does not target port 8080" >&2
    errors=$((errors + 1))
fi

# 3. Verify indexer readinessProbe points to /readyz and port 8080
if ! grep -A 4 'readinessProbe:' "${VALUES_FILE}" | grep -q 'path: /readyz'; then
    echo "FAIL: indexer readinessProbe in values.yaml does not target /readyz" >&2
    errors=$((errors + 1))
fi

if ! grep -A 4 'readinessProbe:' "${VALUES_FILE}" | grep -q 'port: 8080'; then
    echo "FAIL: indexer readinessProbe in values.yaml does not target port 8080" >&2
    errors=$((errors + 1))
fi

# 4. Ensure indexer probes in values.yaml no longer hit /metrics
if grep -B 1 -A 5 'livenessProbe:' "${VALUES_FILE}" | grep -q '/metrics'; then
    echo "FAIL: indexer livenessProbe in values.yaml must not target /metrics (#763)" >&2
    errors=$((errors + 1))
fi

# 5. Verify indexer-deployment.yaml exposes health port
if ! grep -q 'name: health' "${DEPLOY_FILE}"; then
    echo "FAIL: indexer-deployment.yaml missing 'name: health' port definition" >&2
    errors=$((errors + 1))
fi

if ! grep -q 'containerPort: {{ .Values.indexer.healthPort }}' "${DEPLOY_FILE}"; then
    echo "FAIL: indexer-deployment.yaml missing containerPort with healthPort" >&2
    errors=$((errors + 1))
fi

if [[ ${errors} -gt 0 ]]; then
    echo "FAILED: ${errors} probe check(s) failed." >&2
    exit 1
fi

echo "OK: Helm indexer health and readiness probes properly configured (#763)"
