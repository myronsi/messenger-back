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
value() {
  local v q
  v="$(grep -E "^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*=" .env | tail -n1 || true)"
  [ -n "$v" ] || return 0
  v="${v#*=}"
  v="${v%$'\r'}"
  v="${v#"${v%%[![:space:]]*}"}"
  case "$v" in
    \"*|\'*)
      q="${v:0:1}"
      v="${v:1}"
      case "$v" in *"$q"*) v="${v%%"$q"*}" ;; *) v="$q$v" ;; esac
      ;;
    *)
      v="${v%%[[:space:]]#*}"
      v="${v%"${v##*[![:space:]]}"}"
      ;;
  esac
  printf '%s\n' "$v"
}

API_PORT="$(value API_PORT)"; API_PORT="${API_PORT:-8080}"
# Readiness is checked where the API listens: API_BIND, or loopback when it listens everywhere.
API_HOST="$(value API_BIND)"
case "$API_HOST" in ''|0.0.0.0) API_HOST=127.0.0.1 ;; esac
WORKER_PORT="$(value WORKER_PORT)"; WORKER_PORT="${WORKER_PORT:-8081}"
STORES=(postgres redis scylla elasticsearch object-storage)
bundled_stores=false
case ",$(value COMPOSE_PROFILES)," in *,stores,*) bundled_stores=true ;; esac

calls_enabled=false
calls_flag="$(value CALLS_ENABLED | tr 'A-Z' 'a-z')"
case "$calls_flag" in
  1|t|true) calls_enabled=true ;;
  ''|0|f|false) ;;
  *) echo "CALLS_ENABLED must be true or false. Nothing was changed." >&2; exit 1 ;;
esac
CALLS_HTTP_PORT="$(value CALLS_HTTP_PORT)"; CALLS_HTTP_PORT="${CALLS_HTTP_PORT:-7880}"
if $calls_enabled; then
  problems=()
  for name in CALLS_PUBLIC_URL CALLS_API_URL CALLS_API_KEY CALLS_API_SECRET CALLS_NODE_IP CALLS_TURN_DOMAIN \
    CALLS_TURN_CERT_FILE CALLS_TURN_KEY_FILE; do
    [ -n "$(value "$name")" ] || problems+=("$name is empty")
  done
  case "$(value CALLS_PUBLIC_URL)" in
    '') ;;
    *example.com*) problems+=("CALLS_PUBLIC_URL is still the example") ;;
    wss://*) ;;
    *) problems+=("CALLS_PUBLIC_URL must start with wss://") ;;
  esac
  case "$(value CALLS_TURN_DOMAIN)" in
    '') ;;
    *example.com) problems+=("CALLS_TURN_DOMAIN is still the example") ;;
    *[!A-Za-z0-9.-]*) problems+=("CALLS_TURN_DOMAIN must be a host name") ;;
  esac
  case "$(value CALLS_API_KEY)" in
    *[!A-Za-z0-9_-]*) problems+=("CALLS_API_KEY may contain only letters, digits, _ and -") ;;
  esac
  secret="$(value CALLS_API_SECRET)"
  if [ -n "$secret" ]; then
    [ "${#secret}" -ge 32 ] || problems+=("CALLS_API_SECRET is shorter than 32 characters")
    case "$secret" in
      *[!A-Za-z0-9+/=_-]*) problems+=("CALLS_API_SECRET may contain only letters, digits and + / = _ -") ;;
      *dev-only*) problems+=("CALLS_API_SECRET is the development value") ;;
    esac
  fi
  unset secret
  if [ -n "$(value CALLS_NODE_IP)" ] && ! [[ "$(value CALLS_NODE_IP)" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
    problems+=("CALLS_NODE_IP must be the host's public IPv4 address (the Elastic IP)")
  fi
  for name in CALLS_TURN_CERT_FILE CALLS_TURN_KEY_FILE; do
    file="$(value "$name")"
    if [ -n "$file" ] && { [ ! -r "$file" ] || [ ! -s "$file" ]; }; then
      problems+=("$name does not name a file the deploy user can read")
    fi
  done
  for name in CALLS_HTTP_PORT CALLS_METRICS_PORT CALLS_TCP_PORT CALLS_TURN_TLS_PORT CALLS_TURN_UDP_PORT \
    CALLS_UDP_PORT_START CALLS_UDP_PORT_END CALLS_TURN_RELAY_PORT_START CALLS_TURN_RELAY_PORT_END; do
    case "$(value "$name")" in
      *[!0-9]*) problems+=("$name must be a port number") ;;
    esac
  done
  if [ "${#problems[@]}" -gt 0 ]; then
    echo "calls: enabled, but the settings are incomplete (docs/calls.md#deployment):" >&2
    printf '  - %s\n' "${problems[@]}" >&2
    echo "Complete them or set CALLS_ENABLED=false. Nothing was changed." >&2
    exit 1
  fi
  echo "calls: enabled"
else
  echo "calls: disabled (CALLS_ENABLED is not true)"
fi

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

log_has() { case "$1" in *"$2,"*|*"$2}"*) return 0 ;; esac; return 1; }

