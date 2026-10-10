#!/bin/sh
# Starts a single ScyllaDB node for CI on 127.0.0.1:9042 and waits until it answers CQL. The node advertises
# 127.0.0.1, so drivers outside the container can reach it after discovering it.
set -eu

docker run --detach --name scylla \
  --publish 127.0.0.1:9042:9042 --publish 127.0.0.1:19042:19042 \
  scylladb/scylla:2026.1 \
  --smp 1 --memory 1G --overprovisioned 1 --developer-mode 1 --broadcast-rpc-address 127.0.0.1

for _ in $(seq 1 90); do
  if docker exec scylla cqlsh -e 'DESCRIBE KEYSPACES' >/dev/null 2>&1; then
    echo "scylla is ready"
    exit 0
  fi
  sleep 2
done
docker logs scylla
exit 1
