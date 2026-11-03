# Python backend retirement

Retirement date: 2026-11-03. The supported Messenger backend is Go. Production and staging serve `/api/v2` through the Go API and worker; no Python Messenger service is running. This record distinguishes removed infrastructure from deliberately retained recovery material.

## Removed from active source and deployment

- Python server source, Python tests, dependency files and startup script.
- Python production Compose stack, deployment script and `deploy.yml` workflow.
- Python CI builds and snapshot/release image publication. Both Dockerfiles now build Go API/worker binaries; release automation uses `go.Dockerfile`.
- Completed one-time `cmd/migrate-v1` importer, migration Compose overlay, scripts and `v1.env` template.
- Frontend Python-image fallback. Local helpers reject `v0.*`, `master` and `sha-*`; use Go releases, `go-master` or `go-sha-*`.
- The frontend's orphaned nested `messenger-back` gitlink. Source repositories remain separate.

The old Python deployment workflow was disabled before its removal. Historical Git tags, release assets and source history remain records; they are not supported deployment targets.

## Server cleanup performed

The stopped `messenger-backend-1` and legacy `messenger-postgres-1` containers, `messenger_messenger_postgres` and `messenger_messenger_static` volumes, `messenger_default` network and the server's Python `v0.5.4` image were removed. Volume contents were archived and checksum-verified first. `/opt/messenger` was moved out of the active deployment tree. Unused `V1_*` settings were removed from production and staging environment files, and staging's leftover `v1.env` was archived.

Active settings remain `/opt/messenger-go/.env` for production and `/opt/messenger-staging/.env` for staging. Do not substitute archived Python settings. Go readiness checks passed after cleanup. Cookie-based refresh was also verified after correcting `REFRESH_COOKIE_PATH=/api/v2/auth/refresh`.

## Retained archive

The restricted server directory is `/opt/messenger-retired/python-20261103T211835Z` (root-owned, mode 0700). It contains:

- Compressed copies of the retired PostgreSQL and media volumes, with `SHA256SUMS`.
- The former `/opt/messenger` tree as `stack/`, including its historical backups and configuration.
- Retired migration scripts and example configurations from production/staging.
- Staging's obsolete migration credentials file, restricted to mode 0600.

This material can contain credentials and user data. Keep it out of Git, release artifacts and ordinary web roots. A retention/incident decision is required before deleting it; routine image or release pruning must not remove it. Original Go database backups and migration reports are also retained. Nothing in this retirement step validates a full disaster-recovery restore or provides reverse synchronization to Python.

## Remaining compatibility and image boundary

At the 2026-11-03 audit, production and staging still ran the already-deployed Go image `v1.5.0-alpha.1`. It contained a dormant `/app/migrate-v1` binary, and existing API/worker containers still held unused `V1_*` values from before the environment-file cleanup. These were not a running Python backend. New images built after retirement omit the importer; recreating containers loads the cleaned environment. Source cleanup alone does not change an already-running image.

Migrated password/backup-code verification, the frontend v2 adapter, historical schema migrations and archived data remain intentionally supported. Do not delete them merely because they originated during the Python era. System Python installed by the operating system is outside the Messenger backend retirement scope.

## Verification for later releases

1. Check the actual running Go image and `/api/v2/meta`; check `/readyz` locally and the worker `/healthz`.
2. Confirm new containers no longer load `V1_*` and the released image contains only the supported application binaries.
3. Confirm there are no legacy Messenger containers, volumes, network or active `/opt/messenger` deployment.
4. Verify a new login cookie reaches `/api/v2/auth/refresh` and refresh succeeds. Never print token, cookie or environment-secret values during checks.
5. Keep production and staging API origins separate when building frontend assets. Frontend `main` automatically deploys staging; production frontend releases remain independent.

See [completed cutover](switchover.md), [data migration](migration-v1.md), [Go deployment](deploy-go.md), and [authentication](auth.md). Retirement changes: [backend #150](https://github.com/myronsi/messenger-back/pull/150), [frontend #139](https://github.com/myronsi/messenger-front/pull/139), and [frontend gitlink removal #142](https://github.com/myronsi/messenger-front/pull/142).
