# Releasing

The backend always deploys first and stays compatible with the previous frontend release.

## Release order

1. Backend: merge the release PR, deploy the new version.
2. Verify the deployed backend (health check, `GET /version` once available).
3. Frontend: merge its release PR (see `messenger-front/docs/releasing.md`) and deploy.

## Checklist

1. All PRs for the release are merged to `master` with Conventional Commit titles and CI is green.
2. Check that every issue in the milestone is closed or moved to the next milestone.
3. Release: create the tag and the GitHub Release (`vX.Y.Z`, notes from `CHANGELOG.md`). Once release-please is set up (MSGC-58) merging its release PR does this.
4. Deploy: happens automatically after the release: the release workflow moves the `edge` branch to the release commit and dispatches `deploy.yml` on it (environment `production` only accepts deployments from `edge`). It uploads `compose.yaml`, backs up the database with `pg_dump` (the last 10 backups are kept in `backups/`), pulls `ghcr.io/myronsi/messenger-back:vX.Y.Z`, restarts the stack and checks `http://127.0.0.1:8000/`. If the check fails the previous image is restored automatically and the workflow fails.
5. Check the version shown in the app (Profile → About) and the logs after the deployment.

## Starting a new release train

Add a `Release-As: X.Y.0` footer to a commit so that `MAJOR.MINOR` matches the frontend. Pull requests are squash-merged using the PR description as the commit message, so put the footer on its own line at the end of the PR description.

## Rollback

1. Run the **Deploy** workflow manually (Actions → Deploy → Run workflow, branch `edge`) with the previous tag, for example `v0.5.0`.
2. If the release changed the database schema, restore the backup made before the deployment or apply the down migration.
3. Fix forward with a `fix:` commit; never move or delete a published tag.

## CI/CD setup

Workflows in `.github/workflows/`: `ci.yml` (tests against PostgreSQL and a Docker build on every PR), `pr-title.yml`, `release.yml` (release-please, GHCR image `vX.Y.Z` + `latest`, then promote to `edge` and deploy) and `deploy.yml` (SSH deploy, also runnable manually for rollbacks).

One-time setup:

1. Server: install Docker with the Compose plugin, create a deploy user in the `docker` group and the directory `/opt/messenger` owned by it (or set the repository variable `BACKEND_DEPLOY_PATH`). Create `/opt/messenger/.env` with `SECRET_KEY` (see README). `BACKEND_IMAGE` is managed by the deploy script.
2. GitHub: create the environment `production` (optionally with required reviewers) and add the secrets `SSH_HOST`, `SSH_USER`, `SSH_KEY` (a private key dedicated to deploys) and `SSH_KNOWN_HOSTS` (output of `ssh-keyscan <host>`).
3. Repository settings → Actions → General: allow workflows to create pull requests (needed by release-please).
4. Release PRs and tags created with the default `GITHUB_TOKEN` do not trigger other workflows, so CI does not run on the release PR itself. Use a personal access token in `release-please` if you need that.