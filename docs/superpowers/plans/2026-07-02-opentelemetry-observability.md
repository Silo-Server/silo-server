# OpenTelemetry Observability: Logs + Traces Adoption Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Commands assume the repository root is the cwd unless a task explicitly says otherwise.

**Goal:** Adopt OpenTelemetry for **logs and traces** (metrics explicitly deferred), so Silo emits OTLP-exportable structured logs correlated with distributed traces, without discarding the existing stderr + `opslog` DB/admin-stream pipeline. Standardize all `slog` call sites so every log record can carry `trace_id`/`span_id` when a span is active.

**Architecture:** Add a single OTel SDK bootstrap module (`internal/telemetry`) that builds a shared `resource.Resource`, a `TracerProvider`, and a `LoggerProvider` from `OTEL_*` env config, with graceful shutdown wired into the existing signal path. Bridge logs by inserting an `otelslog` handler into the existing `slog.Handler` chain via **fan-out** (`slog.MultiHandler`, stdlib in Go 1.26) — the current stderr + `opslog` sinks are untouched; OTel receives the same stream. Standardize the ~1,200 `slog.*` call sites to `slog.*Context(ctx, …)` and to explicit `component` attrs (retiring the `subsystem:` message-prefix convention as the classification signal, while keeping `opslog.InferComponent` as a backward-compatible fallback). Add tracing at the natural seams: chi HTTP middleware, `pgx` pool tracer, `go-redis` hook, outbound `http.Client` transports, host-side plugin gRPC stats handlers, and domain spans for scanner/playback/taskmanager. W3C `traceparent` propagation ties main-server → transcode-node and → plugin-subprocess traces together.

**Tech Stack:** Go 1.26, `log/slog`, chi/v5, pgx/v5 + pgxpool, go-redis/v9, gRPC, PostgreSQL. New deps: `go.opentelemetry.io/otel` (+ `sdk`, `sdk/trace`, `sdk/log`), OTLP exporters (`otlptracegrpc`/`otlptracehttp`, `otlploggrpc`/`otlploghttp`), `go.opentelemetry.io/contrib/bridges/otelslog`, `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`, `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc`, `github.com/redis/go-redis/extra/redisotel/v9`, and a pgx tracer (`github.com/exaring/otelpgx`).

**Non-goals (explicit):**
- **Metrics stay on Prometheus — and must not break.** The existing `client_golang` instrumentation (`/metrics` on the dedicated `metricsMux` at `cmd/silo/main.go:2078`, `internal/api/middleware/metrics.go`, `internal/api/middleware/domain_metrics.go`) is **not** migrated to the OTel metrics SDK in this plan. Prometheus and OTel run on **separate, parallel rails**: Prometheus → `/metrics` → Grafana scrape (unchanged), OTel → OTLP → traces/logs backend. See the "Prometheus / Grafana coexistence" section below for the one footgun that must be guarded.
- **Plugin-SDK-side instrumentation is out of repo.** Only host-side gRPC handler options live here; the plugin subprocess side is owned by `silo-plugin-sdk` and is a coordination follow-up.
- **Log redaction now covers all sinks.** A shared `internal/logredact` handler masks secret-keyed attrs on console + OTLP (opslog DB path already redacted); `opslog.shouldRedact` delegates to `logredact.SecretKey` so the marker list is single-sourced. (Superseded the earlier "OTel output is raw" risk.)

---

## Validated Findings

Established by codebase research on the `feat/opentelemetry-plan` branch (based off `origin/main` @ `5172155a`).

### Logging
- The `slog.Handler` chain is assembled in two stages. Base handler is `slog.NewJSONHandler`/`NewTextHandler(os.Stderr, …)` in `buildBaseHandler` (`cmd/silo/main.go:132-138`), gated by a shared `*slog.LevelVar` (`cmd/silo/main.go:540-544`).
- `internal/logfilter/handler.go` wraps the base handler to drop records whose message has a configured `prefix:` (hot-reloadable `SetQuiet`). Installed as the first default logger at `cmd/silo/main.go:544-545`.
- Once Postgres/settings exist, `configureOperationalLogging` (`cmd/silo/main.go:169-211`) re-wraps: `slog.New(opslog.NewHandler(filteredHandler, operationalWriter, opsCaptureLevel, nodeID))` (`cmd/silo/main.go:209`).
- `opslog.Handler.Handle` (`internal/opslog/handler.go:33-93`) forwards to `inner` first, then (if level ≥ capture) flattens attrs, redacts secret-ish keys (`internal/opslog/handler.go:188-196`), and writes an `opslog.Entry` to a `Writer` (Redis list or in-memory chan → `Consumer` → partitioned `operational_logs` table → `logstream.Hub` admin stream).
- **Component classification**: explicit `component` attr, else `inferComponent(message)` splitting on first `:` (`internal/opslog/handler.go:51-54,166-174`), default `"app"`. Correlation attrs already extracted if present: `request_id`, `session_id`, `playback_session_id`, `client_ip`/`remote_addr`, `node_id`, `user_id`.
- **~1,211 `slog.(Info|Warn|Error|Debug)(` call sites** across `internal/`. Heaviest: `internal/api/handlers/libraries.go` (83), `internal/metadata/service.go` (70), `internal/api/handlers/playback.go` (60), `internal/metadata/worker.go` (45), `internal/recommendations/worker.go` (43), `internal/scanner/scanner.go` (40), `internal/adminjob/runner.go` (37), `internal/api/handlers/plugins.go` (35). Dominant idiom is the bare package-level `slog.Info("subsystem: msg", "k", v)` (no context). A few subsystems bind `slog.Default().With("component", …)` on a struct field (`internal/notifications/*`, `internal/playback/ffmpeg_log_sink.go`, `internal/api/middleware/request_logger.go`).
- **No `trace_id`/`span_id` concept exists** anywhere yet. No automatic context→attr bridging; each call passes correlation attrs manually.

