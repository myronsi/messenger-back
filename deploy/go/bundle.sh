#!/usr/bin/env bash
# Assembles what deploy-go.yml uploads to the server: the stack, its scripts and the migrations.
# Usage: deploy/go/bundle.sh <out_dir>   (run from the repository root)
set -euo pipefail

out="$1"
mkdir -p "$out/object-storage" "$out/migrations"
cp deploy/go/compose.yaml deploy/go/compose.migrate-v1.yaml deploy/go/env.example deploy/go/v1.env.example "$out/"
cp deploy/go/deploy.sh deploy/go/migrate-v1.sh deploy/go/migrate.sh "$out/"
cp deploy/dev/object-storage/create-buckets.sh "$out/object-storage/"
cp -R migrations/postgres migrations/scylla "$out/migrations/"
chmod +x "$out/deploy.sh" "$out/migrate-v1.sh" "$out/migrate.sh"
