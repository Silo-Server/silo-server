# Observability (OpenTelemetry logs + traces)

Silo emits structured **logs** and distributed **traces** via OpenTelemetry (OTLP),
in addition to the existing stderr and `opslog` database pipeline. The feature is
**opt-in and default-off**: with no `OTEL_*` / `SILO_OTEL_ENABLED` configuration the
server behaves exactly as before (stderr + `opslog` only).

**Metrics are not part of OpenTelemetry here.** They remain on Prometheus
(`client_golang`, the `/metrics` endpoint, and the domain metrics in
`internal/api/middleware`). See [Metrics](#metrics-stay-on-prometheus) below.

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
| `OTEL_TRACES_SAMPLER_ARG` | Trace-id ratio for the ratio-based samplers (0–1; clamps >1 to 1). | `1.0` |

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
`TraceContext + Baggage` propagator. Shutdown is deferred in `cmd/silo/main.go` with a
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

`activitylog`, `adminjob`, `ai`, `api`, `app`, `audiobooks`, `auth`, `autoscan`,
`catalog`, `chapterthumbs`, `downloads`, `ebooks`, `historyimport`, `jellycompat`,
`libraryingest`, `manga`, `metadata`, `nodeconfig`, `nodepool`, `noderecipe`,
`nodesessions`, `notifications`, `opslog`, `playback`, `plugins`, `policy`, `proxy`, `ratelimit`,
`recommendations`, `requests`, `scanner`, `scanqueue`, `sections`, `taskmanager`,
`telemetry`, `transcodenode`, `watchlist`, `watchsync`, `webhooksync`, `worker`.

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