### Instrumentation seams
- Router is chi/v5. Main middleware stack (`internal/api/router.go:209-231`): `RequestID` (`router.go:213`) → `clientip.Middleware` → `apimw.RequestLogger` (`internal/api/middleware/request_logger.go:19`) → `Recoverer` → `apimw.Metrics` (`internal/api/middleware/metrics.go:35`) → `Compress`. `request_logger.go:53-70` already logs `request_id`/`user_id`/`session_id`/`playback_session_id`. `metrics.go:79-89` has `sanitizePath` (solves `http.route` cardinality).
- jellycompat has its own chi router mounting `middleware.RequestID` (`internal/jellycompat/router.go:30`). `internal/transcodenode/server.go` is a standalone chi server in a separate process.
- Postgres pool built at `internal/database/postgres.go:15` via `pgxpool.ParseConfig`; no `Tracer` set (pgx v5 `QueryTracer` seam).
- Redis clients built at `internal/cache/redis.go:145-254` (two `redis.NewClient` sites); go-redis v9 `AddHook`/`redisotel` seam.
- Outbound `http.Client` literals (not a shared factory): `internal/nodepool/health.go:22-26`, metadata providers (`internal/metadata/tmdb`, `.../trakt`, `.../translation`, `internal/mdblist`), subtitle providers (`internal/subtitles/opensubtitles`, `subdl`, `subsource`), plugin HTTP proxy (`internal/plugins.HTTPProxy`, wired `router.go:149`).
- Host-side plugin gRPC server: `grpc.NewServer(opts...)` at `internal/pluginhost/host.go:336` — but note this is inside `broker.AcceptAndServe(...)`, a hashicorp/go-plugin **brokered sub-stream** (reverse channel), not the primary host→plugin invocation channel. The load-bearing seam is the client dial (`grpc.NewClient`, pattern in `internal/pluginhost/client_test.go:18`). See Task 7.
- Background entrypoints: scanner `NewScanner` (`internal/scanner/scanner.go:192`), scoped scan `applyScopedScan` (`internal/scanner/scanner.go:1287`); playback `NewSessionManager` (`internal/playback/session.go:152`); taskmanager `RunTask` (`internal/taskmanager/manager.go:231`).
- `context.Context` is threaded pervasively (`Dependencies.AppContext` at `internal/api/router.go:88`; every repo/service method takes `ctx` first). Span context propagates cleanly for the **request/service call chains** — the work there is adding spans + switching call sites to the `…Context` variants. **Exception:** struct-bound loggers whose methods don't take a `ctx` (`taskmanager`, `notifications/*`, `ffmpeg_log_sink.go`) need signature plumbing or stay uncorrelated; see Task 3 Step 3.
- Existing endpoints: `/metrics` on a dedicated `metricsMux` (`cmd/silo/main.go:2078`), `/health` + `/ready` (`internal/api/router.go:247,1436`). No pprof registered.

### Dependencies
- `go 1.26.4` → `slog.MultiHandler` is available in the stdlib (no `samber/slog-multi` needed).
- Already present: `github.com/prometheus/client_golang v1.23.2`; `go.opentelemetry.io/otel/sdk/metric v1.41.0` is present only as an **indirect** transitive dep (not wired). No `go.opentelemetry.io/otel` core, no trace SDK, no exporters, no otelslog/otelhttp/otelgrpc.

---

## File Structure

### New files
- `internal/telemetry/telemetry.go` — SDK bootstrap: `Config` (parsed from env), `Setup(ctx, Config) (*Providers, shutdown func(context.Context) error, error)`, shared `resource.Resource`, `TracerProvider`, `LoggerProvider`, W3C propagator, batch processors.
- `internal/telemetry/config.go` — `OTEL_*` + `SILO_OTEL_*` env parsing (enable gate, endpoint, protocol, sampler, service name/version).
- `internal/telemetry/loghandler.go` — a best-effort wrapper around `otelslog.Handler` whose `Handle` never propagates an OTel export error (mirrors the `logsink` best-effort contract), plus the fan-out assembly helper.
- `internal/telemetry/httpclient.go` — shared `otelhttp.NewTransport`-wrapped `http.RoundTripper` factory for outbound clients.
- `internal/telemetry/telemetry_test.go`, `internal/telemetry/config_test.go`, `internal/telemetry/loghandler_test.go`.

