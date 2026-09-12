# Admin API

> **API lifecycle:** this documents the frozen alpha `/api/v1` surface. Silo serves it through one
> pre-1.0 bridge release and then retires it; Silo 1.0's stable native API is `/api/v2`. See
> [the native API contract](architecture/api-contract.md).

Server-administration endpoints under `/api/v1/admin`. Every `/api/v1/admin`
route requires an authenticated account with the server-wide `admin` role — the
same authorization as `/api/v1/admin/sessions` — and none of them are part of
the client-facing contract that third-party apps build against. A few
deliberately public reads outside `/api/v1/admin` (marked `public` in the route
tables) are documented beside the admin writes they pair with.

This document is new and covers only the routes listed below. The rest of the
admin surface predates it and is currently documented by the code and by the
design documents under `docs/design/`.

## Branding assets

Uploadable images white-label the server: the sidebar wordmark, the square
mark (collapsed sidebar and installed PWA), optional light-theme variants of
both, the browser favicon, and the login background. Each is stored in the public S3 bucket and referenced from a
`server_settings` row, so uploads return `503 unavailable` until
`s3.public_bucket` is configured.

| Route                                         | Auth   | Purpose                                                              |
| --------------------------------------------- | ------ | -------------------------------------------------------------------- |
| `POST /api/v1/admin/branding/assets/{kind}`   | admin  | Upload (multipart, field name `file`). Replaces whatever is stored.  |
| `DELETE /api/v1/admin/branding/assets/{kind}` | admin  | Clear the asset. `204`, and clearing an unset asset is not an error. |
| `GET /api/v1/branding/assets/{kind}`          | public | Serve the stored bytes. Content-addressed, so `immutable` cached.    |
| `GET /api/v1/theme/branding`                  | public | Current branding, including each asset URL (omitted when unset).     |

Public reads are deliberately unauthenticated: branding has to apply on the
login page, before anyone has a session.

`{kind}` is one of `wordmark`, `wordmark_light`, `mark`, `mark_light`, `favicon`, `login_bg` — the light variants follow their base kind's processing. Uploads are
processed per kind — the numbers below are the contract the admin UI quotes back
to the operator, and they live in `internal/branding/assets.go`:

| Kind       | Accepts             | Max upload | Stored as                                                                                                     |
| ---------- | ------------------- | ---------- | ------------------------------------------------------------------------------------------------------------- |
| `wordmark` | PNG, JPEG, WebP     | 8 MB       | WebP, aspect preserved, capped at 640px wide. Narrower art is not enlarged.                                   |
| `mark`     | PNG, JPEG, WebP     | 8 MB       | WebP, center-cropped to a square, then forced to exactly 512×512 (smaller art is upscaled).                   |
| `favicon`  | PNG, WebP, ICO, SVG | 1 MB       | Byte-for-byte as uploaded, so `.ico` and `.svg` keep working in browsers that will not render a WebP favicon. |
| `login_bg` | PNG, JPEG, WebP     | 12 MB      | WebP, aspect preserved, capped at 2560px wide. Clients display it cover-cropped.                              |

There is one stored variant per kind, not a responsive set: the PWA manifest
advertises the single 512px mark at both 192×192 and 512×512, and native clients
read the same URLs as the web app. Recommend source art at or above the stored
size — anything larger is downscaled, anything smaller is either left small
(wordmark, login background) or upscaled (mark).

Failure modes: `400 bad_request` for an unknown kind, a missing `file` field, or
a content type the kind does not accept; `413 too_large` past the cap;
`503 unavailable` when asset storage is not configured.

Uploaded SVG favicons are admin-controlled but served from the app origin, so
every asset response carries `X-Content-Type-Options: nosniff` and a sandboxing
`Content-Security-Policy` — a directly-navigated SVG cannot run script in the
viewer's session.

## Server status and restarts

Some settings are only read at startup. Two routes carry that contract:

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/v1/admin/server/status` | admin | Process start time and pending-restart state. |
| `GET /api/v1/admin/settings/restart-keys` | admin | The compiled registry of setting keys that only take effect after a restart (`internal/config/restart_keys.go`). |

`GET /api/v1/admin/server/status` response:

| Field | Type | Meaning |
|---|---|---|
| `started_at` | RFC3339 string | When this process started. |
| `restart_required` | bool | A restart-required change was saved. Latches true for the life of the process; a real restart clears it by starting a new process. |
| `restart_required_at` | RFC3339 string | When the flag first latched. Omitted until then. |
| `restart_required_reason` | string | The reason of the **last** restart-required save only — later saves overwrite it. |
| `restart_required_reasons` | string[] | Every distinct reason since boot, first-seen order. Settings saves record one `setting:<key>` entry per restart-required key, so a client can scope a pending restart to the subsystem it belongs to. |
| `restart_mark_count` | int | Increments on every restart-required save. Because the boolean latches, this counter is the only signal that a **new** requirement arrived — the admin UI re-arms its dismissed restart banner on it. |
| `restart_requested`, `restart_requested_at` | bool, RFC3339 string | An in-app restart was requested, and when. |

## Playback node routing

Playback routing separates where media work executes from the process that is
the client-facing media origin. `GET /api/v1/admin/playback-routing/capabilities`
advertises the supported contract:

```json
{
  "features": ["playback_node_routing_v1"],
  "workloads": ["direct_play", "remux", "video_transcode"],
  "execution_preferences": ["prefer_worker", "prefer_transcode", "worker_only", "prefer_api", "api_only"],
  "egress_preferences": ["prefer_proxy", "proxy_only", "prefer_api", "api_only"]
}
```

The existing atomic admin settings update writes the five primitive policies:

| Setting | Default |
|---|---|
| `playback.routing.direct_play_egress` | `prefer_proxy` |
| `playback.routing.remux_execution` | `prefer_transcode` |
| `playback.routing.remux_egress` | `prefer_proxy` |
| `playback.routing.video_transcode_execution` | `prefer_transcode` |
| `playback.routing.video_transcode_egress` | `prefer_proxy` |

`prefer_*` permits fallback; `*_only` is a hard boundary. An atomic update is
rejected when API-only execution is combined with proxy-only egress for remux
or video transcode, because no implemented transport can satisfy that shape.
The policy is read as one immutable snapshot for each playback start or replan.

A **worker** is either a proxy node or a transcode node; the selected route says
which kind executes the work. Progressive remux can run on a proxy, on the API
process, or on a transcode node that streams its output through a proxy. HLS
remux and video transcode can run on a transcode node (or the API process).
`prefer_transcode` ranks a legal transcode-node shape first, then another worker,
then the API without changing the delivery selected for the client. For video
transcode, `prefer_transcode` and `prefer_worker` currently choose the same kind
of worker because only transcode nodes can execute that workload. Egress is
selected independently, and a proxy used only for egress does not run FFmpeg.

Jellyfin-compatible clients select progressive or HLS transport through their
protocol request. Routing does not rewrite a Jellyfin client's requested
transport; progressive Jellyfin remux retains its existing proxy/API execution
path.

`GET /api/v1/admin/sessions/capabilities` advertises `node_routing: true`.
Rows from `GET /api/v1/admin/sessions` may then include
`routing_workload`, `routing_execution`, `routing_execution_node_id`,
`routing_execution_node_name`, `routing_egress`, `routing_egress_node_id`, and
`routing_egress_node_name`. Node fields are absent for the integrated API
process and for direct play's `none` executor.

`silo_playback_routing_decisions_total` counts routing outcomes with bounded
`workload`, `execution`, `egress`, `outcome`, and `reason` labels. It never
labels observations with playback-session or node identity.

## Catalog search status

`GET /api/v1/admin/catalog/search/status` reports the configured search
provider, the provider currently answering requests, Meilisearch health, index
state, semantic readiness, and links to the search maintenance tasks.

`active_provider` describes the route requests actually take; it is not merely
the configured provider. `degraded` and the optional `degraded_reason` explain
temporary fallback or keyword-only operation. `index.rebuild_required` is true
when the active index does not match the current settings. Background search
maintenance runs at startup and every minute: it rebuilds a missing or stale
index, then resumes incremental event sync. When the prior index is known to
have the same document and media scope, Meilisearch continues serving keyword
search while the replacement is built; otherwise searches use PostgreSQL.

## `GET /api/v1/admin/nodes`

Lists every registered stream node — proxy and transcode alike — with its
configuration, last health result, and last stored hardware inventory.

Always `200 OK` with a JSON array.

| Field | Type | Meaning |
|---|---|---|
| `id`, `name`, `type`, `url` | int, string, string, string | Identity. `type` is `proxy` or `transcode`. `url` is the backend address: what the API server dials for health checks, capability fetches, and dispatch, and what a proxy dials to reach a transcode node — a private/internal address is fine and keeps that traffic off the public network. |
| `public_url` | string \| null | Client-facing base URL, when it differs from `url`. Stream and download URLs handed to players are built on it. Only meaningful on proxy nodes — clients never talk to transcode nodes. Absent or `null` means clients use `url`, which must then be publicly reachable. |
| `enabled` | bool | Whether the node is eligible for new placement. Disabling also stops routine health sampling after pool reconciliation; existing streams continue. See the v2 node configuration lifecycle below. |
| `healthy` | bool | Result of the last health check. |
| `active_jobs`, `egress_kbps` | int | Last health-reported load. `egress_kbps` is a rolling average and is currently non-zero for proxy nodes only. |
| `group` | string \| null | Co-location group. A group is only eligible while every enabled member is healthy. |
| `max_jobs`, `max_bandwidth_kbps` | int \| null | Capacity caps. `null` means unlimited. |
| `last_health_check` | RFC3339 string \| null | When the node was last checked. |
| `created_at` | RFC3339 string | When the node was registered. |
| `capabilities` | object | The node's last stored capability report — the same body `GET /hw-capabilities` returns on the node. Omitted until one has been stored. |
| `capabilities_hash` | string | Identity of that report, as computed by the node. Omitted with `capabilities`. |
| `advertised_capabilities_hash` | string | The hash the node named on its last health check. It differs from `capabilities_hash` while a refetch is outstanding or failing — the one case a recent `last_health_check` cannot rule out, since that check keeps succeeding while the refetch does not. Derived per sweep rather than stored, so it is **absent** until the first check after an API restart; **present and empty** when the node answered but named no hash at all, as a build predating capability reports does. Absent says nothing about the stored report; empty says the node is no longer confirming it. |
| `capabilities_refreshed_at` | RFC3339 string | When the report was fetched. This is the age of the *inventory*, not of the health check: an unchanged node keeps a report from hours ago. |
| `physical_gpu_keys` | string[] | Stable identities of the GPUs behind this node, derived from `capabilities` (see below). Omitted when the node reports no identifiable GPU. |
| `last_stats` | object | The node's most recent host resource sample — `{"system": …, "gpu": […]}` in the shape below. Omitted when the node reported none. |
| `hw_accel_override`, `hw_device_override` | string | This node's own acceleration policy (see below). Omitted when the node inherits the cluster-wide settings, which is the normal case. |
| `capability_drift` | string | Human-readable note describing how the node's hardware got worse at the last capability refetch. Omitted when the last refetch found no regression (see below). |
| `capability_drift_baseline` | object | What that note is waiting on — `{"backends": ["nvenc"], "devices": [{"uuid": "GPU-8a7b…", "aliases": ["GPU-8a7b…", "0000:03:00.0", "/dev/dri/renderD128"]}]}`. Never present without `capability_drift`; absent with it only for a note written before this field existed (see below). Each device carries every stable name it answered to, so it is recognized if it returns renumbered; `uuid` is held apart because it is the only name that can prove a *different* card, a replacement in the same slot inheriting both the slot and the render path. Either key is omitted when empty. |

### Acceleration overrides

`hw_accel_override` and `hw_device_override` override the cluster-wide
`playback.hw_accel` and `playback.hw_device` settings for one node.
`hw_accel_override` takes the same values as the cluster setting — `auto`,
`qsv`, `vaapi`, `nvenc`, `none`. Absent means inherit; there is no separate
"inherit" value to set.

They exist for a heterogeneous deployment: one CPU-only node in a QSV cluster
sets `none` for itself instead of forcing every node onto the lowest common
denominator. A homogeneous deployment should leave both unset and configure
`playback.hw_accel` once.

Repointing a node's `url` to a different machine clears the identity-bound
state on that row — `capabilities`, `capabilities_hash`,
`capabilities_refreshed_at`, `last_stats`, and the drift note with its baseline
— because all of it describes the worker the old address reached, and the pools
are reloaded from the row immediately. The replacement is treated as newly
registered until its first health check and capability fetch.

A node finds its own row by URL first: `NODE_URL` on the node is matched
against `stream_nodes.url`, ignoring a trailing slash on either side. Set
`NODE_URL` explicitly on every node. Without it a node guesses
`http://localhost:<port>` and adopts whatever row carries that URL, which on a
multi-node deployment can be a different machine's policy.

If the URL does not match, the node falls back to `NODE_NAME` against the
registered name. This covers split-horizon topologies where `stream_nodes.url`
was registered as a public address the node's own `NODE_URL` never equals —
with `public_url` carrying the client-facing address, `url` can simply be the
node's internal address and match `NODE_URL` directly, which is the
recommended shape. `name`
carries no unique constraint, so an ambiguous match — more than one row
sharing that name — identifies nothing and adopts neither row's overrides;
registered names should be unique per node, and `NODE_NAME` should equal the
registered name. A node whose row *stops* matching — renaming it in the admin
form while the worker's `NODE_NAME` still holds the old value — keeps the last
overrides it read rather than reverting to the cluster settings, since the API
goes on dispatching that row's backend and a row that has gone is not evidence
an operator cleared the override. Fix the mismatch: the node adopts whatever it
finds on its next poll.

The node overlays its row onto the cluster-wide playback settings on every
config reload, so the override is what that node probes with, advertises in
`capabilities.resolved`, and falls back to when a start request names no
backend. The API dispatches remote transcodes with the node's
`hw_accel_override` in preference to its own cluster setting, so the request
agrees with what the node would have run anyway. Dispatch reads the override
column, not `capabilities.resolved`: a node inheriting `auto` is dispatched
`auto` so it resolves against live hardware at session start rather than
against a snapshot.

**A changed override applies without a restart, but not all at once.** An update
that actually moves either override asks the node to re-read its configuration
before the pools are reloaded, so the node adopts the new device before this
server begins dispatching the new backend. Without that ordering, changing both
at once — QSV on a render node to NVENC on a CUDA index, say — would pair the new
backend with the old device until the node's own poll caught up.

That reload is non-destructive: sessions already transcoding are untouched, and
an edit that leaves both overrides where they were makes no call at all. The
API also drops its own cached view of what the node can do, so the next session
is planned against the new backend's tone-map executors rather than the previous
one's. It is
also best effort — a node that is unreachable still applies the change on its
next config reload (within 60 seconds). When it does not confirm, the policy is
published anyway and a warning names the node: withholding it would leave a
stored override never reaching dispatch, since nothing else re-reads the column.
Until that node's poll catches up its backend comes from this server while its
device comes from its own configuration, so a start dispatched to it in that
window can pair the two wrongly and fail. Either way the node re-advertises
`capabilities.resolved` at its next capability snapshot (every 15 minutes). Two
things do wait for a restart: the hardware encoder warmup that ran at boot,
which stays primed for the old backend, and sessions already transcoding, which
keep the backend they started with. Restart the node when you want all four in
agreement immediately.

### `last_stats`

Written by the same 30-second health check that writes `active_jobs`, so it is
exactly as old as `last_health_check` and never fresher. It is the current
sample only: nothing here is a time series, and operators who want history
scrape the node's own `GET /metrics` (unauthenticated, on the node's listener,
same `streamapp_node_*` gauges, with disk series labeled by role rather than by
path). A sample larger than 32 KiB is dropped rather than stored — the health
verdict is what routes streams, and no honest sample comes close to that.

