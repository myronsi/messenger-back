# Account recovery

Recovery keys, trusted-device approval, passkeys and optional verified email are independent methods.
An account does not need an email address. Recovery endpoints do not restore retired Shamir shares.

## Password recovery

A recovery key is shown once when created or rotated. Only its keyed hash is stored. Successful key,
device or email recovery rotates the key, changes the password, revokes every previous session and
invalidates outstanding email tokens and device approvals. The response creates a fresh session and
returns the replacement key once. Recovery method changes are blocked for 24 hours after a reset.
Enabled two-factor authentication still requires a current code or unused backup code.

Device requests expire after ten minutes. An eligible logged-in session must select the matching
number from three choices; the requesting browser displays that number. A wrong number or denial
blocks new requests for fifteen minutes. At most three active requests are allowed. Completion checks
the approving session again, including revocation, inactivity and its pinned security credentials.
Request capabilities are random and only keyed hashes are persisted. Unknown accounts receive the
same start-response shape and polling expires without revealing account existence.

## Passkeys

Set `RP_ID` to the frontend hostname and `RP_ORIGINS` to its exact allowed origins. Use HTTPS;
HTTP is accepted only for loopback development. Both settings empty disables the feature. Development
Compose defaults to `localhost` and `http://localhost:5173`; override both together for another origin.

Registration requires the password and any enabled second factor. Challenges expire after five
minutes and registration challenges are bound to the creating session. Registration requires a
discoverable credential and user verification. Login validates the origin, RP, challenge, credential,
user handle and user verification with the WebAuthn library. Credential updates and new sessions are
committed together. A counter clone warning rejects login. Stored credential records are encrypted.
Deleting the final passkey requires the password; the recovery delay also applies to changes.

## Optional email

Configure `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, and `RECOVERY_WEB_URL` in both API and worker.
Set `SMTP_USERNAME` and `SMTP_PASSWORD` when the provider requires authentication. The web URL
must be the frontend HTTPS origin. `SMTP_TLS_MODE=starttls` requires advertised STARTTLS and never
falls back to plaintext; `tls` uses implicit TLS. `plain` is restricted to a loopback test server.

Adding, replacing or removing an address requires the password and any enabled second factor.
An address must be verified before it can reset a password. Changing it invalidates old tokens.
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
and 20 add device requests, passkeys and email. Keep new tables during an application-only rollback;
running their down migrations deletes the newly registered methods. Removed `/auth/recover` and
`/auth/reset-password` endpoints are absent from the contract and routing. Update clients before
enforcing a minimum contract version that excludes older clients.
