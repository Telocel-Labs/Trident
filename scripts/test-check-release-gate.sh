#!/usr/bin/env bash
#
# Unit tests for scripts/check-release-gate.sh (Issue #781)
#
# Verifies:
# 1. Missing CI check-runs rejects the release.
# 2. Failing CI check-runs rejects the release.
# 3. In-progress CI check-runs rejects the release.
# 4. Successful CI check-runs allows the release.
# 5. Non-ancestor commit on protected branch rejects the release.
# 6. Self-referential Release check-runs are ignored.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECKER="$REPO_ROOT/scripts/check-release-gate.sh"

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

failures=0

assert_fails_with() {
    local desc="$1" want="$2"; shift 2
    local out
    if out=$(bash "$CHECKER" "$@" 2>&1); then
        echo "FAIL: $desc — expected gate to fail, but it passed"
        echo "$out"
        failures=$((failures + 1))
    elif [[ "$out" == *"$want"* ]]; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — failed, but did not match '$want'"
        echo "Got output:"
        echo "$out"
        failures=$((failures + 1))
    fi
}

assert_passes() {
    local desc="$1"; shift
    local out
    if out=$(bash "$CHECKER" "$@" 2>&1); then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — expected gate to pass, but it failed"
        echo "Got output:"
        echo "$out"
        failures=$((failures + 1))
    fi
}

# Fixture 1: No check runs
cat > "$WORKDIR/empty-runs.json" <<'EOF'
{
  "total_count": 0,
  "check_runs": []
}
EOF

# Fixture 2: Failed check run
cat > "$WORKDIR/failed-run.json" <<'EOF'
{
  "total_count": 2,
  "check_runs": [
    {
      "name": "Rust",
      "status": "completed",
      "conclusion": "failure"
    },
    {
      "name": "Go",
      "status": "completed",
      "conclusion": "success"
    }
  ]
}
EOF

# Fixture 3: In progress check run
cat > "$WORKDIR/in-progress-run.json" <<'EOF'
{
  "total_count": 2,
  "check_runs": [
    {
      "name": "Rust",
      "status": "in_progress",
      "conclusion": null
    },
    {
      "name": "Go",
      "status": "completed",
      "conclusion": "success"
    }
  ]
}
EOF

# Fixture 4: All passed
cat > "$WORKDIR/success-runs.json" <<'EOF'
{
  "total_count": 3,
  "check_runs": [
    {
      "name": "Rust",
      "status": "completed",
      "conclusion": "success"
    },
    {
      "name": "Go",
      "status": "completed",
      "conclusion": "success"
    },
    {
      "name": "Release",
      "status": "in_progress",
      "conclusion": null
    }
  ]
}
EOF

echo "Running check-release-gate.sh tests..."

# Test 1: Empty runs should fail
assert_fails_with "Empty CI runs rejected" \
    "No completed CI check runs found" \
    --check-runs-file "$WORKDIR/empty-runs.json" --skip-branch-check

# Test 2: Failed run should fail
assert_fails_with "Failed CI run rejected" \
    "CI checks failed" \
    --check-runs-file "$WORKDIR/failed-run.json" --skip-branch-check

# Test 3: In-progress run should fail
assert_fails_with "In-progress CI run rejected" \
    "CI checks are still in progress" \
    --check-runs-file "$WORKDIR/in-progress-run.json" --skip-branch-check

# Test 4: All passed should pass
assert_passes "Successful CI runs accepted" \
    --check-runs-file "$WORKDIR/success-runs.json" --skip-branch-check

# Test 5: Branch check with non-existent ancestor branch should fail
assert_fails_with "Commit not on protected branch rejected" \
    "does not belong to any allowed release branch" \
    --check-runs-file "$WORKDIR/success-runs.json" --branches "nonexistent-branch-xyz"

echo "----------------------------------------------------------"
if [ "$failures" -eq 0 ]; then
    echo "All tests passed successfully!"
    exit 0
else
    echo "$failures test(s) failed."
    exit 1
fi
