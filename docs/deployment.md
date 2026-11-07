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

## Audio calls (media server)

Calls run through one LiveKit node on the same host as the API (`livekit` service, profile `calls`); how to
enable, disable and rotate it is in [deploy-go.md](deploy-go.md#audio-calls), the ports, AWS checklist and
traffic budget in [calls.md](calls.md#deployment). There is no failover: a host failure or reboot ends every
call, and the clients have to join again.

### Resource budget on the t3a.large (2 vCPUs, 8 GiB, burstable)

Messaging and the stores come first; the media server gets a fixed share and admission control keeps it
inside it.

| Consumer | Memory | CPU |
| --- | --- | --- |
| Bundled stores (Elasticsearch 1 GiB heap, ScyllaDB 1 GiB, PostgreSQL, Redis, object storage) | about 3.5 GiB | most of the baseline |
| `api`, `worker`, nginx, the OS | about 1 GiB | |
| `livekit` (`CALLS_MEM_LIMIT`, `CALLS_CPUS`) | 1 GiB, hard limit | at most 1 vCPU, half the CPU weight of the other containers |
| Headroom | about 2.5 GiB | |

Measure before enabling calls instead of trusting this table: `docker stats --no-stream` and the CloudWatch
`CPUCreditBalance` during a normal day. A t3a.large earns credits for 30 % of both vCPUs (about 0.6 vCPU)
around the clock; above that it spends credits, and with none left it is throttled to that baseline, so
messaging slows down too. The media server's limits keep it from taking more than one vCPU, but sustained
calls still drain credits: watch the balance during the load test. With "unlimited" credit mode the host is
not throttled, and every hour above the baseline is billed instead.

`CALLS_MAX_ACTIVE_PARTICIPANTS` (admission control in the API) and `limit.bytes_per_sec` in
`deploy/go/livekit/livekit.yaml` (LiveKit refuses new participants above about 160 Mbit/s) are the brakes.
Set the first from the load test, below the point where CPU, packets/s or credits run out.

Docker publishes about 100 UDP ports for the media server. With Docker's userland proxy (the default), that
is one small `docker-proxy` process per port; outside traffic does not pass through them (iptables does the
forwarding). If their memory matters, set `"userland-proxy": false` in `/etc/docker/daemon.json` and restart
Docker in a maintenance window. Bridge networking costs some CPU per packet compared with host networking
(which LiveKit recommends for large nodes); if the load test shows the network stack (softirq) as the limit,
moving `livekit` to `network_mode: host` is the next step, which then needs the room API and metrics ports
bound to loopback and the API to reach LiveKit through the host.

### Monitoring and alerts

LiveKit's Prometheus metrics are on `127.0.0.1:${CALLS_METRICS_PORT}` (default 6789, loopback only); the
API's own call metrics are on its `/metrics`. Never route either publicly. Alert on:

| Signal | Source | Alert when |
| --- | --- | --- |
| CPU credits | CloudWatch `CPUCreditBalance` | it falls steadily during calls, or below 20 % of the maximum |
| CPU | CloudWatch `CPUUtilization`, `docker stats` | sustained above the baseline (30 %) for 15 minutes |
| Packets per second | CloudWatch `NetworkPacketsOut`/`NetworkPacketsIn` (per minute), LiveKit `livekit_node_packet_total{type="out"}` | above the rate measured as safe in the load test |
| Dropped packets, quality | LiveKit `livekit_node_packet_total{type="dropped"}`, `livekit_quality_score`, `livekit_forward_latency` | drops rising with load; median quality score falling below 3.5 |
| Egress | CloudWatch `NetworkOut`, the AWS bill (Budgets) | daily egress above the expected profile; monthly budget alert on data transfer |
| Join failures | the API's call metrics and logs (`503 calls_unavailable`, failed joins and grants); LiveKit `livekit_participant_total` and `livekit_room_total` next to the API's joined count | join failures above 5 % for 5 minutes, or LiveKit's participant count far from the API's |
| Media server up | `curl -fsS http://127.0.0.1:${CALLS_HTTP_PORT}/` from the host, container health | not `OK` for 2 minutes |
| Memory | `docker stats` for `livekit` | above 80 % of `CALLS_MEM_LIMIT` |

Logs and metrics must not carry media keys, security codes or key envelope bodies; LiveKit never sees them.
Keep LiveKit at `logging.level: info`: at `debug` it logs each room's TURN password. Its logs name rooms,
participant identities and client addresses; compose keeps at most 30 MB of them.

### Region outage

The host's region is the only one. If it fails, calls and messaging fail together; there is no second
media node to move to (deferred, see the plan). Users far from the region get higher latency; quality by
distance is recorded in the device checklist results.
