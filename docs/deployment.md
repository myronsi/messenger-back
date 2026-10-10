# Deployment notes (production)

`compose.dev.yaml` is a development stack with one node per store, no TLS and throwaway data. Do not use
it in production. This page lists what production needs; the application settings are in
[go-backend.md](go-backend.md) and `.env.example`. How releases are deployed (the `deploy/go` stack,
staging and production) is in [deploy-go.md](deploy-go.md).

## Application

- Run several `api` replicas behind a load balancer and point its health check at `/readyz`. Set
  `HTTP_DRAIN_DELAY` (for example `5s`) so a stopping replica fails `/readyz` before the listener closes,
  and `HTTP_SHUTDOWN_TIMEOUT` above your slowest request.
- `/metrics` and `/healthz` of `api` and `worker` must not be reachable from the internet.
- Set `APP_ENV=production` (adds HSTS and requires `https` CORS origins).
- Inject `JWT_SECRET`, `ENCRYPTION_KEY`, `RECOVERY_PEPPER` and the store credentials from a secret
  manager, not from a file in the repository. Never reuse a development value.
- Run migrations as a deployment step (`make migrate`, or golang-migrate in a job) before the new
  version starts, not from the application.

## Data stores

### ScyllaDB (messages)

- **3 nodes** across 3 racks or zones, keyspace with `NetworkTopologyStrategy` and **replication factor 3**
  per datacenter. The application reads and writes at **`LOCAL_QUORUM`**, which survives one node down.
- Use the production configuration (not `--developer-mode`, not `--overprovisioned`), local NVMe storage
  and the recommended OS tuning (`scylla_setup`).
- Run repairs on a schedule (Scylla Manager).
- Authentication and TLS between clients and nodes, and between nodes.

### Elasticsearch (search)

- **At least 3 nodes** (or a managed service), with replicas of at least 1 for every index, so losing a
  node keeps the index available. Replace the development template (`number_of_replicas: 0`).
- TLS on HTTP and transport, API keys instead of the `elastic` superuser.
- **Snapshots** to an S3-compatible repository with a snapshot lifecycle policy (for example daily,
  kept for 14 days).
- The index is derived data and is meant to be rebuildable from ScyllaDB, but a rebuild takes time
  proportional to the number of messages, so keep snapshots.

### Redis

- A **primary with at least one replica and Sentinel** (three Sentinels), or a managed service with
  automatic failover. Enable AOF (`appendonly yes`) and require a password and TLS.
- Treat Redis as a cache and coordination store: set `maxmemory` with headroom, keep the eviction policy
  `noeviction` and alert on memory use (the reasons and a sizing estimate are in [redis.md](redis.md)).
- Redis Cluster is not supported; the application expects one primary.

### PostgreSQL (accounts and metadata)

- Continuous WAL archiving plus periodic base backups for **point-in-time recovery** (pgBackRest or
  WAL-G, or the managed service's PITR), stored off the database host.
- A streaming replica for failover; backups are not a substitute for it.

### Object storage (media)

- A managed S3-compatible service with versioning and a lifecycle policy. Replicate to a second region or
  account if the media must survive losing the first.

## Backups and restore tests

A backup that was never restored is not a backup. Test every store, on a schedule, in an environment
that is not production.

| Store | Backup | Restore test |
| --- | --- | --- |
| PostgreSQL | base backup + WAL archive (PITR) | monthly: restore to a chosen timestamp, run the migrations check and a smoke test |
| ScyllaDB | `nodetool snapshot` per node via Scylla Manager, kept off the nodes | quarterly: restore a snapshot into a fresh cluster and compare row counts of the main tables |
| Elasticsearch | snapshot lifecycle policy to an object store | quarterly: restore into a scratch cluster and run a search; also rehearse a rebuild from ScyllaDB |
| Redis | AOF/RDB copied off the host | quarterly: start from the copy and check that the application works with it |
| Object storage | versioning plus replication or backup | quarterly: restore a sample of objects and verify their checksums |

Write down the recovery point and recovery time you aim for and measure them during the tests.
