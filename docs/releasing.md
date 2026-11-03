# Releases

Backend releases use release-please on `master`. Merge a Conventional Commit PR, review the generated release PR, then merge it to publish the release.

`release.yml` builds only `go.Dockerfile`. Release images receive the version, `v` tag, `go-master` and `go-sha-<short sha>` tags. Stable releases also receive major/minor and `latest` tags; pre-releases do not. Ordinary merges publish `go-master` and `go-sha-<short sha>` once. The API contract is published by `api-publish.yml`.

All releases deploy to staging through `deploy-go.yml`. Stable releases deploy to production when `GO_PRODUCTION=true`; the workflow advances `edge` because the production environment accepts that branch. Production settings live only on the server in `/opt/messenger-go/.env` and `.env.notifications`. Deployment backs up PostgreSQL, applies migrations and checks readiness; failure restores the previous image. See [deployment](deploy-go.md).

Python `0.x` releases, images, deployment workflows and compose assets are retired. Historical tags and release assets remain records and must not be deployed.

Validation uses `go.yml` (Go tests, lint, image, development and deployment stacks), `api-ci.yml` (API contract), `frontend-compat.yml` (frontend smoke tests against the Go stack), and `pr-title.yml`. The frontend compatibility suite checks its latest release and, nightly or for API changes, its main branch.
