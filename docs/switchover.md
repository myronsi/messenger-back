# Completed Python-to-Go cutover

The production cutover was completed on 2026-11-01. Production and staging now use the Go API and worker. Python infrastructure and the one-time importer were retired on 2026-11-03. This page records the completed transition; current deployment instructions are in [deploy-go.md](deploy-go.md).

## Outcome

- HTTP traffic uses `/api/v2`; realtime uses the Go WebSocket endpoint with single-use tickets.
- Account and chat metadata live in PostgreSQL, message history in ScyllaDB, search in Elasticsearch and media in object storage. Redis supports sessions, rate limits and realtime/background coordination.
- Existing accounts and migrated password hashes remain supported by Go. Account recovery now uses recovery keys.
- The frontend connects to Go in both environments. Every frontend push to `main` deploys staging automatically; production frontend deployments follow releases.
- Go backend releases deploy to staging; stable releases deploy to production when `GO_PRODUCTION=true`. Backend and frontend app version numbers are independent; the API contract determines compatibility.

The original cutover procedure and importer source remain available in Git history. Do not recreate `v1.env`, enable Python `0.x` deployment, or rerun the old importer against current data.

## Rollback boundary

The migration did not provide a reverse copy from Go stores to the old Python schema. Returning to the archived Python database would discard messages, uploads, accounts and other changes written after cutover. The Python service is not a current rollback target.

For an application regression, deploy a previous compatible Go image using the current [deployment rollback](deploy-go.md#rollback-and-restore) process. For data recovery, restore the correct Go backups under the incident procedure. Do not roll database migrations backwards merely to match an older image; releases must preserve schema compatibility.

## Authentication correction after cutover

On 2026-11-03 the active refresh-cookie override was corrected from `/api/auth` to `/api/v2/auth/refresh`. The old prefix is not a parent of the Go refresh endpoint, so browsers omitted the cookie and users were logged out after the 15-minute access token expired. Cookie-based refresh was verified using a disposable account after correction. Browsers retaining only the old cookie must sign in again.

## Evidence and related records

- Migration: [backend #131](https://github.com/myronsi/messenger-back/pull/131), [frontend #122](https://github.com/myronsi/messenger-front/pull/122).
- Retirement: [backend #150](https://github.com/myronsi/messenger-back/pull/150), [frontend #139](https://github.com/myronsi/messenger-front/pull/139).
- Frontend automatic staging deployment: [#141](https://github.com/myronsi/messenger-front/pull/141), verified by a successful push-triggered run.
- [Migrated data and compatibility](migration-v1.md), [Python retirement and archives](python-retirement.md), [authentication](auth.md), and [frontend migration notes](https://github.com/myronsi/messenger-front/blob/main/docs/go-migration.md).