### Modified files (by phase)
- `go.mod` / `go.sum` — add OTel core/sdk/exporters + contrib instrumentation + pgx/redis tracers.
- `cmd/silo/main.go` — call `telemetry.Setup`, insert otelslog fan-out into `buildBaseHandler`, defer `shutdown` in `run()`.
- `internal/api/router.go` — `otelhttp` handler/middleware in the chi chain; inject `trace_id`/`span_id` into request-logger attrs.
- `internal/api/middleware/request_logger.go` — add trace/span IDs to the emitted attrs.
- `internal/jellycompat/router.go`, `internal/transcodenode/server.go` — `otelhttp` server instrumentation + propagation.
- `internal/database/postgres.go` — `otelpgx` `QueryTracer` on the pool config.
- `internal/cache/redis.go` — `redisotel.InstrumentTracing` on both client constructions.
- Outbound-client packages listed above — accept/use the shared traced transport.
- `internal/pluginhost/host.go` (+ client dial sites) — `otelgrpc` server/client stats handlers.
- `internal/scanner/scanner.go`, `internal/playback/session.go`, `internal/taskmanager/manager.go` — domain spans.
- **All packages under `internal/`** — call-site sweep (`slog.* → slog.*Context`, `component` attr). Batched per-package in Phase 3.
- `docs/architecture/observability.md` — operator docs (env vars, collector wiring, what is/isn't instrumented).

---

## Phasing overview

- **Phase 0** — Dependencies + `internal/telemetry` SDK bootstrap (no behavior change; disabled unless configured).
- **Phase 1** — Logs bridge fan-out (logs export works end-to-end; call sites unchanged).
- **Phase 2** — Tracing providers + propagation plumbing (spans exist, exported).
- **Phase 3** — Full call-site sweep to `…Context` + `component` attrs (unlocks `trace_id` in logs).
- **Phase 4** — HTTP server + client tracing seams.
- **Phase 5** — pgx + Redis tracing seams.
- **Phase 6** — Plugin gRPC host-side propagation.
- **Phase 7** — Domain spans (scanner / playback / taskmanager).
- **Phase 8** — Shutdown wiring, docs, verification hardening.

Each phase is independently shippable and leaves `main` green. Phases 1–2 are prerequisites for Phase 3's payoff; Phases 4–7 can land in any order after Phase 2.

---

## Prometheus / Grafana coexistence

**Question this answers:** does adopting OTel break, replace, or entangle the existing Prometheus + Grafana metrics? **Answer: no — they stay separate rails, provided one footgun is guarded. Metrics stay on Prometheus permanently for this plan; migrating them to OTel is not in scope.**

- **Separate lines, not merged (this plan).** OTel `telemetry.Setup` installs **only** a `TracerProvider` and a `LoggerProvider`. It **must not** call `otel.SetMeterProvider` (Task 1 Step 5 builds no MeterProvider). Prometheus keeps its own independent rail: `client_golang` registry → `/metrics` (`cmd/silo/main.go:2078`) → Grafana scrape. Nothing about the metrics path changes. Grafana metric dashboards are untouched; traces become a *new, additive* Grafana/Tempo capability, not a change to existing panels.
- **THE FOOTGUN — global MeterProvider (guard required).** The OTel instrumentation libraries used for traces (`otelhttp`, `otelgrpc`, `otelpgx`, `redisotel`) **also emit metrics by default**, through `otel.GetMeterProvider()`. Because this plan never sets a MeterProvider, that global stays the built-in **no-op** — so those metric calls are silently discarded: no double-counting into Prometheus, no second `/metrics` source, no crash, no measurable overhead. This is safe *by omission*, so make it **explicit and tested**, not incidental:
  - In each instrumentation call, prefer passing an explicit no-op meter provider option where the library supports it (e.g. `otelhttp.WithMeterProvider(noop.NewMeterProvider())`) so a future accidental `SetMeterProvider` can't silently start double-emitting HTTP/DB metrics.
  - Add a guard test asserting `otel.GetMeterProvider()` is the no-op after `telemetry.Setup` (i.e. scope B did not wire metrics).
  - HTTP request metrics continue to come **solely** from the existing `apimw.Metrics` Prometheus middleware (`internal/api/middleware/metrics.go:35`); `otelhttp` contributes **traces only**.
- **Metrics remain fully on Prometheus.** Migrating metrics to the OTel metrics SDK is explicitly out of scope and not planned here — the current `client_golang` instrumentation is retained as-is.

## Blast radius & complexity

Grounded in actual counts (`grep` over `internal/` + `cmd/`): **173 files** contain `slog.*` calls totalling **1,339 call sites** (39 already on the `…Context` variant → **~1,300 to convert**). Total change spans **~185–190 files** and **~2,500–3,200 diff lines**.

Complexity is **concentrated, not spread** — line count is a poor proxy for risk here:

| Phase group | Files | ~LOC | Nature | Risk |
|---|---|---|---|---|
| **Phase 0–2** — telemetry bootstrap, logs fan-out bridge, tracing providers + propagation | ~10–12 (mostly new `internal/telemetry/*` + `cmd/silo/main.go`) | ~900–1,100 net-new | Genuinely new code | **High** — the level-gated fan-out, provider lifecycle, shutdown flush |
| **Phase 3** — call-site sweep | 173 | ~1,300–1,700 modified | Mechanical, codemod-assisted, per-package-green | **Low per file** — a bad conversion fails the build, not runtime; the burden is review volume, not engineering |
| **Phase 4–7** — HTTP/pgx/redis/gRPC/client + domain spans | ~12–15 | ~350–500 net-new | Small, localized seams | **Medium** — ~4 seams are subtly easy to get wrong (gRPC broker-vs-real channel, per-session playback span, chi route timing, struct-bound loggers) |
| **Phase 8** — shutdown wiring, docs | ~2 | ~170 | Wiring + prose | Low |

**Takeaway:** ~90% of the *file count* (the 173-file sweep) is the *safe* part; ~90% of the *risk* lives in the few hundred lines of `internal/telemetry` plus ~4 seams. The scary number (1,300 sites) is mechanical churn.

### Commit strategy

- **Commit 1 — Phases 0–2** (the observability capability itself): telemetry bootstrap + level-gated logs fan-out + tracing providers/propagation. Small, high-value, reviewable as a unit; delivers OTLP log/trace export end-to-end without touching call sites.
- **Commit 2 — Phase 3** (the call-site sweep, in isolation): the ~1,300-site `…Context` + `component` conversion across 173 files, as its own commit so the large mechanical diff never obscures the load-bearing logic in Commit 1. (May itself be split per-package internally, but lands as a distinct changeset from Commit 1.)
- **Later commits — Phases 4–8**: seam tracing + domain spans + shutdown/docs, each landing independently after the foundation is in.

---

## Task 1: Add dependencies and the telemetry SDK bootstrap

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/telemetry/config.go`, `internal/telemetry/config_test.go`, `internal/telemetry/telemetry.go`, `internal/telemetry/telemetry_test.go`

- [ ] **Step 1: Add dependencies.** `go get go.opentelemetry.io/otel go.opentelemetry.io/otel/sdk go.opentelemetry.io/otel/sdk/trace go.opentelemetry.io/otel/sdk/log go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp go.opentelemetry.io/contrib/bridges/otelslog`. Run `go mod tidy`.
- [ ] **Step 2: Write failing config tests** (`config_test.go`) covering: disabled by default (no endpoint, no `SILO_OTEL_ENABLED`); enabled when `OTEL_EXPORTER_OTLP_ENDPOINT` set; `service.name` from `OTEL_SERVICE_NAME` overriding a default of `silo-server`; protocol selection (`grpc` default / `http/protobuf`); sampler default (`parentbased_always_on`) and ratio override; invalid numeric sampler arg falls back to default (not a crash).
- [ ] **Step 3: Implement `config.go`.** Parse `OTEL_*` (respect the OTel env spec) plus a `SILO_OTEL_ENABLED` convenience gate. Default `service.name=silo-server`, `service.version` from build info, `node.id` from the existing `SILO_NODE_NAME`/nodeID signal. Return a fully-defaulted `Config` with an `Enabled bool`.
- [ ] **Step 4: Write failing telemetry test** (`telemetry_test.go`): `Setup` with `Enabled=false` returns a no-op shutdown and installs nothing; `Setup` with a `console`/`none` exporter builds providers and a shutdown that returns nil; shutdown is idempotent.
- [ ] **Step 5: Implement `telemetry.go`.** Follow the canonical `setupOTelSDK` shape: build one shared `resource.Resource` (`resource.WithAttributes(semconv.ServiceName(cfg.ServiceName))`, `WithFromEnv`, `WithProcess`, `WithHost`), a composite W3C `TraceContext{}+Baggage{}` propagator, a `TracerProvider` (`trace.WithBatcher`), and a `LoggerProvider` (`log.WithProcessor(log.NewBatchProcessor(...))`). Register `otel.SetTracerProvider`, `otel.SetTextMapPropagator`, `global.SetLoggerProvider`. **Do NOT build or register a MeterProvider** — metrics stay on Prometheus (see "Prometheus / Grafana coexistence"); leaving `otel.GetMeterProvider()` as the built-in no-op is what keeps the trace instrumentation libraries from double-emitting metrics. Accumulate `shutdownFuncs` and join their errors. When `Enabled=false`, return early with a no-op shutdown so the entire feature is dormant unless configured.
- [ ] **Step 6: Verify.** `go build ./...`, `go vet ./internal/telemetry/`, `go test ./internal/telemetry/ -race`, `gofmt -l` clean.

## Task 2: Logs bridge — fan-out otelslog into the handler chain

**Files:**
- Create: `internal/telemetry/loghandler.go`, `internal/telemetry/loghandler_test.go`
- Modify: `cmd/silo/main.go`

- [ ] **Step 1: Write failing tests** (`loghandler_test.go`): a fan-out handler forwards a record to both a capturing stderr-substitute handler and an otelslog handler backed by an in-memory `LoggerProvider`; an OTel-branch `Handle` error is swallowed and does **not** prevent the console branch from receiving the record (mirrors the `logsink` best-effort contract that keeps `opslog`'s DB path alive — see `internal/opslog/handler.go` "inner error skips capture" behavior).
- [ ] **Step 2: Implement `loghandler.go`.** Provide `NewOTelHandler(lp) slog.Handler` = `otelslog.NewHandler("silo-server", otelslog.WithLoggerProvider(lp))` wrapped so its `Handle`/`WithAttrs`/`WithGroup` never propagate export errors. Provide `FanOut(console, otel slog.Handler) slog.Handler` returning `slog.NewMultiHandler(console, otelBestEffort)` (stdlib, Go 1.26).
- [ ] **Step 2a (REQUIRED — level gating): gate the OTel branch by the shared `LevelVar`.** `slog.MultiHandler.Enabled` returns true if **any** child is enabled. The stderr child is gated by the shared `*slog.LevelVar` (`cmd/silo/main.go:542-543`), but otelslog's `Enabled` follows the (unfiltered-by-default) `LoggerProvider`. Left unfixed, the logger would evaluate `Enabled==true` for `Debug` at `LogLevel=info` — constructing + dispatching Debug records it previously skipped across ~1,200 sites (per-call allocation, violates "Performance first") **and** exporting Debug to OTLP while stderr stays silent (a confusing, silent divergence). Wrap `NewOTelHandler` in a level-gate bound to the same `logLevelVar` (or an equivalent `otelslog` level-bridge option) so console and OTLP share exactly one verbosity knob. Add a test asserting a Debug record is dropped by both branches when the shared level is `info`.
- [ ] **Step 3: Wire into `buildBaseHandler`** (defined `cmd/silo/main.go:132-138`, **called at `cmd/silo/main.go:543`** inside `run()` after the pool is up). When telemetry is enabled, return `FanOut(stderrHandler, levelGated(NewOTelHandler(lp)))` instead of the bare stderr handler; otherwise return today's stderr handler unchanged. The `logfilter` (`:544`) → `opslog` (`:209`, installed `:567`) wrapping above stays exactly as-is, so quiet-filtering and DB capture are preserved and OTel mirrors the console stream (document this mirror behavior, matching the prior file-sink decision). Ordering is safe: `telemetry.Setup` depends only on `OTEL_*` env (not the DB), so call it earlier in `run()` and thread the `LoggerProvider` into `buildBaseHandler` — the provider is available well before `:543`.
- [ ] **Step 3a (coverage caveat): early-boot logs are not exported.** Many `slog` calls fire before the fan-out is installed at `:543` — DB connect (`:389`), migrations (`:423`,`:441`), secret backfill (`:287-305`), auto-tuning (`:223-272`), and everything inside `database.NewPool`. These reach only Go's default stderr handler today (not even opslog), so OTel not capturing them is **not** a regression — but it means "logs export end-to-end" excludes boot. Either document this gap in the observability doc, or (if boot logs matter to operators) move `telemetry.Setup` + a minimal OTel-only tee to the very top of `run()`.
- [ ] **Step 4: Verify** logs actually export. Bring up a local collector (`otel/opentelemetry-collector` with a `debug`/`logging` exporter, documented in Task 10) or use the `console` log exporter; run the server, confirm `slog.Info` records appear on both stderr and the OTLP/console sink. Run `go build ./...`, `go test ./cmd/silo/ ./internal/telemetry/`.

## Task 3: Full call-site sweep — `…Context` variants + `component` attrs

This is the largest task. Batch per-package; each batch is its own commit and must keep the build green. The goal state: every `slog.Info/Warn/Error/Debug` becomes the `…Context(ctx, …)` variant wherever a `ctx` is in scope, and the `"subsystem: message"` prefix convention is replaced by an explicit `slog.String("component", "<subsystem>")` attr. `opslog.InferComponent` stays as the fallback for any residual prefix-only calls (backward compatible — do **not** remove it).

- [ ] **Step 1: Establish the pattern + guardrail.** In a heavily-used package (start with `internal/scanner`), convert calls to `slog.InfoContext(ctx, "msg", slog.String("component", "scanner"), …)`. Confirm `opslog` still classifies correctly (component attr wins over inference — `internal/opslog/handler.go:51-54`). Prototype the `sloglint` rule locally to sanity-check the target form, but do **not** enable it repo-wide yet — it is turned on at `error` severity only as the closing step of the sweep (Task 9), since enabling it before all ~1,300 sites convert turns CI red.
- [ ] **Step 2: Codemod the mechanical conversions.** Use an AST-based rewrite (`gofmt -r` is insufficient for adding a `ctx` arg) — a small `golang.org/x/tools/go/analysis` or `astutil` script that, for each `slog.Info(...)` call inside a function with a `context.Context` in scope, rewrites to `slog.InfoContext(ctx, ...)`. Calls with no reachable `ctx` are left on the non-context variant and flagged for manual review (background init, `main`, top-level goroutines). Review every rewrite — do not blind-commit the codemod.
- [ ] **Step 3: Convert per-package, hottest first**, one commit each, `go build`/`go test` between: `internal/api/handlers/libraries.go` (83), `internal/metadata/service.go` (70), `internal/api/handlers/playback.go` (60), `internal/metadata/worker.go` (45), `internal/recommendations/worker.go` (43), `internal/scanner/scanner.go` (40), `internal/adminjob/runner.go` (37), `internal/api/handlers/plugins.go` (35), then the long tail. For struct-bound `.With("component", …)` loggers (`internal/notifications/*`, `internal/playback/ffmpeg_log_sink.go`, `request_logger.go`, `taskmanager.New(logger *slog.Logger)` at `internal/taskmanager/manager.go:25`), keep the bound `component` and migrate emit calls to the context variants. **Caveat (corrects the "no structural re-plumbing" claim in Validated Findings):** many such structs log from methods that do **not** currently take a `ctx`. For those, either thread a `ctx` parameter through the method signatures (structural change — budget for it) or, where a request/operation ctx genuinely isn't reachable, leave the call on the non-context variant (still captured via fan-out, just without `trace_id`). Span-context propagation is free *only where a `ctx` already flows*; the struct-bound method surfaces are the exception, not the rule.
- [ ] **Step 4: Normalize component names.** Produce a canonical component list (jellycompat, playback, api, scanner, metadata, notifications, catalog, auth, recommendations, adminjob, taskmanager, …) and apply it consistently so trace/log/DB classification agree. Document the list in the package doc.
- [ ] **Step 5: Verify.** `go build ./...`, `go vet ./...`, full `go test ./...`, `gofmt -l` clean, the new sloglint guard passes repo-wide. Spot-check that `opslog` entries still populate `component` after the sweep.

## Task 4: Tracing — HTTP server middleware + request-logger correlation

**Files:**
- Modify: `internal/api/router.go`, `internal/api/middleware/request_logger.go`, `internal/jellycompat/router.go`, `internal/transcodenode/server.go`

- [ ] **Step 1: Add deps.** `go get go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`; `go mod tidy`.
- [ ] **Step 2: Instrument the main router.** Insert `otelhttp` server handling into the chi chain right after `middleware.RequestID` (`internal/api/router.go:213`) and before `apimw.RequestLogger`, so a server span exists for the whole handler and `request_logger` can read it. **Route-tag timing:** chi resolves the route *after* the middleware stack runs, so `http.route` cannot be set at middleware entry — set it post-routing from `chi.RouteContext(r).RoutePattern()`, and commit to `sanitizePath` (`metrics.go:79-89`) on the raw path as the fallback when no pattern is available. Exclude `/health`/`/ready` like the logger already does (`request_logger.go:22`). Note `activitylog.NewMiddleware` (`internal/api/router.go:230`) is also request-scoped and may want the span/`trace_id` — evaluate wiring it in the same pass.
- [ ] **Step 3: Correlate logs.** In `request_logger.go:53-70`, pull the active span from `r.Context()` and add `slog.String("trace_id", …)`/`slog.String("span_id", …)` to the emitted attrs. (Once Task 3 lands and handlers log with `ctx`, the otelslog bridge attaches these automatically to every downstream record too — this step guarantees the access-log line itself carries them.)
- [ ] **Step 4: Instrument jellycompat + transcode-node servers** the same way (`internal/jellycompat/router.go:30`, `internal/transcodenode/server.go`), ensuring incoming W3C `traceparent` headers continue the trace across processes.
- [ ] **Step 5: Verify.** Drive a request through a local collector; confirm one server span per request with `http.route`, `trace_id` present in the access log, and a continued trace when hitting a transcode-node endpoint.

## Task 5: Tracing — outbound HTTP clients (shared traced transport)

**Files:**
- Create: `internal/telemetry/httpclient.go`
- Modify: `internal/nodepool/health.go`, metadata/subtitle provider packages, `internal/plugins` HTTP proxy

- [ ] **Step 1: Shared factory.** Implement `telemetry.NewHTTPTransport(base http.RoundTripper) http.RoundTripper` = `otelhttp.NewTransport(base)` with sane span-name/route options. This satisfies CLAUDE.md's "avoid duplicate logic" — one wrapper, injected everywhere, not per-package patches.
- [ ] **Step 2: Inject** the traced transport into the ad-hoc `http.Client` literals: `internal/nodepool/health.go:22-26` and other nodepool clients, `internal/metadata/tmdb`, `internal/metadata/trakt`, `internal/metadata/translation`, `internal/mdblist`, `internal/subtitles/{opensubtitles,subdl,subsource}`, and `internal/plugins.HTTPProxy` (`router.go:149`). Prefer constructing the transport once in `cmd/silo/main.go` and passing it down over editing each package's client construction independently where a seam exists.
- [ ] **Step 3: Verify.** A play/scan flow that calls a metadata provider and a transcode node shows child client spans nested under the originating server span, with propagated `traceparent`.

## Task 6: Tracing — pgx pool + Redis

**Files:**
- Modify: `internal/database/postgres.go`, `internal/cache/redis.go`, `go.mod`

- [ ] **Step 1: pgx tracer.** `go get github.com/exaring/otelpgx`; set `poolCfg.ConnConfig.Tracer = otelpgx.NewTracer(...)` on the config returned by `pgxpool.ParseConfig` (`internal/database/postgres.go:14`), guarded on telemetry enabled. Zero call-site changes — instruments every query. Use `otelpgx` options to avoid logging full SQL args if that is a concern.
- [ ] **Step 2: Redis hook.** `go get github.com/redis/go-redis/extra/redisotel/v9`; call `redisotel.InstrumentTracing(client)` after **both** `redis.NewClient` constructions (`internal/cache/redis.go:145-254`).
- [ ] **Step 3: Verify.** A request that hits Postgres and Redis shows DB and cache child spans under the server span. `go build ./...`, `go test ./internal/database/ ./internal/cache/`.

## Task 7: Tracing — host-side plugin gRPC propagation

**Files:**
- Modify: `internal/pluginhost/host.go`, plugin gRPC client dial sites, `go.mod`

- [ ] **Step 1: Add dep.** `go get go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc`.
- [ ] **Step 2: Find the real invocation channel FIRST.** `grpc.NewServer(opts...)` at `internal/pluginhost/host.go:336` sits inside `broker.AcceptAndServe(...)` — a hashicorp/go-plugin **brokered sub-stream** (host services exposed back to the plugin), *not* the primary host→plugin RPC channel (go-plugin stands that up internally; silo does not construct it at this line). Instrumenting only `:336` would capture the reverse/broker channel and **miss primary plugin invocations**. Identify where the primary plugin client connection is created/dialed (the load-bearing path) before instrumenting.
- [ ] **Step 3: Client side (load-bearing).** Add `grpc.WithStatsHandler(otelgrpc.NewClientHandler())` to the host→plugin dial that carries actual invocations (dial pattern per `internal/pluginhost/client_test.go:18`). This is the half that must cover the real call path.
- [ ] **Step 4: Server side.** Add `grpc.StatsHandler(otelgrpc.NewServerHandler())` to `grpc.NewServer(...)` at `:336` for the brokered channel — useful but secondary.
- [ ] **Step 5: Note the boundary.** The plugin-subprocess side that receives `traceparent` is owned by `silo-plugin-sdk` — record a coordination follow-up; do **not** attempt SDK-side changes here.
- [ ] **Step 6: Verify against a real plugin call.** Confirm an actual plugin invocation (not just a broker sub-stream) produces a gRPC client span; full end-to-end continuation into the plugin process is deferred to the SDK follow-up.

## Task 8: Domain spans — scanner, playback, taskmanager

**Files:**
- Modify: `internal/scanner/scanner.go`, `internal/playback/session.go`, `internal/taskmanager/manager.go`

- [ ] **Step 1: Scanner.** Start a span per scan run in `applyScopedScan` (`internal/scanner/scanner.go:1287`) and, if not too chatty, a child per file. Attach library/scope attrs.
- [ ] **Step 2: Playback.** Do **not** span `NewSessionManager` (`internal/playback/session.go:152`) — that is the singleton manager constructor, called once at startup; a span there covers the whole process lifetime and cannot carry per-session attrs. Instead attach the span at the actual per-session create/start path (where a `*Session` is added to the `sessions` map), spanning start→stop with `play_method`/session-id attrs, so it links to the transcode-node client spans (Task 5) for end-to-end session traces.
- [ ] **Step 3: Taskmanager.** Wrap `RunTask` (`internal/taskmanager/manager.go:231`) in a per-run span; these background goroutines start new root traces (no incoming context), so create a fresh root span with baggage identifying the task.
- [ ] **Step 4: Verify.** A scan and a playback session each produce a coherent trace tree; background task runs appear as their own traces.

## Task 9: Durable enforcement — keep future code (human + AI) on the standard

The sweep is worthless if new code drifts back to context-less `slog.Info(...)`. Lock the standard in machine-enforced form so it holds without relying on reviewer vigilance. **Sequencing: lands together with the Phase-3 sweep in the same commit, after the legacy sites are converted — enabling it earlier turns CI red repo-wide.**

**Files:**
- Modify: `.golangci.yml`
- Reference: `docs/architecture/observability.md` (from Task 10)

- [x] **Step 1: Machine gate (the real guarantee) — add `sloglint` to `.golangci.yml`.** Added to `linters.enable` with all four rules configured: `context: "scope"`, `static-msg: true`, `key-naming-case: snake`, `no-mixed-args: true`. Since `make lint` runs `golangci-lint run` (`Makefile:54`), this blocks any PR — human- or AI-authored — that regresses the call form. Verified via standalone `sloglint`: after the sweep all four rules report **0 violations** repo-wide (production and test), so no `_test.go` exclusion was needed. (`key-naming-case` and `no-mixed-args` were already clean pre-sweep; `static-msg` had 6 sites, fixed in the sweep.)
- [x] **Step 2: Document the honest gap.** Recorded in `docs/architecture/observability.md`: `sloglint` enforces the *shape* (`…Context`, snake keys, static msg) but **cannot** enforce that a `component` attr is present. That convention rests on the observability doc, the canonical component list, and review.
- **Note (CLAUDE.md subsection dropped):** per maintainer direction, no Logging subsection is added to `CLAUDE.md`. The post-sweep codebase already models the idiom everywhere and the `sloglint` gate enforces it, so the standard holds without a CLAUDE.md entry; `docs/architecture/observability.md` carries the human/LLM-facing rationale and canonical list.

## Task 10: Shutdown wiring, docs, and final verification

**Files:**
- Modify: `cmd/silo/main.go`
- Create: `docs/architecture/observability.md`

- [ ] **Step 1: Graceful shutdown.** `defer shutdown(ctx)` from `telemetry.Setup` in `run()`, integrated with the existing signal handler, using a **generous** timeout so batch processors flush (buffered spans/logs are lost otherwise). Ensure it runs before process exit on every path.
- [ ] **Step 2: Docs.** Write `docs/architecture/observability.md`: the `OTEL_*`/`SILO_OTEL_ENABLED` surface, default-off behavior, a local `docker compose` collector example (OTLP in → debug/Loki/Tempo out), the canonical component list, and the documented non-goals/risks (metrics stay Prometheus; logs unredacted at the OTLP sink; quiet-filter also suppresses OTel logs; plugin-SDK side is a follow-up).
- [ ] **Step 3: Full verification.** `go build ./...`; `go vet ./...`; `go test ./... -race` (at least the touched packages under `-race`); `gofmt -l` clean; `cd web && pnpm run lint && pnpm run format:check` (no-op for this backend change but part of the MR gate); `make verify-local-paths`. Manual: default-off start (no OTel env) behaves exactly as today; enabled start exports logs+traces to a local collector; a single request produces correlated log `trace_id` + a server→DB→cache→client span tree. **Metrics coexistence:** with OTel enabled, confirm `/metrics` still serves the full existing Prometheus set (HTTP + domain metrics), Grafana scraping is unaffected, and the guard test confirms `otel.GetMeterProvider()` is the no-op (no OTel metrics rail wired).

---

## Risk / follow-ups

- **Level-gating is load-bearing (see Task 2 Step 2a).** `slog.MultiHandler.Enabled` is an OR across children; without gating the OTel branch by the shared `LevelVar`, Debug records get allocated + OTLP-exported at `level=info`. Treated as a required step, not optional.
- **Early-boot logs (pre-`:543`) are not exported** — matches existing opslog behavior, documented not fixed (Task 2 Step 3a).
- **Playback span must attach per-session, not at the manager constructor**, and **plugin gRPC instrumentation must target the primary invocation channel, not just the broker sub-stream** — both are easy to get wrong (Tasks 8/7).
- **Call-site sweep is a large, review-heavy diff.** Mitigated by per-package batching, an AST codemod with mandatory human review, and a `sloglint` guardrail. Background/init calls with no `ctx` legitimately stay on the non-context variants.
- **Quiet-filter also suppresses OTel logs** (the fan-out sits at the base, below the filter). This mirrors stderr exactly — intended, but if durable-capture-despite-quiet is wanted, the OTel branch must tee in above `logfilter`. Deferred, product call.
- **Redaction is key-based** (handled via `internal/logredact` on all sinks): secrets under a recognized key are masked, but a secret in a free-text message or under an unrecognized key is not caught.
- **Metrics remain on Prometheus** by explicit non-goal; dual observability backends until a future OTel-metrics plan.
- **Plugin-subprocess trace continuation** needs a coordinated `silo-plugin-sdk` change; host-side only here.
- **Sampler config in multi-node deploys** must use `parentbased_traceidratio` (not bare `traceidratio`) so transcode/proxy nodes respect the parent decision — documented in Task 10.
- **Batch-processor flush on shutdown** is critical; verify no lost telemetry on redeploy/signal paths.

## Verification summary (gate before MR)

- `go build ./...`, `go vet ./...`, `go test ./... -race` (touched packages), `gofmt -l` clean.
- `cd web && pnpm run lint && pnpm run format:check`; `make verify-local-paths`.
- Manual: default-off parity; enabled logs+traces to a local collector; correlated `trace_id` in logs; end-to-end span tree across HTTP → pgx → redis → outbound client → gRPC.
