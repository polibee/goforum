#!/bin/sh
# Fix ownership of the data volume on first start, then drop privileges.
set -eu

mkdir -p /data/uploads /data/backups

if [ "$(id -u)" = "0" ]; then
  chown goforum:goforum /data /data/uploads /data/backups 2>/dev/null || true
  exec su-exec goforum "$@"
fi

exec "$@"
