#!/usr/bin/env bash
# Ingest soak test (issue #322).
#
# Produces sustained event volume by repeatedly invoking `mint` on the
# reference token contract (contracts/token — the same one CI's
# e2e-contract-events job in .github/workflows/ci.yml deploys and mints
# against) on a local Soroban network, while sampling `docker stats` for the
# Go API and Rust indexer containers on an interval and writing the samples
# to a CSV. At the end, it does a coarse "no unbounded growth" check: memory
# in the last sampling window must not have grown by more than
# MAX_GROWTH_PCT relative to the first sampling window.
#
# This is a COARSE check, not a leak detector: a single soak run's memory
# curve can be noisy (GC timing, one-off allocations, page cache accounting
# in `docker stats`), and MAX_GROWTH_PCT is deliberately generous. Treat a
# failure as "worth investigating", not proof of a leak, and a pass as "no
# smoking gun in this run's duration", not proof of the absence of one.
#
# Prerequisites:
#   - Docker + Compose v2
#   - stellar CLI (https://github.com/stellar/stellar-cli) on PATH
#   - The stack is already up: `docker compose -f docker/docker-compose.yml
#     -f docker/docker-compose.ci.yml -f docker/docker-compose.e2e.yml
#     --env-file .env.ci up -d --build` (see .github/workflows/ci.yml's
#     e2e-contract-events job for the exact compose invocation this mirrors)
#   - A local Soroban network reachable at LOCAL_RPC_URL (e.g. the
#     `stellar/quickstart` container the CI job starts)
#   - contracts/target/wasm32v1-none/release/token.wasm built
#     (`cargo build --release --target wasm32v1-none -p token` in contracts/)
#
# Usage:
#   LOCAL_RPC_URL=http://localhost:8000/rpc \
#   SOAK_DURATION_SECONDS=1800 \
#   MINT_INTERVAL_SECONDS=2 \
#   SAMPLE_INTERVAL_SECONDS=30 \
#     ./load-tests/ingest-soak.sh
#
# Output: load-tests/soak-results/<timestamp>/{mint.log,stats.csv,summary.txt}

set -euo pipefail

LOCAL_RPC_URL="${LOCAL_RPC_URL:-http://localhost:8000/rpc}"
LOCAL_NETWORK_PASSPHRASE="${LOCAL_NETWORK_PASSPHRASE:-Standalone Network ; February 2017}"
SOAK_DURATION_SECONDS="${SOAK_DURATION_SECONDS:-1800}"      # 30 minutes default
MINT_INTERVAL_SECONDS="${MINT_INTERVAL_SECONDS:-2}"
SAMPLE_INTERVAL_SECONDS="${SAMPLE_INTERVAL_SECONDS:-30}"
MAX_GROWTH_PCT="${MAX_GROWTH_PCT:-50}"                       # coarse "no unbounded growth" bar
GO_API_CONTAINER="${GO_API_CONTAINER:-docker-api-1}"
INDEXER_CONTAINER="${INDEXER_CONTAINER:-docker-indexer-1}"
WASM_PATH="${WASM_PATH:-contracts/target/wasm32v1-none/release/token.wasm}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_ID="$(date +%Y%m%d-%H%M%S)"
OUT_DIR="${SCRIPT_DIR}/soak-results/${RUN_ID}"
mkdir -p "$OUT_DIR"

MINT_LOG="${OUT_DIR}/mint.log"
STATS_CSV="${OUT_DIR}/stats.csv"
SUMMARY="${OUT_DIR}/summary.txt"

echo "Ingest soak test starting (run id: $RUN_ID)"
echo "  duration:         ${SOAK_DURATION_SECONDS}s"
echo "  mint interval:    ${MINT_INTERVAL_SECONDS}s"
echo "  sample interval:  ${SAMPLE_INTERVAL_SECONDS}s"
echo "  output dir:       ${OUT_DIR}"

if ! command -v stellar >/dev/null 2>&1; then
  echo "stellar CLI not found on PATH — install it: https://github.com/stellar/stellar-cli" >&2
  exit 1
fi
if [ ! -f "$WASM_PATH" ]; then
  echo "Token contract wasm not found at $WASM_PATH — build it first:" >&2
  echo "  (cd contracts && cargo build --release --target wasm32v1-none -p token)" >&2
  exit 1
fi

stellar network add local \
  --rpc-url "$LOCAL_RPC_URL" \
  --network-passphrase "$LOCAL_NETWORK_PASSPHRASE" \
  >/dev/null 2>&1 || true

echo "Setting up admin + recipient accounts and deploying the token contract..."
stellar keys generate soak-admin --network local --no-fund >/dev/null 2>&1 || true
stellar keys generate soak-recipient --network local --no-fund >/dev/null 2>&1 || true
stellar keys fund soak-admin --network local >/dev/null
stellar keys fund soak-recipient --network local >/dev/null
ADMIN_ADDR="$(stellar keys address soak-admin)"
RECIPIENT_ADDR="$(stellar keys address soak-recipient)"

CONTRACT_ID="$(stellar contract deploy \
  --wasm "$WASM_PATH" \
  --source soak-admin \
  --network local)"
echo "Deployed contract: $CONTRACT_ID"

stellar contract invoke \
  --id "$CONTRACT_ID" --source soak-admin --network local \
  -- initialize --admin "$ADMIN_ADDR" --decimals 7 \
      --name "Soak Test Token" --symbol SOAK >/dev/null

