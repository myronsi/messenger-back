# Releasing

The backend always deploys first and stays compatible with the previous frontend release.

## Release order

Never deploy a frontend that needs a contract version the deployed backend does not implement.

1. Backend: merge the release PR. This creates the tag, the GitHub Release, the Docker images and, if the contract changed, the stable contract package `@myronsi/messenger-api` (`latest`). Deploy the new version.
2. Verify the deployed backend (health check, `GET /api/version` returns the new version and commit).
3. Frontend: update the `@myronsi/messenger-api` dependency (Dependabot opens the PR; its npm updates run weekly, or run `npm update @myronsi/messenger-api` for an urgent one), merge the frontend release PR (see `messenger-front/docs/releasing.md`) and deploy.

## Contract package and compatibility checks

- **Contract**: `api-publish.yml` publishes `@myronsi/messenger-api` to npmjs.com with `npm publish --provenance`. Merging a change to `api/` on `master` publishes `<info.version>-next.<run number>` under the `next` dist-tag. A backend release publishes `info.version` under `latest` (a pre-release such as `2.0.0-alpha.1` under `alpha`), but only if that version is not on npm yet, that is, only if the contract changed. It authenticates with npm Trusted Publishing (OIDC), so there is no token or secret. npm accepts one trusted publisher workflow per package, so the `contract` job of `release.yml` calls `api-publish.yml` on every push to `master` (a snapshot only if `api/` changed in that commit) and the trusted publisher is `release.yml`.
- **Stack file**: every Python release (`0.x`) has `compose.stack.yaml` attached (`deploy/compose.stack.yaml` with the image pinned to the release; images only, no build). Download it and run `docker compose -f compose.stack.yaml up -d` to get a matching backend with PostgreSQL. Override the image with `BACKEND_IMAGE` (for example `ghcr.io/myronsi/messenger-back:sha-abc1234`, or `:master` for the latest merge).
- **frontend-compat** (`frontend-compat.yml`): builds the backend of the pull request, starts the stack and runs the messenger-front Playwright smoke suite (`npm run test:smoke`) against it: with the latest frontend release on every PR, and additionally with the frontend `main` nightly and on PRs labelled `api-change`. `BACKEND_URL`, `VITE_BASE_URL` and `VITE_WS_URL` point to the stack. Until the frontend has a `test:smoke` script (messenger-front, cross-repo smoke tests) the job reports a notice and passes. Make it a required status check once the suite exists.
- **Frontend dependency update** after a release: the frontend's Dependabot picks up the new contract version; no manual dispatch is needed.

## Checklist

1. All PRs for the release are merged to `master` with Conventional Commit titles and CI is green.
2. Check that every issue in the milestone is closed or moved to the next milestone.
3. Release: create the tag and the GitHub Release (`vX.Y.Z`, notes from `CHANGELOG.md`). Merging the release-please PR does this and also builds the image with no manual steps.
4. Deploy: happens automatically after the release. Go releases (`1.x`) deploy with `deploy-go.yml` to staging (and to production once `GO_PRODUCTION` is `true`), see [deploy-go.md](deploy-go.md). For Python releases (`0.x`) the release workflow moves the `edge` branch to the release commit and dispatches `deploy.yml` on it (environment `production` only accepts deployments from `edge`). It uploads `compose.yaml`, backs up the database with `pg_dump` (the last 10 backups are kept in `backups/`), pulls `ghcr.io/myronsi/messenger-back:vX.Y.Z`, restarts the stack and checks `http://127.0.0.1:8000/`. If the check fails the previous image is restored automatically and the workflow fails.
5. Check the version shown in the app (Profile → About) and the logs after the deployment.

## Docker images

Images are published to `ghcr.io/myronsi/messenger-back` by `release.yml`:

