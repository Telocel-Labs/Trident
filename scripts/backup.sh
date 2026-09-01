#!/usr/bin/env bash
# Usage: ./scripts/backup.sh <output_dir>
set -euo pipefail

OUTPUT_DIR=${1:-./backups}
mkdir -p "$OUTPUT_DIR"

TIMESTAMP=$(date -u +"%Y%m%d_%H%M%S%Z")
FILENAME="trident_db_backup_${TIMESTAMP}.dump"

echo "Starting backup to $OUTPUT_DIR/$FILENAME..."
pg_dump -Fc > "$OUTPUT_DIR/$FILENAME"

sha256sum "$OUTPUT_DIR/$FILENAME" > "$OUTPUT_DIR/$FILENAME.sha256"

echo "Backup completed successfully."