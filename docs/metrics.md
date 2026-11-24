# Metrics

`api` and `worker` serve Prometheus metrics on `/metrics` (never route it publicly, see [deployment.md](deployment.md)). This page lists the performance metrics and what to look at first; the HTTP, call, moderation and media metrics are described in their `Help` texts.

## Realtime

| Metric | Labels | Look at |
|---|---|---|
| `messenger_websocket_connections_open` | | open sockets on this instance |
| `messenger_websocket_event_duration_seconds` | `type`, `outcome` | p99 of `type="message"` against the 150 ms target of [load-testing.md](load-testing.md); `outcome` is `ack`, `handled` (typing), `error`, `rate_limited`, `restricted` or `dropped` |
| `messenger_websocket_slow_closes_total` | | clients closed with 1013 because they fell behind their send buffer |
| `messenger_fanout_recipients` | | recipients per published event; large values are big groups |
| `messenger_fanout_publish_duration_seconds` | | publishing one event to all recipients on Redis |
| `messenger_fanout_render_duration_seconds` | `event` | rendering a `message` or `edit` event for every recipient (sender views, attachments, replies) |

## Stores

| Metric | Labels | Look at |
|---|---|---|
| `messenger_store_operation_duration_seconds` | `store`, `op` | slowest `op` per store: PostgreSQL by sqlc query name (`sql_<verb>` for queries outside sqlc), Redis by command (`pipeline` for pipelines; blocking reads are left out), ScyllaDB by statement and table (`select_messages`, `batch`) |
| `messenger_store_errors_total` | `store` | failed operations; PostgreSQL and ScyllaDB not-found results and cancelled requests do not count |
| `messenger_postgres_pool_acquired_connections`, `_idle_connections`, `_max_connections` | | acquired close to max means `POSTGRES_MAX_CONNS` is the limit |
| `messenger_postgres_pool_empty_acquires_total` | | acquires that had to wait for a free connection; a rising rate means the pool is too small |
| `messenger_postgres_pool_acquire_seconds_total` | | total waiting time for connections |

## Worker

| Metric | Labels | Look at |
|---|---|---|
| `messenger_stream_lag` | `stream`, `group` | entries the consumer group has not received yet; growing lag means the worker falls behind |
| `messenger_stream_pending` | `stream`, `group` | entries received but not acknowledged |
| `messenger_stream_backlog_read_failed` | | 1 when the last scrape could not read the streams |
| `messenger_events_handled_total` | `group`, `outcome` | alert on `dead_letter` |

## Example queries

```promql
histogram_quantile(0.99, sum by (le) (rate(messenger_websocket_event_duration_seconds_bucket{type="message"}[5m])))
topk(10, histogram_quantile(0.99, sum by (store, op, le) (rate(messenger_store_operation_duration_seconds_bucket[5m]))))
rate(messenger_postgres_pool_empty_acquires_total[5m])
max by (stream, group) (messenger_stream_lag)
```
