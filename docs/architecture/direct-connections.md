# Direct Connections

The administrator setting `direct_connections.enabled` controls readiness
checks and the direct engine. It defaults to false. API and integrated modes
start the engine only when this setting is true, the configured UDP port is valid,
and the replica gate permits it. Proxy and transcode modes run neither the engine
nor the self-check. The engine and gate live in `internal/directconn`.

Devices enroll through the native API and then use the coordination operations
with a ticket; [Direct Connections API](../direct-connections-api.md) defines
the operations and the rules a client follows.

`internal/directconn` reads effective settings, maps observations to feature
states, and reports the API host. While disabled it calls neither the STUN nor
the port-mapping prober. Local socket capability reads send no packets.
`direct_connections.port_mapping` defaults to true and independently gates router
service discovery during checks and port mapping by the running engine.
The discovery probe creates no port mapping.
`direct_connections.stun_servers` defaults to
`stun.cloudflare.com:3478,stun.l.google.com:19302`. Its normalized list contains
at most eight host:port entries. Normalization writes an IPv4-mapped IPv6 literal
as the plain IPv4 endpoint before it removes duplicates; native IPv6 literals and
DNS names stay as written.

Checks start at process startup, on changed effective settings, and on an explicit
status refresh. Same-process saves and settings-change events both reread settings.
Duplicate signals do not rerun probes. Checks run on the service's lifetime context,
with one check in flight and a fifteen-second timeout. Settings changes during a
check coalesce into one reread and, if needed, one follow-up with the latest values.
A check that started while enabled finishes both of its phases, and the follow-up
check, which runs with the disabled values, sends no probe.
A request canceled while waiting does not cancel the shared check. Every settings
read, including its wait for an earlier read, is bounded by the same fifteen seconds
and ends with the service's lifetime context. The two bounds are separate: each
phase is bounded, not the whole request, so a status request that reads settings and
then waits for the check it started can take about thirty seconds unless its own
context ends sooner. After a failed read, the next status read without refresh reads
again and starts a check only if the values differ from the last check's. With a
completed snapshot it returns that snapshot at once, without waiting for the reread
or the new check. The reread runs in the background on the service's lifetime
context under the same fifteen seconds, one at a time; the following read returns
the check once it completes. If the reread fails, the snapshot stands and the next
read tries again. The service also rereads settings once a minute on its own, whether
or not the feature is on, because Redis drops a settings-change event published while
a subscriber reconnects; a status read starts the same reread once the last successful
settings read is a minute old. Both share the one background reread, and a minute tick
while it runs does nothing. A missed change or a failed read therefore reaches the
engine and the snapshot within about a minute plus one bounded read, instead of
waiting for a refresh or a restart. A refresh starts a check even when the values are unchanged, or
joins the running one, and returns the read error if its own read fails. A refresh
that joins a running check reads settings first only after a failed read, and
schedules a follow-up check when the values differ from the running check's.

The capability document and the coordination operations read no settings. They use
the toggle and STUN list that the service last read, taken under one lock with the
engine's state. A disable shows at once after it is read, even while the engine is
still stopping, and a change missed by an event reaches them within the minute
reread above. Before the first successful read they answer `503
dependency_unavailable`, as the [API document](../direct-connections-api.md)
describes.

## Replica Census and Lease

The census and the lease run only while direct connections are on. The lifecycle
starts them when settings enable the feature, before the HTTP handler exists, and
stops them when settings disable it: it closes the engine first, then releases the
lease. A disabled feature runs neither; the presence member that network access
shares still refreshes.

Each API process keeps one member in the Redis sorted set `silo:api-replicas`
(`cache.APIReplicaPresence`, shared with network access providers). The member is
the process's presence ID, scored with its expiry in Redis milliseconds. Members
expire after 90 seconds and refresh every 30 seconds. One script call prunes expired
members, writes this process's member if it is missing, and returns the count. The
count therefore includes this process, a lone member of another process is never
read as this one's, and the cost does not grow with the keyspace. Expiry compares
scores with Redis `TIME`, so no host clock enters. A failed call is a census error.
A process that predates the set writes no member and is not counted; it runs no engine.
Once shutdown withdraws the member, the census no longer writes it, so a restart
within the TTL does not find a stale replica.

