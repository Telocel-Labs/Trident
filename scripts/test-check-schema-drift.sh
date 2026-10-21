#!/usr/bin/env bash
#
# Unit/integration tests for scripts/check-schema-drift.sh (issue #688).
#
# Verifies that check-schema-drift.sh actually catches structural drift when
# schema.sql and the migration chain disagree, rather than assuming it works.
#
# Deliberately introduces:
#   1. Extra column in schema.sql
#   2. Missing column in schema.sql
#   3. Extra migration not reflected in schema.sql
#   4. Extra index in schema.sql
#
# And proves the drift checker rejects each one with exit code 1 and surfaces
# the exact offending object in the diff output.
#
# Usage:
#   DATABASE_URL=postgres://user:pass@localhost:5432/scratch scripts/test-check-schema-drift.sh
#

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECKER="$REPO_ROOT/scripts/check-schema-drift.sh"

if [ ! -x "$CHECKER" ]; then
    echo "error: $CHECKER not found or not executable" >&2
    exit 2
fi

DB_URL="${DATABASE_URL:-}"
if [ -z "$DB_URL" ]; then
    if [ -n "${REQUIRE_TEST_SERVICES:-}" ]; then
        echo "error: DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set" >&2
        exit 1
    fi
    echo "SKIP: DATABASE_URL not set"
    exit 0
fi

command -v psql >/dev/null 2>&1 || {
    echo "error: psql is required" >&2
    exit 2
}

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

failures=0

assert_fails_with() {
    local desc="$1" want="$2"
    shift 2
    local out code=0
    set +e
    out="$("$@" 2>&1)"
    code=$?
    set -e
    if [ "$code" -eq 0 ]; then
        echo "FAIL: $desc — expected failure (non-zero exit), but it passed" >&2
        failures=$((failures + 1))
    elif [[ "$out" != *"$want"* ]]; then
        echo "FAIL: $desc — failed as expected, but output missing '$want':" >&2
        echo "$out" >&2
        failures=$((failures + 1))
    else
        echo "PASS: $desc"
    fi
}

assert_passes() {
    local desc="$1"
    shift
    local out code=0
    set +e
    out="$("$@" 2>&1)"
    code=$?
    set -e
    if [ "$code" -ne 0 ]; then
        echo "FAIL: $desc — expected pass (exit 0), but got code $code:" >&2
        echo "$out" >&2
        failures=$((failures + 1))
    else
        echo "PASS: $desc"
    fi
}

echo "=== 1. Baseline: checking authentic schema.sql and migrations ==="
assert_passes "Authentic schema.sql matches migration chain" bash "$CHECKER"

echo "=== 2. Drift test: extra column in schema.sql ==="
SCHEMA_EXTRA_COL="$WORKDIR/schema_extra_col.sql"
python3 -c "
with open('$REPO_ROOT/database/schema.sql', 'r', encoding='utf-8') as f:
    text = f.read()
modified = text.replace('status text NOT NULL,', 'status text NOT NULL,\n    drift_extra_col text,', 1)
with open('$SCHEMA_EXTRA_COL', 'w', encoding='utf-8') as f:
    f.write(modified)
"
assert_fails_with "Extra column in schema.sql is detected" "drift_extra_col" \
    env SCHEMA_FILE="$SCHEMA_EXTRA_COL" bash "$CHECKER"

echo "=== 3. Drift test: missing column in schema.sql ==="
SCHEMA_MISSING_COL="$WORKDIR/schema_missing_col.sql"
python3 -c "
with open('$REPO_ROOT/database/schema.sql', 'r', encoding='utf-8') as f:
    text = f.read()
# Remove attempts column from webhook_deliveries
modified = text.replace('attempts integer NOT NULL DEFAULT 0,', '', 1)
with open('$SCHEMA_MISSING_COL', 'w', encoding='utf-8') as f:
    f.write(modified)
"
assert_fails_with "Missing column in schema.sql is detected" "attempts" \
    env SCHEMA_FILE="$SCHEMA_MISSING_COL" bash "$CHECKER"

echo "=== 4. Drift test: extra unapplied migration file ==="
MIGRATIONS_EXTRA="$WORKDIR/migrations"
mkdir -p "$MIGRATIONS_EXTRA"
cp "$REPO_ROOT/database/migrations"/*.sql "$MIGRATIONS_EXTRA/"
cat > "$MIGRATIONS_EXTRA/9999_drift_test_table.sql" <<'SQL'
CREATE TABLE drift_untracked_table (
    id serial PRIMARY KEY,
    description text NOT NULL
);
SQL
assert_fails_with "Extra migration not in schema.sql is detected" "drift_untracked_table" \
    env MIGRATIONS_DIR="$MIGRATIONS_EXTRA" bash "$CHECKER"

echo "=== 5. Drift test: extra index in schema.sql ==="
SCHEMA_EXTRA_IDX="$WORKDIR/schema_extra_idx.sql"
cp "$REPO_ROOT/database/schema.sql" "$SCHEMA_EXTRA_IDX"
echo "CREATE INDEX idx_drift_extra ON api_keys (created_at);" >> "$SCHEMA_EXTRA_IDX"
assert_fails_with "Extra index in schema.sql is detected" "idx_drift_extra" \
    env SCHEMA_FILE="$SCHEMA_EXTRA_IDX" bash "$CHECKER"

if [ "$failures" -gt 0 ]; then
    echo "ERROR: $failures test(s) failed." >&2
    exit 1
fi

echo "OK: All drift detection verification tests passed."
exit 0
