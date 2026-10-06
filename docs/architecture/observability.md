# Observability

Silo emits structured **logs** and distributed **traces** via OpenTelemetry (OTLP),
in addition to the existing stderr and `opslog` database pipeline. The feature is
**opt-in and default-off**: with no `OTEL_*` / `SILO_OTEL_ENABLED` configuration the
server behaves exactly as before (stderr + `opslog` only).

**Metrics are not part of OpenTelemetry here.** They remain on Prometheus
(`client_golang`, the `/metrics` endpoint, and instruments registered by their owning packages). See [Metrics](#metrics-stay-on-prometheus) below.

## Enabling it

Telemetry turns on when **either** `SILO_OTEL_ENABLED` is truthy **or**
`OTEL_EXPORTER_OTLP_ENDPOINT` is set.

| Variable | Purpose | Default |
| --- | --- | --- |
| `SILO_OTEL_ENABLED` | Master gate (`1`/`true`/`yes`/`on`). | off |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector endpoint; also implicitly enables telemetry. | — |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` (default) or `http/protobuf`. | `grpc` |
| `OTEL_SERVICE_NAME` | `service.name` resource attribute. | `silo-server` |
| `OTEL_SERVICE_VERSION` | `service.version` resource attribute. | unset |
| `OTEL_TRACES_SAMPLER` | `always_on`, `always_off`, `traceidratio`, `parentbased_always_on`, `parentbased_always_off`, or `parentbased_traceidratio`. Unsupported values (e.g. `jaeger_remote`) fall back to the default. | `parentbased_traceidratio` |
| `OTEL_TRACES_SAMPLER_ARG` | Trace-id ratio for the ratio-based samplers (0–1; clamps >1 to 1). | `0.01` |

The node identity is attached as the `service.instance.id` resource attribute, so
multiple Silo nodes sharing one `service.name` stay distinguishable in the backend.

All other `OTEL_EXPORTER_OTLP_*` knobs (headers, TLS, per-signal endpoints) are read
directly from the environment by the OTLP exporters — the environment is the single
source of truth for exporter wiring.

The endpoint/exporter connection is lazy and non-blocking: an unreachable collector
does **not** delay or crash startup. Setup failure is also non-fatal: if the `OTEL_*`
environment is malformed (e.g. an unparseable endpoint URL), the server logs an error
and keeps running with telemetry disabled rather than crash-looping.

## Architecture

The bootstrap lives in `internal/telemetry` (`Setup` in `telemetry.go`). When enabled
it builds one shared `resource.Resource`, a `TracerProvider` (sampler per
`OTEL_TRACES_SAMPLER`, batched OTLP exporter), a `LoggerProvider` (batched OTLP exporter), and the W3C
`TraceContext` propagator. Shutdown is deferred in `cmd/silo/main.go` with a
flush timeout so buffered spans/logs drain on exit.

### Log handler chain

Logs are bridged, not rerouted. The OTel `otelslog` handler is added to the existing
`slog` handler chain via **fan-out**, so every record still reaches stderr and the
`opslog` DB/admin-stream pipeline unchanged:

```
slog.<Level>Context(ctx, …)
  → opslog.Handler            (DB capture + admin stream, when level ≥ capture)
    → logfilter.Handler       (drops quieted subsystem prefixes)
      → slog.MultiHandler
        ├── stderr (json/text)
        └── otelslog → OTLP    (level-gated + best-effort)
```

Three properties matter:

- **Level-gated.** `slog.MultiHandler.Enabled` ORs its children, so the OTel branch is
  wrapped in a level gate bound to the shared `LevelVar`; console and OTLP share one
  verbosity knob. Without this, Debug records would be built and exported even at
  `log_level=info`.
- **Best-effort.** The OTel branch never propagates an export error, so a failing
  collector cannot break the console or DB branches.
- **Redacted.** The whole fan-out is wrapped in `internal/logredact`, so console and OTLP
  emit secret-masked output. Redaction is key-based (`password`, `secret`, `token`,
  `api_key`, `authorization`, `cookie`, …); a matching attribute's value becomes
  `[REDACTED]`, including attrs bound via `.With(...)` and nested groups. The marker list
  is shared with the opslog DB path (`opslog.shouldRedact` → `logredact.SecretKey`) so all
  sinks agree. Limitation: values are not scanned, so a secret embedded in a free-text
  message or under a non-secret key is not caught.

### Database log persistence

The operational log (`opslog.Handler`, stream `app`) and the activity log
(`activitylog.NewMiddleware`, stream `audit`) run on the goroutine that logs, so their
writers never block and never log. Neither Redis nor Postgres is on that path.

- Each node queues entries in its own bounded in-memory buffer
  (`logstream.Buffer`, 10,000 entries per stream). A consumer on that node
  (`logstream.Drain`) inserts them into Postgres in batches of 100 or every two seconds.
  Each insert attempt has a ten-second deadline.
- While Postgres is unreachable or refuses work for a passing reason (connection
  errors, timeouts, restart, failover including writes refused as read-only,
  overload), the consumer retries the batch with backoff from one to ten seconds, and
  new entries wait in the buffer. When the node is stopping, each batch gets one
  attempt. A connection lost after an insert commits can make the retry insert that
  batch twice.
- Log fields carry client input and file system data (request paths, User-Agent
  headers, file names). Before inserting, the consumers replace invalid UTF-8 and NUL
  bytes with U+FFFD in every text field, and `\u0000` escapes in the attrs JSON,
  because Postgres rejects them. When the server still rejects a value (SQLSTATE
  class 22, for example a client address that is not an `inet`), the consumer inserts
  that batch one row at a time, so only the rejected rows are dropped. Any other batch
  the server rejects is dropped at once, so it cannot hold up the stream.
- `opslog.Handler` encodes non-scalar attribute values (maps, slices, structs,
  pointers) to JSON when the record is logged. The consumer builds the row on its own
  goroutine later, and a caller may change a map once the log call returns.
- A full buffer drops the entry, and a batch that is given up is dropped. Both are
  counted in `silo_log_writer_dropped_total{stream, reason}` (`buffer_full`,
  `insert_failed`). App records also reach stderr and OTLP. Audit entries have no
  other copy, so an audit drop is permanent loss.
- After a batch is inserted, `logstream.Hub` hands each row to local live-tail
  subscribers directly and queues it (1,024 rows) for a goroutine that publishes to
  other nodes through the Redis event bus. The consumer never waits on Redis. While
  Redis is down, other nodes' live tails miss this node's rows, which are still in
  Postgres. Rows that are not sent are counted in
  `silo_log_tail_publish_dropped_total{stream, reason}` (`queue_full`,
  `publish_failed`). The hub logs when publishing starts failing and when it recovers,
  not for each row.
- The opslog consumer reports its own failures to stderr and OTLP only, so a failing
  batch cannot queue records about itself.
- Consumers run past the application context and stop after the HTTP servers drain,
  and the hub then sends its queue before the event bus closes, so graceful-shutdown
  logging is persisted and reaches other nodes. A node that dies loses the entries
  still in its buffer, including the record that `log.Fatalf` writes just before it
  exits.
- Earlier Silo versions pushed entries into the Redis lists `ops_log:buffer` and
  `activity_log:buffer`. Current versions never read them. Once no node runs an earlier
  version, delete both keys.

## Logging conventions (enforced)

Use the **context-carrying** slog variants and tag the subsystem:

```go
slog.InfoContext(ctx, "scanner: starting", "component", "scanner", "folder_id", id)
```

Rules, enforced by `sloglint` in `make lint` (`.golangci.yml`):

- **`slog.<Level>Context(ctx, …)`** wherever a `context.Context` is in scope (so records
  carry the active `trace_id`/`span_id`). The plain `slog.Info(…)` form is only allowed
  where no `ctx` exists (early boot, top-level goroutines).
- **Static message** — the message is a constant; move dynamic parts to attributes.
- **snake_case attribute keys** — `component`, `request_id`, `trace_id`, `user_id`, …
- **No mixed args** — don't combine key-value pairs and `slog.Attr` in one call.

### Component classification

Every direct `slog.*Context` call carries a `component` attribute. `opslog` classifies
records by that attribute first, falling back to `opslog.InferComponent` (the `subsystem:`
prefix in the message) when no attribute is present — bound loggers that already set
`component` via `.With(...)` are left as-is.

**Limitation:** `sloglint` enforces the *shape* (context variant, snake keys, static
message) but **cannot** enforce that a `component` attribute is present. That convention
rests on this doc, the canonical list below, and review.

Canonical component values (first path segment under `internal/`; `app` for `cmd/silo`):

`access`, `activitylog`, `adminjob`, `ai`, `api`, `app`, `audiobooks`, `auth`, `autoscan`,
`catalog`, `chapterthumbs`, `diagnostics`, `downloads`, `ebooks`, `historyimport`,
`jellycompat`, `libraryingest`, `manga`, `metadata`, `nodeconfig`, `nodepool`, `noderecipe`,
`nodesessions`, `notifications`, `opslog`, `playback`, `plugins`, `policy`, `proxy`, `ratelimit`,
`recommendations`, `requests`, `scanner`, `scanqueue`, `sections`, `taskmanager`,
`telemetry`, `transcodenode`, `watchlist`, `watchsync`, `webhooksync`, `worker`.

Two API-handler surfaces predate this rule and log a domain component instead of `api`:
`settings` (`internal/api/handlers/settings_values.go`) and `webhook_sync`
(`internal/api/handlers/webhook_sync.go`). Treat those two values as grandfathered —
dashboards filter on them — but do not add new exceptions.

## Metrics stay on Prometheus

OpenTelemetry here installs **no MeterProvider**. Metrics continue to flow through the
existing Prometheus `client_golang` instrumentation to `/metrics`; Grafana scraping is
unaffected.

This is deliberate and guarded: the trace-instrumentation libraries (`otelhttp`, etc.,
added in later phases) also emit metrics through the *global* MeterProvider. Because none
is set, that global stays the built-in **no-op**, so those metric calls are silently
discarded — no double-counting into Prometheus, no second `/metrics` source. A guard test
asserts `otel.GetMeterProvider()` remains the no-op after `Setup`. Migrating metrics to
the OTel metrics SDK is out of scope.

## Local collector (example)

```yaml
# docker-compose.yml (dev)
otel-collector:
  image: otel/opentelemetry-collector:latest
  command: ["--config=/etc/otelcol/config.yaml"]
  volumes:
    - ./otelcol.yaml:/etc/otelcol/config.yaml
  ports:
    - "4317:4317"   # OTLP gRPC
    - "4318:4318"   # OTLP HTTP
```

```yaml
# otelcol.yaml — minimal debug pipeline
receivers:
  otlp:
    protocols:
      grpc: {}
      http: {}
exporters:
  debug:
    verbosity: detailed
service:
  pipelines:
    traces: { receivers: [otlp], exporters: [debug] }
    logs:   { receivers: [otlp], exporters: [debug] }
```

Run Silo with `SILO_OTEL_ENABLED=1 OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317` and watch
logs + traces arrive at the collector. Swap `debug` for Loki/Tempo (or a vendor OTLP
endpoint) for real storage; Grafana can then show traces alongside the unchanged
Prometheus metrics.

## Log retention & rotation

Silo does **not** write or rotate its own log files. Rotation/retention is handled by the
layer that owns each sink, which is the intended cloud-native split:

- **Console (stderr)** is owned by the container runtime. Under Docker, configure the
  `json-file` driver to cap and rotate on-disk logs, and mount the log directory on a
  volume so history survives container recreation (the problem that motivated this work):

  ```yaml
  # docker-compose.yml
  services:
    silo:
      logging:
        driver: json-file
        options:
          max-size: "50m"   # rotate at 50 MB
          max-file: "5"     # keep 5 rotated files
  ```

  For a non-Docker deployment, run under a supervisor/journald or pipe to `logrotate`.

- **OTLP** is the durable, queryable path: the collector/backend (Loki, a vendor, …) owns
  retention and is the recommended place to keep searchable history. Enable it (see above)
  and set retention on the backend.

- **opslog (Postgres)** self-prunes: daily partitions with per-component retention and
  size caps (`internal/opslog/cleanup.go`). No operator action needed.

An in-process rotating file sink (e.g. lumberjack) was deliberately **not** added — it
would re-introduce a custom sink the OTLP + runtime split already covers.

## Known limitations

- **`log_quiet` also suppresses OTel logs.** The fan-out sits below the quiet filter, so
  quieting a subsystem empties its OTLP stream too (OTLP mirrors the console stream).
- **Redaction is key-based, not value-based.** Secrets under a recognized key are masked
  on every sink, but a secret embedded in a message string or under an unrecognized key is
  not caught (see [Redacted](#log-handler-chain)).
- **Early-boot logs are not exported.** Records emitted before the handler is installed
  (DB connect, migrations, tuning) reach stderr only, matching existing `opslog` behavior.
- **Per-subsystem trace propagation into plugins** is a follow-up owned by
  `silo-plugin-sdk`; this repo instruments only the host side.


## Profiling and resource boundaries

Every serving process can enable a separate literal-loopback profiling listener
with `SILO_DEBUG_LISTEN=127.0.0.1:6060`. It is disabled by default, binds before
PostgreSQL connection, and never participates in readiness. Invalid addresses
fail bootstrap; an occupied debug port reports an error while the workload
continues. Migration and utility commands do not start the listener.
`silo_debug_listener_available` and `silo_debug_listener_failures_total`
distinguish an absent listener from a healthy one.

The listener is an operational interface outside `/api/v2`. It exposes no native
client operations and needs no Apple, Android, Jellyfin, or ABS client changes.
The primary and compatibility listeners never serve Go profiles. A frontend SPA
may answer an unknown profiling path with HTML, so tests and tools check response
content rather than assuming an absent endpoint returns 404. Requests require
GET, a literal loopback Host authority, and local browser-origin headers when
present; responses carry `Cache-Control: no-store`. Loopback binding trusts every
process in the network namespace; it does not isolate tenants sharing a host or
pod.

Captures are bounded per process:

| Profile | Bound |
| --- | --- |
| `profile` (CPU) | Default 30 seconds; maximum 60 |
| `heap`, `allocs`, `goroutine`, `threadcreate` | Immediate, or a delta of up to 60 seconds |
| `block`, `mutex` | Disabled (HTTP 409) unless `SILO_DEBUG_BLOCK_RATE` (1,000,000–1,000,000,000 ns) or `SILO_DEBUG_MUTEX_FRACTION` (100–1,000,000) is set at startup |
| `trace` | Default 1 second; maximum 5 |

Only one capture runs per process, including snapshots and forced GC. Concurrent
requests fail at once with HTTP 429 and increment
`silo_debug_captures_busy_total`; there is no queue. Unknown, duplicate,
malformed, or excessive parameters are rejected before profiling starts. Client
disconnect and server shutdown cancel timed captures, though runtime GC or stack
collection may finish first and the slot stays held until the handler exits.
Duration limits bound neither stop-the-world pauses nor output size, which is why
`scripts/silo-profile` caps output (128 MiB by default) and the request deadline
(75 seconds), never overwrites an artifact, and marks a sidecar `valid: true`
only after a complete response with the uninterrupted-capture trailer. The
listener omits `cmdline`, `symbol`, generic expvar, and dynamically discovered
profile names. Analyze a capture with the Go release and executable that
produced it:

```sh
go tool pprof -top incident/silo incident/cpu.pprof
go tool pprof -sample_index=inuse_space -top incident/silo incident/heap.pprof
go tool pprof -diff_base=incident/before.pprof -top incident/silo incident/after.pprof
go tool trace -http=127.0.0.1:8088 incident/runtime.trace
```

The native API exposes administrator summaries at
`GET /api/v2/admin/system/resources` and capability discovery at
`GET /api/v2/admin/system/resources/capabilities`. Raw profiles stay outside
OpenAPI. The resource response identifies the sampled API process with a random
instance ID, which matters when a load balancer sends successive requests to
different replicas. Workers attach the same attribution to authenticated health
transport. Frozen v1 resource fields retain their existing meaning.

| Measurement | Population and limits |
| --- | --- |
| Go heap/runtime | This Go runtime; excludes FFmpeg, plugins and native allocations such as libvips. |
| Process CPU/RSS/FDs/threads | This process. RSS and Go memory cannot be subtracted to measure native allocation exactly. |
| Linux process I/O | Kernel process counters can include I/O of children already waited for. These overlap child lifecycle/cgroup accounting. |
| Cgroup CPU/memory | All members of the reported leaf or visible ancestor. Memory includes cache and native/child allocations; ancestor limits outside the namespace are unavailable. |
| Live owned children | Bounded FFmpeg sampling, validated by PID/start time. CPU is a changing sum of live lifetime totals, not a counter. RSS can count shared pages repeatedly. |
| Completed children | Existing process owner's Wait/ProcessState CPU and peak RSS. Peaks are distributions, not concurrent usage. Short-lived processes are counted here. |
| Host/network/GPU | Scope and source accompany samples. Whole-device/host readings include unrelated tenants and must not be summed per container. |
| Filesystem | Sampled capacity and inode use per bounded role. Check availability and freshness before using a value. |

Resource and queue collectors read snapshots. Hardware probes and database
sampling run in bounded background work, never in HTTP handlers or scrapes.
Missing sources omit values; stale snapshots carry explicit timestamps and flags.
Use node-exporter, a container exporter, GPU vendor exporters and dependency
exporters for device latency, network drops, host pressure, PostgreSQL locks/WAL,
Redis evictions and object-store capacity. Silo measures the calls it owns.

Go heap profiles describe sampled Go allocations only: not libvips or other
native allocations, FFmpeg memory, plugin internals, filesystem cache, or GPU
allocations. Completed-child metrics consume the owner's existing process exit
state. `silo_subprocess_cpu_seconds_total` reports user and system CPU at
completion, `silo_subprocess_exits_total` counts bounded workload and outcome
categories, and `silo_subprocess_peak_rss_bytes` is a distribution of per-child
maximum RSS, normalized to bytes and omitted on unsupported platforms.
Instrumented owners are playback transcodes and restarts, progressive remux,
scanner probes, plugin shutdown and failed startup, and media analysis,
chapter thumbnail, and trickplay runs through `internal/mediasample`
(workloads `analysis`, `thumbnail`, and `trickplay`). Other FFmpeg uses need their own owner instrumentation before
claiming coverage. No second waiter or reaper is installed.

### Node resource sampling

`internal/nodemetrics` samples CPU, memory, disk, network and GPU in every
serving process every five seconds and keeps only the current sample; trends and
alerts belong to Prometheus. The sample feeds the admin Nodes page, each node's
`/api/v1/health` and `/status`, `GET /api/v2/admin/system/resources`, and
`streamapp_node_*` gauges. Proxy and transcode nodes serve `/metrics` on their
application listener; the API process serves it only on `SILO_METRICS_LISTEN`.

- **Paths stay private.** Disk series are labeled by role (`mount="scratch"`,
  `mount="library-1"`), never by path, so an unauthenticated scrape cannot
  enumerate media locations. A node's unauthenticated `/api/v1/health` withholds paths
  on the same terms. Paths appear only in the admin resources response and a
  node's bearer-authenticated `/status`.
- **Probing is bounded.** Each process's sampler checks at most eight mounts:
  the transcode scratch directory first, then library roots in order. The
  process logs how many go unsampled. The cap is per process, not per host, so
  co-located processes each probe their own set. Each mount costs a `statfs` per interval,
  and one on an unresponsive network mount cannot be interrupted, so the cap
  bounds probing as well as reporting. A mount that stops responding keeps its
  last good numbers marked `stale` and never delays a health response; a path
  the process cannot see reports `unavailable`, not an empty disk.
- **GPU sources.** Intel (i915, xe) and AMD (amdgpu) busyness comes from DRM
  fdinfo, which needs no privileges beyond `/dev/dri` and counts only Silo's own
  FFmpeg processes. NVIDIA exposes no fdinfo, so `nvidia-smi` supplies
  whole-GPU utilization, encoder and decoder utilization, and VRAM, including
  other tenants; a missing or repeatedly failing binary reports
  `source: unavailable`. Whole-GPU Intel sampling would need `intel_gpu_top`
  with `CAP_PERFMON` and a permissive `kernel.perf_event_paranoid`, which a
  default container should not get, so Intel `total_busy_pct` is absent rather
  than zero. Every device reports its `source`.
- **Container scope.** `/proc/stat` and `/proc/meminfo` describe the host, so
  the sampler corrects both against the process's cgroup. Memory is the cgroup
  limit and working set without page cache, the OOM basis. CPU is the cgroup's
  usage against its own quota, and `cores` is that quota. `/proc/net/dev` is
  already per network namespace.
- **Nested LXC.** A Docker container inside an LXC container sees the physical
  machine's `/proc` and no limit on its own cgroup, because the LXC's cap sits on
  an ancestor cgroup outside its namespace. lxcfs virtualizes those files for
  the LXC, so `docker-compose.yml` bind-mounts `/proc/meminfo`, `/proc/stat` and
  `/proc/loadavg` under `/host/proc`. The sampler reads each `/host/proc` file
  when present and falls back to its own `/proc` per file, with no setting. lxcfs
  virtualizes `/proc/loadavg` only with loadavg accounting (`lxcfs -l`), which
  Proxmox leaves off, so `load1` can still read the physical host's load.

## Instrumentation ownership

[The workload catalog](workload-metrics.md) identifies each queue
and execution boundary. Attempt counters belong to the executing process and
are summed across replicas. Shared database queue gauges are sampled by each API
process and use `max by (cluster, queue, state)`, never a replica sum. Queue age
includes retry backoff; it is not the age of the oldest immediately runnable job.
A recent aggregate progress update cannot establish progress for every concurrent
job. Use the administrator job state when investigating a specific stall.

`streamapp_userdb_pool_open`, `streamapp_userdb_pool_evictions_total` and
`streamapp_scanner_files_total` now have producers in their owning packages. The
former middleware declarations had no production writers. The unwritten
`streamapp_userdb_restore_duration_seconds`,
`streamapp_playback_active_sessions`, `streamapp_reconciliation_lag_seconds`,
`streamapp_matcher_resolved_total` and `streamapp_litestream_sync_errors_total`
placeholders are omitted rather than used as health signals. Existing playback,
stream telemetry, matcher queue and workload metrics provide measured coverage;
the placeholder Litestream implementation cannot report replication health.

Names and units of existing measured metrics are preserved. New application
instruments use `silo_`; Go/process collectors retain upstream names. One
`silo_build_info` series holds revision and Go version. The runtime collector adds
only GC, scheduler, memory classes, CPU classes and synchronization families.
Audit the emitted families when upgrading Go.

## Jellyfin-compatible listener

The Jellyfin-compatible listener runs on its own `http.Server`, so the native
request metrics never see it. `observeCompatRequest`
(`internal/jellycompat/observe.go`) runs right after the request ID middleware
and records each request once, whatever answered it: a route, the 404 or 405
fallback, the CORS preflight, or a middleware that refused the request.

| Metric | Labels |
| --- | --- |
| `silo_jellycompat_requests_total` | `route`, `method`, `status_class`, `client` |
| `silo_jellycompat_request_duration_seconds` | `route`, `method` |
| `silo_jellycompat_client_request_duration_seconds` | `client` |

- `route` is the chi route template (`/Items/{id}`), read after routing. It is
  never the raw path or an ID. A request that matched no route is `unmatched`.
- `method` is a standard method or `other`.
- `status_class` is `1xx` to `5xx`, `hijacked` when the session socket took
  over the connection, or `other`.
- `client` comes from the MediaBrowser `Client` field, then the User-Agent:
  `infuse`, `swiftfin`, `findroid`, `streamyfin`, `wholphin`, `fladder`,
  `moonfin`, `senplayer`, `vidhub`, `kodi`, `jellyfin-web`,
  `jellyfin-androidtv`, `jellyfin` (any other client named Jellyfin, such as
  the official apps), `other`, or `none` (no identity at all). Free text never
  becomes a label.

The playback and transfer media routes (streams, HLS segments, subtitles,
attachments, downloads and the bitrate test) and `/socket` are counted but not
timed. Their duration is the client's viewing or connection time, which would
land in the `+Inf` bucket and distort the percentiles. They are counted when
the response ends, which can be hours after the request started, and a stream
cut off by a process exit is not counted, so the counter's rate on these routes
is not a rate of stream starts. Stream telemetry tracks their live sessions,
transfers and bytes. HLS manifests are timed: they are short documents, and
their latency is the server's part of playback start.

Both histograms time the same requests. The route histogram leaves out
`status_class` and `client` because an unauthenticated caller picks both, and
each would multiply the 14 series of every route. About 120 timed method and
route pairs plus `unmatched` give at most about 1,800 series. The client
histogram splits the same requests by client family alone, one histogram per
family, about 200 series; a route by client histogram would be about 26,000.
The counter's full label product is about 15,000 series. In practice a route
answers a client with one or two status classes, so even a server that sees
every client on every route stays near 4,400. All three families count toward
the per-scrape sample limit in the
[example Prometheus configuration](../../deploy/observability/prometheus.yml).

Latency for one client on one route lives on the trace. Each request opens a
server span named `jellycompat <METHOD> <route>` with `http.request.method`,
`http.route`, `http.response.status_code` (or `http.response.outcome` set to
`hijacked`) and `jellycompat.client`. Postgres, Redis and S3 dependency spans
started during the request are its children instead of separate root traces.
The span carries no path, query, header value or body. A `/socket` span stays
open for the life of the connection, as the native v2 socket spans do. It holds
the session check made before the upgrade; the checks the socket repeats while
connected each start their own trace, so a socket left open all day does not
grow one trace without bound.

## Trace trust, privacy and cost

Native v2 and Jellyfin-compatible requests start fresh server traces. Public
trace IDs, sampling flags and baggage cannot select the local sampling decision.
The enabled default is 1%; use 100% only during a bounded investigation.
Authenticated worker HTTP calls propagate W3C trace context without baggage;
redirects cannot forward internal credentials or trace context to another
destination. Plugin host gRPC spans use fixed SDK operation names. Plugins need
SDK extraction before their internal spans can join those traces.

Dependency spans record finite Postgres statement classes, Redis commands,
S3 operations, notification sends and configured pool roles. They exclude SQL, bind arguments,
keys, object names, URLs, request bodies and arbitrary error messages. Workload
spans use fixed categories. Asynchronous attempts link an initiating trace when
available and start their own trace; durable job identity is not a metric label.
Persisted queues do not currently retain initiating trace context across restarts.

API metric client labels are `web`, `apple`, `android`, `other`, or `none`.
Jellyfin-compatible client families are listed under
[Jellyfin-compatible listener](#jellyfin-compatible-listener).
Unmatched legacy routes and unknown HTTP methods fold into fixed values.
No provider or user identity may allocate a new series. Histograms are bounded
by operation categories and fixed buckets; no per-job histogram is permitted.
Review the product of dimensions for every added metric.

Trace batches hold at most 2,048 spans (512 per export); log batches use bounded
SDK queues. Export calls have a five-second budget. SDK queues are nonblocking
and may discard telemetry under pressure. `silo_otel_export_records_total`
reports exporter outcomes; `silo_otel_finished_spans_total` counts sampled spans
that ended. Their difference includes queued, exporting and dropped spans, so
it is not an instantaneous drop count. Inspect it after a drain and monitor
collector/backend self-metrics. There is no OTel MeterProvider.

## Client experience and plugin coordination

Server response bytes and first playable segments do not establish first frame
or rebuffering on a client, so press-play-to-first-frame comes from the clients'
`first_frame` route events. When the server stores a new `first_frame` event
whose `first_frame_ms` diagnostic parses as 0 to 600,000 ms, it observes
`silo_playback_first_frame_seconds{client}` (buckets from 0.1 s to 60 s). The
`client` label is the fixed family (`web`, `apple`, `android`, `other`, `none`)
of the event's client name, which on a v2 report is the declared `X-Client-Name`
or else the `X-Silo-Client` product name that also labels
`streamapp_apiv2_requests_total{client}`. The first-party clients send only
`X-Silo-Client`. Only an inserted row counts. A v2 report that
repeats its `event_id` inserts nothing on any replica, so a retry is not counted
twice. Legacy v1-bridge reports carry no event id and are counted on every
report. Events dropped because the in-process write queue is full are never
observed. Each replica exports the events it wrote; sum across replicas.

The clients do not time the same interval yet. The web player measures from the
viewer's Play action (a Play button, a card, an episode pick, the next-episode
prompt's Play Now, or a version switch inside the player) to the event that
removes its loading overlay. It sends `first_frame` without a duration when
nothing timed the start: a deep link, a reload, or a start the viewer did not
ask for, such as a Watch Party selection, an autoplay countdown, or the next
part of a multi-part file. Android sends `first_frame_ms` measured from plan adoption, which leaves
out the start request. Apple sends `first_frame` through the v2 route-event
endpoint without `first_frame_ms`. Apple's rebuffer counter and Android's buffering callbacks
remain local. Neither native app consumes administrator resource DTOs, so the
additive resource response needs no native model migration.

The API v2 program must coordinate capability-gated first-frame and rebuffer
reporting with both native apps, including v2 route-event migration and retry/
deduplication semantics. Third-party Jellyfin/ABS player experience remains
unknown unless their protocol supplies evidence. The plugin SDK owns trace
extraction and internal plugin spans; the host's Go heap cannot profile a plugin
process. These boundaries are recorded in the PR follow-up checklist.

## Deployment and retention

The main application listener does not serve `/metrics`. Metrics are disabled unless
the operator sets `SILO_METRICS_LISTEN` to an explicit address; that setting applies
to integrated and API processes. Proxy and transcode nodes serve `/metrics` on their
own application listener. The dedicated listener
is an unauthenticated operational endpoint, so bind it to an internal monitoring
network or a loopback address and do not publish it through a public Service or
ingress. Prometheus should scrape that listener directly. Do not publish the loopback
profiler through container ports, a public Service, ingress or a native API proxy.

`deploy/observability/` holds the example Prometheus configuration, alert rules
with their tests, and a Grafana dashboard. Targets carry a `cluster` label and use
`process_role` for the deployment role, because `role` already names
database, cache and storage pools. Prometheus owns metric retention; the OTLP
backend owns trace/log retention. Profiles remain private incident artifacts
with deliberate deletion. Silo adds no time-series database or automatic profile
upload. Dashmetrics retains its existing bounded summary behavior.

### Validating observability changes

Commands assume the repository root is the current directory. CI does not run
them; run them when changing the examples or rules:

```sh
docker run --rm --entrypoint promtool -v "$PWD/deploy/observability:/etc/prometheus:ro" prom/prometheus:v3.5.0 check config /etc/prometheus/prometheus.yml
docker run --rm --entrypoint promtool -v "$PWD/deploy/observability:/etc/prometheus:ro" -w /etc/prometheus prom/prometheus:v3.5.0 test rules silo.rules.test.yml
```

The rules tests exercise alert logic with synthetic series. Package tests cover
Linux source parsing, unavailable states, dependency saturation, queue variants,
capture lifecycle, profile parsing and metric privacy. They do not establish a
production overhead bound, every GPU vendor's behavior, or Kubernetes
forwarding support. For a change that could affect those, run these exercises on
an isolated instance with synthetic media and its own database and cache, and
record the source SHA, image digest, Go version, resource limits, and profiling
and OTLP settings in private release evidence:

1. Baseline request p50/p95/p99, throughput, CPU, RSS, allocations, scrape
   bytes, duration and sample count, and stream stability. Measure metrics,
   sampled traces, CPU profiles, execution traces and contention sampling
   separately. Targets: at most 2% CPU or throughput and 5% p95 latency
   regression.
2. Capture CPU, heap, allocs, goroutine and trace profiles through the process
   network namespace, then test a second concurrent capture, client
   disconnect, duration overflow, an untrusted Host or Origin, and shutdown
   mid-capture. The main and compatibility listeners must never return profile
   data.
3. Occupy the debug port before startup. Readiness must still succeed, with
   debug availability zero and an explicit listener failure.
4. Point OTLP at an unavailable collector and send more than a batch. Requests
   must finish, memory must stay bounded, and exporter failures must be
   visible.
5. Exhaust a one-connection PostgreSQL or Redis pool in a test harness and
   observe wait, timeout, cancellation and recovery without leaking queries,
   keys or endpoints.
6. Exercise cgroup throttling, unreadable procfs fixtures, stale disk probes,
   long and delayed queues, and unavailable sources. Run deliberate OOM tests
   only on disposable instances.
7. Run integrated and worker processes under concurrent scan, image, browse,
   direct stream, transcode and download load. Stop a worker mid-stream,
   verify recovery, and confirm shared queue, host and GPU aggregation does not
   double count replicas.