The engine lease uses `silo:direct:engine-lease`, with this process's presence ID
as its value. The acquisition script writes the value with `PX 90000` when the key
is free and refreshes the TTL when the key already holds this process's value, so
a retry after a lost reply to an applied write succeeds. Renewal every 30 seconds
compares the holder ID before `PEXPIRE`; release compares it before `DEL`. A failed
lease operation is retried after 5 seconds, so one failed renewal leaves the next
attempt well inside the 60-second self-stop. The release has its own 2-second bound
and runs even when the shutdown context has already ended. The census and the lease
operation of one update each have their own 2-second bound, so a slow census cannot
expire the renewal. The presence writes and the lease use a Redis client whose
sockets honor context deadlines, so a Redis that stops answering ends each call at
its bound. The lifecycle stops the engine once the last successful renewal is older
than 60 seconds, independently of an in-flight census or renewal.

A grant is one stretch of ownership of the key. Redis's confirmation of an
acquisition opens it; an attempt that fails, even one whose write applied, opens
nothing, and the retry is idempotent. A renewal answered `0`, a release, or a
renewal that completes after the grant's own 60-second deadline ends it, so
the next success is a new grant, also for the same process. A fresh grant permits
the engine only after 90 seconds, the lease TTL, counted from the send time of the
acquisition or renewal that opened it. The process that held the key before it can
have lost the key at any point up to that send, so it stops permitting traffic
within 60 seconds of its last renewal and has the remaining 30 seconds to tear down.
The hold is the same margin a crash takeover has. Every enable waits the hold: the census and lease start when the
feature is turned on, and the engine starts when the hold ends. A grant lost to
another process makes the engine stop with `replica_lease_held` or
`replica_lease_pending`, and the same process's next grant waits again.

A running engine's packet connection is fenced by its grant. Once the grant ends
or its local deadline (last renewal plus 60 seconds) passes, the connection handed
to HTTP/3 drops every packet in both directions, including segmented writes, before
the engine is drained. The drain of a stop the lease forces closes connections at
once, with no grace period. No local check closes the gap of a process paused
between a check and a send, or of two writable Redis authorities; the 30 seconds
between the 60-second self-stop and the 90-second TTL bound the first case. The
fence covers the HTTP/3 path Silo owns. The engine's own discovery and transport
paths in shunpike are outside it.

Redis is required for the engine in `server.mode = api`: separate API processes
share the lease only through Redis. A single integrated process without Redis runs
the engine with no census and no lease. Two integrated processes without Redis can
overlap during a rolling restart; that case is left open.

