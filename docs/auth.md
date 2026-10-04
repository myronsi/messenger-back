# Authentication, sessions and 2FA

Code: `internal/auth` (use cases), `internal/httpapi/authn.go` (middleware), `internal/httpapi/auth_server.go` (handlers).
The contract is `api/openapi.yaml`.

## Tokens

| Token | Form | Lifetime | Where it lives |
| --- | --- | --- | --- |
| Access | JWT, HS256 with `JWT_SECRET`, claims `sub`, `sid`, `typ=access`, `aud` | 15 minutes | `Authorization: Bearer` header, in memory of the client |
| Refresh | 32 random bytes, only the SHA-256 hash is stored in `user_sessions` | session duration (setting: 30, 90, 180 or 365 days) | `refresh_token` cookie |
| 2FA login challenge | random id | 5 minutes, 5 attempts | Redis, never a JWT |
| WebSocket ticket | random id | 30 seconds, single use | Redis |

The signing algorithm, the token type and the audience are checked on every token; a refresh token or a challenge is never accepted as an access token.
The session (`sid`) is loaded once per request and cached in Redis for `SESSION_CACHE_TTL` (30 s). Revoking a session removes the cache entry, so a revoked session stops working immediately.

## Refresh cookie

`HttpOnly`, `SameSite=Lax`, `Secure` unless `COOKIE_SECURE=false` (development only; production refuses it), `Path=<base>/auth/refresh`.
The cookie is only sent to the refresh endpoint, so `POST /auth/logout` relies on the bearer token; it also accepts the cookie for clients that send it.

Every refresh rotates the token. Every replaced hash is kept in `user_session_rotated_tokens` (until the session is deleted), so a token from any earlier generation is recognised:

- presenting it again within `REFRESH_REUSE_GRACE` (10 s) of its replacement is treated as a lost response or parallel tab: the request fails with `401` but the session survives and the response does **not** clear the refresh cookie, so the cookie set by the winning request stays;
- otherwise it is treated as theft: **the session is revoked** and the event is recorded.

### Migration from the Python backend (MSGC-77)

Refresh hashes and sessions are kept as they are, so existing sessions continue. v1 cookies have `Path=/auth` (or `<COOKIE_PATH_PREFIX>/auth`); set `REFRESH_COOKIE_PATH` to that value during the migration window so browsers still send them.

## Passwords

Argon2id for new hashes. Verified: the Argon2id PHC strings of v1 (`argon2$…`, argon2-cffi) and legacy PBKDF2 `salt:hash`. A successful login with an old format rehashes to the current parameters. At most `PASSWORD_HASH_CONCURRENCY` hashes run at once so that a flood of logins cannot exhaust memory. Unknown users are verified against a dummy hash, so timing does not reveal whether a username exists. Test fixtures are hashes created by the real v1 code.

## Two-factor authentication

TOTP (`pquerna/otp`). The secret is sealed with AES-GCM and `ENCRYPTION_KEY` (the sealed value is bound to the user). Setup and disabling require the password; disabling also requires a code. Confirming returns the recovery codes once; only their hashes are stored. A recovery code works once.
Login with 2FA returns `two_factor_required` and a `login_challenge`; `POST /auth/login/2fa` completes it.

## Rate limiting

Sliding windows in Redis (shared by all API instances), per client IP and per username/user: login (20 per IP, 8 per user per 15 min), 2FA (20 / 10), refresh (300 per IP), registration, password change, recovery and WebSocket tickets. A refused request gets `429` with `Retry-After`. If Redis is unavailable the request is denied: a limiter that fails open is no protection against password guessing.

The client IP is the TCP peer. `X-Forwarded-For` / `X-Real-IP` are honoured only when the peer matches `TRUSTED_PROXIES` (comma-separated IPs or CIDRs; empty by default, so nothing is trusted). The forwarded chain is read from the right, skipping trusted hops. Set it to your load balancer or ingress range when running behind one; otherwise all clients would share the proxy address and its rate limit. Other peers cannot choose their own address.

## Error mapping

| Situation | Status | `code` |
| --- | --- | --- |
| No, invalid or revoked access token | 401 + `WWW-Authenticate: Bearer` | `unauthenticated` |
| Expired access token | 401 | `token_expired` |
| Wrong password or 2FA code on a public route | 401 | `invalid_credentials` / `invalid_two_factor_code` |
| Wrong password or code on a session route | 403 | same |
| Wrong code while confirming 2FA | 400 | `invalid_two_factor_code` |
| Bad challenge or refresh token | 401 | |
| Username taken | 409 | `already_exists` |
| Validation | 422 | `validation_failed` with `errors[]` |
| Rate limited | 429 + `Retry-After` | `rate_limited` |
| Wrong content type / body too large | 415 / 413 | |

## Access control

`routeAccess` in `authn.go` lists the routes that do not need a token (`/meta`, register, login, 2FA login, refresh, recover, reset-password; logout is optional). **Everything else is denied by default**, including routes that are not implemented yet.

## Recovery

`POST /auth/recover` stays unavailable (`501`) until the recovery redesign (MSGC-80 to MSGC-86) and the Shamir port (MSGC-69), so no recovery token is issued yet. The single-use token storage (`recovery_tokens`, 15 minutes) and `POST /auth/reset-password` are in place.

## Configuration

`JWT_SECRET`, `ENCRYPTION_KEY`, `RECOVERY_PEPPER` (required), `COOKIE_SECURE`, `REFRESH_COOKIE_PATH`, `SESSION_CACHE_TTL`, `REFRESH_REUSE_GRACE`, `PASSWORD_HASH_CONCURRENCY`, `TRUSTED_PROXIES`; see `.env.example`.
