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

Later milestones of the moderation plan add the actions here (set an account status with a reason, dismiss
reports, hide or unhide packs, remove a message, delete an account, send an in-app notice, storage figures).

## Read-only database user for the moderation service

The moderation service reads reports, users and moderation state straight from PostgreSQL with a user that can
only read. Create it once as the database owner (replace the password and the database name):

```sql
CREATE ROLE moderation_reader LOGIN PASSWORD '<random password>';
GRANT CONNECT ON DATABASE messenger TO moderation_reader;
GRANT USAGE ON SCHEMA public TO moderation_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO moderation_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO moderation_reader;
```

- `ALTER DEFAULT PRIVILEGES` covers tables that later migrations add; run it as the role that runs the
  migrations.
- The moderation service keeps its own data in a separate database (`moderation`) owned by its own user; it
  never writes to the Messenger database. Changes go through the internal routes above.
