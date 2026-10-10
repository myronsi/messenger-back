#!/usr/bin/env bash
# Deploys the Go backend on this host (uploaded with the rest of the bundle by .github/workflows/deploy-go.yml).
# Usage: deploy.sh <app_dir> <image>
# <app_dir> holds this bundle and .env (from env.example; docs/deploy-go.md).
#
# Order: pull, (bundled stores), backup, migrations, then api and worker on the new image. If they do not
# become ready, the previous image is started again. Migrations are not rolled back: they are written to
# work with the previous version too, and the backup taken before them is in backups/.
set -euo pipefail

APP_DIR="$1"
IMAGE="$2"
KEEP_BACKUPS=10
MIN_FREE_MB="${MIN_FREE_MB:-2048}"
# PRUNE_IMAGES=false keeps unused images (a host that runs other things too, a local test).
PRUNE_IMAGES="${PRUNE_IMAGES:-true}"
case "$MIN_FREE_MB" in
  ''|*[!0-9]*) echo "MIN_FREE_MB must be a whole number of megabytes, got '$MIN_FREE_MB'" >&2; exit 1 ;;
esac

cd "$APP_DIR"
if [ ! -f .env ]; then
  echo "$APP_DIR/.env is missing: create it from env.example (docs/deploy-go.md). Nothing was changed." >&2
  exit 1
fi
chmod 600 .env
umask 077
mkdir -p backups

# value prints a setting from .env (the last one wins, as in Compose).
value() { grep -E "^$1=" .env | tail -n1 | cut -d= -f2- || true; }

API_PORT="$(value API_PORT)"; API_PORT="${API_PORT:-8080}"
# Readiness is checked where the API listens: API_BIND, or loopback when it listens everywhere.
API_HOST="$(value API_BIND)"
case "$API_HOST" in ''|0.0.0.0) API_HOST=127.0.0.1 ;; esac
WORKER_PORT="$(value WORKER_PORT)"; WORKER_PORT="${WORKER_PORT:-8081}"
STORES=(postgres redis scylla elasticsearch object-storage)
bundled_stores=false
case ",$(value COMPOSE_PROFILES)," in *,stores,*) bundled_stores=true ;; esac

# Roll back to what is actually running; .env may name an image that was never pulled.
previous_image=""
running="$(docker compose ps -q api 2>/dev/null || true)"
if [ -n "$running" ]; then
  previous_image="$(docker inspect --format '{{.Config.Image}}' "$running" 2>/dev/null || true)"
fi
if [ -z "$previous_image" ]; then
  previous_image="$(value BACKEND_IMAGE)"
fi

set_image() {
  # grep exits 1 when nothing is left, which is fine; 2 (a read error) must not truncate .env.
  grep -v '^BACKEND_IMAGE=' .env > .env.tmp || [ $? -eq 1 ]
  printf 'BACKEND_IMAGE=%s\n' "$1" >> .env.tmp
  mv .env.tmp .env
}

# The image has no shell, so readiness is checked from here: /readyz covers every store.
wait_ready() {
  for _ in $(seq 1 60); do
    if curl -fsS "http://${API_HOST}:${API_PORT}/readyz" > /dev/null 2>&1 &&
      curl -fsS "http://127.0.0.1:${WORKER_PORT}/healthz" > /dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

free_mb() {
  local dirs=() dir
  dir="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
  [ -n "$dir" ] && [ -d "$dir" ] && dirs+=("$dir")
  [ -d /var/lib/containerd ] && dirs+=(/var/lib/containerd)
  [ "${#dirs[@]}" -gt 0 ] || dirs=(.)
  df -Pm "${dirs[@]}" | awk 'NR > 1 && (min == "" || $4 < min) { min = $4 } END { print min }'
}

# Images used by a container (the running one, the rollback target) are kept.
prune() { [ "$PRUNE_IMAGES" = false ] || docker image prune -af > /dev/null; }
prune
available="$(free_mb)"
if [ -n "$available" ] && [ "$available" -lt "$MIN_FREE_MB" ]; then
  echo "Only ${available} MB free on the Docker disk (need ${MIN_FREE_MB} MB). Nothing was changed." >&2
  exit 1
fi

# Pull before touching anything, so a failed pull leaves the deployment as it was. A release tag never
# moves, so an image that is here already is that release.
docker image inspect "$IMAGE" > /dev/null 2>&1 || docker pull "$IMAGE"

if $bundled_stores; then
  docker compose up -d --wait "${STORES[@]}"
  docker compose run --rm scylla-keyspace
  docker compose run --rm object-storage-init
fi

backup="backups/messenger-$(date +%Y%m%d-%H%M%S).dump"
if ! docker compose run --rm -T postgres-backup > "$backup"; then
  rm -f "$backup"
  echo "The database backup failed; nothing was migrated or deployed." >&2
  exit 1
fi
echo "Database backup written to $backup"
ls -1t backups/messenger-*.dump | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm --

if ! docker compose run --rm postgres-migrate || ! docker compose run --rm scylla-migrate; then
  echo "A migration failed; ${previous_image:-nothing} keeps running. See docs/deploy-go.md (dirty migrations, $backup)." >&2
  exit 1
fi

set_image "$IMAGE"
# A container that cannot even start fails `up`; that rolls back like a failed readiness check.
if docker compose up -d --no-deps api worker && wait_ready; then
  echo "Deployed $IMAGE"
  prune
  exit 0
fi

echo "Readiness check failed for $IMAGE" >&2
docker compose logs --no-color --tail 80 api worker >&2 || true

if [ -n "$previous_image" ] && [ "$previous_image" != "$IMAGE" ] && [ "$previous_image" != "unset" ]; then
  echo "Rolling back to $previous_image" >&2
  set_image "$previous_image"
  docker compose up -d --no-deps api worker
  wait_ready && echo "Rolled back to $previous_image" >&2
fi
exit 1