echo "contract_id=$CONTRACT_ID" > "${OUT_DIR}/contract.env"
echo "recipient_addr=$RECIPIENT_ADDR" >> "${OUT_DIR}/contract.env"

# --- docker stats sampler ---------------------------------------------------
echo "timestamp,container,cpu_pct,mem_usage_mb,mem_limit_mb,mem_pct" > "$STATS_CSV"

sample_stats() {
  local ts
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  for container in "$GO_API_CONTAINER" "$INDEXER_CONTAINER"; do
    if ! docker inspect "$container" >/dev/null 2>&1; then
      continue
    fi
    # docker stats --no-stream one-shot output, parsed with awk for a stable
    # CSV shape regardless of locale-formatted separators in the raw output.
    docker stats --no-stream --format '{{.CPUPerc}},{{.MemUsage}},{{.MemPerc}}' "$container" 2>/dev/null \
      | awk -F',' -v ts="$ts" -v c="$container" '
        {
          cpu = $1; gsub(/%/, "", cpu);
          split($2, mem, " / ");
          usage = mem[1]; limit = mem[2];
          memp = $3; gsub(/%/, "", memp);
          # Normalize GiB/MiB to MB (good enough for a coarse growth check).
          usage_mb = usage; gsub(/GiB/, "", usage_mb); gsub(/MiB/, "", usage_mb);
          if (usage ~ /GiB/) { usage_mb = usage_mb * 1024 }
          limit_mb = limit; gsub(/GiB/, "", limit_mb); gsub(/MiB/, "", limit_mb);
          if (limit ~ /GiB/) { limit_mb = limit_mb * 1024 }
          printf "%s,%s,%s,%s,%s,%s\n", ts, c, cpu, usage_mb, limit_mb, memp
        }' >> "$STATS_CSV"
  done
}

# --- background sampler + mint loop -----------------------------------------
END_TS=$(( $(date +%s) + SOAK_DURATION_SECONDS ))

(
  while [ "$(date +%s)" -lt "$END_TS" ]; do
    sample_stats
    sleep "$SAMPLE_INTERVAL_SECONDS"
  done
) &
SAMPLER_PID=$!

echo "Sustained mint loop running for ${SOAK_DURATION_SECONDS}s (interval ${MINT_INTERVAL_SECONDS}s)..." | tee -a "$MINT_LOG"
MINT_COUNT=0
while [ "$(date +%s)" -lt "$END_TS" ]; do
  if stellar contract invoke \
      --id "$CONTRACT_ID" --source soak-admin --network local \
      -- mint --to "$RECIPIENT_ADDR" --amount 1000 >> "$MINT_LOG" 2>&1; then
    MINT_COUNT=$((MINT_COUNT + 1))
  else
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) mint invocation failed (see above)" >> "$MINT_LOG"
  fi
  sleep "$MINT_INTERVAL_SECONDS"
done

wait "$SAMPLER_PID" 2>/dev/null || true
sample_stats # one final sample right at the end

echo "Soak run complete: $MINT_COUNT mint invocations over ${SOAK_DURATION_SECONDS}s."

# --- coarse growth check -----------------------------------------------------
python3 - "$STATS_CSV" "$MAX_GROWTH_PCT" "$MINT_COUNT" "$OUT_DIR" <<'PYEOF'
import csv
import sys

stats_csv, max_growth_pct, mint_count, out_dir = sys.argv[1], float(sys.argv[2]), sys.argv[3], sys.argv[4]

by_container = {}
with open(stats_csv, newline="") as f:
    for row in csv.DictReader(f):
        try:
            mem_mb = float(row["mem_usage_mb"])
        except ValueError:
            continue
        by_container.setdefault(row["container"], []).append(mem_mb)

lines = [f"Ingest soak summary\n", f"mint invocations: {mint_count}\n\n"]
failed = False

if not by_container:
    lines.append("No docker stats samples were collected (containers not found / docker stats unavailable).\n")
    lines.append("Skipping the growth assertion — verify GO_API_CONTAINER / INDEXER_CONTAINER match your compose project's container names.\n")
else:
    for container, samples in by_container.items():
        if len(samples) < 2:
            lines.append(f"{container}: only {len(samples)} sample(s) — not enough to assess growth.\n")
            continue
        # Compare the average of the first 3 samples to the average of the
        # last 3 (or fewer if the run was short) — smooths out single-sample
        # noise while still being a coarse, not statistically rigorous, check.
        first_window = samples[: min(3, len(samples))]
        last_window = samples[-min(3, len(samples)):]
        first_avg = sum(first_window) / len(first_window)
        last_avg = sum(last_window) / len(last_window)
        growth_pct = ((last_avg - first_avg) / first_avg * 100) if first_avg > 0 else 0.0
        verdict = "OK"
        if growth_pct > max_growth_pct:
            verdict = "FAIL (possible unbounded growth)"
            failed = True
        lines.append(
            f"{container}: first-window avg {first_avg:.1f}MB -> last-window avg {last_avg:.1f}MB "
            f"({growth_pct:+.1f}%, threshold {max_growth_pct:.0f}%) [{verdict}]\n"
        )

summary = "".join(lines)
print(summary)
with open(f"{out_dir}/summary.txt", "w") as f:
    f.write(summary)

sys.exit(1 if failed else 0)
PYEOF
SOAK_EXIT=$?

echo ""
echo "Results written to: $OUT_DIR"
echo "  mint.log      - stellar contract invoke output"
echo "  stats.csv     - docker stats samples over the run"
echo "  summary.txt   - growth-check summary"

exit "$SOAK_EXIT"
