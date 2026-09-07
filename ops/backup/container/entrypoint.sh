#!/usr/bin/env bash
set -Eeuo pipefail

case "${1:-backup}" in
  backup)
    exec /opt/srf-backup/backup.sh
    ;;
  schedule)
    exec /opt/srf-backup/schedule.sh
    ;;
  restore)
    exec /opt/srf-backup/restore.sh
    ;;
  verify)
    exec /opt/srf-backup/verify.sh
    ;;
  shell)
    exec bash
    ;;
  *)
    exec "$@"
    ;;
esac
