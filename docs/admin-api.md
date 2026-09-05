# Admin API

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
| `enabled` | bool | Whether the node is eligible for selection at all. |
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
are exposed as `streamapp_node_*` gauges on this process's existing `/metrics`
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
