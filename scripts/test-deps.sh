#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")/.."
compose="docker compose -f compose.test.yaml"

pg_port="${TEST_POSTGRES_PORT:-15432}"
redis_port="${TEST_REDIS_PORT:-16379}"
scylla_port="${TEST_SCYLLA_PORT:-39042}"
s3_port="${TEST_S3_PORT:-19000}"

case "${1:-up}" in
  up)
    $compose up --detach --wait --wait-timeout 300
    cat > .env.test <<EOF
TEST_DATABASE_URL=postgres://test:test@127.0.0.1:${pg_port}/test?sslmode=disable
TEST_REDIS_URL=redis://127.0.0.1:${redis_port}/0
TEST_SCYLLA_HOSTS=127.0.0.1:${scylla_port}
TEST_S3_ENDPOINT=http://127.0.0.1:${s3_port}
TEST_S3_ACCESS_KEY=testaccess
TEST_S3_SECRET_KEY=testsecret123
EOF
    echo "Test databases are ready. Wrote .env.test; load it with: set -a; . ./.env.test; set +a"
    ;;
  down)
    $compose down --volumes --remove-orphans
    rm -f .env.test
    ;;
  status)
    $compose ps
    ;;
  *)
    echo "usage: $0 [up|down|status]" >&2
    exit 2
    ;;
esac
