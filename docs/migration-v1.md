# Migration from the Python backend

`cmd/migrate-v1` (the image's `/app/migrate-v1`) copies the Python backend's PostgreSQL database and `static/` folder into the stores of the Go backend (#54). The switch itself (when, with what downtime, the rollback) is #56.

## Running

The tool needs the Go backend's own environment (`DATABASE_URL`, `SCYLLA_*`, `REDIS_URL`, `ELASTICSEARCH_URL`, the storage settings, `ENCRYPTION_KEY`, …), with the v2 schema migrated (`make migrate`), and:

| Variable | |
|---|---|
| `V1_DATABASE_URL` | the Python backend's PostgreSQL (read only) |
| `V1_STATIC_DIR` | its `static/` folder (uploads, voice messages, avatars). Files are read through it only: links that lead out of it are refused. |
| `V1_SECRET_KEY` | its `SECRET_KEY`, to decrypt TOTP secrets (surrounding whitespace is ignored, as v1 did). |

```sh
migrate-v1 -dry-run -report dry.json      # reads everything, writes nothing, counts what it would do
migrate-v1 -report run.json               # users, chats, files, messages, then rebuilds the search index
migrate-v1 -phases messages,search        # a later run of some steps
```

| Flag | |
|---|---|
| `-dry-run` | read everything, write nothing; the report counts what a run would do (files it would copy, files that are missing) |
| `-phases` | the steps to run, in order: `users,chats,files,messages,search` |
| `-report` | where the report goes (default `migrate-v1-report.json`) |
| `-restart-messages` | copy the messages from the first one again |
| `-drop-unreadable-2fa` | turn 2FA off for users whose TOTP secret cannot be decrypted, instead of stopping (see 2FA below) |

Run the dry run first: it decrypts every TOTP secret too, so a wrong `V1_SECRET_KEY` shows there. The final run, the one the switch relies on, must read a v1 that no longer changes (the Python backend stopped or read-only, #56): the files and messages phases only see what is in v1 when they read it.

## Repeatable and resumable

Every write is idempotent (upserts, `ON CONFLICT DO NOTHING`, idempotent ScyllaDB writes), so a run can be repeated or interrupted and started again. A rerun also takes over what changed in v1 since:
- **Users:** password hashes, display names, bios and last-seen times are updated. Sessions revoked or expired in v1 since are revoked in v2 too.
- **Messages:** continue after the last batch a run finished (`migrate_v1_state`); `-restart-messages` copies them all again. A message in a chat the chats phase has not copied yet stops the run (run the chats phase again) rather than being skipped for good. Messages of chats the chats phase left out on purpose are counted (`messages_skipped_chat`) and skipped.
- **Files:** copied once each (`migrate_v1_files` maps the v1 path and purpose to its attachment, whose id follows from them), so a rerun or a forwarded file never copies twice. A file that exists but cannot be stored stops the run.
- **Merged and skipped chats:** `migrate_v1_chats` maps every v1 chat to the v2 chat it became; `migrate_v1_skipped_chats` lists the ones left out.

A row v2 refuses does not stop its whole batch: it is counted (`<table>_rows_refused`) and logged by id.

The four `migrate_v1_*` tables can be dropped once the switch is final.

## What becomes what

- **Users:**
  - Ids and password hashes stay as they are. v2 verifies v1's argon2 and PBKDF2 hashes and upgrades them at the next login.
  - Usernames that do not fit v2 (3–32 of `A–Z a–z 0–9 _`, unique ignoring case; v1 never checked, B6) are renamed to their fitting characters plus `_<id>` (and `_<n>` should that be taken). Of names that differ only in case, the lowest id keeps it. Empty display names become the username.
  - New v2 users and chats get ids above every id v1 handed out, including senders of messages whose accounts are gone: a new user must never become the sender of someone else's old messages.
  - Privacy settings with unknown values fall back to `everyone`. Exceptions for settings v2 has none for (`direct_messages`) are dropped. Self-blocks are dropped.
- **2FA:**
  - TOTP secrets are decrypted with the v1 key (Fernet under HKDF-SHA256 of `SECRET_KEY`) and sealed with `ENCRYPTION_KEY`.
  - A secret that cannot be decrypted stops the run: most likely `V1_SECRET_KEY` is wrong, and going on would turn 2FA off for every user. With `-drop-unreadable-2fa`, 2FA is turned off for those users instead (it could never be checked again, and the account would be locked), and the report counts them.
  - Recovery codes keep their hashes, which are the same in v1 and v2. Pending 2FA setups are dropped.
- **Sessions:**
  - Valid sessions (not revoked, not expired) are copied with their refresh token hash, so users stay logged in. Set `REFRESH_COOKIE_PATH` to v1's cookie path (`<COOKIE_PATH_PREFIX>/auth`) so browsers still send the cookie.
  - Access tokens do not carry over: clients refresh once.
  - The recovery shares are copied as they are; their redesign is #57.
- **Chats:**
  - `one-on-one` chats become direct chats. A pair that v1 had several chats for gets one (the lowest id), and the others' messages and pins move into it. Direct chats with a missing user or with oneself are left out, with their messages.
  - Groups keep their id. `groups.admin_id` is the owner, any other `owner` role becomes `admin`, and unknown roles become `member`.
  - Pins and approval requests move along; requests of a shape v2 refuses are dropped.
- **Messages:**
  - They keep their v1 ids (they sort before every v2 id) and times, which also pick their buckets.
  - Text stays text, even when it looks like JSON (B12).
  - A file message becomes a `file` or `voice` message with an attachment only if the file is the message's own (a `message_attachments` row, which is how v1 decided access), lies in `uploads/` or `vm/`, and exists in `V1_STATIC_DIR`. Otherwise the message stays text, and the report counts it: file JSON pointing at an avatar or another user's file never grants access to it.
  - Replies, forwards (with the original sender's name), edits and reactions are kept; v1 kept no reaction time, so a reaction gets its message's time.
  - `deleted_for` and `undelivered_to` become hidden markers. Legacy messages with empty content become deleted ones.
  - `read_by` moves the readers' read markers, and each chat's newest message orders the chat list.
- **Files:**
  - Uploads and voice messages become attachments with links for every message that uses them. A file used in one chat belongs to it; one that forwards spread over several chats belongs to none, so the links decide who may see it and deleting one chat does not take it from the others. Voice waveforms are scaled to 0–255.
  - Images are re-encoded like every v2 upload (EXIF such as locations is removed, and a thumbnail is made). Images the decoder refuses are copied as they are.
  - Avatars (current, history, group avatars) under `avatars/` become avatar attachments, and the current one is marked so in the history. The shared default images are skipped, and files that are not images leave the default avatar.
- **Afterwards:**
  - The search index is rebuilt from ScyllaDB (the `search` step, or `worker reindex`).
  - Unread counters need nothing: they are built from the read markers the first time a user loads the chat list.
- **Not copied:** recovery tokens and 2FA challenges (short-lived), revoked or expired sessions, and the security event log.

## Report

The report (`-report`, JSON, mode 0600) holds counts only, no names or message content:
- what was copied, renamed, merged, skipped or failed;
- a comparison of rows per table in v1 and v2;
- the message counts of up to 50 chats in both stores (the largest and some at random; merged chats are counted under the chat they joined).

Counts that differ are also logged. Read it before the switch; #56 makes that a step of the plan.
