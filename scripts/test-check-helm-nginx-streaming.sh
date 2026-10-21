#!/usr/bin/env bash
#
# Unit tests for Helm and Docker nginx streaming configs and /internal/ blocking (issue #799).
#
# Asserts that:
#   1. docker/nginx/nginx.conf and helm nginx ConfigMap configure high worker_connections (>= 10240).
#   2. Both configs route /ws with WebSocket Upgrade and Connection headers.
#   3. Both configs route /graphql with WebSocket Upgrade and Connection headers.
#   4. Both configs route /v1/events/stream with proxy_buffering off and 86400s timeout.
#   5. Both configs block /internal/ with deny all / 403.
#
# Usage:
#   scripts/test-check-helm-nginx-streaming.sh

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
import sys, re

docker_conf_path = "docker/nginx/nginx.conf"
helm_tmpl_path = "helm/trident/templates/nginx-deployment.yaml"

with open(docker_conf_path, "r", encoding="utf-8") as f:
    docker_conf = f.read()

with open(helm_tmpl_path, "r", encoding="utf-8") as f:
    helm_tmpl = f.read()

def verify_nginx_rules(name, text):
    # 1. worker_connections
    m = re.search(r"worker_connections\s+(\d+)", text)
    assert m, f"[{name}] worker_connections missing"
    conns = int(m.group(1))
    assert conns >= 10240, f"[{name}] worker_connections too low: {conns} (expected >= 10240)"
    print(f"PASS: [{name}] worker_connections is {conns}")

    # 2. /internal/ blocked
    assert re.search(r"location\s+/internal/\s*\{[^}]*deny\s+all", text, re.S), f"[{name}] /internal/ not denied"
    print(f"PASS: [{name}] /internal/ denies all access")

    # 3. /ws WebSocket upgrade
    assert "location /ws" in text, f"[{name}] /ws location missing"
    assert "proxy_set_header Upgrade" in text or "proxy_set_header   Upgrade" in text, f"[{name}] /ws Upgrade header missing"
    assert 'Connection "upgrade"' in text, f"[{name}] /ws Connection upgrade missing"
    print(f"PASS: [{name}] /ws forwards WebSocket upgrades")

    # 4. /graphql WebSocket upgrade
    assert "location /graphql" in text, f"[{name}] /graphql location missing"
    assert "proxy_set_header Upgrade" in text or "proxy_set_header   Upgrade" in text, f"[{name}] /graphql Upgrade header missing"
    assert 'Connection "upgrade"' in text, f"[{name}] /graphql Connection upgrade missing"
    print(f"PASS: [{name}] /graphql forwards WebSocket upgrades")

    # 5. /v1/events/stream unbuffered SSE
    assert "location /v1/events/stream" in text, f"[{name}] /v1/events/stream location missing"
    assert "proxy_buffering" in text and "off" in text, f"[{name}] /v1/events/stream proxy_buffering off missing"
    assert "86400s" in text, f"[{name}] /v1/events/stream read timeout 86400s missing"
    print(f"PASS: [{name}] /v1/events/stream configures unbuffered SSE with long idle timeout")

verify_nginx_rules("docker/nginx/nginx.conf", docker_conf)
verify_nginx_rules("helm nginx ConfigMap", helm_tmpl)

print("\nAll nginx streaming and security configuration assertions passed.")
PYEOF

echo "OK: nginx streaming and internal protection check completed successfully."
