#!/usr/bin/env bash
#
# Unit tests for Helm nginx HA, PDB, topology spread and digest pinning (issue #766).
#
# Verifies that:
#   1. values.yaml pins nginx image by digest and sets replicaCount >= 2.
#   2. values.yaml and values-prod.yaml configure non-blocking PDB (maxUnavailable / minAvailable).
#   3. values.yaml configures topologySpreadConstraints for nginx.
#   4. templates/nginx-deployment.yaml renders topologySpreadConstraints and digest-pinned image.
#   5. If helm CLI is available, renders templates and asserts the generated manifest fields.
#
# Usage:
#   scripts/test-check-helm-nginx.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PYTHON="${PYTHON:-}"
if [ -z "$PYTHON" ]; then
  if command -v python3 >/dev/null 2>&1 && python3 -c "import sys" >/dev/null 2>&1; then
    PYTHON="python3"
  else
    PYTHON="python"
  fi
fi

"$PYTHON" - <<'PYEOF'
import sys, os, yaml

values_path = "helm/trident/values.yaml"
prod_values_path = "helm/trident/values-prod.yaml"
template_path = "helm/trident/templates/nginx-deployment.yaml"

with open(values_path, "r", encoding="utf-8-sig") as f:
    values = yaml.safe_load(f)

nginx_val = values.get("nginx", {})

# 1. replicaCount >= 2
replicas = nginx_val.get("replicaCount", 1)
assert replicas >= 2, f"Expected nginx.replicaCount >= 2, got {replicas}"
print(f"PASS: values.yaml nginx.replicaCount is {replicas} (>= 2)")

# 2. Image digest pinned
img = nginx_val.get("image", {})
digest = img.get("digest", "")
assert digest.startswith("sha256:") and len(digest) == 71, f"Expected valid sha256 digest, got {digest}"
print(f"PASS: values.yaml nginx image digest pinned to {digest}")

# 3. Topology spread constraints
tsc = nginx_val.get("topologySpreadConstraints", {})
assert tsc.get("enabled") is True, "Expected nginx.topologySpreadConstraints.enabled to be true"
print("PASS: values.yaml nginx topologySpreadConstraints enabled")

# 4. PDB configured
pdb = nginx_val.get("podDisruptionBudget", {})
assert pdb.get("enabled") is True, "Expected nginx.podDisruptionBudget.enabled to be true"
assert pdb.get("maxUnavailable") is not None or pdb.get("minAvailable") is not None, "Expected maxUnavailable or minAvailable in PDB"
print("PASS: values.yaml nginx PDB configured with maxUnavailable/minAvailable")

# 5. Check values-prod.yaml
with open(prod_values_path, "r", encoding="utf-8-sig") as f:
    prod_val = yaml.safe_load(f)
prod_nginx = prod_val.get("nginx", {})
assert prod_nginx.get("replicaCount", 2) >= 2, f"Expected prod nginx.replicaCount >= 2, got {prod_nginx.get('replicaCount')}"
prod_pdb = prod_nginx.get("podDisruptionBudget", {})
assert prod_pdb.get("enabled") is True, "Expected prod nginx PDB to be enabled"
assert prod_pdb.get("maxUnavailable") is not None or prod_pdb.get("minAvailable") is not None, "Expected prod PDB maxUnavailable/minAvailable"
print("PASS: values-prod.yaml nginx replicaCount >= 2 and PDB configured")

# 6. Check template contents
with open(template_path, "r", encoding="utf-8") as f:
    tmpl = f.read()
assert "topologySpreadConstraints" in tmpl, "Expected topologySpreadConstraints in nginx-deployment.yaml"
assert "@{{ .Values.nginx.image.digest }}" in tmpl or ".Values.nginx.image.digest" in tmpl, "Expected digest rendering in nginx-deployment.yaml"
assert "maxUnavailable" in tmpl, "Expected maxUnavailable in nginx PDB template"
print("PASS: nginx-deployment.yaml template contains topologySpreadConstraints, image digest, and maxUnavailable PDB")

PYEOF

# If helm is installed, test real rendering
if command -v helm >/dev/null 2>&1; then
  echo "Testing helm template rendering..."
  RENDERED=$(helm template trident helm/trident)
  echo "$RENDERED" | grep -q "kind: PodDisruptionBudget"
  echo "$RENDERED" | grep -q "maxUnavailable: 1"
  echo "$RENDERED" | grep -q "topologySpreadConstraints:"
  echo "$RENDERED" | grep -q "@sha256:"
  echo "PASS: helm template successfully rendered nginx HA, PDB, and digest"

  # Also assert that single-replica mode disables PDB to prevent node drain lockups
  RENDERED_SINGLE=$(helm template trident helm/trident --set nginx.replicaCount=1)
  if echo "$RENDERED_SINGLE" | grep -A 5 "kind: PodDisruptionBudget" | grep -q "nginx"; then
    echo "FAIL: single replica rendered nginx PDB which would block node drain" >&2
    exit 1
  fi
  echo "PASS: single replica correctly omits nginx PDB to prevent node drain lockups"
fi

echo "All Helm nginx HA tests passed."
