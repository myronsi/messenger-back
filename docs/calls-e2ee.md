# ADR: end-to-end encryption of audio calls

Status: accepted for implementation, 2026-11-07. Owner: project owner. Scope: Milestone 1 of the audio calls plan
([calls.md](calls.md)).

## Context

Calls run through a self-hosted LiveKit SFU on the existing host. The SFU forwards media and must never be able to
listen. LiveKit can encrypt media frames in the browser (insertable streams / `RTCRtpScriptTransform`), but it does
not decide which key each participant uses, who may hold it, or how a participant proves who they are. That is the
application's job.

Constraints from the plan:

- No home-made cryptographic primitives or protocols; use maintained, standard building blocks.
- The server may relay encrypted key material but never sees a plaintext media key.
- Removed participants must not decrypt future audio; late joiners must not decrypt past audio.
- A human-comparable security code bound to the real identity keys and the membership.
- No external cryptographic review is budgeted (decision 2026-11-07). The design therefore uses only standard,
  widely deployed constructions and user-facing text must not claim an audit.
- Web only. Chat messages are not end-to-end encrypted, so there is no existing device-key infrastructure.

## Options considered

| Option | Result |
|---|---|
| LiveKit shared key (one key per room) | Rejected: every key change must reach everyone, no sender authentication, the server could hand out the key if it ever saw it. |
| MLS (RFC 9420) with `ts-mls` or OpenMLS compiled to WASM | Rejected for now: `ts-mls` has no published audit and is young; OpenMLS in WASM adds a large bundle and a delivery-service design of its own. Revisit when a reviewed browser MLS library exists. |
| Olm/vodozemac pairwise channels (as MatrixRTC does) | Rejected: needs the full Olm session machinery and one-time-key server; heavy for a web-only client without existing E2EE. |
| **Sender keys per participant and epoch, sealed per recipient with HPKE (RFC 9180) Auth mode** | **Chosen.** The same sender-key pattern MatrixRTC uses with LiveKit, but with HPKE as the standard, authenticated pairwise sealing. |

## Decision

### Building blocks

- **LiveKit** server `v1.13.10`, `livekit-client` `2.23.0` (frame encryption: AES-GCM per frame, keyed per
  participant, key index per epoch). The client uses LiveKit's per-participant key provider
  (`sharedKey: false`, no ratcheting).
- **HPKE** (RFC 9180) through `@hpke/core` `1.10.0`, which runs on WebCrypto: KEM `DHKEM(P-256, HKDF-SHA256)`,
  KDF `HKDF-SHA256`, AEAD `AES-128-GCM`, **mode Auth** (sender authenticated by its static key).
- **WebCrypto** for key generation (`ECDH P-256`, non-extractable private keys kept in IndexedDB) and SHA-256.

### Identities

- Every signed-in session (the backend's unit of "device") creates one call identity key pair on first use and
  registers the public key with `PUT /me/call-identity`. The private key never leaves the browser and cannot be
  exported (`extractable: false`).
- A new session, cleared storage or account recovery means a new identity key. The server keeps one public key per
  session and forgets it when the session ends. Old media keys are never recoverable: they only existed in memory.
- Clients remember the identity key they last saw for each `(user, session)` (trust on first use) and show a
  warning when it changes.

### Epochs and keys

- The server owns the roster: the list of joined `(user, session, identity key)` of a call and an **epoch**
  number. The epoch increases on every join, leave, removal, reconnect with a new session and identity change.
- On every epoch, each joined participant draws a fresh random 256-bit **sender key** and seals it for every other
  roster entry with HPKE Auth: recipient = the entry's identity key, sender = its own identity key,
  `info = "messenger-call-key/v1"`, `aad = call_id | epoch | sender_session_id | recipient_session_id`.
- Envelopes go to `POST /calls/{call_id}/keys`. The server checks that sender and recipients are on the roster of
  that epoch, stores them until the call ends and pushes them as `call_keys` events. The server sees only
  ciphertext.
- A receiver opens an envelope only if the AAD matches the current roster, the sender's identity key is the one on
  the roster, and the epoch is the current one (or the previous one during a short switch-over). It installs the
  key for that sender with LiveKit key index `epoch mod 16`.
- A participant **publishes audio only with the key of the current epoch** and only after its own envelopes for that
  epoch were accepted. A newcomer only ever gets keys of epochs that started after it joined, so it cannot decrypt
  older audio. A removed participant is not a recipient of the next epoch, and every sender switches to the new key,
  so it cannot decrypt later audio.
- If encryption is not supported by the browser (`isE2EESupported()` false), the client does not join and shows an
  upgrade message. There is no fallback to transport-only encryption.

### Security code

- Every client computes SHA-256 over the UTF-8 string
  `messenger-call-code/v1|<call_id>|<epoch>|<entries>`, where each entry is
  `<user_id>|<session_id>|<hex SHA-256 of the raw uncompressed identity public key>`, the entries are sorted as
  strings and joined with `|`.
- The first 30 decimal digits (taken from the hash as six 5-digit groups, each `uint32 mod 100000`) are shown.
  Everyone in the same epoch with the same view of identities sees the same code. If the server substituted an
  identity key for someone, the codes differ.
- The code changes with each epoch, and the "verified" mark is cleared when it does.
- The UI explains: compare the code by voice or in person; seeing the code is not proof of identity. Encryption
  protects the audio, not who called whom and when, not a compromised device, and not malicious code served by a
  compromised web origin.

### What the server stores

Call metadata (who, when, outcome) for 90 days, identity public keys per session, and opaque key envelopes until the
call ends. Never audio, never plaintext media keys, never security codes. Envelope bodies and keys are never
logged.

## Consequences

- One extra round of envelopes per epoch: with 10 participants that is at most 90 small envelopes per change.
  Group calls are capped at 10, which keeps this cheap.
- A short audio gap (well under a second) can occur at each membership change while the new keys arrive.
- The trust root is the web origin: whoever controls the served JavaScript controls the keys. This is inherent to
  web E2EE and is stated in the UI and the docs.
- No external review: the design stays within standard constructions (HPKE Auth, AES-GCM frame encryption, sender
  keys). An internal review against the threat model below is part of the release checklist in [calls.md](calls.md).

## Threat model (summary)

| Attacker | Protected | Not protected |
|---|---|---|
| Curious or compromised SFU / TURN | Audio content | Who talks when (packet timing), IP addresses |
| Compromised API server | Audio content of calls whose identity keys were verified by code comparison | Metadata; it can add a fake device, which shows up in the roster and changes the code |
| Removed participant | Audio after removal | Audio heard before removal |
| Late joiner | Audio before joining | — |
| Compromised browser or web origin | Nothing | Everything |

## Browser matrix

Encryption needs encoded transforms: Chrome / Edge (insertable streams), Firefox 117+ and Safari 15.4+
(`RTCRtpScriptTransform`). Automated proof in CI-like runs uses Chromium with fake audio devices. Physical-device
checks are listed in [calls.md](calls.md#physical-device-checklist) and are done by the project owner.

| Browser | Expected | Verified by |
|---|---|---|
| Chrome / Edge desktop | supported | automated tests (Chromium) |
| Firefox desktop | supported | manual |
| Safari macOS | supported | manual |
| Android Chrome / installed PWA | supported | manual, physical device |
| iOS Safari / home-screen PWA | supported (iOS 15.4+) | manual, physical device |
| Capacitor native shell | out of scope, calls hidden | — |
