#!/bin/sh
set -eu

usage() {
  echo "usage: $0 <target.mmdb> [--if-missing]" >&2
  echo "Downloads the DB-IP Lite city database (IP geolocation by DB-IP, https://db-ip.com, CC BY 4.0)." >&2
  exit 2
}

target="${1:-}"
[ -n "$target" ] || usage
mode="${2:-}"
case "$mode" in
  ''|--if-missing) ;;
  *) usage ;;
esac

if [ "$mode" = "--if-missing" ] && [ -s "$target" ]; then
  echo "GeoIP database already present at $target"
  exit 0
fi

dir="$(dirname "$target")"
mkdir -p "$dir"
tmp="$(mktemp "$dir/.dbip-city-lite.XXXXXX")"
trap 'rm -f "$tmp" "$tmp.gz"' EXIT
trap 'exit 1' INT TERM

year="$(date -u +%Y)"
month="$(date -u +%m)"
month="${month#0}"
prev_year="$year"
prev_month=$((month - 1))
if [ "$prev_month" -eq 0 ]; then
  prev_month=12
  prev_year=$((year - 1))
fi

base="${DBIP_BASE_URL:-https://download.db-ip.com/free}"
release=""
for candidate in "$(printf '%04d-%02d' "$year" "$month")" "$(printf '%04d-%02d' "$prev_year" "$prev_month")"; do
  url="$base/dbip-city-lite-$candidate.mmdb.gz"
  echo "Downloading $url"
  if curl --fail --silent --show-error --location --retry 3 --connect-timeout 20 --max-time 1800 --output "$tmp.gz" "$url"; then
    release="$candidate"
    break
  fi
  echo "Not available: $url" >&2
done
if [ -z "$release" ]; then
  echo "No DB-IP Lite city database could be downloaded; $target was not changed" >&2
  exit 1
fi

if ! gzip -t "$tmp.gz"; then
  echo "The download of $release is not a valid gzip file; $target was not changed" >&2
  exit 1
fi
gzip -dc "$tmp.gz" > "$tmp"
if [ ! -s "$tmp" ] || ! tail -c 131072 "$tmp" | tr -c 'A-Za-z.' '\n' | grep -qx 'MaxMind.com'; then
  echo "The download of $release is not a MaxMind DB file; $target was not changed" >&2
  exit 1
fi
chmod 0644 "$tmp"
mv -f "$tmp" "$target"
echo "Installed DB-IP Lite city database $release at $target (IP geolocation by DB-IP, https://db-ip.com, CC BY 4.0)"
