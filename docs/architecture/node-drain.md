# Transcode node drain

A draining transcode node takes no new work and keeps serving the sessions it
already runs, so stopping, upgrading or scaling down a node does not cut its
streams. Proxy nodes do not drain: a proxy holds no session it could finish on
its own, so nothing could tell when stopping it is safe.

## Entering and leaving a drain

A node drains for either of two reasons:

- **An administrator's request.** `POST /api/v2/admin/nodes/{id}/drain` sets
  `stream_nodes.drain_requested`; `POST .../resume` clears it. Both are
  naturally idempotent and refuse a proxy with `409`. The column is operational
  state, not configuration: `track_stream_node_configuration` does not compare
  it, so a drain does not move the node's admin revision or its `config_etag`.
- **SIGTERM.** A transcode node that receives SIGTERM or SIGINT drains before
  it shuts down. It waits until it has no work left, until
  `SILO_NODE_DRAIN_TIMEOUT` passes (a Go duration or whole seconds, default
  `10m`; `0` turns the drain off), or until a second signal arrives, and then
  runs the ordinary shutdown, which ends whatever is left. A SIGTERM drain
  cannot be resumed.

`GET /api/v2/admin/nodes/capabilities` advertises `drain: true`.

## What a draining node does

The node is `draining` when it received SIGTERM or when its own
`stream_nodes` row carries a drain request. `nodeconfig.Watcher` reads the
request with a one-column query of the row it matched for the acceleration
overrides: on every config load (the 60-second poll and settings changes) and
on every `node_pool_changed` event, which the API publishes after a drain or
resume. The event does not reload the config, since a new config pointer makes
starts already in flight refuse. The query is separate from the override read
so that a node started before the API has migrated the column still reads its
overrides; until then it reads no drain request.

While draining, the node:

- refuses `POST /transcode/start` with a plain-text `503 node is draining`
  before any tone-map preflight, unless the start replaces a session the node
  already runs. Both API retry loops (`playback_v3.go`, `jellycompat/streams.go`)
  exclude a node on any non-`202` start and try the next one. A Jellyfin
  audio-track switch re-posts the same session id, so a replacement is
  admitted;
- refuses download preparation, trickplay extraction, chapter-thumbnail
  extraction and media sampling with the `503` shapes their clients already
  treat as an unavailable node;
- rebuilds a lost session (one the 10-minute idle reaper closed while its
  viewer was paused), and starts a progressive remux for a reconnect or seek,
  only until it has reported itself drained. Once a health check has read
  `drained`, whoever read it may stop the node, so from then on it starts
  neither and the client's next plan moves the session;
- keeps serving manifests, segments, segment acks, `DELETE /transcode/{id}`,
  prepared-artifact reads and deletes, progressive remux requests, status and
  metrics, and restarts FFmpeg for its own sessions on a seek.

The node is `drained` once it is draining, its active job count (HLS
sessions, progressive remuxes, preparations and trickplay runs) is zero, and
none of the requests that start or carry work is in flight: transcode starts,
session rebuilds, download preparations and prepared-download transfers,
trickplay, chapter thumbnail and media-sample runs, and progressive remuxes. That covers work the
job count does not, and work admitted just before the drain began but not yet
counted. An HLS session whose FFmpeg reached the end of the file stays counted
until the client stops it or the idle reaper closes it. Drained says nothing
about prepared downloads stored on the node: they stay served while it runs and
are unavailable while it is stopped.

`GET /api/v1/health` answers `200` in every phase, with `status` `ok`,
`draining` or `drained`. Anything else would make the node pool treat the node
as dead and stop routing its sessions. The node publishes
`cache.EventNodeDrainChanged` (payload: its `stream_nodes` id) when a drain
starts, stops, and reaches drained, so every API server rechecks it at once
instead of at its next 30-second sweep. A node that has not matched its row
yet announces nothing and is found by the sweep.

## What the API servers do

`nodepool.CheckNodeReport` reads the health `status` into
`stream_nodes.drain_state` (`none`, `draining`, `drained`), written with the
other health columns by every check. A node that does not answer, or predates
draining, reads as `none`. Lifting a set request and a URL repoint also reset
it to `none`: the state described the lifted request, or another machine. A
node draining for SIGTERM sees the request change, announces, and reports
`draining` again on the recheck. Lifting a request that was not set keeps the
state, since the node would announce nothing. `Node.Draining()` is
true when `drain_requested` is set or `drain_state` is not `none`, and
`Node.TakesNewWork()` is enabled, healthy and not draining.

The API rechecks a node when it announces a change, at most one check per node
at a time, plus one more when announcements arrived during it. The health
checker runs one check of a node at a time, from the sweep or a recheck, so a
sweep answer from before the change cannot land after the recheck.

A draining node stays in the pools, so it is still health-checked, its jobs
still count against a shared GPU, and its prepared downloads are still served.
New placement skips it: `ReserveTranscodeWorkWith`, `TranscodeWorkAvailableWith`
and `Reservations.Reserve` exclude it (`Reserve` reports `CapacityExhausted`
when it was the only node), v3 capability planning and Jellyfin negotiation of
a Dolby Vision strip or an HEVC encode do not count what it advertises, and
session planning admits it only as the session's current transcode node, and
only until it reports `drained`. That exception keeps a Jellyfin stream alive
when its client asks for the master playlist again, which plans the session
once more; a v3 seek or quality change asks the node for a new transport,
which the node refuses, and the session moves. Once the node reports `drained`
it runs no session, so a returning Jellyfin client is planned on another node
and the runtime recorded on the drained node is not adopted.

The web admin nodes page shows `Draining · N transcodes left` while work
remains and `Drained · safe to stop` only once the node itself reports
`drained`. A request the node has not read yet, or a node too old to report,
stays at `Draining`. A node draining without a request (a stop signal, or a
resume it has not read yet) shows `Draining` and `Drained · no work left`
without claiming which.

## Deployment

The orchestrator's stop timeout must outlast the drain, or it kills the node
mid-drain:

- **Docker Compose** defaults to a 10-second grace before SIGKILL. The example
  `silo-transcode` service sets `stop_grace_period` to the drain timeout plus a
  minute for the shutdown that follows (`11m` for the default).
- **Kubernetes** pods need `terminationGracePeriodSeconds` of at least
  `SILO_NODE_DRAIN_TIMEOUT` plus 60 seconds. Keep the liveness and readiness
  probes on `GET /api/v1/health`; it answers `200` while draining, so a draining
  pod is not restarted. A node's registered URL must keep reaching that one pod
  while it drains: register each node by its own address (its pod IP, a per-pod
  name from a headless Service with `publishNotReadyAddresses`, or a Service
  that selects only that pod). Kubernetes marks a terminating pod not ready in
  its EndpointSlices, and kube-proxy keeps sending it traffic only while it
  passes readiness and the Service has no other ready endpoint, which holds for
  a per-node Service.
- A rolling upgrade can drain a node ahead of time with the drain action, wait
  for `drained`, then stop it; the stop then completes at once.
