#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_ID="$(date -u +%Y%m%d-%H%M%S)"
OUT_DIR="${SCRIPT_DIR}/chaos-results/${RUN_ID}"
mkdir -p "$OUT_DIR"

BASE_URL="${BASE_URL:-http://localhost:3000}"
API_METRICS_URL="${API_METRICS_URL:-http://localhost:9091/metrics}"
INDEXER_METRICS_URL="${INDEXER_METRICS_URL:-http://localhost:9090/metrics}"
COMPOSE_FILES="${COMPOSE_FILES:-${COMPOSE_FILE:-docker/docker-compose.yml}}"
COMPOSE_ENV_FILE="${COMPOSE_ENV_FILE:-}"
COMPOSE_PROJECT="${COMPOSE_PROJECT:-}"
POSTGRES_SERVICE="${POSTGRES_SERVICE:-postgres}"
REDIS_SERVICE="${REDIS_SERVICE:-redis}"
RPC_SERVICE="${RPC_SERVICE:-stellar-rpc}"
NETWORK="${NETWORK:-testnet}"
API_KEY="${API_KEY:-}"
FAULT_SECONDS="${FAULT_SECONDS:-30}"
RECOVERY_SECONDS="${RECOVERY_SECONDS:-180}"
BREAKER_TIMEOUT_SECONDS="${BREAKER_TIMEOUT_SECONDS:-120}"
CURL_TIMEOUT_SECONDS="${CURL_TIMEOUT_SECONDS:-10}"

for value_name in FAULT_SECONDS RECOVERY_SECONDS BREAKER_TIMEOUT_SECONDS CURL_TIMEOUT_SECONDS; do
  value="${!value_name}"
  if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
    echo "$value_name must be a positive integer (got '$value')" >&2
    exit 2
  fi
done

case "$NETWORK" in
  mainnet|testnet|futurenet|sandbox) ;;
  *)
    echo "NETWORK must be mainnet, testnet, futurenet, or sandbox (got '$NETWORK')" >&2
    exit 2
    ;;
esac

if [[ -z "$API_KEY" ]]; then
  echo "API_KEY is required to verify the Redis rate-limit fail-open behavior." >&2
  exit 2
fi

compose() {
  local compose_args=()
  local project_args=()
  local files=()
  local file

  IFS=';' read -r -a files <<< "$COMPOSE_FILES"
  for file in "${files[@]}"; do
    [[ -n "$file" ]] && compose_args+=(-f "$file")
  done
  if [[ -n "$COMPOSE_ENV_FILE" ]]; then
    compose_args+=(--env-file "$COMPOSE_ENV_FILE")
  fi
  if [[ -n "$COMPOSE_PROJECT" ]]; then
    project_args=(-p "$COMPOSE_PROJECT")
  fi

  docker compose "${project_args[@]}" "${compose_args[@]}" "$@"
}

FAILURES=0
FAULT_MODE=""
FAULT_SERVICE=""
LOWER_LEDGER=0

restore_fault() {
  [[ -z "$FAULT_MODE" ]] && return 0

  echo "Restoring ${FAULT_SERVICE} (${FAULT_MODE})" | tee -a "${OUT_DIR}/summary.txt"
  if [[ "$FAULT_MODE" == "stop" ]]; then
    compose start "$FAULT_SERVICE"
  else
    compose unpause "$FAULT_SERVICE"
  fi
  FAULT_MODE=""
  FAULT_SERVICE=""
}

