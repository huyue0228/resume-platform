#!/usr/bin/env bash
set -Eeuo pipefail

require_env() {
  local key="$1"
  if [[ -z "${!key:-}" ]]; then
    printf '缺少必填环境变量：%s\n' "$key" >&2
    exit 2
  fi
}

prepare_restic() {
  require_env RESTIC_REPOSITORY
  require_env RESTIC_PASSWORD
  mkdir -p "$RESTIC_REPOSITORY"
  if [[ ! -f "${RESTIC_REPOSITORY}/config" ]]; then
    restic init
  else
    restic snapshots >/dev/null
  fi
}

open_restic() {
  require_env RESTIC_REPOSITORY
  require_env RESTIC_PASSWORD
  if [[ ! -f "${RESTIC_REPOSITORY}/config" ]]; then
    printf '备份仓库不存在或尚未初始化：%s\n' "$RESTIC_REPOSITORY" >&2
    exit 3
  fi
  restic snapshots >/dev/null
}

postgres_env() {
  require_env POSTGRES_HOST
  require_env POSTGRES_PORT
  require_env POSTGRES_USER
  require_env POSTGRES_PASSWORD
  export PGHOST="$POSTGRES_HOST"
  export PGPORT="$POSTGRES_PORT"
  export PGUSER="$POSTGRES_USER"
  export PGPASSWORD="$POSTGRES_PASSWORD"
}

media_fingerprint() {
  local root="$1"
  if [[ ! -d "$root" ]]; then
    printf '%s' "missing"
    return
  fi
  (
    cd "$root"
    find . -type f -print0 \
      | LC_ALL=C sort -z \
      | xargs -0 -r sha256sum \
      | sha256sum \
      | awk '{print $1}'
  )
}

media_file_count() {
  local root="$1"
  if [[ ! -d "$root" ]]; then
    printf '0'
    return
  fi
  find "$root" -type f -printf '.' | wc -c | tr -d ' '
}

media_total_bytes() {
  local root="$1"
  if [[ ! -d "$root" ]]; then
    printf '0'
    return
  fi
  find "$root" -type f -printf '%s\n' | awk '{ total += $1 } END { print total + 0 }'
}

write_media_checksums() {
  local root="$1"
  local target="$2"
  (
    cd "$root"
    find . -type f -print0 \
      | LC_ALL=C sort -z \
      | xargs -0 -r sha256sum > "$target"
  )
}
