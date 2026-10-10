# Deploying the Go backend

How Go releases (`1.x`) get to staging and, after the switch, to production (#124). The production requirements of the stores are in [deployment.md](deployment.md), the release process in [releasing.md](releasing.md), and the switch itself in [switchover.md](switchover.md).

## What goes where

The tag's major version decides which backend a release is:

| Release | Image built from | Deployed by | To |
|---|---|---|---|
| `0.x` | `Dockerfile` (Python) | `deploy.yml` | production, as before |
| `1.x` pre-release (`1.0.0-alpha.1`) | `go.Dockerfile` | `deploy-go.yml` | staging |
| `1.x` | `go.Dockerfile` | `deploy-go.yml` | staging; production too once the repository variable `GO_PRODUCTION` is `true` |

Until the switch, `GO_PRODUCTION` stays unset, so production keeps the Python backend whatever is released. `deploy-go.yml` also refuses production by hand while it is unset, and it refuses `0.x` tags.

Every merge to `master` also publishes snapshot images: `master` and `sha-<sha>` (Python) and `go-master` and `go-sha-<sha>` (Go).

## The stack

`deploy-go.yml` uploads a bundle (`deploy/go/bundle.sh`) to `GO_DEPLOY_PATH` (default `/opt/messenger-go`), next to the Python backend's `/opt/messenger` and never touching it. The bundle contains:

| File | |
|---|---|
| `compose.yaml` | `api` and `worker` from the release image, the migrations as one-shot steps, a `pg_dump` step, and, with `COMPOSE_PROFILES=stores`, single-node stores |
| `deploy.sh` | the deployment (below) |
| `migrate-v1.sh`, `compose.migrate-v1.yaml` | the data copy from the Python backend (the switch) |
| `env.example` | every setting the stack needs, documented |
| `migrations/`, `migrate.sh`, `object-storage/` | what the one-shot steps run |

The `.env` on the server is the only state the bundle does not bring. Uploads never overwrite it.

**Stores.** Staging can run single-node stores on its host (`COMPOSE_PROFILES=stores`). Their ports are not published, and they hold data that is staging's alone. Production leaves the profile out and points `.env` at its own clusters ([deployment.md](deployment.md)).

**Scaling.** The stack runs one `api` and one `worker` per host, behind the host's reverse proxy (`API_PORT`). More API instances means more hosts with the same stack and `.env`, behind the load balancer. Snowflake node ids are leased from Redis, so they need no setting.

## What a deployment does

`deploy.sh <app_dir> <image>`:

1. **Checks:** stops at once, changing nothing, if `.env` is missing or the disk is too full. Then it pulls the image; a failed pull also leaves the deployment as it was.
2. **Stores:** with the bundled stores, it starts them and creates the ScyllaDB keyspace and the bucket.
3. **Backup:** writes a `pg_dump` (custom format) of `DATABASE_URL` to `backups/` and keeps the last 10. If the dump fails, nothing is migrated or deployed.
4. **Migrations:** runs the PostgreSQL and ScyllaDB migrations. If one fails, the running version stays.
5. **Start:** starts `api` and `worker` on the new image, then waits for `/readyz` (every store) and the worker's `/healthz`.
6. **Rollback:** if they do not get ready, starts the previous image again and fails. Migrations are not rolled back, so they must also work with the previous version: add first, remove a release later.

## One-time setup

### A host

1. Install Docker with the Compose plugin. Create a deploy user in the `docker` group and `/opt/messenger-go` owned by it.
2. Create `/opt/messenger-go/.env` (mode 0600) from `deploy/go/env.example`:
   - **Staging:** run `go run deploy/dev/genenv.go deploy/go/env.example .env`. It generates the secrets and the bundled stores' passwords. Then set `CORS_ORIGINS` and, if needed, `TRUSTED_PROXIES`.
   - **Production:** take the secrets from the secret manager, never from a file in the repository. Leave out `COMPOSE_PROFILES`. Point `DATABASE_URL`, `REDIS_URL`, `SCYLLA_*`, `ELASTICSEARCH_URL` and `S3_*` at the clusters, with TLS. Set `SCYLLA_CONSISTENCY=local_quorum` and `SEARCH_REPLICAS=1`. Create the keyspace with replication factor 3 and the bucket beforehand.
3. Point the reverse proxy at `127.0.0.1:${API_PORT}` for `/api/v2` and `/ws`, with WebSocket upgrades. Do not route `/metrics` or the worker's port publicly.

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

- **To an earlier release:** Actions → Deploy (Go) → Run workflow, with the earlier tag and the environment. For production, run it from `edge`.
- **If a migration broke the data:** stop `api` and `worker`, then restore the backup taken before it. Restore into the database the backup came from. Then deploy the release that matches it:

  ```sh
  cd /opt/messenger-go
  docker compose stop api worker
  docker compose run --rm -T --entrypoint sh postgres-backup \
    -c 'pg_restore --clean --if-exists --no-owner -d "$DATABASE_URL"' < backups/messenger-YYYYmmdd-HHMMSS.dump
  ```

## The data copy (switch)

`migrate-v1.sh /opt/messenger-go [flags]` runs `cmd/migrate-v1` ([migration-v1.md](migration-v1.md)) with the deployed image:

- **Access to v1:** it joins the Python stack's Docker network (`V1_NETWORK`, default `messenger_default`) and mounts its `static/` volume read-only (`V1_STATIC_VOLUME`, default `messenger_messenger_static`).
- **Settings in `.env`:**
  - `V1_DATABASE_URL`: a read-only user on the Python database; its host is the container name, for example `messenger-postgres-1`;
  - `V1_SECRET_KEY`: the Python backend's `SECRET_KEY`.
- **Reports:** written to `reports/`, owned by the deploy user.

```sh
./migrate-v1.sh /opt/messenger-go -dry-run -report dry.json
./migrate-v1.sh /opt/messenger-go -report run.json
```

Remove the `V1_*` settings after the switch.

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