cleanup() {
  if [[ -n "$FAULT_MODE" ]]; then
    restore_fault || echo "WARNING: failed to restore ${FAULT_SERVICE}" >&2
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

pass() {
  echo "PASS: $1" | tee -a "${OUT_DIR}/summary.txt"
}

fail() {
  echo "FAIL: $1" | tee -a "${OUT_DIR}/summary.txt"
  FAILURES=$((FAILURES + 1))
}

probe() {
  local label="$1"
  local path="${2:-/v1/ready}"
  local body="${OUT_DIR}/${label}.body"
  local status

  if status="$(curl -sS -o "$body" -w '%{http_code}' --max-time "$CURL_TIMEOUT_SECONDS" "${BASE_URL}${path}")"; then
    PROBE_STATUS="$status"
  else
    PROBE_STATUS="000"
  fi
  printf '%s,%s,%s,%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$label" "$path" "$PROBE_STATUS" \
    | tee -a "${OUT_DIR}/probes.csv"
}

expect_status() {
  local scenario="$1"
  local expected="$2"
  if [[ "$PROBE_STATUS" == "$expected" ]]; then
    pass "${scenario}: /v1/ready returned ${expected}"
  elif [[ "$PROBE_STATUS" == "000" ]]; then
    fail "${scenario}: /v1/ready timed out or could not connect"
  else
    fail "${scenario}: expected /v1/ready ${expected}, got ${PROBE_STATUS}"
  fi
}

expect_liveness() {
  local scenario="$1"
  if [[ "$PROBE_STATUS" == "200" ]]; then
    pass "${scenario}: /v1/health remained live"
  else
    fail "${scenario}: /v1/health returned ${PROBE_STATUS} (wanted 200)"
  fi
}

assert_authenticated_read() {
  local scenario="$1"
  local status
  if status="$(curl -sS -o "${OUT_DIR}/${scenario}-api.body" -w '%{http_code}' \
    --max-time "$CURL_TIMEOUT_SECONDS" -H "X-API-Key: ${API_KEY}" \
    "${BASE_URL}/v1/events?limit=1")"; then
    :
  else
    status=000
  fi
  if [[ "$status" == "200" ]]; then
    pass "${scenario}: authenticated API read remained available"
  else
    fail "${scenario}: authenticated API read returned ${status} (wanted 200)"
  fi
}

wait_until_ready() {
  local scenario="$1"
  local deadline=$((SECONDS + RECOVERY_SECONDS))
  while (( SECONDS < deadline )); do
    probe "${scenario}-recovery"
    if [[ "$PROBE_STATUS" == "200" ]]; then
      pass "${scenario}: readiness recovered within ${RECOVERY_SECONDS}s"
      return 0
    fi
    sleep 2
  done
  fail "${scenario}: readiness did not recover within ${RECOVERY_SECONDS}s (last status ${PROBE_STATUS})"
  return 1
}

metric_value() {
  local url="$1"
  local metric="$2"
  curl -fsS --max-time "$CURL_TIMEOUT_SECONDS" "$url" |
    awk -v metric="$metric" '
      index($0, metric) == 1 {
        suffix = substr($0, length(metric) + 1, 1)
        if (suffix == " " || suffix == "\t") {
          print $NF
          exit
        }
      }'
}

wait_for_metric_value() {
  local url="$1"
  local metric="$2"
  local expected="$3"
  local timeout="$4"
  local scenario="$5"
  local deadline=$((SECONDS + timeout))
  local value=""

  while (( SECONDS < deadline )); do
    value="$(metric_value "$url" "$metric" 2>/dev/null || true)"
    if [[ -n "$value" ]] && awk -v got="$value" -v want="$expected" 'BEGIN { exit !(got + 0 == want + 0) }'; then
      pass "${scenario}: ${metric} reached ${expected}"
      return 0
    fi
    sleep 2
  done
  fail "${scenario}: ${metric} did not reach ${expected} within ${timeout}s (last value '${value:-missing}')"
  return 1
}

db_scalar() {
  local query="$1"
  compose exec -T "$POSTGRES_SERVICE" sh -ec \
    'exec psql -XAtq -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$1"' chaos "$query"
}

read_cursor() {
  db_scalar "SELECT COALESCE((SELECT value::bigint FROM system_state WHERE key = 'latest_ledger_cursor:${NETWORK}'), 0);"
}

read_event_count() {
  db_scalar "SELECT count(*) FROM soroban_events WHERE ledger_sequence >= ${LOWER_LEDGER};"
}

read_gap_count() {
  db_scalar "WITH recent AS (
    SELECT ledger_sequence, lag(ledger_sequence) OVER (ORDER BY ledger_sequence) AS previous
    FROM ledger_metadata
    WHERE ledger_sequence >= ${LOWER_LEDGER}
  )
  SELECT count(*) FROM recent WHERE previous IS NOT NULL AND ledger_sequence > previous + 1;"
}

read_duplicate_count() {
  db_scalar "SELECT count(*) FROM (
    SELECT network, transaction_hash, event_index
    FROM soroban_events
    WHERE ledger_sequence >= ${LOWER_LEDGER}
    GROUP BY network, transaction_hash, event_index
    HAVING count(*) > 1
  ) duplicates;"
}

