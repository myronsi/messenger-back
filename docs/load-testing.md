# Load tests

k6 scripts in `loadtest/` measure the targets of #55 against a running stack. They need users with tokens, which `loadtest/seed` creates; run everything against a test or staging environment, never production. The seed refuses to run unless `APP_ENV=test`, or `-allow-env <env>` names the environment it is meant for (staging, which runs with `APP_ENV=production`, needs `-allow-env production` and a deliberate shell).

| Script | Measures | Target |
|---|---|---|
| `websockets.js` | idle WebSockets held open | 10 000 per API instance, none closed early |
| `messages.js` | send → delivery to the partner over WebSockets | p99 under 150 ms at 500 messages/s |
| `reads.js` | history pages, searches | p99 under 50 ms (history), under 300 ms (search) |

## Running

1. Start the stack (`make up`, or staging), with the API's environment in the shell.
2. Seed users, which share direct chats in pairs:

   ```sh
   LOADTEST_PASSWORD=<a long random string> go run ./loadtest/seed -users 1000 -out loadtest/users.json
   ```

   The seed registers `load_00000…` (logging in when they exist, with the same password). Each user comes from an address of its own, so the per-address limits of registration and login are respected rather than switched off. The file holds access tokens and is readable for its owner only; delete it after the run.
3. Run a script (k6 opens `users.json` next to the scripts unless `-e USERS=<absolute path>`):

   ```sh
   k6 run -e BASE_URL=http://localhost:8080/api/v2 -e VUS=1000 -e HOLD=300 loadtest/websockets.js
   ```

   Access tokens live for 15 minutes (`auth.AccessTokenTTL`): seed again right before every run, and keep runs shorter than that.

Every script fails when it measured nothing: a ticket that could not be had, a socket that closed early, fewer `hello` events than sockets, or (in `messages.js`) fewer than 90 % of the sent messages delivered.

## What limits the numbers

- **Per-user send limit:** 30 messages per 10 s, bursts of 20 (shared by WebSocket and HTTP sends). For `messages.js`, keep `RATE / VUS` below 3, for example 400 users for 500 messages/s.
- **Per-user search limit:** 60 per minute, bursts of 20. `reads.js` picks a random user for every request; seed enough users that `RATE / 10` searches per second stay below one per user per second.
- **Sockets per client machine:** the load generator needs enough ephemeral ports and file descriptors for 10 000 sockets (`ulimit -n`); spread larger runs over several k6 instances.
- **Slow consumers:** a client that falls behind its send buffer (`WS_SEND_BUFFER`, 256 events) is closed with `1013`. That shows up as `ws_closed_early`.

## Results

The targets are still to be measured on staging with migrated data before the switch (#56). Record each run here: date, commit, environment (instances, CPU, memory), parameters and the k6 summary of the thresholds.

| Date | Run | Environment | Result |
|---|---|---|---|
| 2026-10-10 | smoke of all three scripts (40 users) | one API and one worker on a laptop; PostgreSQL, Redis, ScyllaDB and Elasticsearch in local containers; k6 2.3.0 | 40/40 sockets held; delivery p99 69 ms at 40 messages/s (ack p99 75 ms); history p99 23 ms and search p99 43 ms at 20 + 2 requests/s; no errors |

The smoke run only shows that the scripts and the stack work end to end; its load is far below the targets.