| Event | Tags |
| --- | --- |
| Release `v0.5.0` (Python, `Dockerfile`) | `0.5.0`, `0.5`, `latest` and `v0.5.0` (used by the deploy workflow) |
| Pre-release `v1.0.0-alpha.1` (Go, `go.Dockerfile`) | `1.0.0-alpha.1` and `v1.0.0-alpha.1` (no `1.0`/`latest`) |
| Release `v1.0.0` (Go) | `1.0.0`, `1.0`, `latest` and `v1.0.0` |
| Every merge to `master` | `master` and `sha-<short sha>` (Python), `go-master` and `go-sha-<short sha>` (Go) |

The major version of the tag picks the Dockerfile: `0.x` is the Python backend, `1.x` and later the Go backend. The `compose.stack.yaml` asset is the Python stack and is attached to `0.x` releases only.

The commit is baked into the image (build argument `COMMIT` -> `APP_COMMIT`) and shown by `GET /version`.

## Starting a new release train

Add a `Release-As: X.Y.0` footer to a commit so that `MAJOR.MINOR` matches the frontend. Pull requests are squash-merged using the PR description as the commit message, so put the footer on its own line at the end of the PR description. While the version is `0.x` `bump-minor-pre-major` and `bump-patch-for-minor-pre-major` turn breaking changes into minor and features into patch releases.

## Raising the minimum client version

`MIN_CLIENT_API_VERSION` (environment, default `1.0.0`) is the oldest client API contract version still served. Raise it only deliberately, once the request counts per `X-Client-Api-Version` (`GET /metrics` with `METRICS_ENABLED=true`, or the `First request from client API version` log lines) show that old clients are gone, and mention it in the release notes. The value must be a valid version with the backend's major version and not above its `API_VERSION`; otherwise the server refuses to start.

## Rollback

1. Run the **Deploy** workflow manually (Actions → Deploy → Run workflow, branch `edge`) with the previous tag, for example `v0.5.0`. Go releases roll back with **Deploy (Go)** instead ([deploy-go.md](deploy-go.md)).
2. If the release changed the database schema, restore the backup made before the deployment or apply the down migration.
3. Fix forward with a `fix:` commit; never move or delete a published tag.

## CI/CD setup

Workflows in `.github/workflows/`: `ci.yml` (tests against PostgreSQL and a Docker build on every PR), `pr-title.yml`, `api-ci.yml` (contract checks), `labeler.yml` (`api-change` label), `frontend-compat.yml`, `api-publish.yml` (npm, called by `release.yml`), `release.yml` (release-please, GHCR release and snapshot images, contract package, compose stack asset, then promote to `edge` and deploy), `deploy.yml` (SSH deploy of the Python backend, also runnable manually for rollbacks) and `deploy-go.yml` (the Go backend to staging or production, [deploy-go.md](deploy-go.md)).

One-time setup:

0. npm: `@myronsi/messenger-api` must exist (the first version has to be published with a token or by hand, because a trusted publisher can only be added to an existing package). Then open the package on npmjs.com, Settings, Trusted Publisher, GitHub Actions, and enter owner `myronsi`, repository `messenger-back`, workflow filename `release.yml`, no environment. Optionally set "Require two-factor authentication and disallow tokens" afterwards.

1. Server: install Docker with the Compose plugin, create a deploy user in the `docker` group and the directory `/opt/messenger` owned by it (or set the repository variable `BACKEND_DEPLOY_PATH`). Create `/opt/messenger/.env` with `SECRET_KEY` (see README). `BACKEND_IMAGE` is managed by the deploy script.
2. GitHub: create the environment `production` (optionally with required reviewers) and add the secrets `SSH_HOST`, `SSH_USER`, `SSH_KEY` (a private key dedicated to deploys) and `SSH_KNOWN_HOSTS` (output of `ssh-keyscan <host>`).
3. Repository settings → Actions → General: allow workflows to create pull requests (needed by release-please).
4. Release PRs and tags created with the default `GITHUB_TOKEN` do not trigger other workflows, so CI does not run on the release PR itself. Use a personal access token in `release-please` if you need that.