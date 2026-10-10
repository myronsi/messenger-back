# Development environment

One command starts the Go backend with every data store it needs: PostgreSQL, Redis, ScyllaDB,
Elasticsearch and an S3-compatible object storage. It is for development only. The production
layout is described in [deployment.md](deployment.md).

`compose.dev.yaml` is separate from the root `compose.yaml`, which is the production deployment of the
Python backend and is not touched by this stack.

## Quick start

Requirements: Docker with the Compose plugin (v2.30 or newer) and Go (only for `make env`, which runs a
small generator; you can also fill `.env` by hand).

```sh
make env   # once: creates .env with generated secrets (never overwrites an existing .env)
make up    # builds the image, waits for all stores and init steps, then starts the API and worker
```

`make up` is `docker compose -f compose.dev.yaml up --build --detach --wait`. The first run pulls several
GB of images and takes a few minutes (ScyllaDB needs up to a minute to start); later runs take
under a minute.

```sh
curl http://127.0.0.1:8080/readyz   # {"checks":{"elasticsearch":"ok","postgres":"ok","redis":"ok","scylla":"ok"},"status":"ready"}
make dev-logs                       # follow all logs
make down                           # stop, keep the data
docker compose -f compose.dev.yaml down --volumes   # stop and wipe all data
```

`make env` sets `COMPOSE_FILE=compose.dev.yaml` in the `.env` it generates, so a plain `docker compose up` uses this stack.
Add that line yourself if your `.env` came from elsewhere.

## Services

| Service | Image | Host port (127.0.0.1) | Notes |
| --- | --- | --- | --- |
| `api` | built from `go.Dockerfile` | 8080 | REST API and realtime gateway |
| `worker` | same image, `/app/worker` | 8081 | search indexing, background jobs (`/healthz`, `/metrics`) |
| `postgres` | `postgres:18-alpine` | 5432 | volume, `pg_isready` healthcheck |
| `redis` | `redis:8-alpine` | 6379 | append-only file on, password required |
| `scylla` | `scylladb/scylla:2026.1` | 9042 | `--smp 1 --memory 1G --overprovisioned 1 --developer-mode 1` |
| `elasticsearch` | `elasticsearch:9.5.3` | 9200 | single node, 1 GB heap, security on, plain HTTP |
| `object-storage` | `rustfs/rustfs:1.0.1` | 9000 (S3), 9001 (console) | S3-compatible |
| `kibana` | `kibana:9.5.3` | 5601 | optional: `--profile kibana`, sign in as `elastic` |

Every port is bound to `127.0.0.1` only. Change one with `API_PORT`, `POSTGRES_PORT`, `S3_PORT` and so on
in `.env` (see the end of `.env.example`). The host-side URLs for `make run` and `make migrate` follow
`POSTGRES_PORT`, `REDIS_PORT`, `SCYLLA_PORT` and `ELASTICSEARCH_PORT`, but only when `make env` generates them: set the port
in `.env.example`-style before generating, or edit the matching URL in `.env` by hand.

`api` and `worker` start only after every store reports healthy and every init step has finished. They have no Compose
healthcheck (the images are distroless), so `--wait` returns once they are running: the listeners may need a moment, so retry
`curl http://127.0.0.1:8080/readyz` if the first call is refused.

## Init steps

One-shot containers that are safe to run again, so `make up` can be repeated at any time:

| Step | What it does |
| --- | --- |
| `postgres-migrate` | applies `migrations/postgres/*.up.sql` with golang-migrate |
| `scylla-keyspace` | creates the keyspace (`SCYLLA_KEYSPACE`, default `messenger`) |
| `scylla-migrate` | applies `migrations/scylla/*.up.cql` with golang-migrate |
| `elasticsearch-init` | sets the `kibana_system` password (the worker sets up the message index itself, [search.md](search.md)) |
| `object-storage-init` | creates every bucket in `S3_BUCKETS` (default `messenger-media`) |

An empty migrations directory is fine; the step just says there is nothing to apply. Add a migration as
`migrations/postgres/000001_name.up.sql` (and `.down.sql`), or `migrations/scylla/000001_name.up.cql`,
then run `make up` again. `make migrate` applies the same files to databases you run yourself.

## Credentials

Nothing secret is in `compose.yaml` or the repository. Everything comes from `.env`:

- `make env` fills `JWT_SECRET`, `ENCRYPTION_KEY`, `RECOVERY_PEPPER` and every store password with random
  values, and builds the host-side `DATABASE_URL`, `REDIS_URL` and `ELASTICSEARCH_URL` from them.
- If you write `.env` by hand, copy `.env.example`, fill the empty values and expand the `${NAME}`
  references yourself. Use URL-safe passwords (`openssl rand -hex 16`); they are placed inside URLs.
- A missing value stops `docker compose` with a message that names it.

## Running the API on the host

The stores are published on `127.0.0.1`, so you can run the stores in Docker and the API from source:

```sh
docker compose -f compose.dev.yaml up --detach --wait postgres redis scylla elasticsearch \
  object-storage postgres-migrate scylla-migrate elasticsearch-init object-storage-init
make run
```

If the full stack is already running, stop the containerised API first (`docker compose -f compose.dev.yaml stop api`): both use port 8080.

## Memory

Plan for **4 to 6 GB of RAM** for the whole stack:

| Component | Roughly |
| --- | --- |
| Elasticsearch | 1.5 GB (1 GB heap plus off-heap) |
| ScyllaDB | 1.5 to 2 GB (`--memory 1G` plus reserved memory) |
| PostgreSQL, Redis, object storage, API, worker | 0.5 GB together |
| Kibana (optional) | 1 GB |

Docker Desktop on Windows and macOS: give the VM at least 6 GB (Settings, Resources; on WSL 2 set
`memory=6GB` in `.wslconfig`). Without Kibana 4 GB is tight but works.

## Troubleshooting

- **`elasticsearch` or `scylla` keeps restarting**: usually too little memory for the Docker VM. Check
  `docker compose -f compose.dev.yaml logs scylla`.
- **ScyllaDB fails with an AIO error** (`Could not setup Async I/O`): on Linux raise the limit,
  `sudo sysctl -w fs.aio-max-nr=1048576`.
- **Port already in use**: set another host port in `.env`, for example `POSTGRES_PORT=5433`.
- **Start from scratch**: `docker compose -f compose.dev.yaml down --volumes`, then `make up`.
- **Changing secrets**: delete `.env` only after wiping the volumes as above. PostgreSQL and Elasticsearch keep the passwords they were first initialised with, so new values in `.env` would not match.
- **`.env` already exists** (for example from the Python backend): `make env` leaves it alone. Append the
  values from the end of `.env.example` and fill them in.
