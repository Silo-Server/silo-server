# Server discovery

A client with no saved address should be able to list the Silo servers it can
reach instead of asking the user to type one. Two networks matter: the local
network, and an overlay network that a network access provider joins
([network-access.md](network-access.md)). Neither discovery path is trusted:
everything a client finds is a candidate it confirms with the public identity
operation before using it ([server-identity.md](server-identity.md)).

## Local network: DNS-SD over multicast DNS

Every API process (`server.mode` `integrated` or `api`) advertises one DNS-SD
service once its API listener is bound (`internal/landiscovery`):

| Field | Value |
|---|---|
| Service type | `_silo._tcp` in `local.` |
| Instance name | `branding.server_name`, trimmed to 56 bytes so a conflict suffix fits the 63-byte label |
| Host | `silo-<first 8 alphanumerics of the server ID>-<6 random hex>.local`, one per API process |
| Port | the port the API process listens on; it speaks plain HTTP |
| Addresses | A and AAAA records for the families the API listener accepts, from the interface the query arrived on; never link-local IPv6 |
| TXT `v` | `1`; changes only if an existing key changes meaning |
| TXT `id` | the native server ID from `GET /api/v2/system/identity` |

Rules a client relies on:

- **The TXT `id` is a hint.** A client groups what it finds with saved
  servers by `id`, then probes `http://<address>:<port>/api/v2/system/identity`
  and keeps the candidate only if the answer matches. Anyone on the LAN can
  advertise any ID and answer identity with it, so a found server is a
  suggestion: choosing one is the same as typing its address. It never
  authorizes a pairing handoff, and the existing sign-in and device-login
  rules apply unchanged.
- **Connect by resolved address.** Build the URL from the address the
  platform resolver returns (IPv4 preferred, IPv6 in brackets). `.local` names
  do not resolve on every Android release.
- **Instance names are not unique.** Two servers named `Silo` on one link
  probe and one becomes `Silo (2)`: the later one, or the tiebreak loser when
  both start together. A rename in the admin settings
  is picked up within the check interval below. Clients label entries with the
  live name from `GET /api/v2/theme/branding` and tell servers apart by `id`.
- **Several entries can share one `id`.** Each API process of a deployment
  advertises itself under its own host label, and a host with several
  interfaces is resolved once per interface. Group by `id`.
- **Unknown TXT keys are ignored.** New keys are added without bumping `v`.

The responder is Silo's own (`internal/landiscovery/responder.go`), not a
general mDNS library, and it is deliberately narrow:

- It shares UDP 5353 with the operating system's mDNS daemon (avahi,
  mDNSResponder) and never claims the machine's own `<hostname>.local`.
- It answers only multicast queries sent from port 5353, which are local to
  the link whatever their source subnet (RFC 6762 §11), and answers by
  multicast on that link, never to the sender. Legacy unicast queries
  (`dig -p 5353 @host`) get no reply, so the listener cannot reflect or
  amplify traffic toward another address. Apple's `NWBrowser` and Android's
  `NsdManager` send multicast queries from port 5353. Its probes ask for
  multicast replies, since a unicast reply would reach only the first socket
  bound to the shared port; the one unicast packet it accepts is a response
  from the interface's own networks while it probes.
- It probes its names before announcing, announces twice, and sends goodbyes
  (TTL 0) on every interface when it stops. Two processes probing one name at
  once settle it with the RFC 6762 §8.2 tiebreak. A response that later claims
  one of its names with other data starts the probe over, renaming if the
  other responder keeps the name. Each record is multicast at most once a
  second per link and address family.
- Every check interval (30 seconds) it joins the mDNS groups on any
  multicast interface that appeared, probes, and announces there, and
  re-announces on interfaces whose addresses changed so caches replace them.
  The service is never withdrawn because another interface came or went.
  Point-to-point interfaces (VPN and overlay tunnels) are skipped.
- It owns its sockets and goroutines; stopping it releases both.

`server.lan_discovery` (default `true`, restart required) turns the
advertisement off. It is also skipped, with one log line, when the API
listener is bound to loopback or to a single address: mDNS answers per link,
and a listener on one address serves only one link. A listener on
`0.0.0.0` advertises IPv4 addresses only. A failure (server identity or name
unavailable, port 5353 taken) is logged once without affecting the server and
retried every check interval. The advertisement reaches only the networks the
process itself is attached to, and it carries the port the process listens
on, not a port a container runtime publishes it under.

## Overlay network: the provider's short name

Overlay networks do not carry multicast, and a phone or TV app cannot read the
overlay's device list (another app's local API is out of reach). What clients
can use is overlay DNS: Tailscale MagicDNS adds the tailnet's domain as a DNS
search domain on every device, so the bare name `silo` resolves to the node
whose machine name is `silo`, the Tailscale plugin's default.

A bare name cannot be used with HTTPS directly, because the node's certificate
covers only the full name (`silo.<tailnet>.ts.net`). So a provider answers plain
HTTP on its API host's overlay port 80 with a `307` redirect to its HTTPS API
origin, keeping the path and query. The redirect handler never proxies: no
request reaches Silo without TLS, and methods other than `GET` and `HEAD`
are refused. Proxy nodes do not answer; nobody types their names.

Client flow:

1. Probe `http://silo/api/v2/system/identity`, and `silo-1` and `silo-2` (the
   names a second and third node with the default name receive), with a
   short timeout. Follow at most one redirect, only to an `https` URL whose
   host is the probed name plus a domain, and accept the answer only when
   every connection the probe made ran over the overlay: both its local and
   remote addresses are overlay addresses (`100.64.0.0/10`, or Tailscale's
   `fd7a:115c:a1e0::/48`). A remote overlay address alone is not enough, since
   a local network can resolve the bare name to one and route it to itself.
2. On success, the final origin is the server's overlay address: save that,
   never the bare-name URL. The redirect only answers reads, so a client that
   keeps `http://silo/` as its base URL fails at the first `POST` (sign-in
   included) and pays a redirect on every request. Treat the final origin as a
   candidate like a LAN one, labelled with the provider; a request on it also
   gets the network sign-in offer from `GET /api/v2/auth/providers` when the
   provider vouches for the device.
3. Apply the same redirect when a user types a bare single-label name such as
   `media-box`: admins rename nodes, so the default names are only the common
   case.

Probe again when the device's network changes: an overlay often connects
after the setup screen is already open.

A provider that cannot open port 80 still serves every listener; the redirect
is a convenience, not part of connection setup.

## Out of scope

- Jellyfin's UDP 7359 "who is JellyfinServer" broadcast. jellycompat's `Id`
  defaults to one fixed value on every install, so answering the broadcast
  would make every Silo server look like one; it needs a per-deployment
  compat ID first.
- Listing servers a user can reach over the internet. A client learns the
  public and provider addresses of a known server from
  `GET /api/v2/system/connections` after sign-in.
- Automatic switching between a server's addresses mid-session.
