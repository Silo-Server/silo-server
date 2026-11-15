# Transcode nodes

A remote transcode node (`MODE=transcode`) runs FFmpeg for the API servers and
holds nothing that can mint a token or read the catalog: no Postgres, no Redis,
no data cipher, no event bus, no `SECRET_KEY` and no `auth.jwt_secret`. It
starts with two settings, the API's URL and a key of its own, and takes its
configuration, playback authority and source approval from the API. The API
servers (and proxies, where they relay) make every authorization decision: the
node verifies no stream token, reads no deny marker and checks no catalog row.

This document is the authority for that design. Wire shapes are in the
`x-silo-worker-protocols` extension of `contracts/api/v2/openapi.json`: the
node's own routes under listener `transcode_node`, and the routes it calls on
the API under listener `api` with authorization class `node_signed`
(`nodecontrol.Protocol`). [Worker HTTP contracts](worker-http-protocol.md)
describes the node's routes one by one, [node drain](node-drain.md) the drain
protocol, and [node failure detection](node-failure-detection.md) how placement
reacts to a node that stops answering. Paths are repository-relative.

## Model

There is one transcode node type and no legacy mode. A node from before this
design cannot work with a current API server, and the reverse (see
[Failure behavior](#failure-behavior)). Proxies are unchanged: they keep the
database, Redis and the shared secret, and load the node credential registry so
they can sign their relays to transcode nodes.

```text
                 clients
                    │  every media response is relayed by the API, a proxy or jellycompat
   ┌────────────────▼──────────┐  core → node: signed with the node's key    ┌──────────────────┐
   │ API servers and proxies   │ ─────────────────────────────────────────▶ │ transcode node   │
   │ Postgres, Redis,          │                                            │ SILO_CORE_URL,   │
   │ SECRET_KEY, jwt_secret,   │ ◀───────────────────────────────────────── │ SILO_NODE_KEY,   │
   │ node credential registry  │  node → API: same key, another label       │ library mount    │
   └───────────────────────────┘  (config, report, logs, health-changed,    └──────────────────┘
                                   resolve, revoke-recipes, sources/open)
```

"The core" below means the API servers. Only they serve the node control
routes; a node never calls a proxy.

Everything a node once did against Postgres or Redis is now done by the core,
either at the operation that triggers it (deleting a recipe when a session
stops) or when the node asks over a signed route (reports, logs, drain
announcements, resolving a lost session, revoking recipes, opening a source).
The admin session and telemetry views read a transcode node's records from the
same Redis key spaces as a proxy's. Every control route works on any API
replica: no handler depends on state an earlier request left in the process,
apart from the per-node rate limit and log throttles.

## Node keys

A node key is `snk1_<node id>_<base64url, unpadded, of 32 random bytes>`
(`internal/nodepool/nodekey.go`). The node id is part of the key, so a node
needs no `NODE_URL` or `NODE_NAME` to find its row. `ParseNodeKey` accepts only
the canonical form: a positive decimal id without leading zeros, exactly 32
bytes, and text that re-encodes to itself.

- **Storage.** `stream_nodes.node_key_ciphertext`, encrypted with the data
  cipher under the AAD `stream_nodes:node_key_ciphertext:<id>`
  (`secret.RowAAD`), so a ciphertext copied onto another row does not decrypt;
  decryption also checks that the key names the row. The column is a
  credential, not configuration: the configuration trigger does not compare it,
  so issuing a key moves neither `config_etag` nor the pool generation. After a
  `SECRET_KEY` change the ciphertexts no longer decrypt and every node is
  keyless until it is issued a new key (see
  [secret encryption](secret-encryption.md)).
- **Issue and rotate.** `POST /api/v2/admin/nodes/{id}/key`
  (`Repository.IssueNodeKey`, `NodeHandler.IssueAdminNodeKey`) returns
  `201 {"node_key": "..."}` with `Cache-Control: no-store`, once. Every call
  rotates. Only transcode rows take a key; a proxy gets `409`. The handler
  refreshes this replica's registry before it answers, reloads this replica's
  pools, and publishes `node_pool_changed` whether or not that pool reload
  worked (a failed publish is logged), so other replicas and proxies drop the
  old key within one reload. Rotation takes effect at once: the node's running
  streams fail at their next signed call until it restarts with the new key, so
  drain a node before rotating. A node still running with the replaced key
  shuts down at once on SIGTERM (see [Drain and health](#drain-and-health)). The admin API is in
  [admin API: node keys](../admin-api.md#node-keys-v2).
- **Rejected key.** When the core answers the node's configuration read with
  `401`, `nodeconfig.Remote` sets `key_rejected`, which the node's
  `/api/v1/health` carries (still `200`, so a liveness probe does not restart a
  node a restart cannot fix). A `401` on any other call asks for a
  configuration read at once (`nodecontrol.Client.SetOnUnauthorized`,
  `Remote.ReportUnauthorized`); while the key stays rejected those requests ask
  at most once per 30-second poll. The flag clears on the next read the core
  answers. A `401` has two causes the core does not tell apart in its answer:
  the key was replaced or the node deleted, or the node's clock differs from
  the core's by more than the 30-second signature window. The node's log lines
  and the core's unhealthy detail for `key_rejected` name both. A node started with a key the core refuses never opens its listener:
  its first configuration read keeps failing (see
  [Startup](#startup-and-environment)).
- **Keyless nodes** stay listed and health-checked but take no work. Nothing can
  sign a request to them (`nodeauth.ErrNoNodeKey`), placement skips them
  (`TranscodePool.HasKey`, wired to `KeyRegistry.HasKey`), and the health
  sweep fetches no capabilities from them. Node rows carry `key_issued`.
- **Unique address.** Two transcode nodes cannot be saved at one canonical URL
  (`nodepool.ErrTranscodeNodeURLTaken`, `409`), because the registry finds a node
  by that address.

### Credential registry

Placement pools hold only enabled nodes, but a disabled node keeps serving its
streams, so keys live in a separate registry, `nodepool.KeyRegistry`. It loads
every `stream_nodes` row in id order and decrypts the keys of transcode rows
that have one; a ciphertext that does not decrypt is logged and the node treated
as keyless. It indexes nodes by id and by `CanonicalNodeURL` (scheme and host
lowercased, default port dropped, trailing slash dropped). On a URL collision a
transcode row wins over a proxy, so the shared secret can never go to a
transcode node's address, and between rows of one type the lowest id wins.

It reloads on `node_pool_changed`, every 60 seconds, on a lookup miss at most
once a second, and, without waiting, when a transcode node answers a relay's
signed request with `401` (`KeyRegistry.ReloadSoon`, through
`nodeauth.ReloadAfterRefusal`), also at most once a second, so a replica that
missed a rotation catches up within seconds; concurrent reloads share one database read with a
10-second timeout.

Node writes move it at the same points as the placement pools:

- The API server that takes a key issue, or an admin create, update or delete
  of a node, reloads its registry before it answers (`NodeHandler.refreshNodeKeys`,
  called by `IssueAdminNodeKey` and `nodesChanged`) and publishes
  `node_pool_changed`. After a delete returns, that server neither signs for the
  node nor accepts its calls.
- Every other API server and every proxy reloads on that event. An API server
  that misses it reloads when its configuration reconciliation, which reads the
  node rows every 5 seconds, sees a node appear, disappear or change its
  configuration (`NodeHandler.applyConfigurationSnapshot`); a proxy has no
  reconciliation and falls back to the 60-second reload.
- A URL change keeps the key, so the node is signable at its new URL as soon as
  the registry reloads. A disabled node keeps its credential. A failed reload keeps the credentials already loaded. API
servers load it at startup (`cmd/silo/main.go`) and refuse to start when the
first load fails; proxies load it in `startProxyNodeKeyRegistry`, keep running
when the first load fails, and fill it in on a later reload.

## Signing

Both directions sign an HMAC-SHA256 over a canonical request with a key derived
from the node key by HKDF-SHA256 (`internal/nodeauth`). The direction picks the
HKDF label and canonical version, so a request captured in one direction never
verifies in the other:

| Direction | Signer, verifier | HKDF label | Canonical version |
|---|---|---|---|
| core → node | `nodeauth.SignTranscode`, `nodeauth.VerifySigned` | `silo/node-request-auth/v1` | `silo-node-request-v1` |
| node → core | `nodeauth.SignToCore`, `nodeauth.VerifyFromNode` | `silo/node-to-core-auth/v1` | `silo-node-to-core-v1` |

The header is
`Authorization: Silo-Node-HMAC-SHA256 n=<node id>,t=<unix seconds>,b=<hex body SHA-256>,sig=<hex HMAC>`;
`n` appears only on node-to-core requests, in canonical decimal. The MAC covers,
joined by newlines: the version, `t`, the method, the escaped path relative to
the base URL (any path prefix of the node's registered URL or of `SILO_CORE_URL`
is left out), the raw query and `b`. A node-to-core signature also covers the
`X-Silo-Source-Grant` header value (`nodeauth.SourceGrantHeader`, empty when
absent), and a request carrying that header twice is refused. No current call
sends the header; the signature already binds it so a grant cannot be swapped
into a captured request.

The receiver checks the MAC before it reads any body, then the timestamp
(±30 seconds), then a body of at most 1 MiB against `b`. Every refusal is a
plain-text `401`.

- **Core to node.** The registry's keyring (`KeyRegistry.Keyring`) gives a
  transcode target only that node's own key, and a proxy target the shared
  secret only for a URL registered as a proxy. An unknown URL is
  `nodeauth.ErrUnknownNode` and a keyless node `nodeauth.ErrNoNodeKey`; neither
  falls back to the shared secret, so a transcode node never sees a
  shared-secret signature. The node verifies with its own key in
  `transcodenode.(*Server).requireBearer`, which has no legacy-bearer branch
  and answers `401` to everything else. Work for a node is granted to the id of
  the key that will sign for it (`nodepool.SignedTranscodeNodeID`).
- **Node to core.** `nodecontrol.RequireNode` parses `n`, takes that node's key
  from the registry and verifies. A disabled node is accepted, since it keeps
  serving its streams; a deleted, keyless or proxy row gets `401`, logged with
  the reason at most once a minute per node. A signature that fails against a
  known node's loaded key is checked once more after a registry reload (at most
  once a second), so a node restarted with a just-issued key is accepted before
  the next periodic reload. Handlers take the node's identity only from the
  verified id in the request context (`nodecontrol.NodeID`), never from a body.
- **Redirects.** Every HTTP client on either side refuses redirects: a signed
  request names one receiver.
- **Replay.** A captured request verifies for 30 seconds, in its own direction
  and against its own node only. Its method, path, query and body cannot be
  changed; other headers can. Responses are not signed in either direction.

## Startup and environment

`runTranscodeNode` (`cmd/silo/transcode_node.go`) branches before the bootstrap
configuration loads, so a node never opens a database, Redis, settings store,
cipher or event bus.

| Variable | |
|---|---|
| `SILO_CORE_URL` | Required. Absolute `http` or `https` URL of the API. With several API replicas, use their load-balanced address. A plain-HTTP URL that is not loopback logs a warning at startup. |
| `SILO_NODE_KEY` | Required. A malformed key refuses startup (`transcodeNodeKey`). |
| `PORT` | Listen port, default `8080`. |
| `SILO_NODE_DRAIN_TIMEOUT` | See [node drain](node-drain.md). |
| `DATABASE_URL`, `REDIS_URL`, `SECRET_KEY`, `NODE_URL`, `NODE_NAME` | Ignored, with one warning, so a shared env file keeps working. |

The node reads its configuration, retrying with backoff from one second to 30
seconds until a read succeeds or it is signalled, and only then opens its
listener. A configuration that names another node id than the key is refused
(`Client.FetchConfig`). Each call to the core has a 10-second timeout
(`nodecontrol.Client`).

## Configuration

`GET /api/internal/nodes/self/config` (`nodecontrol.Server.HandleConfig`)
returns the calling node's document, `nodecontrol.NodeConfig`:

```json
{
  "revision": "<sha256 of the rest of the document>",
  "node": {"id": 7, "name": "gpu-1", "url": "http://gpu-1:8080"},
  "drain_requested": false,
  "playback": {"ffmpeg_path": "", "hw_accel": "", "hw_device": "", "transcode_dir": "",
               "segment_retention_seconds": 0, "trickplay_node_capacity": 0},
  "download": {"artifact_dir": "", "artifact_max_bytes": 0},
  "subtitles": {"sync_node_capacity": 0},
  "logging": {"level": "", "format": "", "quiet": "", "capture_level": ""}
}
```

The core builds it per request from the server settings with the node row's
acceleration and download overrides applied (the same `EffectiveHWAccel` the
dispatcher names in a start request). The strong `ETag` is the revision;
`If-None-Match` naming it gets `304`. `404` means the row is gone or is not a
transcode node; `503` means the settings cannot be read. A test fails when
`internal/transcodenode` reads a configuration field the document does not
carry.

On the node, `nodeconfig.Remote` serves the configuration through the
`transcodenode.RuntimeConfig` interface:

- It polls every 30 seconds (`RemotePollInterval`) with `If-None-Match`, and
  rereads at once on `/admin/reload-config` and `/admin/force-reload`. A failed
  poll keeps the last configuration.
- **Pointer stability.** It builds a `*config.Config` from defaults plus the
  document and swaps it in only when it differs from the current one.
  Handlers refuse work whose configuration pointer changed under them, so a
  document whose only change is the drain request, name or URL keeps the
  pointer. Drain callbacks fire when `drain_requested` changes.
- The FFmpeg path, acceleration, segment retention, both capacities, log level
  and quiet filter apply to new work without a restart. The transcode and
  artifact directories, log format and capture level apply when the node
  restarts.

The core pushes `/admin/reload-config` (never `/admin/force-reload`), best
effort, after a drain request or resume and after an admin change to the node's
URL, acceleration overrides or download overrides
(`NodeHandler.reloadNodeConfig`). Server-wide settings reach nodes within one
poll.

## Reports

Every second (`nodecontrol.ReportInterval`) the node posts
`POST /api/internal/nodes/self/report` (`nodecontrol.Reporter`): `boot_id` (its
start time in Unix nanoseconds), `seq` (increasing per process), its session
records from `nodesessions.LocalTracker`, and its stream telemetry snapshot. At
shutdown it sends one last report after its sessions end, so the core drops
their records. `HandleReport`:

1. **Fence.** One Lua script compares `(boot_id, seq)` with
   `silo:nodereport:<node id>` (two-minute TTL), boot first, and stores the pair
   only if it is greater. An older or equal pair gets `409` and writes nothing,
   so a replay or a delayed report changes nothing. The node warns after ten
   `409`s in a row (a clock set back across a restart, or a second process with
   the same key).
2. **Sessions.** `nodesessions.IngestNodeSessions` replaces the node's records
   in one script: each session is `silo:sessions:n<node id>:<session id>` with a
   60-second TTL, indexed by `silo:nodesessions:<node id>`, and indexed keys the
   report no longer lists are deleted. The node URL and name in each record
   come from the registered row (the registry's copy, so a Postgres outage does
   not stop reports), never from the report.
3. **Telemetry** replaces the snapshot of publisher `node:<node id>`
   (`nodecontrol.PublisherID`). The core's clock sets the capture time, and any
   reported time more than 30 seconds past the core's clock is moved back to
   that bound.

Bounds: 2,000 sessions and 2,000 transfers per report (`413` beyond), inside the
1 MiB body. A node with more sends the newest sessions and the most recently
active transfers and sets `truncated`, which the core logs; only an encoding
still over 1 MiB halves both bounds. A core without Redis answers `204` and
drops the report. Two replicas can pass the fence for reports n and n+1 and
write them in the opposite order; the view is then one report old until the
next one.

## Logs

The node's slog handler feeds the operational log buffer. `opslog.Ship` sends
it in batches of up to 100 entries, at least every two seconds, through
`nodecontrol.Client.ShipLogs` to `POST /api/internal/nodes/self/logs`
(`nodecontrol.LogBatch`). A body over 1 MiB is split in half, and a single
oversized entry is dropped. No answer, `429` and `5xx` are retried by the
shipper (`logstream.Drain`), which drops what it cannot deliver within minutes.
FFmpeg output goes through the same handler, and stderr logging continues. `HandleLogs` files every entry under `<name>#<id>`
(`nodecontrol.LogNodeID`) whatever node the entries name, clamps timestamps to
the last 24 hours and the next 30 seconds (`opslog.Consumer.InsertNodeBatch`),
and feeds the admin live tail. Before the insert it normalizes what the node
supplied (`opslog.NormalizeNodeEntry`): a `client_ip` that does not parse as an
IP address is cleared (an address with a port keeps the address), a `user_id`
outside the column's integer range is cleared, and level, component and the
request and session ids are cut to 16, 64 and 256 bytes. The batch then stores
in one statement; if Postgres still rejects a value, the rows are inserted one
at a time so only the rejected ones are lost, with the warning logged at most
once a minute per node. A replayed batch can duplicate entries.

## Drain and health

A drain request reaches the node as `drain_requested` (pushed, and polled every
30 seconds). When the node's drain state changes (an admin request, SIGTERM,
reaching drained, or a drain in force at startup) it posts
`POST /api/internal/nodes/self/health-changed` with a five-second timeout, and
the core publishes `node_drain_changed` so every replica health-checks the node
at once (`subscribeNodeDrainChanges`, `HealthChecker.CheckTranscodeNode`). A
failed call is logged and the next sweep finds the change. See
[node drain](node-drain.md).

A SIGTERM drain does not wait for active jobs while the core refuses the
node's key (`key_rejected`, `nodeconfig.Remote.KeyRejected`): no relay can sign
a request to the node, so its sessions serve no viewer and no stop can reach
them. The node logs that it is shutting down without waiting and shuts down.
The drain reads the state when it begins and once a second while it waits
(`nodeDrain.run` in `cmd/silo/node_drain.go`), so a key rejected mid-drain also
ends the wait. A rejected key alone, without SIGTERM, ends nothing: the state
can be transient (a clock briefly off, a replica holding a stale key for a few
seconds).

The health poll, `GET /api/v1/health` on the node, stays unauthenticated. Its
answer carries `node_control_version` (`nodepool.NodeControlVersion`, now `1`)
and `key_rejected`. `nodepool.CheckNodeReport` treats a transcode node whose
version is missing or lower as unhealthy with reason
`node_control_unsupported`, and one that reports `key_rejected` as unhealthy
with reason `key_rejected`, whose logged detail names a replaced key or deleted
node and a clock more than 30 seconds off as the causes; it still records the node's resources, build and
drain state. A binary from before this design has no version, so it is never
placed.

## Rate limit

Each API replica gives each node three in-memory token buckets and answers
`429` with `Retry-After: 1` past any of them:

- **Background** (`nodecontrol.(*Server).LimitNode`): 20 requests a second with
  bursts of 60 across config, report, logs, health-changed and revoke-recipes.
  A healthy node sends about one and a half a second.
- **Viewer path** (`LimitNodePlayback`): 200 requests a second with bursts of
  600 across resolve and sources/open, so a burst of one never refuses the
  other. Every progressive `/remux` GET and HEAD resolves once and every job
  start opens its source once. A node at 64 concurrent progressive remuxes
  whose players send two range requests a second resolves 128 times a second;
  a restarted node whose 64 sessions all rebuild while their players seek sends
  about 384 calls at once (a resolve, an open and four range requests each).
  Renewals add one call per source every 20 minutes.
- **Log bytes** (`HandleLogs`): 128 KiB of log batches a second with bursts of
  4 MiB, on top of the background budget. The request budget alone would
  admit the 1 MiB body bound twenty times a second into `operational_logs`.
  A node's log lines come to a few kilobytes a second per session; the burst
  takes the backlog a node ships after the core was unreachable.

The node treats a `429` from any route as transient: a remux or rebuild
answers its relay `503`, a start answers `503` and runs elsewhere, a renewal
retries until the grant expires, a configuration read keeps the last
configuration, and a log batch is retried.

## Resolve

A node asks the core for a recipe in two cases: to rebuild an HLS session it
does not hold in memory, and for every progressive `/remux` GET and HEAD. It posts
`POST /api/internal/nodes/self/resolve` (`nodecontrol.Server.HandleResolve`):

```json
{"transport_id": "...", "purpose": "hls" | "remux", "stream_token": "<optional>"}
```

`stream_token` is the `X-Silo-Stream-Token` header a relay forwarded, passed
through unread. A `200` (`nodecontrol.Resolution`) carries the recipe card, the
playback identity (`playback_session_id`, `user_id`, `profile_id`,
`media_file_id`), `started_at` and `started_at_source` when the token or stored
recipe names a start, `theme` for a theme conversion, and `source_grant`, a
fresh grant for the recipe's file naming the calling node.

What the core checks:

- **HLS** (`resolveHLS`). The deny marker on the transport. With a token: it
  must verify and describe the transport (else `not_found`), and its playback
  session must not be denied. Then the recipe store (`internal/noderecipe`):
  a complete stored card wins over a complete token card, and its session is
  deny-checked; with neither the answer is `not_found`. The stored card wins
  because nothing re-mints a token or rewrites a native card under a transport
  id it already issued, while a move rewrites the stored card and the client
  keeps the token minted for the node the transport left. The subtitle
  rendition selection (`Default`) comes from the token when both cards
  describe the same tracks. A recipe store read error is `503`, never
  `not_found`.
- **Remux** (`resolveRemux`). A progressive remux runs on a transcode node
  only on a route whose egress is a proxy: `noderouting.legalShapes` has no
  transcode-executed progressive remux with API egress, so a route with API
  egress runs the remux on the API server. A valid token is required. Deny checks on the
  transport and session; the route must be remux workload, transcode execution
  and proxy egress, with execution node id equal to the calling node (a token
  without the id fails closed); downmix and theme shape checks, and a theme
  file with the token's size and modification time. The stored remux authority
  must still match the token field by field, so a token that is still valid
  cannot start FFmpeg after a stop or force reload withdrew it.
- **Binding** (`servesNode`). The chosen recipe must name the calling node: by
  its execution node id, or for a card without one by
  `CanonicalNodeURL` against the node's registered URL. A card bound to
  neither is refused, since transport ids are not secrets and a recipe names a
  media path.
- **Source** (`grantResolution`). The catalog must still have the recipe's
  exact media file (by id, not missing, at exactly the recipe's path) or its
  theme in an enabled library, and the file must exist as a regular file when
  the grant is minted.

| Status | Reason | Meaning |
|---|---|---|
| `404` | `not_found` | No recipe for the transport, or a token that does not describe it. |
| `410` | `denied` | A deny marker is set. |
| `410` | `stopped` | The remux authority is gone or no longer matches the token. |
| `410` | `source_changed` | The theme file changed, or the catalog no longer has the recipe's source, or the file is gone. |
| `403` | `invalid_token` | A remux request without a valid token. |
| `403` | `wrong_route` | The recipe or token is bound to another node, or to none. |
| `503` | none | Transient: no stream secret or grant key, or the recipe store, catalog or node registration cannot be read. |

On the node:

- A request for a live in-process HLS session uses the URL's transport id and
  calls nothing. A lost session calls resolve once per wave of requests
  (`reconstructLost`, single-flighted per transport) and answers `denied` with
  `410`, any other refusal with `404`, and a core it cannot reach with `503`
  (`writeReconstructFailure`).
- `/remux` maps refusals to the statuses relays and clients expect from a
  node (`writeRemuxRefusal`): `invalid_token` `401`,
  `wrong_route` `503`, `denied` and `stopped` `410`, `source_changed` `400`,
  `not_found` `404`, anything else `503`. HEAD resolves but opens no source.
  A GET for a transport that already has a request running (`409`), or on a
  node that is shutting down or has reported itself drained (`503`), is
  refused before resolve is called; admission checks the same state again
  under its locks.
- **Local fences.** A stop installs a process-local fence for the maximum
  token lifetime, and the node checks it before it calls resolve: HLS
  (`stoppedTranscodes`, lifted only by a fresh start of the same id) and
  progressive remux (`stoppedProgressiveRemuxes`). A release (`fence=0`)
  installs no HLS fence. See
  [worker HTTP contracts](worker-http-protocol.md) for the stop route.
- The node files a rebuilt session and a remux request under the resolution's
  playback identity and `started_at`; without one it uses the rebuild time.

## Recipe lifecycle

The recipe store (`noderecipe.Store`, `silo:noderecipe:<transport id>`, 24-hour
TTL) is written and deleted only by the core. The node reads it only through
resolve and never deletes from it.

- **Writes.** A header-authenticated native start (no client URL carries a
  token), every Jellyfin-compatible start and in-place switch, the authority
  record a transcode-executed progressive remux needs before its URL is
  published (`putRequiredNodeRecipeV3`), and every dead-node move.
- **Stop.** `TranscodeManager.StopRemoteTranscode`, which native and
  Jellyfin-compatible stops use, deletes the transport's card with a
  five-second bound before it sends the node `DELETE /transcode/{id}`, so a
  request relayed during the stop cannot rebuild the session. It keeps a card
  that names another canonical node URL than the one it stops
  (`noderecipe.Store.DeleteUnlessIssuedElsewhere`,
  `playback.RecipeExecutor.IssuedForAnotherRoute`): Jellyfin compatibility
  reuses a transport id across start candidates, so a late stop on one
  candidate must not remove the next one's card. A failed deletion is logged
  and the node is still told to stop.
- **Release** (`ReleaseRemoteTranscode`, a stop with `fence=0`, used by moves
  and rollbacks) deletes nothing. **Cancel** (`CancelRemoteTranscode`) only
  confirms the remote process is gone and leaves the card to its caller.
- **Move.** Every native move to another node writes the card naming the new
  node (`moveTranscodeToNodeV3`, through `putNodeRecipeIfRouteV3`), whether or
  not the session's URLs carry a token: the client keeps presenting the token
  minted for the node it left, which binds only that node, so the stored card
  is what lets the new node resolve the transport after its own restart. The
  write happens as soon as the move commits, before the old node is released
  (`retireUnreachableTransportV3`), under the session lifecycle lock, and only
  while the session still runs that route and carries no deny marker. The
  release leaves the card alone, and a later core stop of the old route keeps
  it because it names another node. A move rechecks the deny markers before it
  commits and before it serves from the new executor. A candidate that refuses
  or fails a move's start is released in the background; the card is not
  touched. A release is fenced to the job it was sent for, since another API
  server's move may have started the transport on that node by the time it
  lands. A move's start carries a fresh `start_id`, and its rollback names it,
  so the node ends only that start's job. A retire of the node the transport
  left names none, since that job may be a rebuild or another server's start;
  the node then ends only the job it held when the release arrived, not one a
  start or rebuild published while the release waited for the lifecycle lock.
  A retire that arrives after a newer job on that node is already running
  still ends it, and the next request rebuilds it from the card. A move to the
  API process deletes the card. Jellyfin-compatible moves write their record at
  commit too, then release the old node
  ([transcode node lost mid-stream](restart-resilient-playback.md#transcode-node-lost-mid-stream)).
- **Force reload.** The node, under its reload gate, asks the core to revoke
  its recipes (`POST /api/internal/nodes/self/revoke-recipes`,
  `teardownForForceReload`): the core revokes by the authenticated node id and
  then by the node's registered URL (`RevokeNodeID`, `RevokeNode`), never by
  anything in the body. The node tears down its sessions whether or not that
  succeeds; a failed revocation fails the command, which the operator retries.
  The core does not revoke first, because a revocation followed by a failed
  call would end the live sessions of a node that never reloaded. Nothing moves
  the sessions: a viewer whose stream URLs carry no stream token (the native
  `/api/v2` delivery routes) gets `404` on its next manifest or segment
  request, because the node no longer holds the session and resolve finds no
  recipe (`resolveHLS` answers `not_found`), and the client must start playback
  again. A URL that carries a complete stream token can still rebuild the
  session from the token's card.
- **Idle reap.** The node closes a session idle for ten minutes and the card
  stays: an idle reap is not a stop, and a returning client rebuilds.

Known limits:

- HLS resolve has no durable stop check for a transport stopped without a deny
  marker (a native replan retiring its old transport). After a node restart
  loses the in-memory fence, a complete token for that transport can rebuild
  it on the node it names until the token expires.
- Force reload revokes cards bound to the node's id and to its current URL. A
  card bound by URL alone to a URL the row no longer has is not revoked.
- `noderecipe.Store` keys a URL-bound card's authority generation by the URL
  with whitespace and trailing slashes trimmed (`nodeAuthorityGenerationKey`),
  while resolve and the registry compare `CanonicalNodeURL`. A card naming the
  node by a differently spelled URL (case, default port) is not revoked by
  `RevokeNode`. A card with a node id is keyed by that id, so a force reload of
  its node always revokes it.

## Relays

The node checks no deny marker, so every relay that signs a request to a
transcode node checks it first:

| Relay | Check |
|---|---|
| Native API manifest and segment relays | `remoteTransportDenied` (the transport id); `loadTranscodeServeSession` already checks the playback session |
| Proxy token and grant routes | `sessionDenied`: the playback session, then the transport |
| Jellyfin-compatible master manifest, playlist and segment relays | `remoteCompatDenied`: the transport, and the recipe's playback session when it differs |

A denied session gets `410` even when a failed stop left its node runtime
alive. `TestStreamDenyRefusesEveryTranscodeNodeRelay`
(`internal/proxy/streamdeny_test.go`) enumerates the relays. Download relays are
not tied to a playback session and have no marker to check.

**Error mapping.** Every relay authenticates its client before it signs to a
node, so a node's `401` refuses the relay's signature or the token it
forwarded, never the client's own credentials. Passed on, a player could read
it as its sign-in failing. The relays answer it as the node being unavailable
(`transcodeproxy.NodeRefusedRelay`), and answer a request they cannot sign
(`ErrNoNodeKey`, `ErrUnknownNode`; `transcodeproxy.NodeUnsignable`) the same
way, each logged at most once a minute per node. The native and proxy relays
answer `502`; the `/api/v2` playback delivery routes map that `502` to `503`
`dependency_unavailable` (`apiv2.playbackDeliveryProblemType`), and the
download relay answers `503` `dependency_unavailable`. Neither case moves a
transcode, marks the node or publishes a move request: a replica or proxy whose
registry is empty or stale signs for no node at all, and moving on that would
restart healthy transcodes on every hop. A start that cannot sign for a
candidate moves on to the next one as any failed start does.

## Source grants

A grant is a stateless capability, signed by the core, to use one revision of
one file through one node (`internal/nodesource/grant.go`). Every job input on
the node goes through one, and the node runs FFmpeg, and reads in Go, only the
path the core approves for it, never a path the job request names.

- **Encoding.** `base64url(JSON payload) + "." + base64url(HMAC-SHA256)`, at most
  8 KiB, keyed by `HKDF-SHA256(auth.jwt_secret, info "silo/node-source-grant/v1")`.
  Only API servers mint grants (`nodesource.Grants`).
- **Payload.** `{"v": 1, "node", "kind": "file" | "theme", "id", "path",
  "size", "mtime_ns", "exp"}`. `path` is the exact fspath-encoded catalog path,
  so no catalog read is needed to use it, and every stat goes through
  `internal/fspath`. The node can read the payload; it is given the path anyway.
- **Revision** is `(kind, id, size, modification time)`, without device, inode
  or change time, so every host computes the same revision of a shared file.
  `mtime_ns` is what the minting stat returned; revisions compare it at
  microseconds, the catalog's precision (`nodesource.SameModTime`,
  `models.NormalizeFileModifiedAt`). The entity tag is
  `"<kind>-<id>-<size>-<mtime in Unix microseconds>"`.
- **Life.** One hour (`GrantLifetime`). The node renews every 20 minutes
  (`RenewInterval`) while the input is open; renewal keeps the revision, so a
  job never moves to another version of its source.
- **Minting** costs one stat on the API server: native and Jellyfin-compatible
  transcode starts, dead-node moves (granting the replacement node), download
  preparation, trickplay, chapter thumbnails and subtitle-sync samples carry
  `source_grant`, and resolve mints one for rebuilds and remux. When no grant
  can be minted, a start fails over as any failed start does, download
  preparation and subtitle sync run on the API server where local work is
  allowed, and trickplay and chapter thumbnails treat the node as unavailable.
  The API server must therefore see every file it sends to a node.

### sources/open

`POST /api/internal/nodes/self/sources/open` (`HandleSourceOpen`) takes
`{"grant": "..."}`, once when a job's input opens and at every renewal:

| Status | Meaning |
|---|---|
| `200` | `{"grant", "path", "size", "mtime_ns", "etag"}`: the same claims valid for another hour, and the approved revision. `Cache-Control: no-store`. |
| `400` | No grant in the body. |
| `403` | The grant does not verify, has expired, or names another node. |
| `410` | The catalog no longer has the source at exactly the granted path (a media file by id and not missing, or a theme in an enabled library), or the file is gone or not a regular file. |
| `412` | The file's size or modification time differs from the grant's revision. |
| `503` | Transient: no grant key, the catalog cannot be read, or the file could not be examined (an I/O error, a stale mount). |

Each open and renewal reads the catalog once and stats the file once, so the
catalog check is never more than one renewal interval stale.

On the node, `nodesource.Opener` exchanges the grant through
`nodecontrol.Client.OpenSource`, stats the approved path on its own mount, and
requires a regular file of the same size and modification time to the
microsecond. A mismatch is a `nodesource.MountMismatchError` naming both sides.
A job route answers a missing or refused grant with `400`, a mismatch with
`409`, and a core it cannot reach with `503` (`transcodenode.sourceFailure`);
the caller runs the job elsewhere. A chapter thumbnail job refused for those
two reasons, or because the node is draining or re-probing its hardware,
carries reason `transcode_node_unavailable`, which the core's chapter client
answers by extracting the frame itself under `prefer_transcode_nodes`
(`chapterthumbs.IsInfrastructureRemoteFailure`). Each job holds its source for its life (a
transcode session across FFmpeg restarts and seeks, one remux request, one
preparation, one trickplay, chapter or sample run) and derives its context from
it, so a source the core withdraws ends the job (`watchSessionSource` for HLS
sessions). Renewal retries transient failures with backoff from one second to
one minute until the current grant expires. A `400`, `403`, `404`, `410` or
`412`, an answer about another revision, or expiry while the core is
unreachable ends the source; a `401` is retried, since the core reloads keys
when a signature fails.

**Operator requirement.** Every API server and every transcode node must see
each library file at the same path with the same size and the same
modification time to the microsecond, as the shipped Compose file arranges by
mounting the library at the same container path. A mismatch fails closed: the
core answers `412`, and a node refuses the job with `409`.

## Failure behavior

| Event | Behavior |
|---|---|
| Core unreachable for a short time | Live sessions keep serving; they need no call. The configuration stays as last read. Reports fail (the admin views lose the node's sessions after 60 seconds) and the next one replaces the view. Logs retry, then drop. Renewals retry. New jobs, lost-session rebuilds and remux requests answer `503`. |
| Core unreachable for longer | A source ends its job when its grant expires, one hour after the last successful renewal; since renewal runs every 20 minutes, a job survives an outage of at least 40 minutes. |
| API restart, several replicas | No control route depends on the replica that served an earlier request. Grants verify on every replica (same `auth.jwt_secret`). Each replica has its own rate-limit bucket and its own registry, which loads at startup. |
| Redis blip on the core | Report ingestion answers `503` and the next report replaces it. Resolve answers `503` when the recipe store cannot be read, and the node answers its relay `503`. Revoke-recipes answers `503`. Deny checks fail open, as everywhere. |
| Postgres blip on the core | Configuration reads answer `503` (the node keeps its last one). Open and renewal answer `503` and retry. Reports and logs name the node from the registry, so Postgres alone does not stop reports; log inserts fail and retry. The registry keeps its loaded keys. |
| Node restart | Sessions rebuild through resolve with fresh grants. The new `boot_id` passes the report fence. In-memory stop fences are lost (see the HLS limit under [Recipe lifecycle](#recipe-lifecycle)). |
| File replaced or removed | The next open or renewal answers `412` or `410` and the job ends. A later rebuild gets a grant for whatever file the catalog still lists at that path. |
| API server cannot examine the file (I/O error, stale mount) | The open or renewal answers `503`, which is not final: the node retries it. Only a missing file answers `410`. |
| Session stopped or denied | The stop deletes the card and fences the node; relays answer `410` to a denied session and resolve refuses it. |
| Node disabled | Its streams continue (the registry keeps disabled nodes); no new placement. |
| Key rotated | The old key stops working at once on the API server that issued the new one, and on every other API server and proxy when `node_pool_changed` reaches it. The node's calls get `401` and it reports `key_rejected` after its next configuration read (at once when another call got the `401`, else within 30 seconds); the next health sweep marks it unhealthy. The core's calls fail on the node, the relays answer "node unavailable" and ask their registry to reload, and nothing moves: its streams fail until it restarts with the new key. SIGTERM stops it at once, without waiting for those streams. |
| Node clock more than 30 seconds off | Every signed request in either direction falls outside the signature window and gets `401`. The node reports `key_rejected` as for a rotated key; the logs name both causes. Fixing the clock heals it without a restart. |
| Node deleted | Its key stops working at once, as a rotated key does: the API server that takes the delete drops the node from its registry before it answers, and every other API server and proxy drops it when `node_pool_changed` reaches it. Relays for its sessions cannot sign and answer "node unavailable" (`502`, or `503` through `/api/v2`) without moving them; clients recover by starting playback again. Its calls get `401`, so it reports `key_rejected` and SIGTERM stops it at once. |
| Mount mismatch | The node refuses the job with `409` instead of reading other bytes, and the caller runs it elsewhere. A file that changed on the API server between minting and opening gets `412` from the core. |
| Node binary from before this design | Health has no `node_control_version`, so the node is unhealthy and never placed; it could not verify the core's signatures anyway. |
| Current node, core from before this design | The node's first configuration read never succeeds, so it never opens its listener and the core sees it as unreachable. |

## Security properties

- A node holds no secret that can mint login, profile or stream tokens or
  source grants, and no database or Redis credential.
- A stolen node key lets its holder act as that node towards the core (read
  its configuration, post reports and logs under its id, resolve transports
  bound to it, open and renew grants issued to it, revoke its recipes) and as
  the core towards that node (start work with grants issued to it, read its
  segments and artifacts, stop its sessions). It cannot sign for another node,
  mint a grant, verify or mint a token, or make the node read a path the core
  did not approve.
- A node cannot write another node's state: report fences, session keys,
  telemetry publishers and log entries are keyed by the authenticated id and
  timed by the core's clock.
- Proxies hold the database credentials and the data cipher, so a compromised
  proxy can decrypt every node key, as it can already read the shared secret.
- Clients never reach a node directly.
- Changing a node's URL does not change its key, so a host that still runs
  with that key keeps the node's identity until the key is rotated.
- Deleting a node ends the sessions it was running, because nothing can sign
  for it. Its key stops working on the API server that takes the delete before
  the delete answers, and on every other API server and proxy when
  `node_pool_changed` reaches it.
- Each API replica bounds what one node key can make it do: the two per-node
  budgets under [Rate limit](#rate-limit) cap catalog reads and stats through
  resolve and sources/open, and log batches store in one statement because
  values Postgres would reject are cleared first.
- **The network.** Signing authenticates requests; it does not encrypt them and
  it does not authenticate responses. A node takes its FFmpeg path, its
  directories and its input paths from the core's answers, so anyone who can
  alter traffic between a node and the core can run a program of their choice
  on the node. Run the link on a trusted network or put HTTPS in front of the
  API (`SILO_CORE_URL` with `https://`); the node logs a warning at startup for
  a plain-HTTP core URL that is not loopback. The node's own listener has no
  TLS, so core-to-node traffic, media included, is readable on the path.

## Not in this design

- Core source access: a node that reads source bytes through the core instead
  of a library mount. Planned as follow-up work.
- TLS on the node listener.
- Enrollment flows or node owners other than the cluster administrator.