read_outbox_backlog() {
  db_scalar "SELECT count(*) FROM event_outbox WHERE published = FALSE;"
}

take_snapshot() {
  local scenario="$1"
  SNAP_CURSOR="$(read_cursor)"
  LOWER_LEDGER=$((SNAP_CURSOR > 10000 ? SNAP_CURSOR - 10000 : 0))
  SNAP_EVENTS="$(read_event_count)"
  SNAP_GAPS="$(read_gap_count)"
  SNAP_DUPLICATES="$(read_duplicate_count)"
  SNAP_OUTBOX="$(read_outbox_backlog)"
  {
    echo "${scenario}_cursor=${SNAP_CURSOR}"
    echo "${scenario}_recent_events=${SNAP_EVENTS}"
    echo "${scenario}_recent_gaps=${SNAP_GAPS}"
    echo "${scenario}_recent_duplicates=${SNAP_DUPLICATES}"
    echo "${scenario}_outbox_backlog=${SNAP_OUTBOX}"
  } >> "${OUT_DIR}/summary.txt"
}

wait_for_cursor_advance() {
  local scenario="$1"
  local baseline="$2"
  local deadline=$((SECONDS + RECOVERY_SECONDS))
  local cursor="$baseline"

  while (( SECONDS < deadline )); do
    cursor="$(read_cursor)"
    if (( cursor > baseline )); then
      pass "${scenario}: ingest cursor advanced from ${baseline} to ${cursor}"
      return 0
    fi
    sleep 3
  done
  fail "${scenario}: ingest cursor did not advance beyond ${baseline} within ${RECOVERY_SECONDS}s (last ${cursor})"
  return 1
}

wait_for_outbox_recovery() {
  local scenario="$1"
  local baseline="$2"
  local deadline=$((SECONDS + RECOVERY_SECONDS))
  local backlog

  while (( SECONDS < deadline )); do
    backlog="$(read_outbox_backlog)"
    if (( backlog <= baseline )); then
      pass "${scenario}: outbox backlog recovered to ${backlog} (baseline ${baseline})"
      return 0
    fi
    sleep 3
  done
  fail "${scenario}: outbox backlog ${backlog} remained above baseline ${baseline}"
  return 1
}

assert_recovered_data() {
  local scenario="$1"
  local baseline_events="$2"

  local events gaps duplicates
  events="$(read_event_count)"
  gaps="$(read_gap_count)"
  duplicates="$(read_duplicate_count)"

  if (( events >= baseline_events )); then
    pass "${scenario}: recent event count did not decrease (${baseline_events} -> ${events})"
  else
    fail "${scenario}: recent event count decreased (${baseline_events} -> ${events})"
  fi
  if (( gaps == 0 )); then
    pass "${scenario}: no ledger gaps remain in the checked cursor window"
  else
    fail "${scenario}: ${gaps} ledger gap(s) remain in the checked cursor window"
  fi
  if (( duplicates == 0 )); then
    pass "${scenario}: no duplicate event keys in the checked cursor window"
  else
    fail "${scenario}: ${duplicates} duplicate event key(s) found in the checked cursor window"
  fi
}

begin_fault() {
  local mode="$1"
  local service="$2"
  FAULT_MODE="$mode"
  FAULT_SERVICE="$service"
  if [[ "$mode" == "stop" ]]; then
    compose stop "$service"
  else
    compose pause "$service"
  fi
}

