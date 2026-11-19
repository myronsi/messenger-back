# Internal API

Endpoints for other services of the operator, first of all the moderation service (repository
`messenger-moderation`). They are not part of the public contract in `api/` and are never reachable from the
internet.

## Listener

| Setting | Meaning |
|---|---|
| `INTERNAL_ADDR` | Address of the internal listener inside the API process, for example `:8082`. Empty (default): no listener. |
| `INTERNAL_TOKEN` | Shared service token, at least 32 characters. Required when `INTERNAL_ADDR` is set. |

- The port is **not published** in `deploy/go/compose.yaml`. Other containers reach it over a Docker network
  (`http://api:8082` from a container attached to the stack's network).
- Every request must send `Authorization: Bearer <INTERNAL_TOKEN>`; anything else gets `401`. The comparison
  takes constant time; rejected requests are logged with method and route only.
- Responses are JSON with `Cache-Control: no-store`.
- Generate the token once with `openssl rand -base64 48` and give the same value to the moderation service.

## Routes

All routes start with `/internal/v1`.

| Route | Purpose | Added in |
|---|---|---|
| `GET /ping` | `{"service":"messenger-api","version":"…"}`; lets the moderation service check the token and the connection | M0 |
| `POST /users/{user_id}/status` | Set an account status as a person on the moderation team: `{"status", "reason_code", "reason_text", "staff_ref"?}` → `{"before", "after"}` (each like the `account_status` event) | M2 |
| `POST /users/{user_id}/reports/dismiss` | Dismiss the account's active reports, all of them or `{"reporter_ids": ["…"]}`; the status is recomputed → `{"dismissed", "before", "after"}` | M2 |
| `POST /packs/{kind}/{pack_id}/hide`, `POST /packs/{kind}/{pack_id}/unhide` | Hide or restore a pack; `kind` is `sticker_pack` or `emoji_pack`; unhiding clears its reports → `{"changed"}` | M2 |
| `DELETE /users/{user_id}` | Delete an account like `DELETE /me` without the password: every session ends, chats are cleaned up, `user.deleted` is emitted → `{"deleted": true}` | M2 |
| `GET /pack-images/{attachment_id}` | The image file of a sticker or custom emoji that belongs to a pack (no other files), for the console's pack page; a `302` to a short-lived storage URL when signed URLs are on | M2 |
| `GET /stats/usage?day=YYYY-MM-DD` | Counts for the daily statistics of the moderation service: `active_day` (accounts with a session used since 00:00 UTC of the day), `active_30d` (accounts with a session used in the 30 days before the end of the day, or before now for today), `storage_bytes` (stored files plus their converted versions, from `attachments.size` and `attachments.variants`), `as_of`. Only totals, no ids. `400` (`field`: `day`) for a malformed or future day | M5 |

Rules and effects of the M2 actions (staff decisions, reporter notices): [moderation.md](moderation.md#staff-decisions).
IDs are decimal strings in paths and bodies. Errors are JSON `{"error", "field"?, "message"?}`:

| Status | `error` | When |
|---|---|---|
| `400` | `invalid_request` | Bad id, bad JSON, an unknown field, or a value the rules refuse (`field` names it: `status`, `reason_code`, `reason_text`, `staff_ref`, `reporter_ids`, `kind`) |
| `403` | `forbidden` | Hiding a built-in sticker pack |
| `404` | `not_found` | Unknown or deleted user or pack |
| `503` | `unavailable` | A database did not answer; retry after `Retry-After` seconds |

Later milestones of the moderation plan add more actions here (remove a message, send an in-app notice).

## Read-only database user for the moderation service

The moderation service reads reports, users and moderation state straight from PostgreSQL with a user that can
only read. Create it once as the database owner (replace the password and the database name):

```sql
CREATE ROLE moderation_reader LOGIN PASSWORD '<random password>';
GRANT CONNECT ON DATABASE messenger TO moderation_reader;
GRANT USAGE ON SCHEMA public TO moderation_reader;
```

- The user gets **only the tables and columns a moderation feature needs**, never `ALL TABLES` and no default
  privileges, so password hashes, sessions, recovery data and messages stay out of reach. The exact grants per
  feature are listed in messenger-moderation's `docs/deploy.md` ("Read grants"); a change here that a
  moderation feature must read (a new table or column) needs a matching grant there.
- The moderation service keeps its own data in a separate database (`moderation`) owned by its own user; it
  never writes to the Messenger database. Changes go through the internal routes above.
