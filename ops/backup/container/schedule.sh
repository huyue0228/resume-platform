#!/usr/bin/env bash
set -Eeuo pipefail

INTERVAL="${BACKUP_INTERVAL_SECONDS:-3600}"
if [[ ! "$INTERVAL" =~ ^[0-9]+$ ]] || (( INTERVAL < 300 )); then
  printf 'BACKUP_INTERVAL_SECONDS 必须是大于等于 300 的整数。\n' >&2
  exit 2
fi

while true; do
  if ! /opt/srf-backup/backup.sh; then
    printf '本轮备份失败；%s 秒后重试。\n' "$INTERVAL" >&2
  fi
  sleep "$INTERVAL"
done
