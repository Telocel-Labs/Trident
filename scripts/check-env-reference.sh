#!/usr/bin/env bash
# Fails if the indexer, backfill, gRPC API, or Go API read an environment
# variable that isn't documented in docs/ENVIRONMENT.md (issue #312).
#
# This is a grep-based check, not a compiler plugin: it scans for
# std::env::var("X") / env::var("X") in Rust and os.Getenv("X") in Go, then
# checks each hit appears (as a whole word, wrapped in backticks) in
# docs/ENVIRONMENT.md. False positives are possible on ALL_CAPS string
# literals that aren't actually env var names — those are allow-listed
# below, not silently skipped.
set -euo pipefail

cd "$(dirname "$0")/.."

DOC="docs/ENVIRONMENT.md"

# Non-env-var ALL_CAPS identifiers that legitimately show up in
# env::var("...")-shaped test/example strings but are not real env vars, or
# are test-only fixtures already covered by inline comments rather than a
# reference table row. Keep this list small and justified.
ALLOWLIST=(
  TEST_POOL_UNSET
  TEST_POOL_VALID
  TEST_POOL_BAD
)

is_allowlisted() {
  local var="$1"
  for a in "${ALLOWLIST[@]}"; do
    [[ "$var" == "$a" ]] && return 0
  done
  return 1
}

# Collect candidate env var names: Rust (std::env::var / env::var) and
# Go (os.Getenv), from application source only (exclude tests, since a test
# fixture string can look like a var but not be one that a lint doc needs to
# cover — real vars read by non-test code are always also referenced from a
# collect_required/envInt/etc call reachable from main, which this still catches).
mapfile -t rust_vars < <(
  grep -rhoE '(std::)?env::var\("[A-Z][A-Z0-9_]*"\)' \
    crates/indexer/src crates/backfill/src crates/api/src crates/common/src 2>/dev/null \
    | grep -oE '"[A-Z][A-Z0-9_]*"' | tr -d '"' | sort -u
)

mapfile -t go_vars < <(
  grep -rhoE 'os\.Getenv\("[A-Z][A-Z0-9_]*"\)' services/api 2>/dev/null \
    | grep -oE '"[A-Z][A-Z0-9_]*"' | tr -d '"' | sort -u
)

all_vars=("${rust_vars[@]}" "${go_vars[@]}")

missing=()
for var in "${all_vars[@]}"; do
  is_allowlisted "$var" && continue
  if ! grep -qF "\`$var\`" "$DOC"; then
    missing+=("$var")
  fi
done

if [ "${#missing[@]}" -gt 0 ]; then
  echo "The following env vars are read by the code but missing from $DOC:" >&2
  printf '  %s\n' "${missing[@]}" >&2
  echo "" >&2
  echo "Add a row for each (service, default, required/optional, description) to $DOC." >&2
  exit 1
fi

# ── Effective-behaviour check (issue #737) ────────────────────────────────
# Documenting a knob is not enough: it must actually change behaviour. Every
# field on `Config` originates in an env var, and a field read only inside
# config.rs (parsed, then logged) was dead — that is exactly how
# MAX_REORG_REWIND_DEPTH, RPC_FAILOVER_THRESHOLD and RPC_ENDPOINT_COOLDOWN_MS
# shipped: validated, logged, and never read by the running indexer. Require
# every field to be *read* (`.<field>`) somewhere outside config.rs.
#
# `.field` rather than a bare name on purpose: struct literals (including the
# test fixtures that populate every field) would otherwise make a dead field
# look used.
config_fields=$(awk '/^pub struct Config \{/{inside=1; next} inside && /^\}/{exit} inside' \
  crates/indexer/src/config.rs \
  | grep -oE '^    pub [a-z_0-9]+:' | sed -E 's/^    pub //; s/:$//')

if [ -z "$config_fields" ]; then
  echo "check-env-reference: could not read Config fields from crates/indexer/src/config.rs" >&2
  exit 1
fi

dead_fields=()
for field in $config_fields; do
  if ! grep -rqE "\.${field}\b" crates/indexer/src --include='*.rs' \
    --exclude='config.rs' 2>/dev/null; then
    dead_fields+=("$field")
  fi
done

if [ "${#dead_fields[@]}" -gt 0 ]; then
  echo "These Config fields (env-var knobs) are never read outside config.rs," >&2
  echo "so setting them changes nothing at runtime:" >&2
  printf '  %s\n' "${dead_fields[@]}" >&2
  echo "" >&2
  echo "Wire the knob into the running indexer, or remove the field and fail" >&2
  echo "startup if the env var is set (see issue #737)." >&2
  exit 1
fi

