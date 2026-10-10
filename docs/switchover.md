# Switching to the Go backend

This is the runbook for #56: moving production from the Python backend (`0.x`) to the Go backend (`1.0.0`, API contract `2.0.0`), and retiring the Python code afterwards. It sends no command to production by itself. Every step marked **production** is done by a person, in the maintenance window, with access the repository does not have.

The data copy is [migration-v1.md](migration-v1.md), the production stores are in [deployment.md](deployment.md), and releases are in [releasing.md](releasing.md).

## Decide beforehand

| Decision | Why |
|---|---|
| **The maintenance window** (date, length) | The final migration runs while nobody writes. The staging rehearsal measures how long it takes; plan twice that. |
| **How long a rollback stays possible** | Nothing copies v2 back to v1. Rolling back loses everything written on the Go backend since the switch (messages, uploads, new accounts). Suggestion: a rollback is only possible until the window closes. After that, fix forward. |
| **How long v1 is kept** (the database, `static/`, the last backup) | It is the rollback target and the source to check the migration against. Suggestion: 30 days, then archive it according to the retention policy and delete it. |
| **Who decides, who runs each step** | One person decides go, no-go and rollback. Name them in the announcement. |

## Before the switch

### 1. The last Python release

- The last `0.x` release is the last Python version: `v0.5.4` today, and the pending `0.5.5` release PR if it is merged.
- Create a `python` branch from its tag for hotfixes until the switch. Release-please keeps working on `master`.

### 2. Release and deploy path for the Go backend

[deploy-go.md](deploy-go.md) describes it:
- `1.x` releases are built from `go.Dockerfile` and deployed by `deploy-go.yml` with the stack in `deploy/go`;
- every Go release goes to staging;
- production gets one only once the repository variable `GO_PRODUCTION` is `true`.

Until then, `0.x` releases keep deploying Python to production, as they should. Set up both hosts and the `staging` environment as described there.

### 3. Production stores and secrets

- **Stores:** PostgreSQL, ScyllaDB, Redis, Elasticsearch and object storage as in [deployment.md](deployment.md), with backups set up and one restore tested.
- **New secrets in the secret manager:** `JWT_SECRET`, `ENCRYPTION_KEY` and `RECOVERY_PEPPER`. Do not reuse a development value.
- **The v1 key:** `V1_SECRET_KEY` is the Python backend's `SECRET_KEY` (in `/opt/messenger/.env` on the server). Pass it to the migration job from the secret manager. It is never pasted anywhere else, and it is dropped after the switch.
- **The refresh cookie path:** set `REFRESH_COOKIE_PATH` to v1's cookie path (`<COOKIE_PATH_PREFIX>/auth`). Copied sessions only stay logged in if browsers keep sending the cookie.

### 4. The releases

- **Backend:** `1.0.0-alpha.N` or `-beta.N` releases on staging. Then `1.0.0` with contract `2.0.0` (a `Release-As: 1.0.0` footer; see [releasing.md](releasing.md)).
- **Contract:** `2.0.0` is published under `latest` by that release.
- **Frontend:** the frontend for v2 (F26–F30) can only be released after that, because its `main` requires a stable contract. Have the release PR ready.

### 5. Rehearsal on staging

Staging gets a copy of production data. That makes it production data: same access rules, nothing of it in tickets, chats, logs that leave the company or external tools, and deleted with staging.

