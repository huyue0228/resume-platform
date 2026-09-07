#!/bin/sh
set -e

mkdir -p "${FILE_UPLOAD_TEMP_DIR:-/app/media/tmp_uploads}"

if [ "${RUN_MIGRATIONS:-1}" = "1" ]; then
  python manage.py migrate --noinput
fi

if [ "${RUN_SEED_BASE:-1}" = "1" ]; then
  python manage.py seed_base
fi

exec "$@"
