#!/usr/bin/env bash
# Runs cmd/migrate-v1 with the deployed image against the Python stack on this host (docs/switchover.md).
# Usage: migrate-v1.sh <app_dir> [migrate-v1 flags], for example
#   migrate-v1.sh /opt/messenger-go -dry-run -report dry.json
# Reports land in <app_dir>/reports. Needs v1.env (from v1.env.example) with V1_DATABASE_URL and V1_SECRET_KEY.
set -euo pipefail

APP_DIR="$1"
shift
cd "$APP_DIR"
if ! grep -qE '^BACKEND_IMAGE=.+' .env; then
  echo "BACKEND_IMAGE is not set in $APP_DIR/.env: deploy first." >&2
  exit 1
fi
for key in V1_DATABASE_URL V1_SECRET_KEY; do
  if ! grep -qE "^${key}=.+" v1.env 2>/dev/null; then
    echo "$key is not set in $APP_DIR/v1.env (see v1.env.example)." >&2
    exit 1
  fi
done
chmod 600 v1.env
umask 077
mkdir -p reports
MIGRATE_V1_USER="$(id -u):$(id -g)" exec docker compose -f compose.yaml -f compose.migrate-v1.yaml \
  run --rm migrate-v1 "$@"
