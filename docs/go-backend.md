# Go backend

The Go backend replaces the Python one (`server/`). Its skeleton is in place; features are added issue by issue.

## Layout

| Path | Purpose |
|---|---|
| `cmd/api` | REST API and realtime gateway |
| `cmd/worker` | event consumers ([events.md](events.md)) and maintenance jobs; serves `/healthz` and `/metrics` on `WORKER_ADDR` |
| `cmd/migrate-v1` | one-time data migration from the Python backend (MSGC-77), a stub for now |
| `internal/app` | process plumbing shared by the commands: config, logger, tracing, signals, HTTP server lifecycle |
| `internal/config` | environment configuration, validated on startup |
| `internal/observability` | JSON `slog` logger with redaction, Prometheus metrics, optional OpenTelemetry tracing |
| `internal/httpapi` | router, middleware, health checks, generated server (`api.gen.go`) |
| `internal/realtime` | the WebSocket gateway ([realtime.md](realtime.md)): connections, delivery between instances, presence |
| `internal/messages` | message use cases with their authorization checks |
| `internal/users` | users as other users may see them (privacy) |
| `internal/version` | the contract version and SemVer comparison |
| `internal/events`, `internal/jobs` | domain events on Redis streams and the worker's handlers |
| `internal/media` | uploads, downloads, avatars: storage (disk, S3), content checks, image processing ([media.md](media.md)) |
| `internal/testenv` | fresh stores for integration tests |
| `internal/store/{postgres,redis,scylla,elastic}` | store clients with `Ping` for `/readyz`; PostgreSQL also has the repositories ([schema](postgres-schema.md)); Redis has presence, pub/sub, unread counters, the membership cache and rate limits ([redis.md](redis.md)); ScyllaDB has the message store ([messages.md](messages.md)) |
| `internal/ids` | Snowflake IDs for messages |
| `internal/{auth,chats,groups,media,search}` | domain packages; `auth` is done ([auth.md](auth.md)), the others follow |
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
- `/healthz` is process liveness. `/readyz` checks PostgreSQL, Redis, ScyllaDB and Elasticsearch and answers only `ok` / `unavailable` per store. Neither, nor `/metrics`, should be exposed through the public proxy.
- Logs are JSON and carry a `request_id`. Message content, tokens and secrets are never logged: sensitive attribute names are redacted and request paths with query strings are not logged, only the route pattern.
- Shutdown on SIGINT/SIGTERM: `/readyz` turns 503, optionally waits `HTTP_DRAIN_DELAY`, closes WebSockets with code 1012 "reconnect", and finishes in-flight requests within `HTTP_SHUTDOWN_TIMEOUT`.
- CORS origins come from `CORS_ORIGINS`; `*` is rejected. Request bodies are limited by `HTTP_MAX_BODY_BYTES`.
- `go.Dockerfile` is separate from the Python `Dockerfile` until cutover. It is not called `Dockerfile.go` because Go would treat that name as a source file.

- Authentication, sessions and 2FA are described in [auth.md](auth.md). Every route except a short allowlist needs a bearer token.

## CI

`.github/workflows/go.yml`: golangci-lint, `go vet`, `go mod tidy` and `go generate` drift checks, `go test -race ./...` (with PostgreSQL and Redis services for the database tests), `migrations` (up, down and up again with the real tool), `govulncheck`, an image build, and `dev-stack`, which starts `compose.dev.yaml` and checks that `/readyz` reports every store ready.