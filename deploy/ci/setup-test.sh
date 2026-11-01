#!/usr/bin/env bash
# Start independent stores and extract the same versioned static media tools as production.
set -euo pipefail

install_media() (
  set -euo pipefail
  container=$(docker create mwader/static-ffmpeg:8.0.1)
  trap 'docker rm "$container" >/dev/null' EXIT
  tools=$(mktemp -d)
  trap 'docker rm "$container" >/dev/null; rm -rf "$tools"' EXIT
  docker cp "$container:/ffmpeg" "$tools/ffmpeg"
  docker cp "$container:/ffprobe" "$tools/ffprobe"
  sudo install -m 755 "$tools/ffmpeg" "$tools/ffprobe" /usr/local/bin/
  ffmpeg -version
  ffprobe -version
)

./deploy/ci/start-scylla.sh & scylla_pid=$!
./deploy/ci/start-s3.sh & s3_pid=$!
./deploy/ci/start-elastic.sh & elastic_pid=$!
install_media & media_pid=$!

# A bare `wait` can hide failures. Check every child before running the tests.
status=0
for pid in "$scylla_pid" "$s3_pid" "$elastic_pid" "$media_pid"; do
  wait "$pid" || status=1
done
exit "$status"
