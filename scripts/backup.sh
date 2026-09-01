#!/usr/bin/env bash
set -euo pipefail

# Usage: ./scripts/backup.sh ./backups
BACKUP_DIR=${1:-./backups}
TIMESTAMP=$(date -u +%Y%m%d_%H%M%SZ)
FILENAME="trident_db_backup_${TIMESTAMP}.dump"

mkdir -p "$BACKUP_DIR"

# Perform pg_dump in custom format (-Fc) for restore flexibility
pg_dump -Fc -f "${BACKUP_DIR}/${FILENAME}"

# Generate SHA256 checksum for integrity
sha256sum "${BACKUP_DIR}/${FILENAME}" > "${BACKUP_DIR}/${FILENAME}.sha256"

echo "Backup created: ${BACKUP_DIR}/${FILENAME}"