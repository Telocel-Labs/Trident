#!/usr/bin/env bash
set -euo pipefail

# Usage: ./scripts/restore.sh <backup_file> <target_db_url>
BACKUP_FILE=${1:?"Missing backup file argument"}
TARGET_URL=${2:?"Missing target database URL"}

# Verify checksum
CHECKSUM_FILE="${BACKUP_FILE}.sha256"
if [ -f "$CHECKSUM_FILE" ]; then
    sha256sum -c "$CHECKSUM_FILE"
else
    echo "Warning: No checksum file found for ${BACKUP_FILE}"
fi

# Restore into target database
pg_restore --clean --if-exists --no-owner --no-privileges -d "$TARGET_URL" "$BACKUP_FILE"

echo "Restore completed successfully."