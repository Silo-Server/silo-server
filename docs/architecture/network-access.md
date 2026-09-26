# Network access providers

Network access providers let an installed plugin give a Silo deployment an
overlay-network identity (Tailscale via tsnet first, NetBird later) so clients
reach it without port forwarding or a public reverse proxy. Phase one covers
one API server plus any number of proxy nodes; transcode nodes are never
exposed because clients never talk to them. The plugin owns the overlay client
and reverse-proxies to the local Silo listeners; the server owns supervision,
per-instance state, the access path, status, and the admin API
([docs/network-access-api.md](../network-access-api.md)). Tracking issue:
Silo-Server/silo-server#1001.

```
                 overlay                       LAN / backend
 client ──► [plugin listener] ──► 127.0.0.1:8080  API server
              X-Silo-Ingress-Token                │ relay            ▲ health pull
                                                  ▼                  │ (network_access)
 client ──► [plugin listener] ──► 127.0.0.1:PORT  proxy node ── transcode node
              on the proxy host
```

## Why a plugin

The overlay client (tsnet) updates on its own cadence; shipping it as a plugin
lets it release without a Silo release. The capability
`network_access_provider.v1` is provider-neutral so a second provider is a
second plugin, not a new server surface. Providers are discovered from the
manifest's typed descriptor (`network_access_provider.provider`, the stable
slug, and `display_name`) without launching the plugin.

## Resident contract

Plugins declaring `network_access_provider.v1` are *resident*: ingress must be
up before anyone can use it, so they do not start lazily on first RPC like
other plugins. The resident supervisor (`internal/plugins/resident.go`) owns
them:

- Started once the API listener is bound (`Service.StartResidents`), never
  from the boot-time lifecycle hooks, so a plugin cannot proxy to a listener
  that does not exist yet.
- Reconciled on every `OnLifecycleChange` (install, enable, disable, config
  save, auto-update, uninstall). A config save or auto-update stops the
  process; the next reconcile starts it again.
- Restarted after a crash with exponential backoff from 1 s to 60 s plus
  jitter, reset after five minutes of stable running; parked as `failed`
  after ten consecutive failures until an admin restarts or reconfigures it.
  The plugin host's exit watcher reports the exit; the gRPC health probe
  still catches a hung process.
- Stopped before the HTTP servers drain (`Service.StopResidents`) so overlay
  ingress goes away first.
- Visible to admins as `runtime` on each installation and restartable through
  `POST /api/v2/admin/plugins/installations/{id}/restart`.

The predicate is a list of capability types
(`IsResidentCapabilityType`), so a later resident kind is one more entry.

The admin network access reads never launch a plugin either: a host whose
process is not running answers `unavailable` with the supervisor's last error,
and the admin fixes it from the Plugins page.

## Ingress token and access path

The server must know which requests arrived through a provider so stream URLs
and WebSocket origin checks can act on it, and it must learn that from
something a client cannot forge. Trusted-proxy CIDRs would not do: the
plugin's loopback source is indistinguishable from any other local proxy.

- On every start of a provider instance the host mints 32 random bytes, the
  *ingress token*, and hands it to the plugin through `RuntimeHost.GetHostInfo`
  over the plugin's private gRPC broker. Stopping the process revokes it. The
  token is in memory only (`internal/netaccess.Registry`) and so outlives
  neither the plugin process nor the server process; a stale token after a
  plugin restart is rejected until the plugin re-reads `GetHostInfo`.
- The plugin stamps `X-Silo-Ingress-Token` on every request it proxies, keeps
  `Host`, overwrites `X-Forwarded-Proto` with `https`, and sets
  `X-Forwarded-For` to the overlay peer.
- `netaccess.Middleware` runs on all three listeners (API, Jellyfin, ABS)
  before logging or any handler. It validates the token in constant time,
  strips the header, and records `netaccess.Path{Provider}` on the request
  context. An unknown token is `403`; a request without the header is on the
  default path. The route inventory classifies it as infrastructure
  middleware: it never grants or changes authorization.
- `clientip` already trusts loopback, so the forwarded headers are honoured.
  Operators who narrow `clientip.trusted_proxies` must keep loopback.
- `socketOriginAllowed` accepts the overlay origins of providers currently
  connected on this host (`StatusCache.ConnectedOrigins`) next to
  `server.public_url` and the request's own origin, so a browser reaching Silo over the overlay can open
  WebSockets.

Downstream, `Path.Provider` selects the proxy origin a client is handed: a
tailnet client cannot reach a LAN proxy origin, so a provider path returns
that provider's connected origin on the proxy or falls back to API relay.

A status push is accepted only from the process holding the installation's
current ingress token. A push that was in flight when its process was
stopped, crashed, or replaced lands after the revoke and is dropped, so a
dead instance can never write a stale origin over the replacement's.

## Status

Plugins push status on every change through
`RuntimeHost.ReportNetworkAccessStatus`; the host keeps the latest per
installation in `netaccess.StatusCache` and logs state transitions only,
never `auth_url`. The admin API still asks the plugin directly (`GetStatus`,
`Connect`, `Disconnect`, ten-second timeout each) and writes the answer back
into the cache so the origin allow list does not wait for the next push. Each
push and each read go through one converter
(`pluginhost.NetworkAccessStatusFromProto`) so the two views cannot drift.

`unavailable` is a host-side state a plugin never reports: it means the
process is not running or did not answer.

## Instance state and scopes

