#!/usr/bin/env bash
# Usage: ./scripts/restore.sh <backup_file> <target_db_url>
set -euo pipefail

BACKUP_FILE=$1
TARGET_DB_URL=$2

# Verify integrity
sha256sum -c "$BACKUP_FILE.sha256"

# Restore database
# Using --clean to ensure we start from a fresh state if overwriting
pg_restore --clean --if-exists --no-owner --no-privileges -d "$TARGET_DB_URL" "$BACKUP_FILE"

echo "Restore complete for $TARGET_DB_URL"