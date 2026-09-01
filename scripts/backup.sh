#!/usr/bin/env bash
# Usage: DATABASE_URL=... ./scripts/backup.sh <output_dir>
set -euo pipefail

OUTPUT_DIR=${1:-./backups}
mkdir -p "$OUTPUT_DIR"

TIMESTAMP=$(date -u +%Y%m%d_%H%M%SZ)
FILENAME="trident_db_backup_$TIMESTAMP.dump"
OUTPUT_PATH="$OUTPUT_DIR/$FILENAME"

# Perform pg_dump in custom format (-Fc)
pg_dump -Fc "$DATABASE_URL" > "$OUTPUT_PATH"

# Create checksum
sha256sum "$OUTPUT_PATH" > "$OUTPUT_PATH.sha256"

echo "Backup created at $OUTPUT_PATH"
echo "Integrity checksum created at $OUTPUT_PATH.sha256"