tsnet needs a persistent node key per overlay identity, and each proxy needs
its own. State lives in `plugin_instance_state`, keyed by installation and
*host scope*, encrypted with GCM and row AAD bound to `<installation>:<scope>:<key>`
(see [secret-encryption.md](secret-encryption.md)). The scope is derived by
the host, never supplied by the plugin: `api` on the API server, `node:<id>`
(`stream_nodes.id`) on a proxy. The same scope string is the `host.id` the
admin API reports, so the two never disagree. Limits: key ≤ 256 bytes, value
≤ 256 KiB, ≤ 256 keys per scope. Uninstall cascades; config test-runs
(negative installation ids) get no state. The API never returns instance
state. Empty values also use an encrypted, row-bound envelope; a plaintext
empty marker is rejected.

## Proxy nodes

Every enabled proxy node runs the same provider installations as the API
server, each instance with its own overlay identity under its own
`node:<stream_nodes.id>` scope. Transcode nodes run none: clients never talk
to them.

- The proxy process builds a reduced plugin host (`cmd/silo/proxy_plugins.go`,
  `plugins.NewNodeService`): the resident supervisor, an archive cache, the
  instance-state store scoped to the node row, and the netaccess broker. No
  admin routes, no catalog or installer, no metadata or other capability
  dispatch. `GetHostInfo` reports role `proxy`, the node name and row id, and
  a single `api` listener: the proxy's own address.
- A proxy never reads the install path the API server recorded on the row:
  that names a directory on the API server's machine. The archive cache
  (`plugins.NewArchiveCacheAt`) rehydrates each release from
  `plugin_archives` into the proxy's own cache root, `SILO_PLUGIN_CACHE_DIR`
  (default `<tmp>/silo-plugins`), under
  `<root>/<plugin id>/<version>/<release>/plugin`, where `<release>` is the
  unique install directory name the API's installer chose. Only that root
  has to exist and be writable on the proxy; a shared plugin volume is not
  required. When a new release lands the previous one is removed from the
  cache. The archive was resolved for the API server's platform at install
  time, so every proxy must run the same OS and architecture as the API
  server in this release; a proxy on another platform refuses the binary
  with a clear error at rehydration instead of failing every launch.
- A replaced or auto-updated binary changes the row's version and install
  path. The API host stopped its own process before installing; the proxy's
  process is still alive and healthy, so the supervisor stops it on the next
  reconcile (`Reconcile` treats a version or install-path change as a
  replacement, not a restart-worthy failure) and starts the new release from
  its rehydrated archive.
- The node row id comes from the config watcher, which matches `NODE_URL` /
  `NODE_NAME` to `stream_nodes`. Until it resolves there is no state scope
  for the node key, so the supervisor's resident gate keeps every provider
  stopped, logs why once, and the admin status reports the reason as that
  host's `unavailable` error. The gate also requires the row to be an
  enabled proxy node: disabling the node or changing its type stops its
  providers on the next reconcile, since the API stops listing it as a
  network-access host and could no longer disconnect them. The gate is
  re-checked on every reconcile. The node scope is also the host identity
  residents run under: if the row is deleted and re-registered under a new
  id, the next reconcile replaces every provider process, since a process
  keeps the identity it started with (its overlay node key, the node id it
  reports) in memory.
- Lifecycle changes happen on the API server. It publishes
  `cache.EventPluginsChanged` on `ChannelAdmin` after every
  `OnLifecycleChange`. Config saves and admin restarts advance the persisted
  `runtime_generation`; the event asks proxies to reconcile against that
  generation. Proxies also reconcile on a 60 s poll
  (`Service.FollowLifecycleChanges`), so a missed publish costs at most one
  minute. A caller that cancels an accepted restart stops waiting while the
  supervisor continues the restart and records the result.
- The API reaches proxies over their backend URL with the node bearer:
  `GET /network-access/status`, `POST /network-access/{provider}/connect`
  and `.../disconnect` on the proxy listener, ten seconds each, in parallel,
  like force-reload. Those routes carry `auth_url` and error text because
  they take the bearer; the public `/health` report carries only state,
  origin and hostname. The proxy listener also runs `netaccess.Middleware`,
  so its own provider's stamped requests are validated there too.
- The proxy's `/health` `network_access` block is what the API stores on the
  node row every sweep and what `ClientURLFor` hands overlay clients. The
  admin status is live and can run ahead of it by up to one sweep.

A provider slug names one provider per deployment. If two enabled
installations declare the same slug, the one with the lowest installation
id owns it: commands, status, and the node health report address that one,
and the duplicate is neither started as a resident nor listed; it is
logged and skipped on every host.
Other capabilities on a resident plugin cannot launch it lazily. This also
applies to excluded duplicates and hosts whose resident gate is closed;
capability RPCs may only reuse the supervisor's running process.

## Single-API constraint

Two API replicas would both load the `api` scope and share one node key,
which the overlay treats as one node flapping between two machines. Phase one
therefore supports one API server; in `server.mode = api` each replica keeps
a unique process presence marker in Redis (`cache.APIReplicaPresence`, 90 s
TTL), independent of shared logical node names, and logs a
warning at start when providers are installed and more than one live replica
is seen (best effort, no hard refusal). The census runs once per start, so
the warning appears in the log of whichever replica starts second, not the
one already running. Proxy nodes scale freely because each has its own scope.

## Security notes

- `auth_url` is admin-only and never logged.
- The ingress token is per process start, delivered over the private broker,
  compared in constant time, and revoked on stop.
- Instance state is encrypted with row AAD and never leaves the server.
- Enrollment supports an auth key in plugin config so N proxies enroll
  without N admin clicks, with the interactive `auth_url` as fallback.

## Out of scope for phase one

Funnel or any public exposure, login via overlay identity, multiple API
replicas, NetBird. The non-goals in [docs/non-goals.md](../non-goals.md)
apply unchanged: a provider proxies Silo's own listeners and nothing else.