echo "OK: every env var read by indexer/backfill/grpc-api/go-api is documented in $DOC"
echo "OK: every Config field is read outside config.rs (no dead knobs)"
# ── Deployment config validation (Fly & Helm) (issue #777) ───────────────
# Neither Fly toml [env] blocks nor Helm chart values/templates may supply
# environment variables that config validation rejects (e.g. POLL_INTERVAL_MS,
# MAX_REORG_REWIND_DEPTH, RPC_FAILOVER_THRESHOLD, RPC_ENDPOINT_COOLDOWN_MS),
# or variables that are not documented in docs/ENVIRONMENT.md.
PYTHON="${PYTHON:-}"
if [ -z "$PYTHON" ]; then
  if command -v python3 >/dev/null 2>&1 && python3 -c "import sys" >/dev/null 2>&1; then
    PYTHON="python3"
  else
    PYTHON="python"
  fi
fi

FLY_DIR="${1:-fly}"
HELM_DIR="${2:-helm/trident}"

"$PYTHON" - "$DOC" "$FLY_DIR" "$HELM_DIR" <<'PYEOF'
import glob, os, re, sys, tomllib, yaml

doc_path = sys.argv[1]
fly_dir = sys.argv[2]
helm_dir = sys.argv[3]

with open(doc_path, "r", encoding="utf-8") as f:
    doc_text = f.read()

# Variables explicitly rejected by indexer/service config validation (issue #629, #737, #777)
rejected_vars = {
    "POLL_INTERVAL_MS": "no longer used — use POLL_INTERVAL_FLOOR_MS / POLL_INTERVAL_CEILING_MS / LAG_HIGH_WATERMARK instead",
    "MAX_REORG_REWIND_DEPTH": "no longer used — use MAX_REORG_DEPTH instead",
    "RPC_FAILOVER_THRESHOLD": "no longer used — use RPC_BREAKER_FAILURE_THRESHOLD instead",
    "RPC_ENDPOINT_COOLDOWN_MS": "no longer used — use RPC_BREAKER_COOLDOWN_MS instead",
}

failures = []

# 1. Fly toml [env] blocks
for toml_path in sorted(glob.glob(os.path.join(fly_dir, "*.toml"))):
    try:
        with open(toml_path, "r", encoding="utf-8-sig") as f:
            data = tomllib.loads(f.read())
    except Exception as e:
        failures.append(f"{toml_path}: failed to parse TOML: {e}")
        continue

    env_map = data.get("env", {})
    if not isinstance(env_map, dict):
        continue

    for var_name in env_map:
        if var_name in rejected_vars:
            failures.append(
                f"{toml_path}: sets rejected env var `{var_name}` ({rejected_vars[var_name]})"
            )
        elif f"`{var_name}`" not in doc_text:
            failures.append(
                f"{toml_path}: sets env var `{var_name}` which is not documented in {doc_path}"
            )

# 2. Helm chart values
for val_path in sorted(glob.glob(os.path.join(helm_dir, "values*.yaml"))):
    try:
        with open(val_path, "r", encoding="utf-8-sig") as f:
            data = yaml.safe_load(f) or {}
    except Exception as e:
        failures.append(f"{val_path}: failed to parse YAML: {e}")
        continue

    for comp in ["indexer", "grpcApi", "goApi"]:
        comp_obj = data.get(comp)
        if isinstance(comp_obj, dict) and "env" in comp_obj and isinstance(comp_obj["env"], dict):
            for var_name in comp_obj["env"]:
                if var_name in rejected_vars:
                    failures.append(
                        f"{val_path} ({comp}.env): sets rejected env var `{var_name}` ({rejected_vars[var_name]})"
                    )
                elif f"`{var_name}`" not in doc_text:
                    failures.append(
                        f"{val_path} ({comp}.env): sets env var `{var_name}` which is not documented in {doc_path}"
                    )

# 3. Helm templates (hardcoded env definitions)
for tmpl_path in sorted(glob.glob(os.path.join(helm_dir, "templates", "*.yaml"))):
    try:
        with open(tmpl_path, "r", encoding="utf-8") as f:
            content = f.read()
    except Exception:
        continue
    for match in re.finditer(r"^\s+-\s+name:\s+([A-Z0-9_]+)", content, re.M):
        var_name = match.group(1)
        if var_name in rejected_vars:
            failures.append(
                f"{tmpl_path}: template defines rejected env var `{var_name}` ({rejected_vars[var_name]})"
            )

if failures:
    print(f"ERROR: {len(failures)} deployment configuration issue(s) found in Fly/Helm env vars:", file=sys.stderr)
    for f in failures:
        print(f"  - {f}", file=sys.stderr)
    sys.exit(1)

PYEOF

echo "OK: all Fly and Helm env declarations reference documented, non-rejected env vars"
