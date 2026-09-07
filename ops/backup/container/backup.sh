#!/usr/bin/env bash
set -Eeuo pipefail

source /opt/srf-backup/common.sh

require_env POSTGRES_DB
postgres_env
prepare_restic

MEDIA_ROOT="${MEDIA_ROOT:-/data/media}"
STATUS_DIR="${BACKUP_STATUS_DIR:-/repository/status}"
STARTED_AT="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
STARTED_EPOCH="$(date +%s)"
BACKUP_ID="$(date -u '+%Y%m%dT%H%M%SZ')"
WORK_DIR="$(mktemp -d /tmp/srf-backup.XXXXXX)"
SUCCESS=0
FAILURE_REASON=""
TOTAL_BYTES=0

mkdir -p "$STATUS_DIR"

write_status() {
  local finished_epoch duration state escaped_reason
  finished_epoch="$(date +%s)"
  duration="$((finished_epoch - STARTED_EPOCH))"
  state="failed"
  [[ "$SUCCESS" -eq 1 ]] && state="success"
  escaped_reason="${FAILURE_REASON//\"/\\\"}"
  cat > "${STATUS_DIR}/last-attempt.json.tmp" <<EOF
{"backup_id":"${BACKUP_ID}","started_at":"${STARTED_AT}","finished_at":"$(date -u '+%Y-%m-%dT%H:%M:%SZ')","status":"${state}","duration_seconds":${duration},"logical_size_bytes":${TOTAL_BYTES},"failure_reason":"${escaped_reason}"}
EOF
  mv "${STATUS_DIR}/last-attempt.json.tmp" "${STATUS_DIR}/last-attempt.json"
  cat > "${STATUS_DIR}/backup.prom.tmp" <<EOF
# HELP smart_resume_backup_last_attempt_success Whether the latest backup attempt succeeded.
# TYPE smart_resume_backup_last_attempt_success gauge
smart_resume_backup_last_attempt_success ${SUCCESS}
# HELP smart_resume_backup_last_attempt_timestamp_seconds Unix timestamp of the latest backup attempt.
# TYPE smart_resume_backup_last_attempt_timestamp_seconds gauge
smart_resume_backup_last_attempt_timestamp_seconds ${finished_epoch}
# HELP smart_resume_backup_last_duration_seconds Duration of the latest backup attempt.
# TYPE smart_resume_backup_last_duration_seconds gauge
smart_resume_backup_last_duration_seconds ${duration}
EOF
  if [[ -f "${STATUS_DIR}/last-success.env" ]]; then
    cat "${STATUS_DIR}/last-success.env" >> "${STATUS_DIR}/backup.prom.tmp"
  fi
  mv "${STATUS_DIR}/backup.prom.tmp" "${STATUS_DIR}/backup.prom"
  rm -rf "$WORK_DIR"
}
capture_failure() {
  local code=$?
  trap - ERR
  FAILURE_REASON="backup_command_failed_exit_${code}"
  exit "$code"
}
trap capture_failure ERR
trap write_status EXIT

pg_isready -d "$POSTGRES_DB" >/dev/null
pg_dump --format=custom --no-owner --no-privileges --file "${WORK_DIR}/database.dump" "$POSTGRES_DB"

DATABASE_SHA256="$(sha256sum "${WORK_DIR}/database.dump" | awk '{print $1}')"
DATABASE_BYTES="$(wc -c < "${WORK_DIR}/database.dump" | tr -d ' ')"
MEDIA_COUNT="$(media_file_count "$MEDIA_ROOT")"
MEDIA_BYTES="$(media_total_bytes "$MEDIA_ROOT")"
MEDIA_SHA256="$(media_fingerprint "$MEDIA_ROOT")"
TOTAL_BYTES="$((DATABASE_BYTES + MEDIA_BYTES))"
POSTGRES_VERSION="$(psql --dbname "$POSTGRES_DB" --tuples-only --no-align --command 'SHOW server_version')"

printf '%s  %s\n' "$DATABASE_SHA256" "database.dump" > "${WORK_DIR}/database.dump.sha256"
write_media_checksums "$MEDIA_ROOT" "${WORK_DIR}/media.sha256"
cat > "${WORK_DIR}/manifest.json" <<EOF
{"schema_version":1,"backup_id":"${BACKUP_ID}","created_at":"${STARTED_AT}","app_version":"${APP_VERSION:-unknown}","postgres_version":"${POSTGRES_VERSION}","database":"${POSTGRES_DB}","database_bytes":${DATABASE_BYTES},"database_sha256":"${DATABASE_SHA256}","media_bytes":${MEDIA_BYTES},"media_file_count":${MEDIA_COUNT},"media_tree_sha256":"${MEDIA_SHA256}","logical_backup_bytes":${TOTAL_BYTES}}
EOF

restic backup \
  "${WORK_DIR}/database.dump" \
  "${WORK_DIR}/database.dump.sha256" \
  "${WORK_DIR}/media.sha256" \
  "${WORK_DIR}/manifest.json" \
  "$MEDIA_ROOT" \
  --host "${BACKUP_HOST:-smart-resume-filter}" \
  --tag smart-resume-filter \
  --tag "$BACKUP_ID"

restic forget \
  --host "${BACKUP_HOST:-smart-resume-filter}" \
  --tag smart-resume-filter \
  --keep-hourly "${BACKUP_KEEP_HOURLY:-48}" \
  --keep-daily "${BACKUP_KEEP_DAILY:-30}" \
  --keep-monthly "${BACKUP_KEEP_MONTHLY:-12}" \
  --prune

SUCCESS=1
FAILURE_REASON=""
FINISHED_EPOCH="$(date +%s)"
cat > "${STATUS_DIR}/last-success.env.tmp" <<EOF
# HELP smart_resume_backup_last_success_timestamp_seconds Unix timestamp of the latest successful backup.
# TYPE smart_resume_backup_last_success_timestamp_seconds gauge
smart_resume_backup_last_success_timestamp_seconds ${FINISHED_EPOCH}
# HELP smart_resume_backup_last_size_bytes Logical database and media size of the latest successful backup.
# TYPE smart_resume_backup_last_size_bytes gauge
smart_resume_backup_last_size_bytes ${TOTAL_BYTES}
EOF
mv "${STATUS_DIR}/last-success.env.tmp" "${STATUS_DIR}/last-success.env"
printf '备份完成：%s，逻辑大小 %s bytes，media 文件 %s 个。\n' "$BACKUP_ID" "$TOTAL_BYTES" "$MEDIA_COUNT"
