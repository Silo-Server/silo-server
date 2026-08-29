---
title: Monitoring Stream Nodes
description: How to read the admin Nodes page, when to re-probe a node's hardware, and how scratch-disk pressure affects transcode placement.
summary: How to read a node's Acceleration, Load, and Capacity blocks, the GPU re-probe action, scratch admission, and scraping node metrics with Prometheus.
tags:
  - silo
  - docs
  - wiki
  - nodes
  - gpu
  - monitoring
audience:
  - operator
last_reviewed: 2026-08-27
related:
  - ../index.md
  - ../deployment/docker.md
---

# Monitoring Stream Nodes

**Settings -> Nodes** lists every registered proxy and transcode node with its
current health, its verified GPU capability, and its host resource usage. This
page explains what each reading means, which of them affect where playback is
placed, and what to do when the page disagrees with the hardware you know is in
the machine.

Everything here applies to a distributed deployment. A single-node install has no
registered nodes, and its own resource usage appears on the admin dashboard
instead.

## Reading a node

Each node renders as one unit: a header naming the node, its state, its
co-location group, and its URL — with the enable switch and the actions beside
them — over three labelled blocks. **Acceleration** is what the node's FFmpeg
verified it can do, **Load** is the host underneath it, and **Capacity** is what
it is carrying against any caps it was given. Readings measured against a
ceiling draw a meter; a reading with no ceiling, or one nothing measured, draws
none — a bar at zero would read as "measured and idle", which is the one thing
an unmeasured value is not.

When any node carries a group label, a **Groups** chip row above the sections
filters both of them at once — a group's proxy sits in one section and its
transcode nodes in the other, and the filter is how to see the pairing. A
group's chip carries an amber dot while an enabled member is unhealthy, because
that takes the whole group out of service: its transcode nodes stop taking
work, and a group with proxies of its own never falls back to another group's.

### State

The rail on a unit's left edge, the dot in its header, and one label carry the
node's state. **Disabled** is the administrator's switch: a disabled node is in
no pool, is never selected, stops counting against its co-location group, and
renders dimmed — its readings are whatever they were when it left the rotation.
**Healthy** and **Unhealthy** are the result of the last check. Every node is
polled every 30 seconds; a node that does not answer is routed around
immediately, and existing streams on it fail over on their next segment
request. **Checked**, in the Capacity block, is how long ago that poll ran —
hover for the exact time.

Health and capability are independent. A node whose GPU driver broke stays
perfectly healthy: it still answers, it just encodes in software now. That is
what the Acceleration block and the drift badge exist to surface.

### Acceleration

The Acceleration block reports what the node's FFmpeg could actually *do* the last time
it was asked, not what its configuration names. Silo verifies hardware by running
a real single-frame encode on each candidate device, so the states are evidence,
not guesses:

| Reading | Meaning |
|---|---|
| **QSV / VAAPI / NVENC**, green | The backend passed its FFmpeg probe on this node. Hover for the device it passed on. |
| **QSV / VAAPI / NVENC**, amber | The backend is configured and in use, but its probe *failed*. Hover for the reason FFmpeg gave. Transcodes will attempt it anyway, because an explicitly configured backend is honored verbatim. |
| **QSV / VAAPI / NVENC**, plain | The backend is in use but the node reported no verification for it — normal when a backend is configured on a node with no candidate devices to probe. |
| **SW** | No hardware backend verified: this node encodes in software. |
| **SW**, "devices not accessible" on hover | Every candidate device was *skipped* rather than probed, because this process cannot open any of them. This is the normal reading for a proxy node reading a cluster-wide `playback.hw_device` that points at the transcode nodes' cards. It is not a driver failure. |

Below the badge, each render device the node can see is listed with its live
video-engine busyness — a meter and a session count per device — where a
measurement source exists; an unmeasured device shows a dash and no meter.

**`stale`** in the Acceleration block means no health check has *confirmed* this inventory
for more than ten minutes. Checks run every 30 seconds and every response carries
the node's current capability hash, so roughly twenty checks in a row have to go
missing before the marker appears — it says the confirming sweep stopped, not
that the report is old. An old report is normal and is deliberately not flagged:
a node recomputes its snapshot every 15 minutes, the server refetches only when
the advertised hash changes, and a node whose GPU has not changed in a week is
serving a week-old report that is still true. An unhealthy node is never marked
stale, since it cannot confirm a report at all.

