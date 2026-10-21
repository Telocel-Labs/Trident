#!/usr/bin/env bash
#
# Fail when a Prometheus alert name is defined more than once across the
# configured rule files or within the same file (issue #769).
#
# Why this exists
# ----------------
# promtool check rules validates PromQL and YAML syntax, but does not flag
# duplicate alert names. When an alert name is repeated (e.g. across groups
# or within the same file), Alertmanager receives multiple firing alerts for
# the same underlying condition, leading to duplicate on-call pages and
# configuration divergence.
#
# Usage:
#   scripts/check-alert-names-unique.sh [file1.yml file2.yml ...]
#
# Defaults to:
#   monitoring/alerts.yml
#   observability/burn-rate-alerts.yml
#   observability/rpc-alerts.yml
#
# Exit codes:
#   0 - all alert names are strictly unique
#   1 - one or more duplicate alert names found
#   2 - usage or file not found error

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if [ $# -gt 0 ]; then
  RULE_FILES=("$@")
else
  RULE_FILES=(
    "monitoring/alerts.yml"
    "observability/burn-rate-alerts.yml"
    "observability/rpc-alerts.yml"
  )
fi

for f in "${RULE_FILES[@]}"; do
  if [ ! -f "$f" ]; then
    echo "error: rule file $f not found" >&2
    exit 2
  fi
done

PYTHON="${PYTHON:-}"
if [ -z "$PYTHON" ]; then
  if command -v python3 >/dev/null 2>&1 && python3 -c "import sys" >/dev/null 2>&1; then
    PYTHON="python3"
  else
    PYTHON="python"
  fi
fi

"$PYTHON" - "${RULE_FILES[@]}" <<'PY'
import sys
from collections import defaultdict
import yaml

rule_paths = sys.argv[1:]
seen = defaultdict(list)
total_alerts = 0

for rule_path in rule_paths:
    with open(rule_path, encoding="utf-8") as fh:
        doc = yaml.safe_load(fh) or {}

    for group in doc.get("groups", []) or []:
        group_name = group.get("name", "<unnamed group>")
        for rule in group.get("rules", []) or []:
            alert_name = rule.get("alert")
            if not alert_name:
                # Recording rule (record:), not an alert.
                continue
            total_alerts += 1
            seen[alert_name].append((rule_path, group_name))

duplicates = {name: occ for name, occ in seen.items() if len(occ) > 1}

if duplicates:
    print(f"::error::{len(duplicates)} duplicate alert name(s) found across {len(rule_paths)} rule file(s):")
    for name, occurrences in sorted(duplicates.items()):
        details = ", ".join(f"'{f}' (group '{g}')" for f, g in occurrences)
        print(f"  - alert '{name}' defined {len(occurrences)} times: {details}")
    sys.exit(1)

print(f"OK: all {total_alerts} alert rule(s) across {len(rule_paths)} file(s) have unique names ({len(seen)} unique)")
PY
