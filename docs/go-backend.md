# Go backend

The Go backend is the sole supported backend. The Python implementation and its one-time importer are retired.

## Layout

| Path | Purpose |
|---|---|
| `cmd/api` | REST API and realtime gateway |
| `cmd/worker` | event consumers ([events.md](events.md)) and maintenance jobs; serves `/healthz` and `/metrics` on `WORKER_ADDR` |
| `internal/app` | process plumbing shared by the commands: config, logger, tracing, signals, HTTP server lifecycle |
| `internal/config` | environment configuration, validated on startup |
| `internal/observability` | JSON `slog` logger with redaction, Prometheus metrics, optional OpenTelemetry tracing |
| `internal/httpapi` | router, middleware, health checks, generated server (`api.gen.go`) |
| `internal/realtime` | the WebSocket gateway ([realtime.md](realtime.md)): connections, delivery between instances, presence |
| `internal/messages` | message use cases with their authorization checks |
| `internal/users` | users as other users may see them (privacy); the account and user endpoints are in [accounts.md](accounts.md) |
| `internal/version` | the contract version and SemVer comparison |
| `internal/events`, `internal/jobs` | domain events on Redis streams and the worker's handlers |
| `internal/media` | uploads, downloads, avatars: storage (disk, S3), content checks, image processing ([media.md](media.md)) |
| `internal/testenv` | fresh stores for integration tests |
| `internal/store/{postgres,redis,scylla,elastic}` | store clients with `Ping` for `/readyz`; PostgreSQL also has the repositories ([schema](postgres-schema.md)); Redis has presence, pub/sub, unread counters, the membership cache and rate limits ([redis.md](redis.md)); ScyllaDB has the message store ([messages.md](messages.md)) |
| `internal/ids` | Snowflake IDs for messages |
| `internal/chats` | the chat list, direct chats, groups, pins, read markers and approval requests ([chats.md](chats.md)) |
| `internal/auth` | authentication, sessions and 2FA ([auth.md](auth.md)) |
| `internal/calls` | audio calls: lifecycle, key epochs, LiveKit grants, room API and webhooks, call pushes, the sweeper ([calls.md](calls.md)) |
| `internal/search` | message search: the Elasticsearch index, its indexer and the searches ([search.md](search.md)) |
| `migrations/{postgres,scylla}` | schema migrations (`make migrate`, applied automatically by the dev stack) |
| `compose.dev.yaml`, `deploy/dev` | development stack and its init steps, see [dev-environment.md](dev-environment.md); production notes are in [deployment.md](deployment.md) |

## Everyday commands

```sh
make env               # creates .env with generated secrets (or: cp .env.example .env and fill it in)
make up                # the whole stack in Docker: API, worker and all stores (docs/dev-environment.md)
make run               # or only the API from source on :8080, against stores that are already running
make test              # go test -race ./...
make lint              # golangci-lint + go vet
make vuln              # govulncheck
make generate          # regenerate the server (api/openapi.yaml) and the sqlc queries
make migrate           # DATABASE_URL and SCYLLA_* must be set
make docker            # distroless, non-root image from go.Dockerfile
```

`make help` lists all targets. `-race` needs a C compiler (cgo); on Windows run it in WSL or a container.

## Behaviour worth knowing

- The process exits with a non-zero status and a message that names the variables (never the values) when a required setting is missing or weak: `JWT_SECRET` and `RECOVERY_PEPPER` need 32+ characters, `ENCRYPTION_KEY` is base64 of 32 bytes.
- `/healthz` is process liveness. `/readyz` checks PostgreSQL, Redis, ScyllaDB and Elasticsearch (object storage too, when it is S3), and in the API that it can issue message ids (`ids`: the Snowflake node lease, which on a fresh Redis waits about 40 s). It answers only `ok` / `unavailable` per check. Neither, nor `/metrics`, should be exposed through the public proxy.
- Logs are JSON and carry a `request_id`. Message content, tokens and secrets are never logged: sensitive attribute names are redacted and request paths with query strings are not logged, only the route pattern.
- Shutdown on SIGINT/SIGTERM: `/readyz` turns 503, optionally waits `HTTP_DRAIN_DELAY`, closes WebSockets with code 1012 "reconnect", and finishes in-flight requests within `HTTP_SHUTDOWN_TIMEOUT`.
- CORS origins come from `CORS_ORIGINS`; `*` is rejected. Request bodies are limited by `HTTP_MAX_BODY_BYTES`.
- `go.Dockerfile` and the default `Dockerfile` build the Go API and worker.

- Authentication, sessions and 2FA are described in [auth.md](auth.md). Every route except a short allowlist needs a bearer token.
- `POST /api/v2/calls/media-webhook` (LiveKit) is mounted outside the contract and authenticated by its signed body hash, not a bearer token ([calls.md](calls.md#implementation)).

## Audio call settings

Calls are off unless `CALLS_ENABLED=true`; then `CALLS_PUBLIC_URL`, `CALLS_API_URL`, `CALLS_API_KEY` and
`CALLS_API_SECRET` are required, or the process refuses to start. With calls off the other `CALLS_*` values are not
checked, and deploy-only variables (`CALLS_NODE_IP`, `CALLS_TURN_*`, ports, limits of the LiveKit container) are
ignored by the API. Locally, start LiveKit with `docker compose -f compose.dev.yaml --profile calls up -d`; production
setup is in [calls.md](calls.md#deployment).

| Variable | Default | Meaning |
|---|---|---|
| `CALLS_ENABLED` | `false` | master switch; off: `GET /calls/config` reports `enabled: false`, every other call operation `503 calls_unavailable`; calls still open are ended as `failed` with their system message |
| `CALLS_PUBLIC_URL` | — | `wss://` URL of LiveKit for browsers (`ws://` allowed outside production) |
| `CALLS_API_URL` | — | `http(s)://` URL of LiveKit's room API as the API reaches it |
| `CALLS_API_KEY` | — | LiveKit API key |
| `CALLS_API_SECRET` | — | LiveKit API secret, at least 32 characters (redacted like other secrets) |
| `CALLS_MAX_PARTICIPANTS` | `10` | joined sessions per call (2-65: a session seals its key for at most 64 others per request; the product limit is 10) |
| `CALLS_MAX_ACTIVE_PARTICIPANTS` | `50` | joined sessions across all calls; above it joins get `503 calls_unavailable` with `Retry-After` |
| `CALLS_RING_TIMEOUT` | `30s` | ring window (5s-5m) |
| `CALLS_EMPTY_GRACE` | `20s` | reconnect grace of an empty group call or a lost participant (1s-5m) |
| `CALLS_TOKEN_TTL` | `10m` | lifetime of a media grant (1m-24h) |

## CI

`.github/workflows/go.yml`: golangci-lint, `go vet`, `go mod tidy` and `go generate` drift checks, `go test -race ./...` (with PostgreSQL and Redis services for the database tests), `migrations` (up, down and up again with the real tool), `govulncheck`, an image build, and `dev-stack`, which starts `compose.dev.yaml` and checks that `/readyz` reports every store ready.
