# @myronsi/messenger-api

The API contract of the Messenger backend, published to npm so that backend and frontend are built from the same source.

```
api/
├─ openapi.yaml          REST contract (OpenAPI 3.1); info.version = package version
├─ websocket/            JSON Schema per event + examples; websocket.md describes the protocol
├─ oapi-codegen.yaml     Go server generation config
├─ CHANGELOG.md          contract changes only
├─ package.json          "name": "@myronsi/messenger-api"
└─ dist/                 generated (not committed)
   ├─ openapi.yaml
   ├─ schema.d.ts        REST types (openapi-typescript)
   ├─ ws-events.d.ts     WebSocket event types (from the JSON Schemas)
   ├─ ws-events.schema.json  bundled JSON Schemas (used by the breaking-change check)
   ├─ ws-docs.json       websocket.md and the examples (used by the PATCH check)
   └─ index.js           export const API_VERSION = "2.0.0-alpha.2"
```

## Use in the frontend

```sh
npm install @myronsi/messenger-api
```

```ts
import { API_VERSION, type paths, type ServerEvent } from "@myronsi/messenger-api";
type Chats = paths["/chats"]["get"]["responses"]["200"]["content"]["application/json"];
```

Send `API_VERSION` as `X-Client-Api-Version`. Dist-tags: `latest` is the last stable contract, `next` is the contract of the latest `master`, and pre-releases of a version (`2.0.0-alpha.N`) use `alpha`.

## Working on the contract

```sh
cd api
npm ci
npm run lint    # Redocly lint
npm run check   # package version == info.version; WebSocket examples match their schemas
npm run build   # dist/
```

Change the contract first, in the same PR as the code that implements it. Bump `info.version` **and** `package.json` `version` together (CI fails if they differ) as required by `docs/api-compatibility.md`; add an entry to `CHANGELOG.md`. Pull requests that touch `api/` get the `api-change` label, run the lint, the breaking-change check against the last released contract and the version-bump check (`.github/workflows/api-ci.yml`).

## Go server

The Go server code is generated from `openapi.yaml` with [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) (strict server) in the same PR as a contract change. The configuration is `oapi-codegen.yaml`, the output is `internal/httpapi/api.gen.go`, and the `go:generate` directive lives in `internal/httpapi/generate.go`:

```sh
make generate   # go generate ./...
```

When an operation is added to the contract, the build fails until it is added to `internal/httpapi/unimplemented.go` (which answers 501) or to a real implementation. CI runs `go generate ./... && git diff --exit-code`, so a stale generated file fails the build.
## Publishing

See `docs/releasing.md`: `next` on every merge to `master` that changes `api/`, the stable version after a backend release when `info.version` is not on npm yet. Both use `npm publish` with Trusted Publishing, which also attaches provenance.