The census and the lease run on every replica whether or not
`direct_connections.enabled` is true, so a sole replica holds the lease while the
feature is off. The cost is the census, the renewal and the coordination workers
on each replica. The presence ID is also the fence value of the store's host and
confirmation writes (see [Store Authority](#store-authority)).

`internal/directconn/replica.go` separates permission to keep the engine running
from availability for new direct connections:

| Redis | Census | Lease | Engine May Run | Available | Reason |
|---|---|---|---|---|---|
| Absent, `server.mode` not `api` | -- | -- | Yes | Yes | None |
| Absent, `server.mode = api` | -- | -- | No | No | `replica_census_unavailable` |
| Present | One replica | Held, hold over | Yes | Yes | None |
| Present | One replica | Held, fresh grant inside its hold | No | No | `replica_lease_pending` |
| Present | One replica | Free or acquiring | No | No | `replica_lease_pending` |
| Present | One replica | Held by another process | No | No | `replica_lease_held` |
| Present | More than one replica | Held | Yes | No | `multiple_api_replicas` |
| Present | More than one replica | Not held | No | No | `multiple_api_replicas` |
| Present | Error | Held | Yes | No | `replica_census_unavailable` |
| Present | Error | Not held | No | No | `replica_census_unavailable` |
| Configured, unusable | -- | -- | No | No | `replica_census_unavailable` |

A held lease in this table means a successful renewal less than 60 seconds old.
A holder can keep established sessions when the census fails or finds another
replica. The gate makes the `direct` status feature unavailable in those cases.
The separate `engine` feature reports whether the engine runs.

The capability reports the API host available only while the engine runs and
the gate is available. While the host is unavailable and the engine still runs,
the signaling gate (registration, netmap and the signal stream) admits only a
device whose entry holds a live registration: lease and presence both unexpired,
the rule by which the module's peer directory admits a device to the engine's
peer set (`Coordination.Established`). Every other device, one idle past its
90-second presence included, gets `503` until the gate reopens. So an established
session keeps its registration fresh and stays in the engine's peer set, and no
new device starts. A replica whose engine does not run refuses every device.

## Engine Identity and Lifecycle

`internal/directconn/keys.go` derives the API node key with
HKDF-SHA256 from `SECRET_KEY`, a nil salt, and the label
`silo/direct-node/v1|api`. It passes the 32 output bytes to
`engine.NodePrivateFromBytes`, which clamps them.

The UDP port defaults to 8094. An invalid port leaves the engine off with
`udp_port_invalid`; a start that fails because the port is taken reports
`udp_port_bind_failed`, and any other start failure reports `engine_start_failed`.
A candidate engine built while its permission lapsed, because the lease or the
settings changed during construction, is closed unpublished and is no failure.
The engine advertises the port it binds, so a container's published port must
equal it: Compose maps `SILO_NET_DIRECT_UDP_PORT` (default 8094) to the same
container port over UDP and passes the variable to the container, and the image
exposes `8094/udp`.
`internal/directconn/engine.go` uses one start function and one stop function.
No engine starts before the HTTP handler it serves exists: the API host calls
`HandlerReady` once the router is built. Until then a settings read records its
values and starts nothing, and `HandlerReady` runs the start only if settings and
the gate still allow it. This keeps the lease deadline in force during a slow
startup, because the lifecycle never waits for the handler.
Each start loads the peer cache's devices into the engine and the path tracker,
removes the previous holder's API host entry and confirmations from the
coordination store (see [Store Authority](#store-authority)), writes the engine's
own API host entry, and only then constructs a fresh HTTP/3 server. Until the
removal succeeds, the engine writes no host entry or confirmation and the
signaling dial waits; the next event or 30-second tick retries it. A peer list
the running engine rejects is not stored, so a restart never replays it. Each
stop shuts down that server before calling `Engine.Close` without a timeout
wrapper. One goroutine reads `Engine.Changed`, writes the host entry and the
confirmations (see [Store Authority](#store-authority)), and ends before that
engine closes. If the engine reports a different local port after a rebind, Silo
stops it and tries once on the configured port. A failed start waits for a settings or gate
change before another attempt: the first successful settings read, changed values,
a settings-change event, or a changed replica gate retries it. The minute reread of
unchanged values does not, and neither does the follow-up read the service schedules
itself after a check; only a change in the values they read does.

If HTTP/3 `Serve` returns while its engine is current, nobody asked it to stop, so
Silo withdraws the engine. One step, before any blocking one, detaches the engine,
turns the `engine` row `off` with `engine_start_failed`, and records the settings
reads and lease successes counted so far. Then the row is published and the event
goroutine is joined, so no further host write follows. The API host entry and its
confirmations are removed under the engine's lease fence and a 5-second bound, so
devices are not offered a host whose HTTP/3 server no longer serves. Then the
engine is drained and closed. A failed or refused removal is logged, and the entry
lapses or the next engine's start removes it. The engine restarts once,
on the first lifecycle pass after both a successful settings read and a successful
lease renewal or acquisition that completed after that step, if settings still enable
it and the gate permits it. The exit starts a background settings reread. The replica
monitor counts successful lease operations and wakes the lifecycle after every census,
also before the first status snapshot. The next qualifying lease operation, normally
the next census, lets the restart run; a failed read or renewal keeps it pending until
a later one succeeds. Without Redis there is no lease and the read alone is enough.
Settings that disable the engine, or a lost lease, cancel the pending restart. A
second unexpected exit before any settings or gate change leaves the engine off with
`engine_start_failed` until one occurs.

`Engine.Close` drains the engine's receive path until the socket reports an
error, with no deadline, even when the QUIC listener has stopped reading. Silo
therefore calls it without a timeout wrapper and only after the HTTP/3 server has
drained.

The server uses `n4.DefaultServerPolicy()` with the configured BBR loss policy.
Its receive windows keep the quic-go defaults and ACK decimation stays off.
The receive-window and ACK-decimation knobs do not change the server policy.
The engine config sets no `RelayedDataAllowed`, so relayed sends and receives
are denied for every device: UDP between the device and the API host is the only
data path. The engine's signaling region is the in-process coordination region
(see [Signaling](#signaling)).

## HTTP/3 Request Path

`internal/directconn/http3.go` builds an N4 HTTP/3 server on the engine's
`PacketConn`. It uses a clone of the LAN TLS configuration with ALPN `h3`,
preserving its certificate and SPKI pin. This server does not enable WebTransport.

Before the root handler resolves client IPs or checks ingress tokens, the wrapper
in `internal/directconn/wrapper.go` performs these steps:

1. Delete `Forwarded`, every `X-Forwarded-*`, `X-Real-IP`, and both network access
   ingress headers.
2. Read the admitted peer from `n4.PeerFromContext` and its observed UDP endpoint
   from `Engine.Path(peer).Endpoint`. A missing peer or endpoint returns
   `421 Misdirected Request` without calling the router.
3. Look the peer up in the peer cache. A peer that the cache does not hold, or
   whose account is not numeric, also returns `421`.
4. Set `RemoteAddr` to the observed endpoint.
5. Attach `netaccess.Path.Direct` to the request context, then call the root
   handler.

This order prevents a peer from supplying proxy headers that override the observed
source or claim a provider path. The wrapper uses the engine's current direct
endpoint, not an address supplied during registration. It reads the device and its
policy from the peer cache on every request, so an enrollment change reaches the
wrapper with the cache's next refresh, which runs every 10 seconds.

`Path.Direct` contains the terminating host ID (`api`), the peer key, the
account's user ID, the device ID, the policy (`automatic` or `direct_only`), the
engine's local UDP port, the path generation and class `direct`. The path names the
transport. It does not authenticate the caller: the router still checks the
caller's token, and the receipt and attribution code compares the path's account
and device with the authenticated caller's. Other paths leave `Path.Direct` nil.
`Path.IsDefault()` still means no provider, so existing provider URL selection stays
unchanged. `streamlocation.FromContext` classifies a direct path as `Remote`,
including one from a private IP. `FromMetadata` is unchanged; playback sessions
retain their computed location in the `sl` claim.

The listener serves the native router only. The Jellyfin-compatible and
Audiobookshelf-compatible listeners never terminate a direct connection.

## Enrollment and Tickets

An enrollment is a lease of 10 minutes on one entry per device, written to the
coordination store by `Coordination.Enroll`. An account holds at most 32
unexpired entries. The store refuses the next one and evicts none. A node key is
unique across accounts, so a second device cannot take a key that is held.

Only a login session that is not a View as user session enrolls or renews. The
enrollment operation refuses an API key and a View as user session with `403 permission_denied`, and
the capability document reads `allowed: false` for both. A View as user session's
entry would name the viewed account, and several revocations that end such a
session withdraw none of the viewed account's entries, so no entry belongs to
one. An administrator viewing as someone cannot test the direct path.
`AuthHandler.EndImpersonation` still withdraws the ending session's entries,
whatever made them.

A signaling ticket is an HS256 token signed with a key derived by HKDF-SHA256
from the auth JWT secret, with no salt and the label `silo/direct-signal/v1`.
No other token signed with that secret verifies as a ticket. Its claims are the
type `direct_signal`, the user ID, the profile ID, the login session ID, the device
ID, the node key, the issue time and the expiry, which equals the lease end. A
ticket check passes only when all of these hold:

- the signature and method verify, and the expiry is present and in the future;
- the type is `direct_signal`, the user ID is positive, the session ID and the
  device ID are not empty, and the node key is valid and not zero;
- the store holds an entry for that account and device with the same node key and
  an unexpired lease;
- the entry's policy records the ticket's session ID as its issuer.

An entry belongs to the login session that last enrolled or renewed it: each
enrollment rewrites the issuer in the entry's policy, and `Coordination.Authorize`
compares it with the ticket's session ID. A renewal by another session of the same
account therefore refuses the earlier issuer's ticket on its next authorization,
though the node key is the same. A signaling stream that was admitted under the
earlier ticket is not closed by the change: it has proved possession of the node
key, and it ends at its ticket's expiry. The device ID is whatever the client sent,
so no login-session revocation selects an entry by it alone.

Every refusal returns one error, so a caller cannot tell which check failed, and
the API answers it as `401 invalid_token`. A store outage is not a refusal: it
answers `503 dependency_unavailable`. A ticket confers no authority beyond the
coordination operations. The engine never holds an access token or cookie.

## Store Authority

The coordination store is the authority for enrollments, registrations, the host
entry and the host's confirmations. It is Redis when the process has Redis, and the
module's memory store otherwise, which is correct only for one process. Device IDs
are scoped by account: the store key is the numeric user ID plus the device ID, so
two accounts can use the same device ID and hold different entries.

The peer cache (`server.Directory`) is a read-through copy on the replica that
terminates direct connections. It rereads held entries every 10 seconds and on
each registration or enrollment change. It feeds the engine's peer set and the
wrapper. Playback reads the store, not the cache, for a device's enrollment:
`Service.Device` makes one store read per request and answers `ok=false` while the
toggle is off. The planner reads that state once and uses fields afterwards.

The engine writes the API host entry on each engine event and every 30 seconds.
It writes a confirmation of a device when the engine confirms a direct pong, and
renews each live confirmation every 30 seconds. An unchanged confirmation renews
its lifetime without waking the device's held netmap. An event, not a renewal,
clears a dropped confirmation. Both entries live 90 seconds without a renewal.

### Clock Rule

In the Redis store, Redis time decides whether an enrollment or a registration is
live, so every replica agrees whatever its own clock reads. Each method takes the
caller's clock reading only to turn a deadline it is given into a lifetime from
now. The scripts that write or judge a deadline read the Redis `TIME`, and a read
returns each deadline as the caller's now plus the time left at Redis time. This
differs from the module store's rule that an entry is absent at its expiry
reading: here an entry is absent once Redis time reaches its deadline, whatever
the caller's clock. Each such script opens with `redis.replicate_commands()`
when Redis defines it, because Redis before 5.0 refuses a write after `TIME`
unless the script first switches to effects replication. Host entries and
confirmations stay on the caller's clock: the lease holder is their one writer
and reader. The memory store uses the caller's clock throughout. The ticket's
signature expiry stays on each replica's own clock, as every Silo token does.

### Lease Fencing

On Redis, a write of the host entry or of a confirmation, and every removal of
them, succeeds only while the value of `silo:direct:engine-lease` is the writing
process's presence ID. A write that fails the check changes nothing and returns an
error that the engine logs. It does not stop the engine: the next census fails the
renewal and stops it within 30 seconds. So a process that lost the lease cannot
replace or remove its successor's entries. The check holds against one Redis
primary: with replication to a replica that can be promoted, a failover can lose
the lease key and let two processes pass it. The memory store has no lease and no
fence.

A new engine removes the API host entry and every confirmation under it, under its
own fence, before its first host write. The previous holder's confirmations are
gone before the new host entry exists, and the previous holder cannot write them
back, because its writes are fenced. A netmap read across a handoff can still pair
the old host with the new holder's confirmation. A device treats a path as ready
only after its own direct pong from the host, so the mismatch lasts until the
next netmap.

## Revocation

The server withdraws entries from the store's list for the account, never from a
peer cache, so a device that enrolled and never registered is withdrawn too. It
does so when the profile that enrolled a device forgets it in the account's device settings, when a login
session is signed out or revoked, for every entry that session issued, under
whatever device ID it named, for every entry that
records a profile when that profile is deleted, and for every entry of
an account when an administrator disables it, resets its password, signs it out
everywhere or deletes it, when the account owner resets the password, or when a
sign-in provider check revokes the account's sessions. A password change that
signs out the account's other sessions withdraws every entry except one that the
session which made the change issued for its own device; a session ID or device
ID that is empty or cannot be read, and an entry whose policy names no readable
issuer, select the entry (fail closed). A session-selected withdrawal deletes an
entry only while its key and policy are still the ones listed; when the entry
changed in between, the selector reads it again and applies the same rule, up to
three times, so an entry that another session has since renewed stays and a
changed audit marker does not save an entry the ending session still owns. A withdrawn or replaced key leaves the peer cache, and
its signaling stream closes. A stream held on another replica closes once the
store confirms that the lease is gone, or at ticket expiry. The service counts the
open device streams per node key, and a sweep worker, separate from the one that
applies peer updates, checks them. A pass runs when the peer cache changes and
10 seconds after the previous pass ends, one at a time. For each node key with an
open stream that the peer cache does not hold, in key order, it reads the entry
from the store, each read bounded by 5 seconds, so a pass over N keys takes at
most N times 5 seconds and a slow pass delays only the next pass, never a peer
update. The stream closes when the store reports no entry; a failed read leaves
the key for the next pass. A device that left the peer cache only while its entry
reloads keeps its stream. A store failure during revocation is logged once with a reason, does not
fail the event, and leaves the entry to lapse at its lease end.

The operator commands `silo owner set`, when it promotes or enables the account,
and `silo auth local-login enable -user <name> -temporary-password` run in their
own process with no coordination store: they end the account's login sessions
and withdraw none of its entries. Each such entry ends within one enrollment
lease, 10 minutes, because a renewal needs a live login session and its ticket
expires with the lease.

## Enrollment Audit

The first enrollment of a node key for a device, a key replacement included,
records the domain row `direct_connection.enrolled` after the entry is written
and before a ticket is minted. The row is written in its own transaction and
names the device ID and the node key, never the ticket. After the row commits,
`Enroll` sets an `audited` marker in the entry's policy, with a compare-before-write
that applies only while the entry still holds this request's node key and policy.
A renewal carries the marker only when the stored entry holds the same key and is
audited, and then records no row. A new key, a missing entry, or an entry without
the marker records a row, so an entry whose audit is pending, failed, or was lost
with its process is audited again. Two overlapping requests can both record a
row for one key; that duplicate is accepted.

When the row cannot be written, `Enroll` returns the error and deletes the entry
only while it still holds this request's key and unaudited policy, so a failed
audit never withdraws an entry that a later request replaced or audited. When the
stored entry cannot be read, `Enroll` refuses with `server.ErrUnavailable` and
writes nothing. So no ticket for a first enrollment is issued before an audit row
for its key has committed. The two conditional writes are `SwapDevicePolicy` and
`DeleteDeviceIf` on `directconn.CoordinationStore`, implemented by the Redis store
(one Lua script each) and the memory store.

## Signaling

The API engine dials the coordination region in process. The dial waits until the
host entry exists, so the region never misses the API host. Devices connect
through `GET /api/v2/direct-connections/signal`, which checks the ticket before the
WebSocket upgrade. The region shares the coordination server and the peer cache,
so a revoked entry stops admitting at once.

The signaling stream carries DERP frames as binary messages and a per-pair and
per-connection budget bounds it. The frame set, the discovery-data rules and the
budget are in the shunpike module's `coordination/SPEC.md` ("Signaling
Envelope"): ClientInfo (type 2, first frame only), SendPacket (4, with at most
1,280 bytes of discovery data), NotePreferred (7), and Ping and Pong (18 and 19).
Any other frame type closes the connection. The pair frame bucket has a burst of
96 and refills 38 frames per second. The pair byte bucket has a burst of 14,368
bytes and refills 5,790 bytes per second. A device's stream may send 179,344
bytes in each 90-second window. The API host's own stream carries the frames of
every device pair, so its window limit grows with the pairs it carries: 156,962
bytes for each pair that carried a frame in the window, and never less than
7,848,076. A pair that expires in the window still counts until the window ends,
so the limit never falls within a window. The third consecutive
overrun closes the stream. The limits live in `internal/directconn/region.go`,
which replaces the module defaults with its own numbers. The region counts drops
and closes by reason. It logs each rise once a minute: drops at Debug, closes at
Info. The envelope
never carries bulk data, which uses the direct UDP path.

## Observation Authority

The serving host decides the class of a path, never the client.

- A path's class and endpoint come from the engine's observation of the peer
  (`Engine.Path`). The host draws a random nonzero path generation whenever the
  class or endpoint changes.
- A client's `media_transport` is a request. The plan's `media_transport` is
  `direct` only when the API host serves the media and its path to the device is
  live and confirmed (`internal/api/handlers/playback_direct.go`). A reused session
  transport or a seek re-anchor keeps the session's attribution.
- The probe receipt is signed by the serving host and names the account, the
  device, the host and the class. A result is recorded under the receipt's class.
  On the replica that runs the engine, a direct receipt records only while its
  generation is the API host's current one. Only that replica knows the
  generation, so a replica without the engine records a valid, unexpired direct
  receipt of the API host under the receipt's key with no further check. The
  key's keyed hash of the measured address keeps the result filed under the path
  it measured. A client cannot claim a class by sending a field.
- The connection key of a direct path is `direct:` plus an HMAC of the peer's
  public address under the auth JWT secret, whole for IPv4 and by /64 for IPv6.
  Nothing stores the raw address. The key of a regular request classifies the
  address alone, so a LAN request that also holds a direct path keeps the
  `local` key.

## Direct Only Rule

A device enrolled with `connection_path` `direct_only` never gets a regular media
proxy origin. No proxy offers a direct transport, so `nodepool.MediaReachableVia`
admits no proxy for the device, `Node.MediaURLFor` returns no URL, and the API
host serves its media. The rule holds for start, replan and the probe redirect. It
holds when the device's enrollment cannot be read: the planner treats an unreadable
device as `direct_only`. Downloads, the Jellyfin-compatible surface and theme
delivery keep `ClientURLFor`, so the device setting does not reach them.

## Knob Rules

No knob widens authority. `SILO_NET_DIRECT_UDP_PORT`, `SILO_NET_DIRECT_GSO` and
`SILO_NET_DIRECT_BBR_LOSS` set where the engine binds and how it sends.
`SILO_NET_DIRECT_ACK_DECIMATION` and `SILO_NET_DIRECT_RECEIVE_WINDOW` only advise
the client through the capability document. None changes which peers the engine
admits, which operations the direct listener serves, or whether relayed data is
allowed. An invalid value falls back to its default, or turns the engine off for
the port, and the status read reports the adjustment. `TestKnobsNeverWidenAuthority`
in `internal/directconn/engine_test.go` holds this rule.

## Shutdown

The order builds on [API server shutdown](api-shutdown.md).

1. When readiness fails, `Service.BeginDrain` makes enrollment answer `503
   dependency_unavailable` with `Retry-After: 5` and marks the API host `draining`
   in the capability document. Every other direct request serves as normal during
   `SILO_SHUTDOWN_DELAY`. A netmap that answered at once now would make every
   device poll again without a pause, because the client backs off only after
   errors.
2. On process stop, the task manager stops first, before the drain can spend the
   shutdown deadline. At HTTP server stop, `Service.Shutdown` runs in parallel
   with `srv.Shutdown` inside the same 30-second budget. It first marks the
   coordination service stopping, so every netmap call answers at once, and wakes
   every held netmap. Held netmaps would otherwise keep `srv.Shutdown` waiting for
   their 25-second hold.
3. The engine's stop then drains the HTTP/3 server and joins `Serve`. Open streams
   may run until 5 seconds before the shutdown context's deadline (the 30-second
   budget, which resident plugins also spend while they stop, leaves up to 25
   seconds); the server then closes them, and that cut is logged, not returned as a
   shutdown error. The stop goes on to close the region and every signaling
   stream, delete the `api` host entry when this replica ran the engine (under its
   own 5-second deadline), wake any netmap that began to hold meanwhile, call
   `Engine.Close` without a timeout wrapper, and then stop the replica monitor,
   which ends a census or renewal in flight and releases the engine lease. The
   engine closes before the lease is released, so no successor holds the lease
   while this engine is open, and the host entry is deleted while this process
   still holds it. A delete that the store refuses because another process now
   holds the lease leaves that process's entry, which is the successor's; it is
   logged and is not a shutdown error.
4. The service context is cancelled last, so the peer cache and the engine's
   workers run until the engine has closed. Then the shutdown joins the check
   workers, the minute settings reread and the coordination workers, the
   revocation sweep among them.

The direct service runs on a context that the shutdown owns, not on the
application context, because the application context is cancelled before the
shutdown sequence starts.

## Diagnostics and Transport Dependencies

Linux socket-buffer readback includes the kernel's doubling. The status and logs
report half the readback as the usable grant, so a full grant is a usable grant at
or above the request. The report distinguishes missing
NET_ADMIN from NET_ADMIN held at startup and dropped by the privilege constructor.
UDP FORCE requests run after that drop; the privileged helper accepts TCP sockets
only. Capability rows do not promise that UDP forcing succeeds.

The API status and feature logs use the same feature, kind, state, reason, requested,
granted, and detail values, so a log record can carry the server's observed public
address and the router's port-mapping protocols. Disabled checks emit one Info record
for the direct feature; other feature records use Debug. Enabled checks emit each
changed feature at Info. The deduplication key includes enablement, feature, state,
reason, and grant, not detail, because the observed address port can change on every
probe.
NAT hairpin and mapped-address reachability remain unmeasured. TCP listener
outcomes and startup tuning values are separate from UDP probe observations.

The binary links the quic-go and Tailscale forks through shunpike. Sender selection
remains opt-in, and the default QUIC sender stays upstream's Reno-mode cubicSender.
The quic-go fork raises the shared congestion-window ceiling to 42,000 packets,
including for the default sender. The WebTransport compatibility shim uses the
pinned WebTransport API and retains the default QUIC sender. Audiobookshelf
compatibility does not open a WebTransport listener.

Go ignores dependency-module replace directives in a consuming main module.
The server must repeat shunpike's fork replacements; silently dropping them
would select upstream modules. `make verify-fork-pins` compares those directives
and refuses missing, different, or local-directory replacements. It reads the
shunpike release from the `require` line and downloads that release's `go.mod`.

The [API contract](../direct-connections-api.md) defines the administrator status
and peers reads, which expose only the API host. No Jellyfin operation changes.
