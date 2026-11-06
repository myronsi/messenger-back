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

The worker (`cmd/worker`) runs a maintenance job every hour: it deletes sessions that expired or were revoked more than 30 days ago (the rotation history goes with them), prunes rotation history rows older than 30 days (an older replayed token is then just unknown, not recognised as reuse), and removes expired recovery capabilities (device recovery requests, WebAuthn challenges, email tokens and abandoned recovery mail jobs).

### Migration from the Python backend (MSGC-77)

The Python migration is complete. Keep migrated password verification for existing accounts. Refresh cookies must use `/api/v2/auth/refresh` (or the matching public Go base path); `/api/auth` does not match the Go endpoint and causes logout when access tokens expire.

## Passwords

Argon2id for new hashes. Verified: the Argon2id PHC strings of v1 (`argon2$…`, argon2-cffi) and legacy PBKDF2 `salt:hash`. A successful login with an old format rehashes to the current parameters. At most `PASSWORD_HASH_CONCURRENCY` hashes run at once so that a flood of logins cannot exhaust memory. Unknown users are verified against a dummy hash, so timing does not reveal whether a username exists. Test fixtures are hashes created by the real v1 code.

## Two-factor authentication

TOTP (`pquerna/otp`). The secret is sealed with AES-GCM and `ENCRYPTION_KEY` (the sealed value is bound to the user). Setup and disabling require the password; disabling also requires a code. Confirming returns the recovery codes once; only their hashes are stored. A recovery code works once.
Login with 2FA returns `two_factor_required` and a `login_challenge`; `POST /auth/login/2fa` completes it.

### Missing second factor on other operations

Clients do not show a code field up front. They send the request without `code`; when it is needed the server answers `401` with `code: two_factor_required` and the client asks for the code and repeats the same request with it. This applies to every operation that takes a second factor outside login:

- public: `POST /auth/recover/key` (recovery key), `POST /auth/password-reset/confirm` (email reset token);
- signed in: `POST /me/2fa/disable`, `POST /me/recovery-key`, `POST /auth/passkeys/register/options`, `PUT /me/email`, `DELETE /me/email`, `POST /auth/recovery-requests/{requestId}/approve`, `POST /auth/qr-login/{requestId}/approve`.

The order is fixed so the answer never reveals the 2FA state to someone who has not proven the rest: the password, recovery key, email token, or request ownership and matching number are checked first. Only when they are valid, the account has 2FA on and `code` is empty does the server answer `two_factor_required`. Invalid other credentials get the operation's usual error whatever the 2FA state, and a wrong code keeps its usual error (`invalid_credentials` with 400 on the recovery flows, `invalid_two_factor_code` with 403 on `POST /me/2fa/disable`). Nothing single-use is consumed by a `two_factor_required` answer, so the same recovery key, email token or recovery request works on the retry; the attempt still counts towards the rate limits. On signed-in routes this is the one 401 that does not mean a lost session, so clients must check `code` before refreshing or signing out.

## Rate limiting

Sliding windows in Redis (shared by all API instances), per client IP and per username/user: login (20 per IP, 8 per username and IP, and at most 100 per username from all addresses, per 15 min), 2FA (20 / 10), refresh (300 per IP), registration, password change, recovery (10 per IP, 5 per username and IP, 30 per username, per hour), email reset links (at most 3 sent per account per hour, enforced by the worker so the public response stays the same), passkey login failures (20 per IP per 15 min, separate from the password-login budget), QR sign-in requests (10 per IP per 10 min) and their polls (120 per IP per 2 min) and WebSocket tickets. Only failures from the same address count towards the per-username limit, so a stranger cannot lock an account; the higher per-username cap stops guessing spread over many addresses. IPv6 clients are counted by /64 network. A refused request gets `429` with `Retry-After`. If Redis is unavailable the request is denied: a limiter that fails open is no protection against password guessing.

