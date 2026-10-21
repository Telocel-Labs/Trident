#!/usr/bin/env bash
#
# Verification tests for Helm monitoring templates and Prometheus Operator integration (issue #768).
#
# Asserts that:
#   1. ServiceMonitor, PodMonitor, and PrometheusRule templates exist.
#   2. Alert rule files are packaged under helm/trident/rules/.
#   3. ServiceMonitor relabels job -> trident-api.
#   4. PodMonitor relabels job -> trident-indexer.
#   5. values.yaml contains monitoring configuration with a safe default (disabled).
#   6. If helm is available, lints the chart and verifies template rendering.
#
# Usage:
#   ./scripts/test-check-helm-monitoring.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART_DIR="$REPO_ROOT/helm/trident"

echo "=== Running Helm monitoring verification (issue #768) ==="

# 1. Required template files
for f in "templates/service-monitor.yaml" "templates/pod-monitor.yaml" "templates/prometheusrule.yaml"; do
    if [ ! -f "$CHART_DIR/$f" ]; then
        echo "FAIL: Missing template $f" >&2
        exit 1
    fi
    echo "  [OK] Found $f"
done

# 2. Packaged alert rules
for r in "alerts.yml" "burn-rate-alerts.yml" "rpc-alerts.yml"; do
    if [ ! -f "$CHART_DIR/rules/$r" ]; then
        echo "FAIL: Missing rule file rules/$r" >&2
        exit 1
    fi
    echo "  [OK] Found rules/$r"
done

# 3. Relabeling verification
grep -q "replacement: trident-api" "$CHART_DIR/templates/service-monitor.yaml" || {
    echo "FAIL: service-monitor.yaml missing job replacement 'trident-api'" >&2
    exit 1
}
echo "  [OK] ServiceMonitor sets job=trident-api"

grep -q "replacement: trident-indexer" "$CHART_DIR/templates/pod-monitor.yaml" || {
    echo "FAIL: pod-monitor.yaml missing job replacement 'trident-indexer'" >&2
    exit 1
}
echo "  [OK] PodMonitor sets job=trident-indexer"

# 4. Values.yaml verification
grep -q "monitoring:" "$CHART_DIR/values.yaml" || {
    echo "FAIL: values.yaml missing monitoring block" >&2
    exit 1
}
grep -q "enabled: false" "$CHART_DIR/values.yaml" || {
    echo "FAIL: values.yaml monitoring.enabled should default to false" >&2
    exit 1
}
echo "  [OK] values.yaml monitoring default configuration valid"

# 5. Helm lint & template rendering if helm binary is present
if command -v helm >/dev/null 2>&1; then
    echo "Testing with helm CLI..."
    helm lint "$CHART_DIR"
    helm lint "$CHART_DIR" --set monitoring.enabled=true

    RENDERED=$(helm template trident-test "$CHART_DIR" --set monitoring.enabled=true)
    echo "$RENDERED" | grep -q "kind: ServiceMonitor" || { echo "FAIL: ServiceMonitor not rendered" >&2; exit 1; }
    echo "$RENDERED" | grep -q "kind: PodMonitor" || { echo "FAIL: PodMonitor not rendered" >&2; exit 1; }
    echo "$RENDERED" | grep -q "kind: PrometheusRule" || { echo "FAIL: PrometheusRule not rendered" >&2; exit 1; }
    echo "  [OK] Helm template renders ServiceMonitor, PodMonitor, and PrometheusRule"
else
    echo "  [INFO] helm CLI not found in path; skipping live render check"
fi

echo "=== All Helm monitoring checks passed! ==="
