# Chaos drill findings (issue #499)

## Status: compose validation complete, fault run not performed

Issue #499 asks for `load-tests/chaos-launch.sh` (built for #439) to be run
against staging for Postgres, Redis, and RPC faults, with real observed
degradation behavior recorded. That could not be done from this environment:

- Staging is a Kubernetes deployment reached only through CI secrets
  (`STAGING_KUBECONFIG`, `STAGING_URL`, `STAGING_DATABASE_URL` — see
  `.github/workflows/staging-deploy.yml`), none of which are available here.
- The harness is compose-native (`docker compose stop/pause/exec` against
  compose service names), so it cannot target Kubernetes staging unmodified.
- The local Docker CLI has no reachable daemon (`docker info` fails to connect
  to the Docker Desktop engine), so the induced faults and recovery assertions
  could not be executed here.

`bash -n` and `docker compose ... config --quiet` pass for the current harness
and compose overlays. The focused Go rate-limit and readiness tests pass. A
broader handler test run still has unrelated contract-stats range failures.
No fault-injection run is claimed. The static findings below record limitations
that remain in the current setup, followed by the current local run procedure.

## Historical review and remaining limitations

1. **Current static checks.** `bash -n load-tests/chaos-launch.sh` and
   `docker compose -p trident-chaos -f docker/docker-compose.yml
   -f docker/docker-compose.ci.yml -f docker/docker-compose.chaos.yml
   --env-file .env.ci config --quiet` pass. ShellCheck was not installed.

2. **Readiness contract cross-check.** Read
   `services/api/handlers/health.go` (`Ready` handler) directly against the
   harness's assertions:
   - Postgres and Redis are direct readiness dependencies, so their outages
     should return 503 while `/v1/health` stays live.
   - RPC is upstream of the indexer, not a direct API readiness dependency.
     During RPC outage, `/v1/ready` and authenticated reads should remain
     available while `trident_indexer_rpc_breaker_state` reaches 1.
   - The current harness additionally checks Redis rate-limit fail-open,
     cursor recovery, event-count monotonicity, gaps, duplicate event keys,
     and outbox drain. These remain statically verified only until run with a
     reachable Docker daemon.

3. **Compose topology cross-check** (`docker/docker-compose.yml`). This is
   where the two findings below came from — see "Findings to file as
   follow-up issues".

## Findings to file as follow-up issues

### Finding 1 — the harness never faults PgBouncer itself

Every service (`indexer`, `api`) connects to `pgbouncer:6432`, never to
`postgres:5432` directly (`docker-compose.yml:91,129`, both comments say
"Route through PgBouncer, never postgres directly"). `chaos-launch.sh`'s
`POSTGRES_SERVICE` defaults to `postgres` and every Postgres scenario
(`postgres-down`, `postgres-slow`) stops or pauses the `postgres` container.
In the production compose overlay, applications connect through PgBouncer.
The local CI compose overlay bypasses PgBouncer, so that local workflow only
checks database failure, not pooler failure.

This is a real gap, not just a naming nit: PgBouncer sits in front of
Postgres and is responsible for detecting a dead backend and either failing
fast or queuing, depending on its pool state — behavior the current
scenarios never exercise. `/v1/ready` failing when `postgres` is stopped only
proves PgBouncer eventually surfaces the outage to `checkPostgres`'s `Ping`;
it says nothing about how long PgBouncer takes to notice, whether it queues
client connections against a dead backend (which would show up as `/v1/ready`
hanging past `--max-time 10` rather than failing fast, indistinguishable in
the harness's output from a genuine timeout), or what happens if `pgbouncer`
itself is stopped/paused instead of the Postgres behind it — a distinct
failure mode (`api`/`indexer` lose their pooler, not their database) that has
no scenario at all today.

**Suggested follow-up**: add a `pgbouncer-down`/`pgbouncer-slow` scenario
pair (stop/pause the `pgbouncer` service directly, same before/during/after
probe shape as the existing scenarios), and consider adding a
`PGBOUNCER_SERVICE` variable alongside `POSTGRES_SERVICE` so the existing
Postgres scenarios can optionally be run against the pooler layer too.

### Finding 2 — recovery timing assumption doesn't account for PgBouncer's reconnect behavior

`RECOVERY_SECONDS` defaults to 45s, and Postgres's own compose healthcheck needs up to
`start_period(15s) + retries(10) × interval(10s)` ≈ 115s in the worst case to
report `healthy` after a restart (`docker-compose.yml:13-18`). The current
harness uses a 180-second recovery timeout, but the actual recovery duration
has not been measured.

**Suggested follow-up**: when the harness is run, record recovery duration for
each service and adjust the shared timeout or make it configurable per
scenario if any service approaches the limit.

## Running the real drill (for whoever has staging or local Docker access)

### Against local compose (once a Docker daemon is reachable)

```bash
COMPOSE_PROJECT=trident-chaos
COMPOSE_FILES='docker/docker-compose.yml;docker/docker-compose.ci.yml;docker/docker-compose.chaos.yml'
docker compose -p "$COMPOSE_PROJECT" \
  -f docker/docker-compose.yml \
  -f docker/docker-compose.ci.yml \
  -f docker/docker-compose.chaos.yml \
  --env-file .env.ci up -d stellar-rpc
# Wait for getHealth at http://localhost:8000/rpc to report healthy.
docker compose -p "$COMPOSE_PROJECT" \
  -f docker/docker-compose.yml \
  -f docker/docker-compose.ci.yml \
  -f docker/docker-compose.chaos.yml \
  --env-file .env.ci up -d --build
BASE_URL=http://localhost:3000 \
COMPOSE_PROJECT="$COMPOSE_PROJECT" \
COMPOSE_FILES="$COMPOSE_FILES" \
COMPOSE_ENV_FILE=.env.ci \
FAULT_SECONDS=30 \
RECOVERY_SECONDS=180 \
API_KEY=ci-test-api-key-do-not-use-in-production \
./load-tests/chaos-launch.sh
```

Use only the disposable `.env.ci` credentials. The
`docker-compose.chaos.yml` overlay supplies the local RPC service and lowers
test-only request timeouts so the circuit breaker can be observed within the
test's recovery budget.

### Against staging

The harness as written targets `docker compose` service names and cannot run
against a Kubernetes deployment unmodified. Two options, in order of
preference:

1. **Point `BASE_URL` at staging, induce faults at the Kubernetes layer.**
   Keep the harness's probe/assert logic (`probe`, `expect_degraded`,
   `expect_healthy`) but replace the `compose stop/pause/exec` calls with
   `kubectl scale --replicas=0` (Postgres/Redis down),
   `kubectl exec ... -- redis-cli FLUSHDB` (Redis eviction), or a
   network-policy/`kubectl exec ... -- tc` byte-delay for the "slow" variants.
   This preserves the exact readiness assertions this script already encodes
   against `/v1/ready`'s real contract, which is the part worth reusing.
2. **Run `docker compose` directly against staging's Postgres/Redis
   connection strings** if staging genuinely runs the same compose stack
   behind `STAGING_KUBECONFIG` (unclear from this repo alone — the deploy
   workflow uses Helm, which implies Kubernetes, not compose, actually runs
   in staging, making option 1 the realistic path).

Either way: run it, record the produced `load-tests/chaos-results/<run-id>/`
directory's `summary.txt` and `probes.csv` (both gitignored — copy the
relevant numbers into this file or a dated section here rather than the raw
directory), and file every unexpected result as its own issue per the
harness's own printed review checklist and issue #499's "Done when".
