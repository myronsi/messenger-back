#!/usr/bin/env bash
# Assembles what deploy-go.yml uploads to the server: the stack, its scripts, the media server's
# configuration and the migrations.
# Usage: deploy/go/bundle.sh <out_dir>   (run from the repository root)
set -euo pipefail

out="$1"
mkdir -p "$out/object-storage" "$out/migrations" "$out/geoip" "$out/livekit"
cp deploy/go/compose.yaml deploy/go/env.example "$out/"
cp deploy/go/deploy.sh deploy/go/migrate.sh "$out/"
cp deploy/dev/object-storage/create-buckets.sh "$out/object-storage/"
cp deploy/geoip/update-dbip.sh "$out/geoip/"
cp deploy/go/livekit/livekit.yaml "$out/livekit/"
cp -R migrations/postgres migrations/scylla "$out/migrations/"
chmod +x "$out/deploy.sh" "$out/migrate.sh" "$out/geoip/update-dbip.sh"
