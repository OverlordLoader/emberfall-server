#!/usr/bin/env bash
# Nightly Postgres backup -> local dir (rsync this to Hetzner Object Storage
# or any S3-compatible bucket). Run from cron on the box:
#   0 3 * * * /opt/emberfall/scripts/backup.sh >> /var/log/emberfall-backup.log 2>&1
set -euo pipefail
cd "$(dirname "$0")/.."
BACKUP_DIR="${BACKUP_DIR:-/var/backups/emberfall}"
mkdir -p "$BACKUP_DIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
FILE="$BACKUP_DIR/emberfall-$STAMP.sql.gz"
docker compose exec -T postgres pg_dump -U emberfall emberfall | gzip > "$FILE"
# 7-day retention
find "$BACKUP_DIR" -name 'emberfall-*.sql.gz' -mtime +7 -delete
echo "backup written: $FILE"
