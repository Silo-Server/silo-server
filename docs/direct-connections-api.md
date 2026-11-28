# Direct Connections API

A native client that signs in with a login session can reach the API host
directly over UDP. This document defines the `/api/v2` operations for that path,
their problem codes, and the rules a client follows. The invariants behind them
are in [Direct Connections](architecture/direct-connections.md). The
Jellyfin-compatible surface has no matching operation.

| Operation ID | Method and path | Caller | Success |
|---|---|---|---|
| `getDirectConnectionsCapability` | GET `/api/v2/capabilities/direct-connections` | signed-in profile | `200` capability document |
| `enrollDirectConnectionDevice` | POST `/api/v2/direct-connections/enrollment` | login session | `200` enrollment with ticket |
| `withdrawDirectConnectionDevice` | DELETE `/api/v2/direct-connections/enrollment` | signed-in profile | `204` |
| `registerDirectConnectionPeer` | POST `/api/v2/direct-connections/registration` | enrollment ticket | `204` |
| `getDirectConnectionNetmap` | GET `/api/v2/direct-connections/netmap` | enrollment ticket | `200` netmap |
| `connectDirectConnectionSignal` | GET `/api/v2/direct-connections/signal` | enrollment ticket | `101` WebSocket |
| `listAdminDirectConnectionPeers` | GET `/api/v2/admin/direct-connections/peers` | acting administrator | `200` peer list |
| `getAdminDirectConnectionsStatus`, `getAdminDirectConnectionsCapabilities` | GET `/api/v2/admin/direct-connections/status` and `/capabilities` | acting administrator | `200` |

