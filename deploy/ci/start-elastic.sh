#!/bin/sh
# Starts a single-node Elasticsearch (as in the dev stack, security off: CI only) on 127.0.0.1:9200 and waits
# until it answers.
set -eu

docker run --detach --name elastic --publish 127.0.0.1:9200:9200 \
  --env discovery.type=single-node --env xpack.security.enabled=false \
  --env ES_JAVA_OPTS="-Xms512m -Xmx512m" \
  docker.elastic.co/elasticsearch/elasticsearch:9.5.3

for _ in $(seq 1 120); do
  if curl -fs 'http://127.0.0.1:9200/_cluster/health?wait_for_status=yellow&timeout=1s' >/dev/null 2>&1; then
    echo "elasticsearch is ready"
    exit 0
  fi
  sleep 1
done
docker logs elastic
exit 1