1. Restore the latest production backup (`backups/` of the deploy script) and a copy of the `messenger_static` volume into staging.
2. Run the migration exactly as on the day (below): a dry run, then the real run. Write down how long each phase takes.
3. Read the report (below). Every difference must be explained before the day.
4. Run the frontend for v2 against staging (F30), with accounts that exist in the copy: one with 2FA, one with a group avatar, one with voice messages.
5. Run the load tests against staging ([load-testing.md](load-testing.md)) and record the results there (#55).
6. Rehearse the rollback once: switch the staging proxy back to the Python backend and log in.

Repeat steps 1 to 3 after any change to `cmd/migrate-v1`.

## On the day

All steps are **production**.

1. **Announce** the window in the app and wherever users are told about outages, at least a few days ahead.
2. **Stop writes to v1.** The Python backend has no read-only mode, so stop it (`docker compose stop backend` in `/opt/messenger`). The reverse proxy shows a maintenance page for the API and `/ws`.
3. **Freeze the rollback point:**
   - take a `pg_dump` of the v1 database and a copy of the `messenger_static` volume;
   - keep both, and the database itself, untouched until the rollback window is over.

   The migration only reads v1 (give its `V1_DATABASE_URL` a read-only user, and mount `static/` read-only).
4. **Deploy `1.0.0` to production:**
   - set the repository variable `GO_PRODUCTION` to `true`;
   - run Actions → Deploy (Go) from `edge` with `v1.0.0` and `production`. `deploy.sh` migrates the empty stores and starts `api` and `worker` in `/opt/messenger-go`.

   The proxy does not send them traffic yet.
5. **Dry run:**

   ```sh
   /opt/messenger-go/migrate-v1.sh /opt/messenger-go -dry-run -report dry.json
   ```

   It reads v1 through the Python stack's network and volume; the `V1_*` settings are in [deploy-go.md](deploy-go.md).

   It stops if a TOTP secret cannot be decrypted. That most likely means `V1_SECRET_KEY` is wrong: fix the key, do not reach for `-drop-unreadable-2fa`. Use that flag only if the rehearsal showed a known number of broken secrets.
6. **The run**, into empty v2 stores:

   ```sh
   /opt/messenger-go/migrate-v1.sh /opt/messenger-go -report run.json
   ```

   It is resumable: if it stops, fix the cause and start it again with the same command. Do not pre-copy into production before the window. Reactions removed and messages delivered in v1 after an early run would not be taken back.
7. **Read the report** (`reports/run.json`, mode 0600; counts only). Go on only if all of these hold:
   - `verification` has the same count for v1 and v2 in every table, or the differences match the rehearsal: merged direct chats, dropped self-blocks, skipped requests;
   - every `chat_samples` entry has `v1_messages` = `v2_messages`;
   - `*_rows_refused` is 0, or explained by the rehearsal;
   - `two_factor_disabled_unreadable` is 0, or the number known from the rehearsal;
   - `message_files_missing` and `file_messages_kept_as_text` match the rehearsal;
   - the notes say the search index was rebuilt.
8. **Deploy the frontend for v2,** and point the reverse proxy at the Go backend: `/api/v2` and `/ws` to `api`. The Python backend stays stopped and out of the proxy.
9. **Smoke test** with a test account created for this, not a user's:
   - `/readyz` is ready on every replica;
   - `GET /api/v2/meta` answers `backend_version` `1.0.0` and `api_version` `2.0.0`;
   - log in (also with 2FA), open a chat with history, send a message and see it arrive on a second device;
   - upload and download a file, play a voice message, and search for an old message.
10. **Open the window:** remove the maintenance page.
11. **Watch** errors, latency (p99 of history, sending and search) and WebSocket counts for at least the first hours. The decider is reachable for the whole rollback window.

## Rollback

Possible only within the rollback window decided beforehand.

1. Put the maintenance page back.
2. Point the proxy at the Python backend and start it (`docker compose start backend`). Its database and `static/` are as they were at step 3, because the migration only reads them.
3. Deploy the last Python frontend release.
4. Tell users that whatever they wrote since the switch is gone.
5. Keep the v2 stores as they are, so the failure can be investigated. Wipe them before the next attempt, because the migration expects empty stores.

After the window, there is no rollback: fix forward on `1.0.x`.

## After a stable period

- Drop the `migrate_v1_*` tables (`docs/migration-v1.md`), and remove `V1_SECRET_KEY` from the secret manager.
- After the agreed time, archive and then delete the v1 database, the `messenger_static` copy and the v1 backups.
- Remove the Python code from `master`: `server/`, `tests/`, `Dockerfile`, `compose.yaml`, `requirements*.txt`, `start.sh` and the Python CI jobs. Update the README.
- Close B10–B20 as superseded by the Go backend.
- Raise `MIN_CLIENT_API_VERSION` once old clients are gone (see [releasing.md](releasing.md)).
- Close #56.
