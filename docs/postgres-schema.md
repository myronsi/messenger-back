# PostgreSQL schema v2

PostgreSQL holds everything except messages (those live in ScyllaDB): accounts, the relationships between
users, chats and their participants, attachment metadata and the security log.

- Migrations: `migrations/postgres/NNNNNN_name.{up,down}.sql`, applied with `make migrate-postgres`
  (`make migrate-postgres-down` rolls everything back; development only).
- Queries: `internal/store/postgres/queries/*.sql`, compiled by [sqlc](https://sqlc.dev) into
  `internal/store/postgres/sqlcdb` (generated, do not edit).
- Repositories: `internal/store/postgres` (`Store.Users()`, `Chats()`, `Attachments()`, `SecurityEvents()`).
  Feature packages depend on the interfaces in `repository.go`, not on the pool or the generated code.

## Tables

| Migration | Tables |
| --- | --- |
| `000001_users` | `users`, `user_recovery_shares` (temporary, for the MSGC-69 port) |
| `000002_user_settings` | `user_avatar_history`, `user_privacy_settings`, `user_privacy_exceptions`, `user_blocks`, `user_contact_names` |
| `000003_auth` | `user_security_settings`, `user_sessions`, `recovery_tokens`, `two_factor_challenges`, `user_2fa_recovery_codes`, `user_security_events` |
| `000004_chats` | `chats`, `participants`, `user_chat_pins`, `approval_requests` |
| `000005_attachments` | `attachments` |

Changes compared to v1:

- Usernames are unique ignoring case (`users_username_lower_key` on `LOWER(username)`) and validated by a
  `CHECK` (`[A-Za-z0-9_]{3,32}`). The Shamir recovery material is not in `users` but in the separate
  `user_recovery_shares` table, which is dropped when MSGC-69 no longer needs it.
- One membership model. `chats.type` is `direct` or `group`; **every** member, direct or group, is a row of
  `participants(chat_id, user_id, role, last_read_message_id, joined_at)`. There is no `user1_id`/`user2_id`
  and no `groups.admin_id`: the owner is the participant with role `owner`, and a partial unique index allows
  at most one owner per chat.
- A direct chat has a `direct_key` (`"<low id>:<high id>"`, unique, checked by a constraint), so a pair of
  users can never have two chats, whichever of them creates it. Groups have a name and no key.
- `last_read_message_id` is a plain `BIGINT` without a foreign key: messages live in ScyllaDB.
- Every timestamp is `TIMESTAMPTZ` with a `now()` default; `updated_at` columns are kept by a trigger.
- Attachments store a storage key and metadata only. `duration` is in seconds, `waveform` a `SMALLINT[]`.

## Foreign keys and account deletion

Every foreign key has an explicit `ON DELETE` rule (a test fails otherwise):

| Rule | Used for |
| --- | --- |
| `CASCADE` | everything that belongs to a user or to a chat: settings, sessions, tokens, security events, blocks, pins, participants, approval requests, attachments of a chat |
| `SET NULL` | authorship that survives its author: `chats.created_by`, `attachments.uploader_id` |

`UserRepository.DeleteAccount` runs in one transaction:

1. Locks the user, then every chat the user is in (in chat-id order, so concurrent deletions and ownership
   transfers cannot deadlock or act on stale roles).
2. Deletes the user's direct chats (they cannot outlive one side).
3. For every group the user owns, hands the group to the next admin, then moderator, then the member who has
   been there longest, and removes the user from it. A group without anyone else is deleted.
4. Deletes the user; all remaining rows cascade.

The rows of the deleted chats are gone afterwards, so the call **returns the storage keys of their attachments**
and the caller deletes the objects from object storage. Files the user uploaded to chats that survive stay,
without an uploader.

## Data access rules

- One `pgxpool.Pool` per process (`POSTGRES_MAX_CONNS`). Every repository call runs under a timeout
  (`POSTGRES_QUERY_TIMEOUT`); a transaction counts as one call.
- Multi-step operations are transactions: creating a group with members, ownership transfer, deleting a chat or
  an account. Operations that change a group's membership lock the chat row first, so they run one after another.
- Errors are sentinels (`ErrNotFound`, `ErrUsernameTaken`, `ErrAlreadyParticipant`, `ErrInvalid`, ...). They
  name constraints, never the offending values.

## Adding a migration

1. Add `NNNNNN_name.up.sql` and `.down.sql` (the next number; the down file must undo the up file exactly).
2. Add or change queries in `internal/store/postgres/queries/`.
3. `make generate` and commit the changes to `sqlcdb/`; CI fails when they are out of date.
4. Never edit a migration that was released; add a new one.

## Tests

Database tests skip unless `TEST_DATABASE_URL` points at a server where they may create databases, for example
the dev stack: `TEST_DATABASE_URL=postgres://postgres:...@localhost:5432/postgres?sslmode=disable go test ./...`.
Each test creates a database of its own with the full schema and drops it afterwards. They cover the migrations
(up, down, up), the foreign key rules and indexes, and the repositories including account deletion.

## Open points

- The index list of MSGC-51 was not available when this was written; the indexes were derived from the access
  patterns of the v1 backend (every foreign key column is indexed, plus partial unique indexes for rules).
  Compare with MSGC-51 and add what is missing in a new migration.