run_scenario() {
  local scenario="$1"
  local service="$2"
  local mode="$3"
  local kind="$4"

  echo "=== ${scenario} ===" | tee -a "${OUT_DIR}/summary.txt"
  probe "${scenario}-before"
  if [[ "$PROBE_STATUS" != "200" ]]; then
    echo "Stack is not ready before ${scenario}; refusing to induce a fault." >&2
    exit 1
  fi
  take_snapshot "${scenario}_before"

  local fail_open_before=0
  if [[ "$kind" == "redis" ]]; then
    fail_open_before="$(metric_value "$API_METRICS_URL" 'trident_ratelimit_fail_open_total{limiter="per_key"}' 2>/dev/null || echo 0)"
    [[ "$fail_open_before" =~ ^[0-9]+([.][0-9]+)?$ ]] || fail_open_before=0
  fi

  begin_fault "$mode" "$service"
  sleep 5
  probe "${scenario}-during"

  case "$kind" in
    rpc)
      # RPC is upstream of the indexer, not an API serving dependency: reads
      # from already-indexed data remain available while the breaker opens.
      expect_status "$scenario" 200
      probe "${scenario}-liveness" /v1/health
      expect_liveness "$scenario"
      assert_authenticated_read "$scenario"
      wait_for_metric_value "$INDEXER_METRICS_URL" trident_indexer_rpc_breaker_state 1 \
        "$BREAKER_TIMEOUT_SECONDS" "$scenario"

      local stable_cursor previous_cursor stable=0
      stable_cursor="$(read_cursor)"
      previous_cursor="$stable_cursor"
      for _ in 1 2 3; do
        sleep 2
        local current_cursor
        current_cursor="$(read_cursor)"
        if (( current_cursor == previous_cursor )); then
          stable=$((stable + 1))
        else
          stable=0
        fi
        previous_cursor="$current_cursor"
      done
      if (( stable == 3 )); then
        pass "${scenario}: cursor remained stable at ${stable_cursor} while the RPC breaker was open"
      else
        fail "${scenario}: cursor continued changing while the RPC breaker was open"
      fi
      ;;
    postgres|redis)
      expect_status "$scenario" 503
      probe "${scenario}-liveness" /v1/health
      expect_liveness "$scenario"
      ;;
  esac

  if [[ "$kind" == "redis" ]]; then
    local fail_open_after
    assert_authenticated_read "$scenario"

    fail_open_after="$(metric_value "$API_METRICS_URL" 'trident_ratelimit_fail_open_total{limiter="per_key"}' 2>/dev/null || echo 0)"
    if awk -v before="$fail_open_before" -v after="$fail_open_after" 'BEGIN { exit !(after + 0 > before + 0) }'; then
      pass "${scenario}: per-key rate-limit fail-open metric incremented"
    else
      fail "${scenario}: per-key rate-limit fail-open metric did not increment (before ${fail_open_before}, after ${fail_open_after:-missing})"
    fi
  fi

  sleep "$FAULT_SECONDS"
  restore_fault

  if ! wait_until_ready "$scenario"; then
    return 1
  fi
  if [[ "$kind" == "rpc" ]]; then
    wait_for_metric_value "$INDEXER_METRICS_URL" trident_indexer_rpc_breaker_state 0 \
      "$RECOVERY_SECONDS" "${scenario} recovery" || true
    wait_for_cursor_advance "${scenario} recovery" "$stable_cursor" || true
  else
    wait_for_cursor_advance "${scenario} recovery" "$SNAP_CURSOR" || true
  fi
  if [[ "$kind" == "redis" ]]; then
    wait_for_outbox_recovery "$scenario" "$SNAP_OUTBOX" || true
  fi
  assert_recovered_data "$scenario" "$SNAP_EVENTS"
}

echo "timestamp,label,path,status" > "${OUT_DIR}/probes.csv"
{
  echo "Trident dependency fault-injection run ${RUN_ID}"
  echo "BASE_URL=${BASE_URL}"
  echo "COMPOSE_FILES=${COMPOSE_FILES}"
  echo "NETWORK=${NETWORK}"
} > "${OUT_DIR}/summary.txt"

if [[ -n "$COMPOSE_PROJECT" ]]; then
  compose ps
fi

if ! wait_until_ready initial; then
  echo "The API did not become ready before chaos scenarios started." >&2
  exit 1
fi

probe initial
run_scenario postgres-down "$POSTGRES_SERVICE" stop postgres
run_scenario postgres-slow "$POSTGRES_SERVICE" pause postgres
run_scenario redis-down "$REDIS_SERVICE" stop redis
run_scenario rpc-down "$RPC_SERVICE" stop rpc
run_scenario rpc-slow "$RPC_SERVICE" pause rpc

{
  echo
  echo "Assertion failures: ${FAILURES}"
  echo "Results: ${OUT_DIR}"
} | tee -a "${OUT_DIR}/summary.txt"

if (( FAILURES > 0 )); then
  echo "Chaos verification FAILED with ${FAILURES} assertion failure(s)." >&2
  exit 1
fi
echo "Chaos verification passed all degradation and recovery assertions."
