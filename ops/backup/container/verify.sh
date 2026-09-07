#!/usr/bin/env bash
set -Eeuo pipefail

source /opt/srf-backup/common.sh
open_restic

VERIFY_ROOT="$(mktemp -d /tmp/srf-verify.XXXXXX)"
trap 'rm -rf "$VERIFY_ROOT"' EXIT

restic check
restic restore "${RESTIC_SNAPSHOT:-latest}" --tag smart-resume-filter --target "$VERIFY_ROOT"
DUMP_PATH="$(find "$VERIFY_ROOT" -type f -name database.dump -print -quit)"
CHECKSUM_PATH="$(find "$VERIFY_ROOT" -type f -name database.dump.sha256 -print -quit)"
MEDIA_SOURCE="$(find "$VERIFY_ROOT" -type d -path '*/data/media' -print -quit)"
MANIFEST_PATH="$(find "$VERIFY_ROOT" -type f -name manifest.json -print -quit)"
MEDIA_CHECKSUM_PATH="$(find "$VERIFY_ROOT" -type f -name media.sha256 -print -quit)"
[[ -n "$DUMP_PATH" && -n "$CHECKSUM_PATH" ]] || {
  printf '快照缺少数据库 dump 或校验文件。\n' >&2
  exit 3
}
(cd "$(dirname "$DUMP_PATH")" && sha256sum --check "$(basename "$CHECKSUM_PATH")")
pg_restore --list "$DUMP_PATH" >/dev/null
[[ -n "$MEDIA_SOURCE" && -n "$MANIFEST_PATH" ]] || {
  printf '快照缺少 media 或 manifest。\n' >&2
  exit 3
}
EXPECTED_MEDIA_SHA256="$(sed -n 's/.*"media_tree_sha256":"\([^"]*\)".*/\1/p' "$MANIFEST_PATH")"
EXPECTED_MEDIA_COUNT="$(sed -n 's/.*"media_file_count":\([0-9][0-9]*\).*/\1/p' "$MANIFEST_PATH")"
ACTUAL_MEDIA_SHA256="$(media_fingerprint "$MEDIA_SOURCE")"
ACTUAL_MEDIA_COUNT="$(media_file_count "$MEDIA_SOURCE")"
[[ -n "$EXPECTED_MEDIA_SHA256" && -n "$EXPECTED_MEDIA_COUNT" && -n "$MEDIA_CHECKSUM_PATH" && "$EXPECTED_MEDIA_SHA256" == "$ACTUAL_MEDIA_SHA256" && "$EXPECTED_MEDIA_COUNT" == "$ACTUAL_MEDIA_COUNT" ]] || {
  printf 'media 内容 checksum 或文件数量校验失败。\n' >&2
  exit 3
}
if [[ -s "$MEDIA_CHECKSUM_PATH" ]]; then
  (cd "$MEDIA_SOURCE" && sha256sum --check "$MEDIA_CHECKSUM_PATH" >/dev/null)
elif [[ "$EXPECTED_MEDIA_COUNT" != "0" ]]; then
  printf 'media 文件校验清单为空，但 manifest 声明存在文件。\n' >&2
  exit 3
fi
printf '备份仓库和最新快照校验通过。\n'
