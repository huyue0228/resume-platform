#!/usr/bin/env bash
set -Eeuo pipefail

source /opt/srf-backup/common.sh

require_env POSTGRES_DB
require_env RESTORE_DATABASE
require_env CONFIRM_RESTORE
if [[ "$CONFIRM_RESTORE" != "YES_I_UNDERSTAND" ]]; then
  printf '恢复被拒绝：必须显式设置 CONFIRM_RESTORE=YES_I_UNDERSTAND。\n' >&2
  exit 2
fi
if [[ "$RESTORE_DATABASE" == "$POSTGRES_DB" && "${ALLOW_PRODUCTION_OVERWRITE:-}" != "YES" ]]; then
  printf '恢复被拒绝：目标是运行数据库；还需设置 ALLOW_PRODUCTION_OVERWRITE=YES。\n' >&2
  exit 2
fi

postgres_env
open_restic

if [[ ! "$RESTORE_DATABASE" =~ ^[A-Za-z0-9_]+$ ]]; then
  printf 'RESTORE_DATABASE 只能包含字母、数字和下划线。\n' >&2
  exit 2
fi
ACTIVE_CONNECTIONS="$(psql --dbname postgres --tuples-only --no-align --command "SELECT count(*) FROM pg_stat_activity WHERE datname = '${RESTORE_DATABASE}'")"
if (( ACTIVE_CONNECTIONS > 0 )); then
  printf '恢复被拒绝：目标库仍有 %s 个连接。请先停止应用服务。\n' "$ACTIVE_CONNECTIONS" >&2
  exit 2
fi

RESTORE_ROOT="$(mktemp -d /tmp/srf-restore.XXXXXX)"
STAGING_DATABASE=""
cleanup_restore() {
  if [[ -n "$STAGING_DATABASE" ]]; then
    dropdb --if-exists "$STAGING_DATABASE" >/dev/null 2>&1 || true
  fi
  rm -rf "$RESTORE_ROOT"
}
trap cleanup_restore EXIT
restic restore "${RESTIC_SNAPSHOT:-latest}" --tag smart-resume-filter --target "$RESTORE_ROOT"

DUMP_PATH="$(find "$RESTORE_ROOT" -type f -name database.dump -print -quit)"
CHECKSUM_PATH="$(find "$RESTORE_ROOT" -type f -name database.dump.sha256 -print -quit)"
MEDIA_SOURCE="$(find "$RESTORE_ROOT" -type d -path '*/data/media' -print -quit)"
MANIFEST_PATH="$(find "$RESTORE_ROOT" -type f -name manifest.json -print -quit)"
MEDIA_CHECKSUM_PATH="$(find "$RESTORE_ROOT" -type f -name media.sha256 -print -quit)"
[[ -n "$DUMP_PATH" && -n "$CHECKSUM_PATH" ]] || {
  printf '快照缺少数据库 dump 或校验文件。\n' >&2
  exit 3
}

(cd "$(dirname "$DUMP_PATH")" && sha256sum --check "$(basename "$CHECKSUM_PATH")")
pg_restore --list "$DUMP_PATH" >/dev/null
[[ -n "$MANIFEST_PATH" ]] || {
  printf '快照缺少 manifest.json。\n' >&2
  exit 3
}
EXPECTED_MEDIA_SHA256="$(sed -n 's/.*"media_tree_sha256":"\([^"]*\)".*/\1/p' "$MANIFEST_PATH")"
EXPECTED_MEDIA_COUNT="$(sed -n 's/.*"media_file_count":\([0-9][0-9]*\).*/\1/p' "$MANIFEST_PATH")"
BACKUP_ID="$(sed -n 's/.*"backup_id":"\([^"]*\)".*/\1/p' "$MANIFEST_PATH")"
[[ -n "$EXPECTED_MEDIA_SHA256" && -n "$EXPECTED_MEDIA_COUNT" && -n "$BACKUP_ID" && -n "$MEDIA_CHECKSUM_PATH" ]] || {
  printf '备份 manifest 缺少 media checksum、文件数量或 backup_id。\n' >&2
  exit 3
}

if [[ "${RESTORE_MEDIA:-1}" == "1" ]]; then
  require_env RESTORE_MEDIA_ROOT
  [[ -n "$MEDIA_SOURCE" ]] || {
    printf '快照缺少 media 目录。\n' >&2
    exit 3
  }
  ACTUAL_MEDIA_SHA256="$(media_fingerprint "$MEDIA_SOURCE")"
  ACTUAL_MEDIA_COUNT="$(media_file_count "$MEDIA_SOURCE")"
  if [[ "$ACTUAL_MEDIA_SHA256" != "$EXPECTED_MEDIA_SHA256" || "$ACTUAL_MEDIA_COUNT" != "$EXPECTED_MEDIA_COUNT" ]]; then
    printf 'media 内容 checksum 或文件数量校验失败。\n' >&2
    exit 3
  fi
  if [[ -s "$MEDIA_CHECKSUM_PATH" ]]; then
    (cd "$MEDIA_SOURCE" && sha256sum --check "$MEDIA_CHECKSUM_PATH" >/dev/null)
  elif [[ "$EXPECTED_MEDIA_COUNT" != "0" ]]; then
    printf 'media 文件校验清单为空，但 manifest 声明存在文件。\n' >&2
    exit 3
  fi
fi

# 所有备份内容必须先完成校验，再对目标数据库或 media 做任何修改。
STAGING_DATABASE="srf_restore_stage_$(date -u '+%Y%m%d%H%M%S')_$$"
createdb "$STAGING_DATABASE"
pg_restore --exit-on-error --no-owner --no-privileges --dbname "$STAGING_DATABASE" "$DUMP_PATH"

# 临时库完整恢复成功后才替换目标库；损坏 dump 不会提前删除目标库。
dropdb --if-exists "$RESTORE_DATABASE"
psql --dbname postgres --set ON_ERROR_STOP=1 --command \
  "ALTER DATABASE \"${STAGING_DATABASE}\" RENAME TO \"${RESTORE_DATABASE}\"" >/dev/null
STAGING_DATABASE=""

if [[ "${RESTORE_MEDIA:-1}" == "1" ]]; then
  mkdir -p "$RESTORE_MEDIA_ROOT"
  find "$RESTORE_MEDIA_ROOT" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
  cp -a "${MEDIA_SOURCE}/." "$RESTORE_MEDIA_ROOT/"
fi

mkdir -p "${BACKUP_STATUS_DIR:-/repository/status}"
cat > "${BACKUP_STATUS_DIR:-/repository/status}/last-restore.json.tmp" <<EOF
{"backup_id":"${BACKUP_ID}","snapshot":"${RESTIC_SNAPSHOT:-latest}","restored_at":"$(date -u '+%Y-%m-%dT%H:%M:%SZ')","database":"${RESTORE_DATABASE}"}
EOF
mv "${BACKUP_STATUS_DIR:-/repository/status}/last-restore.json.tmp" "${BACKUP_STATUS_DIR:-/repository/status}/last-restore.json"
printf '恢复完成：backup_id=%s snapshot=%s database=%s media=%s。\n' "$BACKUP_ID" "${RESTIC_SNAPSHOT:-latest}" "$RESTORE_DATABASE" "${RESTORE_MEDIA:-1}"
