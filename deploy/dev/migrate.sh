#!/bin/sh
# Applies the migrations in directory $1 to $MIGRATE_DATABASE. An empty directory is not an error
# (golang-migrate fails on it), so the stack also starts before the first migration exists.
set -eu

dir=$1
found=0
for f in "$dir"/*.up.sql "$dir"/*.up.cql; do
  if [ -e "$f" ]; then found=1; fi
done
if [ "$found" = 0 ]; then
  echo "migrate: no migrations in $dir yet"
  exit 0
fi
exec migrate -path "$dir" -database "$MIGRATE_DATABASE" up
