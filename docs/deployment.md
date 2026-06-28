# Trident Production Deployment Runbook

Trident is a Stellar blockchain event indexer. The production stack runs four services under Docker Compose: `postgres`, `redis`, `indexer` (Rust), and `api` (Go), with `nginx` providing TLS termination via a prod overlay.

---

## Prerequisites

Before deploying, ensure the following are ready on the target server:

- Docker v24 or later
- Docker Compose v2 (`docker compose`, not `docker-compose`)
- DNS A record pointing your domain to the server's public IP
- TLS certificate files: `fullchain.pem` and `privkey.pem`
- Git installed

---

## First Deployment

### 1. Clone the repository

```bash
git clone https://github.com/Telocel-Labs/Trident.git
cd Trident
```

### 2. Create `.env` from the example

```bash
cp .env.example .env
```

### 3. Configure required environment variables

Open `.env` and set every value below. Do not leave defaults in production.

| Variable | Description |
|---|---|
| `DATABASE_URL` | PostgreSQL connection string, e.g. `postgresql://trident:password@postgres:5432/trident` |
| `REDIS_URL` | Redis connection string, e.g. `redis://redis:6379` |
| `STELLAR_RPC_URL` | Soroban RPC endpoint (`https://soroban-testnet.stellar.org` for testnet) |
| `NETWORK` | One of `mainnet`, `testnet`, or `futurenet` |
| `POLL_INTERVAL_MS` | Ledger poll interval in milliseconds (default: `5000`) |
| `INDEX_DIAGNOSTIC` | Set `false` in production (diagnostic events are high-volume) |
| `LOG_LEVEL` | One of `error`, `warn`, `info`, `debug`, `trace` (use `info` in production) |
| `PORT` | API listen port (default: `3000`) |
| `API_KEY_SALT` | Random secret for hashing API keys — **must be changed** |
| `POSTGRES_USER` | PostgreSQL username |
| `POSTGRES_PASSWORD` | PostgreSQL password |
| `POSTGRES_DB` | PostgreSQL database name |
| `ALLOWED_ORIGINS` | Comma-separated allowed CORS origins, or `*` to allow all |
| `REQUEST_TIMEOUT_MS` | HTTP request timeout in milliseconds (default: `30000`) |

Generate a secure `API_KEY_SALT`:

```bash
openssl rand -hex 32
```

### 4. Place TLS certificates

The nginx service expects certificates in the `nginx_certs` Docker volume.

```bash
docker volume create trident_nginx_certs
```

> **Note**: Docker Compose prefixes volume names with the project name (directory name by default).
> If your working directory is not named `trident`, use
> `docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml config --volumes`
> to find the actual volume name, then substitute it in the `docker volume create` and `docker run` commands above.

```bash
docker run --rm \
  -v trident_nginx_certs:/certs \
  -v $(pwd)/certs:/src \
  alpine \
  sh -c "cp /src/fullchain.pem /certs/ && cp /src/privkey.pem /certs/"
```

Replace `$(pwd)/certs` with the directory containing your certificate files.

### 5. Start PostgreSQL and run database migrations

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml up -d postgres
```

Wait for the health check to pass (postgres has a 15 s start period, 10 retries):

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  ps postgres
```

Apply migrations in order:

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB \
  -f /docker-entrypoint-initdb.d/0001_init.sql

docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB \
  -f /docker-entrypoint-initdb.d/0002_system_state_health.sql
```

### 6. Start all services

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml up -d
```

### 7. Verify health

```bash
curl https://your-domain.com/v1/health
```

Expected response:

```json
{"status":"ok"}
```

---

## Updating (Rolling Update)

### 1. Pull latest images and rebuild

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml pull
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml build
```

### 2. Check for new migrations

Inspect `database/migrations/` for any files added since the last deploy. Apply each new file in ascending numeric order:

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB \
  -f /docker-entrypoint-initdb.d/<new-migration-file>.sql
```

Current migration files:
- `0001_init.sql`
- `0002_system_state_health.sql`

### 3. Restart the API service (zero-downtime)

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  up -d --no-deps api
```

To also restart the indexer:

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  up -d --no-deps indexer
```

### 4. Verify deployment

```bash
curl https://your-domain.com/v1/health
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  logs --tail=50 api
```

---

## Rollback

### 1. Identify the previous image

```bash
docker images | grep trident
```

