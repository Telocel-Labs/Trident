#!/usr/bin/env bash
#
# check-release-gate.sh: Ensure tagged releases meet CI and branch requirements.
# Resolves Issue #781:
# - Gate release jobs on a successful CI run for the tag's SHA.
# - Restrict tag creation to protected branches (dev, main).
# - Fail if CI runs are missing, failing, or still in progress.
#
# Usage:
#   scripts/check-release-gate.sh [options]
# Options:
#   --sha <sha>               Commit SHA to verify (default: $GITHUB_SHA or HEAD)
#   --repo <owner/repo>       GitHub repository (default: $GITHUB_REPOSITORY)
#   --branches <b1,b2>        Allowed base branches (default: dev,main)
#   --check-runs-file <file>  Read check-runs JSON from file (for testing/mocking)
#   --skip-branch-check       Skip git ancestry check
#   --token <token>           GitHub token (default: $GITHUB_TOKEN or $GH_TOKEN)

set -euo pipefail

COMMIT_SHA="${GITHUB_SHA:-}"
REPO="${GITHUB_REPOSITORY:-}"
ALLOWED_BRANCHES="dev,main"
CHECK_RUNS_FILE=""
SKIP_BRANCH_CHECK=false
TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --sha)
            COMMIT_SHA="$2"
            shift 2
            ;;
        --repo)
            REPO="$2"
            shift 2
            ;;
        --branches)
            ALLOWED_BRANCHES="$2"
            shift 2
            ;;
        --check-runs-file)
            CHECK_RUNS_FILE="$2"
            shift 2
            ;;
        --skip-branch-check)
            SKIP_BRANCH_CHECK=true
            shift
            ;;
        --token)
            TOKEN="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [--sha <sha>] [--repo <owner/repo>] [--branches <dev,main>] [--check-runs-file <path>] [--skip-branch-check]"
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            exit 2
            ;;
    esac
done

if [[ -z "$COMMIT_SHA" ]]; then
    if git rev-parse --verify HEAD >/dev/null 2>&1; then
        COMMIT_SHA=$(git rev-parse HEAD)
    else
        echo "::error::No commit SHA provided and not in a git repository" >&2
        exit 1
    fi
fi

errors=()

# -----------------------------------------------------------------------------
# 1. Branch Ancestry Verification
# -----------------------------------------------------------------------------
if [[ "$SKIP_BRANCH_CHECK" == "false" ]]; then
    IFS=',' read -ra BRANCH_LIST <<< "$ALLOWED_BRANCHES"
    found_in_branch=false
    checked_branches=()

    for branch in "${BRANCH_LIST[@]}"; do
        branch=$(echo "$branch" | xargs)
        [[ -z "$branch" ]] && continue
        checked_branches+=("$branch")

        # Try local branch or origin remote branch
        target_ref=""
        if git rev-parse --verify "origin/$branch" >/dev/null 2>&1; then
            target_ref="origin/$branch"
        elif git rev-parse --verify "$branch" >/dev/null 2>&1; then
            target_ref="$branch"
        fi

        if [[ -n "$target_ref" ]]; then
            if git merge-base --is-ancestor "$COMMIT_SHA" "$target_ref" 2>/dev/null; then
                found_in_branch=true
                echo "✓ Commit $COMMIT_SHA is an ancestor of $target_ref"
                break
            fi
        fi
    done

    if [[ "$found_in_branch" == "false" ]]; then
        errors+=("Commit $COMMIT_SHA does not belong to any allowed release branch (${checked_branches[*]}). Release tags must be created from protected branches.")
    fi
fi

# -----------------------------------------------------------------------------
# 2. Check-Runs Verification
# -----------------------------------------------------------------------------
json_payload=""

if [[ -n "$CHECK_RUNS_FILE" ]]; then
    if [[ ! -f "$CHECK_RUNS_FILE" ]]; then
        echo "::error::Check-runs file not found: $CHECK_RUNS_FILE" >&2
        exit 1
    fi
    json_payload=$(cat "$CHECK_RUNS_FILE")
else
    if [[ -z "$REPO" ]]; then
        echo "::error::GITHUB_REPOSITORY or --repo is required when --check-runs-file is not provided" >&2
        exit 1
    fi

    # Retrieve check runs via gh CLI or curl
    if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
        json_payload=$(gh api "repos/$REPO/commits/$COMMIT_SHA/check-runs?per_page=100")
    elif [[ -n "$TOKEN" ]]; then
        json_payload=$(curl -sS -f \
            -H "Authorization: Bearer $TOKEN" \
            -H "Accept: application/vnd.github+json" \
            "https://api.github.com/repos/$REPO/commits/$COMMIT_SHA/check-runs?per_page=100")
    else
        echo "::error::Either gh CLI must be authenticated or GITHUB_TOKEN/--token must be supplied" >&2
        exit 1
    fi
fi

# Parse check runs using python (available in our environments)
parse_result=$(python3 -c '
import json, sys

try:
    data = json.loads(sys.stdin.read())
except Exception as e:
    print(f"INVALID_JSON: {e}")
    sys.exit(0)

check_runs = data.get("check_runs", [])
if not check_runs and isinstance(data, list):
    check_runs = data

# Ignore the release workflow itself
filtered_runs = [
    r for r in check_runs
    if r.get("name") not in ["Release", "release", "verify-ci", "Release Gate", "verify-release-gate"]
]

if not filtered_runs:
    print("NO_CHECK_RUNS: No CI check runs found for this commit")
    sys.exit(0)

in_progress = []
failures = []
passed = []

for r in filtered_runs:
    name = r.get("name", "unknown")
    status = r.get("status")
    conclusion = r.get("conclusion")

    if status != "completed":
        in_progress.append(f"{name} (status={status})")
    elif conclusion in ["failure", "timed_out", "cancelled", "action_required", "stale"]:
        failures.append(f"{name} (conclusion={conclusion})")
    elif conclusion in ["success", "neutral", "skipped"]:
        passed.append(name)

if failures:
    print("FAILURES: " + "; ".join(failures))
elif in_progress:
    print("IN_PROGRESS: " + "; ".join(in_progress))
else:
    print(f"SUCCESS: {len(passed)} checks passed")
' <<< "$json_payload")

case "$parse_result" in
    SUCCESS:*)
        echo "✓ $parse_result"
        ;;
    FAILURES:*)
        errors+=("CI checks failed on commit $COMMIT_SHA: ${parse_result#FAILURES: }")
        ;;
    IN_PROGRESS:*)
        errors+=("CI checks are still in progress on commit $COMMIT_SHA: ${parse_result#IN_PROGRESS: }")
        ;;
    NO_CHECK_RUNS:*)
        errors+=("No completed CI check runs found on commit $COMMIT_SHA. Refusing to release unverified commit.")
        ;;
    INVALID_JSON:*)
        errors+=("Failed to parse check-runs API response: ${parse_result#INVALID_JSON: }")
        ;;
    *)
        errors+=("Unexpected check-runs verification outcome: $parse_result")
        ;;
esac

# -----------------------------------------------------------------------------
# Summary and Exit
# -----------------------------------------------------------------------------
if [[ ${#errors[@]} -gt 0 ]]; then
    echo "==========================================================" >&2
    echo "❌ RELEASE GATE FAILED" >&2
    echo "==========================================================" >&2
    for err in "${errors[@]}"; do
        echo "::error::$err" >&2
        echo "- $err" >&2
    done
    exit 1
fi

echo "=========================================================="
echo "✓ RELEASE GATE PASSED: Commit $COMMIT_SHA is verified for release"
echo "=========================================================="
exit 0
