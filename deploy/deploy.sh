#!/usr/bin/env bash
# Runs on the server (uploaded and started over SSH by .github/workflows/deploy.yml).
# Usage: deploy.sh <app_dir> <image>
# <app_dir> holds compose.yaml and .env (with SECRET_KEY).
set -euo pipefail

APP_DIR="$1"
IMAGE="$2"
KEEP_BACKUPS=10
# Free space needed before pulling a new image (the backend image is about 170 MB compressed).
MIN_FREE_MB="${MIN_FREE_MB:-1024}"
case "$MIN_FREE_MB" in
  ''|*[!0-9]*) echo "MIN_FREE_MB must be a whole number of megabytes, got '$MIN_FREE_MB'" >&2; exit 1 ;;
esac

cd "$APP_DIR"
touch .env
mkdir -p backups

# Roll back to what is actually running; .env may name an image that was never pulled.
running_container="$(docker compose ps -q backend 2>/dev/null || true)"
previous_image=""
if [ -n "$running_container" ]; then
  previous_image="$(docker inspect --format '{{.Config.Image}}' "$running_container" 2>/dev/null || true)"
fi
if [ -z "$previous_image" ]; then
  previous_image="$(grep -E '^BACKEND_IMAGE=' .env | tail -n1 | cut -d= -f2- || true)"
fi

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

# Smallest free space (MB) of the disks images are stored on: Docker's root and, with the containerd
# image store, /var/lib/containerd.
free_mb() {
  local dirs=() dir
  dir="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
  [ -n "$dir" ] && [ -d "$dir" ] && dirs+=("$dir")
  [ -d /var/lib/containerd ] && dirs+=(/var/lib/containerd)
  [ "${#dirs[@]}" -gt 0 ] || dirs=(.)
  df -Pm "${dirs[@]}" | awk 'NR > 1 && (min == "" || $4 < min) { min = $4 } END { print min }'
}

# Old release images are tagged, so the prune after a deploy never removed them. Images used by a
# container (including the one running now, the rollback target) are kept.
docker image prune -af > /dev/null
available="$(free_mb)"
if [ -n "$available" ] && [ "$available" -lt "$MIN_FREE_MB" ]; then
  echo "Only ${available} MB free on the Docker disk after pruning unused images (need ${MIN_FREE_MB} MB); the deployment and its configuration were not changed." >&2
  exit 1
fi

# Pull before touching .env, so a failed pull leaves the running deployment and its config as they were.
BACKEND_IMAGE="$IMAGE" docker compose pull backend

if docker compose ps --status running --services | grep -qx postgres; then
  backup="backups/messenger-$(date +%Y%m%d-%H%M%S).sql.gz"
  docker compose exec -T postgres pg_dump -U messenger messenger | gzip > "$backup"
  echo "Database backup written to $backup"
  ls -1t backups/messenger-*.sql.gz | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm --
fi

set_image "$IMAGE"
docker compose up -d

if wait_healthy; then
  echo "Deployed $IMAGE"
  docker image prune -af > /dev/null
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
