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
   │ node credential registry  │  node → API: same key, another label       │ library mount or │
   └───────────────────────────┘  (config, report, logs, health-changed,    │ core access      │
                                   resolve, revoke-recipes, sources/open,   └──────────────────┘
                                   source bytes)
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
  [secret encryption](secret-encryption.md)); a
  [declared node](#declared-nodes) takes its key again at the next API server
  start.
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
  [admin API: node keys](../admin-api.md#node-keys).
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

### Declared nodes

An API server (integrated or `api` mode) can create transcode nodes and their
keys from its deployment configuration, so a declarative deployment needs no
admin step per node. It reads one document from `SILO_TRANSCODE_NODES` (JSON)
or `SILO_TRANSCODE_NODES_FILE` (a YAML or JSON regular file up to 1 MiB);
setting both stops startup, and a blank value counts as unset
(`nodepool.ReadNodeProvisioning`):

```yaml
apiVersion: 1
nodes:
  gpu-1:
    url: http://gpu-1:8090
    key: { fromFile: /run/secrets/gpu-1-node-key }   # or { fromEnv: GPU1_NODE_KEY }
```

A key is always a reference, never inline, so the document can live where
secrets do not. The operator chooses each node's id by the key it writes:
`silo node-key generate --id <n>` prints one, or a secret store can template
`snk1_<n>_` followed by 43 base64url characters of 32 random bytes. A declared
id is at most 2147483646, so the identity sequence always has room. The node
gets the same key in `SILO_NODE_KEY`. Field names match exactly and an unknown
one is refused. Parse errors name the variable and the node, never a key or an
unknown field's name, where a misplaced key could sit.

`nodepool.ProvisionTranscodeNodes` applies the document at startup, before the
first pool and credential loads, in one transaction under the transcode URL
lock and an `EXCLUSIVE` lock on `stream_nodes`. Row locks, writes and
`nextval` wait; plain reads go on; API servers starting together run one after
the other. A weaker mode admits the `SELECT ... FOR UPDATE` that admin edits and
node declarations take without the URL lock, and the two deadlock when both then
write that row.
Every decision is made before anything is written, so a refusal changes
nothing, and any refusal stops startup:

- A declared id with no row is created: transcode, enabled, with the declared
  name and URL and the key, and the identity sequence is moved past it first
  with `ALTER SEQUENCE ... RESTART`, which rolls back with a failed pass (a
  `setval` would not, and the retry would refuse the ids as used).
  Only an id the sequence has never handed out is created, so no id ever names
  two machines. The URL is checked against every row, proxies included, as a
  node's own declaration is.
- A row the deployment manages keeps its ciphertext when the declared key is
  the stored one, and takes the declared key otherwise (rotation, or a stored
  key that no longer decrypts after a `SECRET_KEY` change).
- An unmanaged transcode row is adopted only when it already holds the declared
  key. A proxy row, a keyless row, or one with another key stops startup: a
  declaration never takes over a row it did not create, so an existing node
  comes under the document only with the key it already holds.
- A managed row the document no longer lists is released. `nodes: {}` releases
  every node; an API server with neither variable set leaves them as they are.

`stream_nodes.deployment_managed` records ownership. While it is set, key
issue (`Repository.IssueNodeKey`, in its `UPDATE`) and delete
(`AdminConfigurationStore.Delete`, after the `If-Match` guard, under the row
lock) refuse with `ErrNodeDeploymentManaged`, so every API server refuses
alike. A released node keeps its row and key and is managed in the admin API
again; releasing does not revoke the key, rotating it does. Name and URL are
used only to create a row; afterwards they belong to the node's own
[declaration](#registration) or the administrator. When the pass changed
anything it publishes `node_pool_changed`; a missed event is caught by the
periodic registry and pool reloads.

Every API server needs the same document, or the last to start wins. A
declared key changes only when an API server starts with the new one; until the
node restarts with it, its running streams fail, as after an admin rotation.
`ALTER SEQUENCE` needs the role that owns `stream_nodes`, which every API server
already uses to run migrations. An API server that predates
`deployment_managed` ignores the document and still lets an administrator issue
a key or delete the node.

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
| `SILO_NODE_KEY` | Required (or `SILO_NODE_KEY_FILE` naming a file that holds it; surrounding whitespace is removed from either): the key an administrator issued, or the one the API servers' [declaration](#declared-nodes) references. A malformed key, or both variables set, refuses startup (`transcodeNodeKey`). |
| `PORT` | Listen port, default `8080`. |
| `SILO_NODE_DRAIN_TIMEOUT` | See [node drain](node-drain.md). |
| `SILO_NODE_NAME`, `SILO_NODE_URL`, `SILO_NODE_GROUP`, `SILO_NODE_MAX_TRANSCODES`, `SILO_NODE_SOURCE_ACCESS`, `SILO_NODE_HW_ACCEL`, `SILO_NODE_HW_DEVICE`, `SILO_NODE_DOWNLOAD_ARTIFACT_DIR`, `SILO_NODE_DOWNLOAD_ARTIFACT_MAX_BYTES` | Optional. The node's declaration of its own row; see [Registration](#registration). A bad value refuses startup, naming the variable (`nodedeclare.FromEnv`). |
| `DATABASE_URL`, `REDIS_URL`, `SECRET_KEY` (and their `_FILE` variants), `NODE_URL`, `NODE_NAME` | Ignored, with one warning, so a shared env file keeps working. `NODE_URL` and `NODE_NAME` stay ignored although the node declares a URL and name: an env file shared with a proxy sets them, and honoring them would rewrite rows on upgrade. |

Before its first configuration read the node sends its declaration
([Registration](#registration)), so the configuration it loads already
reflects it. The node then reads its configuration, retrying with backoff from one second to 30
seconds until a read succeeds or it is signaled, and only then opens its
listener. A configuration that names another node id than the key is refused
(`Client.FetchConfig`). Each call to the core has a 10-second timeout
(`nodecontrol.Client`).

## Registration

`PUT /api/internal/nodes/self/registration` (`nodecontrol.Server.HandleRegistration`)
carries the configuration the node declares from its environment,
`nodecontrol.Registration`. Every field is optional and absent means not
declared:

```json
{
  "name": "gpu-1",
  "url": "http://10.0.0.5:8082",
  "group": "rack-a",
  "max_jobs": 0,
  "source_access": "core",
  "hw_accel_override": "vaapi",
  "hw_device_override": "/dev/dri/renderD128",
  "download_artifact_dir_override": "/srv/artifacts",
  "download_artifact_max_bytes_override": 0
}
```

The core applies it to the row the key names, never one named in the body,
through the admin node service (`NodeHandler.DeclareNode`, then
`nodepool.AdminConfigurationStore.Declare`): each field is validated as an
administrator update is, a changed URL must be free among transcode nodes
under the canonical-URL lock, the fields are written, and `env_managed`
becomes exactly the declared field names in one transaction. A changed row
then reaches every API server's registry and pools and every proxy's registry
as an administrator's edit does; the core does not ask the node to reload,
since it reads its configuration next. An empty body unlocks every field and
changes nothing else, which is what a node with no declaration variables sends.
A body with a field the core does not know is refused whole, so a misspelled
field cannot read as an empty declaration.

The table is for a node that declares something. A node that declares nothing
sends its empty body once and starts whatever the answer: a failure is tried
again every 30 seconds in the background, a missing route included, since a
gateway that briefly refused the route looks the same. It logs once, at info
level, that the fields stay read-only until the core takes it.

The core also answers `404` when the node's row was deleted between
authentication and the write. The node cannot tell that from a missing route
and treats both alike; its next configuration read then gets `401`.

| Answer | Body | Node |
|---|---|---|
| `200` | `{"node": {"id", "name", "url"}, "env_managed": [...], "changed": bool}` | Continues to the configuration read. |
| `422` | `{"reason": "invalid", "field", "detail"}` | Exits, naming the variable for `field`; `field` is empty for a body the core could not read. |
| `409` | `{"reason": "url_taken", "field": "url", "detail"}` | Exits, naming `SILO_NODE_URL`. |
| `401`, `429`, `5xx` other than `501`, no answer | text | Retries with the configuration read's backoff, 1 to 30 seconds. |
| `403`, `404`, `405`, `501` (a core without the route, or a gateway in front of it that blocks the request) | text | Starts, warns once, and offers the declaration every 30 seconds until it is accepted, then rereads its configuration. |

The route has its own per-node budget on each API replica, far below the
background routes': a burst of six, refilled one every ten seconds, and a 16
KiB body. A changed declaration commits a configuration revision and makes
every API server reload its node pools, so a node cannot repeat it quickly.
Changing a variable takes a restart.

A node that declared something sends its declaration again every minute while
it runs (`nodeconfig.MaintainDeclaration`, `DeclarationRefreshInterval`), so a
row or an `env_managed` list changed on the core, by SQL or otherwise, is put
back within a minute without a restart. `changed` in the answer says whether
it was, and the node logs it. An unchanged declaration commits no
configuration revision, moves no pool generation, publishes no
`node_pool_changed` and asks no server to reload: it costs one transaction
that rewrites the node's row with its own values and leaves `env_managed`
alone. One send a minute is a sixth of the budget's refill, so restarts keep
the burst. A refusal on a re-send never stops the node: it logs it once (again
only after a later send succeeds) and keeps running with its own values. A
node that started against a core without the route keeps offering it every 30
seconds, as before, and moves to the one-minute cadence once it is taken.

## What the node enforces

The row is what the core's planner and the node editor read, and whoever runs
the core can change it. A node can be run by someone else (a rented or lent
GPU), so the node itself holds the values it acts on to what its environment
says, whatever its row or its configuration document says:

| Variable | Enforced by |
|---|---|
| `SILO_NODE_MAX_TRANSCODES` | The node: it refuses new work past the cap ([worker protocol](worker-http-protocol.md)). The planner also reads the row's `max_jobs`. |
| `SILO_NODE_HW_ACCEL` | The node: `nodeconfig.Remote` uses it over the document's `hw_accel`, and a start runs with it whatever backend the request names (`Server.jobHWAccel`). The core may still ask for `none`. Only a node that declares `auto` lets a hardware tone map name its backend, chosen from the node's own capability report; a node that declares a backend runs that one for a hardware tone map too. |
| `SILO_NODE_HW_DEVICE` | The node: used over the document's `hw_device`. |
| `SILO_NODE_SOURCE_ACCESS` | The node: `Remote.SourceAccess` returns it over the document's. With `local` the node never fetches source bytes over HTTP; with `core` it never opens a local path. The core still grants and serves sources either way. |
| `SILO_NODE_DOWNLOAD_ARTIFACT_DIR` | The node: used over the document's `artifact_dir`. |
| `SILO_NODE_DOWNLOAD_ARTIFACT_MAX_BYTES` | The node, as a ceiling: it refuses a new preparation once its last measurement of the directory reaches the budget. Clean-up and the core's own budget check still read the row. |
| `SILO_NODE_GROUP` | The row only. A group is placement on the main server's side; the node does not act on it. |
| `SILO_NODE_NAME`, `SILO_NODE_URL` | The row only. They are the node's identity and address in the main server's pools. The node cannot stop the main server from naming or addressing it otherwise; the minute re-send puts them back. |

The node never sees which planner, if any, sends it work, so the row-only
fields are as strong as the main server's operator allows. The values the
node enforces hold even against a main server that ignores the row.

## Configuration

`GET /api/internal/nodes/self/config` (`nodecontrol.Server.HandleConfig`)
returns the calling node's document, `nodecontrol.NodeConfig`:

```json
{
  "revision": "<sha256 of the rest of the document>",
  "node": {"id": 7, "name": "gpu-1", "url": "http://gpu-1:8080"},
  "drain_requested": false,
  "source_access": "local",
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
  and quiet filter apply to new work without a restart. `source_access` is not
  part of the `Config`: the source opener reads it at every open
  (`Remote.SourceAccess`), so it applies to jobs opened after the change, and a
  change to it alone keeps the configuration pointer. The transcode and
  artifact directories, log format and capture level apply when the node
  restarts.

The core pushes `/admin/reload-config` (never `/admin/force-reload`), best
effort, after a drain request or resume and after an admin change to the node's
URL, acceleration overrides, download overrides or source access
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

## macOS node build

Docker on a Mac runs Linux in a VM with no VideoToolbox, so a Mac transcodes
with a native node beside a server in Docker. The base image (and the omnibus
image built on it) carries that node at
`/usr/share/silo/nodes/silo-darwin-arm64`, from the `node_darwin` stage of the
root `Dockerfile`: `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64` with
`-tags silo_node`, cross-compiled on the build host and signed ad hoc by the Go
linker. The binary is the image's own revision, so a node copied from an image
matches the server that runs it. `make build-node-darwin` runs the same build;
the Go vet CI job runs it and vets the node build for darwin, which no Linux
build compiles.

- **The `silo_node` tag** leaves libvips and the web app out.
  `internal/imageutil` keeps the declarations both builds share in
  `shared.go`, and the node build replaces the functions with
  `imageutil_node.go`, the same exported API with every function failing.
  `web/embed_node.go` replaces `web/embed.go` with an empty `DistFS`. A node
  processes no images: its one image job, `chapterthumbs.ExtractFrame`, is
  FFmpeg's. The binary links only system frameworks, so no Homebrew library
  can stop it from loading. A new exported `imageutil` function needs a stub,
  and a new cgo import the node links fails the build.
- **Node builds run only as a node.** `main` refuses every subcommand but
  `silo node`, and, after the transcode branch, any run without
  `MODE=transcode` (`nodebuild.Node`, `internal/nodebuild/node.go`).
- **Version.** `silo node version` and the installer name the build as Admin
  shows the server's and each node's: the build number and the display
  revision.
- **Installer.** `silo node install` and `silo node uninstall`
  (`internal/nodeinstall/node_command.go`; the install, swap and file work
  in `node_install_darwin.go`, launchctl in `launchd_darwin.go`) manage one
  per-user LaunchAgent,
  `org.siloserver.node`, and nothing system-wide. The cgo darwin build refuses
  to install itself, since it loads Homebrew libraries, and other platforms
  refuse both commands.

| File | |
|---|---|
| `~/Library/Application Support/Silo/node/silo` | The binary. |
| `~/Library/Application Support/Silo/node/node.env` | Mode 0600. The installer manages `MODE=transcode`, `SILO_CORE_URL`, `SILO_NODE_KEY_FILE` naming `node-key`, and `PORT` (default 8100, since Docker Desktop often publishes 8080). Any other variable in the file, such as one from [Startup and environment](#startup-and-environment), is kept across reruns, except `SILO_NODE_KEY`, which would clash with the key file. |
| `~/Library/Application Support/Silo/node/node-key` | Mode 0600: the node key. |
| `~/Library/LaunchAgents/org.siloserver.node.plist` | Runs the binary with `-env` and `node.env`, `RunAtLoad`, `KeepAlive` with `SuccessfulExit` false (launchd restarts the node after a crash or a non-zero exit, not after the clean exit that ends a drain), and an `ExitTimeOut` of the drain timeout `node.env` sets plus a minute (660 seconds with the default). Current macOS caps `ExitTimeOut` at 60 seconds, so a logout or shutdown stops the node's streams a minute after SIGTERM; the installer's own stop does not depend on it. |
| `~/Library/Application Support/Silo/node-install.lock` | Mode 0600, in a 0700 folder: the lock every install and uninstall holds. It sits outside the node's folder, which uninstall removes while it holds the lock, and is never removed, so two runs always lock the same file. |
| `~/Library/Logs/Silo/node.log` | The node's output. Uninstall keeps it. |

An install is also the upgrade: a rerun takes every value its flags leave out
from the existing install, and replaces the binary unless it runs from the
installed one. The key is never a command-line argument, which other users of
the Mac could read: a first install, and a rerun with `--new-key`, ask for it at
a hidden prompt, or read it from standard input when that isn't a terminal. It checks everything it can before it writes anything: the
flags and key (`--server` must be a plain `http` or `https` URL: no userinfo,
which would go out as Basic auth, and no query or fragment, which would swallow
the paths joined onto it), a `SILO_NODE_DRAIN_TIMEOUT` the node would ignore
(`transcodenode.ParseDrainTimeout`, which the node parses it with), FFmpeg (Homebrew's `ffmpeg-full`, as `playback`'s discovery
finds it), the server's `GET /api/v1/ready` (joined onto the URL's path, so a
server behind a path prefix works; a `200` within five seconds, no redirects,
so a server that is restarting or migrating stops the run), and the
port (nothing else may answer on it). It then takes the lock, reads the
install again under it, stages the new files beside the old ones, stops the
old job, swaps the files in, bootstraps the job, and waits up to 60 seconds
for the node's `GET /api/v1/health`.

The stop drains before it boots the job out. `launchctl bootout` returns at
once and launchd kills the job a minute after it, whatever `ExitTimeOut` asks,
so the installer sends SIGTERM itself (`launchctl kill SIGTERM`), polls
`launchctl print` until the signaled pid is gone, and only then boots the job
out and waits for launchd to let it go. A `launchctl print` error while it
waits is retried, not taken for the exit. A job an older plist keeps alive
unconditionally restarts under a new pid; the signaled pid's exit still ends
the drain. Each wait is bounded by the running node's drain timeout plus two
minutes: the longer of the one `node.env` sets and the one the installed
plist's `ExitTimeOut` recorded, since the node read `node.env` when it started
and the file may have changed since. A node still running at that deadline is booted out anyway, and
the installer says it is stopping a node that is still finishing streams, so a
stop never leaves the job stopped without the install's restart or rollback
running. Uninstall stops the job the same way.

The swap moves each file to `<file>.old` and keeps it until the new node is
ready; a `<file>.old` already there is the last working file of a run that
stopped mid-swap, so it stays the backup and the unverified file in its place
is dropped. For the same reason, an install reads its stored settings from
`node.env.old` and `node-key.old` when `node.env` or `node-key` is missing, so a
rerun after such a run keeps the previous server, port, key and other
variables instead of starting over as a first install.

The node is ready when that answers `200` with a `node_control_version`, which
neither the API server's health nor a proxy's carries, and without
`key_rejected`. A drained node is ready: an administrator's drain outlasts the
restart, and the installer says so. The node opens its port only once the core
accepted its key, so a ready node also shows that the key and core URL work.

When the new job does not start or become ready, or the run is interrupted
after the files were swapped, the previous files come back, with a context of
their own, and the previous job is bootstrapped only if launchd had it before
the install, so a node that was stopped stays stopped. A rollback first stops
the new job, and restores nothing until launchd confirms it is gone: when the
stop fails, every file and backup stays where it is and the installer reports
the recovery as incomplete, naming the backups. The installer exits
non-zero with the end of the log. A stop interrupted, or failed in launchctl, after its signal went out
waits until launchd lets the old job go and bootstraps its plist again, so the
old node is not left stopped. Only `launchctl print`'s "not found" status
(113) counts as a job launchd does not have. A bootout that answers
`EINPROGRESS` (36), a job still shutting down, went out, and the installer
waits for the job to go as it does after a bootout that timed out. Each
launchctl call has its own timeout.

## Rate limit

Each API replica gives each node three in-memory token buckets and answers
`429` with `Retry-After: 1` past any of them:

- **Background** (`nodecontrol.(*Server).LimitNode`): 20 requests a second with
  bursts of 60 across config, registration, report, logs, health-changed and revoke-recipes.
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

The source bytes route (see [Core source access](#core-source-access)) is under
neither budget: each request streams for as long as FFmpeg reads, and a node
keeps several open per job. The node's signature, the grant and the grant's
whole life bound it instead.

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
  egress runs the remux on the API server. Of the execution preferences, only
  `playback.routing.remux_execution=prefer_transcode` (the default) ranks the
  transcode node above the proxy itself; `prefer_worker` and `worker_only`
  rank both as workers, and the proxy's own execution wins on fewer hops
  (`noderouting.rank`), which a proxy without the library mount cannot run. A valid token is required. Deny checks on the
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
  the node then ends the job it held when the release arrived, or a rebuild
  published while the release waited for the lifecycle lock, but not a job a
  start published then.
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

- A rebuild does not notice a file replaced in place. A stored recipe carries
  no file revision, and resolve mints the rebuild's grant for whatever file the
  catalog row's path holds now (`grantResolution`), so when a file is replaced
  under the same catalog row, a rebuild of an existing session (after a node
  restart, a withdrawn source, or a move) opens the new file with the recipe
  made for the old one: its track indexes, durations and keyframe plan. This
  applies to both source modes.
- A Jellyfin-compatible audio track switch on a remote session restarts the
  transcode on the node it already runs on (`restartCompatTranscodeForAudioSelection`'s call to
  `startRemoteTranscode`). When that node has core source access and the
  session burns in a text subtitle whose track cannot be staged, the node
  refuses the start with `409` (`playback.ErrRelayedSubtitleBurnIn`) and the
  switch fails with an error; it does not move to a `local` node or the API
  server as a fresh start would. The dead-node move
  (`moveCompatTranscodeOffNode`) cannot take it over, because it rebuilds from
  the stored recipe, which still names the previous audio track.
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
- **Payload.** `{"v": 2, "node", "kind": "file" | "theme", "id", "path",
  "size", "mtime_ns", "iat", "exp"}`. `iat` is when the grant was first minted;
  renewal keeps it. `path` is the exact fspath-encoded catalog path,
  so no catalog read is needed to use it, and every stat goes through
  `internal/fspath`. The node can read the payload; it is given the path anyway.
  A version 1 payload, which has no `iat` and which builds between the two
  transcode node changes minted, still verifies: it is read as version 2 with
  `iat` one lifetime before its expiry, so a job holding one survives the
  update.
- **Revision** is `(kind, id, size, modification time)`, without device, inode
  or change time, so every host computes the same revision of a shared file.
  `mtime_ns` is what the minting stat returned; revisions compare it at
  microseconds, the catalog's precision (`nodesource.SameModTime`,
  `models.NormalizeFileModifiedAt`). The entity tag is
  `"<kind>-<id>-<size>-<mtime in Unix microseconds>"`.
- **Life.** One hour (`GrantLifetime`). The node renews every 20 minutes
  (`RenewInterval`) while the input is open; renewal keeps the revision, so a
  job never moves to another version of its source. A grant's whole life is
  capped at 24 hours from `iat` (`MaxGrantAge`): renewal never signs an expiry
  past it, and `Verify` refuses a grant that old, so open, renewal and byte
  reads answer `403` and the job fails like any job whose renewal is refused.
  24 hours is the longest a job legitimately holds one source: a paused player
  loses its session to the ten-minute idle reap and the rebuild gets a fresh
  grant, a continuous playback session cannot outlive its stream token's
  absolute life (`playback.MaxTokenTTL`), and the slowest job, a software
  re-encode of a four-hour film for download preparation at a quarter of real
  time, takes 16 hours.
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
| `403` | The grant does not verify, has expired or reached its whole life (`MaxGrantAge`), or names another node. |
| `410` | The catalog no longer has the source at exactly the granted path (a media file by id and not missing, or a theme in an enabled library), or the file is gone or not a regular file. |
| `412` | The file's size or modification time differs from the grant's revision. |
| `503` | Transient: no grant key, the catalog cannot be read, or the file could not be examined (an I/O error, a stale mount). |

Each open and renewal reads the catalog once and stats the file once, then
signs a grant valid for one lifetime from that moment. While a node's renewals
succeed, its last check is at most one renewal interval old. When they fail
transiently, the grant it holds stays valid until it expires, so the check can
be up to one grant lifetime old, plus the time the core spent on that check.

On the node, `nodesource.Opener` exchanges the grant through
`nodecontrol.Client.OpenSource`. With local source access it stats the approved
path on its own mount and requires a regular file of the same size; with core
access it serves the source on the loopback relay instead (see
[Core source access](#core-source-access)). A mismatch is a
`nodesource.MountMismatchError` naming both sides. The node does not compare
the modification time: mounts of storage that keeps none each report a
placeholder of their own, and network filesystems round times differently, so
it tells two mounts apart more often than two files. A difference is logged,
as a warning the first time and at debug afterwards, and the source reports
both times (`ModTime`, `InputModTime`). Two things a job carries hold a file
time the API server recorded, and playback compares each with a stat of the
node's input, so the node restates them at its own mount's time when they name
the granted revision. A session's planned subtitle version is restated, and
otherwise made to match no file (`transcodenode.renditionSourceOnNode`), so
on-demand subtitle segments still match the node's file. A tone-mapped job's
catalog revision is restated (`transcodenode.toneMapRevisionOnNode`), so its
recheck (`tonemap.SourceRevision.ValidatePath`) passes on the node's file; its
size and content hash are checked either way, and a catalog revision the core
did not grant is refused. A job route answers a missing or refused grant with `400`, a mismatch with
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

**Operator requirement.** Every API server must see each library file at the
same path with the same size and the same modification time to the
microsecond: a grant minted on one is checked on another, and a difference
answers `412`. Every transcode node with local source access must see each
file at the same path with the same size, as the shipped Compose file arranges
by mounting the library at the same container path; a file that is missing or
of another size makes the node refuse the job with `409`. A node's mount may
date files differently from the API servers'. A node with core source access
needs no library mount.

## Core source access

`source_access` is a per-node admin setting, `local` (the default) or `core`,
stored in `stream_nodes.source_access` (a check constraint keeps a proxy at
`local`), compared by the configuration trigger, and delivered in the
configuration document. Dispatch never branches on it: the core mints the same
grant either way. The node's `Opener` reads the mode at each open
(`Opener.EnableCoreAccess`, `nodeconfig.Remote.SourceAccess`), so jobs opened
after a change use the new mode and running jobs keep theirs.

- **local** reads the approved catalog path through the node's library mount.
- **core** reads the bytes over HTTP from the core through a loopback relay on
  the node. The node needs no library mount; every byte it reads passes
  through the API servers.

### Source bytes route

`GET` and `HEAD /api/internal/nodes/sources` (`nodecontrol.Server.HandleSource`),
node-signed, with the grant in the `X-Silo-Source-Grant` header, which the
node-to-core signature covers. A header, not a path segment, so access logs on
the way never record it.

1. The grant must verify, be unexpired, be younger than `MaxGrantAge` and name
   the calling node (`403`). There is no catalog query: the catalog is checked
   when the grant is minted, from the row its caller read, and again at open
   and at every renewal. A request needs an unexpired grant, so the last check
   behind it is up to one grant lifetime old (see [sources/open](#sourcesopen)),
   plus, for a grant never renewed, the time between the caller's read and the
   mint.
   The core does not cut a response it is already sending when the source is
   withdrawn, and keeps serving new requests that carry an unexpired grant. A
   node that renews ends the source at its next renewal, which the core refuses
   with `410`, and that interrupts its reads in flight. The grant's expiry is
   the core's own bound on a node that does not renew.
2. The core opens the granted path through `internal/fspath` without blocking
   on a FIFO, `fstat`s the descriptor, and compares the size and the
   modification time at microseconds with the grant: `410` when gone or not a
   regular file, `412` when changed, and `503` when the file could not be
   opened or examined (an I/O error, a stale mount), which the node retries.
3. It serves from that descriptor with `http.ServeContent`, so a file replaced
   after the check is never served. Single and suffix ranges and `If-Match` on
   the grant's entity tag are honored; several ranges answer `416`.
   `If-None-Match`, `If-Modified-Since`, `If-Unmodified-Since` and `If-Range`
   are dropped: a condition may only narrow the answer to the granted
   revision. The answer is identity-encoded (the node control prefix is outside
   compression), carries `Accept-Ranges: bytes`, the grant's `ETag` and
   `Cache-Control: no-store`, and keeps the sendfile path. Its write deadline
   rolls with progress (`httpstream.RollingDeadlineWriter`) instead of the
   server's absolute write timeout.
4. It sits outside both per-node request budgets and outside the request and
   activity logs. Each API replica serves one node at most 512 of these
   responses at once (`maxSourceStreamsPerNode`), counted from the request's
   arrival to the end of its response; past that it answers `429` with
   `Retry-After: 1`, which the node's fetch loop backs off from and retries.
   Legitimate use stays well below it: a node keeps at most 128 reader fetches
   in flight, and each FFmpeg process holds one relayed read, two while it
   seeks, so 32 jobs and 16 trickplay, chapter and sampling runs hold about
   224. The cap bounds the descriptors and goroutines one node key can hold on
   a replica.
5. Every answer, refusals and `416` included, carries `Cache-Control:
   no-store`. A `416` carries the grant's `ETag` and `Content-Range: bytes
   */size`; other refusals carry no `ETag`.
6. When graceful shutdown begins (main cancels the app context, which the
   route receives as `nodecontrol.Options.Lifetime`, before it shuts the
   listener down), every response in flight is cut and a request arriving
   after that answers `503`. The node's relay resumes the read from its offset
   against another replica or after the restart, so a long read does not hold
   the shutdown.

**Metrics.** `silo_node_source_streams_active{node}`,
`silo_node_source_bytes_sent_total{node}` and
`silo_node_source_stream_ends_total{outcome}` (`completed`, `client_gone`,
`stalled_reap`, `shutdown`, `forbidden`, `gone`, `changed`,
`range_not_satisfiable`, `throttled`, `unavailable`), in the style of the
direct-play metrics. The bytes counter advances as the body is sent, at most
4 MiB at a time (each sendfile slice), so its rate is the bandwidth in use
during a long transcode. It is how an operator sizes API bandwidth before
moving nodes to `core`.

A copy remux reads far ahead of the player. FFmpeg copies as fast as its input
allows and nothing paces it, so a copy session pulls the rest of the file from
the API server within seconds of each start or seek. A throttled transcode
reads about as far ahead as its throttle allows. Size API bandwidth for those
bursts, not for the media bitrate.

### Loopback relay

`internal/nodesource` (`relay.go`, `fetch.go`) serves core-access sources to
FFmpeg and to the node's Go readers.

- **One URL per source revision.** The listener is `127.0.0.1:0`, started by
  the first core-access source and kept for the process. A source's URL is
  `http://127.0.0.1:<port>/src/<32 hex>`, the token an HMAC of the path and the
  entity tag under a key drawn at process start. It is the job's input path, so
  every job on one revision gets the same URL, and the caches keyed by input
  path (keyframe indexes and their rejections, subtitle indexes and cue
  windows, staged burn-in reads, the DV RPU probe, copy-seek anchors, the
  automatic transcode fallback) hit across sessions as they do on a library
  path. The jobs share one reference-counted relay entry with one renewal loop;
  a job that opens the revision offers its fresher grant to the entry, so jobs
  that share a revision run on the newest grant the node holds for it, and the
  whole-life cap applies to that grant rather than to each job. The URL
  resolves while at least one job holds it; after the last releases it, it
  answers `404` until a job opens the revision again.
- **Serving FFmpeg.** `Range` is forwarded on `GET` only. The relay always sends
  `Accept-Ranges: bytes` and an exact `Content-Length`, `Content-Range` on
  `206`, and `Content-Range: bytes */size` on `416`. `HEAD` is answered from
  the revision.
- **Upstream.** One primitive (`fetchRange`) serves FFmpeg and the readers.
  Each request goes through `nodecontrol.Client.ReadSource`: signed, carrying
  `If-Match` and `Accept-Encoding: identity`, over a client with no proxy, no
  compression, no redirects, a ten-second response-header timeout and pooled
  connections. Each answer must be `206` with the revision's `ETag`, identity
  encoding, and exactly the requested `Content-Range` and length.
- **Close.** The last job's release ends the entry: renewal stops, the URL
  stops resolving, reads in flight end, and a response FFmpeg's back pressure
  holds in a write is interrupted (`SetWriteDeadline(now)`).

### Resume, stall and idle

| Upstream answer | Effect |
|---|---|
| Network error, invalid answer, `401`, `429`, `5xx` | Transient: back off from 250 ms, doubling to 4 seconds, and give up after 30 seconds spent waiting on the core without a byte. Time the caller spends not reading does not count. |
| `400`, `403`, `404`, `410`, `412` | Fatal: the read fails and the entry ends, so every job reading that revision ends. |
| `416` | Only that read fails; FFmpeg gets `416`. |
| A body that delivers nothing for 10 seconds | The attempt is cancelled and resumed from its offset. |
| A body the caller left unread for over 10 seconds | Dropped; the next read starts a fresh request. |

A cut body resumes from where it stopped. When the relay cannot complete a body
it aborts the loopback connection instead of ending it cleanly, so FFmpeg sees
a short read, not a short file.

### Go readers and fspath

Main reaches source files through `internal/fspath`, so relayed inputs work at
that layer: `nodesource` registers itself (`fspath.RegisterRelayedSources`) and
`internal/fspath` does not depend on it.

- `fspath.Stat`, `StatContext` and `Lstat` describe a relay URL as a read-only
  regular file of the granted size and modification time, the nanoseconds the
  core's stat returned, so a version the core recorded from its own stat
  matches. A released URL is a read error, never a missing file.
- `fspath.OpenReader` returns a `Reader` (`ReadAt`, `Close`, `Stat`) for a
  library path (the file) or a relay URL. The tone-map OpenSubtitles hash,
  `keyframes.Load`, `internal/embeddedsub` (indexes, packet reads, burn-in
  staging, rendition windows) and the trickplay Cues read use it. Through
  `keyframes.Load`, a video copy into fMP4 segments gets its keyframe-planned
  VOD playlist on a mountless node as it does locally; a copy into MPEG-TS
  segments keeps FFmpeg's growing playlist in either mode
  (`planCopySegments`).
  `fspath.Open` of a relay URL is `fspath.ErrRelayed`.
- **Reader.** Each revision's entry has a cache of 64 KiB pages, 64 of them
  (4 MiB), shared by every reader of the revision. Reads fetch outside any lock
  and a page another reader is fetching is shared, not fetched twice. A read
  larger than a page fetches directly in ranges of at most 1 MiB, uncached. One
  reader fetches at most twice the source's size (64 GiB at most). The process
  keeps at most 128 reader fetches in flight: burn-in staging keeps 64, a
  rendition window up to 16, and tone-map hashing, keyframe index loads and
  concurrent trickplay Cues reads a few each, so a playback start is not queued
  behind background work. Every read failure is an `*fs.PathError` naming
  `source <etag>`.

### FFmpeg argument guard

`fspath.CommandContext`, which every FFmpeg and ffprobe launch on media goes
through, puts `-protocol_whitelist http,tcp`, `-format_whitelist` of the
admitted demuxers and an empty `-http_proxy` before every relayed input: before
its `-i`, or for ffprobe before a positional URL too. FFmpeg's `http` protocol
otherwise sends loopback requests through the proxy in `http_proxy` as well,
and the protocol whitelist does not stop it, so on a host with a proxy set
every relayed read would fail and leave its URL in the proxy's logs. A launch
with a relayed input also runs without the `http_proxy`, `https_proxy`,
`all_proxy` and `no_proxy` variables (either case); every other launch
inherits the environment unchanged. The admitted demuxers are those of the containers
the scanner catalogs (`internal/mediacontainer`, which the scanner's extension
lists come from), `m4v` included; playlist, concat, image-sequence, HLS and DASH
demuxers, which open further URLs or files, are not. A relay URL is restricted
whether or not its source is still open. `TestEveryMediaLaunchGoesThroughCommandContext`
(`internal/fspath`) fails when a new process launch bypasses it without a
recorded reason.

### Relayed concat

Trickplay sampling reads its input through an ffconcat list on stdin
([media sampling](media-sampling.md)), which the argv guard cannot see. With a
relayed input the list opens with `-protocol_whitelist http,tcp,pipe
-format_whitelist concat -f concat -safe 0`, and each entry names the bare URL
followed by `option protocol_whitelist http,tcp`, `option http_proxy direct`
(an ffconcat option cannot be empty, and FFmpeg uses a proxy only for a value
starting with `http://`) and `option format_whitelist` of the admitted
demuxers, which replace what the entry inherits from the list.
`mediasample.Capabilities` checks that form against a URL nothing listens on
(`checkRelayedConcatSamples`). An FFmpeg that cannot read it refuses a relayed
sample run as `unsupported`, which is not permanent, so the caller moves the
work.

No other ffconcat list may name a relayed input: `fspath.FFConcatFileURL`
refuses one with `fspath.ErrRelayed`, and the Samples list names it itself
(`mediasample.concatPath`). The progressive remux's IDR primer for macOS
Firefox HEVC resumes (see [playback protocol v3](playback-protocol-v3.md))
takes its video from such a list, so a relayed input gets the plain seeked
copy without the primer.

### Text subtitle burn-in

A text burn-in stages its track from the file's index
(`stageBurnInSubtitle`), which works through the reader and the guarded FFmpeg
and ffprobe calls. When staging is not possible, a local input falls back to
handing libass the media file through an alias. That fallback is refused for a
relayed input (`playback.ErrRelayedSubtitleBurnIn`): FFmpeg's `subtitles=`
filter opens its file with no protocol whitelist and reads all of it, which
over the relay would pull the whole source through the core. The node answers
the start `409`, so it runs on a node that reads the file locally or on the API
server. Bitmap burn-in is unaffected.

### Redaction

A relay URL is a bearer capability on the node host and never reaches a log.
`nodesource.RedactRelayURLs` replaces each with `source <etag>`, or
`source (released)`, and is applied by the node's slog handler
(`nodesource.RedactingHandler`, installed outermost, so stderr, telemetry and
the operational log shipped to the core through `opslog.Ship` all see redacted
records), the FFmpeg argv logs, the FFmpeg log sink, and the error bodies of
the trickplay, media sample and chapter routes.

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
| Mount mismatch (the node's mount lacks the file, or shows another size) | The node refuses the job with `409` instead of reading other bytes, and the caller runs it elsewhere. A file that changed on the API server between minting and opening gets `412` from the core. |
| Node mount dates a file differently from the API server's | The job runs on the node's file, matched by path and size. The node logs both times. |
| Core unreachable under 30 seconds, core access | Source reads retry and resume; FFmpeg waits. Renewal retries. |
| Core unreachable 30 seconds or more, core access | The active read fails and FFmpeg exits, as with a lost network mount; the source stays open, and a local-access job keeps reading its mount. A viewer's next request for a segment FFmpeg did not write waits, then restarts FFmpeg at that segment on the same source (`restartSegmentLocked`): once the core answers again, the player's retry recovers; until then the request answers `404`. Renewal still survives about 40 minutes. |
| Rolling API restart, or reads spread over replicas, core access | Grants and revisions do not depend on the replica. A stopping API server cuts its source responses when graceful shutdown begins, and the relay resumes each from its offset against any replica. |
| File replaced or removed, core access | The next read, open or renewal answers `412` or `410`, and every job on that revision ends: the node forgets its sessions (`watchSessionSource`). The viewer's next request rebuilds through resolve. For a removed file, or one the catalog dropped, resolve answers `source_changed`, which the node answers `404` (`writeReconstructFailure`); retries get the same, and the client must start playback again. For a file replaced under the same catalog row, the rebuild opens the new file with the old recipe (see the known limit under [Recipe lifecycle](#recipe-lifecycle)). |
| Node dies, proxy egress | The proxy cannot reach the node and answers `502`. A native client's transcode moves on the client's recovery replan; see [node failure detection](node-failure-detection.md). A Jellyfin-compatible transcode moves on the proxy's move request ([transcode node lost mid-stream](restart-resilient-playback.md#transcode-node-lost-mid-stream)). |
| Grant reaches `MaxGrantAge` | Renewal and reads answer `403`; the job fails like any refused renewal. |
| Relayed text burn-in cannot be staged | The node answers the start `409` and the start runs elsewhere. |
| Node binary from before this design | Health has no `node_control_version`, so the node is unhealthy and never placed; it could not verify the core's signatures anyway. |
| Current node, core from before node declarations | The registration route answers `404`; the node starts on its row as the administrator left it and offers its declaration every 30 seconds (see [Registration](#registration)). |
| Current node, core from before this design | The node's first configuration read never succeeds, so it never opens its listener and the core sees it as unreachable. |

## Security properties

- A node holds no secret that can mint login, profile or stream tokens or
  source grants, and no database or Redis credential.
- A stolen node key lets its holder act as that node towards the core (read
  its configuration, post reports and logs under its id, resolve transports
  bound to it, open and renew grants issued to it, read the bytes those grants
  name, revoke its recipes) and as
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
- **Core source access.** A stolen node key plus a grant issued to that node
  reads that one file revision's bytes from the core, without a catalog check,
  until the grant expires (one hour after its last renewal) and never past 24
  hours after it was first minted; a read that started before expiry can run
  past it. Renewal re-checks the catalog, so a file removed from the catalog
  stops renewing. The key alone reads nothing: no grant, no bytes.
- **Loopback relay URLs** are bearer capabilities on the node host: anyone who
  can reach `127.0.0.1` there can read an open source by its URL, and FFmpeg's
  argv in `/proc/<pid>/cmdline` shows it. The design assumes the node host runs
  no untrusted local users or processes. The URLs are never logged.
- **The network.** Signing authenticates requests; it does not encrypt them and
  it does not authenticate responses. A node takes its FFmpeg path, its
  directories and its input paths from the core's answers, so anyone who can
  alter traffic between a node and the core can run a program of their choice
  on the node. Run the link on a trusted network or put HTTPS in front of the
  API (`SILO_CORE_URL` with `https://`); the node logs a warning at startup for
  a plain-HTTP core URL that is not loopback. The node's own listener has no
  TLS, so core-to-node traffic, media included, is readable on the path.

## Not in this design

- TLS on the node listener.
- Enrollment flows or node owners other than the cluster administrator.