The client IP is the TCP peer. `X-Forwarded-For` / `X-Real-IP` are honoured only when the peer matches `TRUSTED_PROXIES` (comma-separated IPs or CIDRs). Unset, only loopback (`127.0.0.0/8`, `::1`) is trusted; in production the API logs a warning at startup when it is unset and `HTTP_ADDR` is not a loopback address. The forwarded chain is read from the right, skipping trusted hops, so the proxy must append the peer address to `X-Forwarded-For`. Set it to your load balancer or ingress range when running behind one; otherwise all clients would share the proxy address and its rate limit. Other peers cannot choose their own address.

## Error mapping

| Situation | Status | `code` |
| --- | --- | --- |
| No, invalid or revoked access token | 401 + `WWW-Authenticate: Bearer` | `unauthenticated` |
| Expired access token | 401 | `token_expired` |
| Wrong password or 2FA code on a public route | 401 | `invalid_credentials` / `invalid_two_factor_code` |
| Wrong password or code on a session route | 403 | same |
| Wrong code while confirming 2FA | 400 | `invalid_two_factor_code` |
| Other credentials valid, 2FA on, no `code` (public and session routes) | 401 | `two_factor_required` |
| Bad challenge or refresh token | 401 | |
| Username taken | 409 | `already_exists` |
| Validation | 422 | `validation_failed` with `errors[]` |
| Rate limited | 429 + `Retry-After` | `rate_limited` |
| Wrong content type / body too large | 415 / 413 | |

## Access control

`routeAccess` in `authn.go` lists the routes that do not need a token (`/meta`, register, login, 2FA login, refresh, recover, reset-password, creating and polling a QR sign-in; logout is optional). **Everything else is denied by default**, including routes that are not implemented yet.

## Sign in with a QR code

A device that is not signed in (the new device) can be signed in by a device where the user is already signed in (the approving device), without typing the password on the new device.

