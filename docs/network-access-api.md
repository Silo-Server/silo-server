# Network Access API

Network access providers are plugins that give a Silo deployment an identity on
an overlay network (Tailscale first; the contract is provider-neutral) so
clients reach it without port forwarding or a public reverse proxy. The plugin
owns the overlay client and reverse-proxies to the local Silo listeners; the
server owns supervision, state storage, status and this admin surface. The
design is in [docs/architecture/network-access.md](architecture/network-access.md).

Provider slugs (`tailscale`, `netbird`, …) come from the plugin's manifest
descriptor and key every route below. A slug is a lowercase path-safe token
matching `^[a-z0-9]+(?:[._-][a-z0-9]+)*$`; anything else is rejected as
`422 validation_failed` before lookup. Clients need no changes to use a
deployment reached this way.

## `GET /api/v2/network-access/capabilities`

Authenticated account; no profile required. Lists the installed providers,
read from plugin manifests without launching any plugin:

```json
{
  "revision": "…",
  "state": "available",
  "allowed": true,
  "providers": [
    { "provider": "tailscale", "display_name": "Tailscale", "installation_id": "7" }
  ]
}
```

`state` is `available` when at least one enabled installation declares
`network_access_provider.v1`, `not_configured` otherwise (including worker
modes where the plugin service is not wired). Provider health is not state;
read it from the admin status below. `providers` is always an array. The
document carries an `ETag` and answers `If-None-Match` with 304.

## `GET /api/v2/system/connections`

Authenticated account; no profile required. The client-facing view of the
same providers, for a phone handing a TV an address it can reach:

```json
{
  "revision": "…",
  "state": "available",
  "allowed": true,
  "server_id": "3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35",
  "current": { "kind": "default" },
  "endpoints": [
    { "kind": "public", "url": "https://silo.example.test" },
    { "kind": "provider", "provider": "tailscale", "display_name": "Tailscale",
      "state": "connected", "url": "https://silo.overlay.example.test" }
  ]
}
```

`server_id` is the deployment identity the public
`GET /api/v2/system/identity` also answers. `current` is the access path this
request arrived on (`provider` with the slug when it came through an overlay
listener). `endpoints` lists `server.public_url` as `kind: public` when it is
configured, then every installed provider in slug order with its state on the
API host; `url` is present only while that provider is connected there. Node
addresses, `auth_url` and error text stay on the admin status below. The
identity is self-asserted: clients pair through device login before trusting
an address. Design and client flow:
[docs/architecture/server-identity.md](architecture/server-identity.md).

## `GET /api/v2/admin/network-access/{provider}/status`

Acting admin. Asks the provider's plugin instance on every host that runs it
for its live status, with a ten-second timeout per host:

```json
{
  "provider": "tailscale",
  "hosts": [
    {
      "host": { "id": "api", "role": "api", "name": "Living Room" },
      "state": "connected",
      "hostname": "silo.overlay.example.test",
      "origin": "https://silo.overlay.example.test",
      "addresses": ["127.0.0.1"],
      "provider_version": "tsnet 1.102.4",
      "updated_at": "2026-09-14T09:00:00.000Z"
    }
  ]
}
```

- `host.id` is `api` for the API server and `node:<id>` for a proxy node; it
  is also the instance-state scope the plugin's node keys are stored under.
  The API host comes first, then every enabled proxy node in id order. Proxy
  rows are read over the node's backend URL with the node bearer
  (`GET /network-access/{provider}/status` on the proxy listener) with the same
  ten-second timeout; a proxy that cannot be reached, refuses the bearer,
  runs a build without the routes, or has not resolved its `stream_nodes`
  row yet answers `unavailable` with the reason in `error`. Transcode nodes
  are never listed. Each request reads only the named provider, so another
  provider's timeout cannot hide its status.
- `state` is one of the plugin's states `disconnected`,
  `awaiting_authorization`, `connecting`, `connected`, `error`, or the
  server-side `unavailable`: the plugin process is not running on that host
  (stopped, in restart backoff, failed, or unhealthy) or did not answer in
  time. Provider states outside this published set map to `error`; the
  provider's original value is preserved in `raw_state`. `error` then says
  why, and `updated_at` is absent.
- `auth_url` is present only while `awaiting_authorization`. It is
  admin-only and the server never logs it.
- `origin` is the `scheme://host[:port]` clients on the overlay use for the
  API listener. `addresses` is always an array.
- Fields the provider did not report are omitted.

An unknown provider slug is `404 not_found`. The status is read live and is
not cacheable; the web page polls it every 15 seconds while in front.

## `POST /api/v2/admin/network-access/{provider}/connect`

## `POST /api/v2/admin/network-access/{provider}/disconnect`

Acting admin; refused to non-admins in demo mode. Body is optional:

```json
{ "hosts": ["api"] }
```

`hosts` names the host ids to act on; omitting the body, sending `{}`, or
omitting `hosts` acts on every host. An empty `hosts` array or an id this
deployment does not run the provider on is `422 validation_failed` at
`body.hosts`, and nothing is applied.

Both answer `202 Accepted` with the same body as the status read: the state
each host reached within ten seconds. Connect starts enrollment; the plugin
may keep working in the background (`awaiting_authorization` carries the
`auth_url`; `connecting` becomes `connected` later), so poll status for the
final state. Disconnect tears the overlay listener down and clears the
plugin's desired-connected intent. Hosts not named in `hosts` answer their
current status. A host whose plugin is not running is still acknowledged: its
row says `unavailable` and the command was not applied there; restart the
plugin from `POST /api/v2/admin/plugins/installations/{id}/restart` first.

Both are naturally idempotent: repeating connect converges on one connected
instance per host, repeating disconnect on disconnected. An unknown provider
is `404 not_found`.

Proxy hosts are commanded over the node bearer routes
`POST /network-access/{provider}/connect` and
`POST /network-access/{provider}/disconnect` on the proxy listener, each
bounded by the same ten seconds; the proxy answers the state its own
instance reached. A proxy's overlay origin then reaches stream URL selection
through the node's `/health` report on the next sweep (see
`network_access` in [docs/admin-api.md](admin-api.md)).

## Related surfaces

- Plugin process state (resident supervisor, restart counts, last error) is
  on each installation's `runtime` in the
  [plugin installation API](admin-api.md) and on the Plugins page.
- Proxy nodes report their provider status through the node health check;
  `GET /api/v2/admin/nodes` carries it as `network_access` (see
  [admin-api.md](admin-api.md)).
- The web client's Settings → Network Access page is built on these three
  operations. jellycompat has no equivalent: Jellyfin clients reach the
  Jellyfin listener through the same overlay origin the provider exposes for
  it, and need no provider awareness.
