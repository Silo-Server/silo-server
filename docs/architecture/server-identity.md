# Server identity and connection discovery

A phone and a TV can reach one Silo deployment through different addresses:
the configured public URL, a LAN address, or the overlay origin a network
access provider exposes ([network-access.md](network-access.md)). Clients used
to derive a server's identity from its URL, so two addresses looked like two
servers, and companion pairing handed the TV the phone's address whether or
not the TV could reach it. Two `/api/v2` operations fix that. Tracking issue:
Silo-Server/silo-server#1268; client work: Silo-Server/silo-apple#341 and
Silo-Server/silo-android#352.

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
  carrying a `url` only while connected.

What it deliberately leaves out: proxy-node backend addresses, provider
enrollment URLs, provider error text and everything else on the admin status.
A missing public URL is a missing endpoint; the server never guesses one from
the request's `Host` and never enables public exposure. Reachability is the
client's to test: a provider that is connected on the server says nothing
about whether a given TV is on that overlay.

## Client flow

During companion setup the phone reads the connections document and hands
the TV the server ID plus the candidate endpoints. The TV probes each one
with the public identity operation, keeps those whose ID matches, and opens
device login at the first reachable match. If the overlay address fails, the
provider's display name drives setup help and the public endpoint, when
present, is the explicit fallback. The address that worked is what the TV
saves; the phone's preferred address does not change. Automatic mid-playback
route switching is out of scope.

## jellycompat

No change. Jellyfin clients keep the compat `Id`, and the Jellyfin listener is
reached through the same overlay origin the provider exposes for it.
