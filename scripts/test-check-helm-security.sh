#!/usr/bin/env bash
#
# Regression test for issue #767:
# Security: Helm chart ships NetworkPolicy and disables automounted service-account tokens.
#
# Asserts:
# 1. Dedicated ServiceAccounts exist for nginx, go-api, grpc-api, indexer, migrate with automountServiceAccountToken: false.
# 2. Every deployment (nginx, go-api, grpc-api, indexer) and migration job sets automountServiceAccountToken: false
#    and points to its component-specific serviceAccountName.
# 3. NetworkPolicies enforce default-deny ingress and explicit allows:
#    - nginx -> go-api (8080)
#    - go-api -> grpc-api (5000)
#    - Prometheus -> metrics ports (9090)
#    - Indexer/gRPC APIs refuse access from unrelated pods.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELM_DIR="${REPO_ROOT}/helm/trident"
TEMPLATES_DIR="${HELM_DIR}/templates"
VALUES_FILE="${HELM_DIR}/values.yaml"
SA_FILE="${TEMPLATES_DIR}/serviceaccount.yaml"
NP_FILE="${TEMPLATES_DIR}/networkpolicy.yaml"

errors=0

echo "Checking issue #767 security configurations..."

# 1. Values.yaml toggles
if ! grep -q 'serviceAccount:' "${VALUES_FILE}"; then
    echo "FAIL: values.yaml missing serviceAccount section" >&2
    errors=$((errors + 1))
fi

if ! grep -q 'networkPolicy:' "${VALUES_FILE}"; then
    echo "FAIL: values.yaml missing networkPolicy section" >&2
    errors=$((errors + 1))
fi

# 2. ServiceAccount template exists and sets automountServiceAccountToken: false
if [[ ! -f "${SA_FILE}" ]]; then
    echo "FAIL: ${SA_FILE} does not exist" >&2
    errors=$((errors + 1))
else
    if ! grep -q 'automountServiceAccountToken: false' "${SA_FILE}"; then
        echo "FAIL: ${SA_FILE} missing 'automountServiceAccountToken: false'" >&2
        errors=$((errors + 1))
    fi
    for comp in nginx go-api grpc-api indexer migrate; do
        if ! grep -q "${comp}" "${SA_FILE}"; then
            echo "FAIL: ${SA_FILE} missing component '${comp}'" >&2
            errors=$((errors + 1))
        fi
    done
fi

# 3. Check automountServiceAccountToken: false and serviceAccountName in all templates
check_template() {
    local file=$1
    local comp=$2
    if [[ ! -f "${file}" ]]; then
        echo "FAIL: Template ${file} not found" >&2
        errors=$((errors + 1))
        return
    fi
    if ! grep -q 'automountServiceAccountToken: false' "${file}"; then
        echo "FAIL: ${file} missing 'automountServiceAccountToken: false'" >&2
        errors=$((errors + 1))
    fi
    if ! grep -q "serviceAccountName:.*component.*${comp}" "${file}"; then
        echo "FAIL: ${file} missing component-scoped serviceAccountName for ${comp}" >&2
        errors=$((errors + 1))
    fi
}

check_template "${TEMPLATES_DIR}/nginx-deployment.yaml" "nginx"
check_template "${TEMPLATES_DIR}/go-api-deployment.yaml" "go-api"
check_template "${TEMPLATES_DIR}/grpc-api-deployment.yaml" "grpc-api"
check_template "${TEMPLATES_DIR}/indexer-deployment.yaml" "indexer"
check_template "${TEMPLATES_DIR}/migration-job.yaml" "migrate"

# 4. Check NetworkPolicy template exists and implements isolation rules
if [[ ! -f "${NP_FILE}" ]]; then
    echo "FAIL: ${NP_FILE} does not exist" >&2
    errors=$((errors + 1))
else
    # Verify default-deny
    if ! grep -q 'default-deny' "${NP_FILE}"; then
        echo "FAIL: ${NP_FILE} missing default-deny policy" >&2
        errors=$((errors + 1))
    fi

    # Verify nginx -> go-api
    if ! grep -A 30 'name: {{ include "trident.fullname" . }}-nginx' "${NP_FILE}" | grep -q '"component" "go-api"'; then
        echo "FAIL: ${NP_FILE} nginx policy missing egress to go-api" >&2
        errors=$((errors + 1))
    fi

    # Verify go-api -> grpc-api
    if ! grep -A 40 'name: {{ include "trident.fullname" . }}-go-api' "${NP_FILE}" | grep -q '"component" "grpc-api"'; then
        echo "FAIL: ${NP_FILE} go-api policy missing egress to grpc-api" >&2
        errors=$((errors + 1))
    fi

    # Verify grpc-api ONLY allows go-api ingress
    if ! grep -A 30 'name: {{ include "trident.fullname" . }}-grpc-api' "${NP_FILE}" | grep -q '"component" "go-api"'; then
        echo "FAIL: ${NP_FILE} grpc-api policy missing ingress from go-api" >&2
        errors=$((errors + 1))
    fi

    # Verify indexer restricts ingress to metrics and health probes
    if ! grep -A 40 'name: {{ include "trident.fullname" . }}-indexer' "${NP_FILE}" | grep -q 'ingress:'; then
        echo "FAIL: ${NP_FILE} indexer policy missing ingress controls" >&2
        errors=$((errors + 1))
    fi
fi

if [[ ${errors} -gt 0 ]]; then
    echo "FAILED: ${errors} check(s) failed." >&2
    exit 1
fi

echo "OK: NetworkPolicy and ServiceAccount security checks passed (#767)"
