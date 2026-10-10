#!/bin/sh
# Starts an S3-compatible server (RustFS, as in the dev stack) for CI on 127.0.0.1:9000 and waits for it.
set -eu

docker run --detach --name s3 --publish 127.0.0.1:9000:9000 \
  --env RUSTFS_ACCESS_KEY="$TEST_S3_ACCESS_KEY" --env RUSTFS_SECRET_KEY="$TEST_S3_SECRET_KEY" \
  rustfs/rustfs:1.0.1

for _ in $(seq 1 60); do
  if curl -fs http://127.0.0.1:9000/health >/dev/null 2>&1; then
    echo "s3 is ready"
    exit 0
  fi
  sleep 1
done
docker logs s3
exit 1
