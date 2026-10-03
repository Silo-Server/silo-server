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
| Instance name | `branding.server_name` (default `Silo`), trimmed to 63 bytes |
| Host | `silo-<first 8 alphanumerics of the server ID>.local` |
| Port | the port the API process listens on; it speaks plain HTTP |
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
- **Instance names are not unique or current.** Two servers named `Silo` on
  one link are renamed by the responder (`Silo (2)`), and the instance name is
  read when the process starts, so a later rename shows after a restart.
  Clients label entries with the live name from `GET /api/v2/theme/branding`
  and tell servers apart by `id`.
- **Several entries can share one `id`.** Each API replica of a deployment
  advertises itself, and a host with several interfaces is resolved once per
  interface. Group by `id`.
- **Unknown TXT keys are ignored.** New keys are added without bumping `v`.

The host name is derived from the server ID rather than the machine's
hostname, so the responder never contends with the operating system's own
mDNS daemon (avahi, mDNSResponder) for `<hostname>.local`. The two share UDP
5353. The responder answers standard multicast queries, which is what Apple's
`NWBrowser` and Android's `NsdManager` send; it does not answer legacy one-shot
unicast queries (`dig -p 5353`).

`server.lan_discovery` (default `true`, restart required) turns the
advertisement off. It is also skipped, with one log line, when the API
listener is bound to loopback only, and any failure to start (no multicast
interface, port 5353 unavailable) is logged once without affecting the
server. A listener bound to one address advertises only that address, on the
interface that holds it. The advertisement reaches only the networks the process itself is
attached to, and it carries the port the process listens on, not a port a
container runtime publishes it under.

The responder announces on every multicast-capable interface, including
overlay interfaces such as `tailscale0`. Overlay networks do not carry
multicast, so that costs nothing and finds nobody; overlay discovery is the
provider's (below).

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
   short timeout. Accept the answer only when the bare name resolved to an
   overlay address (`100.64.0.0/10`, or Tailscale's `fd7a:115c:a1e0::/48`),
   and follow at most one redirect, only to an `https` URL whose host is the
   probed name plus a domain. A bare name resolved through the local
   network's DNS instead could send the client anywhere.
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
