# Direct Connections

The hidden administrator setting `direct_connections.enabled` controls readiness
checks. It defaults to false. It does not start a direct-connection engine or
open a direct playback path. The API and integrated modes run the self-check;
proxy and transcode modes do not.

`internal/directconn` reads effective settings, maps observations to feature
states, and reports the API host. While disabled it calls neither the STUN nor
the port-mapping prober. Local socket capability reads send no packets.
`direct_connections.port_mapping` defaults to true and independently gates router
service discovery when enabled. Discovery creates no port mapping.
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
read tries again. The same background reread runs once the last successful settings
read is a minute old, because Redis drops a settings-change event published while a
subscriber reconnects. A missed change therefore reaches the snapshot through the
first status read after that minute instead of waiting for a refresh or a restart. A refresh starts a check even when the values are unchanged, or
joins the running one, and returns the read error if its own read fails. A refresh
that joins a running check reads settings first only after a failed read, and
schedules a follow-up check when the values differ from the running check's.

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
