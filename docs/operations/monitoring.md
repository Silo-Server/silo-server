# Monitoring Silo

Use the examples in `deploy/observability/` with private Silo API and worker
scrape targets. Load `silo.rules.yml` into Prometheus and import
`grafana-dashboard.json`, choosing its Prometheus datasource. Add a `cluster`
label to each target. Use `process_role` for the deployment role: `role` already
identifies metric-owned database/cache/storage pools and must not be overwritten
by target relabeling.

Commands assume the repository root is the current directory.

The application listener does not expose metrics. Enable the dedicated listener
only on a private monitoring address, for example
`SILO_METRICS_LISTEN=127.0.0.1:9091`, and point Prometheus at
`http://127.0.0.1:9091/metrics`. The listener is disabled when the variable is
unset and does not require application credentials; network placement is its
access boundary. Do not publish the port through a public ingress or host-wide
port binding.

```sh
docker run --rm --entrypoint promtool -v "$PWD/deploy/observability:/etc/prometheus:ro" prom/prometheus:v3.5.0 check config /etc/prometheus/prometheus.yml
docker run --rm --entrypoint promtool -v "$PWD/deploy/observability:/etc/prometheus:ro" -w /etc/prometheus prom/prometheus:v3.5.0 test rules silo.rules.test.yml
```

The examples use a 15-second interval, 10-second timeout and 20,000 samples per
scrape. Check `scrape_samples_scraped`, scrape bytes and duration on the deployed
build before using those limits for a large installation. A sample-limit failure
rejects the entire scrape. Set Prometheus retention time/size and OTLP backend
retention separately; neither is controlled by a Silo administrator setting.

Host/device/dependency exporters complete coverage outside the application.
Use node-exporter for host/filesystem/network errors and latency, a container
exporter for orchestration quotas and OOM history, GPU vendor exporters for
whole-device power/thermal limits, and Postgres/Redis/storage exporters for
service-side health. Restrict those exporters to the monitoring network too.

## Reading a failure

| Symptom | Detection and next evidence |
| --- | --- |
| CPU saturation or throttling | Compare process CPU with reported cgroup quota and throttled periods. Capture a short CPU profile of the responsible Silo process. Compare child CPU and whole-host competition separately. |
| Growing memory or OOM | Compare process RSS, Go heap/allocations, live children and cgroup current/events. Take comparable heap profiles. Check runtime GC and allocation rate; use native tooling for libvips. Retrieve orchestrator exit/OOM history if the process died before scraping. |
| Slow API with idle CPU | Inspect dependency acquire/query/command latency, Postgres acquired versus maximum connections and Redis wait timeouts. A goroutine or short execution trace can reveal waiting owners. A query duration includes client-side iteration; acquisition is measured separately. |
| Increasing queue age | Check sampling availability first. Deduplicate queue replicas, inspect retry gates, execution outcomes and progress, then administrator job state. A healthy heartbeat is liveness, not progress; one advancing job can mask another stalled job in aggregate progress. |
| Stream interruption or lost node | Check `up`, node health freshness, route outcomes and existing streamtelemetry. Correlate bounded worker spans and FFmpeg exit outcomes. Verify recovery on another node with the same media before declaring a node restart harmless. |
| Missing traces | Check configured sample rate and export outcome counters, collector connectivity and self-metrics. Completed-minus-exported spans includes queued/exporting/lost data; inspect after drain. Requests should keep succeeding through exporter failure. |
| Missing hardware values | Inspect source availability and timestamps; a missing value does not mean zero consumption. Denied procfs, unavailable driver tools and inaccessible mounts require fixing the measurement source. |

Raw profiles remain on the [local profiler](profiling.md). Do not attach captures,
private URLs, account identifiers or media details to public reports. Public
metric names and sanitized outcomes are enough for an initial report.

## Regression and fault exercises

Run disruptive cases in an isolated instance with synthetic media and its own
database/cache. Pin source SHA, image digest, Go version, CPU/memory limits and
all profiling/OTLP settings in private evidence before each run.

1. Establish baseline request p50/p95/p99, throughput, CPU, RSS, allocations,
   scrape bytes/duration/sample count and stream stability on matching workloads.
   Run repeated warm samples with metrics, then sampled traces, CPU profiles,
   execution traces and contention sampling separately. Compare medians and noise;
   targets are at most 2% CPU/throughput and 5% p95 request latency regression.
2. Enable the debug listener and collect CPU/heap/allocs/goroutine/trace captures
   through the process network namespace. Read profiles with Go tools; check
   metadata validity. Test a second capture, client disconnect, duration overflow,
   untrusted Host/Origin, and shutdown while capturing. Verify main and compat
   listeners never return profile data.
3. Occupy the requested debug port before startup. Workload readiness must still
   succeed, with debug availability zero and an explicit listener failure.
4. Point OTLP at an unavailable local collector, send requests beyond the batch
   capacity and drain. Requests must finish, memory must remain bounded, and
   exporter failures must be visible. Do not interpret exporter acceptance as
   backend durability without collector-side evidence.
5. Exhaust a one-connection Postgres/Redis pool in a test harness. Observe wait,
   timeout and recovery after release; test cancellation too. Stop the disposable
   dependency and confirm errors without leaking queries, keys or endpoints.
6. Exercise cgroup quota throttling, unreadable procfs fixtures, stale disk probes,
   long queues, delayed retries and unavailable resource sources. Only disposable
   instances should receive deliberate OOM tests. Distinguish terminal job state
   from admission and from a completed outer scheduled task.
7. Run integrated and worker instances with concurrent scan/image/browse/direct
   stream/transcode/download activity. Stop an isolated worker during streaming,
   verify recovery and ensure shared queue/host/GPU aggregation does not double
   count replicas. Validate container-exec access; validate pod forwarding on the
   actual Kubernetes runtime if that topology is deployed.

The committed rules tests exercise alert logic with synthetic series. Package
tests cover Linux source parsing, unavailable states, dependency saturation,
queue variants, capture lifecycle, profile parsing and metric privacy. These do
not establish a universal production overhead bound, every GPU vendor's behavior,
or Kubernetes forwarding support. Record the actual deployment exercises and
remaining gaps in release evidence.
