#!/usr/bin/env bash
# =============================================================================
# Trident — Automated PostgreSQL Backup Script
# =============================================================================
# Generates a compressed, custom-format PostgreSQL dump with SHA-256 checksum.
# Preserves table partitions, indexes, constraints, and sequences.
#
# Usage:
#   ./scripts/backup.sh [output_dir]
#
# Required Environment:
#   DATABASE_URL — Connection string to the source PostgreSQL instance
#
# Metrics:
#   On exit, writes trident_database_backup_last_success_timestamp_seconds
#   (only on success) and trident_database_backup_last_attempt_timestamp_seconds
#   (always) to node_exporter's textfile collector directory, so
#   monitoring/alerts.yml's TridentDatabaseBackupFailed can tell a healthy
#   backup cadence from a job that stopped running or started failing.
#   Override the directory with TEXTFILE_COLLECTOR_DIR if it is not at
#   node_exporter's default.
# =============================================================================

set -euo pipefail

OUTPUT_DIR="${1:-./backups}"
mkdir -p "$OUTPUT_DIR"
TEXTFILE_COLLECTOR_DIR="${TEXTFILE_COLLECTOR_DIR:-/var/lib/node_exporter/textfile_collector}"

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "[-] ERROR: DATABASE_URL environment variable is required." >&2
  exit 1
fi

write_metric() {
  # Best-effort: a missing/unwritable textfile collector directory must
  # never fail the backup itself, only the observability of it.
  local metric_file="${TEXTFILE_COLLECTOR_DIR}/trident_database_backup.prom"
  mkdir -p "$TEXTFILE_COLLECTOR_DIR" 2>/dev/null || return 0
  {
    echo "trident_database_backup_last_attempt_timestamp_seconds $(date +%s)"
    if [[ "$1" == "success" ]]; then
      echo "trident_database_backup_last_success_timestamp_seconds $(date +%s)"
    fi
  } > "${metric_file}.tmp" 2>/dev/null && mv "${metric_file}.tmp" "$metric_file" 2>/dev/null || true
}

on_failure() {
  echo "[-] Backup failed." >&2
  write_metric "failure"
}
trap on_failure ERR

TIMESTAMP=$(date -u +"%Y%m%d_%H%M%SZ")
BACKUP_FILENAME="trident_db_backup_${TIMESTAMP}.dump"
BACKUP_PATH="${OUTPUT_DIR}/${BACKUP_FILENAME}"

echo "[+] Starting Trident PostgreSQL backup at ${TIMESTAMP}..."
echo "[+] Target output: ${BACKUP_PATH}"

START_TIME=$(date +%s)

# Use custom format (-Fc) with maximum compression (-Z 6)
pg_dump \
  --format=custom \
  --compress=6 \
  --verbose \
  --no-owner \
  --no-privileges \
  --dbname="$DATABASE_URL" \
  --file="$BACKUP_PATH"

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

# Optional encryption (issue #794)
if [[ -n "${BACKUP_ENCRYPTION_KEY:-}" ]]; then
  echo "[+] Encrypting backup with AES-256 (PBKDF2)..."
  ENC_PATH="${BACKUP_PATH}.enc"
  openssl enc -aes-256-cbc -salt -pbkdf2 -in "$BACKUP_PATH" -out "$ENC_PATH" -pass env:BACKUP_ENCRYPTION_KEY
  rm -f "$BACKUP_PATH"
  BACKUP_PATH="$ENC_PATH"
fi

# Generate SHA-256 Checksum
echo "[+] Computing SHA-256 checksum..."
sha256sum "$BACKUP_PATH" > "${BACKUP_PATH}.sha256"

SIZE=$(du -h "$BACKUP_PATH" | cut -f1)
echo "[+] Backup successfully completed in ${DURATION}s (${SIZE})."
echo "[+] Artifact: ${BACKUP_PATH}"
echo "[+] Checksum: $(cat "${BACKUP_PATH}.sha256")"

# Optional off-site upload / replication (issue #794)
if [[ -n "${OFFSITE_COPY_CMD:-}" ]]; then
  echo "[+] Running off-site copy command: ${OFFSITE_COPY_CMD}..."
  eval "$OFFSITE_COPY_CMD"
fi

# Retention policy: prune backups older than BACKUP_RETENTION_DAYS (default 30)
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
if [[ "$RETENTION_DAYS" -gt 0 ]]; then
  echo "[+] Enforcing retention: cleaning up backups older than ${RETENTION_DAYS} days in ${OUTPUT_DIR}..."
  find "$OUTPUT_DIR" -type f \( -name "trident_db_backup_*.dump*" -o -name "trident_db_backup_*.dump.enc*" \) -mtime "+${RETENTION_DAYS}" -delete 2>/dev/null || true
fi

write_metric "success"
