#!/usr/bin/env bash
#
# Unit tests for scripts/check-alert-names-unique.sh (issue #769).
#
# Asserts that the uniqueness checker properly detects:
#   1. Valid rule sets where all alert names are unique across files.
#   2. Duplicates within a single rule file.
#   3. Duplicates across different rule files.
#   4. Ignoring recording rules (record:) without alerts.
#
# Usage:
#   scripts/test-check-alert-names-unique.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECKER="$REPO_ROOT/scripts/check-alert-names-unique.sh"

if [ ! -f "$CHECKER" ]; then
    echo "error: $CHECKER not found" >&2
    exit 2
fi

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

failures=0

assert_pass() {
    local desc="$1"
    shift
    if out=$(bash "$CHECKER" "$@" 2>&1); then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — expected checker to pass, got: $out"
        failures=$((failures + 1))
    fi
}

assert_fail() {
    local desc="$1"
    local want_snippet="$2"
    shift 2
    if out=$(bash "$CHECKER" "$@" 2>&1); then
        echo "FAIL: $desc — expected checker to fail, but it passed"
        failures=$((failures + 1))
    elif [[ "$out" == *"$want_snippet"* ]]; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — failed, but not for expected reason ($want_snippet): $out"
        failures=$((failures + 1))
    fi
}

# --- Case 1: valid single file with unique alerts ---
cat > "$WORKDIR/valid_single.yml" <<'EOF'
groups:
  - name: group1
    rules:
      - alert: AlertOne
        expr: up == 0
      - alert: AlertTwo
        expr: errors > 0
EOF
assert_pass "valid single file passes" "$WORKDIR/valid_single.yml"

# --- Case 2: valid multiple files with mutually exclusive alerts ---
cat > "$WORKDIR/valid_file_a.yml" <<'EOF'
groups:
  - name: group_a
    rules:
      - alert: ServiceDown
        expr: up == 0
EOF
cat > "$WORKDIR/valid_file_b.yml" <<'EOF'
groups:
  - name: group_b
    rules:
      - alert: DiskFull
        expr: disk_free < 0.1
EOF
assert_pass "valid multi-file set passes" "$WORKDIR/valid_file_a.yml" "$WORKDIR/valid_file_b.yml"

# --- Case 3: recording rules only without alerts are skipped ---
cat > "$WORKDIR/recording_only.yml" <<'EOF'
groups:
  - name: recording_group
    rules:
      - record: job:rate5m
        expr: rate(requests[5m])
EOF
assert_pass "recording rules only passes" "$WORKDIR/recording_only.yml"

# --- Case 4: duplicate alert within the same file fails ---
cat > "$WORKDIR/duplicate_intra_file.yml" <<'EOF'
groups:
  - name: group1
    rules:
      - alert: DuplicateAlert
        expr: up == 0
      - alert: AnotherAlert
        expr: errors > 0
      - alert: DuplicateAlert
        expr: up == 0
EOF
assert_fail "duplicate within same file caught" "alert 'DuplicateAlert' defined 2 times" "$WORKDIR/duplicate_intra_file.yml"

# --- Case 5: duplicate alert across different files fails ---
cat > "$WORKDIR/dup_file_1.yml" <<'EOF'
groups:
  - name: group1
    rules:
      - alert: SharedAlertName
        expr: up == 0
EOF
cat > "$WORKDIR/dup_file_2.yml" <<'EOF'
groups:
  - name: group2
    rules:
      - alert: SharedAlertName
        expr: fallback > 0
EOF
assert_fail "duplicate across files caught" "alert 'SharedAlertName' defined 2 times" "$WORKDIR/dup_file_1.yml" "$WORKDIR/dup_file_2.yml"

echo
if [ "$failures" -gt 0 ]; then
    echo "$failures test(s) failed"
    exit 1
fi
echo "All check-alert-names-unique.sh tests passed"
