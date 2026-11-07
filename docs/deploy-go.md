# Deploying the Go backend

How Go releases get to staging and production after the completed Python cutover. The production requirements of the stores are in [deployment.md](deployment.md), the release process in [releasing.md](releasing.md), and the switch itself in [switchover.md](switchover.md).

## What goes where

Only Go releases are supported; Python `0.x` tags are retired:

| Release | Image built from | Deployed by | To |
|---|---|---|---|
| `1.x` pre-release (`1.0.0-alpha.1`) | `go.Dockerfile` | `deploy-go.yml` | staging |
| `1.x` | `go.Dockerfile` | `deploy-go.yml` | staging; production too once the repository variable `GO_PRODUCTION` is `true` |

Python deployment is retired. `deploy-go.yml` refuses `0.x` tags. `GO_PRODUCTION=true` enables production deployment.

Every merge publishes Go snapshot images `go-master` and `go-sha-<sha>`. Release images carry these tags without a duplicate snapshot build.

## The stack

`deploy-go.yml` uploads a bundle (`deploy/go/bundle.sh`) to `GO_DEPLOY_PATH` (default `/opt/messenger-go`). The bundle contains:

| File | |
|---|---|
| `compose.yaml` | `api` and `worker` from the release image, the migrations as one-shot steps, a `pg_dump` step, and, with `COMPOSE_PROFILES=stores`, single-node stores |
| `deploy.sh` | the deployment (below) |
| `env.example` | every setting the stack needs, documented |
| `migrations/`, `migrate.sh`, `object-storage/` | what the one-shot steps run |
| `geoip/` | `update-dbip.sh`, which the `geoip-init` step runs to download the DB-IP Lite city database ([GeoIP](geoip.md)) |
| `livekit/livekit.yaml` | the media server of audio calls (`livekit` service, profile `calls`); see [Audio calls](#audio-calls) |

An upload replaces the bundle's files and `migrations/` as a whole: after a rollback, no later release's migrations are left on disk. It never touches what only the server has: `.env`, `.env.notifications`, `backups/` and `reports/`.

**Stores.** Staging can run single-node stores on its host (`COMPOSE_PROFILES=stores`). Their ports are not published, and they hold data that is staging's alone. Production leaves the profile out and points `.env` at its own clusters ([deployment.md](deployment.md)).

**Scaling.** The stack runs one `api` and one `worker` per host, behind the host's reverse proxy (`API_PORT`). More API instances means more hosts with the same stack and `.env`, behind the load balancer. Snowflake node ids are leased from Redis, so they need no setting.

## What a deployment does

`deploy.sh <app_dir> <image>`:

1. **Checks:** stops at once, changing nothing, if `.env` is missing, the disk is too full, or `CALLS_ENABLED=true` comes with incomplete call settings. It prints `calls: enabled` or `calls: disabled`. Then it pulls the image (and LiveKit's with calls enabled); a failed pull also leaves the deployment as it was.
2. **Stores:** with the bundled stores, it starts them and creates the ScyllaDB keyspace and the bucket.
3. **Backup:** writes a `pg_dump` (custom format) of `DATABASE_URL` to `backups/` and keeps the last 10. If the dump fails, nothing is migrated or deployed.
4. **Migrations:** runs the PostgreSQL and ScyllaDB migrations. If one fails, the running version stays. A database that is already newer than the release (a rollback) is left as it is. Then `geoip-init` downloads the DB-IP Lite city database into the `geoip-data` volume when it is missing. A failed download only means sessions and security events have no location; the deployment continues.
5. **Media server:** with `CALLS_ENABLED=true`, starts `livekit` and waits until it answers on `CALLS_HTTP_PORT`; if it does not, the deployment stops and the running version stays. Compose restarts it only when its image, settings or `livekit.yaml` changed. With calls disabled, a `livekit` container that is still there is removed.
6. **Start:** starts `api` and `worker` on the new image, then waits for `/readyz` (every store) and the worker's `/healthz`.
7. **Rollback:** if they do not get ready, starts the previous image again and fails. Migrations are not rolled back, so they must also work with the previous version: add first, remove a release later.

## One-time setup

### A host

1. Install Docker with the Compose plugin. Create a deploy user in the `docker` group and `/opt/messenger-go` owned by it.
2. Create `/opt/messenger-go/.env` (mode 0600) from `deploy/go/env.example`:
   - **Staging:** in a checkout of the repository, run `go run deploy/dev/genenv.go deploy/go/env.example /opt/messenger-go/.env` (or copy the result there). It generates the secrets and the bundled stores' passwords. Then set `CORS_ORIGINS` and `TRUSTED_PROXIES`.
   - **Production:**
     - Take the secrets from the secret manager, never from a file in the repository.
     - Leave out `COMPOSE_PROFILES`.
     - Point `DATABASE_URL`, `REDIS_URL`, `SCYLLA_*`, `ELASTICSEARCH_URL` and `S3_*` at the clusters, with TLS.
     - Set `SCYLLA_CONSISTENCY=local_quorum` and `SEARCH_REPLICAS=1`.
     - Create the keyspace with replication factor 3 and the bucket beforehand.
     - With `sslmode=verify-full`, set `BACKUP_DATABASE_URL` to the same URL plus `sslrootcert=system`: `pg_dump` (libpq) does not look in the system's CAs by itself, and without it every deployment stops at the backup.
3. Point the reverse proxy at `${API_BIND}:${API_PORT}` for `/api/v2` and `/ws`, with WebSocket upgrades. It must append the client address to `X-Forwarded-For`. Set `TRUSTED_PROXIES` to the address the API sees for the proxy: with this stack that is Docker's bridge gateway (for example `172.16.0.0/12`), not `127.0.0.1`. Unset, only loopback is trusted and the API logs a warning at startup. Do not route `/metrics` or the worker's port publicly. Readiness is checked at `API_BIND`, or at `127.0.0.1` when it is `0.0.0.0`.
4. The bundled stores and the migration steps take their passwords as command-line arguments (`redis-server --requirepass`, `pg_dump` and `migrate` URLs), which other users of the host can see in `ps`. Keep the host to the deploy user and administrators.

### GitHub

- **Staging:**
  - Create the environment `staging`, with the secrets `STAGING_SSH_HOST`, `STAGING_SSH_USER`, `STAGING_SSH_KEY` (a key for this host only) and `STAGING_SSH_KNOWN_HOSTS`.
  - The release workflow dispatches it from `master`.
  - It deliberately uses its own secret names: an environment without them fails instead of reaching the production host.
- **Production:**
  - Uses the existing environment `production` and its `SSH_*` secrets, and deploys only from `edge`.
  - `edge` must contain `deploy-go.yml`. That holds after the first release that includes this change, or once the release workflow moves `edge` for a Go release.
- **Optional:** set the variable `GO_DEPLOY_PATH` for a different directory.
- **At the switch:** set the repository variable `GO_PRODUCTION` to `true` ([switchover.md](switchover.md)).

## Rollback and restore

- **To an earlier release:** Actions → Deploy (Go) → Run workflow, with the earlier tag and the environment. For production, run it from `edge`. The newer schema stays, which the earlier release works with.
- **A migration failed halfway:** golang-migrate marks the database dirty, and every later deployment stops at it.
  1. Look at what the migration did and finish or undo it by hand.
  2. Mark the version, either the one it reached or the one before it:

     ```sh
     docker compose run --rm --entrypoint sh postgres-migrate -c 'migrate -path /migrations -database "$MIGRATE_DATABASE" force N'
     ```

     Use `scylla-migrate` instead for ScyllaDB.
  3. Deploy again.
- **A ScyllaDB migration:** only PostgreSQL is backed up by `deploy.sh`. Before a release with a ScyllaDB migration, take a snapshot (`nodetool snapshot`, or Scylla Manager on a cluster; see [deployment.md](deployment.md)).
- **If a migration broke the data:** stop `api` and `worker`, then restore the backup taken before it. Restore into the database the backup came from. Then deploy the release that matches it:

  ```sh
  cd /opt/messenger-go
  docker compose stop api worker
  docker compose run --rm -T --entrypoint sh postgres-backup \
    -c 'pg_restore --clean --if-exists --no-owner -d "$DATABASE_URL"' < backups/messenger-YYYYmmdd-HHMMSS.dump
  ```

## Audio calls

Calls are off until `CALLS_ENABLED=true` in `.env`. Off, nothing of them runs and messaging is unaffected. The design, ports, AWS checklist and budgets are in [calls.md](calls.md#deployment).

### Enabling calls on a host

1. **AWS and DNS** (project owner, by hand): the Security Group rules, the `calls.<domain>` and `turn.<domain>` records and the certificates from the [checklist](calls.md#aws-and-dns-checklist).
2. **Certificates.** `calls.<domain>` is served by nginx like the other sites (`certbot certonly --nginx -d calls.<domain>`). LiveKit terminates TURN/TLS itself, so `turn.<domain>` needs a certificate the deploy user can read. Request it with a deploy hook that copies it and restarts the media server:

   ```sh
   sudo certbot certonly --nginx -d turn.<domain> \
     --deploy-hook /etc/letsencrypt/renewal-hooks/deploy/messenger-turn.sh
   ```

   `/etc/letsencrypt/renewal-hooks/deploy/messenger-turn.sh` (mode 0755; replace the domain and the user):

   ```sh
   #!/bin/sh
   set -eu
   case " $RENEWED_DOMAINS " in *" turn.<domain> "*) ;; *) exit 0 ;; esac
   install -d -o deploy -g deploy -m 700 /opt/messenger-go/tls/turn
   install -o deploy -g deploy -m 600 "$RENEWED_LINEAGE/fullchain.pem" "$RENEWED_LINEAGE/privkey.pem" /opt/messenger-go/tls/turn/
   if [ -n "$(cd /opt/messenger-go && docker compose ps -q livekit)" ]; then
     cd /opt/messenger-go && docker compose restart livekit
   fi
   ```

   Run it once by hand after the first request (`sudo RENEWED_DOMAINS=turn.<domain> RENEWED_LINEAGE=/etc/letsencrypt/live/turn.<domain> sh /etc/letsencrypt/renewal-hooks/deploy/messenger-turn.sh`). LiveKit reads the certificate at start, so a renewal restarts it, which ends the calls running at that moment (about every 60 days).
3. **nginx:** add the `calls.<domain>` server block from `deploy/go/livekit/nginx-calls.conf.example`. For TURN on 443 instead of 5349, also apply `deploy/go/livekit/nginx-turn-443-stream.conf.example`. `nginx -t && systemctl reload nginx`.
4. **`.env`:** set `CALLS_ENABLED=true`, `CALLS_PUBLIC_URL=wss://calls.<domain>`, `CALLS_NODE_IP` (the Elastic IP), `CALLS_TURN_DOMAIN=turn.<domain>`, the certificate paths, and a key and secret (`CALLS_API_KEY`: any name, for example `calls-2026-11`; `CALLS_API_SECRET`: `openssl rand -hex 32`). Keep the secret in the secret manager and copy it into `.env`; nothing reads the secret manager directly. Staging and production never share a key or secret.
5. **Deploy** (the workflow, or the same tag again). The log shows `calls: enabled` and `calls: media server healthy`. Then `curl -fsS https://calls.<domain>/` prints `OK`.
6. Follow the [load test](calls.md#load-test) and the [device checklist](calls.md#physical-device-checklist) before telling users.

A preflight failure lists the settings that are missing or wrong (never their values), and nothing is changed.

### Disabling calls (rollback)

1. Set `CALLS_ENABLED=false` in `.env`.
2. Either deploy again (it removes `livekit`), or at once on the host:

   ```sh
   cd /opt/messenger-go
   docker compose up -d --no-deps api worker
   docker compose rm -sf livekit
   ```

The API stops offering calls; removing `livekit` ends the calls that are running. Messages, history and the call tables stay; the schema is additive, so earlier releases keep working. Enabling again is the steps above without the one-time parts.

### Rotating the media credentials

Rotate `CALLS_API_SECRET` (and give the key a new name) when it may have leaked, when someone who knew it leaves, and at least yearly.

1. Pick a quiet hour: the media server restarts and running calls end; clients ask for a new grant when the users join again.
2. Put a new `CALLS_API_KEY` and `CALLS_API_SECRET` into `.env` (and keep them in the secret manager).
3. Deploy again. `livekit` (new keys), `api` and `worker` restart with the new pair; grants and webhooks signed with the old one stop working at once.

LiveKit could accept two keys at once for a rotation without interruption, but the API signs with one; with calls of a few minutes, the short interruption is the simpler choice.

### Staging on the same host

The plan allows staging calls on the production host. That needs its own compose project name (`COMPOSE_PROJECT_NAME` in its `.env`), its own `API_PORT`, `WORKER_PORT` every `CALLS_*_PORT` (including the UDP ranges, with their own Security Group rule) and `CALLS_TURN_TLS_PUBLISH`, its own `calls`/`turn` names and certificates, and a small `CALLS_MAX_ACTIVE_PARTICIPANTS`. Its media competes with production's for the same two vCPUs: keep `CALLS_CPUS` low and test in a quiet window.

## Python retirement

The one-time migration is complete and its executable tooling has been removed. See the [completed cutover](switchover.md), [data migration](migration-v1.md), and [retirement/archive record](python-retirement.md). Never set the refresh cookie path to `/api/auth`; use `/api/v2/auth/refresh`.

## Testing the stack

The `go-stack` job (`go.yml`) runs the scripts on every pull request, the same way a server does:

- a bundle, a `.env` from `genenv`, and a deployment with the bundled stores;
- a repeated deployment;
- a broken image, which must be rolled back.

To run it locally, pass `PRUNE_IMAGES=false`, so that `deploy.sh` keeps the unused images of your other projects:

```sh
bash deploy/go/bundle.sh /tmp/go-stack
go run deploy/dev/genenv.go deploy/go/env.example /tmp/go-stack/.env
docker build -f go.Dockerfile -t messenger-go:local .
PRUNE_IMAGES=false bash /tmp/go-stack/deploy.sh /tmp/go-stack messenger-go:local
```
