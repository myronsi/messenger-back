#!/usr/bin/env bash
# Runs on the server (uploaded and started over SSH by .github/workflows/deploy.yml).
# Usage: deploy.sh <app_dir> <image>
# <app_dir> holds compose.yaml and .env (with SECRET_KEY).
set -euo pipefail

APP_DIR="$1"
IMAGE="$2"
KEEP_BACKUPS=10

cd "$APP_DIR"
touch .env
mkdir -p backups

previous_image="$(grep -E '^BACKEND_IMAGE=' .env | tail -n1 | cut -d= -f2- || true)"

set_image() {
  grep -v '^BACKEND_IMAGE=' .env > .env.tmp || true
  printf 'BACKEND_IMAGE=%s\n' "$1" >> .env.tmp
  mv .env.tmp .env
}

wait_healthy() {
  for _ in $(seq 1 30); do
    if curl -fsS http://127.0.0.1:8000/ > /dev/null; then
      return 0
    fi
    sleep 2
  done
  return 1
}

if docker compose ps --status running --services | grep -qx postgres; then
  backup="backups/messenger-$(date +%Y%m%d-%H%M%S).sql.gz"
  docker compose exec -T postgres pg_dump -U messenger messenger | gzip > "$backup"
  echo "Database backup written to $backup"
  ls -1t backups/messenger-*.sql.gz | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm --
fi

set_image "$IMAGE"
docker compose pull backend
docker compose up -d

if wait_healthy; then
  echo "Deployed $IMAGE"
  docker image prune -f > /dev/null
  exit 0
fi

echo "Health check failed for $IMAGE" >&2
docker compose logs --tail 80 backend >&2 || true

if [ -n "$previous_image" ] && [ "$previous_image" != "$IMAGE" ]; then
  echo "Rolling back to $previous_image" >&2
  set_image "$previous_image"
  docker compose up -d
  wait_healthy && echo "Rolled back to $previous_image" >&2
fi
exit 1