Cgroup correction alone is not always enough: a Docker container nested inside
an LXC container sees no limit on its own cgroup (the LXC's cap lives on an
ancestor cgroup outside its namespace), so `cpu_pct`, `cores`, `load1`, and the
memory fields below read as the bare-metal host's totals unless the deployment
bind-mounts lxcfs's virtualized `/proc` files in — see the LXC section of
[docs/wiki/deployment/docker.md](wiki/deployment/docker.md#node-metrics).

`last_stats.system`:

| Field | Type | Meaning |
|---|---|---|
| `cpu_pct` | int | Aggregate busy percentage across all cores over the last sampling interval (5s), 0-100. Idle and iowait both count as not busy. Under a cgroup this is the container's own consumption against its own quota, not the host's. |
| `load1` | float | 1-minute load average. Unlike `cpu_pct` it also counts tasks blocked on storage, so a node stuck on I/O looks idle in one and busy in the other. Always host-wide: the kernel keeps no per-cgroup load average. |
| `cores` | int | CPUs this process may run on — the cgroup's CPU quota rounded up where one is set, otherwise every CPU the kernel reports. This is what `cpu_pct` is normalized against and what `load1` must be read relative to. |
| `mem_used_mb`, `mem_total_mb` | int | Memory. Under a cgroup with a concrete limit these are the cgroup's limit and working set (page cache excluded); otherwise both are the host's. The pair always comes from one domain — a container with no limit publishes a readable working set, and reporting that against host RAM would read as idle on a machine that is nearly out of memory. |
| `disks` | object[] | Sampled mounts, transcode scratch first, deduplicated by filesystem and capped at 8 — unmeasurable paths included, so the array never grows with the library count. The cap is on what is *probed*, not only on what is reported: each mount costs a `statfs` goroutine per interval that a dead network mount parks indefinitely. Roots past the cap are not sampled, and the number left out is logged (`component=nodemetrics`) rather than left to look like a clean bill of health. A second ceiling bounds probes outstanding at once across every path ever offered, so reconfiguring library roots while mounts are wedged cannot accumulate parked goroutines. |
| `net_rx_bps`, `net_tx_bps` | int | Aggregate throughput in **bits** per second, loopback excluded. In a container this is the container's own network namespace. |

Each entry in `disks`:

| Field | Type | Meaning |
|---|---|---|
| `path` | string | Where the mount is. Absent from a node's `last_stats`: the node reports it on its own bearer-authed `/status`, and the API host reports its own on `GET /admin/system/resources`, but a node's `/health` takes no credential and withholds it. Use `role`. |
| `role` | string | What the mount is for: `scratch`, or `library-N` positionally per media root. Assigned when the sample is built, so it names the same mount on `/health`, `/status`, `/admin/system/resources` and the `streamapp_node_disk_*` series, and it stays with the mount even when a probe cannot measure it. |
| `used_gb`, `total_gb` | float | Capacity in GiB. `used_gb` counts filesystem-reserved blocks, as `df` does. `total_gb` is the capacity usable by the node process — used plus still-available — so it reads lower than the device's nameplate size on a volume that reserves blocks for root, and `used_gb`/`total_gb` is the ratio `df` prints as Use%. |
| `stale` | bool | The numbers are real but carried over from an earlier pass because the current probe has not returned — the normal reading for a network mount whose server went away. Omitted when false. |
| `unavailable` | bool | The path has never been measured on this node (it does not exist here, or the first probe is still hanging). `used_gb`/`total_gb` are meaningless. Omitted when false. |
| `scratch` | bool | This is the node's transcode working directory. Set on at most one entry; a media root sharing that volume is deduplicated onto it. Omitted when false. |

`scratch` exists because this server does not know a node's transcode directory —
the node does. It is the one mount whose filling up breaks transcoding rather
than browsing, so the entry has to identify itself. Node selection reads it:
see "Scratch admission" below. It is also what labels the node's own
`streamapp_node_disk_*` series `scratch` instead of `library-N`.

### Scratch admission

A transcode writes HLS segments to its node's scratch volume for the whole life
of the session, so admitting one onto a nearly full node produces a stream that
dies mid-playback — after the client has already committed to it. Transcode
selection therefore skips a node whose `scratch` entry reports **95% or more**
used, and prefers a node with headroom even when the full one carries fewer
jobs.

The exclusion is soft in two directions:

- If it would leave no eligible candidate at all, it is ignored and the ordinary
  least-jobs selection stands. Degraded service beats no service, and 95% was
  not chosen to be a kill switch for a whole cluster.
- A node whose fill cannot be read is never excluded: no sample, no `scratch`
  entry (a node predating the flag), an unmeasurable path, or numbers the node
  itself marked `stale`. Taking capacity away on a fill we cannot read would be
  worse than the failure it prevents.

Each transition into pressure is logged once per node (`component=nodepool`,
"scratch volume nearly full"), not once per session start.

Nothing else routes on `last_stats`, and the guard applies to playback and
local-egress transcode selection only — not to proxy selection, and not to the
non-streaming transcode reservations used by prepared downloads.

Each entry in `last_stats.gpu`:

| Field | Type | Meaning |
|---|---|---|
| `device` | string | The render node path (`/dev/dri/renderD128`), or `cuda:N` for an NVIDIA GPU with no readable DRM node. |
| `vendor` | string | `intel`, `nvidia` or `amd`. Omitted when sysfs names a vendor we do not recognize. |
| `sessions` | int | GPU workloads this node currently has pinned to the device. It comes from the playback device balancer, so it is exact for Silo's own work and blind to any other tenant's. With no `playback.hw_device` configured the workload is counted against the render device the transcode will actually open — the one auto-detection verified the backend on, or the first available render node when the backend was named explicitly and no detection walk ran. It goes uncounted only on a host with no render device at all. |
| `video_busy_pct`, `render_busy_pct` | int | Engine busy percentages over the sampling interval. |
| `total_busy_pct` | int | Whole-GPU utilization *including other tenants*. |
| `vram_used_mb`, `vram_total_mb` | int | GPU memory. |
| `source` | string | What produced the numbers: `fdinfo`, `nvidia-smi`, `fdinfo+nvidia-smi`, or `unavailable`. |

Every measurement field above is omitted when nothing measured it, and
availability is per field rather than per device: absent is not zero and must
not be rendered as an idle GPU. A card can answer for some columns and not
others — `nvidia-smi` prints `[N/A]` for an engine a GPU cannot report while
still giving real memory figures, and a device reached only through `nvidia-smi`
has no render-engine reading at all — so read each field's presence, not the
device's.

`source` is what tells an operator how far to trust the busy percentages that
are present. `fdinfo` is the unprivileged DRM baseline and covers **only this
node's own ffmpeg children** — a GPU shared with anything outside Silo reads as
less busy than it is. `nvidia-smi` is whole-GPU. `unavailable` means nothing
could measure the device this interval, so it carries no percentages at all.

A node reports these fields in its own `/health` and `/status`; the API stores
them opaquely and parses only what it routes on. No GPU field is one of those —
nothing in node selection reads `last_stats.gpu`. The one part that is read is
the `scratch` disk entry, described under "Scratch admission" above.

`last_stats` comes from `/health`, which takes no credential, so it carries no
filesystem paths — disk entries are named by `role`. GPU `device` values are
kept: a render node or a CUDA index is a fact about the hardware rather than
about this deployment, and the unauthenticated `/metrics` already labels its
per-GPU series with the same value.

Capability reports are refreshed by the background health sweep, not by this
read: a node advertises a `capabilities_hash` in its own health response, and
only a hash that differs from the stored one triggers a refetch. A node running
a build from before capability snapshots advertises no hash and therefore
carries none of the four fields above. A failed refetch keeps the previous
report rather than clearing it — a node that cannot be reached is not evidence
that its hardware changed. The refetch itself runs outside the sweep's own wait,
one at a time per node, so a slow capability probe cannot delay the health
cadence of the other nodes; a new report can therefore land shortly after the
check that noticed the change rather than with it.

An operator who cannot wait for the sweep — or whose node will never advertise a
changed hash because its probe results are cached for its process lifetime — uses
`POST /api/v1/admin/nodes/{id}/reprobe`, which stores the new report before it
answers.

### `capability_drift`

Set when a capability refetch shows the node's hardware got **worse** than the
report it replaced: a backend that used to pass its FFmpeg probe and now fails,
or a render device that is gone. It reads like
`verified hardware backends lost: qsv; render devices gone: /dev/dri/renderD128;
resolved backend qsv -> none`, and is capped at 512 characters.

It exists because that regression is otherwise only a log line, and the node
stays `healthy` throughout: a driver that stopped working silently turns a GPU
transcoder into a CPU one, which shows up to users as slow or failing playback
long before anyone reads a log.

Semantics worth knowing:

- Setting it is a comparison; clearing it is not. The note appears when a refetch
  loses something, and it records what it lost in `capability_drift_baseline`.
  Clearing requires that specific hardware back: every backend in the baseline
  verifying again, and every device in it answering to one of its recorded
  aliases. A refetch that finds nothing *newly* lost leaves the note alone,
  because a delta against an already-degraded report always finds nothing — a
  reboot moves `boot_id`, a reworded FFmpeg failure moves the probe reason, and
  either would otherwise erase a standing regression. Three cases make the
  baseline necessary rather than pedantic, and none of them are caught by
  looking at the current report alone: a GPU that disappeared completely leaves
  no candidate backend to fail; a multi-GPU node that lost one card keeps
  probing the survivor perfectly cleanly; and adding an unrelated GPU grows the
  inventory without repairing anything. Successive losses accumulate, so two
  cards going one at a time must both return.
- A note carried over from before the baseline existed has nothing recorded to
  wait for, and a clean report clears it.
- Only a backend that was *probed and failed* counts as lost. A backend simply
  absent from the report was not asked about — detection probes the backends the
  configured `hw_device` gives it candidates for — so repointing a node from a
  QSV render path to an NVENC index is not a regression. Hardware actually
  disappearing shows up in the device inventory, which is the host's own and
  owes nothing to the configuration: `render_devices` for cards with a DRM node,
  and `nvidia_gpu_uuids` for those without one, which is the ordinary shape of
  an NVENC container. A card that vanishes from either is a loss.
- A backend reported as `skipped` neither sets the note nor holds it open.
  Skipping means no probe ran because the node cannot open the backend's
  configured devices, which is a statement about access rather than about
  hardware — the GPU column reports that state separately.
- Only a loss is reported. Added hardware is not drift.
- A node's first stored report carries none — there is nothing to compare it
  against.
- It is written in the same statement as `capabilities` and `capabilities_hash`,
  so it always describes the report stored beside it.
- Nothing routes on it. Node selection reads `healthy`, capacity, capability
  eligibility, and the scratch guard above — never this field.

Refetches only happen when a node advertises a changed `capabilities_hash`, so a
node whose GPU broke while its process kept running may report nothing new: the
probe results are cached for its process lifetime. `POST
/api/v1/admin/nodes/{id}/reprobe` is what forces the question.

### `physical_gpu_keys`

One key per GPU in the stored report, deduplicated and sorted. From each render
device:

- the device's `gpu_uuid` when present (NVIDIA's permanent GPU identity, which
  follows the card between slots and hosts), otherwise
- `<boot_id>|<pci_address>`, because a PCI slot only means the same hardware
  within one boot of one kernel.

Plus every entry in `nvidia_gpu_uuids`, which is what covers a card with no
readable DRM node — the ordinary NVIDIA container, where NVENC works and
`render_device_details` is empty. A uuid is host-independent, so a card reported
both ways yields one key, and a container that sees only `/dev/nvidia*` and one
that also sees `/dev/dri` recognize the same physical GPU.

A device with neither identity contributes no key rather than a synthetic one,
and so does a slot on a host that reported no `boot_id`: `boot_id` detection is
best-effort, and an unscoped slot is not an identity, since every host with an
Intel iGPU has one at `0000:00:02.0`. Two nodes sharing a key are backed by the
same physical GPU — the case that makes per-node capacity accounting wrong, and
which no single node's report can express. The keys are derived from the stored report on every read, so they are
present as soon as a report is, including immediately after an API restart.

Caveats on what a key can prove:

- A key is only stable within one boot of the host it came from. `boot_id`
  changes on reboot, so a fallback key does too, and the same card looks like a
  different GPU until every node on that host has re-reported. An NVIDIA
  `gpu_uuid` has no such limit.
- Intel and AMD GPUs passed through to separate VMs cannot be correlated at
  all: each guest reports its own `boot_id` and its own PCI topology, so two
  guests on one card produce two unrelated keys. Sharing there is invisible to
  the server, and stays a matter for how the host is partitioned.

Node selection uses the same keys as a tie-breaker: among transcode nodes that
are otherwise level on effective job count, the one whose physical GPU group —
itself plus every pooled transcode node sharing a key with it — carries the
fewest jobs wins. It never overrides the job count itself or the soft affinity
that keeps a session on its current node, and it does not apply to proxy
selection, which is round-robin and does no GPU work.

## `POST /api/v1/admin/nodes`

Registers a node. Body: `name`, `type` (`proxy` or `transcode`), `url`, and the
optional `public_url`, `group`, `max_jobs`, `max_bandwidth_kbps`. A
non-positive cap and an empty group mean "unlimited" and "ungrouped"; an empty
`public_url` means clients use `url`.

`201 Created` with the created node in the same shape as one list entry (with
no capability fields yet — nothing has been fetched). `400 Bad Request` when a
required field is missing or `type` is not one of the two allowed values. The
node pools are reloaded afterwards.

## `PUT /api/v1/admin/nodes/{id}`

Updates a node's mutable fields. Every field is optional; an omitted field is
left unchanged. An empty-string `group` clears the group, and a non-positive
`max_jobs` or `max_bandwidth_kbps` clears that cap.

`public_url` follows the same convention as the overrides below: `null` or an
empty string clears it, sending clients back to `url`; an omitted field leaves
it alone.

`hw_accel_override` and `hw_device_override` are writable here. Either `null`
or an empty string clears one, restoring inheritance of the cluster-wide
setting; an omitted field leaves it alone. Clearing an override is a real
change with a real effect, so it is deliberately expressible rather than being
indistinguishable from omission.

`200 OK` with the updated node, `404 Not Found` for an unknown id,
`400 Bad Request` when `hw_accel_override` is not one of `auto`, `qsv`,
`vaapi`, `nvenc`, `none` (matched case-insensitively and stored lowercase, as
`playback.hw_accel` is). The node pools are reloaded afterwards, so remote
dispatch honors a new override immediately; the target node itself picks it up
on its next config reload — see "Acceleration overrides" above for what waits
for a restart.

Capability fields are not writable here. They are owned by the health sweep,
because only the node can say what hardware it has.

## `DELETE /api/v1/admin/nodes/{id}`

Removes a node. `204 No Content`, or `404 Not Found` for an unknown id. The
node pools are reloaded afterwards. Sessions already streaming from the node
are not torn down by this call.

## `POST /api/v1/admin/nodes/{id}/check`

Runs one health check against a node immediately and persists the result, for
an admin who does not want to wait for the next 30-second sweep.

Always `200 OK`; an unreachable node is reported as `healthy: false` rather
than as an error status. `404 Not Found` for an unknown id.

| Field | Type | Meaning |
|---|---|---|
| `healthy` | bool | The node answered its health endpoint. |
| `active_jobs`, `egress_kbps` | int | What it reported. Zero when unhealthy. |
| `capabilities_hash` | string | The hash the node advertised on this check. Omitted when the node reports none. |

The check also persists the node's resource sample, so `last_stats` on the list
response reflects this check immediately. The sample itself is not echoed here.

This is the node's *current* hash, not the stored one. A value here that
differs from the `capabilities_hash` in the list response means the background
sweep has a refetch pending; this route does not fetch capabilities itself.

## `POST /api/v1/admin/nodes/{id}/reprobe`

Tells one node to discard its cached hardware-probe verdicts and re-verify
against live hardware, then refetches and stores the resulting inventory
immediately.

This is the answer to hardware that stopped working underneath a running node. A
node caches a **successful** probe for its whole process lifetime — re-verifying
per request would put FFmpeg execs on the playback path — so a GPU that has since
been removed, or whose driver was replaced with one that cannot encode, keeps
reporting `verified: true` until the node restarts. That is not visible in a
health check, because the node is healthy either way. Use it after installing or
downgrading a GPU driver, after changing which devices a node's container can
open, after replacing an FFmpeg build in place, and to confirm a
`capability_drift` note is still true.

The reverse needs no action: a **failed** GPU probe carries a 15-second negative
TTL and is retried on its own, so a repaired driver flips `verified` to `true`,
changes the node's `capabilities_hash`, and is refetched within one snapshot
interval. The exception is the tone-map matrix, which caches any non-empty
inventory for the process lifetime, so a node whose GPU was broken at start can
stay software-only for tone mapping until it is re-probed or restarted.

Body: none. Always `200 OK`; a node that refused or could not be reached is
reported in the body rather than as an HTTP error status, matching
`{id}/check` and `{id}/force-reload`. `404 Not Found` for an unknown id.

| Field | Type | Meaning |
|---|---|---|
| `node_id`, `node_name` | int, string | The node this action ran against. |
| `status` | string | `ok` or `error`. |
| `error` | string | Why the node failed. Omitted on success. |
| `resolved` | string | The backend the node picked after re-probing. Omitted on failure. |
| `capability_hash` | string | The snapshot the node published. Compare it against `capabilities_hash` from the list response taken *before* the call to see whether anything changed. Omitted on failure. |
| `capabilities_refreshed` | bool | Whether this server also stored the node's new inventory before answering. |

`capabilities_refreshed: false` with `status: ok` means the node re-probed but
the stored row has not caught up yet — a refresh for that node was already
running, or this deployment has no health sweep. The next sweep stores it.

A node whose probe could not complete answers `status: error` and **keeps its
previous capability report**: an unfinished probe is not evidence the hardware
changed, and publishing a partial one would announce a change that did not
happen. Nothing is stored in that case, so a failed re-probe never degrades what
the list shows.

