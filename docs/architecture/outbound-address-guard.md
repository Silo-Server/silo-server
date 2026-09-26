# Outbound address guard

Some features make Silo send HTTP requests to an address a user typed or a
third party supplied: history import, webhook sync, and notification webhooks.
The server sits on a network the user may not: a household LAN, or a cluster
with its database, cache, transcode nodes, and a cloud metadata endpoint. An
unchecked address lets anyone who can sign in probe that network from the
server's position. `internal/netguard` is the one place that decides which
addresses those requests may reach.

## Address classes

`netguard.Classify` puts every IP address in one class:

- **Public**: reachable by anyone on the internet.
- **Private**: the server's own network. Loopback, RFC 1918, CGNAT
  (`100.64.0.0/10`, which includes Tailscale), IPv6 ULA and site-local, and the
  special-use unicast ranges (TEST-NETs, benchmarking, documentation). A
  well-known NAT64 address (`64:ff9b::/96`) is private too, or blocked when the
  IPv4 address it embeds is blocked, since a NAT64 gateway translates to it.
- **Blocked**: never dialed by anyone. Unspecified (`0.0.0.0` dials loopback on
  Linux), link-local (`169.254.0.0/16`, `fe80::/10`, where cloud metadata
  services live), known metadata addresses outside link-local
  (`100.100.100.200`, `fd00:ec2::254`), multicast, reserved ranges, the
  deprecated IPv4-compatible IPv6 form (`::a.b.c.d`), and local-use NAT64
  (`64:ff9b:1::/48`), whose embedded IPv4 destination cannot be read reliably.

IPv4-mapped IPv6 addresses are classified as their IPv4 form, and zones are
ignored, so `::ffff:127.0.0.1` and `fe80::1%eth0` cannot slip through.

A request is either untrusted (Public only) or trusted with the local network
(Public and Private). Untrusted requests also may not reach the server's own
interface addresses, because a service bound to a public interface can be
hidden from the internet by a cloud firewall but still answer the host itself.

## Enforcement

The check runs in the HTTP client's dialer, on the address actually being
connected to. That covers redirect hops, hostnames that resolve to private
addresses, and DNS answers that change between a check and the connection.
The guarded transport ignores proxy environment variables: behind a proxy the
dialer would only see the proxy's address.

`netguard.Transport` keeps two connection pools and picks one per request from
the request context (`netguard.WithPrivateAccess`). The pools never share
connections, so an idle keep-alive connection a trusted request opened to a
LAN server cannot be reused by an untrusted request to the same address.

`netguard.CheckURL` resolves an address up front so a caller can refuse it
before queueing work or storing credentials. It is advisory: an address that
does not parse or does not resolve within two seconds passes, and the dialer
decides when the request is made.

## Media server addresses

History import and webhook sync share one policy,
`historyimport.LocalNetworkAccess`:

- A server an admin configured as an import source is trusted.
- Any other address is the user's input. That includes a typed URL and the
  server addresses Emby Connect or plex.tv list for the user's account, since
  the user's own server reports those. It is trusted only when the account is
  an enabled admin, or when an admin turned on
  `media_servers.allow_private_destinations` (Security & Access > Network).
- The setting is off by default, so a server with only an admin account works
  as before. Turning it on lets every account reach the server's local
  network; the admin page says so.

The policy is read again when a queued run starts and on every webhook sync
call, so turning the setting off or demoting an account also stops work that
was already accepted. A refused address fails with a message that tells the
user what to change (`historyimport.PrivateAddressMessage`), never as an
unreachable server.

Run monitors, v1 run responses, and realtime history-import events carry only
the fixed summaries from `historyimport.PublicRun`; stored diagnostics can hold
an upstream response body and stay on the server.

## Notification webhooks

Webhook destinations use the same classes with a stricter rule: Public only,
HTTPS only, checked at registration and at every connection. The admin-only
`notifications.webhooks.allow_private_destinations` development switch lifts
it. See [Notifications](notifications.md#trust-model-and-ssrf-guard).

## Limits

The guard cannot tell a friendly public host from a hostile one: any account
can still make the server request public addresses and learn whether they
answer, as it could from its own machine. In a container with bridge
networking the server cannot see the host's public address, so a request to
it is not recognized as the server's own. Run the container behind a firewall
that also applies to traffic from the container if that matters.