### 2. Update the image tag in compose or re-tag, then restart

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  up -d --no-deps api
```

### 3. Handle migration rollback (if applicable)

Migrations in `database/migrations/` are plain SQL and have no automated down path. If a schema change must be reversed, write and apply the inverse SQL manually. Review the relevant migration file before making any irreversible changes in production.

### 4. Verify health after rollback

```bash
curl https://your-domain.com/v1/health
```

---

## Secret Rotation

### `API_KEY_SALT`

> **Warning:** Rotating `API_KEY_SALT` invalidates all existing API keys. Clients must re-authenticate after rotation.

1. Generate a new salt:
   ```bash
   openssl rand -hex 32
   ```
2. Update `API_KEY_SALT` in `.env`.
3. Restart the API service:
   ```bash
   docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
     up -d --no-deps api
   ```

### `POSTGRES_PASSWORD`

1. Connect to PostgreSQL and change the password:
   ```bash
   docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
     exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB
   ```
   ```sql
   ALTER USER trident WITH PASSWORD 'new-password';
   \q
   ```
2. Update `DATABASE_URL` and `POSTGRES_PASSWORD` in `.env`.
3. Restart all services that connect to the database:
   ```bash
   docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
     up -d --no-deps api indexer
   ```

### `REDIS_PASSWORD`

1. Update the Redis ACL or `requirepass` setting in your Redis config.
2. Update `REDIS_URL` in `.env` to include the new password (e.g. `redis://:new-password@redis:6379`).
3. Restart the services that use Redis:
   ```bash
   docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
     up -d --no-deps api indexer
   ```

---

## Monitoring

### Health Endpoints

| Endpoint | Description |
|---|---|
| `GET /v1/health` | Public liveness check. Returns indexer poll status. |
| `GET /internal/status` | Internal metrics endpoint (planned for a future release). |

`/v1/health` response shapes:

```json
{"status":"ok"}
```
Indexer is polling within the last 60 seconds.

```json
{"status":"degraded"}
```
Indexer has stalled or the database is unreachable.

### PostgreSQL Disk Usage

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB \
  -c "SELECT pg_size_pretty(pg_database_size('$POSTGRES_DB'));"
```

Alert when disk usage exceeds 80% of available space.

### Redis Stream Backlog

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  exec redis redis-cli XLEN trident:events
```

A growing `trident:events` stream length indicates consumer lag. Investigate the `api` service logs if the stream is not draining.

### Indexer Lag

Check `last_poll_at` in the health response. If `status` is `degraded` or `last_poll_at` is more than 5 minutes ago, the indexer has stalled.

```bash
curl https://your-domain.com/v1/health | jq .
```

### nginx / WebSocket Connections

WebSocket connections arrive at `/ws` through nginx. Monitor active connections in nginx access logs:

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml \
  logs nginx | grep "/ws" | tail -20
```

### View Service Logs

```bash
# All services
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml logs -f

