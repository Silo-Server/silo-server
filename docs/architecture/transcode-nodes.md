# Transcode nodes

A transcode node runs FFmpeg for the core and holds nothing that can mint a
token or read the catalog: no Postgres, no Redis, no `auth.jwt_secret`, no
`SECRET_KEY`. It starts with the core's URL and a key of its own, takes its
configuration from the core, and reads each job's source either through its
own mount of the library or over HTTP from the core. The core (the API
servers, and proxies where they relay) is the only verifier: the node never
checks a stream token, a deny marker or the catalog.

This document is the authority for that design. The wire descriptions are in
the `x-silo-worker-protocols` extension of `contracts/api/v2/openapi.json`:
the node's own routes under listener `transcode_node`, and the routes it calls
on the core under listener `api` with auth class `node_signed`. Core-to-worker
signing is in [worker HTTP contracts](worker-http-protocol.md#authentication);
drain is in [node drain](node-drain.md). Paths are repository-relative.

## Model

There is one transcode node type, started with `MODE=transcode`
(`cmd/silo/transcode_node.go`). Proxies are unchanged: they keep the database,
Redis and the shared secret, and gain only the node credential registry so
they can sign their relays to transcode nodes.

```text
            client
              │  (unchanged: the API, jellycompat or a proxy relays every response)
     ┌────────▼─────────┐   core → node: HMAC with the node's key       ┌──────────────┐
     │ API / proxy      │ ────────────────────────────────────────────▶ │ transcode    │
     │ (DB, Redis,      │                                               │ node         │
     │  shared secret)  │ ◀──────────────────────────────────────────── │ (core URL +  │
     └──────────────────┘   node → core: HMAC with the node's key,      │  node key)   │
                            another label: config, resolve, sources,    └──────────────┘
                            reports, logs, health changes
```

Everything a node used to do against the database or Redis is now either done
by the core at the operation that triggers it (deleting a recipe when a session
stops, revoking recipes on a force reload) or done by the core when the node
asks for it over a signed internal route. Readers of the Redis keys on the core
did not change. Every internal route works on any API replica: none relies on
process state from an earlier request, and source identity does not depend on
host-local file system fields.

## Node keys

A node key is `snk1_<node id>_<base64url, unpadded, of 32 random bytes>`
(`internal/nodepool/nodekey.go`). The id is part of the key, so a node needs
no `NODE_URL` or `NODE_NAME` to find its row. `ParseNodeKey` accepts only the
canonical form: a positive decimal id without leading zeros, exactly 32 bytes,
text that re-encodes to itself.

- **Storage.** `stream_nodes.node_key_ciphertext`, encrypted with the data
  cipher under the AAD `stream_nodes.node_key/<id>`, which binds the ciphertext
  to its row; decryption also checks that the key names that row. The column is
  a credential, not configuration: `track_stream_node_configuration` does not
  compare it, so issuing a key does not move `admin_revision` or `config_etag`.
  After a `SECRET_KEY` change the ciphertexts no longer decrypt, and every node
  is keyless until it is issued a new key.
- **Issue and rotate.** `POST /api/v2/admin/nodes/{id}/key` returns
  `201 {"node_key": "..."}` with `Cache-Control: no-store`, once. Every call
  rotates. Only `type = 'transcode'` rows take a key; a proxy gets `409`. The
  handler refreshes this replica's registry, reloads its pools and publishes
  `node_pool_changed`, so other replicas and proxies drop the old key within
  one refresh. When that pool reload fails, the event is not published and the
  others adopt the key on their periodic reload.
  `key_issued` on the node row says whether one exists. The web nodes page
  issues and rotates keys and shows the key once with the node's environment.
  Drain a node before rotating its key: the old key stops working at once, so
  the streams the node runs fail at their next signed call.
- **Rejected key.** When the core answers the node's configuration read with
  `401`, `nodeconfig.Remote` records it and the node's `/api/v1/health`
  answers `200` with `"key_rejected": true`, which the core's health sweep
  reads as unhealthy (`nodepool.CheckNodeReport`), so placement leaves the
  node out within one sweep. A `401` on any other call the node makes asks for
  a configuration read at once (`nodecontrol.Client.SetOnUnauthorized`,
  `nodeconfig.Remote.ReportUnauthorized`). While the flag is set every call
  gets `401`, so those requests ask for at most one read per 30-second poll
  interval, and the poll's own `401` warning is logged at most every ten
  minutes; the change into and out of the rejected state is logged at once.
  The node keeps polling every 30 seconds and clears the flag on the next read
  the core answers. Health keeps answering `200` so a liveness probe does not
  restart a node that a restart cannot fix.
- **Keyless nodes** stay listed and health-checked but get no work: nothing can
  sign a request to them (`nodeauth.ErrNoNodeKey`), and placement leaves them
  out.

### Credential registry

Placement pools hold only enabled nodes, but a disabled node keeps serving its
streams, so keys live in a separate registry, `nodepool.KeyRegistry`. It loads
every `stream_nodes` row and decrypts the keys of transcode rows that have one;
a ciphertext that does not decrypt is logged and the node treated as keyless.
It indexes nodes by id and by `CanonicalNodeURL`; on a URL collision the
transcode node wins.

It reloads on `node_pool_changed`, every 60 seconds, and on a lookup miss at
most once a second; concurrent reloads share one read with a ten-second
timeout. A deleted row or a rotated key is gone after one reload. API servers
and proxies both run one (proxies have the database and the cipher).

### Signing in both directions

Both directions use the construction in
[worker HTTP contracts](worker-http-protocol.md#authentication): an HMAC-SHA256
over a canonical request (version, timestamp, method, escaped path, raw query,
body SHA-256), under a key derived from the input key with HKDF-SHA256. The
direction picks the HKDF label and canonical version, so a request captured in
one direction never verifies in the other:

| Direction | HKDF label | Canonical version | Header fields |
|---|---|---|---|
| core → node | `silo/node-request-auth/v1` | `silo-node-request-v1` | `t`, `b`, `sig` |
| node → core | `silo/node-to-core-auth/v1` | `silo-node-to-core-v1` | `n`, `t`, `b`, `sig` |

Both allow ±30 seconds of clock skew and a 1 MiB body. The header is
`Authorization: Silo-Node-HMAC-SHA256 n=<node id>,t=<unix>,b=<hex body sha256>,sig=<hex hmac>`;
`n` appears only on node-to-core requests, in canonical decimal.

**Core to node.** Senders call `nodeauth.SignTranscode` or `SignProxy` with a
`nodeauth.Keyring` (`SignWith`). The registry's keyring is target-aware: a
transcode target gets only that node's own key, and a proxy target gets the
shared secret only when the URL is registered as a proxy. An unknown URL is
`ErrUnknownNode` and a keyless transcode node `ErrNoNodeKey`; neither falls back
to the shared secret, so a transcode node never sees a shared-secret signature.
`Node.SignTarget()` picks the target from the row type. A caller holding only a
URL names the target from where the URL came from (the proxy pool, a route's
proxy), never from the URL itself. Proxies and transcode nodes both serve
`/hw-capabilities`, so `transcodenode.FetchHWCapabilities` takes the target, and
the protocol-v3 capability cache keys each inventory by target and URL. The node verifies with
its own key through `nodeauth.VerifySigned`, which has no legacy-bearer branch.

**Node to core.** The node signs every call with its own key, relative to
`SILO_CORE_URL` (the same path-prefix rule as the other direction).
`nodecontrol.RequireNode` parses `n`, takes that node's key from the registry
and verifies (`nodeauth.VerifyFromNode`). A disabled node is accepted, since it
keeps serving its streams; a deleted, keyless or proxy row, and anything
unsigned or badly signed, gets a plain-text `401`. A signature that fails
against a known node's loaded key is checked once more after a registry reload
(at most once a second), so a node restarted with a just-issued key is not
refused until the next periodic refresh. The verified id goes into the request
context, and handlers take the node's identity only from there, never from a
body.

**Replay.** A captured request verifies for 30 seconds, in its own direction
only. Reads are harmless to replay. Reports are fenced by sequence. A replayed
log batch can duplicate entries. Links that cross untrusted networks still need
TLS.

## Startup and environment

`MODE=transcode` branches before bootstrap configuration loading, so a node
never needs `DATABASE_URL` or `SECRET_KEY` and opens no cipher, database,
settings store or event bus.

| Variable | |
|---|---|
| `SILO_CORE_URL` | Required. The API address the node calls. With several API replicas, use their load-balanced address, so the node's control calls and source reads survive one replica restarting. |
| `SILO_NODE_KEY` | Required. The node's key. A malformed key refuses startup. |
| `PORT` | Optional listen port, default `8080`. |
| `SILO_NODE_DRAIN_TIMEOUT` | Optional; see [node drain](node-drain.md). |
| `DATABASE_URL`, `REDIS_URL`, `SECRET_KEY`, `NODE_URL`, `NODE_NAME` | Ignored, with one startup warning, so shared env files keep working. |

The node then fetches its configuration, retrying with backoff from one second
to 30 seconds until it succeeds, and starts its listener. A configuration whose
`node.id` differs from the id in the key is refused. Logs go to stderr as
before and to the core (see [Logs](#logs)).

## Configuration

`GET /api/internal/nodes/self/config` (`nodecontrol.Server.HandleConfig`)
returns a document for the calling node alone (`nodecontrol.NodeConfig`):

```json
{
  "revision": "<sha256 of the rest of the document>",
  "node": {"id": 7, "name": "gpu-1", "url": "http://gpu-1:8080"},
  "source_access": "local",
  "drain_requested": false,
  "playback": {"ffmpeg_path": "", "hw_accel": "", "hw_device": "", "transcode_dir": "", "segment_retention_seconds": 0},
  "download": {"artifact_dir": "", "artifact_max_bytes": 0},
  "subtitles": {"sync_node_capacity": 0},
  "logging": {"level": "", "format": "", "quiet": "", "capture_level": ""}
}
```

The core builds it from the server settings with the node's own overrides
applied (acceleration, device, artifact directory and budget). The strong
`ETag` is the revision; `If-None-Match` gets `304`. `404` means the row is
gone.

On the node, `nodeconfig.Remote` serves the configuration to
`transcodenode.Server` through the `RuntimeConfig` interface:

- It polls every 30 seconds with `If-None-Match`, and refetches at once on
  `/admin/reload-config` and `/admin/force-reload`.
- **Pointer stability.** It replaces its `*config.Config` only when the
  configuration it builds differs from the current one. Handlers refuse work
  whose configuration pointer changed mid-request, so an unchanged poll must
  never swap it. Drain callbacks fire when `drain_requested` changes.
- A failed poll keeps the last configuration.
- Settings read once at startup (`transcode_dir`, `artifact_dir`) apply when
  the node restarts. `sync_node_capacity` applies live: the media sampling
  route resizes its limiter to the current value on every request.

The core pushes `/admin/reload-config` (never `/admin/force-reload`) after an
admin change to a node's URL, acceleration or device override, or
`source_access`, and after a drain request or resume. Server-wide settings
reach nodes within one poll.

## Reports

Every second the node posts `POST /api/internal/nodes/self/report`
(`nodecontrol.Report`): `boot_id` (its start time in Unix nanoseconds),
`seq` (increasing per process), its session records
(`nodesessions.SessionInfo`) and its stream telemetry snapshot. The core
ingests it without process state (`HandleReport`):

1. **Fence.** One Lua call compares `(boot_id, seq)` with
   `silo:nodereport:<node id>` (two-minute TTL), boot first, and stores the
   pair only if it is greater. An older or equal pair gets `409` and nothing is
   written, so replays and delayed reports are refused. The node warns after ten
   consecutive `409`s.
2. **Sessions.** One Lua script replaces the node's records: each session is
   `silo:sessions:n<node id>:<session id>` with a 60-second TTL, indexed by
   `silo:nodesessions:<node id>`, and keys missing from the report are
   deleted. The node URL and name in each record come from the registered row,
   not the report: the credential registry's copy of it
   (`nodepool.KeyRegistry.Identity`), so a Postgres outage does not drop
   reports, with a row read only for a node the registry does not hold. Log
   batches and resolve read the node the same way. Keys are scoped by node id, so no node can overwrite or
   delete another's.
3. **Telemetry.** The snapshot is written as a full replace under the publisher
   id `node:<node id>`; the wire snapshot carries no publisher id. The core's
   clock sets `CapturedAt` and the roster score, and every reported timestamp is
   clamped to at most 30 seconds after the core's now, so a node cannot date its
   way past the pruning cutoff and evict other publishers.

Limits: 2,000 sessions and 2,000 transfers per report (`413` beyond), within
the 1 MiB body. A node with more keeps the 2,000 newest sessions and the 2,000
telemetry sessions and transfers with the most recent activity, and sets
`truncated`, which the core logs. Only a body still over 1 MiB makes it halve both lists until it fits. A core without Redis answers `204` and
drops the report.

Two replicas can pass the fence for reports n and n+1 and write them in the
opposite order. The view is then one report old until the next one replaces
it; putting every write into one script was judged not worth it.

The 1-second cadence keeps the 5-second telemetry freshness window with room
for four missed reports. Readers (`GET /api/v2/admin/node-sessions`,
`/api/v1/admin/node-sessions`, telemetry parity) read the same keys as before.

## Logs

The node's slog handler feeds a `logstream.Drain` whose insert posts
`POST /api/internal/nodes/self/logs` (`nodecontrol.LogBatch`): up to 100
entries, every two seconds, split in half when a body would exceed 1 MiB (a
single oversized entry is dropped). Network errors, `429` and `5xx` are
retried under Drain's usual rules. FFmpeg output goes through the same handler.
stderr logging continues.

The core stamps every entry with the authenticated node as `<name>#<id>`,
ignoring any node id in the entries; the id keeps two nodes with the same name
apart. It clamps timestamps to the window from 24 hours ago to 30 seconds
ahead, inserts the batch into `operational_logs` and publishes it on the logs
channel for live tails. Nodes run no partition DDL.

## Drain and health

A drain request reaches the node in `drain_requested` (pushed, and polled every
30 seconds). When the node's drain state changes (an admin request, SIGTERM,
reaching drained, or a drain already in force at startup) it posts
`POST /api/internal/nodes/self/health-changed` with a five-second timeout. The
core publishes `node_drain_changed` on the admin channel, and every replica
health-checks the node at once (`nodepool.HealthChecker.CheckTranscodeNode`). The health poll
itself stays unauthenticated; a node whose key the core refuses reports `key_rejected` on it
and reads as unhealthy (see [rejected key](#node-keys)). See [node drain](node-drain.md).

## Resolve

A node rebuilds a session it lost, and serves each progressive `/remux`
request, through `POST /api/internal/nodes/self/resolve`
(`nodecontrol.Server.HandleResolve`):

```json
{"transport_id": "...", "purpose": "hls" | "remux", "stream_token": "<opaque, optional>"}
```

`stream_token` is the `X-Silo-Stream-Token` a relay forwarded, passed through
unread. A `200` carries the recipe card, a fresh source grant for its file or
theme, and the playback identity (`playback_session_id`, `user_id`,
`profile_id`, `media_file_id`, and `started_at` with `started_at_source` when
the token or stored recipe names a start). A theme conversion also carries `theme`: the theme file's
id, size and modification time, which the recipe card has no fields for.

The core does what the node used to do:

- **HLS.** Deny check on the transport id. With a token: verify it, require
  that the card names the transport, deny-check the token's session id. A token
  that does not verify or names another transport gets `404` `not_found`. Then
  the stored recipe (`internal/noderecipe`): when the store holds a complete
  card for the transport, that card is used even when the token is complete,
  and its session id is deny-checked; otherwise a complete token card is used,
  and with neither the answer is `404`. The core writes the stored card at a
  tokenless or jellycompat start and rewrites it when it moves a transport off
  an unreachable node, while the client keeps the token minted for the node the
  transport left; the stored card names the node it runs on now. A store that
  cannot be read is `503`. The chosen recipe must be bound to the calling node:
  its execution node id matches, or, for a card with no node id, its canonical
  node URL matches the node's registered URL. A card bound to neither is
  refused.
- **Remux.** A token is required. Deny checks on the transport and the session;
  the route must be remux with transcode execution and proxy egress, and its
  execution node id must be the calling node; downmix and theme shape checks;
  and the remux authority must still match the stored recipe field by field.
- **Source.** The catalog must still have the recipe's exact file, or its theme
  in an enabled library, before a grant is minted.

| Status | Reason (JSON body) | Meaning |
|---|---|---|
| 404 | `not_found` | No recipe for the transport, or a token that does not describe it. |
| 410 | `denied` | The deny marker is set. |
| 410 | `stopped` | The remux authority is gone or no longer matches. |
| 410 | `source_changed` | The source left the catalog, or the theme file changed. |
| 403 | `invalid_token` | A remux request without a valid token. |
| 403 | `wrong_route` | The recipe or token is another node's, or an HLS recipe bound to none. |
| 503 | none | Transient; the node answers its relay with `503`. |

A `/remux` request answers a refusal as the node answered the same condition
when it read the token itself (`transcodenode.writeRemuxRefusal`):
`invalid_token` `401`, `wrong_route` `503`, `denied` and `stopped` `410`,
`source_changed` `400`, `not_found` `404`, and anything else `503`. A lost HLS
session answers `denied` with `410`, any other refusal with `404`, and a core
it cannot reach with `503`.

On the node, an HLS request for a live in-process session uses the URL's
transport id and calls nothing; only a lost session calls resolve. Every
`/remux` GET and HEAD calls resolve once. Telemetry and tracker identities
come from the start request or from resolve; the node parses no token. A
rebuilt session and a remux request record the resolved `started_at` (the
token's `ostn`, the stored recipe's original start, or the token's `iat`), and
the rebuild time only when resolve names none.

## Recipe deletion

The node no longer writes the recipe store, and it reports no terminal events.

- **Stop.** The core's remote stop (`TranscodeManager.stopRemoteTranscode`,
  used by native and Jellyfin-compatible stops) deletes the transport's recipe
  first, including the one a move wrote for its new node, with a five-second timeout, and then sends `DELETE /transcode/{id}`.
  The recipe goes first so a request relayed during the stop cannot rebuild the
  session. `StopRemoteTranscode` logs a failed deletion rather than returning
  it, and the node is told to stop either way.
- **Cancel.** `CancelRemoteTranscode` only sends the DELETE. It confirms that a
  remote process is gone before a user-visible stop removes the playback
  session, and leaves the recipe to its caller: a stop deletes it, and a
  rollback that cannot cancel the live route keeps it.
- **Force reload.** Before the admin force-reload calls a transcode node, the
  core revokes the recipes bound to it, by node id and then by URL. When
  revocation fails, the node is not called and its result is an error. The node
  only tears down its local state.
- **Idle reap** is unchanged: the node closes the runtime of a session idle for
  ten minutes and the recipe stays, because an idle reap is not a stop.

Force reload revokes cards bound to the node's id and those bound to its
current URL. A card bound by URL alone to a URL the row no longer has is not
revoked. See also [the URL normalization note](#recipe-authority-url-normalization).

## Relay deny checks

The node checks no deny marker, so every relay that signs a request to a
transcode node checks it first:

| Relay | Check |
|---|---|
| Native API manifest and segment relays | `remoteTransportDenied` (transport id); `loadTranscodeServeSession` already checks the playback session |
| Proxy token and grant routes | `sessionDenied`: the playback session id, then the transport id |
| jellycompat master manifest, HLS manifest and segment relays | `remoteCompatDenied`: the transport id, and the playback session id when it differs |

A denied session gets `410` even when the node's DELETE failed and its runtime
is still alive. A native relay that moves its transcode off an unreachable node
checks the playback session and the transport again before the move commits
and before it serves from the new executor, since a stop can land while the
move runs; see
[Transcode node lost under the API relay](restart-resilient-playback.md#transcode-node-lost-under-the-api-relay). `TestStreamDenyRefusesEveryTranscodeNodeRelay`
(`internal/proxy/streamdeny_test.go`) enumerates the relays, with
per-surface tests beside it. Download and acknowledgement relays are not tied
to a playback session and have no marker to check. Resolve applies the same
check before it rebuilds anything.

## Relay error mapping

Every relay authenticates its client (a login, a stream token or a grant)
before it signs a request to a transcode node, so a node's `401` never
describes the client's credentials: it refuses the relay's signature (a key
being rotated, or not yet loaded on one side) or the stream token the relay
forwarded. Passed through, a player can read it as its own sign-in failing and
sign out. The relays answer it as the node being unavailable, in their usual
error shape, and log a warning naming the node at most once a minute
(`transcodeproxy.NodeRefusedRelay`). The native `/api/v2` playback delivery
routes answer `503` `dependency_unavailable` (`apiv2.playbackDeliveryProblemType`
maps the relay's `502`). The `/api/v1` native relays and the proxy token, grant
and theme, Jellyfin-compatible and download-artifact relays answer `502`. A relay's own
`401` for a bad client token is unchanged. A node answers no `403` of its own:
it maps the core's resolve refusals to `400`, `401`, `404`, `410` and `503`
(see [resolve](#resolve)). Other node answers, such as `404` and `410`, reach
the client as before.

## Source access

### Source grants

A grant is a stateless, core-signed capability to use one revision of one file
through one node (`internal/nodesource/grant.go`).

- **Encoding.** `base64url(JSON payload) + "." + base64url(HMAC-SHA256)`, at most
  8 KiB. The key is `HKDF-SHA256(auth.jwt_secret, info "silo/node-source-grant/v1")`,
  so only holders of the shared secret (API servers and proxies) could mint
  one; the API servers do.
- **Payload.** `{"v": 1, "node": 7, "kind": "file" | "theme", "id": 123,
  "path": "/media/...", "size": ..., "mtime": <unix seconds>, "exp": <unix>}`.
  The path is the exact catalog path, so a source read needs no database
  lookup. The node can read the payload; that is fine, because in local mode it
  gets the path anyway.
- **Revision** is `(kind, id, size, mtime)` with the modification time in
  **whole seconds**. It deliberately leaves out device, inode and ctime, and
  sub-second time, so every replica computes the same revision for a shared
  library whatever its mount and file system. The ETag is
  `"<kind>-<id>-<size>-<mtime>"`.
- **Life.** One hour (`GrantLifetime`). The node renews every 20 minutes
  (`RenewInterval`) while the input is open, and renewal keeps the revision. No
  job lifetime limit follows from grant expiry.
- **Minting.** `nodesource.Grants.ForFile` and `ForTheme`. The core mints a
  grant for every job it sends a transcode node, whatever the node's
  `source_access`: native and Jellyfin-compatible transcode starts, download
  preparation, trickplay, chapter thumbnails and subtitle-sync samples carry
  `source_grant`, and resolve mints one for rebuilds and remux. When a grant
  cannot be minted, downloads and subtitle sync fall back to preparing
  locally (unless subtitle sync is restricted to transcode nodes); trickplay
  and chapter thumbnails treat the node as unavailable.

**Operator requirement.** Every API replica, and every node with `local`
source access, must see a library file with the same size and the same
modification time to the second. This is the same assumption as identical
library paths. A mismatch fails closed: the core answers `412`, and a local
node refuses the job (`409` at start).

### Open and renew

`POST /api/internal/nodes/self/sources/open` with `{"grant": "..."}`. Every job
input on every node goes through it, in both modes.

1. The grant must verify, be unexpired, and name the calling node: otherwise
   `403`.
2. The catalog must still have the source: a media file by id, not missing, at
   exactly the granted path; a theme by id at exactly that path in an enabled
   library. Otherwise `410`.
3. The core stats the path (following symlinks, which the scanner approves for
   catalog paths): gone or not a regular file is `410`; another size or mtime
   second is `412`.
4. `200 {"grant": <same claims, exp = now + 1 h>, "path", "size", "mtime", "etag"}`
   with `Cache-Control: no-store`.

`503` (no grant key, catalog unavailable) is transient. The node calls open,
for a job's start and for renewal, through its control-plane client
(`nodecontrol.Client.OpenSource`, the relay's `RelayOptions.Open`), so it is
signed like every other node call and a `401` triggers the same configuration
reread. Byte reads sign themselves on the relay's own streaming transport. On
the node:

- **local**: stat `path`, require a regular file with the same size and mtime
  second, and open it. A mismatch logs both sides and refuses the job with `409`
  and a mount-mismatch message.
- **core**: create a relay handle (below) with the returned grant and metadata.

A job request on the node (start, download preparation, trickplay, chapter
thumbnails, media samples) answers plain-text `400` when it carries no grant
or the core refuses it, `409` on a local mismatch, and `503` when the core
cannot be reached.

Renewal calls open with the current grant and swaps in the answer. Transient
failures retry with backoff from one second to one minute until the current
grant expires, so a core outage of up to about 40 minutes is survived. A
`400`, `403`, `404`, `410` or `412`, an answer describing a different source,
or a grant that expires while the core is unreachable, ends the job's input.

### Source bytes

`GET` and `HEAD /api/internal/nodes/sources/` (`HandleSource`; the node
requests it without the trailing slash, which the router also serves), with the grant
in the `X-Silo-Source-Grant` header. A header, not a path segment, so access
logs on the way (a reverse proxy in front of the core) never record it.

1. The grant must verify, be unexpired, and name the calling node: otherwise
   `403`. **There is no catalog query**: the catalog is checked at open and at
   every renewal, so it is at most 20 minutes stale.
2. The core opens the path with `O_NONBLOCK`, `fstat`s the descriptor and
   compares size and mtime second with the grant: `410` when gone, `412` when
   changed.
3. It serves from that descriptor with `playback.ServeOpenFile`, so a file
   replaced after the check is never served. `http.ServeContent` handles single
   and suffix ranges and `If-Match` on the grant ETag, with the rolling write
   deadline instead of the absolute server timeout. `If-None-Match`,
   `If-Modified-Since`, `If-Unmodified-Since` and `If-Range` are dropped: a
   condition may only narrow the answer to the grant's revision, never widen or
   redirect it.

There is no per-replica concurrency cap. The node control routes are excluded
from the activity log and the request logger.

### Local and core access

`source_access` is a per-node admin setting (`local` by default, or `core`),
stored in `stream_nodes.source_access` and delivered in the configuration. The
relay reads it at each open, so jobs started after a change use the new mode
and running jobs keep theirs. Dispatch never branches on it: the core mints
the same grant either way.

- **local** reads the approved catalog path through the node's own mount, as
  nodes always have. The authority is the exact path open returns, which
  replaces the old catalog database check one for one; the node never trusts an
  `input_path` it was sent.
- **core** reads the bytes over HTTP from the core through the loopback relay.
  The node needs no library mount.

In core mode the first start of a copy-video session with a planned keyframe
playlist (fMP4 HLS, `planCopySegments`) reads the source twice through the
core: FFmpeg reads it, and the keyframe index's full Matroska check
(`keyframes.LoadSource`) streams the whole file in the background through the
sequential reader (see [ReaderAt and the sequential verifier](#readerat-and-the-sequential-verifier)).
The checked index is cached per source revision in the node process, so later
starts of the same revision read it once. With local access both reads hit
the node's mount.

### Loopback relay

`internal/nodesource` relays core-mode sources to FFmpeg and to Go consumers.

- **Listener.** `127.0.0.1:0`, started with the first core-mode handle.
  `Relay.Open` registers a handle under `http://127.0.0.1:<port>/src/<32 hex>`
  (128 random bits). A `Handle` has `URL()`, `Identity()` (the grant ETag),
  `Size()`, `ModTime()`, `ReaderAt()` and `Close()`, and owns its renewal loop.
- **Lifetime** is the job's: a transcode session across FFmpeg restarts and
  seeks, one remux request, one download preparation, one trickplay, chapter
  or sample run.
- **Serving FFmpeg.** `Range` is forwarded on GET only. The relay always sends
  `Accept-Ranges: bytes` and an exact `Content-Length`, `Content-Range` on
  `206`, and `Content-Range: bytes */size` on `416`.
- **Upstream.** One fetch primitive (`fetchRange`) serves both FFmpeg and Go
  readers. Each request is signed, carries `If-Match` and
  `Accept-Encoding: identity`, and its answer is checked for the ETag, identity
  encoding, exact `Content-Range` and length. The client has no proxy, no
  compression, no redirects, a ten-second response-header timeout, and pooled
  connections.
- **Bearer capabilities.** A loopback URL lets anyone on the node host read
  that source while its handle lives. The URLs are redacted wherever the node
  logs: `RedactLoopback` replaces each with `source <etag>` (or
  `source (released)`), and the node's slog handler, FFmpeg argv logs, the
  FFmpeg log sink, subtitle staging stderr and error responses all apply it.
  The design assumes the node host runs no untrusted local users or processes;
  `/proc/<pid>/cmdline` exposure is accepted on that basis.
- **Close.** `Handle.Close` fails the handle with `ErrClosed`, cancels in-flight
  reads, interrupts a write held by FFmpeg back-pressure
  (`SetWriteDeadline(now)`) and unregisters the URL. A released URL answers `404`
  or its handle's fatal status; Go consumers get an `fs.PathError` naming
  `source (released)`.

### Resume, stall and idle

When the upstream body ends early or fails and the caller is still live (the
FFmpeg request, or the job for a Go reader), `fetchRange` asks again from the
current offset.

- **Transient** (a network error, an invalid answer, and every status not
  listed below: `5xx`, `429`, and `401` too, because the core reloads node keys
  when a signature fails to verify, so a key rotation in progress heals): back
  off from 250 ms, doubling to 4 seconds, and give up after 30 seconds spent
  waiting on the core without progress. Time the caller spends not reading does
  not count.
- **Fatal** (`400`, `403`, `404`, `410`, `412`): fail that read and the handle.
  A malformed request does not heal on a retry. Only these close a handle.
- **`416`** fails only that read and passes through to FFmpeg.
- **Stall.** Each attempt arms a ten-second timer; a body that delivers nothing
  for ten seconds is cancelled, which interrupts a blocked read, and the next
  attempt resumes.
- **Idle.** When the caller has not read for more than ten seconds (throttling,
  client back-pressure), the body is dropped and the next read starts a fresh
  request. The core's write deadline would drop an idle connection anyway.

When a body cannot be completed (the retries ran out, or a fatal status), the
relay aborts the loopback connection instead of ending it cleanly, so FFmpeg
sees a short read rather than a short file.

### ReaderAt and the sequential verifier

Go consumers (keyframe indexes, tone-map revision checks, Matroska index
verification) read through `nodesource.OpenReaderAt` or the handle's
`ReaderAt`. Process registry lookups (`nodesource.Stat`, `OpenReaderAt`,
`Identity`, `InputKey`) accept local paths and registered loopback URLs alike;
for a local path the identity is `path|size|mtime`, with mtime in Unix
nanoseconds.

- **ReaderAt.** 64 KiB pages in a 2 MiB LRU; reads larger than a page fetch
  directly in ranges of at most 1 MiB. Budget `min(2 × size, 64 GiB)`, four
  readers per process, a two-minute deadline. Errors are `*fs.PathError`, so
  `keyframes.transient()` treats them as retryable. With local access, the
  handle's `ReaderAt` opens the file non-blocking and rechecks `fstat` against
  the revision; `nodesource.OpenReaderAt` given a plain local path (no handle)
  is an ordinary open with no revision check.
- **SequentialReader.** Matroska index verification reads front to back, which
  page-sized ranges would turn into hundreds of requests. The sequential reader
  streams one range of up to 256 MiB and reaches reads up to 8 MiB ahead by
  discarding bytes; a read behind or further ahead starts a new request. Its
  deadline is two minutes plus size at 16 MiB/s, at most two hours, and it shares
  the ReaderAt slots and budget. A full pass takes about size ÷ 256 MiB
  requests.

### FFmpeg argument guard

`nodesource.RestrictArgs` puts `-protocol_whitelist http,tcp` and
`-format_whitelist <admitted demuxers>` before an `-i` whose input is a
loopback URL; `RestrictProbeArgs` does the same for ffprobe, whose input is
positional. Any argument matching the loopback pattern is restricted, whether
or not its handle is registered. The subtitle filter never takes a relay URL:
`checkSubtitleFilterInput` refuses one that was not staged (below), since that
filter has no protocol whitelist.

## Consumers

| Consumer | Core-mode behavior |
|---|---|
| Transcode, remux, download preparation, trickplay, chapter thumbnails, media samples | Input from `resolveInput` / the source opener; argv guard on loopback inputs. |
| Tone-map `SourceRevision.ValidatePath` | `nodesource.Stat` (mtime compared to the second for relayed sources) and the OpenSubtitles hash through `OpenReaderAt`. A `404`, `410` or `412` means the revision changed. |
| Keyframe index cache | Keyed on identity and size. |
| DV RPU probe | Keyed on `binary|nodesource.Identity(input)`; the probe now runs on relayed inputs instead of failing open. |
| Copy-seek anchor and auto-transcode fallback caches | Keyed on `nodesource.InputKey`. |
| Text subtitle burn-in | Staged on the node; see below. Bitmap burn-in is unchanged. |
| Trickplay sample concat | Relayed concat; see below. |

### Relayed text subtitle staging

FFmpeg's `subtitles=` filter opens its file with no protocol whitelist, so it
must not be given a loopback URL. For a core-mode source the node extracts the
selected embedded text track to `selected.ass` (with `-copyts`) and its fonts
(`-dump_attachment`) into a node-level staging cache
(`playback.RelayedSubtitleStaging`, in `<transcode_dir>/subtitle-staging`). A local source keeps the symlink alias it always
used. `escapeFilterPath` escapes `' [ ] ; , :`.

- **Cache.** Entries are keyed by `(source identity, track, codec)`, so starts
  and rebuilds of the same source and track share one staging. A staging runs
  detached from the request on its own reopened handle and commits by rename.
- **Admission.** Two stagings run at once per node; up to eight more wait in a
  queue. Each in-progress staging reserves 256 MiB for its script and 32 MiB for
  fonts against a 1 GiB cache bound. A full queue, a full cache or a cancelled
  wait is `ErrSubtitleStagingBusy`, which a start answers with `503 subtitle
  staging is busy; retry shortly` and a rebuild with `503`.
- **Leases.** Every session using a staging holds a lease; a leased entry is
  never evicted. When the last lease on an unfinished staging is released, the
  staging is cancelled after 30 idle seconds. A failed staging is kept for a
  minute so a burst of retries does not read the source again; a failure that
  follows from the source's own bytes is kept until the sweep, which removes
  entries after 24 hours.
- **Timeout.** Two minutes plus the file size at 4 MiB/s, at most four hours.
- **Fonts.** Only attachments with font magic bytes are kept, at most 32 of
  them and 32 MiB in total; other attachments and those past the limits are
  skipped, not fatal.
- **Preparing sessions.** A start whose staging is not done registers the
  session as preparing, and FFmpeg launches when staging completes. Without
  `RequireReady` the start returns `202` at once; a `RequireReady` start still
  waits for the manifest, and fails like any readiness failure if staging
  outlasts that wait.
  Meanwhile segment requests wait up to 12 seconds and `init.mp4` up to
  10 seconds, then answer `404`, and the player retries.

**Known limitation.** A cold burn-in start of a large file over a slow link can
fail its first attempt in either mode: `RequireReady` waits for a manifest for
a bounded time, and reading the whole file for the subtitle track can take
longer. A local node mounting the library over NFS or SMB reads at the same
network speed inside that wait. In core mode the staging survives the failed
start, and the retry joins it, so a later attempt succeeds without starting
over.

### Keyframe plan records

A copy or fMP4 session that plays from the source's own keyframes keeps its
plan in `<transcode_dir>/keyframe-plans/<hex(sha256(session id)[:16])>.json`
(32 hex characters),
outside the session's output directory, so the plan survives the idle reaper
and a rebuild needs no second Matroska verification.

- The record stores `source_identity` (the grant ETag). A rebuild whose source
  identity differs fails with `410` (`ErrCopyPlanSourceChanged`); a rebuild
  that cannot read the index returns a retryable `503`
  (`ErrCopyPlanUnavailable`). A rebuild also serves FFmpeg's own playlist
  when its record says the first start did, when the record cannot be read as
  a plan, or when a rebuild that found no record cannot write one. A rebuild
  that follows a record never publishes a different plan.
- **Candidate commit.** A start writes its decision to a candidate file beside
  the record and publishes it by rename only after the start succeeds, so a
  failed replacement leaves the live session's record intact. A failed start
  removes its candidate.
- The sweep removes records older than 24 hours that no live session uses;
  orphaned candidates age out the same way. A force reload still deletes plan
  records: the core has revoked the recipes first, and a later token rebuild
  verifies again.

### Relayed concat

Trickplay sampling reads the input through an ffconcat list
([media sampling](media-sampling.md)). With a relayed input the list opens
with `-protocol_whitelist http,tcp,pipe -format_whitelist concat -f concat
-safe 0`, and each entry names the bare URL followed by
`option protocol_whitelist http,tcp` and
`option format_whitelist <admitted demuxers>`. An FFmpeg that cannot parse
that list fails on the list itself, which would read as a broken file, so
`mediasample.Capabilities` checks the relayed form (`relayedConcatSamples`)
against an input it cannot open, and the node refuses a relayed sample run its FFmpeg
cannot do with `422` reason `unsupported` (or `capabilities` when the check
itself failed). Neither is permanent, so the caller moves the work to another
node instead of marking the file unusable.

## Failure behavior

| Event | Behavior |
|---|---|
| Rolling API restart, or reads spread across replicas | Grants and revisions do not depend on the replica. The relay resumes from its offset against any replica. |
| Core unreachable for under 30 seconds | Source reads retry. The configuration keeps its last value. Reports are lost and the next one replaces the view. Logs retry, then drop. Output stalls anyway, since the core relays it. |
| Core unreachable for 30 seconds or more | In core mode, the active FFmpeg read fails and that job fails, as with a lost NFS mount; a local-mode job keeps reading its mount. Renewal survives about 40 minutes before a grant lapses. |
| Redis or Postgres blip on the core | Source bytes need neither. Open and renewal retry. Report ingestion fails on a Redis error and the next report replaces it; the node's identity comes from the credential registry, so Postgres alone does not stop reports. Resolve answers `503` when it cannot read the recipe store, and the node retries. |
| Node restart | Sessions rebuild through resolve with fresh grants. The new `boot_id` passes the report fence. |
| File replaced or removed on the core | `412` or `410` at the next read, open or renewal, and the job fails. A rebuild gets a new grant if the catalog still has the file. |
| Session stopped or denied | Every relay refuses it, and resolve will not rebuild it. The core's stop or the idle reaper ends the node's work. |
| Node disabled | Its streams continue (the registry keeps disabled nodes); no new placement. |
| Key rotated or node deleted | The node's calls get `401`, the core's calls fail to verify on the node, and its streams fail. The node reports `key_rejected` on health after its next configuration read (at once when another call got the `401`, else within 30 seconds), and the next health sweep marks it unhealthy, so placement avoids it. Relays answer the node's `401` as the node being unavailable (see [relay error mapping](#relay-error-mapping)). Drain a node before rotating its key. |
| Size or mtime differ between replicas or a local node | The job fails (`412` from the core, `409` from a local node) rather than reading other bytes. |

## Security properties

- A node holds no secret that can mint login, profile or stream tokens or
  source grants, and no database or Redis credential.
- A stolen node key lets the holder accept work for that node, open files
  granted to that node and renew them while the catalog still has them, and
  post reports and logs as that node. It cannot sign requests to other nodes,
  read files no grant names, or reach paths outside the catalog in local mode:
  local inputs come only from open.
- The bytes endpoint does not check the catalog. A holder of a stolen key and a
  grant can read a file removed from the catalog until that grant expires (at
  most one hour after its last open), and one read that started before expiry
  can run past it.
- A node cannot write another node's Redis state: report fences, session keys,
  telemetry publisher ids and roster times come from the authenticated id and
  the core's clock.
- Proxies hold the database credentials and the data cipher, so a compromised
  proxy can decrypt every node key, as it can already read the shared secret.
- Clients still never reach a node directly.
- Loopback relay URLs are bearer capabilities on the node host, under the
  trusted-host assumption above.

## Recipe authority URL normalization

`noderecipe.Store` keys a URL-bound recipe's authority generation by the URL
with whitespace and trailing slashes removed
(`nodeAuthorityGenerationKey`), while the credential registry, resolve and
jellycompat compare URLs through `nodepool.CanonicalNodeURL`. A card that names
the node by a differently spelled URL (case, default port) is therefore not
revoked by `RevokeNode(url)`. Cards with a node id use the key
`node-id:<id>`, which `RevokeNodeID` covers. This predates thin nodes. Aligning the key needs a store that reads both forms
during an upgrade, so it was left as is.
