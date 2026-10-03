#!/usr/bin/env bash
#
# Plan Soroban event partition pre-creation ahead of a target mainnet ledger
# height, with a safety margin (issue #680).
#
# `create_soroban_partition` (database/migrations/0017_soroban_events_partitioning.sql)
# is a manual, reactive tool: an operator invokes it once the
# TridentPartitionExhaustionWarning alert already fired. This script is the
# forward-looking half of that story: given a target ledger height (e.g. a
# projected height at a future launch date, or "current tip + N days of
# runway") and a safety margin, it computes which 2,000,000-ledger partitions
# need to exist and prints the exact `create_soroban_partition` calls to run,
# skipping any partition that already exists.
#
# Usage:
#   scripts/plan-soroban-partitions.sh --target-ledger N [--margin-ledgers N]
#                                       [--database-url URL] [--apply]
#
#   scripts/plan-soroban-partitions.sh --target-date YYYY-MM-DD
#                                       [--current-ledger N] [--margin-ledgers N]
#                                       [--database-url URL] [--apply]
#
# Exactly one of --target-ledger or --target-date is required.
#
# --target-date projects forward from --current-ledger (or the database's
# current partition upper bound, if --database-url is set and
# --current-ledger is omitted) at LEDGERS_PER_DAY ledgers/day.
#
# Without --apply, this only prints the plan (a dry run — safe to run
# repeatedly). With --apply, it executes each missing `create_soroban_partition`
# call against --database-url (or $DATABASE_URL).

set -euo pipefail

usage() {
  sed -n '3,26p' "$0" | sed 's/^# \?//'
  exit "${1:-0}"
}

# Matches the constant documented in migration 0017 and docs/runbooks/alerts.md.
PARTITION_SIZE_LEDGERS=2000000
# ~17,280 ledgers/day at Stellar's ~5s ledger close time (documented in
# migration 0017 and docs/runbooks/alerts.md).
LEDGERS_PER_DAY=17280

TARGET_LEDGER=""
TARGET_DATE=""
CURRENT_LEDGER=""
MARGIN_LEDGERS=5000000
DATABASE_URL="${DATABASE_URL:-}"
APPLY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target-ledger)  TARGET_LEDGER="$2"; shift 2 ;;
    --target-date)    TARGET_DATE="$2"; shift 2 ;;
    --current-ledger) CURRENT_LEDGER="$2"; shift 2 ;;
    --margin-ledgers) MARGIN_LEDGERS="$2"; shift 2 ;;
    --database-url)   DATABASE_URL="$2"; shift 2 ;;
    --apply)          APPLY=1; shift ;;
    -h|--help)        usage 0 ;;
    *) echo "unknown argument: $1" >&2; usage 1 ;;
  esac
done

if [[ -z "$TARGET_LEDGER" && -z "$TARGET_DATE" ]]; then
  echo "error: exactly one of --target-ledger or --target-date is required" >&2
  usage 1
fi
if [[ -n "$TARGET_LEDGER" && -n "$TARGET_DATE" ]]; then
  echo "error: pass only one of --target-ledger or --target-date, not both" >&2
  usage 1
fi

query_current_upper_bound() {
  command -v psql >/dev/null 2>&1 || {
    echo "error: psql is required to query the database but is not installed" >&2
    exit 1
  }
  psql "$DATABASE_URL" -tA -c "
    SELECT COALESCE(MAX(
        (regexp_match(
            pg_get_partition_constraintdef(child.oid),
            'ledger_sequence < (\d+)'
        ))[1]::bigint
    ), 0)
    FROM pg_inherits
    JOIN pg_class parent ON parent.oid = inhparent
    JOIN pg_class child  ON child.oid  = inhrelid
    WHERE parent.relname = 'soroban_events';
  " | tr -d '[:space:]'
}

partition_exists() {
  local start="$1" end="$2"
  psql "$DATABASE_URL" -tA -c "
    SELECT EXISTS (
      SELECT 1 FROM pg_class c
      JOIN pg_inherits i ON i.inhrelid = c.oid
      JOIN pg_class p ON p.oid = i.inhparent
      WHERE p.relname = 'soroban_events'
        AND c.relname = 'soroban_events_p${start}_$(( end - 1 ))'
    );
  " | tr -d '[:space:]'
}

