#!/bin/sh
# Applies the migrations in directory $1 to $MIGRATE_DATABASE (deploy/go, inside migrate/migrate).
#
# - A database that is newer than this bundle's newest migration is a rollback to an earlier release:
#   its migrations are left alone (they are written to work with the previous version).
# - A dirty database (a migration failed halfway) stops: fix it by hand, then `migrate force` (docs/deploy-go.md).
set -eu

dir=$1
latest=0
for f in "$dir"/*.up.sql "$dir"/*.up.cql; do
  [ -e "$f" ] || continue
  n=$(basename "$f" | cut -d_ -f1)
  n=$(expr "$n" + 0)
  [ "$n" -gt "$latest" ] && latest=$n
done
if [ "$latest" = 0 ]; then
  echo "migrate: no migrations in $dir"
  exit 0
fi

# `migrate version` prints the version (or "N (dirty)") on stderr, and fails when nothing was applied yet.
if current=$(migrate -path "$dir" -database "$MIGRATE_DATABASE" version 2>&1); then
  case "$current" in
    *dirty*)
      echo "migrate: the database is dirty at version ${current%% *}: a migration failed halfway (docs/deploy-go.md)" >&2
      exit 1
      ;;
  esac
  if [ "$current" -gt "$latest" ] 2>/dev/null; then
    echo "migrate: the database is at $current, newer than this release ($latest): a rollback, nothing to migrate"
    exit 0
  fi
fi
exec migrate -path "$dir" -database "$MIGRATE_DATABASE" up