1. The new device calls `POST /auth/qr-login` (public). The answer (`201`) has `request_id`, `poll_token`, `url` and `expires_at`. `url` is `<RECOVERY_WEB_URL>/qr/<request_id>` (the same frontend origin as the recovery links; without `RECOVERY_WEB_URL` it is the relative path `/qr/<request_id>`). The new device shows `url` as a QR code and keeps `poll_token` in memory only.
2. The new device calls `POST /auth/qr-login/{requestId}/poll` with `{ "poll_token": "..." }` about once per second. The answer is `{ "status": "pending" | "scanned" | "approved" | "denied" | "expired" }`.
3. The approving device opens the link (phone camera or in-app scanner) and calls `GET /auth/qr-login/{requestId}` with its access token. The first call moves the request from `pending` to `scanned` and binds it to that account; other accounts get `404` from then on. The answer shows `device` (the new device's user agent), `location` (its approximate GeoIP location, or `null`), `created_at` and `expires_at`.
4. The user confirms with `POST /auth/qr-login/{requestId}/approve` (body `{ "code": "..." }` when two-factor is on; the body may be omitted otherwise) or refuses with `POST /auth/qr-login/{requestId}/deny`. Both answer `204`.
5. The next poll of the new device after approval answers `status: approved` with `access_token`, `token_type` and `expires_in` and sets the refresh cookie, exactly like a successful `POST /auth/login`. The session belongs to the approving account and records the new device's user agent, IP and location. This happens once: the request is marked used in the same transaction that creates the session, and every later poll answers `expired`.

Rules:

- Requests expire after 2 minutes. `request_id` and `poll_token` are independent 32-byte random values (base64url); only their HMAC-SHA256 with `RECOVERY_PEPPER` is stored in `qr_login_requests`. Expired rows are deleted by the worker's recovery maintenance.
- The poll never reveals more than the status: an unknown `request_id`, a wrong `poll_token`, an expired request and a used one all answer `expired`. Only the device that holds `poll_token` can receive the session, so a photographed QR code is useless on its own.
- Approval is single use (`409 conflict` once approved or denied; `404 not_found` when unknown, expired, used or bound to another account). The approving session must be active and used within `RECOVERY_APPROVAL_MAX_AGE`, the same freshness rule as device-recovery approval; otherwise the answer is `403 forbidden`. With two-factor on and no `code` the answer is `401 two_factor_required` and the request stays approvable; a wrong code answers `400 invalid_credentials`. A recovery code is consumed together with the approval.
- If the approving session is revoked, goes idle or the password changes between approval and the poll, the poll answers `expired` and no session is created.
- Security events: `qr_login_approved` and `qr_login_denied` (with the new device's user agent and location) when the user decides, and `qr_login` (with `login_method: qr`) when the new device signs in. Approval sends the `security_alert` WebSocket event with type `qr_login_approved` to the account's sockets.

**Phishing.** A QR code can be relayed: an attacker can show a victim a QR code that belongs to the attacker's browser and ask them to "scan to verify". Approving it signs the attacker into the victim's account. Clients must therefore show the approving user the requesting `device` and `location` and ask for an explicit confirmation ("Sign in on Firefox on Windows near Berlin?") with a clear warning to approve only a sign-in they started themselves on a device in front of them. Never approve automatically after scanning. The 2-minute expiry keeps the window for a relayed code short, and every approval is visible in `GET /me/security-events` and in `GET /me/sessions`, where the session can be revoked.

## Recovery

New accounts receive a 160-bit base32 `recovery_key` once in the registration response. Only HMAC-SHA256(RECOVERY_PEPPER, normalized key) is stored. Keys accept case, spaces and hyphens. Treat RECOVERY_PEPPER as durable key material: losing or changing it invalidates existing recovery keys.

- `GET /me/recovery-key`: existence, creation time and any security lock deadline; never returns the key or hash.
- `POST /me/recovery-key`: current password and `code` if 2FA is enabled. Returns a new key once and immediately invalidates the previous key.
- `POST /auth/recover/key`: username, recovery_key, new_password and code when 2FA is enabled. Unknown users, wrong/consumed keys and wrong second factors all return the same 400 invalid_credentials; a valid key without a code on an account with 2FA returns 401 `two_factor_required` and the key stays unused. Recovery is rate limited by IP and by username and IP. A TOTP or unused 2FA backup code is mandatory when enabled.

A reset atomically replaces the password and recovery key, consumes a supplied 2FA backup code, revokes all existing sessions, invalidates outstanding email tokens and device recovery requests and records a password_reset security event. It preserves 2FA. The response sets a new session cookie and returns access_token plus the replacement recovery_key once. Existing WebSocket clients receive a best-effort security_notice and their old sessions close. Pending password/2FA logins are bound to their verified password and cannot create a session after it changes.

For 24 hours after recovery, creating/replacing recovery keys and setting up, confirming or removing 2FA return 409 conflict. The replacement key issued inside the reset transaction is the exception, so the account retains recovery protection. GET /me/security-events returns the owner's newest 50 events.

Existing users without a key receive recovery_setup_required=true from GET /me and must enroll using their current password and 2FA. The frontend prompts enrollment and also offers it in Security & devices. Legacy Shamir shares were never converted into keys; migration 000018 dropped them. The unused `recovery_tokens` table stays until a later release, so earlier releases keep working after a rollback. The old `/auth/recover` and `/auth/reset-password` endpoints are not part of the contract or routing. The Go production cutover is complete. Existing accounts that have not enrolled still need to create a recovery key using their current credentials; keep this enrollment path available. See [completed migration](migration-v1.md) and [Python retirement](python-retirement.md).

Migration 000012 is additive. Apply it before deploying the updated API. Keep recovery keys out of logs, browser storage, screenshots and analytics. A failed response after a committed reset may require signing in with the new password; the rotated key cannot be retrieved from the server.

## Configuration

`JWT_SECRET`, `ENCRYPTION_KEY`, `RECOVERY_PEPPER` (required), `COOKIE_SECURE`, `REFRESH_COOKIE_PATH`, `SESSION_CACHE_TTL`, `REFRESH_REUSE_GRACE`, `PASSWORD_HASH_CONCURRENCY`, `TRUSTED_PROXIES`; see `.env.example`.
