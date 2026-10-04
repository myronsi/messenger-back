# Releasing

The backend always deploys first and stays compatible with the previous frontend release.

## Release order

1. Backend: merge the release PR, deploy the new version.
2. Verify the deployed backend (health check, `GET /version` returns the new version and commit).
3. Frontend: merge its release PR (see `messenger-front/docs/releasing.md`) and deploy.

## Checklist

1. All PRs for the release are merged to `master` with Conventional Commit titles and CI is green.
2. Check that every issue in the milestone is closed or moved to the next milestone.
3. Release: create the tag and the GitHub Release (`vX.Y.Z`, notes from `CHANGELOG.md`). Merging the release-please PR does this and also builds the image with no manual steps.
4. Deploy: happens automatically after the release: the release workflow moves the `edge` branch to the release commit and dispatches `deploy.yml` on it (environment `production` only accepts deployments from `edge`). It uploads `compose.yaml`, backs up the database with `pg_dump` (the last 10 backups are kept in `backups/`), pulls `ghcr.io/myronsi/messenger-back:vX.Y.Z`, restarts the stack and checks `http://127.0.0.1:8000/`. If the check fails the previous image is restored automatically and the workflow fails.
5. Check the version shown in the app (Profile → About) and the logs after the deployment.

## Docker images

Images are published to `ghcr.io/myronsi/messenger-back` by `release.yml`:

| Event | Tags |
| --- | --- |
| Release `v0.5.0` | `0.5.0`, `0.5`, `latest` and `v0.5.0` (used by the deploy workflow) |
| Pre-release `v1.0.0-alpha.1` | `1.0.0-alpha.1` and `v1.0.0-alpha.1` (no `0.5`/`latest`) |
| Every merge to `master` | `master` and `sha-<short sha>` |

The commit is baked into the image (build argument `COMMIT` -> `APP_COMMIT`) and shown by `GET /version`.

## Starting a new release train

Add a `Release-As: X.Y.0` footer to a commit (for example `git commit --allow-empty -m "chore: start 0.6" -m "Release-As: 0.6.0"`) so that `MAJOR.MINOR` matches the frontend. While the version is `0.x` `bump-minor-pre-major` and `bump-patch-for-minor-pre-major` turn breaking changes into minor and features into patch releases.

## Raising the minimum client version

`MIN_CLIENT_API_VERSION` (environment, default `1.0.0`) is the oldest client API contract version still served. Raise it only deliberately, once the request counts per `X-Client-Api-Version` (`GET /metrics` with `METRICS_ENABLED=true`, or the `First request from client API version` log lines) show that old clients are gone, and mention it in the release notes.

## Rollback

1. Run the **Deploy** workflow manually (Actions → Deploy → Run workflow, branch `edge`) with the previous tag, for example `v0.5.0`.
2. If the release changed the database schema, restore the backup made before the deployment or apply the down migration.
3. Fix forward with a `fix:` commit; never move or delete a published tag.

## CI/CD setup

Workflows in `.github/workflows/`: `ci.yml` (tests against PostgreSQL and a Docker build on every PR), `pr-title.yml`, `release.yml` (release-please, GHCR release and snapshot images, then promote to `edge` and deploy) and `deploy.yml` (SSH deploy, also runnable manually for rollbacks).

One-time setup:

1. Server: install Docker with the Compose plugin, create a deploy user in the `docker` group and the directory `/opt/messenger` owned by it (or set the repository variable `BACKEND_DEPLOY_PATH`). Create `/opt/messenger/.env` with `SECRET_KEY` (see README). `BACKEND_IMAGE` is managed by the deploy script.
2. GitHub: create the environment `production` (optionally with required reviewers) and add the secrets `SSH_HOST`, `SSH_USER`, `SSH_KEY` (a private key dedicated to deploys) and `SSH_KNOWN_HOSTS` (output of `ssh-keyscan <host>`).
3. Repository settings → Actions → General: allow workflows to create pull requests (needed by release-please).
4. Release PRs and tags created with the default `GITHUB_TOKEN` do not trigger other workflows, so CI does not run on the release PR itself. Use a personal access token in `release-please` if you need that.