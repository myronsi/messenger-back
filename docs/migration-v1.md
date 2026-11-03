# Completed v1 data migration

The Python-to-Go data migration was completed during the production cutover on 2026-11-01. The one-time `cmd/migrate-v1` importer, its deployment scripts, Compose overlay and credential template were removed on 2026-11-03. This is a record of the migration's data mapping and compatibility boundaries, not an executable migration runbook. Historical source and detailed procedures remain in Git history.

## Data mapping

| Legacy data | Go destination / treatment |
| --- | --- |
| Accounts, chat/group metadata, memberships, privacy and blocking | PostgreSQL; references retain migrated identity relationships |
| Password hashes | Existing formats remain verifiable; successful login upgrades old hashes to the current Argon2id parameters |
| TOTP secrets and backup codes | Converted for Go's authentication implementation; current encryption uses `ENCRYPTION_KEY` |
| Valid sessions and refresh-token hashes | Migrated session records; expired/revoked sessions and short-lived login/recovery challenges were not carried forward |
| Message history, reactions and visibility markers | ScyllaDB; server IDs remain string-valued in the frontend |
| Uploaded files, voice messages and avatars | Object-storage attachments with ownership/access links |
| Search | Rebuilt from the migrated Go message store |

The importer normalized invalid/colliding usernames and duplicate direct-chat relationships according to its migration rules. Migration reports recorded copied, renamed, merged, skipped and failed counts without message contents or credentials. These historical reports, not an assumed one-to-one row count, are the evidence for the completed copy. Do not infer today's account or message counts from the cutover reports.

## Compatibility still required

Password and backup-code verification for migrated accounts remains part of the supported Go application. Removing legacy format verification is a separate account-migration decision, not a Python infrastructure cleanup step. The frontend's screen-to-v2 transport adapter also remains in use.

Recovery now uses a recovery key. Historical recovery shares/QR parts are not valid substitutes for that key. Existing accounts without a recovery key can enroll using their current credentials. See [authentication](auth.md) and the current API contract for recovery requirements.

Refresh-cookie scope is the public Go endpoint, normally `/api/v2/auth/refresh`. Do not copy the old Python `/api/auth` override from historical migration notes.

## Operating after migration

Use the current Go deployment and schema migrations. Do not provision `V1_DATABASE_URL`, `V1_SECRET_KEY`, `V1_NETWORK` or `V1_STATIC_VOLUME` for normal operation. Do not reimport archived Python data into live Go stores: it is a historical snapshot and has no record of post-cutover writes.

For archive locations, removed services and the final deployed-image cleanup boundary, see [Python retirement](python-retirement.md). For the cutover outcome and rollback limitations, see [completed cutover](switchover.md).
