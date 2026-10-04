# API compatibility rules

The API contract (`api/openapi.yaml` and `api/websocket/`) is versioned separately from the apps (see `docs/versioning.md`). The contract version is `info.version`, published as `@myronsi/messenger-api`.

## Allowed in a MINOR version

- New endpoints.
- New **optional** request fields and query parameters.
- New response fields.
- New WebSocket event types.
- New enum values. This is only non-breaking because the frontend must handle unknown values with a fallback (messenger-front MSGC-23), and unknown WebSocket events and fields must be ignored.

## Breaking: needs a new MAJOR with a new `/api/vN`, or expand–contract

- Removing or renaming endpoints, fields or events.
- Making an optional field required.
- Changing a type, format or meaning.
- New required request fields.
- Stricter validation (smaller maximum, new pattern, …).

## Version bumps

| Change | Required bump |
| --- | --- |
| Breaking | MAJOR (and `servers[0].url` becomes `/api/v<MAJOR>`) |
| Anything else that changes the API | MINOR |
| Documentation only (descriptions, examples) | PATCH |

`info.version` and `api/package.json` must be equal. While the baseline is a pre-release (`2.0.0-alpha.N`) any change is allowed, but the version must increase.

## Removing something within a MAJOR (expand–contract)

1. Add the replacement (MINOR), mark the old part `deprecated: true` in the spec, and send `Deprecation` / `Sunset` headers.
2. The frontend switches to the replacement and is released.
3. When the metric `messenger_client_api_requests_total` shows no more clients using the old contract version, raise `MIN_CLIENT_API_VERSION` (and mention it in the release notes).
4. Remove the old part in the next MAJOR (or after the sunset date).

## Checks on pull requests that change `api/`

`.github/workflows/api-ci.yml`:

| Check | How |
| --- | --- |
| Lint | `redocly lint` (`api/redocly.yaml`) |
| Package version equals `info.version`; WebSocket examples match their schemas | `npm run check` |
| Breaking changes and version bump | `api/scripts/check-version-bump.mjs` uses [oasdiff](https://github.com/oasdiff/oasdiff) to compare the REST contract and the WebSocket schemas (as a synthetic OpenAPI document: server events are responses, client events are request bodies) with the last released contract (the latest stable `@myronsi/messenger-api` on npm; before the first stable release the contract on the base branch). A pre-release version is also compared with the contract on the base branch, so each change to it needs a higher pre-release version even when a stable release exists. `websocket.md` and the example payloads count as documentation (PATCH). A removed event or field without a MAJOR bump fails; so does an addition without a MINOR bump or a docs change without a PATCH bump |
| Generated Go code is current | `go generate ./... && git diff --exit-code`, once the repository has a `go.mod` |
| Label | `labeler.yml` adds `api-change` to every PR that touches `api/**` |

Run the same checks locally:

```sh
cd api && npm ci && npm run lint && npm run check
OASDIFF=oasdiff node scripts/check-version-bump.mjs <last released contract dir: unpacked package dist/ or an api/ checkout> .
```
