# Messenger backend

The backend is Go: an HTTP/WebSocket API and a background worker, with PostgreSQL, Redis, ScyllaDB, Elasticsearch and S3-compatible object storage.

## Development

Install Go matching `go.mod`, Docker Compose and Make, then run:

```sh
make env
make up
```

The API is served at `http://127.0.0.1:8080/api/v2`; readiness is `/readyz`.
Use `make test`, `make lint` and `make docker` for validation and builds.

## Deployment

Build `go.Dockerfile`; API and worker share that image. Production uses `deploy/go/compose.yaml` and a server-owned `.env` in `/opt/messenger-go`. See [deployment](docs/deploy-go.md), [development](docs/dev-environment.md), [authentication](docs/auth.md) and [releases](docs/releasing.md).

Refresh cookies must match `/api/v2/auth/refresh`. Never reuse the retired `/api/auth` cookie path.

## Python retirement

The Python service, build, deployment, tests and completed one-time importer have been removed. Historical source remains in Git history. Existing migrated password formats remain supported so users keep access to their accounts. Historical migration records and backups are retained for audit and recovery; they are not deployment instructions.
