# Approximate location (GeoIP)

Sessions (`GET /me/sessions`), security events (`GET /me/security-events`) and device recovery requests
(the `recovery_request` WebSocket event) carry a `location`: `{ city, country_code, country }`, every field
nullable, or `null` as a whole when nothing is known. `country_code` is ISO 3166-1 alpha-2 and `country` the
English name; clients localise the country name from the code. The API never returns client IP addresses.

IP geolocation by [DB-IP](https://db-ip.com).

## How it works

- The lookup is offline: the API reads the free **DB-IP Lite "IP to City"** database (MMDB format) from
  `GEOIP_DB_PATH` with `github.com/oschwald/maxminddb-golang/v2`. No address is ever sent to another service.
- The location is looked up once, when a session is created or refreshes its token, when a security event is
  recorded and when a recovery request starts, and is stored with the row as a JSONB snapshot (migration 22).
  Later database updates do not change stored locations.
- Private, loopback, link-local, multicast and unspecified addresses are not looked up (`null`).
- `GEOIP_DB_PATH` unset, a missing or unreadable file: every location is `null`, the API logs one warning at
  startup and keeps running. A new file is picked up on restart.
- Only the API uses the database; the worker never sees client addresses.

## Getting the database

The database is about 130 MB and is never committed (`*.mmdb` is in `.gitignore`).
`deploy/geoip/update-dbip.sh <target.mmdb> [--if-missing]` downloads the current month's
`https://download.db-ip.com/free/dbip-city-lite-YYYY-MM.mmdb.gz`, falls back to the previous month when the
new one is not published yet, checks the gzip and the MMDB marker, and replaces the target atomically.
`--if-missing` does nothing when the target already exists. `DBIP_BASE_URL` overrides the download location.

- **Development stack:** the one-shot `geoip-init` service runs the script into the `geoip-data` volume when the
  file is missing. `api` and `worker` mount the volume read-only at `/geoip` and get
  `GEOIP_DB_PATH=/geoip/dbip-city-lite.mmdb`. A failed download does not stop the stack. Refresh with
  `docker compose -f compose.dev.yaml run --rm --entrypoint sh geoip-init /scripts/update-dbip.sh /data/dbip-city-lite.mmdb`
  and restart `api`.
- **`make run`:** `deploy/geoip/update-dbip.sh data/geoip/dbip-city-lite.mmdb` once, then set
  `GEOIP_DB_PATH=data/geoip/dbip-city-lite.mmdb` in `.env`.
- **Production (`deploy/go`):** `deploy.sh` runs `geoip-init` before starting `api` and `worker`, which downloads
  the file into the `geoip-data` volume when it is missing. A failed download is reported and the deployment
  continues. Set `GEOIP_DB_PATH=/geoip/dbip-city-lite.mmdb` in `.env` (as in `env.example`). DB-IP publishes a new
  Lite release every month; refresh it with a monthly job on the host:

  ```sh
  cd /opt/messenger-go && docker compose run --rm geoip-init /data/dbip-city-lite.mmdb && docker compose restart api
  ```

## Licence

The DB-IP Lite databases are licensed under [Creative Commons Attribution 4.0](https://creativecommons.org/licenses/by/4.0/).
Any product that shows the data must credit DB-IP with a link to https://db-ip.com, for example
"IP geolocation by DB-IP" next to the location in the sessions and security activity screens.
