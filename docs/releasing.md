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
4. Deploy:
   - `docker compose pull && docker compose up -d` with the image of the released version;
   - the database is backed up before the deployment (`pg_dump`).
5. Check the version shown in the app (Profile → About) and the logs after the deployment.

## Starting a new release train

Add a `Release-As: X.Y.0` footer to a commit so that `MAJOR.MINOR` matches the frontend.

## Rollback

1. Deploy the image of the previous release (`vX.Y.Z`) again.
2. If the release changed the database schema, restore the backup made before the deployment or apply the down migration.
3. Fix forward with a `fix:` commit; never move or delete a published tag.
