# Tests

`go test ./...` runs everything. Tests that need a store skip without its address. CI sets all of them (`.github/workflows/go.yml`) and runs with `-race`.

| Variable | Store | Isolation |
|---|---|---|
| `TEST_DATABASE_URL` | PostgreSQL (admin connection) | a new database per test, all migrations applied; chat ids start at random |
| `TEST_REDIS_URL` | Redis | a key prefix per test |
| `TEST_SCYLLA_HOSTS` | ScyllaDB | one keyspace per package run, dropped afterwards |
| `TEST_ELASTIC_URL` | Elasticsearch | an index alias per test, its indices deleted afterwards |
| `TEST_S3_*` | S3-compatible storage | a bucket per test |

The helpers are `internal/testenv`. CI starts ScyllaDB, RustFS and Elasticsearch with `deploy/ci/start-*.sh` and PostgreSQL and Redis as service containers.

**Containers:** #55 suggests `testcontainers-go`. The real stores run as containers in CI already, and locally the same variables point at a dev stack or single containers. Starting containers from the tests would add a Docker dependency to every test run and slow down the package runs that share a ScyllaDB keyspace, without testing anything more.

## What is covered

- **Domain logic:**
  - Permissions: messages, chats, groups, roles under locks.
  - Privacy rules: the directory, search, read receipts, blocks.
  - ID generation (`internal/ids`), paging (chat list cursor, history, media, search_after), the event consumer (crashes, retries, dead letters).
- **Integration:** every repository against real PostgreSQL, ScyllaDB, Redis and Elasticsearch, and the HTTP endpoints through the real router with real stores (`internal/httpapi/*_test.go`).
- **Contract:**
  - Every response the HTTP tests receive is validated against `api/openapi.yaml` (`internal/httpapi/contract_test.go`, JSON Schema 2020-12): a status the operation does not list, an unexpected body or a body that breaks its schema fails the test. `5xx` are exempt, since they are server failures.
  - Every WebSocket event the realtime tests receive is validated against `api/websocket/server-events.schema.json` (`internal/realtime/contract_test.go`).
  - `api/scripts/check.mjs` checks the examples and some negative cases.
- **WebSocket** (`internal/realtime`): delivery between two instances, removal from a chat, reconnect and catch-up, `ack` and `error` with `client_temp_id`, close codes, slow consumers.
- **Security:** the B1–B9 issues of the Python backend (#18–#21 and the ones the epic lists) have tests in the new code:

| Issue | Tests |
|---|---|
| secrets from the environment, weak secrets refused | `TestNewServiceRejectsWeakConfig`, `TestShortSecretRefused`, config tests |
| forged or foreign tokens | `TestAuthenticateRejectsForgedAndForeignTokens`, `TestForgedAndInvalidTokensAreRejected`, `TestProtectedRoutesNeedAToken` |
| membership on every action (the "chat 0" leaks) | message, chat, group and search tests: outsiders get `404` everywhere |
| no secrets or message content in logs | `TestLogsHoldNoCredentials`, `TestSecurityEventsHaveNoSecrets` |
| uploads checked by content (#18, B6) | `TestUploadsAreCheckedByContent`, `TestDetect`, `TestProcessImageRefusesBombsAndJunk`, `TestGIFFramesAreCountedBeforeDecoding`, `TestCleanName` |
| authenticated media (#18) | `TestUploadLinkAndAccess`, `TestUploadAndDownloadOverHTTP`, `TestAvatarsOverHTTP` |
| CORS, cookies, trusted proxies (#20) | `TestInsecureCookieAndPathOverride`, `TestClientIP`, router tests |
| WebSocket authorization (#21) | `TestWebSocketTickets`, gateway tests (membership, `reply_to`, no token in the URL) |
| rate limiting | `TestLoginRateLimit`, `TestLoginRateLimitSendsRetryAfter`, `TestLimiter*`, `TestTwoFactorChallengeAttemptsAreLimited` |

- **Load:** k6 scripts and how to run them in [load-testing.md](load-testing.md).
- **Migration from v1** (`cmd/migrate-v1/migrate_test.go`): a fixture database with the v1 schema (`testdata/v1_schema.sql`, from `server/database.py`) and files, migrated twice (the second run must change nothing). The TOTP decryption is checked against a token made by Python's `cryptography`.
