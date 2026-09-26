# Profiling one Silo process

Silo can serve Go profiles on an optional local HTTP listener. Enable it in the
process environment, then restart that process:

```dotenv
SILO_DEBUG_LISTEN=127.0.0.1:6060
```

The default is disabled. Literal IPv4 and IPv6 loopback addresses are accepted;
hostnames, wildcard addresses, non-loopback addresses, and port zero are rejected.
Configuration is read after dotenv loading and before database connection work.
Integrated, API, frontend, proxy, and transcode serving modes share this lifecycle.
Migration and compatibility-web utility commands do not enable it. A malformed
setting fails startup. An occupied port or listener failure is logged and leaves
the application running. Scrape `silo_debug_listener_available` and
`silo_debug_listener_failures_total` to distinguish absence from a healthy listener.

The listener is an operational interface outside `/api/v2`. It exposes no native
client operations and requires no Apple, Android, Jellyfin, or ABS client changes.
The primary and compatibility listeners do not serve Go profiles. A frontend SPA
may answer an unknown profiling path with HTML, so check response content rather
than assuming every absent endpoint returns 404.

## Access boundaries

Only local processes in the same network namespace can connect. Those processes
and local users are trusted; loopback binding does not isolate tenants sharing a
host or pod. Use OS/container isolation where they are not mutually trusted.
Requests require GET, a literal loopback Host authority, and local browser-origin
headers when present. Use literal addresses in tools rather than `localhost`.
Forwarded local ports are supported. Profiles are served with `Cache-Control:
no-store` and can contain symbols, paths, and stack information. Keep them in
private incident artifacts and delete them after incident review.

Do not publish the profiling port with Docker, ingress, a public proxy, or a
load-balanced service. Each capture describes one process. The main application
listener does not serve `/metrics`. Metrics are disabled by default and can be
enabled on a separate operator listener with
`SILO_METRICS_LISTEN=127.0.0.1:9091`. The dedicated listener is unauthenticated;
bind it only to a loopback or private monitoring network. Prometheus can scrape
`http://127.0.0.1:9091/metrics` when it shares that network namespace.

## Collecting bounded captures

Commands assume the repository root is the current directory. The helper needs
Node.js, which is already included in the Silo runtime image. It streams captures
to private files, defaults to a 128 MiB maximum output, and imposes a 75-second
total request deadline. It never overwrites an existing artifact.

```sh
mkdir -m 700 incident
scripts/silo-profile --profile cpu --seconds 30 --output incident/cpu.pprof
scripts/silo-profile --profile heap --gc 1 --output incident/heap.pprof
scripts/silo-profile --profile allocs --seconds 30 --output incident/allocs.pprof
scripts/silo-profile --profile goroutine --output incident/goroutine.pprof
scripts/silo-profile --profile trace --seconds 1 --max-bytes 67108864 --output incident/runtime.trace
```

Every capture gets a JSON sidecar with revision, Go version, instance identity,
sampling settings, start/end times, byte count, response trailers, and checksum.
The helper sets `valid: true` only after a complete HTTP response with an explicit
uninterrupted-capture trailer. Tool parsing below is the additional format check.
An oversized, disconnected, interrupted, or failed capture stays under the
`.partial` name with `valid: false`; never analyze it as a complete capture.
SIGINT/SIGTERM cancel the request and update the sidecar. A forced process kill
leaves the initial incomplete sidecar and partial file for inspection.

Retain the exact matching Silo executable privately with the capture. Build
revision metadata identifies the source; the executable supplies full symbols
for disassembly and comparisons. Record image digests for container deployments.

### Docker

Container loopback belongs to the container network namespace. Publishing a host
port cannot reach a listener bound only to that loopback. Stream the helper into
the running container and retrieve the resulting private artifacts:

```sh
docker exec CONTAINER sh -c 'umask 077; mkdir -p /tmp/silo-incident'
docker exec -i CONTAINER node - --profile heap --gc 1 --output /tmp/silo-incident/heap.pprof < scripts/silo-profile
docker cp CONTAINER:/tmp/silo-incident/. incident/
docker exec CONTAINER sh -c 'cat "$(command -v silo)"' > incident/silo
chmod 600 incident/silo
```

The image already provides curl for immediate listener checks:

```sh
docker exec CONTAINER curl --fail http://127.0.0.1:6060/debug/pprof/
```

On a Linux host with Node installed, a privileged operator can instead enter
only the container's network namespace. The capture and helper stay on the host:

```sh
container_pid=$(docker inspect --format '{{.State.Pid}}' CONTAINER)
sudo nsenter --target "$container_pid" --net node scripts/silo-profile --profile heap --output incident/heap.pprof
```

### Kubernetes and SSH

Target an individual pod. Where the container runtime supports loopback port
forwarding, keep the forwarding bind on a literal local address:

```sh
kubectl port-forward --address 127.0.0.1 pod/POD 16060:6060
scripts/silo-profile --url http://127.0.0.1:16060 --profile heap --output incident/heap.pprof
```