The playback operations `startPlayback`, `replanPlayback`,
`getPlaybackBandwidthProbe` and `reportPlaybackBandwidthProbe` also carry the
transport fields in [Media Transport](#media-transport).

## Capability Document

`GET /api/v2/capabilities/direct-connections` is the document a client reads
before it enrolls. Like every capability document it answers with
`Cache-Control: private, no-cache`, an ETag, and conditional reads.

The head holds `revision`, `state` and `allowed`. `state` is `disabled` while
`direct_connections.enabled` is false, `not_configured` when the server has no
direct service, and `available` otherwise. `allowed` is true only when `state` is
`available` and the caller holds a login session that is not a View as user
session, so an API key and a View as user session see false. A client treats an
unknown `state` as unavailable.

| Field | Meaning |
|---|---|
| `protocol_revision` | Opaque revision of the direct protocol; see [Protocol Revision](#protocol-revision). |
| `coordination_url` | Base URL of registration, netmap and signal. Today it is `/api/v2/direct-connections`. |
| `tls_pin` | Base64 SHA-256 of the SubjectPublicKeyInfo that the direct path and the local route present. It never authenticates `coordination_url`. |
| `stun_servers` | The server's STUN servers as `host:port`; empty while the toggle is off. |
| `ack_decimation` | Optional boolean, the value of `SILO_NET_DIRECT_ACK_DECIMATION`. |
| `receive_window_mib` | Optional integer from 4 to 128, the clamped value of `SILO_NET_DIRECT_RECEIVE_WINDOW`. |
| `hosts` | Array, never null. Each entry has `host_id`, `node_key` and `state`. |

`ack_decimation` and `receive_window_mib` are advice to the client, which
receives the media. Nothing negotiates them and the server does not check them. A
client whose QUIC stack cannot apply one ignores it; an absent field means the
client keeps its own default.

- `true` asks the client to send fewer QUIC ACKs for application data on its
  direct connections to this server. `false` asks for the standard RFC 9000 rate.
- `receive_window_mib` is the largest flow-control window, in MiB of 1,048,576
  bytes, that the client grants one QUIC stream. It sets neither the initial
  stream window nor the connection window.

`hosts` lists the API host, `host_id` `api`, and nothing else. Its `state` is
`available` while the engine runs and the replica gate admits new direct
connections, `unavailable` otherwise, and `draining` once the server has begun to
shut down while it is otherwise available. With more than one API replica in the
census the state is `unavailable`, and admin status gives the reason
`multiple_api_replicas`; a replica that holds the engine lease keeps its engine
running and keeps serving devices it already serves (see the signaling gates
below). `hosts` is empty
while the toggle is off. A `not_configured` document has empty `protocol_revision`,
`coordination_url` and `tls_pin`, and omits `ack_decimation` and
`receive_window_mib`.

The engine runs on one API replica, so the host `state` is the answering
replica's view: a replica that does not hold the engine reports `unavailable`
while another replica serves. The document follows the toggle the service last
read. Once that read sees a disable, `state` is `disabled` and `hosts` is empty
at once, even while the engine is still stopping. Until the replica has read the
settings once after it starts, `GET` answers `503 dependency_unavailable` with
`Retry-After: 5`.

`host_id`, `node_key` and the endpoints in the registration and netmap describe
the transport to one engine. They do not identify the server: every deployment's
API host has `host_id` `api`, and the `node_key` derives from the deployment's
`SECRET_KEY`, so it changes when that secret does. A client groups addresses by
`server_id` ([server identity](architecture/server-identity.md)) and checks each
address against it, and it never stores a host ID, node key or endpoint as the
identity of a server.

## Enrollment

`POST /api/v2/direct-connections/enrollment` registers a device's engine and
returns the ticket that the engine presents on every coordination request. The
header `X-Silo-Device-Id` (1 to 128 characters) is required. The body has
`node_key` (`nodekey:` followed by 64 lowercase hex digits, not all zero) and
`connection_path`, which is `automatic` or `direct_only`. The response has
`ticket`, `expires_at` and `coordination_url`.

- Only a login session that is not a View as user session enrolls or renews. An
  API key and a View as user session get `403 permission_denied`.
- While the toggle is off the answer is `409 capability_disabled`.
- A malformed or all-zero `node_key` is `422` at `body.node_key` with code `invalid`.
- An enrollment is a lease of 10 minutes. The ticket and `expires_at` end at the same
  second. Enrolling the same device with the same node key renews the lease and
  returns a new ticket. A client renews at half of `expires_at - now`.
- A new node key for an enrolled device replaces the entry, closes the old key's
  signaling stream and ends the old key's peer entry.
- An entry belongs to the login session that last enrolled or renewed it, and its
  ticket names that session. When another login session of the account enrolls or
  renews the same device, even with the same node key, the entry changes owner and
  the earlier owner's ticket gets `401 invalid_token` on its next registration,
  netmap or signal request. That client enrolls again. A signaling stream that was
  already open under the earlier ticket stays open until that ticket expires.
- The first enrollment of a node key for a device, a key replacement included,
  records the audit action `direct_connection.enrolled` (see "Account and
  permission audit details" in `docs/admin-api.md`) before the ticket is issued. A
  renewal of the same key records none once that row has committed. A renewal can
  record a second row, for example when it overlaps the first enrollment's audit
  or when the first enrollment's audited mark could not be saved. When the audit row
  cannot be written, the call fails with `500 internal_error`, the new entry is
  withdrawn unless another request has since replaced, renewed or audited it, and
  no ticket is returned.
- The server reads the login session again after it writes the entry, even when
  the client has since canceled the request. A session that ended meanwhile gets
  `401 session_expired`, the entry is withdrawn and no ticket is returned.
- When the coordination store reports an error for the write, which can mean that
  it applied the write and lost the reply, the server withdraws the entry that
  write may have made before it answers `503`.
- An account holds at most 32 unexpired enrollments. The 33rd device gets `409
  direct_device_limit`; the server evicts none. A lapsed lease frees its slot.
- A node key that another device holds gets `409 direct_node_key_conflict`. The
  client makes a new key and enrolls again.
- Enrollment answers `503 dependency_unavailable` with `Retry-After: 5` in four
  cases: the coordination store is unavailable, including when the device's stored
  entry cannot be read; the login session or the profile cannot be read; the replica has not yet
  read the direct-connection settings once after it starts; and the server has
  begun to shut down. Any other unexpected failure answers `500 internal_error`.
- Enrollment answers `404 not_found` when the profile was deleted while the request
  ran; the server withdraws the entry the request wrote.

`DELETE /api/v2/direct-connections/enrollment` withdraws the device named by
`X-Silo-Device-Id` and answers `204`. A device with no enrollment also gets `204`.
A client withdraws on sign-out.

The server also withdraws an enrollment on its own, so the ticket stops working
and the signaling stream closes. It does so when the profile that enrolled the device
forgets it in the account's device settings, when a login session ends by
sign-out or revocation (including an administrator revoking that one session),
for every entry that session last enrolled or renewed, whatever device ID it named,
and when a profile is deleted, for every enrollment whose entry records that
profile. Other profiles' enrollments stay, a device last enrolled by another
profile included, and so does an entry that another login session has renewed
since.

It withdraws every enrollment of an account when an administrator disables it,
resets its password, signs it out everywhere, or deletes it, when the account
owner resets the password, or when a sign-in provider check revokes the account's
sessions. A password change that signs out the account's other sessions
withdraws every enrollment except one that the changing session itself last
enrolled or renewed for the device that session carries. When that device cannot
be read, or the session carries no device ID, it withdraws all of them; a
withdrawn device's client gets `401 invalid_token` and enrolls again. A store
outage during a withdrawal is logged, and the entry lapses at the end of its lease.
A withdrawal runs to its end when the request that caused it is canceled.

## Registration, Netmap and Signal

The three operations take the ticket in the header `Silo-Signaling-Ticket`. They
need no access token or cookie. The engine appends `registration`, `netmap` and
`signal` to the base URL, as the shunpike module's wire protocol
(`coordination/SPEC.md` in the module) defines.

Each call runs three gates in this order and stops at the first that fails:

1. The ticket must verify and name a live enrollment with the same node key that
   the ticket's login session last enrolled or renewed: otherwise `401
   invalid_token`. Every ticket refusal returns the same answer.
2. While the toggle is off: `409 capability_disabled`.
3. When the API host's state in the capability is `unavailable` (a `draining`
   host passes this gate):
   `503 dependency_unavailable` with `Retry-After: 5`, with one exception. While
   this replica's engine runs but the replica gate refuses new direct connections
   (more than one API replica, or a failed census, on the lease holder), a device
   whose enrollment holds a live registration (registered within its 90-second
   presence) passes, so its established session continues. Every other device is
   refused, one idle past its presence included. The detail names which case
   applies: "This server replica does not run the direct connection engine; try
   again shortly." or "This server replica is not accepting new direct
   connections; try again shortly."

The second gate reads the toggle the service last read, not the settings store.
Until the replica has read the settings once after it starts, every call that
passes the first gate answers `503 dependency_unavailable` with `Retry-After: 5`;
a later failed read leaves the last good value in force. A store failure,
including a store timeout, is also `503 dependency_unavailable` with
`Retry-After: 5`. Any other unexpected failure is `500 internal_error`. Only a
request body or request context that ends on its own is `408 request_timeout`.

`POST /api/v2/direct-connections/registration` stores the engine's discovery key,
endpoints and confirmations. The body is the module's registration JSON, at most
65,536 bytes, with at most 64 endpoints and 64 confirmations. Its `node_key` must
equal the ticket's, or the answer is `401`. A body over the limit is `413
payload_too_large`. A body that is malformed or breaks a rule of the module is `422
validation_failed` with no `errors` entries. The engine's client sends no
`Content-Type`, so the operation accepts a body with none; a present type other
than JSON is `415 unsupported_media_type`, and a body not received in time is `408
request_timeout`. The answer is `204`. The operation and the netmap share the
`direct_signaling` rate limit, per client IP: beyond it the answer is `429
rate_limited` with `Retry-After`.

`GET /api/v2/direct-connections/netmap` returns the hosts the device can reach. The
optional query `version` is the `version` of the netmap the caller holds. A version
that equals the current one holds the call for up to 25 seconds, until the
netmap changes or the hold ends; any other version, or none, answers at once.
The response carries `Cache-Control: no-store` and these members:

- `version`, an opaque content hash.
- `lease_seconds` (default 90) and `refresh_seconds` (default 30).
- `hosts`: the API host with `label` `api`, `node_key`, `disco_key`, `endpoints`,
  `home_region` (always 0) and, when the host has observed the device, a
  `confirmation` of `peer`, `disco_key` and `endpoint`.

The server never sends `regions` or a host `origin`. Once the server is stopping, every
netmap call answers at once. A call whose request context ends before the netmap
is ready answers `408 request_timeout`.

`GET /api/v2/direct-connections/signal` opens the signaling stream. The request
carries `Upgrade: websocket`, `Sec-WebSocket-Protocol: derp` and the ticket. The
refusals that check the ticket and the server are Problem Details: `401
invalid_token` for the ticket, `409 capability_disabled` while the toggle is off,
`503 dependency_unavailable` (with `Retry-After`) when this replica does not
run the engine, the store is down, or the replica has not read the settings yet,
and `500 internal_error` for any other unexpected failure. A request that does not offer `derp`, or
that is not a WebSocket handshake, is a plain-text `400`. The success answer is
`101` with `Sec-WebSocket-Protocol: derp`. The stream carries DERP frames as binary messages
under the module's per-pair budget. It closes when the ticket expires, when the
enrollment is withdrawn or replaced, and when the server shuts down. A replica
that holds the stream and did not take the withdrawal closes it on its next sweep
pass, which runs at most 10 seconds after the previous pass, once the store
confirms the withdrawal. A pass that cannot read the store leaves the stream for
the next pass, and ticket expiry stays the backstop. The
`direct_signaling` limit does not apply to the stream.

## Peers Read

`GET /api/v2/admin/direct-connections/peers` (`listAdminDirectConnectionPeers`)
lists the devices that have a direct path to the API host now. It requires the
acting administrator; no other caller sees an endpoint. The response is `{items}`,
ordered by `user_id` and then `device_id`, with no pagination because the list
is bounded by the host's peer cache. Each item has:

| Field | Meaning |
|---|---|
| `host` | The `NetworkAccessHostRef` of the API host. |
| `user_id`, `username` | The account; `username` is empty when the name cannot be read. |
| `device_id`, `device_name` | The device; `device_name` is absent when it has none. |
| `class` | `direct`. A client treats an unknown value as `direct`. |
| `address_family` | `ipv4` or `ipv6`, of the peer endpoint. |
| `peer_endpoint` | The device's observed UDP address as `host:port`. |
| `local_port` | The API host's UDP port. |
| `connected_at` | When the device's path last became direct. |

The list shows the answering replica's view: it holds the devices that have a
direct path to the engine on that replica. It is empty while that engine is off,
and a replica that does not run the engine always returns an empty list, which
means only that. The item has no rate field. A missing service answers `503
dependency_unavailable`.

## Problem Codes

Registration, netmap and enrollment use the shared problem catalog. Two types are
new, because each needs its own client behavior. The signal operation answers its
gate refusals with the same types (see above).

| Status | Type or code | Operation | Client behavior |
|---|---|---|---|
| `409` | `direct_device_limit` | enrollment | Show the limit. |
| `409` | `direct_node_key_conflict` | enrollment | Make a new node key and enroll again. |
| `409` | `capability_disabled` | enrollment, registration, netmap | Direct connections are off; use the regular path. |
| `401` | `invalid_token` | registration, netmap | Enroll again. |
| `401` | `session_expired` | enrollment | The login session ended; sign in again. |
| `403` | `permission_denied` | enrollment | An API key or a View as user session cannot enroll. |
| `408` | `request_timeout` | registration, netmap | Retry. |
| `413` | `payload_too_large` | registration | The body is over 65,536 bytes. |
| `415` | `unsupported_media_type` | registration | Send JSON or no `Content-Type`. |
| `422` | `validation_failed` | enrollment, registration, probe result | `body.node_key` and `body.connection_receipt` carry codes; see below. |
| `429` | `rate_limited` | registration, netmap | Wait for `Retry-After`. |
| `503` | `dependency_unavailable` | every operation in this document; the capability read answers it only while the replica has not read the settings, and reports a missing engine as `state` | Wait for `Retry-After`. |
| `500` | `internal_error` | capability read, enrollment, registration, netmap, signal | An unexpected failure; retry later. |

`Retry-After` is 5 seconds for a store outage, a draining server, a replica
without the engine and a replica that has not read the direct-connection settings
yet. It is 30 seconds when the server has no direct service.

Error codes under `422`:

- `body.node_key` with `invalid`: the key is malformed or zero.
- `body.connection_receipt` with `missing`: an enrolled device sent no receipt.
- `body.connection_receipt` with `invalid`: the receipt is not the probe host's for this account and device.
- `query.media_transport` with `invalid`: the value is not `regular` or `direct`.

A media request that reaches the direct path while the server cannot tie the
peer to an enrolled device is answered `421` with a plain-text body, before any
router sees it. A client reads `421` as "the path is not ready".

## Media Transport

`media_transport` names the path that carries the media. The values are `regular`
and `direct`. A missing field means `regular`.

- A client sends `media_transport` in the `startPlayback` and `replanPlayback` bodies, and as
  the query parameter of `getPlaybackBandwidthProbe`. A value outside the two
  is `422`.
- A client sends `direct` only when it has no active local route, the device is
  enrolled and its engine runs. Downloads never ask for `direct`.
- Every v2 `playback_plan` carries `media_transport`. It is `direct` only when the
  request asked for `direct` and the API host serves the media over a live,
  confirmed direct path. In every other case it is `regular`: the client asked
  for it, the device is not enrolled, direct connections are off, the path is not
  live, or a media proxy serves the media. A reused session transport and a seek
  re-anchor keep the session's value.
- When the plan says `direct`, the client sends the requests it would send to its
  saved base URL, and only those, through the engine to the API host.

A device enrolled with `connection_path` `direct_only` never gets a proxy origin
from start, replan, seek or the probe redirect: the API host serves its media. The
planner reads the device's enrollment once per request. When it cannot read it, the
planner treats the device as `direct_only`. When the operator sets `proxy_only`
egress for every delivery, a `direct_only` device has no legal route for any media
and the start or replan ends in the final `routing_policy_unsatisfied`; a start or
replan whose enrollment cannot be read answers `503` `dependency_unavailable` with
`Retry-After` and stores no decision, so the client sends the same request again.
A replay of a start or a completed replan made before the device switched to
`direct_only` returns the stored decision, which can still hold a proxy URL; every
new start, replan and seek follows the rule.

A seek of a session whose plan is `direct` is planned without proxy shapes: it stays
on the API host when a legal API route exists and ends in the final
`routing_policy_unsatisfied` when none does. A seek of a regular session is planned
as before.

### Probe Receipt

The probe reports the path it measured, so a result lands under the right
connection. When a request names its device in `X-Silo-Device-Id`, the host that
serves the probe bytes sends the header `Silo-Connection-Receipt`. The receipt
is a signed token that names the account, the device, the serving host and the
connection class. It is valid for 2 minutes.

- The API host sends class `regular` for a request over TCP, even when the
  device also holds a live direct path. It sends class `direct` for a request over
  the direct path only when the peer is the enrolled device of the same account and the
  API host's path to it is live and confirmed, and still the path the request
  arrived on. Otherwise it sends none.
- A media proxy sends class `regular` for a probe token that names a device. A
  proxy serves an enrolled device's probe only when it advertises
  `connection_receipt_v1` and knows its own node ID; otherwise the redirect is
  `503 dependency_unavailable`, never a measurement of the API path.

A client that runs the paired test sends the probe with `media_transport=regular`,
then with `media_transport=direct`, and returns the header value of each as
`connection_receipt` (at most 2,048 characters) in `reportPlaybackBandwidthProbe`.
The server answers like this:

| Case | Answer | Recorded under |
|---|---|---|
| Device not enrolled, no receipt | `204` | the regular key of the result request |
| Device enrolled while the toggle is on, no receipt | `422` `missing` | nothing |
| Bad signature, another account or device, wrong token type, malformed claims | `422` `invalid` | nothing |
| Expired receipt | `204` | nothing |
| Direct receipt whose generation is no longer the API host's current one, posted to the replica that runs the engine | `204` | nothing |
| Valid `direct` receipt from the API host, posted to the replica that runs the engine, with the current generation | `204` | the receipt's key |
| Valid `direct` receipt from the API host, posted to a replica that does not run the engine | `204` | the receipt's key |
| Valid `regular` receipt | `204` | the regular key of the result request |

Only the replica that runs the engine knows the path's generation. A replica
without the engine records a valid direct receipt under the receipt's key without
that check. The key is a keyed hash of the address the probe measured, so the
result stays filed under the path it measured, and a receipt of an earlier path
can still be recorded there for the 2 minutes it is valid. A direct receipt from
any other host records nothing. A failed read of the
device's enrollment, on a result with no receipt, answers `503
dependency_unavailable`.

## Client Contract

These rules apply to any native client that signs in to `/api/v2` with a login
session. The saved base URL is the server address the client saved, path
included, for example `https://example.com/silo`. A "First-party engine" note
maps a rule onto the shunpike module's Go API; a client with its own engine
follows the wire rule.

1. Feature detection: read the capability document. Use direct only when `state`
   is `available`, `allowed` is true, the `api` host is `available`, the client
   knows `protocol_revision`, and the saved base URL meets rule 8. Do not
   sniff versions. Refetch after sign-in, profile change, foregrounding and a
   capability invalidation event.
2. Enrollment: enroll with the login session, `X-Silo-Device-Id`, the engine's
   node key and `connection_path`. A node key belongs to one account on one
   server: a device signed in to several servers or accounts makes a separate
   key for each and never reuses one. Renew at half of `expires_at - now` and hand each
   new ticket to the engine. On `401 invalid_token` from an engine operation,
   enroll again. On `409 direct_device_limit`, show the limit. On `409
   direct_node_key_conflict`, make a new node key and enroll again. Withdraw on
   sign-out.
3. Engine: take the coordination base URL from `coordination_url`. A value that
   starts with `/` is appended to the saved base URL and keeps its path. An absolute `https`
   URL is used as is. The engine's TLS uses the platform's system trust, never `tls_pin`. Every
   coordination request carries `Silo-Signaling-Ticket`; the engine never holds an
   access token or cookie. Signaling dials the signal operation as a WebSocket
   with subprotocol `derp`. The client applies `ack_decimation` and
   `receive_window_mib` to new direct connections after each refetch. First-party
   engine: pass the base URL as `coordclient.Options.BaseURL` and return the
   ticket from `Options.Headers`; start `n4.Policy` from `n4.DefaultClientPolicy()`
   and set `ACKDecimation` and, in bytes, `StreamWindow`; `InitialStreamWindow`
   keeps the default of 4 MiB, and the module derives the connection windows
   from the stream windows.
4. Media transport: send `media_transport=direct` on start, replan and the probe
   only when the local route is not active, the device is enrolled and the engine
   is up; otherwise send `regular`. When the plan says `direct`, carry the
   media requests for the saved base URL (manifests, segments, progressive
   streams, subtitles and fonts) through the engine to the API host's node key.
   Remove the saved base URL's path from a request before it goes over the direct
   path or into a loopback gateway: the direct listener serves the native router at
   its root, and no reverse proxy removes the prefix. The logical URL
   `https://example.com/silo/api/v2/stream/abc` goes over the direct path as
   `/api/v2/stream/abc`. Logical URLs stay on the saved base URL.
5. Direct only: a `direct_only` client accepts a `regular` plan only on a URL its
   active local route covers. Otherwise it stops and tells the user, with a
   one-tap switch to Automatic. A failure of a `direct_only` client does not
   lower its learned bandwidth limit.
6. Probe: a client that runs the paired bandwidth test uses the receipt rules in
   [Probe Receipt](#probe-receipt).
7. Path labels: a client that shows the path uses Local, Direct, Direct (mapped
   port) and Regular.
8. Trust: direct is available only when the saved base URL is `https` with a
   certificate the platform's system trust accepts (see
   [server identity](architecture/server-identity.md)). A plain-HTTP or
   app-verified self-signed address never enrolls. Control and signaling stay
   on that HTTPS address. API calls do not go over the direct path.
9. `421`: a `421` on a media request over the direct path means the path is not
   ready. An `automatic` client replans with `media_transport=regular`. A
   `direct_only` client stops as in rule 5. The client learns no bandwidth
   limit from it. The server treats that replan as a path failure, not a recipe
   failure, so the plan that was playing stays eligible on the regular path.

## Protocol Revision

`protocol_revision` names one version of the direct protocol between a client
engine and the server. The shunpike module owns the value: `coordination/protocol`
exports the string constant `ProtocolRevision`, and the server publishes it
unchanged. It is `"1"` today. It covers:

- the registration and netmap JSON shapes, limits and decoder rules in the module's
  wire protocol;
- signaling, which is DERP over a WebSocket with subprotocol `derp`, and the
  control envelope;
- discovery, as the pinned Tailscale fork sends it;
- the direct transport: QUIC version 1 on the confirmed UDP path, HTTP/3 with ALPN
  `h3`, and TLS 1.3 authenticated by the `tls_pin` pin, with no client
  certificate.

It does not cover the `/api/v2` operations or the capability document, which
follow the additive rules in the [API contract](architecture/api-contract.md).
The registration and netmap decoders refuse unknown fields, so any change to those
shapes, an added field included, needs a new revision.

A client compares the advertised value for exact equality with each revision it
supports. It does not order values, read them as numbers, or use a module tag or the
capability head's `revision` in their place. A client that does not know the
advertised revision does not enroll and does not use direct; it sends
`media_transport=regular`.

## Administrator Status Read

`GET /api/v2/admin/direct-connections/status` requires an acting administrator.
Operation ID: `getAdminDirectConnectionsStatus`.
The setting `direct_connections.enabled` gates readiness probes and the
API host's direct engine. The engine also requires a valid UDP port and permission
from the replica gate. Status reads report that lifecycle; refreshes rerun probes.
`GET /api/v2/admin/direct-connections/capabilities` (operation ID
`getAdminDirectConnectionsCapabilities`) requires the same acting administrator.
Its common head contains `revision`, `state`, and `allowed`; the typed `status`
and `refresh` fields report support for readiness reads and explicit refreshes.
The state is `available` when the service is wired and `not_configured` otherwise.
It does not run probes or depend on the setting or current probe health.
Responses use `Cache-Control: private, no-cache`, an ETag, and conditional reads.

The optional boolean query `refresh` defaults to false. A normal read returns
the last completed snapshot, waiting for an initial check if needed. A refresh
starts one check or joins the check already in flight. Each check has a fifteen-
second timeout, and the settings read that starts a check has its own fifteen-second
timeout, so a request that must read settings and start a check can take about
thirty seconds. Canceling the request stops its wait, not the shared check.
A missing service, or a settings read that fails or runs out of time, answers
`503 dependency_unavailable` with `Retry-After`.

A successful response contains `enabled` and a `hosts` array with the API host
only. Each host has `host` (the existing `NetworkAccessHostRef`, ID `api`),
`checked_at` (an Instant), `features`, `knobs`, and, after binding, `listeners`.
Each API process keeps its own result. With several API replicas behind a load
balancer, the replica that answers reports its own process under the same `api`
host, so consecutive reads can come from different replicas. The readiness
rows, the `engine` row and the peers list are all that replica's view. A replica
that does not hold the engine lease shows the engine `off` and an empty peers
list, and says nothing about the replica that does. The response does not yet
identify the replica.
Generated [OpenAPI](../contracts/api/v2/openapi.json) defines the exact schemas
and additive feature, state, and reason vocabularies.

Each feature contains `feature`, `kind` (`local` or `network`), `state`, and
optional `reason`, `requested`, `granted`, and `detail`. Byte counts in feature
rows are decimal strings; Linux socket-buffer grants report the usable size,
half the kernel's doubled readback (socket(7)).
Disabled network rows are `off` with reason `disabled`, with no network probes.
When enabled but router discovery is disabled, `port_mapping` is `off` with
reason `admin_off`. Probe errors and timeouts produce `degraded/probe_failed`
for the affected observations. A probe that gets no STUN reply leaves
`nat_mapping`, `ipv4`, and `ipv6` at `off/probe_failed`. When a NAT probe
succeeds but a family has no available address, its `ipv4` or `ipv6` row is
`off/not_available`, except for a family that no configured STUN server can
answer in. The prober sends to an IP literal only in its own family, so a list
of IPv4 literals cannot show whether IPv6 works. That family's row reads
`unknown/not_measured`. A host name, or an entry that does not parse, covers both
families.
Hairpin and mapped-address reachability remain
`unknown/not_measured`; reachability includes a mapped address when observed.
On a non-Linux platform the probe's local rows, including `direct`, are
`off/unsupported_platform`. The `engine` row is added by the engine lifecycle and
does not follow that rule.

The `engine` feature has kind `local`. It is `on` while the engine runs and `off`
otherwise. Its reasons include `disabled`, `udp_port_invalid`,
`udp_port_bind_failed`, `engine_start_failed` and `state_store_unavailable`.
`udp_port_bind_failed` means the engine could not bind the requested UDP port.
`engine_start_failed` means the engine stopped or could not start for any other
reason: its construction failed, or the HTTP/3 server stopped while the engine
ran. After an HTTP/3 stop the row shows it from the stop, including while the
restart is pending. The engine restarts once, after a settings read and, with
Redis, a successful lease renewal or acquisition that both completed after the
stop; the next such success permits the restart, and a failed read or renewal
keeps it pending. Without Redis the read alone permits it. After a second stop
the engine stays off until the settings or the replica gate change. A running
engine reports `state_store_unavailable` while the coordination store refuses a
host or confirmation write, until the next write succeeds. Both
`engine` and `direct` can report these replica reasons:

| Reason | Meaning |
|---|---|
| `multiple_api_replicas` | The census sees more than one API process. |
| `replica_census_unavailable` | The census fails, Redis is configured but this process has no usable client or presence identity, or `server.mode` is `api` and Redis is not configured. |
| `replica_lease_pending` | No lease is held locally, acquisition is pending or unavailable, or a fresh lease is inside its 90-second hold. |
| `replica_lease_held` | Another process holds the lease. |

The `direct` row, when otherwise `on`, is `off` whenever new direct connections
are unavailable: the replica gate says so, or the engine is stopped with
`udp_port_invalid`, `udp_port_bind_failed`, or `engine_start_failed`, and the row
carries that reason. An `on` `engine` row can carry
`multiple_api_replicas` or `replica_census_unavailable`: a holder keeps serving
established sessions while its last successful renewal is less than 60 seconds
old. Engine state and replica permission are read live; `checked_at` identifies
the probe snapshot. See the [gate table](architecture/direct-connections.md#replica-census-and-lease).

Listener rows carry `listener`, `requested`, `granted`, `state`, `reason`, optional
`errno`, and `send_buffer`. The nested send-buffer requested and granted values
are integers; the grant uses the same usable-byte convention as feature rows.
Knob rows carry `env`, `default`, `effective`, optional `allowed`, `min`, and `max`,
and optional `requested` and `reason` when startup parsing adjusts a request.
The bounds are decimal strings for numeric knobs, which omit `allowed`.
`internal/tuning/tuning.go` defines these numeric rows:

| Knob | Default | `min` | `max` | Meaning |
|---|---|---|---|---|
| `SILO_NET_TCP_SEND_BUFFER` | `33554432` | `1` | `1073741823` | TCP send-buffer request in bytes; `off` disables the request. |
| `SILO_NET_DIRECT_UDP_PORT` | `8094` | `1` | `65535` | Fixed engine UDP port; invalid input becomes effective `0` and prevents binding. |
| `SILO_NET_DIRECT_RECEIVE_WINDOW` | `128` | `4` | `128` | Client maximum stream receive window in MiB; it does not affect downloads or playback speed. |

Receive-window values outside the bounds clamp with reason `clamped`. Malformed
receive windows use the default with reason `invalid_default`; invalid UDP ports use
`0` with reason `invalid`. `SILO_NET_DIRECT_GSO` and
`SILO_NET_DIRECT_ACK_DECIMATION` allow `on` and `off`, defaulting to `on`.
`SILO_NET_DIRECT_BBR_LOSS` allows `both`, `no_lower`, `loss_thresh`, and `off`,
defaulting to `both`. An unrecognized value of one of these three turns that knob
`off`, with reason `invalid`, as for the TCP knobs. These adjustments do not prevent
process startup. `SILO_NET_DIRECT_RECEIVE_WINDOW` and
`SILO_NET_DIRECT_ACK_DECIMATION` are client policy: the rows report the configured
values, and no client receives them yet. They do not change the
server's own engine or transport policy.

The architecture and privilege invariants are in
[Direct Connections](architecture/direct-connections.md). The web administrator
consumer uses this read and the peers read; Apple, Android, and Jellyfin have no
matching readiness operation.
