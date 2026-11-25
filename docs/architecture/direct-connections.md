# Direct Connections

The hidden administrator setting `direct_connections.enabled` controls readiness
checks and the direct engine. It defaults to false. API and integrated modes
start the engine only when this setting is true, the configured UDP port is valid,
and the replica gate permits it. Proxy and transcode modes run neither the engine
nor the self-check. The engine and gate live in `internal/directconn`.

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
`internal/directconn/engine.go` uses one start function and one stop function.
No engine starts before the HTTP handler it serves exists: the API host calls
`HandlerReady` once the router is built. Until then a settings read records its
values and starts nothing, and `HandlerReady` runs the start only if settings and
the gate still allow it. This keeps the lease deadline in force during a slow
startup, because the lifecycle never waits for the handler.
Each start replays the stored peer set before constructing a fresh HTTP/3 server.
A peer list the running engine rejects is not stored, so a restart never replays it.
Each stop shuts down that server before calling `Engine.Close` without a timeout
wrapper. One goroutine reads `Engine.Changed` and ends before that engine closes.
If the engine reports a different local port after a rebind, Silo stops it and
tries once on the configured port. A failed start waits for a settings or gate
change before another attempt: the first successful settings read, changed values,
a settings-change event, or a changed replica gate retries it. The minute reread of
unchanged values does not, and neither does the follow-up read the service schedules
itself after a check; only a change in the values they read does.

If HTTP/3 `Serve` returns while its engine is current, nobody asked it to stop, so
Silo withdraws the engine. One step, before any blocking one, detaches the engine,
turns the `engine` row `off` with `engine_start_failed`, and records the settings
reads and lease successes counted so far. Then the row is published, the event
goroutine is joined, and the engine is drained and closed. The engine restarts once,
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

On process stop, Silo stops the task manager first, then shuts the direct service
down beside the API server's drain: it drains the HTTP/3 server and joins `Serve`,
calls `Engine.Close`, then stops the replica monitor, which ends a census or renewal
in flight and releases the lease, and cancels and joins the check workers and the
minute settings reread. The engine closes before the lease is released, so no
successor holds the lease while this engine is open.

The server uses `n4.DefaultServerPolicy()` with the configured BBR loss policy.
Its receive windows keep the quic-go defaults and ACK decimation stays off.
The receive-window and ACK-decimation knobs do not change the server policy.
Signaling is unset and relayed data is denied for every peer.

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
   `421 Misdirected Request` without calling the router. Otherwise, set
   `RemoteAddr` to that endpoint.
3. Attach `netaccess.Path.Direct` to the request context before the root handler.

This order prevents a peer from supplying proxy headers that override the observed
source or claim a provider path. The wrapper uses the engine's current direct
endpoint, not an address supplied during registration.

`Path.Direct` contains the terminating host ID, peer key, and class `direct`.
Its `UserID`, `DeviceID`, and `Policy` remain empty because enrollment is absent.
This transport identity grants no application authority. Other paths leave
`Path.Direct` nil. `Path.IsDefault()` still means no provider, so existing provider
URL selection stays unchanged. `streamlocation.FromContext` classifies a direct
path as `Remote`, including one from a private IP. `FromMetadata` is unchanged;
playback sessions retain their computed location in the `sl` claim.

The engine transport adds no client operation. Production code supplies no peers;
only tests populate the engine's peer set. Enrollment, registration, and direct
playback are not available through the current API.

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

The administrator-only [status contract](../direct-connections-api.md) exposes
only the API host. No native playback client or Jellyfin operation changes.
