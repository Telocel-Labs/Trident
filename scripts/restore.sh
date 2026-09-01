#!/usr/bin/env bash
# Usage: ./scripts/restore.sh <backup_file> <target_db_url>
set -euo pipefail

BACKUP_FILE=$1
TARGET_DB_URL=$2

if [ ! -f "$BACKUP_FILE" ]; then
    echo "Backup file not found: $BACKUP_FILE"
    exit 1
fi

if [ ! -f "$BACKUP_FILE.sha256" ]; then
    echo "Checksum file not found: $BACKUP_FILE.sha256"
    exit 1
fi

echo "Verifying checksum..."
sha256sum -c "$BACKUP_FILE.sha256"

echo "Restoring database from $BACKUP_FILE to $TARGET_DB_URL..."
pg_restore --clean --if-exists --no-owner --no-privileges -d "$TARGET_DB_URL" "$BACKUP_FILE"

echo "Restore completed successfully."