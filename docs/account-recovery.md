# Account recovery

Recovery keys, trusted-device approval, passkeys and optional verified email are independent methods.
An account does not need an email address. Recovery endpoints do not restore retired Shamir shares.

## Password recovery

A recovery key is shown once when created or rotated. Only its keyed hash is stored. Successful key,
device or email recovery rotates the key, changes the password, revokes every previous session and
invalidates outstanding email tokens and device approvals. The response creates a fresh session and
returns the replacement key once. Recovery method changes are blocked for 24 hours after a reset.
Enabled two-factor authentication still requires a current code or unused backup code. A request without
a code answers `401` `two_factor_required` once the other credentials are proven, without consuming them; see
[Missing second factor on other operations](auth.md#missing-second-factor-on-other-operations).

Device requests expire after ten minutes. An eligible logged-in session must select the matching
number from three choices and confirm with the account password and any enabled second factor; the
requesting browser displays that number. The `recovery_request` event carries a separate approval handle,
so signed-in devices can approve or deny but cannot poll or complete the request: only the requesting
browser holds that capability. A wrong number or denial
blocks new requests for fifteen minutes. At most three active requests are allowed. Completion checks
the approving session again, including revocation, inactivity and its pinned security credentials.
Request capabilities are random and only keyed hashes are persisted. Unknown accounts, blocked accounts
and requests over the cap store an ownerless decoy request with the same expiry and run the same
queries, so the start response and status polling are identical: `pending` until expiry, then
`expired`. A decoy never notifies anyone; approving or completing it fails like a request that was
never approved.

The approving device sees the requester's sanitised user agent and approximate location (city and country
from the offline DB-IP Lite database, `null` when unknown; see [GeoIP](geoip.md)), never its IP address
or network. IP geolocation by [DB-IP](https://db-ip.com).

## Passkeys

Set `RP_ID` to the frontend hostname and `RP_ORIGINS` to its exact allowed origins. Use HTTPS;
HTTP is accepted only for loopback development. Both settings empty disables the feature. Development
Compose defaults to `localhost` and `http://localhost:5173`; override both together for another origin.

Registration requires the password and any enabled second factor. Challenges expire after five
minutes and registration challenges are bound to the creating session. Registration requires a
discoverable credential and user verification. Login validates the origin, RP, challenge, credential,
user handle and user verification with the WebAuthn library. Credential updates and new sessions are
committed together. A counter clone warning rejects login. Stored credential records are encrypted.
Deleting the final passkey requires the password; the user row is locked before passkeys are counted,
so concurrent deletes cannot remove the last one without that check. The recovery delay also applies to
changes. Failed passkey logins have their own per-IP limit and do not use the password-login budget.

## Optional email

Configure `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, and `RECOVERY_WEB_URL` in both API and worker.
Set `SMTP_USERNAME` and `SMTP_PASSWORD` when the provider requires authentication. The web URL
must be the frontend HTTPS origin. `SMTP_TLS_MODE=starttls` requires advertised STARTTLS and never
falls back to plaintext; `tls` uses implicit TLS. `plain` is restricted to a loopback test server.

Adding, replacing or removing an address requires the password and any enabled second factor.
An address must be verified before it can reset a password. Changing it invalidates old tokens.
Adding an address always gets the same response; only verified addresses are unique. If another account
has already verified the address, no verification mail is sent and verification fails with the generic
invalid-token error. At most three reset links per account are sent per hour (counted by the worker,
so the public response does not change) and a new reset link invalidates older unused ones.
Verification and reset tokens expire after thirty minutes, are bound to the address and are single use.
The public reset request always returns the same accepted message, including when mail is disabled
or the account/address is unknown. The worker resolves identifiers asynchronously from an encrypted
queue; no recipient, token, link or SMTP error text is logged. Jobs expire after one day and have at
most five delivery attempts. Monitor failed delivery counts and provider health before enabling email
as a recovery option for users.

Expired recovery requests, WebAuthn challenges, email tokens and abandoned mail jobs are deleted by
worker maintenance. Security activity records contain method/device metadata, never secret values.

## Migration and rollback

Migration 18 removes `user_recovery_shares`; the preceding application already uses recovery keys.
Its down migration restores the empty table shape, not historical secrets. Retain verified database
backups under the deployment retention policy if historical restoration is required. Migrations 19
and 20 add device requests, passkeys and email. Migration 21 keeps the unused legacy `recovery_tokens`
table, because earlier releases still write it and must keep working after a rollback; a later release drops
it. Keep new tables during an application-only rollback;
running their down migrations deletes the newly registered methods. Removed `/auth/recover` and
`/auth/reset-password` endpoints are absent from the contract and routing. Update clients before
enforcing a minimum contract version that excludes older clients.

Migration 22 replaces the network hint of recovery requests (`ip_hint`) with a `location` snapshot and adds
`location` to sessions and security events; its down migration restores the empty column shapes.

Migration 24 adds the approval handle (`approval_hash`) of device recovery requests.
