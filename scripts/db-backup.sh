#!/usr/bin/env bash
set -euo pipefail

BACKUP_DIR="/var/backups/trident"
TIMESTAMP=$(date -u +"%Y%m%dT%H%M%SZ")
BACKUP_FILE="${BACKUP_DIR}/trident_prod_${TIMESTAMP}.sql.gz"
ALERT_WEBHOOK_URL="${ALERT_WEBHOOK_URL:-}"

mkdir -p "${BACKUP_DIR}"

echo "Starting production PostgreSQL backup..."

if pg_dump "${DATABASE_URL}" --format=custom --compress=9 | gzip > "${BACKUP_FILE}"; then
    echo "Backup completed successfully: ${BACKUP_FILE}"
    
    # Enforce retention: delete local backups older than 30 days
    find "${BACKUP_DIR}" -name "trident_prod_*.sql.gz" -mtime +30 -delete
    
    exit 0
else
    echo "ERROR: Database backup failed!" >&2
    
    if [ -n "${ALERT_WEBHOOK_URL}" ]; then
        curl -s -X POST -H "Content-Type: application/json" \
            -d "{\"alert\": \"TridentDatabaseBackupFailed\", \"severity\": \"critical\", \"message\": \"Scheduled database backup failed at ${TIMESTAMP}\"}" \
            "${ALERT_WEBHOOK_URL}" || true
    fi
    
    exit 1
fi