if [[ -n "$TARGET_DATE" ]]; then
  if [[ -z "$CURRENT_LEDGER" ]]; then
    if [[ -n "$DATABASE_URL" ]]; then
      CURRENT_LEDGER="$(query_current_upper_bound)"
      echo "current partition upper bound (from database): $CURRENT_LEDGER" >&2
    else
      echo "error: --target-date requires --current-ledger, or --database-url to read it from the database" >&2
      exit 1
    fi
  fi

  target_epoch="$(date -j -f "%Y-%m-%d" "$TARGET_DATE" +%s 2>/dev/null || date -d "$TARGET_DATE" +%s)"
  now_epoch="$(date +%s)"
  days_ahead="$(awk -v a="$target_epoch" -v b="$now_epoch" 'BEGIN { d = (a - b) / 86400; print (d < 0) ? 0 : d }')"
  ledgers_ahead="$(awk -v d="$days_ahead" -v r="$LEDGERS_PER_DAY" 'BEGIN { printf "%d", d * r }')"
  TARGET_LEDGER=$(( CURRENT_LEDGER + ledgers_ahead ))

  echo "target date $TARGET_DATE is ~$(awk -v d="$days_ahead" 'BEGIN { printf "%.0f", d }') days out" >&2
  echo "projected target ledger: $TARGET_LEDGER (at ${LEDGERS_PER_DAY} ledgers/day)" >&2
fi

TARGET_LEDGER_WITH_MARGIN=$(( TARGET_LEDGER + MARGIN_LEDGERS ))

echo
echo "Partition capacity plan"
echo "------------------------"
echo "target ledger            : $TARGET_LEDGER"
echo "safety margin            : $MARGIN_LEDGERS ledgers"
echo "target + margin          : $TARGET_LEDGER_WITH_MARGIN"
echo "partition size           : $PARTITION_SIZE_LEDGERS ledgers"
echo

# Find the starting point: the current known upper bound (if any), rounded
# down to the nearest partition boundary.
start="${CURRENT_LEDGER:-0}"
start=$(( (start / PARTITION_SIZE_LEDGERS) * PARTITION_SIZE_LEDGERS ))

echo "Partitions needed (from ledger $start through $TARGET_LEDGER_WITH_MARGIN):"
echo

commands=()
while (( start < TARGET_LEDGER_WITH_MARGIN )); do
  end=$(( start + PARTITION_SIZE_LEDGERS ))
  status="pending"
  if [[ -n "$DATABASE_URL" ]]; then
    if [[ "$(partition_exists "$start" "$end")" == "t" ]]; then
      status="exists"
    fi
  fi
  printf '  %-10s soroban_events_p%d_%d  (SELECT create_soroban_partition(%d, %d);)\n' \
    "[$status]" "$start" "$(( end - 1 ))" "$start" "$end"
  if [[ "$status" != "exists" ]]; then
    commands+=("SELECT create_soroban_partition($start, $end);")
  fi
  start=$end
done

echo
if (( ${#commands[@]} == 0 )); then
  echo "Nothing to do: all required partitions already exist."
  exit 0
fi

echo "${#commands[@]} partition(s) need to be created."

if (( APPLY == 0 )); then
  echo
  echo "Dry run only (pass --apply to execute). To apply manually, run each of:"
  for cmd in "${commands[@]}"; do
    echo "  $cmd"
  done
  exit 0
fi

if [[ -z "$DATABASE_URL" ]]; then
  echo "error: --apply requires --database-url (or \$DATABASE_URL)" >&2
  exit 1
fi

command -v psql >/dev/null 2>&1 || {
  echo "error: psql is required to apply the plan but is not installed" >&2
  exit 1
}

for cmd in "${commands[@]}"; do
  echo "applying: $cmd"
  psql "$DATABASE_URL" -c "$cmd"
done

echo "done."