If that runtime cannot forward to pod loopback, use `kubectl exec` with the same
Node helper pattern as Docker. For a listener running in an SSH host's network
namespace:

```sh
ssh -N -L 127.0.0.1:16060:127.0.0.1:6060 OPERATOR_HOST
scripts/silo-profile --url http://127.0.0.1:16060 --profile heap --output incident/heap.pprof
```

An SSH host's loopback is different from its containers' loopback. Use container
exec or namespace access for that case. Validate the chosen topology's recipe
against the running image; forwarding support and host tool availability vary.

## Choosing and interpreting profiles

| Profile | Diagnostic use | Bound |
| --- | --- | --- |
| `profile` / helper `cpu` | Where Go execution spends CPU time | Default 30 seconds; maximum 60 |
| `heap` | Sampled live Go allocations; `gc=1` requests a GC first | Immediate, or delta up to 60 seconds |
| `allocs` | Allocation churn, including objects already collected | Immediate, or delta up to 60 seconds |
| `goroutine` | Stack ownership and growing goroutine populations | Immediate, or delta up to 60 seconds |
| `threadcreate` | Stacks leading to OS thread creation | Immediate, or delta up to 60 seconds |
| `block` | Time blocked on synchronization | Requires explicit startup sampling |
| `mutex` | Lock contention attributed to holder stacks | Requires explicit startup sampling |
| `trace` | Scheduler, GC, and goroutine timing | Default 1 second; maximum 5 |

Only one capture runs per process, including snapshots and forced GC. Concurrent
work fails immediately with HTTP 429 and increments
`silo_debug_captures_busy_total`. There is no waiting queue. Unknown, duplicate,
malformed, or excessive parameters are rejected before profiling work starts.
Client disconnect and server shutdown cancel active timed captures. Runtime GC
or stack collection may finish before cancellation takes effect; the capture
slot remains occupied until the handler exits. Duration limits do not bound
stop-the-world pauses or bytes generated, hence the helper's independent output
cap. Expensive delta profiles may also allocate internal buffers in Go itself.

Use the Go release matching the captured binary:

```sh
go tool pprof -top incident/silo incident/cpu.pprof
go tool pprof -sample_index=inuse_space -top incident/silo incident/heap.pprof
go tool pprof -sample_index=alloc_space -top incident/silo incident/allocs.pprof
go tool pprof -http=127.0.0.1:8088 incident/silo incident/goroutine.pprof
go tool pprof -diff_base=incident/before.pprof -top incident/silo incident/after.pprof
go tool trace -d=parsed incident/runtime.trace
go tool trace -http=127.0.0.1:8088 incident/runtime.trace
```

The trace debug spelling above is for Go 1.26. The listener deliberately omits
`cmdline`, `symbol`, generic expvar, and dynamically discovered profile names.
Current Go pprof tooling reads symbols carried in these profiles; direct URL
fetching is also covered by compatibility tests.

To investigate contention, explicitly enable bounded startup sampling and
restart the target process:

```dotenv
SILO_DEBUG_BLOCK_RATE=10000000
SILO_DEBUG_MUTEX_FRACTION=100
```

The block value is average blocked nanoseconds per sample (allowed nonzero range:
1,000,000–1,000,000,000). The mutex value samples one in N contention events
(allowed nonzero range: 100–1,000,000). Both default to zero and require the
listener to be enabled. Disabled profiles return HTTP 409 and appear as disabled
in the index. They provide no evidence that contention is absent. Restore zero
and restart after the investigation. The Go memory sampling rate stays unchanged.

## Resource accounting limits

Go heap profiles describe sampled Go allocations. They do not measure all
libvips/native allocations, FFmpeg subprocess memory, plugin internals, filesystem
cache, or GPU allocations. Compare heap with process RSS, container memory,
host memory, and workload measurements using their stated scopes. Do not add
these overlapping scopes as if they were independent resources.

Completed-child metrics consume the owner's existing process exit state:
`silo_subprocess_cpu_seconds_total` reports user/system CPU at completion;
`silo_subprocess_exits_total` counts bounded workload/outcome categories; and
`silo_subprocess_peak_rss_bytes` is a distribution of per-child maximum RSS.
Maximum RSS is a peak, not current memory or a summable concurrent total. Linux
KiB values are normalized to bytes; Darwin already reports bytes. Unsupported
platforms omit maximum-RSS samples. Instrumented owners include playback
transcode/restarts, progressive remux, scanner probes, and plugin shutdown/failed
startup. Plugin accounting is delayed until the host joins the existing plugin
process owner. Other FFmpeg uses need their own owner instrumentation before
claiming coverage. No second waiter or reaper is installed.

Compare equivalent workloads and binaries before and after a change. Measure
ordinary metrics, sampled tracing, CPU profiling, execution tracing, and
contention sampling separately; their overhead and attribution differ. Use
native-library or OS profiling tools when RSS/CPU evidence points outside Go.
