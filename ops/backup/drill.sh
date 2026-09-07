#!/usr/bin/env bash
set -Eeuo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

PROJECT_NAME="${DRILL_PROJECT_NAME:-smart-resume-filter-drill}"
ENV_FILE="${ENV_FILE:-.env}"
ENV_BACKUP_TARGET="$(sed -n 's/^BACKUP_TARGET_PATH=//p' "$ENV_FILE" 2>/dev/null | tail -n 1)"
EFFECTIVE_BACKUP_TARGET="${BACKUP_TARGET_PATH:-${ENV_BACKUP_TARGET:-./backups}}"
REPORT_DIR="${DRILL_REPORT_DIR:-${EFFECTIVE_BACKUP_TARGET}/drill-reports}"
STARTED_AT="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
REPORT_ID="$(date -u '+%Y%m%dT%H%M%SZ')"
REPORT_PATH="${REPORT_DIR}/${REPORT_ID}.json"
STATUS="failed"
FAILURE_REASON=""

mkdir -p "$REPORT_DIR"

compose() {
  POSTGRES_BIND=127.0.0.1 \
  POSTGRES_PORT="${DRILL_POSTGRES_PORT:-55432}" \
  REDIS_BIND=127.0.0.1 \
  REDIS_PORT="${DRILL_REDIS_PORT:-56379}" \
  BACKEND_BIND=127.0.0.1 \
  BACKEND_PORT="${DRILL_BACKEND_PORT:-58000}" \
  FRONTEND_BIND=127.0.0.1 \
  FRONTEND_PORT="${DRILL_FRONTEND_PORT:-55173}" \
  docker compose --project-name "$PROJECT_NAME" --env-file "$ENV_FILE" -f docker-compose.yml "$@"
}

write_report() {
  local finished_at escaped_reason
  finished_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  escaped_reason="${FAILURE_REASON//\"/\\\"}"
  cat > "${REPORT_PATH}.tmp" <<EOF
{"started_at":"${STARTED_AT}","finished_at":"${finished_at}","project":"${PROJECT_NAME}","requested_snapshot":"${RESTIC_SNAPSHOT:-latest}","recovered_version":"${RECOVERED_VERSION:-unknown}","status":"${STATUS}","failure_reason":"${escaped_reason}","statistics":${STATISTICS:-{}}}
EOF
  mv "${REPORT_PATH}.tmp" "$REPORT_PATH"
  printf '演练报告：%s\n' "$REPORT_PATH"
}
cleanup() {
  compose down --volumes >/dev/null 2>&1 || true
}
on_exit() {
  local code=$?
  if (( code != 0 )) && [[ -z "$FAILURE_REASON" ]]; then
    FAILURE_REASON="command_failed_exit_${code}"
  fi
  cleanup
  write_report
  exit "$code"
}
trap on_exit EXIT

[[ -f "$ENV_FILE" ]] || { FAILURE_REASON="env_file_missing"; exit 2; }
command -v docker >/dev/null || { FAILURE_REASON="docker_missing"; exit 2; }
[[ "$PROJECT_NAME" == *drill* && "$PROJECT_NAME" != "smart-resume-filter" ]] || {
  FAILURE_REASON="unsafe_drill_project_name"
  exit 2
}

compose up -d --wait db redis
CONFIRM_RESTORE=YES_I_UNDERSTAND \
ALLOW_PRODUCTION_OVERWRITE=YES \
RESTORE_DATABASE="$(sed -n 's/^POSTGRES_DB=//p' "$ENV_FILE" | tail -n 1)" \
RESTIC_SNAPSHOT="${RESTIC_SNAPSHOT:-latest}" \
compose --profile restore run --rm restore
RECOVERED_VERSION="$(sed -n 's/.*"backup_id":"\([^"]*\)".*/\1/p' "${EFFECTIVE_BACKUP_TARGET}/status/last-restore.json")"

compose run --rm \
  -e RUN_MIGRATIONS=0 \
  -e RUN_SEED_BASE=0 \
  backend python manage.py migrate --check --plan
compose run --rm \
  -e RUN_MIGRATIONS=0 \
  -e RUN_SEED_BASE=0 \
  backend python manage.py check
STATISTICS="$(compose run --rm -e RUN_MIGRATIONS=0 -e RUN_SEED_BASE=0 backend python manage.py verify_restored_data --json)"
compose up -d --wait backend

STATUS="success"
FAILURE_REASON=""
