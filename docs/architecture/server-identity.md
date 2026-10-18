# Server identity and connection discovery

A phone and a TV can reach one Silo deployment through different addresses:
the configured public URL, a LAN address, or the overlay origin a network
access provider exposes ([network-access.md](network-access.md)). Clients used
to derive a server's identity from its URL, so two addresses looked like two
servers, and companion pairing handed the TV the phone's address whether or
not the TV could reach it. Two `/api/v2` operations fix that. Tracking issue:
Silo-Server/silo-server#1268; client work: Silo-Server/silo-apple#341 and
Silo-Server/silo-android#352.

Finding a server a client does not know yet (LAN advertisement, overlay
short names) is [server-discovery.md](server-discovery.md); it hands clients
candidates that this identity confirms.

## Identity

`GET /api/v2/system/identity` is public and answers `{"server_id": …}`.
`GET /api/v2/system/info` links to it as `links.identity`, so discovery
stays a fixed build-wide document and the per-deployment value lives next to
it. The ID is:

- **One per deployment.** A random UUID stored in `server_settings` under
  `server.identity_id` (`internal/serveridentity`). Every API process of the
  deployment reads the same row, so every address answers the same value.
- **Minted once, atomically.** The first read of a fresh deployment seeds it
  with insert-if-absent; concurrent API processes converge on the winner.
  Each process caches it for its lifetime because it never changes.
- **Stable across restarts, hostname and URL changes, and database restores.**
  All of those keep the deployment the same server.
- **Copied by a database clone.** A clone carries the row, so until it is
  changed the clone *is* the same server to every client: clients group it
  with the original and may offer its addresses as candidates. That is the
  intended reading for a restore or a migrated host. An operator who wants a
  clone to be a separate deployment deletes the `server.identity_id` row on
  the clone before serving clients; the next read mints a fresh ID. There is
  no API to regenerate it. The ID authorizes nothing (below), so a collision
  costs a failed pairing attempt at an address that turns out to be the
  wrong server, not a credential.
- **Plaintext.** It is public discovery data, and an encrypted row would
  become unreadable after a `SECRET_KEY` rotation, which must not change who
  a server is.
- **Separate from the other IDs.** The diagnostics installation ID
  (`diagnostics.server_instance_id`) keys upload manifests and import
  receipts; the Jellyfin-compat `Id` is the configurable
  `jellyfin_compat.server_id` on the compatibility surface; `/api/v1/health`
  still echoes that compat ID. None of them is this value, and clients must
  not key credential storage on it: local credential and settings keys stay
  whatever the client chose.

## The identity authorizes nothing

The value is self-asserted. Any host can echo a server ID it has seen, so a
matching ID (or a matching display name) never by itself authorizes a
pairing handoff or the sending of credentials to an address. The proof that
two addresses share one backend is the existing device-login flow
(`/api/v2/auth/device/*`): the TV opens a pairing request at the candidate
address, the phone approves it through its own connection, and the TV
collects tokens only if both requests landed in the same store. The ID is a
hint that makes the candidate worth trying and lets clients group saved
servers; the approval is the gate. Profile confirmation and every other
authorization rule are unchanged.