A node that is **transcoding refuses**, also as `status: error`, and keeps its
report. Every hardware probe ends in a real encode on the GPU; a card at its
concurrent encoder-session limit fails that encode with an error nothing can
distinguish from a missing device, and the resulting `verified: false` would be
stored as a hardware regression for a GPU that is at that moment encoding.
Disable the node or wait for it to drain, then re-probe.

The call can take a while. The node is given the probe budget it advertises in
its own report (`probe_request_timeout_ms`, up to five minutes); a node that has
never been inventoried gets 150 seconds. The capability refetch that follows adds
up to two minutes. The connection's write deadline is extended to cover both, so
this route can legitimately outlive the API listener's ordinary 120-second
`WriteTimeout`; a client should allow for that rather than treating a long wait
as a hung request. This re-probes only — it does not reload configuration or tear
anything down.

Under the hood this is a bearer-authenticated `POST
/admin/reprobe-capabilities` on the node's own listener, which both transcode
nodes and proxy nodes serve. That route is internal to the cluster and is not
part of any client contract.

## `GET /api/v1/admin/system/hw-accel`

Reports GPU hardware and acceleration capability. With healthy transcode nodes
registered it probes each of them; with none it probes this host. The top-level
fields are the first node that answered (or the local probe), and `nodes`
carries one entry per healthy node.

`playback.hw_device` is one cluster-wide value, so the per-node inventories are
what an operator needs to see that a device path exists on every node before
pinning one.

Always `200 OK`. A node that failed its probe is reported in `nodes` with an
`error` rather than dropped, so a hardware problem is visible instead of silent.

