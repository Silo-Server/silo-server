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
[Profiling operations](../operations/profiling.md) describes capture limits,
namespace access, private artifacts, cancellation and native profiling.

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

## Instrumentation ownership

[The workload catalog](../operations/workload-metrics.md) identifies each queue
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

## Trace trust, privacy and cost

Native v2 requests start fresh server traces. Public trace IDs, sampling flags
and baggage cannot select the local sampling decision. The enabled default is
1%; use 100% only during a bounded investigation. Authenticated worker HTTP calls
propagate W3C trace context without baggage; redirects cannot forward internal
credentials or trace context to another destination. Plugin host gRPC spans use
fixed SDK operation names. Plugins need SDK extraction before their internal
spans can join those traces.

Dependency spans record finite Postgres statement classes, Redis commands,
S3 operations, notification sends and configured pool roles. They exclude SQL, bind arguments,
keys, object names, URLs, request bodies and arbitrary error messages. Workload
spans use fixed categories. Asynchronous attempts link an initiating trace when
available and start their own trace; durable job identity is not a metric label.
Persisted queues do not currently retain initiating trace context across restarts.

API metric client labels are `web`, `apple`, `android`, `other`, or `none`.
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
or rebuffering on a client. Apple already emits first-frame route events through
its v1 bridge; its rebuffer counter remains local. Android has a route-event DTO
and API/repository methods, but its first-frame and buffering callbacks currently
update local player state. Neither native app consumes administrator resource
DTOs, so the additive resource response needs no native model migration.

The API v2 program must coordinate capability-gated first-frame and rebuffer
reporting with both native apps, including v2 route-event migration and retry/
deduplication semantics. Third-party Jellyfin/ABS player experience remains
unknown unless their protocol supplies evidence. The plugin SDK owns trace
extraction and internal plugin spans; the host's Go heap cannot profile a plugin
process. These boundaries are recorded in the PR follow-up checklist.

## Deployment and retention

The main application listener does not serve `/metrics`. Metrics are disabled unless
the operator sets `SILO_METRICS_LISTEN` to an explicit address. The dedicated listener
is an unauthenticated operational endpoint, so bind it to an internal monitoring
network or a loopback address and do not publish it through a public Service or
ingress. Prometheus should scrape that listener directly. Do not publish the loopback
profiler through container ports, a public Service, ingress or a native API proxy.

[Monitoring operations](../operations/monitoring.md) includes scrape, dashboard,
alert and failure-exercise examples. Prometheus owns metric retention; the OTLP
backend owns trace/log retention. Profiles remain private incident artifacts
with deliberate deletion. Silo adds no time-series database or automatic profile
upload. Dashmetrics retains its existing bounded summary behavior.