check_livekit_ports() {
  local before="${1:-}" logs="" server="" turn="" want
  for _ in $(seq 1 15); do
    logs="$(docker compose logs --no-color livekit 2>/dev/null || true)"
    server="$(printf '%s\n' "$logs" | grep -F '"starting LiveKit server"' | tail -n1 || true)"
    turn="$(printf '%s\n' "$logs" | grep -F '"Starting TURN server"' | tail -n1 || true)"
    [ -n "$server" ] && [ -n "$turn" ] && break
    sleep 2
  done
  if [ -z "$server" ] || [ -z "$turn" ]; then
    if [ -n "$before" ] && [ "$before" = "$(docker compose ps -q livekit 2>/dev/null || true)" ]; then
      echo "calls: the media server was not recreated and its log no longer holds the startup lines; its ports were checked when it started"
      return 0
    fi
    echo "calls: the media server did not log \"starting LiveKit server\" and \"Starting TURN server\" within 30 seconds; its ports cannot be checked." >&2
    return 1
  fi
  for want in "\"rtc.portTCP\":$(port_value CALLS_TCP_PORT 7881)" \
    "\"rtc.portUDP\":{\"Start\":$(port_value CALLS_UDP_PORT_START 50000),\"End\":$(port_value CALLS_UDP_PORT_END 50003)}"; do
    log_has "$server" "$want" || { echo "calls: the media server does not use $want; check compose.yaml." >&2; return 1; }
  done
  for want in "\"turn.relay_range_start\":$(port_value CALLS_TURN_RELAY_PORT_START 50004)" \
    "\"turn.relay_range_end\":$(port_value CALLS_TURN_RELAY_PORT_END 50100)" \
    "\"turn.portTLS\":$(port_value CALLS_TURN_TLS_PORT 5349)" "\"turn.portUDP\":$(port_value CALLS_TURN_UDP_PORT 3478)"; do
    log_has "$turn" "$want" || { echo "calls: the media server does not use $want; check compose.yaml." >&2; return 1; }
  done
}

port_value() { local v; v="$(value "$1")"; echo "${v:-$2}"; }

wait_livekit() {
  for _ in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:${CALLS_HTTP_PORT}/" > /dev/null 2>&1; then
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
if $calls_enabled; then
  livekit_image="$(docker compose config --images livekit)"
  docker image inspect "$livekit_image" > /dev/null 2>&1 || docker pull "$livekit_image"
fi

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

if ! docker compose run --rm geoip-init; then
  echo "The GeoIP database could not be downloaded; sessions and security events are shown without a location." >&2
fi

if $calls_enabled; then
  LIVEKIT_CONFIG_SHA256="$(sha256sum livekit/livekit.yaml | cut -d' ' -f1)"
  export LIVEKIT_CONFIG_SHA256
  livekit_before="$(docker compose ps -q livekit 2>/dev/null || true)"
  if ! docker compose up -d --wait --wait-timeout 120 livekit || ! wait_livekit || ! check_livekit_ports "$livekit_before"; then
    echo "calls: the media server is not healthy; $IMAGE was not deployed, ${previous_image:-nothing} keeps running." >&2
    echo "calls: fix the media server, or set CALLS_ENABLED=false to deploy without calls." >&2
    docker compose logs --no-color --tail 40 livekit >&2 || true
    exit 1
  fi
  echo "calls: media server healthy"
elif [ -n "$(docker compose ps -aq livekit 2>/dev/null || true)" ]; then
  docker compose rm -sf livekit
  echo "calls: media server stopped and removed"
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