**`Shared GPU`** marks a node whose physical card is also visible to another
registered node — two containers on one GPU, most often. Silo detects this from
each report's device identity (NVIDIA GPU UUID where available, otherwise the PCI
slot scoped to the host's boot id) and uses it when placing work: among transcode
nodes level on job count, the one whose *card* carries the fewest jobs wins.
Without that, spreading sessions across node records that share silicon would not
spread the work at all.

**Drift**, an amber badge, means a capability refetch found the node's hardware
got *worse* than the report it replaced: a backend that used to pass its probe
now fails, or a render device is gone. Hover for the note. The badge records what it
lost and stays until exactly that comes back — the backend verifying again, the
card answering to one of the identities it had. It is not erased by a refetch
that merely loses nothing further, so a reboot or a reworded FFmpeg error cannot
make a standing regression look repaired; nor by the surviving card on a
multi-GPU node probing cleanly; nor by an unrelated GPU being added, which grows
the inventory without repairing anything. Cards lost one at a time must all
return. Re-probing the node is the
direct way to ask whether it is still true. It is a warning, not a routing input
— nothing in node selection reads it.

### Load and Capacity

**Load** is CPU, memory, the fullest sampled disk, and network throughput,
sampled by the node itself every five seconds and carried on its health
response. It is the current sample only; Silo keeps no history (see [Scraping
node metrics](#scraping-node-metrics) below). Network draws no meter: the
sampler reports bytes moved, never the link's speed, so there is no ceiling to
draw it against.

**Capacity** is the node's concurrency (transcodes, or relayed streams on a
proxy) and, on proxy nodes, measured egress — each against its configured cap
where one is set. An uncapped reading shows the bare number and no meter, since
any bar would be measured against a ceiling the page invented. The readings
tint once a cap is reached, which is when the planner routes new work
elsewhere.

An unhealthy node shows a dash rather than its last numbers: the sample predates
the check that failed, and a frozen CPU percentage looks exactly like a live one.
A dash on a *healthy* node means the node reported no sample — sampling is
Linux-only, and a node running an older build reports none.

The disk reading is the fullest mount the node can see, and it tints when that
mount passes 85% used. A mount whose server stopped responding shows its last
good numbers rather than blocking the health response; a path the node cannot see
at all is reported as unmeasurable rather than as an empty disk.

In a container these numbers are the *container's*, corrected against its cgroup
— but only where the cgroup actually caps something. A CPU quota or cpuset the
size of the whole machine restricts nothing, and both are ordinary: an
unconstrained container inherits a cpuset holding every online CPU, and a
deployment sized to the box writes a matching quota. Silo reads those as
uncapped and keeps reporting the host, because on a shared machine the CPU a
neighbor burns is CPU this node cannot have. A real cap — two cores of a
sixty-four core host — switches both the busy figure and the core count to the
cgroup's. This is also true on an LXC host running Docker nested inside it, which
needs three bind-mounts to read its own limits instead of the physical
machine's. See the LXC
notes in [Deploy Silo with Docker](../deployment/docker.md#node-metrics),
including the caveat that lxcfs only virtualizes `/proc/loadavg` when it runs
with loadavg accounting enabled (`lxcfs -l`; off by default on Proxmox), so
`load1` can remain the physical host's while CPU and memory are correct.

## Re-probing a node's hardware

Each node's header has a **Re-probe** action beside Check. It tells that node to throw
away its cached hardware verdicts, re-verify against live hardware, and hand the
fresh inventory straight back to the server.

It exists because a *successful* probe is cached for the node's whole process
lifetime. That is deliberate — re-verifying on every playback request would put
FFmpeg executions on the playback path — but it means a node goes on reporting
hardware that has since stopped working:

- **After a GPU driver upgrade, downgrade, or reinstall.** A card that worked
  when the node started keeps reading as verified until the node restarts, even
  once the new driver cannot encode a frame.
- **After changing which devices a container can open** (removing a `/dev/dri`
  passthrough, switching the NVIDIA overlay, changing group membership).
- **After replacing an FFmpeg build in place**, where the path did not change.
- **To confirm a drift badge is still true**, rather than a transient failure
  during a driver reload.

The opposite direction needs no action. A *failed* GPU probe is cached for only
15 seconds, so a card that was broken when the node started and has since been
repaired verifies on its own; that flips the node's capability hash and the
server picks it up within one 15-minute snapshot. The exception is the tone-map
matrix, which caches any non-empty result permanently: a host whose GPU was
broken at start can stay software-only for tone mapping until it is re-probed or
restarted.

Re-probe does not restart anything and does not reload configuration. It is
**refused on a node that is transcoding**: every probe ends in a real encode on
the GPU, and a card at its concurrent session limit fails that encode with an
error nothing can tell apart from a broken driver — which would flag working
hardware as a regression and take the node's tone-map inventory down with it.
Disable the node or wait for it to drain, then re-probe. "Transcoding" covers
everything that opens an encoder on that node — playback transcodes,
reconstructed sessions, prepared downloads, and hardware chapter-thumbnail
extraction — and the exclusion runs both ways: while a re-probe is in progress
the node refuses new GPU work with a 503 rather than queueing it, so the API
places that session elsewhere, and its own scheduled capability snapshot stands
down until the re-probe finishes. A scheduled snapshot does not refuse while
transcodes run, though: a node under sustained load would otherwise never
refresh its inventory at all. On an idle node the call can take a couple of
minutes, because it pays the full cold probe cost on purpose.

Two outcomes are worth knowing:

- On success the row's capability report, verified backends, and drift note are
  all updated before the action returns — you are not waiting for the next
  30-second sweep.
- If the node's probe cannot finish, the action reports an error and the node
  **keeps its previous report**. An unfinished probe is not evidence that the
  hardware changed, so nothing is overwritten. Re-running it after the node
  settles is safe.

Force reload is a different tool: it makes a node re-read its configuration (and
tears down its sessions to do so). Use it after changing a node's acceleration
overrides; use Re-probe after changing the hardware or the driver under it. It
has no button today — it is API-only, `POST
/api/v1/admin/nodes/{id}/force-reload`.

## Scratch admission

A transcode writes HLS segments to its node's scratch directory for the entire
life of a session. A node that is nearly full therefore does not fail fast: it
accepts the session, streams for a while, and then dies with a write error after
the client has already committed to it.

To avoid that, transcode selection **skips a node whose scratch volume is 95% or
more full**, preferring a node with headroom even when the full one is carrying
fewer jobs. Each time a node crosses into that state the server logs it once
(`component=nodepool`, "scratch volume nearly full") — once per transition, not
once per session.

The rule is deliberately forgiving in two ways:

- If skipping would leave *no* usable transcode node, the rule is ignored and
  selection proceeds normally. Degraded playback beats no playback, and 95% was
  never meant to darken a whole cluster. The log says which of the two happened:
  a node that was genuinely kept out reads "excluded from selection", while a
  node the guard had to admit anyway reads "still selected because no eligible
  node has scratch headroom" and is accompanied by "transcode scratch guard
  ignored". The second is an outage in progress — sessions are landing on a disk
  that will fail mid-stream — and is worth paging on where the first is not.
- A node whose scratch fill cannot be read is never skipped: no sample, an
  unmeasurable path, or numbers the node itself flagged as carried over from an
  earlier probe. Removing capacity on a reading we do not have would be worse
  than the failure it prevents.

If you see this in your logs, the fix is on the node: raise the volume's size,
lower segment retention, or clear stale artifacts from the transcode directory.

## Scraping node metrics

Every Silo process — the API host and each node — publishes its own sample as
`streamapp_node_*` gauges on `/metrics`, on the same listener it serves traffic
from. It is unauthenticated, matching the API listener, because a scrape target
that needs a credential is a scrape target that goes unmonitored; what it exposes
is host resource counters, not media.

Disk series are labeled by **role**, not by path — `mount="scratch"` and
`mount="library-1"`, `library-2`, ... — so an anonymous scrape cannot enumerate
where your media lives. A node's `/health` is unauthenticated for the same
reason and reports the same roles without paths, which is what the Nodes page
draws from. The real paths are behind admin authentication on
`GET /api/v1/admin/system/resources` and behind a bearer token on each node's
`/status`. Library ordering is stable for a given configuration, but it is
positional: adding a library root can renumber the series after it, so alert on
`mount="scratch"` by name and on the library mounts by aggregate. A mount that
goes unavailable keeps its number rather than renumbering the ones after it.

At most eight mounts are sampled per host, scratch first. The cap bounds
probing, not just reporting — an unresponsive network mount parks a `statfs`
call that cannot be interrupted — so roots past it are not sampled, and the
process logs how many were left out (`component=nodemetrics`).

A minimal scrape config, with one job per role:

```yaml
scrape_configs:
  - job_name: silo-api
    metrics_path: /metrics
    static_configs:
      - targets: ["silo-api.internal:8080"]
        labels:
          silo_role: api

  - job_name: silo-nodes
    metrics_path: /metrics
    static_configs:
      - targets:
          - "transcode-1.internal:8081"
          - "transcode-2.internal:8081"
        labels:
          silo_role: transcode
      - targets:
          - "proxy-1.internal:8082"
        labels:
          silo_role: proxy
```

Replace the hostnames and ports with your own; the metrics path is the same on
every role. The two alerts worth having first are the scratch volume filling and
a node's GPU going quiet while its CPU does not:

```yaml
groups:
  - name: silo-nodes
    rules:
      - alert: SiloScratchVolumeFilling
        expr: |
          streamapp_node_disk_used_bytes{mount="scratch"}
            / streamapp_node_disk_total_bytes{mount="scratch"} > 0.9
        for: 15m
        annotations:
          summary: "Silo scratch volume above 90% on {{ $labels.instance }}"

      - alert: SiloNodeCPUSaturated
        expr: streamapp_node_cpu_percent > 90
        for: 15m
        annotations:
          summary: "Silo node CPU pegged on {{ $labels.instance }} — check the Acceleration block for a failed probe"

      - alert: SiloDiskMeasurementStale
        expr: streamapp_node_disk_stale == 1
        for: 15m
        annotations:
          summary: "Silo has not measured {{ $labels.mount }} on {{ $labels.instance }} for a while — its used/total figures are carried over"
```

That last one matters more than it looks. A mount whose probe stops returning
keeps exporting its last real used and total bytes, because dropping the series
would blank the panel for a network mount that is merely slow to answer — which
happens routinely. The numbers are genuine, only old, so `SiloScratchVolumeFilling`
above still fires on a volume that was nearly full when measurement stopped. What
it cannot do is notice a volume that stopped answering at 40% and has been
filling since. `streamapp_node_disk_stale` is what closes that gap: it is `1`
whenever the used and total beside it are carried over, and `0` when they are
current. A mount Silo has never measured at all exports nothing — not even a
staleness series, since there would be no numbers for it to qualify.

A GPU nothing could measure exports no engine gauges at all rather than zeros —
a Prometheus sample carries no `source` to qualify them, and a zero would read as
idle on a card that may be busy and merely unobservable. Its session count still
ships, since that comes from Silo's own accounting rather than from a driver.

The GPU gauges (`streamapp_node_gpu_video_busy_percent`,
`streamapp_node_gpu_render_busy_percent`, `streamapp_node_gpu_busy_percent`,
`streamapp_node_gpu_sessions`, `streamapp_node_gpu_vram_used_bytes`,
`streamapp_node_gpu_vram_total_bytes`) are labeled by `device`. On Intel and AMD
the engine gauges measure Silo's own FFmpeg processes only, so a card shared with
anything outside Silo reads as less busy than it is; on NVIDIA they come from
`nvidia-smi` and are whole-GPU. A device that nothing could measure this interval
publishes no VRAM series rather than a zero.

`streamapp_node_gpu_busy_percent` is the card's own utilization, other tenants
included, and it ships wherever a source reports one — `nvidia-smi` today. It is
the series to alert on for a shared GPU: the engine gauges can show Silo idle
while the card it is planned onto is saturated by something else.

If `nvidia-smi` fails five samples running, Silo stops calling it — a host
without the NVIDIA toolkit would otherwise spawn a doomed subprocess every few
seconds forever. It is not retired for good: one probationary call goes out every
ten minutes, so a driver reset or a toolkit installed after startup is picked up
on its own. `POST /api/v1/admin/nodes/{id}/reprobe` puts it back in service
immediately, which is the faster path when you have just fixed the driver
yourself.

## Source References

- `internal/nodepool` — health sweep, capability refetch, drift detection, and
  transcode placement including the scratch guard.
- `internal/nodemetrics` — host and GPU sampling, and the `streamapp_node_*`
  collector.
- `internal/playback/gpudetect.go` — the hardware verification probes behind the
  Acceleration block.
- [Admin API](../../admin-api.md) — the `GET /api/v1/admin/nodes` field table and
  the re-probe endpoint.