Top-level (and each node's own report):

| Field | Type | Meaning |
|---|---|---|
| `resolved` | string | The backend that would actually be used: `nvenc`, `qsv`, `vaapi`, or `none`. An explicitly configured backend wins even when its probe failed — read `detected_backends` for why. |
| `render_devices` | string[] | Every accessible `/dev/dri/renderD*` path. |
| `render_device_details` | object[] | One entry per device (see below). |
| `intel_detected` | bool | An Intel GPU is present in the inventory. |
| `detected_backends` | object[] | One entry per backend that had candidate hardware, with the outcome of its FFmpeg verification (see below). |
| `boot_id` | string | The host's kernel boot identity (Linux only). Pairs with a device's `pci_address` to distinguish the same GPU from the same slot after a reboot. |
| `nvidia_gpu_uuids` | string[] | Every GPU `nvidia-smi` reports on this host, sorted. Independent of `render_device_details`, because a card is not always reachable through a DRM node — an NVIDIA container is routinely given `/dev/nvidia*` and the toolkit with no `/dev/dri` at all. Omitted where `nvidia-smi` is absent. |
| `capability_hash` | string | `sha256:<hex>` over this report's hardware identity and capability fields — not over `source`, `node_url`, the probe budget, or itself. Two reports of unchanged hardware hash identically regardless of probe order. |
| `source` | string | `local` for a probe of this host. |
| `node_url` | string | Set on a node's report. |
| `transformations`, `tone_map_capabilities` | object[] | What this host can execute, as advertised to the planner. |

`render_device_details` entries:

| Field | Type | Meaning |
|---|---|---|
| `path` | string | The `/dev/dri` path. Assigned by enumeration order, so it moves when hardware is added or removed. |
| `pci_address` | string | The device's PCI slot (e.g. `0000:03:00.0`), read from sysfs. Omitted when the device has no PCI identity. |
| `gpu_uuid` | string | NVIDIA's permanent GPU identity. Reported only for NVIDIA devices on hosts with `nvidia-smi` installed; omitted otherwise. |
| `description` | string | Short human label, e.g. `NVIDIA GPU (0x2204)`. |

`detected_backends` entries:

| Field | Type | Meaning |
|---|---|---|
| `backend` | string | `nvenc`, `qsv`, or `vaapi`. |
| `verified` | bool | At least one candidate device passed a real single-frame encode, not just an FFmpeg build-flag listing. |
| `devices` | string[] | Every candidate considered for this backend. |
| `device` | string | The candidate whose probe passed. Empty for NVENC, which addresses its GPU through CUDA rather than a render node. |
| `reason` | string | Why verification failed, attributed per device when several were tried. |

Each entry in `nodes` carries `node_url` and `node_name` plus either that
node's `resolved`, `render_devices` and `render_device_details`, or an `error`
explaining why it could not be probed. The full report for one node — including
`detected_backends`, `boot_id` and `capability_hash` — is what
`GET /api/v1/admin/nodes` stores per node in `capabilities`.

## `GET /api/v2/admin/system/resources`

Returns the API process's last completed resource sample. The existing `system`
and `gpu` fields retain their meanings. `stale` is true when no sample exists or
its age exceeds three sampling intervals. The handler reads an immutable
snapshot and never probes a device, mount, dependency, or worker.

`GET /api/v2/admin/system/resources/capabilities` advertises
`instance_attribution`, `process_resources`, `cgroup_resources`, and
`sample_freshness`. It uses the common capability state, allowed, revision, ETag
and conditional request conventions. Support does not guarantee each operating
system source is readable. Both operations require acting administrator
account authority; a secondary profile does not acquire that authority from
being on an administrator account.

The additive `attribution` object contains:

| Field | Meaning |
|---|---|
| `instance_id` | Random identifier for this sampler lifetime; identifies which process answered through a load balancer without exposing a hostname. |
| `sample_interval_seconds`, `sample_duration_seconds` | Sampling cadence and duration of the last completed pass. |
| `cpu`, `memory`, `load`, `network` | Scope, source and availability of each corresponding `system` reading. Scopes include `host`, `virtualized_host`, `cgroup`, and `network_namespace`. |
| `process` | Silo process RSS, virtual memory, CPU seconds, threads, open FD count/limit, last-GC live Go heap, runtime memory reservation and goroutine count. Linux storage I/O byte counters include waited-for children. Unreadable values are omitted. |
| `cgroup_cpu` | Usage, capacity in cores, throttling, bandwidth periods and CPU pressure at the selected visible cgroup level. A tighter cpuset can set the reported capacity. |
| `cgroup_memory` | Raw charge, concrete limit, working set, swap, limit/OOM events and memory pressure. Usage and limit are read from the same visible level. |
| `children` | Bounded snapshot of live owned FFmpeg children. Reports partial/unavailable and truncated sampling explicitly. CPU totals can decrease when children exit; RSS can count shared pages more than once. |
| `disks` | Inode counts keyed by the existing disk role, with the matching disk's freshness. |
| `dropped_disk_roots` | Number of configured roots beyond the bounded disk sampling set. |

Cgroup `scope` is `leaf` or `ancestor`; an ancestor's counters include sibling
workloads. Limits outside the process's cgroup namespace cannot be observed.
Pressure values are percentages averaged over ten seconds. Missing counters
remain absent. Working set is raw charge minus inactive file pages; it is not
an exact OOM predictor. Process RSS, live Go heap, child RSS and cgroup charge
overlap and must not be added together or subtracted to infer exact native
allocations. Linux process I/O includes waited-for children and must not be
added to child I/O as if they were disjoint counters. The Go runtime memory field describes its reservation, not RSS.

Worker operational health/status responses carry the same attribution plus
`sampled_at`; the bounded `last_stats` snapshot preserves them for administrator
node views. A node answering health checks with a stopped sampler remains
visibly stale. Public operational responses contain disk roles, with paths
available only on authenticated status and administrator responses. Native
clients and Jellyfin/ABS do not gain profiling routes through this capability.

## `GET /api/v1/admin/system/resources`

Reports the **API host's own** current resource sample — the counterpart to the
per-node `last_stats` above.

The API host is not a registered stream node, so without this route the one
machine an operator cannot see is the machine serving the request (and, in
integrated mode, doing the transcoding). Unlike a node, this host also samples
the configured library roots: it is the process that knows what the library is,
and its view of a media mount is the authoritative one.

Always `200 OK`. It reads a snapshot the sampler already published, so it costs
nothing and cannot hang regardless of what a mount or a GPU query is doing.

| Field | Type | Meaning |
|---|---|---|
| `available` | bool | This host can be sampled. False on a non-Linux host, before the first sample lands, or when no sampler is running — in which case the fields below are absent. |
| `sampled_at` | RFC3339 string | When the sample was taken. Omitted when there is none. |
| `system` | object | Same shape as `last_stats.system` above. |
| `gpu` | object[] | Same shape as `last_stats.gpu` above. |

Sampling is Linux-only: `available: false` on macOS or Windows is expected and
is not an error. History and alerting are Prometheus's job — the same numbers
are exposed as `streamapp_node_*` gauges on this process's dedicated opt-in `/metrics`
endpoint, with one deliberate difference: `/metrics` is unauthenticated, so its
disk series are labeled `mount="scratch"` / `mount="library-N"` and the library
paths themselves appear only here, behind admin auth.

## `GET /api/v1/admin/stream-telemetry/parity`

Returns the merged stream-telemetry view beside the two legacy live-session
projections an admin reads today, plus the diff between them.

It is a diagnostic: it compares and does not cut over. No existing admin read has
been repointed onto telemetry, and nothing here blocks, throttles or ends a
session. Design: [`docs/design/2026-08-17-stream-telemetry.md`](design/2026-08-17-stream-telemetry.md).

The view is served from a bounded-staleness cache with single-flight refresh, so
several admins polling this route pay at most one rebuild per TTL.

Stream telemetry runs by default, so this route reports on an unconfigured
server. An `enabled: false` body means this process was switched off with
`SILO_STREAM_TELEMETRY_ENABLED=false`, or that a bad core setting disabled it —
the startup log names the variable in that case.

### Response

Always `200 OK`. "Nothing to compare" is expressed in the body rather than as an
error status, because an empty report with a success status would read as
agreement.

| Field     | Type   | Meaning                                                                              |
| --------- | ------ | ------------------------------------------------------------------------------------ |
| `enabled` | bool   | Stream telemetry is running in this process.                                         |
| `reason`  | string | Present when there is nothing to compare (telemetry disabled, or no view built yet). |
| `view`    | object | State of the merged view the comparison was built from.                              |
| `sources` | array  | One report per legacy projection. Empty when `enabled` is false.                     |

`view`:

| Field                                 | Type             | Meaning                                                                                                                                                                                            |
| ------------------------------------- | ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `available`                           | bool             | A merged view exists.                                                                                                                                                                              |
| `built_at`                            | RFC3339 string   | When it was built. Omitted if never.                                                                                                                                                               |
| `age_ms`, `stale`                     | int, bool        | Age of the cached view, and whether it exceeded the TTL.                                                                                                                                           |
| `build_took_ms`                       | int              | Cost of the last rebuild.                                                                                                                                                                          |
| `refreshes`, `failures`, `last_error` | int, int, string | Cache counters since process start.                                                                                                                                                                |
| `complete`                            | bool             | No publisher was stale, degraded or truncated.                                                                                                                                                     |
| `incomplete_reasons`                  | string[]         | Why `complete` is false — e.g. `missing_publisher`, `publisher_truncated`, `decode_errors`, `truncated`.                                                                                           |
| `missing_publishers`                  | string[]         | Publisher ids present in the roster but with no usable snapshot.                                                                                                                                   |
| `clock_skew_suspected`                | bool             | A publisher stamped a time in the future. A clock running _behind_ is indistinguishable from a stalled publisher in one sample; compare `publishers` sequence across two reads to tell them apart. |
| `publishers`                          | string[]         | `<publisher-id>=<state>`, where state is `fresh`, `degraded`, `stale` or `departed`.                                                                                                               |
| `session_count`, `transfer_count`     | int              | Sizes of the merged view.                                                                                                                                                                          |

Each entry in `sources`:

| Field       | Type     | Meaning                                      |
| ----------- | -------- | -------------------------------------------- |
| `source`    | string   | `playback_sessions_sync` or `node_sessions`. |
| `available` | bool     | The projection could be read.                |
| `error`     | string   | Why it could not.                            |
| `notes`     | string[] | Caveats that apply to this comparison.       |
| `report`    | object   | The diff, when available.                    |

`report`:

| Field                                               | Type     | Meaning                                                                                                                          |
| --------------------------------------------------- | -------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `telemetry_count`, `legacy_count`, `in_both`        | int      | Session counts on each side and their intersection.                                                                              |
| `agrees`                                            | bool     | Same session set, and no field both sides express disagrees. Read `fields_absent` before treating this as clearance to cut over. |
| `telemetry_only`, `legacy_only`                     | string[] | Session ids present on one side only, capped.                                                                                    |
| `telemetry_only_truncated`, `legacy_only_truncated` | int      | How many ids the cap dropped.                                                                                                    |
| `mismatches`                                        | object[] | Per-session field disagreements, capped.                                                                                         |
| `mismatches_truncated`                              | int      | How many the cap dropped.                                                                                                        |
| `fields_absent`                                     | object   | Per field, sessions both sides know where one side carries no value. A gap in a projection, not a disagreement.                  |

A single report samples three independently updated stores, so one-sided
differences are normal and are not on their own evidence of a defect. Repeated
agreement over time is what the legacy-retirement project is gated on.

## `/api/v1/admin/dashboard/layout`

The admin dashboard is a widget grid each admin arranges for themselves. The
arrangement is stored per **account** (`users.id`), not per household profile,
so the same admin sees the same dashboard in every browser they log in from.

The server stores the document verbatim and validates only that the body is at
most 16 KiB and that `layout` is a JSON object. Widget ids, column spans and row
heights are the admin web client's vocabulary: it already sanitizes what it
loads — dropping widgets it does not know, clamping each axis to that widget's
range, and filling in the default height for an entry saved before row heights
existed — so a second copy of that schema on the server would only be another
place to update whenever a widget is added. That also means a layout written by
a newer build degrades gracefully on an older one instead of being rejected.

Writes are last-write-wins. The layout is one admin's own blob, so a race
between two of their tabs can cost only the older arrangement; `updated_at` is
returned so a compare-and-set could be layered on later without a contract
change.

The web client keeps a copy in `localStorage` for instant paint and offline use,
adopts the server document when it arrives, and — the first time it finds no
server document but does have a local one — uploads that local layout once.

### `GET /api/v1/admin/dashboard/layout`

`200 OK`. Both fields are `null` when this admin has never saved a layout; that
is the normal first-load answer, not an error.

| Field | Type | Meaning |
|---|---|---|
| `layout` | object \| null | The stored document, exactly as it was written. |
| `updated_at` | RFC3339 string \| null | When it was last written. |

```json
{
  "layout": {
    "version": 1,
    "entries": [{ "id": "libraries", "span": 7, "rows": 4 }]
  },
  "updated_at": "2026-08-26T10:00:00Z"
}
```

### `PUT /api/v1/admin/dashboard/layout`

Body: `{"layout": {…}}`. Responds `204 No Content` on success, and
`400 bad_request` when the body is not valid JSON, when `layout` is absent or
`null`, when `layout` is not a JSON object, or when the body exceeds 16 KiB.

### `DELETE /api/v1/admin/dashboard/layout`

Resets this admin to the default arrangement. `204 No Content`, and idempotent:
deleting a layout that is not there succeeds.

## `GET /api/v1/admin/dashboard/capabilities`

Feature detection for the admin dashboard surface. Per the v1 rules a new
feature is detected rather than inferred from a server version, and every field
here is additive: a server that has this endpoint answers `true` for all of
them, and a server that predates the dashboard answers `404`. That is how a
client tells "this deployment is older than my build" from "the request failed".

| Field | Meaning |
|---|---|
| `server_layouts` | `GET`/`PUT`/`DELETE /admin/dashboard/layout` store the widget arrangement per admin account. |
| `timeseries` | `GET /admin/stats/timeseries` serves sampled concurrent-stream and egress history. |
| `playback_activity` | `GET /admin/stats/playback-activity` serves the rolling playback activity aggregate. |
| `top_activity` | `GET /admin/stats/top-activity` serves the leaderboards. |
| `health` | `GET /admin/server/status` carries the additive `health` object. |
| `log_level_list` | `GET /admin/logs/app` accepts a multi-level filter. |
| `watch_providers` | `GET /admin/stats` carries the per-provider `watch_providers` array. |
| `downloads_stats` | `GET /admin/stats/downloads` serves the offline-download aggregate, and timeseries points carry the additive `download_egress_kbps` split. |

```json
{
  "server_layouts": true,
  "timeseries": true,
  "playback_activity": true,
  "top_activity": true,
  "health": true,
  "log_level_list": true,
  "watch_providers": true,
  "downloads_stats": true
}
```

## `GET /api/v1/admin/stats`

Library, user, and playback totals for the dashboard, plus one entry per watch
provider. Cached in-process for 15s and bypassed with `?refresh=1`.

| Field | Type | Meaning |
|---|---|---|
| `total_items`, `total_files`, `total_users` | int | Catalog and account totals. |
| `total_movies`, `total_movie_files`, `total_shows`, `total_show_files` | int | Per-kind catalog totals. |
| `active_streams` | int | Playback sessions currently synced as live. |
| `total_storage_bytes` | int | Sum of every scanned media file's size. |
| `watch_providers` | object[] | One entry per watch provider, ordered by `provider`. Always an array, never null. |

`watch_providers` covers the union of the providers registered in the watchsync
registry — built-in and plugin-contributed alike, so a provider installed by a
plugin appears as soon as it registers, with zeros — and any provider that has
rows in the watch-provider tables. The second half of that union keeps history
visible after a provider's plugin is uninstalled; such an entry carries
`"registered": false` and falls back to its key as the display name.

Each entry:

| Field | Type | Meaning |
|---|---|---|
| `provider` | string | Provider key (`trakt`, `simkl`, `mdblist`, a plugin's key). |
| `display_name` | string | Human name from the registry, or the key when the provider is not registered. |
| `registered` | bool | False when the provider only exists in stored rows. |
| `scrobbling` | bool | The provider declares the scrobble-playback capability. |
| `exporting` | bool | The provider declares the export-watched capability. |
| `connected_profiles` | int | Profiles with a connection to this provider. |
| `enabled_profiles` | int | Connected profiles with at least one sync direction enabled. |
| `export_enabled_profiles`, `scrobble_enabled_profiles` | int | Connected profiles with that toggle on. |
| `last_sync_completed_at` | string | RFC3339 timestamp of the newest completed sync run. Omitted when there is none. |
| `sync_runs_24h`, `sync_errors_24h` | int | Sync runs started in the last 24h, and how many of those failed. |
| `imported_watched_24h`, `imported_progress_24h`, `exported_watched_24h` | int | Rows moved by those runs. |
| `pending_exports`, `failed_exports` | int | Queued history exports by status, all-time. |
| `open_scrobbles` | int | Scrobble sessions started but not yet stopped. |
| `scrobbles_24h` | int | Scrobble sessions touched in the last 24h. |

```json
{
  "total_items": 4821,
  "total_files": 5310,
  "total_users": 6,
  "total_movies": 1980,
  "total_shows": 212,
  "active_streams": 3,
  "total_storage_bytes": 91234567890,
  "watch_providers": [
    {
      "provider": "mdblist",
      "display_name": "MDBList",
      "registered": true,
      "scrobbling": false,
      "exporting": false,
      "connected_profiles": 0,
      "enabled_profiles": 0,
      "export_enabled_profiles": 0,
      "scrobble_enabled_profiles": 0,
      "sync_runs_24h": 0,
      "sync_errors_24h": 0,
      "imported_watched_24h": 0,
      "imported_progress_24h": 0,
      "exported_watched_24h": 0,
      "pending_exports": 0,
      "failed_exports": 0,
      "open_scrobbles": 0,
      "scrobbles_24h": 0
    },
    {
      "provider": "trakt",
      "display_name": "Trakt",
      "registered": true,
      "scrobbling": true,
      "exporting": true,
      "connected_profiles": 2,
      "enabled_profiles": 2,
      "export_enabled_profiles": 2,
      "scrobble_enabled_profiles": 1,
      "last_sync_completed_at": "2026-03-01T12:00:00Z",
      "sync_runs_24h": 5,
      "sync_errors_24h": 0,
      "imported_watched_24h": 30,
      "imported_progress_24h": 4,
      "exported_watched_24h": 12,
      "pending_exports": 0,
      "failed_exports": 0,
      "open_scrobbles": 1,
      "scrobbles_24h": 9
    }
  ]
}
```

`watch_providers` replaced the Trakt-hardcoded `watch_provider_activity` object,
which was removed pre-lock; see the removals table in
[architecture/v1-scope.md](architecture/v1-scope.md).

## `GET /api/v1/admin/stats/timeseries`

Sampled history for the concurrent-streams and egress charts. Cached in-process
for 30s, dropped early on playback or admin activity, and bypassed with
`?refresh=1`.

| Parameter | Type | Meaning |
|---|---|---|
| `hours` | int | Window length. Default 24, clamped to 1..744 (31 days, the retention window). A non-numeric value is `400 bad_request`. |
| `refresh` | bool | Bypass the cache for this read. |

Neither series can be reconstructed after the fact — live sessions leave no
per-minute trace once they end, and node egress is a rolling average that each
health check overwrites — so a sampler (`internal/dashmetrics`) writes them as
they happen, once a minute, into `dashboard_metric_samples`. Samples older than
31 days are deleted.

Reads bucket those minutes down so a response stays under ~750 points at any
window. `resolution_seconds` reports the bucket that was used — read it rather
than assuming the sampler's minute:

| Requested window | `resolution_seconds` |
|---|---|
| ≤ 2 hours | 60 |
| ≤ 48 hours | 300 |
| ≤ 336 hours (14 days) | 1800 |
| wider | 7200 |

A bucket wider than a minute reports the **peak** minute of each column, never
an average: these charts are read to answer "how bad did it get", and a mean
would erase exactly that. Stream counts and egress are maxed independently, so
a bucket's columns may come from different minutes within it.

Each minute holds up to two kinds of row. The `shared` row is the cluster-wide
snapshot: stream counts by play method, plus the egress reported by enabled,
healthy stream nodes. Every replica tries to write it and the first one to land
wins, so the values for a minute come from whichever replica got there first —
they differ only by sub-second timing. A `proc:<node_id>` row per API process
carries the viewer egress that process served, measured from stream telemetry;
without it a deployment with no stream nodes would chart zero egress forever.
Relay traffic is excluded, so bytes a proxy node passes through the API node are
not counted twice.

Stream counts in a point therefore come from the shared row, while `egress_kbps`
sums every source for a minute before the peak minute of the bucket is taken.
Precision is mixed by design: node egress is a 30-second rolling average and
process egress is an exact byte delta.

`egress_kbps` keeps its pre-split meaning — the total viewer egress across
every source — so a chart drawn from it alone stays truthful.
`download_egress_kbps` is the additive file-transfer subset of that total:
offline and direct downloads, ebook reads, and ABS file fetches, measured as
the actual bytes each API process wrote (including partial range-request
bodies). Node egress cannot be split and counts entirely outside the subset.
The sampler keeps the subset ≤ the total per minute, and each field takes its
own per-bucket peak, so neither can exceed the total and their difference is
never negative. But past the two-hour display resolution the two maxima are
preserved independently and may come from different minutes: subtracting them
does not yield any minute's playback rate. Chart the total and the download
subset as separate series rather than deriving a playback series. Samples
written before the split report `0` — read that as "not measured yet", not
"no downloads".

A bucket with no sample in it is absent from `points` rather than zero — a gap
(a restart, a stopped server) and an idle bucket are different facts. Stream
telemetry being disabled means no `proc:` rows, not an error.
`oldest_sample_at` is `null` until the first sample exists, which is how a
fresh install renders "collecting data" instead of an empty chart.

```json
{
  "resolution_seconds": 300,
  "from": "2026-08-25T12:00:00Z",
  "to": "2026-08-26T12:00:00Z",
  "oldest_sample_at": "2026-08-24T09:31:00Z",
  "points": [
    {
      "t": "2026-08-26T11:55:00Z",
      "streams": 3,
      "direct": 1,
      "remux": 0,
      "transcode": 2,
      "egress_kbps": 48211,
      "download_egress_kbps": 6100
    }
  ]
}
```

## `GET /api/v1/admin/stats/playback-activity`

Bucketed playback starts split by play method, plus reliability scalars, for the
admin dashboard. Answers are cached in-process for 60s and dropped early when
the shared event bus reports playback or admin activity; `?refresh=1` drops the
cache before reading.

| Parameter | Type | Meaning |
|---|---|---|
| `hours` | int | Window length. Default 24, clamped to 1..744. A non-numeric value is `400 bad_request`. |
| `refresh` | bool | Bypass the cache for this read. |

Buckets are hourly up to a 48-hour window and daily beyond it; `bucket_seconds`
is `3600` or `86400` accordingly. A bucket's `hour` field is its start instant
at either width — it keeps that name because it is the same fact, and
`bucket_seconds` already says how wide the bucket is.

Sessions come from `playback_history_admin` (which only gains a row when a
session finalizes) unioned with the live sessions table, so the current hour is
not under-counted. A live session cannot already be in history, so nothing is
counted twice. Live sessions with no recorded start — reconstructed after a
restart — are dated by their last update instead.

`from` and `to` are the queried window on the database clock — the clock the
bucket filter ran against. Clients should anchor their zero-fill grid on `to`
rather than their own clock: a browser a minute behind the server around a
boundary would otherwise discard the newest bucket.

`buckets` contains only buckets that saw a session; the client zero-fills the
window on the `bucket_seconds` grid so a quiet server draws empty columns rather
than a shorter chart. Everything in `reliability` is computed over the whole
requested window. `completion_rate` is
`completed_sessions / finalized_sessions`: live sessions are excluded from both
sides, because a session that is still playing has not failed to complete.

`profiles_active_24h` is a fixed rolling-24h figure that ignores `hours` — it
answers "who watched today" whatever window the chart beside it is showing. It
counts distinct (account, profile) pairs in
`user_watch_history` over a rolling 24 hours, excluding history that was
imported or synced from a watch provider (`import`, `trakt`, `simkl`,
`mdblist`), so it means "watched on this server". Marked-watched (`manual`)
rows are counted: they are on-server actions.

**Not reported:** time-to-first-frame and failed-start counts. Nothing records
a playback *start* event today, so both would have to be inferred from log
parsing. They need start-event capture in playback first, and are deliberately
absent rather than approximated.

```json
{
  "hours": 24,
  "bucket_seconds": 3600,
  "from": "2026-08-25T10:41:03Z",
  "to": "2026-08-26T10:41:03Z",
  "buckets": [{ "hour": "2026-08-26T10:00:00Z", "direct": 4, "remux": 1, "transcode": 2 }],
  "reliability": {
    "sessions_started": 42,
    "transcode_starts": 11,
    "finalized_sessions": 38,
    "completed_sessions": 27,
    "completion_rate": 0.7105,
    "unique_profiles": 9
  },
  "profiles_active_24h": 9
}
```

## `GET /api/v1/admin/stats/top-activity`

Most-watched titles and most-active profiles over a multi-day window. Cached
for 5 minutes — a seven-day ranking barely moves within minutes — with the same
`?refresh=1` escape hatch.

| Parameter | Type | Meaning |
|---|---|---|
| `days` | int | Window length. Default 7, clamped to 1..30. |
| `limit` | int | Rows per list. Default 10, clamped to 1..25. |
| `refresh` | bool | Bypass the cache for this read. |

`plays` on both lists counts `user_watch_history` rows with the same source
exclusions as `profiles_active_24h` above, so marking something watched counts
as a play. Episodes are rolled up to their series, so a season binge reads as
one show and a title's `media_item_id` is a series content id for TV.

`total_seconds` is **watched time**, summed from finalized playback sessions
(`playback_history_admin.watched_seconds`) that *ended* inside the same window
— the same stop instant `watched_at` records, so plays and watch time see the
same sessions — not the runtime of what was played. Watch history records the media's full duration,
so summing that would report three hours for a movie someone abandoned after a
minute. An entry that was only ever marked watched has no sessions and reports
`0`. Because `watched_seconds` records a session's final absolute position, a
resumed session would claim the already-watched stretch again, so each
session's contribution is capped at its wall-clock length; the figure is an
estimate until playback records true elapsed viewing time.

Profile display names live in the per-user stores rather than in watch history,
so they are read back from that profile's most recent `playback_history_admin`
row; a profile that has only ever marked things watched falls back to its
profile id. Ties are broken on a stable key (`media_item_id`, or
`user_id`/`profile_id`) so equal rows keep their order between refreshes. No
poster URLs are returned — the bar-list widgets do not need them, and it keeps
the query cheap.

Both lists are `[]` on a server with no history, never `null`.

```json
{
  "days": 7,
  "limit": 10,
  "titles": [
    {
      "media_item_id": "…",
      "title": "…",
      "media_type": "series",
      "plays": 18,
      "total_seconds": 54120
    }
  ],
  "profiles": [
    {
      "user_id": 3,
      "username": "quick",
      "profile_id": "p1",
      "profile_name": "Quick",
      "plays": 12,
      "total_seconds": 40100
    }
  ]
}
```

## `GET /api/v1/admin/stats/downloads`

Offline-download aggregate for the dashboard's downloads widget. Cached
in-process for 60s, dropped early on admin activity from the shared event bus,
and bypassed with `?refresh=1`.

| Parameter | Type | Meaning |
|---|---|---|
| `limit` | int | Rows in `top_users`. Default 10, clamped to 1..25. A non-numeric value is `400 bad_request`. |
| `refresh` | bool | Bypass the cache for this read. |

The aggregate reads the `downloads` table, which carries two lifecycles: a
**managed device entry** (a device keeps the item offline; `device_id` set) and
a **one-shot web download** (`device_id` null, pruned over time). "Active"
means a managed entry whose status is `queued`, `preparing`, `ready`,
`downloading`, or `completed` — anything that has not ended in failure,
cancellation, or revocation. The headline numbers and `top_users` count active
managed entries only; the 24-hour counters cover both lifecycles, so one-shot
web downloads show up there.

| Field | Type | Meaning |
|---|---|---|
| `users_with_downloads` | int | Distinct accounts (login accounts, not household profiles) with at least one active managed download. |
| `active_downloads` | int | Active managed entries. A series batch contributes one entry per episode. |
| `total_bytes` | int | Sum of `file_size` over completed managed entries — bytes sitting on devices as far as the server can know without devices reporting back. |
| `downloads_started_24h` | int | Rows created in the last 24 hours, both lifecycles. |
| `downloads_completed_24h` | int | Rows that reached `completed` in the last 24 hours, both lifecycles. |
| `limit` | int | The clamped `top_users` size the response was built with. |
| `top_users` | object[] | Accounts ranked by active managed downloads; `[]` when nobody downloads, never `null`. |

Each `top_users` entry: `user_id`, `username`, `downloads` (active managed
entries), and `total_bytes` (completed managed entries only, like the headline).

A deployment with the downloads feature disabled answers all zeros rather than
an error — the table exists on every deployment.

```json
{
  "users_with_downloads": 2,
  "active_downloads": 14,
  "total_bytes": 52613349376,
  "downloads_started_24h": 3,
  "downloads_completed_24h": 2,
  "limit": 10,
  "top_users": [
    { "user_id": 3, "username": "quick", "downloads": 11, "total_bytes": 41234567890 },
    { "user_id": 5, "username": "kid", "downloads": 3, "total_bytes": 11378781486 }
  ]
}
```

## `GET /api/v1/admin/server/status` — `health`

The status route carries an additive `health` object for the dashboard health
strip. Every field the route already returned is unchanged; only `health` is
new, and the example below is trimmed to the fields it discusses:

```json
{
  "started_at": "2026-08-26T09:00:00Z",
  "restart_required": false,
  "health": {
    "postgres": { "configured": true, "ok": true, "latency_ms": 1.42 },
    "redis": { "configured": true, "ok": true, "latency_ms": 0.31 },
    "errors_24h": 4,
    "warnings_24h": 12
  }
}
```

Each component reports `configured` first: `false` means this deployment runs
without that service — a supported single-node shape for Redis — and `ok` and
`latency_ms` are then absent, so "not present" and "present but broken" do not
look the same on the strip. Latency is the round trip of one ping, in
milliseconds with two decimals, bounded by a 2s timeout: a wedged dependency is
reported as `ok: false` rather than holding the route open.

`errors_24h` / `warnings_24h` count `operational_logs` rows at those levels over
a rolling 24 hours, cached for 30s. A server with operational logging disabled
reports zeros and logs a warning; this route never fails over a secondary
number.

Version, uptime and node health are not repeated here. The client composes them
from `GET /admin/system/build`, `started_at` above, and `GET /admin/nodes`.

## `GET /api/v1/admin/logs/app` — `level`

`level` accepts a comma-separated list, so one request can ask for several
levels at once (`?level=error,warn`). Values are trimmed, lowercased and
de-duplicated; a single value behaves exactly as before. The same parsing
applies to the log-stream WebSocket, so a stream filtered on two levels
delivers both.

## API v2 playback history

`GET /api/v2/admin/playback-history` (`listAdminPlaybackHistory`) ports
`GET /api/v1/admin/playback-history`: the finalized playback log across every account and
profile, newest ended first, for acting administrators. It keeps the v1 filters
(`user_id`, `profile_id`, `media_item_id`, `completed` as `all`, `true` or `false`) and
replaces offset paging with `limit` (default 50, maximum 200) plus an opaque `cursor` bound
to the operation, acting account and profile, filters and limit; `offset` is refused with
422. Each page is one consistent read ordered by `ended_at` then `session_id`; later pages
read the live log. Account and file identifiers are strings, instants are RFC 3339, and
`duration_seconds` is null when the media duration was never recorded. A deleted account
lists with an empty `username`; an item no longer in the catalog lists with an empty
`media_title` and `media_type`. The stored client address is not part of this projection,
matching v1. A missing database returns 503; storage errors are masked as 500.

The administrator history page and the user detail watch-history tab read this route
under the authority captured when the query was created, keyed by an opaque authority
generation, and refuse a page whose account, server, profile or PIN authority changed while
it was in flight. Both views keep their single-page reading at the v2 page ceiling.

## API v2 history imports

The administrative history-import surface uses `/api/v2/admin/history-import-sources`
for source configuration and `/api/v2/admin/history-imports` for mappings, credentials,
and runs. All operations require an acting administrator. Mutations enforce the
demo restriction; reads do not.
`GET /api/v2/admin/history-imports/capabilities` reports availability, guarded
configuration, durable administrative runs, and the 200-mapping bulk limit.

Read a source or mapping by ID before editing it. The response contains its canonical
configuration and a strong `ETag`. Send that exact tag in `If-Match` on source,
mapping, or token updates and deletes. Missing preconditions return 428; stale
versions return 412 with the current tag. Reload explicitly before retrying an edit.
Token replacement advances the source version without exposing the saved token.
Legacy credential-bearing source URLs are redacted and marked `needs_reconfiguration`;
review and save a safe address before using the source again.
Changing a credential-bearing source address or system ID also requires a replacement
token or an explicit empty-string clear. Source and mapping deletes, including token
clear, return 204. Mapping labels and import timestamps are not canonical editor fields.

Lists return `items` and `page`; follow the opaque `next_cursor`. Limits default to 50
and cannot exceed 200. Mapping lists require `source_id`; run lists optionally filter
by it. Source and mapping configuration sets and upstream user discovery are read as
sets and exposed in bounded ID-ordered pages. Run history uses database keyset paging.
Cursors are bound to the acting account, profile, operation, and filter. IDs are strings
and run timestamps are UTC instants.

The web history-import page displays mapping loads and failures explicitly. A
failed list request is not an empty mapping set: the page offers a retry and
withholds mapping controls until the list succeeds.

Starting a mapping run returns 202 only after durable dispatch intent is stored. Poll
its `Location`, respecting `Retry-After`, until `terminal` is true. Polling supports
`ETag` and `If-None-Match`; pending cancellation is reported as `canceling`. Bulk start returns
200 with an ordered outcome for each mapping (`accepted`, `active`, or `failed`), run
locations when available, and counts for each outcome. A source with more than 200
mappings is rejected before any run admission. Bulk work is partially successful;
it is not an atomic transaction across mappings. Mutations are not automatically
retryable after an uncertain response.

Cancellation records intent. A queued run can become cancelled immediately; a running
worker acknowledges cancellation at its next boundary. Pending cancellation returns
202, an already cancelled run returns 200, and completed or failed runs return
409 `job_not_cancelable`. Already committed external
history effects are not undone. Editing a source or mapping invalidates previously
captured execution configuration rather than retargeting its run. Stale executions
fail without automatic replay. See [History import execution](architecture/history-import-execution.md)
for the persistence and cancellation boundaries. Run monitor errors and warnings use safe summaries; stored source
credentials are never returned. The explicit Plex login exchange returns its newly
issued token so the administrator can assign it to a source.


### Personal history import acceptance and monitoring

`POST /api/v2/history-imports/runs` accepts an account-owned import for the supplied
profile. It returns 202 with the persisted queued run, canonical `Location`, and
`Retry-After: 2` only after both execution intent and encrypted run credentials commit.
Session-backed imports consume their login session in that transaction. Passwords are
exchanged before admission and are never persisted. An uncertain response must not be
automatically resubmitted; check the account's import list before starting another run.

Poll `GET /api/v2/history-imports/runs/{id}` at its `Location`. The response has a strong
`ETag`, supports `If-Match` and `If-None-Match`, and returns a bodyless 304 when unchanged.
The account ownership check runs before evaluating either precondition; another
account's run returns 404. Nonterminal responses, including 304, carry `Retry-After`.
When `terminal` is true, stop polling; terminal responses omit the polling hint.

The personal projection always reports `cancelable: false`: this surface has no cancel
command. Existing administrator cancellation can appear as nonterminal `canceling`
until the worker acknowledges it, then terminal `cancelled`. Both personal and admin
monitors replace persisted diagnostic errors, warnings, and unmatched reasons with safe
summaries. Run credentials and private dispatch metadata never appear in these responses.

New queued personal imports survive server restart. Source changes invalidate captured
configuration without retargeting the import; stale running executions fail without replay.
Already committed history effects are retained. Historical personal jobs without durable
metadata are left unchanged by the migration. See
[History import execution](architecture/history-import-execution.md) for transaction,
claim, cleanup, and failure boundaries.

## V2 dashboard aggregate reads

The dashboard uses four acting-administrator operations under `/api/v2/admin/stats`:

| Path | Operation ID | Query bounds |
| --- | --- | --- |
| `/timeseries` | `getAdminDashboardTimeseries` | `hours`: 1–744, default 24 |
| `/playback-activity` | `getAdminDashboardPlaybackActivity` | `hours`: 1–744, default 24 |
| `/top-activity` | `getAdminDashboardTopActivity` | `days`: 1–30, default 7; `limit`: 1–25, default 10 |
| `/downloads` | `getAdminDashboardDownloadsStats` | `limit`: 1–25, default 10 |

All accept `refresh=true` to invalidate the provider cache before reading.
Out-of-range v2 inputs return a validation Problem; the bridge continues its
existing clamping behavior. Missing data services return 503. The web normalizes
its window before choosing a query cache key and retains its existing page-owned
refresh cadence.

Both transports use the same providers, invalidation, and aggregate queries.
The database clock still determines windows and bucket boundaries; sampled gaps
remain gaps, and `oldest_sample_at: null` means no samples exist yet. V2 timestamps
use UTC with millisecond precision. Account IDs cross v2 as opaque strings.
Top activity still ranks household profiles, while download totals rank accounts
and preserve the existing managed-device versus transient-web definitions.
These endpoints report existing aggregates; they do not infer missing playback
start telemetry or promise instantaneous device state.

The native clients and Jellyfin compatibility do not consume these dashboard
administrator aggregates. The web discards responses decoded after the selected
profile authority changes.

## V2 node inventory

`GET /api/v2/admin/nodes` (`listAdminNodes`) returns a collection of configured
nodes and their stored health, hardware, and resource observations. It requires
an acting administrator and the selected profile's verification headers.
The endpoint does not probe, reload, or schedule work on a node. An unavailable
repository returns 503.

Pages default to 50 entries and accept at most 200. A signed cursor binds the
administrator, selected profile, and page size. Ordering is by type, name, then
opaque node ID as the unique tie-breaker. A configuration change during paging
is not a snapshot; restart the list to obtain the updated ordering. The current
repository reads the configured inventory per page, matching the existing
administrator read source. The web reads successive pages under one captured
authority and retains the existing visibility-gated health polling cadence.

V2 node IDs are strings and metadata timestamps use UTC milliseconds. The
original stored worker capability, drift-baseline, and resource documents retain
their owning wire format. An absent `advertised_capabilities_hash` means the
process has not checked the node; an empty string means a check returned no hash.
A stored capability hash alone does not establish freshness or worker protocol
compatibility. Worker readiness and wire behavior remain owned by the worker
protocol layer. No native client or Jellyfin caller consumes this administrator
inventory operation.

## V2 server status

`GET /api/v2/admin/server/status` (`getAdminServerStatus`) returns the API
process's start time, restart-required reasons and mark counter, restart-request
state, and existing dependency-health observations. The web restart banner and
health strip consume this read through the captured administrator/profile
session boundary. Response timestamps use UTC with millisecond precision.

The restart tracker is process-local and resets when that process restarts.
This response does not describe a durable job, acknowledge completion of a
restart, or establish cluster-wide restart state. An unhealthy configured
Postgres or Redis remains a 200 response with `ok: false`; a service that is not
configured omits `ok` and latency. The existing bounded probes, optional
Jellyfin restart derivation, and cached log counts are shared with the bridge.
Failure to read settings does not hide the dependency-health response. A missing
administrator status service returns 503.

No native client or Jellyfin-protocol consumer calls this administrator read.

## V2 SMTP configuration test

`POST /api/v2/admin/email/test` (`sendAdminTestEmail`) accepts `{ "to": "recipient@example.test" }`
and synchronously submits one test message through the saved SMTP settings.
It requires acting-administrator access. A 200 response contains `ok`,
`duration_ms`, and an optional safe explanation when the mail server did not
confirm the send. Invalid recipients return 422; an unavailable sender returns
503. V2 excludes provider error text from the response. The bridge keeps its
existing response behavior and shares the message construction and send call.

This operation is non-retryable: a lost response does not prove the message was
not sent. There is no durable job or replay identity. The web captures the
recipient and profile when the administrator submits, prevents another dispatch
while pending, and disables authentication replay. A profile change suppresses
late result presentation. No native or Jellyfin caller consumes this operation.

### Rate-limit configuration reads

`GET /api/v2/admin/rate-limits/config` returns desired rate-limit settings with a
strong, administrator/profile-bound ETag and conditional read support. It includes
API-key tiers and authentication endpoint budgets. Map entries remain extensible.
`GET /api/v2/admin/rate-limits/status` returns process-local `active`, optional
`active_backend`, and `redis_available`. The latter reflects valid persisted or
bootstrap Redis configuration, not a successful reachability probe. Both reads
require acting-administrator authority and report missing dependencies as 503.

Runtime observations are separate from the canonical configuration validator.
The web reads both under captured account/profile authority and discards results
after an authority change. The two reads are not an atomic runtime snapshot;
backend changes may require a restart. The frozen bridge retains its combined
response and existing save path. This read slice adds no write, reload, job, or
cross-replica convergence guarantee.

`PATCH /api/v2/admin/rate-limits/config` merges supplied configuration fields;
omitted fields and map entries keep their values, while explicit null is rejected.
An `If-Match` validator is required. Both conditional headers are evaluated against
the canonical configuration inside the same settings transaction that validates
and saves changes, including wildcard conditions and Redis backend eligibility.
A stale request returns 412 with the current validator. Invalid budgets or backend
selection return 422. Success returns `status` and `restart_required`; this receipt
is not the canonical configuration and does not carry an ETag. Read config again
for the next edit.

Unchanged configuration causes no write or reload. After a change, local reloads
serialize the store read and configuration application so an older concurrent
reload cannot overwrite a newer snapshot. Backend changes or enabling an absent
limiter may require restart. Multi-instance reload event publication remains best
effort; a successful save is not a durable cluster-wide application receipt. A
post-commit failure can leave settings saved, so clients should reload and inspect
state before another edit.

The web captures the draft, displayed validator and account/profile authority at
Save, before awaiting the preceding general-settings save. Offline queued writes
retain that intent. It never refreshes/replays, retries or rebases automatically;
a 412 asks the administrator to reload and review. Draft hydration includes
validator and authority identity so edits cannot carry into another profile's
otherwise identical configuration.

### Settings discovery

`GET /api/v2/admin/settings/{key}` returns one visible stored value and its
`restart_required` flag. It shares the bridge's bootstrap precedence and hides
sensitive and machine-managed keys with 404. Missing and empty values also remain
404. The setup wizard uses this route for its Redis configuration read and treats
only 404 as absence, rejecting stale responses after an authority change.

`GET /api/v2/admin/settings/sections` reuses the existing profile-section flag
reader: read failures preserve the disabled default. It requires acting-admin
authority. `GET /api/v2/admin/playback-routing/capabilities` exposes the shared
routing vocabulary under the same authority; these are configuration choices,
not evidence of available worker capacity. No recorded first-party consumer uses
these two discovery reads.

### Jellyfin compatibility status

`GET /api/v2/admin/jellyfin-compat/status` requires acting-administrator authority
and shares the bridge's configured compatibility and local web-component status
reader. It preserves configured API/web states, versions, provenance, installer
prerequisites, restart indication and local operation progress. This is not a
listener-health probe or a durable cluster-wide job receipt. The underlying reader
can reconcile stale installation locks on the local filesystem.

Installation and operation timestamps use UTC milliseconds; missing or malformed
historical timestamps are omitted. No operation is represented by an absent
`operation`, and missing prerequisites are an empty array. The web preserves its
existing refresh behavior and discards status decoded after an account/profile
change. This read adds no installation, update, removal or provider request.

### Autoscan callback URL display

The autoscan administrator UI displays and copies the accepted
`/api/v2/autoscan/webhooks/{token}` delivery URL, including immediately created or
rotated source responses. Bridge management responses retain their frozen URL;
the UI projects only the known delivery path to v2 and preserves the configured
host, deployment prefix, query and existing secret. An already-v2 URL is unchanged.

Operators must replace the saved URL in existing download-manager connections.
Displaying or copying it does not rotate the secret, update a provider, send a
test delivery or replay an event. New setup and existing-source instructions both
state the external configuration step. Other source-management operations remain
separate migration work.

`GET /api/v2/admin/autoscan/sources` returns administrator-only source metadata in
signed, account/profile/page-size-bound cursor pages ordered by label then source
ID. Each page retains the existing full configured-source and batched endpoint
queries; it is not a database-bounded query or a snapshot. Existing token reveal
failures preserve webhook status while omitting the URL. Successful reveals emit
the v2 delivery path using the existing token and configured public base.

Source timestamps use UTC milliseconds. Path rewrites remain an array; connection
identity and stored source configuration retain their existing meanings. The web
collects at most 100 pages of 100 sources, rejects missing/repeated continuations
or overflow, and discards source URLs decoded after an authority change. Reads do
not create sources, rotate tokens, dispatch events or update external providers.


Autoscan settings and status reads are available at `GET /api/v2/admin/autoscan/settings`
and `GET /api/v2/admin/autoscan/status`. The settings validator describes desired
configuration and is scoped to the acting administrator/profile. Status combines
sequential source, poll and queue reads; it is not an atomic scheduler snapshot.
Running poll IDs are opaque strings and timestamps use UTC milliseconds. Source
observations omit webhook credentials and source configuration. The web settings
and activity queries use these reads under captured authority and retain their
existing refresh cadence. Bridge configuration mutations and scheduler updates
remain unchanged. No Apple, Android or Jellyfin caller uses these administrator reads.

`GET /api/v2/admin/autoscan/connections` lists configured connections using a
signed administrator/profile/limit-bound name-and-ID cursor. Each page enumerates
the full configured connection list; concurrent edits can reorder the live list.
The response preserves connection metadata and `has_api_key`, while excluding
credential references and resolved secrets. Reading does not contact a provider.
The existing web picker drains bounded pages under captured authority; connection
writes remain on the bridge. No native or Jellyfin connection-list caller exists.

### Native administrator playback observations

`GET /api/v2/admin/sessions` returns `{items, page}` through the shared live
playback-session loader. The enriched account/profile, requested/selected file,
source/target audio, client, compatibility and routing fields remain available.
Numeric identifiers are decimal strings and timestamps use UTC milliseconds.
The detailed session list also accepts `user_id` to filter by login account
before pagination. Cursors bind that filter, the page size and caller authority;
changing a filter requires a fresh first page.

`GET /api/v2/admin/sessions/summary` returns `{count, items}`. Optional `user_id`
filters by account; omitted means all accounts. `limit` chooses a sample of 1–100
observations (default 20), ordered by start time descending and session identity
as the tie-breaker. The count includes every matching observation and shares the
sample's database snapshot. Missing accounts or no activity return zero and an
empty array. The summary has no pagination.

Summary items expose only account ID, media title/type, optional series/episode
labels and numbers, and paused state. They omit session/profile identifiers,
client network/device details, file IDs and playback controls. The summary reuses
the live session loader and inherits its synchronization and cleanup delay.
`admin:sessions:summary:read` grants only this summary and session capabilities;
it does not grant detailed observations or session controls. The key owner must
still be an administrator. This is access to summaries across accounts;
`user_id` filters results and is not an account-specific authorization grant.
Capabilities advertise `user_filter` and `summary` when the reader is available.

`GET /api/v2/admin/sessions/capabilities` retains the shared feature vocabulary,
adds `available` for this loader and `node_observations` for the Redis reader.
These reads require an acting administrator and do not enforce a demo restriction.

`GET /api/v2/admin/node-sessions` returns `{items, page, undecodable}` using the
owning node-session reader. Optional positive string `node_id` filters by the
registered node URL; invalid IDs fail validation and unknown nodes return 404.
The typed projection carries one observation per node and session and reports
unreadable records from the source enumeration before filtering. Missing or
unparseable start timestamps are null, and the original nanosecond precision is
the decimal string `started_at_unix_nano` (`"0"` when absent). Unknown numeric
ownership keys remain `"0"`; the older `user_id` is a display label, while
`auth_user_id` identifies the account when reported. The frozen v1 raw-record
response is unchanged.

Both lists accept `limit` (default 50, maximum 100) and an opaque `cursor` bound
to the acting account/profile, filter and page size. The playback list applies
its session-ID keyset and limit in SQL, fetching one extra row to determine
continuation. The frozen v1 loader retains its newest-200 cap. The node list
bounds response size but rereads the live source on each page. The node reader
refuses a truncated 50,000-record enumeration with 503 instead of claiming a
complete list. Expired keys and failed GETs can be absent under the existing
best-effort reader semantics. Redis observations may include records for
sessions that have already ended, until cleanup or TTL expiry. These are not
snapshot listings; refresh to discover new records behind a cursor. The web
drains all pages under one captured authority, rejects broken continuation, and
publishes no partial list on failure.

These projections do not grant playback-control authority. In particular,
`has_playback_control` describes the existing live control connection, not a
sequenced administrator stop capability. The five administrator mutations need
a playback-owned adapter that authorizes the administrator separately and
retains the exact target binding and durable stop identity. The caller-bound
user stop cannot be reused by forging the target account/profile context. No
legacy cleanup fallback is introduced by these reads. No Apple or Android HTTP
caller for these three administrator reads was found; neither platform gains a
new caller. Jellyfin has no equivalent administrator diagnostic contract to
migrate.

`GET /api/v2/admin/stats` exposes the existing dashboard statistics provider,
including cached aggregates, explicit refresh, PostgreSQL fallback and the
account-count fallback when PostgreSQL is absent. Query/TTL semantics are unchanged.
Provider history remains visible after a provider is unregistered; absent rows
serialize as an empty list and last-sync times use UTC milliseconds. The actual
web query and manual-refresh cache writer share the captured authority key and
reject results decoded after an authority switch. These are aggregate observations,
not an atomic cluster snapshot. No native or Jellyfin caller uses this admin read.

`PUT /api/v2/admin/settings/sections` replaces `allow_profile_custom_sections`.
The administrator GET on the same path now returns an actor/profile-bound ETag
and supports conditional reads. PUT requires `If-Match`, accepts `If-None-Match` as an additional exclusion, and
evaluates both against current canonical state inside the existing settings
transaction; stale state returns 412, and unchanged state performs no write.
The required boolean rejects omitted and null values. The response contains the
canonical flag and its ETag. The profile-facing flag reader keeps its existing
disabled default on read failure; the write fails closed without an atomic store.
No first-party or internal writer is recorded, so no new UI or native flow is added.
The bridge writer and profile section enforcement remain unchanged.

### Sequenced administrator playback commands (v2)

`POST /api/v2/admin/sessions/{session_id}/pause`, `/resume`, `/stop` and
`/message` port the bridge control routes with an ordered command identity the
session applies once. `/terminate` is ported separately below with a different
contract: it revokes first and notifies second.

Every request body carries `command_id` (a client-allocated canonical UUID) and
`sequence` (a client-allocated positive integer that must rise within the
session), plus optional `reason` and `deadline_ms` (bounded to 10000, default
3000; ignored by message). Message adds a required `message` and optional
`title`. Allocate the identity once per intended command and preserve the
whole body on retry.

The server keeps one ledger per playback session in the process that serves
the session's realtime lane and answers deterministically:

- A new identity above the latest applied sequence is dispatched once: `202`
  with `{command_id, sequence, outcome: "applied", delivery}`.
- The same identity with the same action, actor and content is a replay:
  `200` with the recorded receipt (`outcome: "replayed"`); nothing is sent again.
- The same `command_id` with different content is `409 idempotency_conflict`.
- A new identity whose sequence is at or below the latest applied sequence is
  `409 conflict` and is never dispatched, so a delayed retry of a pause that
  lands after a resume cannot revert the newer state.
- Pause, resume and message require a live realtime lane (`409 conflict`
  otherwise). Stop without a lane answers `delivery: "fallback_scheduled"` and
  the server ends the session after the deadline, as on the bridge.
- An unknown session is `404`; a server without playback control answers
  `503 dependency_unavailable`.

`GET /api/v2/admin/sessions/command-capabilities` reports `available`, the
`actions` list and `sequenced_commands: true`. All of these require an acting
administrator. Commands enforce the demo restriction; capability reads do not.
The web session actions send
pause, resume, stop and message through these operations under captured
administrator authority and allocate a fresh identity per click; terminate
stays on the bridge.


### Administrator terminate (v2)

`POST /api/v2/admin/sessions/{session_id}/terminate`
(`terminateAdminPlaybackSession`) reverses the bridge order. The bridge sends a
terminate command and, when the player is silent, ends the session after a
deadline: revocation follows the client. On v2 the server first revokes the
session's playback authority durably, then dispatches the dismissal as best
effort, and reports the two facts separately. It never promises that media
already buffered stops instantly, and it never waits for the player.

Revocation is the ordinary user stop plus the stream deny marker. The server
stops the live session and its transcode, runs the stop and history writer, and
writes the Redis key `silo:streamauth:<session_id>` so no replica serves or
reconstructs the session from a stream token that is still inside its TTL
([restart-resilient playback](architecture/restart-resilient-playback.md)). Only
then is the dismissal dispatched on the session's realtime control lane, without
waiting for an acknowledgement. Terminates for one session are serialized, so a
repeat observes the first outcome instead of racing it.

The optional JSON body carries `reason`. The `200` receipt carries
`session_id`, `authority_revoked`, `already_revoked`, `durable_state`,
`client_notified`, `delivery` (`dispatched`, `unavailable`, `failed`, `none`)
and `command_id` when a dismissal was issued. `durable_state` reports `stopped`:
a terminate that succeeds always commits the terminal stop. `already_revoked`
stays `false`, because a repeat against a session this server no longer holds is
`404` rather than a converged receipt. A player that is offline yields
`authority_revoked: true` with `client_notified: false` and `200`. The ledger
row keeps `natural_idempotent`. An unknown or already-ended session is `404`; a
session whose stop cannot be committed in its current state is `409`. The
operation requires an acting administrator, is restricted in demo mode, and is
registered unconditionally: it answers `503` and the shared command capability
lists `terminate` with `terminate_revokes_authority: true` only when the durable
revocation seam is wired. The bridge terminate route is unchanged. The web
session actions send terminate through this operation under captured
administrator authority and show both facts.

### General settings writes in v2

`PUT /api/v2/admin/settings` accepts `{ "values": { "key": "value" } }`;
`PUT /api/v2/admin/settings/{key}` accepts `{ "value": "value" }`. Both require
an acting administrator and `If-Match` from the stored or effective settings GET.
`If-None-Match` is an optional additional exclusion. Null values are rejected.
The validator covers raw stored settings and effective defaults, including hidden
secret changes, using a keyed digest bound to the acting account/profile/access
scope. Neither raw secrets nor digest inputs are returned to the client.

The service checks the precondition before prerequisite checks and again against
the current settings inside the existing atomic update. A stale request returns
412 without writing settings or publishing settings notifications. A successful
response is a redacted update receipt, not a new canonical settings snapshot.
The batch PUT returns the settings ETag captured from its resulting state under
the transaction lock. Read settings again before preparing another edit. Retained
drafts may advance automatically only when that read's validator exactly matches
the acknowledged batch validator; an intervening writer's snapshot cannot rebase
them. Batch validation considers the
prospective combined values. Single-key validation preserves the existing paired
setting repair behavior. Empty values retain the established clear/default rules.

Both writes are nonretryable. Enabling diagnostics can probe storage before the
transaction, and runtime notifications after commit are not durable command
receipts. A lost response does not establish whether the update committed. The
web captures the displayed validator, copied values and acting profile before an
offline pause, does not refresh/replay a failed mutation, and keeps dirty edits on
the original baseline across background reads and 412 responses. Changing acting
authority clears local drafts. The diagnostics upload toggle uses the same guarded
single-setting writer.

Trusted-proxy reloads serialize each authoritative read and resolver application
within an API process. Direct settings callbacks, Redis notifications and config
watchers use that same ordering; a failed read retains the previous trusted set.
This prevents an older local read from overwriting a newer applied value. It does
not make settings notifications a durable queue or provide simultaneous trust
updates across replicas. The frozen bridge retains its existing wire behavior.

### Jellyfin compatibility settings patch in v2

`PATCH /api/v2/admin/jellyfin-compat/settings` accepts the existing optional
`enabled`, `public_url`, `server_name`, `emulated_server_version`, `web_enabled`,
`web_version`, `web_dir` and `web_install_dir` fields. Read the canonical general
settings GET and send its `ETag` in required `If-Match`; `If-None-Match` optionally
excludes another state. Explicit null fields and empty patches are rejected.

V2 validates the guard and applies the complete patch under the shared settings
transaction. Disabling compatibility forces `web_enabled` off. `web_dir` remains
managed: an empty value resolves to the managed path under the supplied or stored
installation root; arbitrary active directories are rejected before any write.
The bridge preserves its existing per-key writes and error precedence.

After commit, settings notifications and restart markers retain their existing
behavior. The response uses the existing desired/active compatibility status
projection, including local installation observations. A successful settings patch
does not install/update/remove web assets or restart the listener. The operation is
nonretryable because those notifications are not durable replay receipts. The
existing web serving toggle captures its displayed settings validator, copied
patch and profile before an offline pause, disables authentication replay, and
invalidates the exact compatibility status scope only for the active authority.

### Dashboard capability discovery in v2

`GET /api/v2/admin/dashboard/capabilities` returns the eight dashboard support
flags to an acting administrator. These describe the v2 surface in the API build;
they do not promise that a database or other runtime dependency is healthy.

`timeseries`, `playback_activity`, `top_activity`, `health`, `watch_providers` and
`downloads_stats` are true for the existing v2 dashboard operations.
`server_layouts` remains false while the v2 layout lifecycle is unavailable.
`log_level_list` is true for the accepted multi-level application-log reader. Clients
must not infer v2 support from the bridge's independently retained capabilities.

### Retained application-log read in v2

`GET /api/v2/admin/logs/app` returns a paginated `items` collection for an acting
administrator. `limit` defaults to 50 and accepts 1–200. Filters retain the existing
`level` (comma-separated, trimmed/lowercased/deduplicated), `component`, `node_id`,
`request_id`, `user_id`, `session_id`, `playback_session_id`, `q`, `from` and `to`
meanings. Session fields distinguish login sessions from playback sessions.
Time bounds remain inclusive; text search retains its existing SQL ILIKE behavior.

The repository still fetches at most limit+1 entries ordered by descending
(timestamp, id). V2 wraps the original source cursor in a signature bound to the
acting account/profile/access scope, normalized filters and page size. The cursor
retains the source timestamp's full precision even though returned timestamps
use UTC milliseconds. This live traversal is not a snapshot and does not promise
coverage of entries committed later with older timestamps or retained after a
retention sweep.

Identifiers use decimal strings. `attrs` is the logging component's structured
JSON extension bag; this read preserves existing collector redaction and data
semantics. Database errors remain private. The FFmpeg activity panel and recent
errors widget use the scoped v2 reader. Their existing numeric UI model checks
safe-integer conversion and visibly rejects unrepresentable identifiers rather
than rounding them. The main log-viewer websocket, its cursor protocol and audit
logs remain separate.

### Administrator log stream handshake in v2

`GET /api/v1/admin/logs/ws` is retained as a plain WebSocket path with a documented
handshake, like the realtime events and playback control sockets. Its v2 form is
`GET /api/v2/admin/logs/ws` (`connectAdminLogsSocket`), registered as a raw handshake in
`openapi.json` through the raw-operation registry rather than a Huma operation.

`GET /api/v2/admin/logs/ws/capabilities` (`getAdminLogsSocketCapabilities`) reports whether
the handshake is served on this process, the protocol (`silo.admin-logs.v2`) and the stream
selections (`app`, `audit`). `POST /api/v2/admin/logs/ws-ticket` (`createAdminLogsSocketTicket`)
delegates the acting administrator's current access-token login session for one connection.
API keys and credentials without a bounded expiry cannot mint; a login whose effective role is
not administrator (a secondary profile on an administrator account) is refused with 403. The
ticket is opaque, single-use, expires within 30 seconds and binds the account, login session,
role, profile proof and resolved access scope. The response carries `ticket`, `expires_in`,
`max_connection_seconds` (300) and `protocol`, is not cached, and minting is naturally
idempotent in effect: extra credentials are unused orphans that expire.

Connect to `/api/v2/admin/logs/ws` offering exactly `silo.admin-logs.v2` then
`silo.ticket.<ticket>`; the server echoes only the first. The query carries the stream
selection (`stream=app|audit`, required) and the same filters as `GET /admin/logs/app` and
`GET /admin/logs/audit` (`limit`, `cursor`, `level`, `component`, `node_id`, `q`, `method`,
`path_prefix`, `status_code`, `client_ip`, `request_id`, `user_id`, `session_id`,
`playback_session_id`, `from`, `to`); a `token` or `ticket` query, a request body, a foreign
Origin, a missing protocol, an invalid stream or filter, or a malformed upgrade is refused
before the credential is consumed. After consumption the login session, account, profile
proof and administrator role are validated again before 101.

The connection ends at the earlier of five minutes or access-token expiry, and the handler
rechecks session, account, profile and administrator role every 15 seconds with a two-second
bound; a failed check or a demotion closes the socket. Reconnect with a newly minted credential;
this is bounded revocation detection, not instantaneous. Without Redis the ticket is
process-local and requires affinity to the minting node.

The frames after the upgrade are the bridge's: one `snapshot` (`entries`, `next_cursor`)
from the matching list route, then buffered `append` frames newer than the snapshot, then live
`append` frames filtered by the same options and deduplicated by id, or an `error` frame
followed by close 1011 when a repository read fails. The hub buffers 64 messages per
connection and drops on overflow; the stream is a live tail, not a gap-free feed. Ping runs
every 20 seconds with a 30-second pong deadline.

The bridge route is unchanged: bearer token via middleware (the legacy web client carried it
in the URL), same statuses (503 without a hub, 400 `bad_request` "Invalid stream" or the filter
error), shared origin check, no lifetime bound. The web logs page now mints a ticket under
captured administrator authority for each connection, offers the protocol pair, never puts a
credential in the URL, and discards frames after the authority changes. Native clients have
no log stream caller.

### Retained audit-log read in v2

`GET /api/v2/admin/logs/audit` returns an acting-administrator `items` collection,
with a default limit of 50 and maximum of 200. It retains inclusive `from`/`to`,
uppercase-normalized `method`, integer `status_code`, `path_prefix` (existing SQL
LIKE pattern semantics), `client_ip`, `request_id`, `user_id`, `session_id` and
`playback_session_id` filters. Client IP accepts IPv4/IPv6 addresses or prefixes;
prefix host bits remain intact because the source compares PostgreSQL inet values
for equality, rather than testing subnet containment.

The descending timestamp/ID source cursor is wrapped verbatim in a signed cursor
bound to acting account/profile/access, normalized filters and page size. Source
time precision is retained; response timestamps use UTC milliseconds and IDs use
decimal strings. `user_id` is the subject account; `impersonator_user_id` separately
identifies the impersonating account when recorded. Existing audit metadata and
collector redaction behavior are unchanged. Pagination is a live retained view,
without snapshot, late-commit or retention completeness guarantees.

The web logs page uses the scoped v2 HTTP readers for application and audit
history. Choosing **Browse log history** pauses the live stream and loads a fresh
first page, then **Older** and **Newer** follow the server-issued cursor chain.
It does not reuse the live snapshot's cursor, because appended messages may have
trimmed rows from the displayed live tail. Returning to live logs reconnects the
stream. Changing filters or administrator authority discards the history cursor
chain; failed history requests show a retry action. The HTTP helpers reject IDs
outside the numeric UI model's safe range. This browsing mode retains the API's
live traversal and retention limits described above.

### Autoscan setup descriptor discovery in v2

`GET /api/v2/admin/autoscan/scan-source-plugins` lists installed and built-in
scan-source identities with resolved setup descriptors. The acting-administrator
read reuses discovery and its defaults without invoking a provider or reading
stored source configuration. Descriptor fields, setup form controls, options,
conditions, validation and manifest defaults retain their existing meanings;
`default_value` is a plugin-defined JSON extension value, not a stored secret.

Each page enumerates the full current discovery list, sorts by `(plugin_id,
capability_id)`, and returns at most `limit` entries (default 50, maximum 200).
The signed cursor binds that identity position to actor/profile/access and limit.
Duplicate identities fail rather than silently skip entries. This is a live list,
not a snapshot; installation changes can affect later pages.

The existing Add-source/edit and activity descriptor consumers drain pages through
`useAvailableScanSources`, with a 100-page bound, loop/missing-cursor rejection,
and authority checks before requests and after decoding. Cache keys separate
observed PIN-proof changes without including credentials. Unknown form controls
use the generic text fallback, and unknown connection requirements remain
optional. Source writes, callback delivery and provider operations are separate.

### Autoscan rewrite preview in v2

`GET /api/v2/admin/autoscan/sources/{id}/rewrite-suggestions` performs the existing
synchronous comparison between the bound provider's root folders and local library
paths. The administrator-only response retains `proposed` (from/to/match depth),
`unmatched`, `ambiguous` (root/candidates), and `covered` groups. These are suggestions,
not an applied source update or a persisted job. Missing source/connection is404;
a source without a bound connection is422. Missing service is503; unexpected
provider/store errors are generic500 without upstream messages or credentials.

The existing editor captures source and administrator/profile/PIN authority before
queuing its explicit read. It disables automatic mutation retries and authentication
replay, rejects stale decoded results and replacement-editor completion, and does
not save merely by displaying a preview. Applying selected suggestions remains a
separate source write; a preview from replaced authority cannot be applied.

### Canonical dashboard-layout read in v2

`GET /api/v2/admin/dashboard/layout` reads the acting administrator account's saved
layout, shared across that account's profiles. No row returns `layout: null` and
`updated_at: null`, preserving the local/default arrangement behavior. Stored
layouts remain client-owned JSON objects; consumers must sanitize widget names and
spans. The extension object preserves future fields and JSON number precision.
Timestamps use UTC milliseconds.

The strong conditional-read ETag binds the account, acting profile/access scope,
canonical layout object and full stored timestamp. It supports If-Match and
If-None-Match/304, but is not an acknowledged write revision or a promise of
write-side compare-and-set. Save/reset transport and the complete `server_layouts`
capability remain separate work; a read alone does not advertise that lifecycle.

The web reader keys its cache by captured service/account authentication context,
profile and setter-owned non-secret PIN generation, and rejects responses decoded
after authority changes. Legacy save/reset success invalidates layout query
variants so active readers fetch the canonical document; it does not seed a local
timestamp as a server acknowledgement. Local immediate edits, debounced saves
and serialized legacy writes remain unchanged. This read migration does not add
write-side conflict handling or change the legacy writer's authority semantics.

### Hardware-acceleration inventory in v2

`GET /api/v2/admin/system/hw-accel` requires an acting administrator and reuses
the synchronous inventory walk described for the bridge endpoint. Enabled healthy
nodes are queried concurrently within their advertised/configured budgets. The
first successful node supplies the primary report; no nodes, or all failed nodes,
falls back to the local host with current playback settings. Failed nodes remain
visible with a generic error. Node URL userinfo, query and fragment are removed.
This is a live inventory, not a cluster capability union, snapshot or durable job.
`resolved` may name an explicitly configured backend whose probe did not verify;
`detected_backends` carries verification/skipped results. Missing service returns
503, distinct from an inventory reporting unavailable hardware. Arrays of render
devices/details are non-null. Existing worker transport and probe cache behavior
are unchanged; local detection retains its own budget rather than promising
request cancellation stops every subprocess.

The actual web settings/overview reader captures service/account/profile/PIN
authority, rejects late decoded responses and partitions cached success by the
setter-owned PIN generation. Automatic query retries and authentication replay
are disabled. This contract does not advertise a new hardware support flag.

### Stored plugin repositories in v2

`GET /api/v2/admin/plugins/repositories` reads stored configuration under the
acting-admin gate. It does not fetch repository
indexes or require a running plugin. A missing database-backed store returns503;
private store errors are masked. String IDs, managed status, source kind, configured
URL and UTC-millisecond timestamps preserve the existing repository meanings.

The collection uses ascending numeric ID and signed continuation bound to account,
profile/access scope and page limit. Each page enumerates the full stored list;
this is live pagination, not a snapshot or bounded database query. The web reader
drains at most100pages and fails visibly on invalid/duplicate identity, unsupported
source kind or bad continuation rather than publishing a partial list. It captures
authority and keys cached success by non-secret PIN generation. Existing legacy
repository writes invalidate the shared key prefix; their transport is unchanged.
Plugin installation/runtime compatibility remains a separate gate.

### Stream telemetry parity in v2

`GET /api/v2/admin/stream-telemetry/parity` compares the cached global telemetry
view with bounded PostgreSQL and Redis legacy session reads. It requires an acting
administrator. A missing comparison service returns503; disabled telemetry returns
200 with enabled:false and a reason. An unbuilt view and unavailable legacy source
remain distinct from an empty successful comparison. No publisher, session storage,
worker protocol or cutover behavior changes.

The view retains availability, staleness, completeness, missing publishers, clock
skew and refresh counters; built_at uses UTC milliseconds. Source/cache errors are
masked. Existing differences retain their explicit truncation counts and absent
field counts. Each source exposes the existing60000record legacy_scan_limit and
legacy_may_be_truncated: PostgreSQL reaching the cap is conservatively uncertain;
Redis uses its actual scan truncation signal. Undecodable Redis records remain
noted. Reads are not a cross-store snapshot and may race session updates. A report's
agrees flag ignores fields absent on one side and does not establish completeness
or authorize retirement/cutover. No first-party web, Swift or Kotlin caller exists
in the inspected source inventory.

### Creating a plugin repository in v2

`POST /api/v2/admin/plugins/repositories` creates one stored configuration and
returns201 with the same typed repository projection as the list. URL and display
name are required; omitted enabled defaults to true. Built-in repository URLs
remain managed through catalog settings and are rejected here with422. The route
requires an acting administrator, restricts demo access and reports missing store503.
It performs the existing single INSERT, not a remote catalog fetch or installation.

Creation has no replay identity or deduplication guarantee and is nonretryable. A
store/transport failure may leave completion uncertain; it is not proof that no row
exists. The Add Repository hook copies the submitted body and captures authority
before queueing, disables mutation retries and authentication replay, and rejects
stale completion. Successful active-authority creation retains existing plugin
query invalidations. Failure requires explicit reconciliation before another
submission; no automatic retry, replacement or legacy fallback occurs.

### Autoscan connection checks in v2

`POST /api/v2/admin/autoscan/connections/test` performs one advisory status check
without saving a connection. A nonblank connection_id takes precedence over draft
fields. Otherwise supply base_url or request_integration_id; api_key_ref is input
only and is never returned. The existing resolver and status checker are reused.
An unreachable/unauthorized/unresolvable connection remains200 with ok:false and a
generic error; success returns ok:true and the reported version. Missing stored
connection404, absent adapter503 and other service failures500 remain distinct.
An installed adapter with an internally missing probe is a service failure, not
a claim of hardware/provider support. Private upstream error text is never echoed.

The acting-admin/demo-gated operation is nonretryable: no automatic mutation retry
or authentication replay, fallback, connection write or durable job. Both web
connection dialogs capture draft/authority at the gesture and reject results from
replaced authority, older drafts or a closed/reopened dialog. The check remains
advisory and never blocks the separate save operation.

### Creating an autoscan connection in v2

`POST /api/v2/admin/autoscan/connections` creates one stored connection and returns
201 with the credential-free connection projection. Name is required, with either
a base URL or a Requests integration link. Text is trimmed and blank links become
null. Credentials remain input-only; has_api_key reports presence. The existing
store generates the connection ID, encrypts with its existing row-ID binding and
performs one INSERT. No probe, provider update or source creation occurs.

The acting-admin/demo-gated operation is nonretryable and has no replay identity
or durable job. Failed completion may follow a committed insert; explicitly refresh
and reconcile before submitting again. Both dialogs capture copied body/authority
before queueing, disable retries/auth replay and reject stale completion. A late
result cannot close a newer connection draft or select the created ID in a changed
inline source draft. Update/delete and source writes remain separate migrations.

### Update an autoscan connection (v2)

`PUT /api/v2/admin/autoscan/connections/{id}` (`updateAdminAutoscanConnection`)
requires an acting administrator and is unavailable in demo mode. The required
name and kind plus optional base_url, write-only api_key_ref and
request_integration_id use the creation input shape. A name and either a base URL
or Requests integration are required. Blank API key input preserves the encrypted
stored key; blank integration input clears that link. The existing single stored
update returns a credential-free connection with has_api_key (200), or missing
connection (404), invalid input (422), unavailable service (503), or masked error
(500). There is no revision precondition, replay identity or durable job. An error
can follow a committed update: refresh and reconcile explicitly before another
submission. No automatic retry, authentication replay or provider update occurs.
The web edit submission captures identity and input before queueing and refuses
late completion from another authority or a newer dialog draft.

### Delete an autoscan connection (v2)

`DELETE /api/v2/admin/autoscan/connections/{id}` (`deleteAdminAutoscanConnection`)
requires an acting administrator and is blocked in demo mode. The existing single
stored DELETE returns empty 204 success; missing targets return 404. References
from scan sources prevent deletion: reconfigure those sources first. The current
store does not expose a typed conflict, so storage failures return masked 500;
missing service returns 503. No sources are detached, provider requests sent, or
jobs created. Errors can follow commit; refresh and reconcile before another
explicit submission. There is no automatic retry, authentication replay or durable
receipt. The actual web confirmation captures target and authority before queueing
and fences completion/invalidation to that authority.

### Update a stored plugin repository (v2)

`PUT /api/v2/admin/plugins/repositories/{id}` (`updateAdminPluginRepository`)
requires an acting administrator and is blocked in demo mode. IDs are positive
decimal strings. The partial body accepts url, display_name and enabled; blank
name/URL values are ignored, nonblank values are preserved, and omitted enabled
stays unchanged. Managed repository writes return409; missing targets return404.
The existing store update is followed by a read returning current configuration
(200), not an atomic write revision. Readback disappearance or failure returns
uncertain500, as can update failure; missing service returns503. Refresh and
reconcile before another explicit submission. The enabled switch captures target,
body and authority before queueing, disables retries/authentication replay, and
fences invalidation. No catalog fetch, installation or plugin runtime operation is
performed by this endpoint.

### Delete a stored plugin repository (v2)

`DELETE /api/v2/admin/plugins/repositories/{id}` (`deleteAdminPluginRepository`)
requires an acting administrator and is blocked in demo mode. Positive decimal
string IDs identify stored records. Managed repositories return409; missing
records return404. The unchanged store delete returns empty204 success, masked500
on failure and503 when unavailable. It does not uninstall plugins or invoke the
runtime or a remote catalog. The existing database foreign key clears the
repository link on retained plugin records. There is no durable receipt or automatic replay;
an error can follow commit, so refresh and reconcile before another submission.
The actual Remove hook captures target and authority before queueing, disables
mutation retries and authentication replay, and fences completion/invalidation.

### Autoscan scan history (v2)

`GET /api/v2/admin/autoscan/scans` (`listAdminAutoscanScans`) requires an acting
administrator. It accepts limit (1–200, default50), cursor, status and q. Items use
string library/event IDs and UTC-millisecond timestamps; the separate total is a
live count. Continuation is signed to actor/profile, filters and limit and wraps
the existing SQL offset. Ordering uses the latest completed/started/requested
timestamp descending, then scan ID descending. Lifecycle changes or concurrent
inserts can move rows between pages; neither count nor continuation provides a
snapshot. A full final page may require one additional empty read. Missing service
returns503; source errors are masked. No scan/worker execution changes.

The Activity panel retains polling and numbered pages through at most100 cursor
reads per request. It rejects unsupported/unsafe row values and invalid continuation
without partial success. Cache identity includes captured profile/PIN generation;
previous-page placeholders are not reused, and stale-authority results are rejected.

### Autoscan poll-event history (v2)

`GET /api/v2/admin/autoscan/events` (`listAdminAutoscanEvents`) requires an acting
administrator. Filters are source_id, status (running/success/error/unresolved), q,
limit (1–200/default50) and cursor. Event IDs and nested library IDs are decimal
strings; timestamps are UTC milliseconds. Items include all scan runs associated
with the selected events. Only the event SQL page is bounded; nested runs are fully
enumerated. Ordering remains completed_at DESC/id DESC with a signed offset bound
to actor/profile/access, filters and limit. A separate live total and mutable page
positions do not provide snapshot consistency; full final pages may require an
additional empty read. Running events retain the existing start-time placeholder
in completed_at and their running status. Missing service returns503, private
source failures500. No execution or worker behavior changes.

The Activity panel keeps polling and numbered pages through at most100 cursor
reads per requested page. Captured authority/PIN cache identity, stale-response
checks and no previous-page placeholders isolate authority transitions. Unsupported
states, unsafe numeric IDs and invalid continuation fail without partial success.

### Write autoscan settings (v2)

`PUT /api/v2/admin/autoscan/settings` (`updateAdminAutoscanSettings`) requires an
acting administrator. The required enabled, default_poll_interval_seconds (1 to
2147483647) and debounce_seconds (0 to2147483647) fields replace desired settings.
The existing UPSERT completes before the optional poll-task reschedule call. A200
response contains settings and reschedule_state: applied, failed or not_configured.
Applied describes that call only, not durable cluster-wide application or ordering
against concurrent writers. Failed/not_configured still means settings persisted;
no retry is performed. Missing writer503, invalid422 and masked uncertain500 remain
separate. There is no revision precondition or replay identity. Reload and reconcile
uncertain persistence before another explicit submission.

The web enable switch and advanced form capture body and authority, disable retry
and authentication replay, and invalidate the canonical reader only for the active
authority. Reschedule warnings distinguish stored settings from runtime outcome.
The advanced form uses explicit Save and preserves newer edits after acknowledgement;
missing configuration is not replaced by default values for submission.

### Reset the administrator dashboard layout (v2)

`DELETE /api/v2/admin/dashboard/layout` (`resetAdminDashboardLayout`) requires an
acting administrator and resets the authenticated account's stored layout. The
single DELETE succeeds with empty204 even when already absent. Missing storage
returns503; private storage errors are masked. It provides no write revision or
durable receipt. The operation is `non_retryable`: after a lost successful reset
response, retrying can delete an intervening save. Clients must not retry until
the server guards the layout generation or provides equivalent ordering protection.

The actual Reset layout action checks its rendered authority before changing local
state, drops its unsent debounced save, and queues the captured reset behind any
in-flight save in the existing mutation scope. The reset disables retries and
authentication replay and fences late invalidation. Reset now shares the account
transaction lock and advances the persistent generation, but remains an unguarded
explicit deletion: replay after a replacement save is still unsafe. server_layouts
stays false until the full lifecycle is accepted.

### Save the administrator dashboard layout (v2)

`PUT /api/v2/admin/dashboard/layout` (`saveAdminDashboardLayout`) requires an acting
administrator and a layout JSON object, within a16KiB request body. The document
belongs to the client; unknown fields and numeric JSON spelling are preserved.
The operation requires the validator from the original GET in If-Match. Missing
header428, stale or weak match412, and malformed lists400 follow the shared parser;
If-Match is evaluated before If-None-Match. A successful empty204 includes the ETag
of that exact committed write. Invalid objects422, excessive body413, missing storage503
and masked uncertain500 remain. No automatic replay or durable job receipt is supplied.

A dashboard-specific revision table retains generations after reset. All current
bridge/v2 save/reset handlers lock the account before checking or changing layout and
revision in one transaction. The migration preserves existing layouts; bridge HTTP
bodies/statuses remain unchanged. The lock covers an absent layout too, and reset
cannot make an old absent-layout validator current again. Old binaries that do not
participate in this writer protocol are not covered by this ordering guarantee.

The dashboard captures the original version, copied layout and authority before
edit/debounce/cleanup. One save is in flight locally; only its acknowledged ETag can
advance retained pending edits. Background GET data never rebases a draft. On conflict,
uncertain response or reset, further server saves stop while local edits remain visible.
An explicit Reload server layout discards local edits and resumes from the returned
canonical version; edits made during that reload prevent its late adoption. Authority
changes cannot recapture an old draft for submission. Retries/auth replay stay disabled.
The server_layouts capability remains false pending separate lifecycle activation review.

### Delete an autoscan source (v2)

`DELETE /api/v2/admin/autoscan/sources/{id}` (`deleteAdminAutoscanSource`)
requires an acting administrator. It deletes the stored source with empty204;
unknown IDs return404, absent storage503, and private failures an uncertain500.
No installed plugin resolution is required, allowing removal of orphaned sources.
Existing foreign keys cascade webhook endpoint and queued delivery rows; event
history remains with a null source reference. Already-running work is not canceled.
No provider update, scheduler acknowledgement or durable completion receipt is supplied.

The operation is `non_retryable`. Reconcile uncertain completion before another
explicit submission. The actual Sources confirmation captures source and authority
when opened, keeps them through queueing, disables retries/authentication replay,
and fences dispatch, completion and invalidation. Source query cache includes the
setter-owned non-secret PIN generation; a changed PIN cannot reuse cached success.
Other source and webhook lifecycle operations remain separate migrations.

### Create and update autoscan sources (v2)

`POST /api/v2/admin/autoscan/sources` (`createAdminAutoscanSource`) returns201
for a stored source bound to a currently installed plugin/capability pair.
`PUT /api/v2/admin/autoscan/sources/{id}` (`updateAdminAutoscanSource`) returns200
for full-state configuration replacement; stored plugin/capability identity is immutable.
Both require an acting administrator, an enabled boolean and path_rewrites array,
and cap request bodies at64KiB. Nullable/omitted connection unbinds; nullable/omitted
poll interval inherits the default, otherwise it is1–2147483647 seconds. Empty update
delivery mode preserves the stored mode. Rewrites need nonblank from/to values.
Configuration keys/values, connection and label are normalized as in the bridge.
Webhook mode is restricted to the built-in identity, with auto/sonarr/radarr provider
validation. Creation does not create a webhook endpoint. Update returns existing
webhook state with v2 callback URL projection; reveal failures can omit the URL.

Missing source or connection returns404; invalid configuration422; missing dependency503;
private failures500 with uncertain completion. Both operations are non_retryable.
Update is last-write-wins with no revision/ordering receipt; optional webhook readback
can observe current state. Neither promises execution, scheduling, provider changes
or durable job completion. The existing webhook setup operation remains separate.

Actual Add/row edit/toggle callers capture copied body and draft authority before
queueing, disable retry/authentication replay and fence late receipt/callback/cache effects.
Row drafts retained across PIN replacement cannot submit under the new authority.
Creation does not close or advance a newer dialog draft after an older acknowledgement.

### Autoscan source webhook lifecycle (v2)

Acting administrators can create, remove or rotate a source endpoint:

| Method and path | Operation | Success |
| --- | --- | --- |
| POST `/api/v2/admin/autoscan/sources/{id}/webhook` | `createAdminAutoscanSourceWebhook` | 200 current source view |
| DELETE `/api/v2/admin/autoscan/sources/{id}/webhook` | `deleteAdminAutoscanSourceWebhook` | 204 empty |
| POST `/api/v2/admin/autoscan/sources/{id}/webhook/rotate` | `rotateAdminAutoscanSourceWebhook` | 200 current source view |

All three are demo-restricted, require no request body and are `non_retryable`.
Create requires webhook delivery mode and preserves an existing endpoint/token.
Rotate requires an existing endpoint and replaces its token, invalidating the old URL;
it retains the bridge behavior for an endpoint whose source mode subsequently changed.
Delete removes the endpoint, without canceling queued or running source work.
Missing sources/endpoints return404, wrong create delivery mode422, absent storage503,
and private failures an uncertain500. No provider configuration or external send occurs.

POST responses use best-effort current endpoint readback and the accepted v2 callback
URL projection. They are not exclusive write receipts: a competing change or reveal
failure can alter or omit the URL. There is no revision guard, durable job receipt or
execution promise. Delayed create can recreate a deleted endpoint; repeated rotation
changes the token again; delayed deletion can remove a replacement endpoint. After an
uncertain outcome, inspect refreshed source state before a new explicit submission.

Existing web generate/rotation confirmation and Add-source setup use captured source
and profile authority, disable automatic/authentication retries, and reject late results
under changed authority. Rotation hides the previous secret while pending or uncertain;
a successful current readback replaces the cached URL only for the matching source.
Local observation ordering retires that readback when a source-list read begun after
it supplies newer state. Older in-flight reads cannot restore an earlier URL; these
local markers are not server revisions or exclusive write receipts.
Reload the page after an uncertain endpoint mutation before using or replacing its URL.
The exported DELETE hook is migrated but has no mounted web caller. These admin operations
have no Jellyfin protocol equivalent; shared ingress and token storage are unchanged.

### Run an autoscan poll (v2)

`POST /api/v2/admin/autoscan/trigger` (`triggerAdminAutoscan`) takes no body and
requires an acting administrator; demo mode refuses it. It uses the existing task
manager to reserve and start `autoscan_poll` on the receiving process and returns200
with the canonical `AdminTask` snapshot (`execution_scope: process`). This is not a
202 job acceptance, durable dispatch, restart-safe execution or completed scan receipt.
It does not create another worker implementation or change the frozen bridge trigger.

An absent task manager returns503, an unregistered poll task404, an already reserved
worker409, and private start failures500. The operation is `non_retryable`: a repeat
after the process task finishes can invoke providers again. After a lost response,
inspect task/activity state before an explicit new command. No job Location is supplied.

The existing poll honors autoscan enabled state and per-source interval floors, skips
webhook sources, and records per-source provider/enqueue failures in activity without
necessarily failing the overall task. A successful start does not promise provider
success, new scan runs or completed downstream work. The web Run-now button captures
profile authority before queueing, disables retries/auth replay, stays pending until
acknowledgement and fences late feedback/invalidation under a changed authority.
There is no corresponding Jellyfin administration operation.

### Node health and reprobe commands (v2)

`POST /api/v2/admin/nodes/{id}/check` (`checkAdminNode`) and
`POST /api/v2/admin/nodes/{id}/reprobe` (`reprobeAdminNode`) require an acting
administrator, reject demo mode, take no body and accept positive database node IDs.
Both return synchronous200 observations; neither accepts a durable job or promises
cluster-wide completion. Missing nodes return404, missing configuration503, invalid
IDs422, and repository failures a private500 problem.

Check uses the existing bounded health request and URL-fenced persistence/pool updates.
An unreachable or unhealthy node is an observation, not an HTTP error. The response
includes healthy, active_jobs, egress_kbps and optional capabilities_hash.
health_persisted means the persistence call returned successfully; that call may ignore
a node repointed during the request. A false value does not erase the live observation.
It is not a revision receipt. This observational command remains natural_idempotent.

Reprobe retains existing node refusal, probe timeout, policy-derived write deadline and
capability refresh behavior. Its200 body carries status ok/error and a private summary
for worker refusal or uncertain completion. node_id is an opaque decimal string;
capabilities_refreshed distinguishes a refreshed inventory from a successful node probe.
A failed refresh does not turn an already successful probe into failure. No hardware
support or plugin launch is inferred. The operation remains non_retryable; inspect
node state before repeating an uncertain command. Frozen bridge response shapes and
worker wire contracts remain unchanged through shared service extraction.

Actual node-row actions copy the selected node and capture authority before queueing,
disable automatic/authentication replay, and fence late receipts, feedback and cache
invalidation. Node list caches include profile/PIN authority so old rows cannot be reused
under a new authority. There is no corresponding Jellyfin administrative operation.

### Force reload nodes (v2)

`POST /api/v2/admin/nodes/force-reload` (`forceReloadAdminNodes`) takes a snapshot
of the stored enabled-node list and requests each node in parallel. Results retain
that list order. An empty enabled list returns200 with an empty results array.
`POST /api/v2/admin/nodes/{id}/force-reload` (`forceReloadAdminNode`) addresses one
stored node, including a disabled node. Both require an acting administrator, reject
demo mode, take no body and are non_retryable. Invalid single IDs return422, missing
nodes404, absent configuration503, and repository failures a private500 problem.

Both return synchronous200 with per-node node_id (opaque decimal string), node_name,
and status ok/error. An ok result means the worker answered200 or204. A timeout,
redirect, transport failure or other status yields a private error summary; it may
follow partial work. No automatic retry or redirect dispatch occurs. Each worker
request has a ten-second timeout and follows request cancellation; bulk results are
neither atomic nor a durable job. No202/job Location or rollback is promised.

Force reload can tear down active worker sessions. Lost responses and error results
must be reconciled before another explicit command; do not interpret them as proof
that no reload or teardown happened. The API does not launch plugins, verify hardware
support or manufacture worker completion beyond the acknowledgement. Shared URL/auth
request construction preserves the frozen bridge's existing behavior; worker reload
and teardown implementation remains unchanged. There are no actual web callers or
helpers for either route, and no Jellyfin administration equivalent.

### Node configuration lifecycle (v2)

`POST /api/v2/admin/nodes` (`createAdminNode`) stores a node and returns201 with
its configuration and ETag. URL is the natural unique key. An explicit repeated
create resolves the existing node only when its normalized configuration still
matches the submitted creation fields; conflicting configuration returns409.
It does not overwrite a competing administrator's node or contact the worker.

`GET /api/v2/admin/nodes` adds `config_etag` to each stored node. This validator
covers configuration, including bridge edits, and excludes health/capability
samples. Paging remains a live full-discovery projection, not a cross-page
snapshot. Capture the node's validator when opening an edit or delete action.

`PUT /api/v2/admin/nodes/{id}` (`updateAdminNode`) and
`DELETE /api/v2/admin/nodes/{id}` (`deleteAdminNode`) evaluate `If-Match` while
holding the stored row lock. An absent precondition returns428; a stale or weak
validator returns412 with the current ETag. The shared header grammar and
If-Match-before-If-None-Match ordering apply. PUT returns200 and its own committed
configuration validator. Only an actual configuration change advances the validator
and pool generation. A no-change PUT with the current `If-Match` returns200 with
the same ETag. After a lost response, a retry using the original `If-Match`
returns412 if the first request changed configuration (or another write did).
Re-read `config_etag` and compare stored configuration before resubmitting;
neither200 with an unchanged ETag nor412 identifies which request wrote the state.
Public URL and acceleration/device override nulls clear those fields; omitted fields retain their values. When a PUT changes the URL or
an acceleration/device override, the server asks that worker to re-read its
configuration and drops its cached capabilities after the commit, off the
request; the response does not wait on or report the worker's answer. DELETE returns204 after the
row deletion and durable pool invalidation commit together. A later404 does not
prove which caller deleted the node.

Every API replica reconciles persisted node configuration on startup and on a
five-second cadence, retrying failed reads and recovering missed notifications.
Configuration changes and deletions advance a durable generation in the same
transaction, including writes through the frozen bridge. Reconciliation reapplies
the current snapshot even when the generation is unchanged so a late legacy
notification cannot leave the pool stale indefinitely. A response acknowledges
stored configuration, not reconciliation by every replica, worker policy reload,
or session teardown. Workers retain their existing configuration watcher. The
post-commit URL/override reload described above creates no durable execution job.

Setting only `enabled` to false removes the node from new placement and routine
health sweeps on each replica's next pool reconciliation. It does not contact
the worker, drain or reassign existing sessions, or revoke their stream authority;
existing streams keep serving. Previously stored `healthy`, `active_jobs`,
`egress_kbps`, `last_stats` and `last_health_check` remain the last observations,
not current liveness or load. A health check already in flight may still finish;
an explicit administrator check can also refresh the sample. Re-enabling restores
eligibility and routine sampling after reconciliation. Force reload is a separate,
disruptive command; its200 response is an acknowledgement, not a durable receipt
proving session teardown.

The administrator form, enable toggle, delete confirmation and setup node form
use the v2 routes with captured authority and no automatic/authentication replay.
Edits and confirmations retain their original validator across background list
updates. Inputs are disabled while a form save is pending. After a conflict or
uncertain result, explicitly reload nodes and reopen the action; retained drafts
are not silently rebased onto another writer's configuration.

### Plugin catalog and installations in v2

`GET /api/v2/admin/plugins/catalog` returns cursor-paged, typed catalog entries sorted by
`(plugin_id, version)`; repository identifiers are opaque strings. Each page performs the same
live fetch of every enabled repository index as the legacy read, recording
`last_fetched_at` on the repositories it reaches, so continuation enumerates that page's
fetch and is not a snapshot. Plugin-defined JSON (manifest metadata, capability metadata,
admin-form default values) travels in named extension bags; every other object is closed.

`GET /api/v2/admin/plugins/installations` returns cursor-paged, typed installations sorted
by `id`. Both listeners share one projection: global configuration values contain only the
public fields the manifest declares and list configured secret names in
`configured_secrets`; a configuration whose schema is missing from the manifest is projected
empty. The reserved built-in host row is excluded and a projection that contains it is an
internal error. Timestamps are RFC 3339 UTC milliseconds.

Both reads require an acting administrator and answer 503 when the
plugin service or stores are not wired. The web plugins page and admin sidebar read these
routes under captured profile authority and drain pages with a bounded loop; a stale
authority or a duplicated identifier fails the read rather than merging pages.

### Plugin installation lifecycle and archive uploads in v2

`POST /api/v2/admin/plugins/installations` installs a plugin from either a catalog target
(`repository_id`, `plugin_id`, `version`; identifiers are opaque strings) or a direct
`archive_url`; mixing the two or supplying neither is a 422 naming the field. Both paths
fetch over the network. An installation with the same manifest `plugin_id` is stopped and
replaced rather than duplicated, and metadata-provider capabilities are appended to the
library chains as on v1. Success is 201 with the installation. There is no replay identity
and no database uniqueness, so the row is non-retryable: a lost response may follow a
committed install and the client reconciles from the installation list.

`PUT /api/v2/admin/plugins/installations/{id}` assigns `enabled` and/or `update_policy`
(`auto`, `notify`, `off`, `manual`); an empty body is 422. Disabling stops the running plugin
before the write; any enabled change rebuilds the event subscriber index. Repeating the
same assignment converges, so the row is naturally idempotent. Success is 200 with the
installation.

`POST /api/v2/admin/plugins/installations/{id}/update` installs the recorded available
version from the installation's repository (a network fetch) and clears the marker; an
installation without a recorded update or without a repository is 409 (v1 answered 500).
Success is 200. Non-retryable: no replay identity, and a lost response may follow a
committed update.

`DELETE /api/v2/admin/plugins/installations/{id}` stops the plugin, deletes the row
(configuration, bindings and archives cascade) and removes its files; on a failed row delete
an enabled plugin is restarted. Row delete and file removal are not one transaction and a
repeat finds no row, so a later 404 is not this caller's receipt; non-retryable. Success is
204.

`POST /api/v2/admin/plugins/uploads` installs one uploaded archive from the multipart
`archive` part, capped at 256 MiB; JSON on this operation is 415, a missing part 422 at
`body.archive`, an oversize body 413. A zip archive is installed from its manifest; any
other file is treated as a plugin binary whose manifest the server obtains by executing
it, exactly as v1 does. Success is 201 with the installation. Non-retryable: the same
plugin_id is replaced, so a delayed retry can replace a newer installation. The frozen
bridge route keeps its own answers (an oversize or malformed form is 400 `bad_request`).

Chunked uploads mirror the diagnostics transport. `POST /api/v2/admin/plugins/uploads/chunked`
opens a process-local session for one file (`filename`, `size_bytes` up to 256 MiB, optional
`chunk_size` up to 1 MiB, default 512 KiB) and answers 201 with the session, including its
inactivity `expires_at`; every call creates a new session, so it is non-retryable.
`PUT .../chunked/{upload_id}/chunks/{chunk_index}` stores one `application/octet-stream`
chunk whose length equals the session chunk size for that index; chunks may arrive in any
order, a chunk already received is accepted without rewriting (naturally idempotent), a
concurrent write to the same index is 409. `POST .../chunked/{upload_id}/complete` consumes a
fully received session before the install runs and answers 201 as the direct upload does;
an incomplete session is 409, and because the session is removed first a repeat finds 404
and a lost result has no receipt (non-retryable). `DELETE .../chunked/{upload_id}` discards
a session and answers 204 even when it is already gone (naturally idempotent). Sessions
live on the replica that opened them and are unknown to other replicas or after a restart;
an unknown or expired session answers 404 on every route.

All nine require an acting administrator, restrict demo access, answer 404 for an unknown
installation, 409 for the reserved built-in host row, and 503 when the plugin service or
stores are not wired. No revision precondition exists on installations; `If-Match` is a
follow-up, not part of this port. The web plugins page installs, updates, applies and
removes under captured profile authority with no automatic or authentication replay; the
upload sends a small file as one multipart request and a larger one through the chunked
operations, halving the chunk size after a 413 and cancelling the abandoned session.

### Plugin installation configuration and bindings in v2

`PUT /api/v2/admin/plugins/installations/{id}/config` replaces one global configuration
entry. The body names the manifest `global_config_schema` key and the entry's fields;
manifest-declared secret fields left blank keep their stored value, and a secret is removed
only when named in `clear_secrets`. The server validates the merged entry against the
plugin's schema and returns 422 with the plugin's own message on failure, then persists
under a compare-and-swap on the stored revision and stops the running plugin so it rebinds.
Success is 204. Repeating the same request converges on one stored entry, so the row is
classified naturally idempotent. The merge preserves stored secrets only; it does not
protect concurrent edits to public fields by two administrators, and the last write wins.
The entry is administrator-only and the web client never replays a submission
automatically. A revision precondition (`If-Match`) is a follow-up, not part of this port.

`POST /api/v2/admin/plugins/installations/{id}/config/test` probes one prospective entry:
the server starts a temporary plugin instance with the merged configuration, runs the
plugin's connection check under a 20 s timeout, and stops the instance. Nothing is stored.
A completed failed check, including a plugin without connection-check support, is a 200
result with `success` false and the message; only transport or store failures are problems.
The probe launches a process and may call a billed provider, so it is non-retryable: the
web hook disables mutation retries and authentication replay and reports an uncertain
result to the operator instead of resubmitting.

`PUT /api/v2/admin/plugins/installations/{id}/auth-binding` and
`PUT /api/v2/admin/plugins/installations/{id}/task-bindings/{capability_id}` assign one
whole binding row keyed by installation and capability and mark a server restart required.
The auth binding answers 204 with `X-Silo-Restart-Required: true`; the task binding answers
200 with `restart_required` true, matching the legacy shapes. An omitted task trigger
stores an empty object. Both are naturally idempotent upserts.

All four require an acting administrator, restrict demo access, answer 404 for an unknown
installation, 409 for the reserved built-in host row, 422 for a blank key or capability, and
503 when the plugin service or stores are not wired. The web plugin dialog captures profile
authority before each submission and ignores a completion that arrives under a replaced
authority.