# Single service
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml logs -f api
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml logs -f indexer
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml logs -f postgres
docker compose -f docker/docker-compose.yml -f docker/docker-compose.prod.yml logs -f nginx
```

---

## Connection Topology

Trident runs three database clients:

| Service              | Role                         | Pool env var            | Default |
| -------------------- | ---------------------------- | ----------------------- | ------- |
| Indexer (Rust)       | single writer, low write QPS | `INDEXER_DB_POOL_SIZE`  | 3       |
| gRPC API (Rust)      | read-heavy, moderate QPS     | `GRPC_API_DB_POOL_SIZE` | 10      |
| REST API (Go)        | per-replica request handler  | `GO_API_DB_POOL_SIZE`   | 5       |

In production every service connects to **PgBouncer**, never to Postgres
directly. PgBouncer (transaction pooling mode) multiplexes the many short-lived
application connections over a small set of real Postgres connections, so
Postgres never approaches its `max_connections` limit even as the Go API scales
to multiple replicas.

```
indexer  ─┐
gRPC API ─┼─▶  PgBouncer (pgbouncer:6432, transaction mode)  ─▶  Postgres :5432
Go API   ─┘        default_pool_size = 20
(N replicas)
```

### PgBouncer Transaction Mode: Common Pitfalls

Transaction pooling is efficient but means **no session state survives across transaction boundaries**. The following do **not** work in transaction mode:

1. **Named/server-side prepared statements** — A prepared statement lives on one server connection; the next transaction may land on another.
2. **`SET SESSION` variables** — Not preserved across transactions.
3. **Session-level advisory locks** — Behave unexpectedly because the "session" is not stable.

Trident's clients are configured to avoid (1):

- **Rust (sqlx):** `PgConnectOptions::statement_cache_capacity(0)`
- **Go (pgx v5):** `cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol`

### Schema Migrations

Run migrations against a **direct** Postgres connection, not the transaction-mode pooler. Keep a direct DSN available for that purpose; do not point your migration tool at `pgbouncer:6432`.

## Admin Stats Endpoint

The Go API exposes `GET /v1/admin/db` for capacity planning. Set `ADMIN_API_KEY` and `PGBOUNCER_ADMIN_URL` in `.env`, then:

```bash
curl -H "X-Admin-Key: $ADMIN_API_KEY" http://localhost:3000/v1/admin/db
```

A missing or wrong key returns `401`; an unreachable PgBouncer returns `502`.

## Load Testing

`load-tests/pgbouncer-validation.js` is a [k6](https://k6.io) script that drives
100 concurrent clients, each issuing 10 requests to `GET /v1/events`, and asserts
no `too many connections` errors with p99 latency under 500ms.

```bash
BASE_URL=http://localhost:3000 k6 run load-tests/pgbouncer-validation.js
```

---

## Fly.io Deployment

Trident can be deployed to [Fly.io](https://fly.io) as three separate apps sharing a private network (6PN). Configuration files live in `fly/`.

### Prerequisites

- [flyctl](https://fly.io/docs/flyctl/installing/) installed and authenticated (`fly auth login`)
- A Fly.io organization with Fly Postgres and Fly Redis provisioned

### App topology

| App name | Config | Description |
|---|---|---|
| `trident-grpc-api` | `fly/grpc-api.toml` | Rust gRPC API — event query backend |
| `trident-indexer` | `fly/indexer.toml` | Rust Stellar event indexer |
| `trident-api` | `fly/api.toml` | Go REST API — public-facing |

Services communicate over Fly's private 6PN network:
- Go API → gRPC API at `trident-grpc-api.internal:50051`
- Indexer → database and Redis directly (no external exposure needed)

### First-time setup

#### 1. Create the Fly apps

```bash
fly apps create trident-grpc-api
fly apps create trident-indexer
fly apps create trident-api
```

#### 2. Provision Fly Postgres

```bash
fly postgres create --name trident-db --region iad
fly postgres attach trident-db -a trident-grpc-api
fly postgres attach trident-db -a trident-indexer
fly postgres attach trident-db -a trident-api
```

`fly postgres attach` automatically sets `DATABASE_URL` as a secret on each app.

#### 3. Provision Fly Redis

```bash
fly redis create --name trident-redis --region iad
```

Note the Redis URL from the output, then set it on the apps that need it:

```bash
fly secrets set -a trident-indexer REDIS_URL="redis://..."
fly secrets set -a trident-api     REDIS_URL="redis://..."
```

#### 4. Set required secrets

**gRPC API** (`trident-grpc-api`):
```bash
# DATABASE_URL already set by postgres attach
```

**Indexer** (`trident-indexer`):
```bash
fly secrets set -a trident-indexer \
  STELLAR_RPC_URL="https://soroban-testnet.stellar.org" \
  NETWORK="testnet"
```

**Go REST API** (`trident-api`):
```bash
fly secrets set -a trident-api \
  API_KEY_SALT="$(openssl rand -hex 32)" \
  ADMIN_API_KEY="$(openssl rand -hex 32)"
```

#### 5. Run database migrations

Attach to a temporary machine in the Trident private network and run migrations directly against Postgres (not through PgBouncer):

```bash
fly ssh console -a trident-grpc-api -C \
  "psql \$DATABASE_URL -f /path/to/migrations/0001_init.sql"
```

Or use a local `psql` with the direct Postgres URL (bypassing PgBouncer).

#### 6. Deploy

```bash
make deploy
```

This deploys in dependency order: gRPC API → Indexer → Go REST API.

To deploy a single service:

```bash
fly deploy -c fly/grpc-api.toml --remote-only
fly deploy -c fly/indexer.toml --remote-only
fly deploy -c fly/api.toml     --remote-only
```

### Scaling

```bash
fly scale count 2 -a trident-api       # scale Go API to 2 instances
fly scale vm shared-cpu-2x -a trident-indexer  # upgrade indexer VM
```

### Monitoring

- **Indexer metrics**: accessible on the 6PN at `trident-indexer.internal:9090/metrics`
- **Go API metrics**: `GET /metrics` on the public `trident-api` endpoint
- **Health check**: `GET /v1/health` (used by Fly's HTTP service check)

### Updating secrets

```bash
fly secrets set -a <app-name> KEY=new_value
```

Fly automatically redeploys the app when secrets change.

### Rollback

```bash
fly releases -a trident-api          # list releases
fly deploy --image-label <version> -a trident-api  # roll back to a specific release
```
