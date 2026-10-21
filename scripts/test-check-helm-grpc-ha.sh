#!/usr/bin/env bash
#
# Regression test for issue #765:
# infrastructure: Helm deploys the gRPC API with high availability:
# - Multi-replica (>=2)
# - PodDisruptionBudget (PDB)
# - HorizontalPodAutoscaler (HPA)
# - Topology spread constraints
# - Headless service and round_robin client load balancing

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VALUES_FILE="${REPO_ROOT}/helm/trident/values.yaml"
DEPLOY_FILE="${REPO_ROOT}/helm/trident/templates/grpc-api-deployment.yaml"
HPA_FILE="${REPO_ROOT}/helm/trident/templates/grpc-api-hpa.yaml"
CLIENT_GO="${REPO_ROOT}/services/api/grpc/client.go"

errors=0

echo "Checking issue #765 gRPC API HA configurations..."

# Extract grpcApi block from values.yaml
grpc_block=$(awk '/^grpcApi:/{flag=1; next} /^goApi:/{flag=0} flag' "${VALUES_FILE}")

# 1. Values.yaml verification
if ! echo "${grpc_block}" | grep -q 'replicaCount: 2'; then
    echo "FAIL: values.yaml grpcApi.replicaCount is not set to 2" >&2
    errors=$((errors + 1))
fi

if ! echo "${grpc_block}" | grep -q 'hpa:'; then
    echo "FAIL: values.yaml grpcApi missing hpa block" >&2
    errors=$((errors + 1))
fi

if ! echo "${grpc_block}" | grep -q 'podDisruptionBudget:'; then
    echo "FAIL: values.yaml grpcApi missing podDisruptionBudget block" >&2
    errors=$((errors + 1))
fi

if ! echo "${grpc_block}" | grep -q 'topologySpreadConstraints:'; then
    echo "FAIL: values.yaml grpcApi missing topologySpreadConstraints block" >&2
    errors=$((errors + 1))
fi

# 2. Template verification
if ! grep -q 'topologySpreadConstraints:' "${DEPLOY_FILE}"; then
    echo "FAIL: grpc-api-deployment.yaml missing topologySpreadConstraints" >&2
    errors=$((errors + 1))
fi

if ! grep -q 'grpc-api-headless' "${DEPLOY_FILE}"; then
    echo "FAIL: grpc-api-deployment.yaml missing headless service definition" >&2
    errors=$((errors + 1))
fi

if ! grep -q 'kind: PodDisruptionBudget' "${DEPLOY_FILE}"; then
    echo "FAIL: grpc-api-deployment.yaml missing PodDisruptionBudget" >&2
    errors=$((errors + 1))
fi

if [[ ! -f "${HPA_FILE}" ]]; then
    echo "FAIL: ${HPA_FILE} does not exist" >&2
    errors=$((errors + 1))
fi

# 3. Go client round_robin verification
if ! grep -q 'round_robin' "${CLIENT_GO}"; then
    echo "FAIL: services/api/grpc/client.go missing round_robin load balancing config" >&2
    errors=$((errors + 1))
fi

if [[ ${errors} -gt 0 ]]; then
    echo "FAILED: ${errors} check(s) failed." >&2
    exit 1
fi

echo "OK: gRPC API HA, PDB, HPA, and client load-balancing verified (#765)"
