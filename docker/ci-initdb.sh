#!/usr/bin/env bash
#
# Applies the full migration chain when the CI postgres container first
# initialises (issue #371).
#
# postgres' initdb entrypoint only executes files directly in
# /docker-entrypoint-initdb.d, not files in subdirectories, so the mounted
# migrations/ directory needs this shim to be applied at all.
#
# Why the whole chain rather than database/schema.sql: schema.sql is a second,
# hand-maintained copy of the schema that drifts whenever a migration lands
# without it being regenerated. The last drift left out webhook_subscriptions,
# which aborted initdb and made every e2e job fail against a postgres that
# never finished starting. Applying the same artifact production applies means
# CI cannot silently diverge from it again.

set -euo pipefail

MIGRATIONS_DIR=/docker-entrypoint-initdb.d/migrations

echo "Applying migrations from $MIGRATIONS_DIR"

for f in "$MIGRATIONS_DIR"/*.sql; do
    echo "  -> $(basename "$f")"
    psql -v ON_ERROR_STOP=1 \
         --username "$POSTGRES_USER" \
         --dbname "$POSTGRES_DB" \
         --quiet \
         --file "$f"
done

echo "Migrations applied."