The one other gate is the API server's own TLS key
([Local network TLS](#local-network-tls)): a signed-in client may send its
existing credentials to a local-network address when TLS there presents the
key it pinned from the connections document. A matching ID or name is never
enough on its own.

## Connections

`GET /api/v2/system/connections` is authenticated (account, no profile) and
is the capability document for this feature: a client that gets
`state: available` can rely on both operations. It reports:

- `server_id`, the same value as the identity document;
- `current`, the access path the request arrived on (`default` or
  `provider` with the slug), read from the ingress middleware so a client
  learns which of its saved addresses is an overlay one;
- `endpoints`, the addresses the deployment offers: `server.public_url` as
  `kind: public` when configured, then one `kind: provider` entry per
  installed provider with its display name and its state on the API host,
  carrying a `url` only while connected;
- `local_route`, the pin of the key the API listener presents over TLS and
  `server.local_url` when an admin set it
  ([Local network TLS](#local-network-tls)).

What it deliberately leaves out: proxy-node backend addresses, provider
enrollment URLs, provider error text and everything else on the admin status.
A missing public URL is a missing endpoint; the server never guesses one from
the request's `Host` and never enables public exposure. Reachability is the
client's to test: a provider that is connected on the server says nothing
about whether a given TV is on that overlay.

## Local network TLS

A client saved with the deployment's public URL, reverse proxy or tunnel takes
the long way round at home: through hairpin NAT or the tunnel's bandwidth
limits, and the server counts its streams as remote because the address it
sees is public. The server's LAN address is shorter, but it is plain HTTP and
anyone can advertise one, so the client has to authenticate it before sending
credentials there. `internal/lantls` does that without a public certificate:

- **TLS on the API port.** The API listener (not the Jellyfin, ABS or proxy
  listeners) answers TLS and plain HTTP on the same port. It reads each new
  connection's first byte in a goroutine of its own: a TLS handshake record
  (`0x16`) gets the TLS server, anything else is served as plain HTTP with that
  byte replayed, and a connection that says nothing for five seconds is handed
  on as plain HTTP. Accept never waits on a slow client, a server shutdown
  closes connections still being read, and TLS connections reach the HTTP
  server as `*tls.Conn`, so `request.TLS` and the request scheme are set. HTTP/1.1
  only. Docker port publishing, the network access plugins' plain upstream and
  every existing client are unchanged.
- **A derived key.** The key is ECDSA P-256, derived from `SECRET_KEY` with
  HKDF-SHA256 (`silo/lan-tls/v1`). Nothing is stored, every API process and
  replica presents the same key, and rotating `SECRET_KEY` replaces it. The
  self-signed certificate around it is minted per process; clients ignore its
  names and dates.
- **A pin over a trusted address.** `GET /api/v2/system/connections` carries
  `local_route.spki_sha256`, the base64 SHA-256 of the key's
  SubjectPublicKeyInfo. Clients learn and replace the pin only through a saved
  address they already authenticate (HTTPS with a system-trusted certificate),
  and follow no redirect on that read: a saved plain-HTTP address, or one a
  redirect leads to, cannot vouch for a key.

Client rules for using a local address (the local route):

1. Candidates are `local_route.url` and `_silo._tcp` adverts whose TXT
   `id` matches the saved server's identity
   ([server-discovery.md](server-discovery.md)). Each is reduced to a private
   IPv4 address (RFC 1918) and tried as `https` on the same port, whatever
   scheme the endpoint names. Public certificate authorities do not issue
   certificates for those addresses, and App Transport Security exempts them,
   so the pin is the only trust that applies.
2. The connection is accepted only when the presented key matches the pin. No
   other certificate is ever accepted for a candidate, publicly valid or not,
   and redirects are not followed.
3. `GET /api/v2/system/identity` over that connection must answer the saved
   server's identity.
4. The client keeps its saved address as the credential owner; only where its
   requests go changes. It moves back as soon as a request on the local route
   fails to connect, and checks again when the device's network changes.

A relay cannot pass: it cannot present the key, and forwarding the TLS bytes to
the real server only produces an end-to-end connection to the real server. The
key authenticates the deployment, not a location, so a client on a VPN into the
home network may use the local route too; that is intended.

## Client flow

During companion setup the phone reads the connections document and hands
the TV the server ID plus the candidate endpoints. The TV probes each one
with the public identity operation, keeps those whose ID matches, and opens
device login at the first reachable match. If the overlay address fails, the
provider's display name drives setup help and the public endpoint, when
present, is the explicit fallback. The address that worked is what the TV
saves; the phone's preferred address does not change, and the flow never
changes a saved address later. The only automatic routing change is the local
route above, which leaves the saved address in place: a stream already playing
is not moved onto it, and a stream on it that fails reconnects from the saved
address.

## jellycompat

No change. Jellyfin clients keep the compat `Id`, and the Jellyfin listener is
reached through the same overlay origin the provider exposes for it.
