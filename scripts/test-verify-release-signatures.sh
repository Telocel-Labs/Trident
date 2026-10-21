#!/usr/bin/env bash
#
# scripts/test-verify-release-signatures.sh
# Unit tests for scripts/verify-release-signatures.sh (issue #780).
#
# Usage:
#   scripts/test-verify-release-signatures.sh
#

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/verify-release-signatures.sh"

if [ ! -f "$SCRIPT" ]; then
    echo "error: $SCRIPT not found" >&2
    exit 2
fi

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

failures=0

assert_passes() {
    local desc="$1"
    shift
    if out=$("$@" 2>&1); then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — expected command to pass, got exit $?: $out"
        failures=$((failures + 1))
    fi
}

assert_fails_with() {
    local desc="$1"
    local want="$2"
    shift 2
    if out=$("$@" 2>&1); then
        echo "FAIL: $desc — expected failure, but command succeeded"
        failures=$((failures + 1))
    elif [[ "$out" == *"$want"* ]]; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc — failed, but output did not contain '$want': $out"
        failures=$((failures + 1))
    fi
}

# 1. Test --help
assert_passes "verify-release-signatures.sh --help displays help" \
    bash "$SCRIPT" --help

# 2. Test missing parameters
assert_fails_with "fails when neither --tag nor --image is provided" \
    "either --tag <tag> or --image <image-ref> must be provided" \
    bash "$SCRIPT"

# 3. Test dry-run with tag
assert_passes "dry run with --tag generates expected commands" \
    bash "$SCRIPT" --tag v0.2.0 --dry-run

dry_out=$(bash "$SCRIPT" --tag v0.2.0 --dry-run 2>&1)
if [[ "$dry_out" == *"Target count: 4"* && "$dry_out" == *"cosign verify"* && "$dry_out" == *"gh attestation verify"* ]]; then
    echo "PASS: dry-run output contains all default images and commands"
else
    echo "FAIL: dry-run output missing expected content: $dry_out"
    failures=$((failures + 1))
fi

# 4. Test dry-run with specific image
img_out=$(bash "$SCRIPT" --image ghcr.io/telocel-labs/trident-indexer:v0.2.0 --dry-run 2>&1)
if [[ "$img_out" == *"Target count: 1"* && "$img_out" == *"trident-indexer:v0.2.0"* ]]; then
    echo "PASS: single image dry run operates on single target"
else
    echo "FAIL: single image dry run missing expected target: $img_out"
    failures=$((failures + 1))
fi

# Setup mock executables
MOCK_BIN_DIR="$WORKDIR/bin"
mkdir -p "$MOCK_BIN_DIR"

cat > "$MOCK_BIN_DIR/mock-cosign-success" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$MOCK_BIN_DIR/mock-cosign-success"

cat > "$MOCK_BIN_DIR/mock-cosign-fail" <<'EOF'
#!/usr/bin/env bash
echo "signature verification failed" >&2
exit 1
EOF
chmod +x "$MOCK_BIN_DIR/mock-cosign-fail"

cat > "$MOCK_BIN_DIR/mock-gh-success" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$MOCK_BIN_DIR/mock-gh-success"

cat > "$MOCK_BIN_DIR/mock-gh-fail" <<'EOF'
#!/usr/bin/env bash
echo "attestation not found" >&2
exit 1
EOF
chmod +x "$MOCK_BIN_DIR/mock-gh-fail"

# 5. Test mock success
assert_passes "verifications succeed when cosign and gh return success" \
    env COSIGN_BIN="$MOCK_BIN_DIR/mock-cosign-success" GH_BIN="$MOCK_BIN_DIR/mock-gh-success" \
    bash "$SCRIPT" --image ghcr.io/telocel-labs/trident-indexer:v0.2.0

# 6. Test cosign failure
assert_fails_with "detects cosign verification failure" \
    "Cosign verification FAILED" \
    env COSIGN_BIN="$MOCK_BIN_DIR/mock-cosign-fail" GH_BIN="$MOCK_BIN_DIR/mock-gh-success" \
    bash "$SCRIPT" --image ghcr.io/telocel-labs/trident-indexer:v0.2.0

# 7. Test gh attestation failure
assert_fails_with "detects gh attestation verification failure" \
    "Attestation verification FAILED" \
    env COSIGN_BIN="$MOCK_BIN_DIR/mock-cosign-success" GH_BIN="$MOCK_BIN_DIR/mock-gh-fail" \
    bash "$SCRIPT" --image ghcr.io/telocel-labs/trident-indexer:v0.2.0

# 8. Test skip flags
assert_passes "skip cosign bypasses cosign failure" \
    env COSIGN_BIN="$MOCK_BIN_DIR/mock-cosign-fail" GH_BIN="$MOCK_BIN_DIR/mock-gh-success" \
    bash "$SCRIPT" --image ghcr.io/telocel-labs/trident-indexer:v0.2.0 --skip-cosign

assert_passes "skip attestation bypasses gh failure" \
    env COSIGN_BIN="$MOCK_BIN_DIR/mock-cosign-success" GH_BIN="$MOCK_BIN_DIR/mock-gh-fail" \
    bash "$SCRIPT" --image ghcr.io/telocel-labs/trident-indexer:v0.2.0 --skip-attestation

echo
if [ "$failures" -eq 0 ]; then
    echo "All verify-release-signatures tests PASSED."
    exit 0
else
    echo "$failures test(s) FAILED." >&2
    exit 1
fi
