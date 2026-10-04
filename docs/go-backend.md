# Go backend

The Go backend replaces the Python one (`server/`). Its skeleton is in place; features are added issue by issue.

## Layout

| Path | Purpose |
|---|---|
| `cmd/api` | REST API and realtime gateway |
| `cmd/worker` | event consumers (search indexing, background jobs); serves `/healthz` and `/metrics` on `WORKER_ADDR` |
| `cmd/migrate-v1` | one-time data migration from the Python backend (MSGC-77), a stub for now |
| `internal/app` | process plumbing shared by the commands: config, logger, tracing, signals, HTTP server lifecycle |
| `internal/config` | environment configuration, validated on startup |
| `internal/observability` | JSON `slog` logger with redaction, Prometheus metrics, optional OpenTelemetry tracing |
| `internal/httpapi` | router, middleware, health checks, generated server (`api.gen.go`) |
| `internal/realtime` | WebSocket registry and graceful "reconnect" close (code 1012) |
| `internal/store/{postgres,redis,scylla,elastic}` | lazy store clients with `Ping` for `/readyz` |
| `internal/{auth,users,chats,groups,messages,media,search}` | domain packages, empty for now |
| `migrations/{postgres,scylla}` | schema migrations (`make migrate`) |

## Everyday commands

```sh
cp .env.example .env   # fill in the secrets, see the comments in the file
make run               # API on :8080
make test              # go test -race ./...
make lint              # golangci-lint + go vet
make vuln              # govulncheck
make generate          # regenerate the server from api/openapi.yaml
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

## CI

`.github/workflows/go.yml`: golangci-lint, `go vet`, `go mod tidy` and `go generate` drift checks, `go test -race ./...`, `govulncheck` and an image build.