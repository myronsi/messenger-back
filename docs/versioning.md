# Versioning

Frontend and backend use Semantic Versioning independently. Their application version numbers need not match. Compatibility is decided by the API contract, the frontend's exact `@myronsi/messenger-api` pin, and the backend's minimum client API version.

## Version numbers

| Version | Source | Meaning |
| --- | --- | --- |
| Backend application | Git release tag/manifest; embedded in `internal/version` at build time | Identifies the Go application image |
| Frontend application | Frontend Git tag and `package.json` | Identifies the client build |
| API contract | `api/openapi.yaml` and published `@myronsi/messenger-api` | MAJOR changes the API base; MINOR adds compatible behavior; PATCH fixes/documents the contract |

Release-please manages application release PRs and tags. Go image builds set `VERSION` and `COMMIT`; runtime metadata reports the backend version and commit through `GET /api/v2/meta`. See [API compatibility](api-compatibility.md) and [contract publishing](../api/README.md).

## Runtime compatibility

Clients send `X-Client-Version` and `X-Client-Api-Version`. The current Go version middleware:

- Allows a client contract with the same MAJOR as the backend API and SemVer precedence at least `MIN_CLIENT_API_VERSION`.
- Returns `426 client_outdated` for an unsupported contract and `400 invalid_client_version` for a malformed version.
- Allows requests without a version header; authentication and authorization still apply separately.
- Exempts the API `/meta` and `/ws` endpoints and `/healthz`, `/readyz`, `/metrics` from version gating. Keep operational endpoints behind the deployment's access controls.
- Reports contract/minimum versions through WebSocket `hello`, because browser WebSocket requests cannot set these headers.

`/api/v2/meta` returns `backend_version`, `commit`, `api_version`, and `min_client_api_version`, with a 60-second public cache lifetime. The retired Python `/version` endpoint is not the current metadata interface. A newer backend contract alone does not mean the browser has a new frontend build; frontend `version.json` identifies that separately.

## Release lineage and migration

Python application releases used `0.x`; they are historical and must not be redeployed. Go application releases use `1.x` and later, with the v2 API. The cutover is complete; the old milestone roadmap is not a list of pending migration work. See [completed cutover](switchover.md) and [Python retirement](python-retirement.md).

Use Conventional Commit titles and squash merges. `feat`, `fix`, `perf` and breaking-change metadata feed release notes; release-please opens the release PR. A deliberate `Release-As` footer selects a future application version when necessary. It does not change the API contract or require the frontend's application version to match. See [releases](releasing.md).
