# App keys

An app can sign in to an account with a key it holds instead of a password.
This document covers the rules the server keeps for that: what it stores, how
it verifies a signed request, who may do what, and what ends a key. The wire
format (signed messages, the sealed reply, the canonical address forms) lives
in `internal/appkeys`, with every constant in `protocol.go`. Storage and
revocation are in `internal/auth/app_key_repository.go`, sign-in in
`internal/auth/app_key_sign_in.go` and `app_key_session.go`, and the
operations in `internal/apiv2/app_keys.go`, `app_key_sign_in.go` and
`app_key_manage.go`. The operation contract is in
[auth-api.md](../auth-api.md#app-key-sign-in).

The server never sees a private key: it stores only public keys, and the
private halves stay on the devices.

## Two kinds of key

- **Person key.** One per account at a time. It vouches for the account's
  device keys. An account holds at most one person key that is not revoked
  (a partial unique index on `auth_person_keys.user_id`).
- **Device key.** One per app install on an account. It opens login sessions.
  `kind` is `device` for an app's own install, or `app` for another app the
  person allowed. `status` is `active`, or `pending` and `denied` for a key
  waiting for, or refused, an approval.

Both kinds are ECDSA P-256 keys. A public key travels as base64url without
padding of the 65-byte uncompressed point (`0x04`, then X and Y). The server
refuses any value that is not a point on the curve. A key's ID is base64url
without padding of the first 16 bytes of SHA-256 of those 65 bytes.

## Storage

`auth_person_keys` and `auth_device_keys` hold the public key, its key ID, the
account, how the key was enrolled (`enrolled_via`) and when it was created
and revoked. A device key also has a name and platform for display, the
person key that vouched for it (`person_key_id`), and when it last signed in.

Each key row records whether the session that enrolled it came from a provider
identity: `identity_id` while that identity exists, and `provider_bound`, which
stays true after the identity row is deleted.

- **A public key is enrolled once per server.** `public_key` and `key_id` are
  unique in each table, and an enrollment also refuses a key already stored in
  the other table. The cross-table check runs under a transaction lock on the
  key ID, so two enrollments of one key cannot both pass it. A duplicate is
  `ErrAppKeyAlreadyEnrolled`.
- **Key rows are not deleted.** Revoking a key sets `revoked_at`, and the row
  stays, so a revoked key can never be enrolled again. Only deleting the
  account removes its keys (`ON DELETE CASCADE`).
- Revoking the person key leaves the device keys it vouched for working; it
  can no longer vouch for new ones.

## Sessions opened by a key

A login session opened by a device key carries that key in
`auth_sessions.device_key_id`. It stays an ordinary account session
(`purpose = 'account'`); every other sign-in leaves the column NULL.

**Revoking a device key ends the sessions it opened, in the same
transaction.** The revocation locks the account row, then the key row, sets
the key's `revoked_at`, then sets `revoked_at` on every live session with that
`device_key_id`. The per-request session check (`ActiveSession`) is unchanged
and reads no key table: the session is already revoked, so its next request
fails like any other revoked session's. Revoking every key of an account does
the same for each of its device keys.

**Lock order.** Every key path takes its locks in one order: the account row
(`lockUser`), then the plugin installation and the provider identity a key is
bound to (`keySignInPolicy`), then the person key, then device key rows, then
the login sessions those keys opened. A sign-in reads the key's binding
without a lock, locks the installation and identity, then locks the key row
and, when its binding changed meanwhile (a link committed in between),
answers 429 with a one-second `Retry-After`: the key is still good, and the
next attempt sees the new binding. An identity deleted in between has revoked
the key (below), so that sign-in answers `signature_invalid`. `enroll` and
`PUT /auth/keys/person` lock the enrolling session's identity before the key
rows and the session's own row last. Every sweep that revokes an account's
keys and sessions together (a provider refusal, an email match, a promotion,
an ownership move) revokes the keys first, then the sessions. Deleting an
account deletes its device keys (which ends their sessions by cascade) and
then its person keys before it revokes the remaining sessions and deletes the
account: revoked key rows would be updated again by the identity trigger when
the delete cascades to the account's identities.

Deleting an identity takes the identity, then the key rows bound to it, then
the device sign-ins approved in advance from their sessions, then those
sessions (the revoking trigger). Unlinking takes the account lock first
(`UnlinkOwn` and `AdminUnlink`); uninstalling a plugin takes none, and the
trigger never reads the account row. The rule that keeps it so: **the
trigger leaves nothing for a later statement in the same transaction to
update again.** When a transaction updates a row twice, Postgres re-checks
the row's foreign keys on the second update, and for a row that names an
account that share-locks the `users` row, which a sweep, a key sign-in or a
password reset may hold while it waits on the same keys. So the trigger
clears `identity_id` on the keys and sessions it revokes, and denies the
pre-approvals of those sessions and clears their `approved_identity_id`, so
that neither the foreign keys' `SET NULL` nor the sessions' pre-approval
withdrawal trigger has anything left to change.
`TestIdentityDeleteNeverWaitsForAccountDB` holds the account row and requires
an uninstall to finish, and `TestRefusalSweepAndIdentityDeleteDontDeadlockDB`
runs a refusal sweep against an identity delete. Opening a session by key
holds the account and key locks when it inserts the session, so a sign-in
cannot create a live session after a concurrent revocation has ended the
key's sessions.

## Protocol

The protocol is named `silo-sync-v1`. Its test vectors are committed under
`internal/appkeys/testdata/vectors/v1` and refreshed with
`scripts/sync-app-key-vectors`; `internal/appkeys` reads every case, so a
refreshed file that adds a case type the code doesn't handle fails the test.

A signed request is an envelope of a base64url payload and an ECDSA P-256
signature over `silo-sync-v1.<purpose>.<payload>`. The verifier takes the
purpose from the route, never from the payload, so a message signed for one
route never verifies at another; the payload must then name the same purpose.
It checks the signature first and parses the payload after: nothing is
re-serialised. The envelope and the key body have each member once; a body
that repeats one is refused. Inside the signed payload a repeated member keeps
its last value, as JSON readers do: a signer never repeats one, and the
signature covers the payload as sent. Enrolling purposes (`introduce`, `enroll`) also carry
`device_sig`, the new device key's signature over the same bytes, which proves
the caller holds the key being enrolled; every other purpose refuses it. The
payload names this server's `server_id` and its own `iat` and `exp`: at most
300 seconds apart, `iat` at most 60 seconds ahead of the server's clock.

**Challenges.** Every signed request is on a challenge from
`createAppKeyChallenge`: 32 random bytes, good for two minutes and one request.
The server stores only its SHA-256 in `auth_key_challenges` and spends it with
`DELETE ... RETURNING`, so two requests carrying one challenge succeed at most
once across every API process. A challenge is spent as soon as the request's
signature verifies, even when a later check refuses the request, and a request
whose signature fails spends nothing. A challenge is not tied to whoever
asked for it: anyone who holds an enrolled key, or a login session, and has
seen a challenge can spend it by signing a request on it, and the device that
asked then asks for another. What is guaranteed is that a signed message can't
be replayed, and a signer can't retry one message against different checks.
Spending happens outside the enrollment transaction. An hourly retention step
(`cleanup_app_key_challenges`) deletes the challenges that expired unspent.

**Addresses.** An address has one spelling. `CanonicalOrigin` normalises
three things: it lower-cases the scheme and an ASCII host, rewrites an IPv6
host into its RFC 5952 form, and drops any path, query and fragment. It
refuses every other variation instead of rewriting it: a host with any
character outside ASCII (checked before lower-casing), an IPv4 host that isn't
four decimal numbers, a trailing dot or empty label, userinfo, an empty,
leading-zero or default port, an IPv6 zone or embedded IPv4 address.
`CanonicalServerAddress` keeps the path, drops trailing slashes, and refuses
an empty, `.` or `..` segment, a character a path can't hold unescaped, and an
escape that is lower-case or not needed. The signed `origin` must already be canonical. The same function
canonicalizes the request's origin, `server.public_url` and provider origins
before any comparison; one that won't canonicalize can't be judged.

## Signing in

`introduce` is signed by the account's person key and enrolls the device key in
its payload; `sign-in` is signed by an enrolled device key; `enroll` comes from
a login session with the person key beside the envelope. Each opens an ordinary
account session with `device_key_id` set and `opened_via = 'app_key'`.

Opening the session takes the locks in the order above, then inserts the
session, in one transaction, so a sign-in racing a revocation either is
refused or has its session ended by it. A disabled account, and a revoked,
waiting or denied key, opens nothing. A waiting or denied key is refused
before any provider check, so it never causes a provider call. A request
naming a key the server doesn't hold, or holds revoked, is refused after one
signature check against a throwaway key, so that refusal takes about as long
as a known key's.

`last_used_at` on a device key is the last time it opened a session. `enroll`
and an `introduce` that answers `signed_in` open one, so a key enrolled and not
used since shows `last_used_at` equal to `enrolled_at`. A waiting key never
opens one: its `last_used_at` stays empty, and its sign-in attempts are counted
in `last_attempt_at`, which no answer shows.

**Provider-bound keys.** A key enrolled from a session that came through a
sign-in provider records that identity (`identity_id`, `provider_bound`); an
introduced key takes its person key's. A key sign-in is not a provider sign-in,
so it never restarts the provider's clock. The session continues the identity
from the identity's last vouched time, `COALESCE(last_authenticated_at,
linked_at)`, read under the identity lock, as its `provider_since` (see
[external-sign-in.md](external-sign-in.md#provider-re-check)). Before the
sign-in's transaction the server makes the decision a refresh of that session
would make (`Service.providerExpiry`, shared with `checkSessionStanding`): it
re-checks the provider when due, under the outage policy, and where the
provider can't re-check the identity it refuses the sign-in once that time is
more than `auth.refresh_token_expiry` ago. Inside the transaction the identity
and its installation are locked and checked as an OAuth sign-in checks them.
A key past that age limit is 409 `provider_sign_in_required`: its identity
still exists, and the provider last vouched for it too long ago. The person
recovers by signing in through the provider again on any device, which moves
`last_authenticated_at`; their keys then work again without enrolling anew.
That age limit does not revoke keys. One the provider refused is 403
`not_permitted`, as an OAuth sign-in answers it; a disabled installation or a
fail-closed outage is 503 `provider_unavailable`; a store failure during the
check is 503 `dependency_unavailable` with `Retry-After`, as at refresh.

**Linking a provider later.** When a provider is linked to an account in a
way that moves the account's open sessions under the identity (a link the
person or an administrator makes, not a break-glass account or a network
provider), the account's live keys that are not provider-bound are bound to
the identity in the same transaction. From then on the provider rule applies
to them, so a key enrolled from a password session can't keep opening sessions
the provider never checks.

**Deleting an identity.** Unlinking an identity, or uninstalling its plugin,
revokes every key bound to that identity and ends the sessions its device keys
opened: a new provider sign-in makes a new identity row, so such a key could
never sign in again. A trigger on `plugin_auth_identities` does it, so it
holds on every path that deletes one, the cascade from uninstalling a plugin
and nodes on an older release included. The device then gets
`signature_invalid` and enrolls again. A provider-bound key with no identity
should not exist; the sign-in code refuses one as a guard.

**Local passwords off.** A key that is not provider-bound is a local
credential. While `auth.local_password_login` is off it signs in only for a
break-glass account, as a password would, and is refused with 403
`local_login_disabled` otherwise (`keySignInPolicy`). On such a server the
person signs in through the provider and enrolls from that session.

**Administrators.** Every key an administrator's `introduce` enrolls is stored
`pending`, and the sealed answer is `approval_required` with no tokens, so a
copied person key alone can't open an admin session. Keys from `enroll` start
active: the enrolling session already proves the admin. A waiting device asks
again with `sign-in` on a fresh challenge, no sooner than five seconds after
its last try (`last_attempt_at`; sooner is 429 with `Retry-After`), and a key that
has waited 24 hours, or was denied, answers `key_denied`. The routes that
approve and deny waiting keys are not served yet; until then a waiting key
stays out.

## Who decides

The operations that hand out lasting access check how the calling session was
opened (`auth_sessions.opened_via`, written at every session creation:
`password`, `provider`, `device_login`, `app_key` or `impersonation`). The rule
is one function, `auth.SessionAllows`:

- `enroll` and `PUT /auth/keys/person` need the account's own login session
  (`Claims.IsOwnLoginSession`: no API key, no administrator acting as the
  account, no remote-playback handoff), opened by a password, a provider or the
  person's own app key. A session opened by device sign-in (a TV, a shared
  tablet, a browser approved by QR code) is refused: whoever holds it isn't
  necessarily the person, and through a key it could reach lasting access. A
  session with no record, older than the column, is refused the same way.
- A session opened by a key enrolled through a grant (another app) is refused
  every such change, and also approving a device sign-in: the app's access ends
  when its key is revoked, so it must not mint access that outlives that.
- `POST /auth/device/preapprove` (a device sign-in approved in advance, see
  [device-login.md](device-login.md#approval-in-advance)) follows the same rule
  as `enroll`: a sign-in approved in advance outlives the session that made it.
- `DELETE /auth/keys/devices/{id}` follows the same rule, except that a
  grant-enrolled app may revoke its own key. A device may revoke its own key,
  which ends the session asking.

## The origin rule and the sealed reply

The payload names the address the device is talking to. The server refuses it
only where it can judge: a request that arrived through a network access
provider (its ingress token, `netaccess.PathFromContext`, not the `Host`
header) must name one of that provider's connected origins, and a request
whose own canonical origin is `server.public_url`'s must name that origin.
Anything else, a LAN address or a reverse proxy, can't be judged and is
accepted. A `Host` header on a direct connection is chosen by the client, so
this catches a mistaken client, not a hostile relay.

That is why the answer is sealed. Its plaintext, which holds the session
tokens, is encrypted to `reply_key`, a one-time key inside the signed payload:
ECDH P-256 with a fresh server key, HKDF-SHA256 over `Z || E || P` salted with
the challenge, AES-256-GCM. A fake server that relays a device's request to the
real one can read nothing it gets back, and can't swap the reply key without
breaking the signature. Refusals are not sealed.

## The pinned route

A `signed_in` sealed reply from `introduce`, `sign-in` and `enroll` (and from
any later operation that seals a session the same way) also carries
`local_route`, in the shape `getServerConnections` gives it: `spki_sha256`, the
pin of the key the API listener presents over TLS on its own port, and `url`,
`server.local_url` when an admin set one. How the listener and the key work is
in [server-identity.md](server-identity.md#local-network-tls). On a running
server every `signed_in` reply carries it, because the server always has the
key: it is derived from `SECRET_KEY` at start. It is absent only on a server
built without one, which happens in tests. `approval_required` never carries
it. An app that signs in by key follows these rules:

- The reply is sealed but not signed, so a pin learned from it is trust on
  first use, and only when the device holds no pin for that server. A reply
  whose pin differs from the one the device holds is a mismatch, and the device
  stops, unless the connection it arrived on is one that may replace a pin: TLS
  presenting the old pin, or validated HTTPS at an address the person reached
  themselves (next rule).
- A device replaces a pin it holds only over TLS that presents the old pin, over
  HTTPS with a system-trusted certificate at an address the person reached
  themselves, or when the person says so. HTTPS with a system-trusted
  certificate at an address the server merely reported is not enough: a server
  that reports a domain it no longer controls would let whoever holds that
  domain, with a valid certificate, hand the device their own key as the new
  pin.
- TLS that presents a key while the device holds no pin for the server is
  acceptable only at an address the person reached themselves, as first
  contact, and the pin the device takes from the sealed reply must equal the
  key that connection presented.
- After a first contact over plain HTTP, the device moves to HTTPS with the pin
  on the same host and port before it sends anything else.
- An address that has presented the pinned key once must always present it.
  There is no fallback to plain HTTP for that address.
- The pin belongs to the server, so any address that presents it over TLS is
  authenticated, whoever reported the address and whatever range it is in. The
  private-IPv4 limit in server-identity.md's local-route rules applies to the
  local route of apps already shipped and is unchanged; it does not apply to an
  app using the pin for key sign-in.
- An address behind a proxy that offers only plain HTTP has no pinned route:
  HTTPS on that host and port does not present the key. Such an address has
  only the trust of an address the person typed themselves.

First contact has a limit the pin can't remove. The reply is sealed, not
signed, so on a first contact over plain HTTP someone on the network path can
answer in the server's place, with their own pin, and the device has no way to
tell. First contact is as trustworthy as the address the person was given. The
pin protects every contact after it.

The server authenticates the connection rather than signing the reply because
a signature would cover one message: whatever follows it over plain HTTP (the
tokens in use, a profile's PIN) would still be readable on the path. A pinned
TLS connection covers all of it.

## What revokes keys

- **Revoking a device key** (the person's devices list) ends its sessions at
  once, as above.
- **Replacing the person key** revokes the old one: new devices need the new
  key, devices already enrolled keep theirs.
- **A provider refusing the person** (`applyAnswer`), **an email match taking
  over the account** (`account_resolution.go`), **a promotion to admin or a
  move of ownership** (`revokeCredentialsIssuedToNonAdmin`) revoke every key of
  the account and end their sessions, in the same transaction, as they delete
  the account's API keys. A promoted account's keys never had an admin's
  approval.
- **A password reset, "sign out everywhere" and a password change** end
  sessions as before and keep keys: the device signs in again with its key. The
  devices list is where a key is revoked.
- **Deleting an identity** (unlinking it, or uninstalling its plugin) revokes
  the keys bound to it and ends their sessions (above).
- **A provider that can't re-check** bounds key sign-in by age (above) without
  revoking anything.

## Rate limits

`createAppKeyChallenge` spends the per-address `key_challenge` budget;
`introduce`, `sign-in` and `enroll` spend `key_sign_in`. Both default to 60 a
minute with a burst of 20, enough for a few waiting devices behind one address
(each asks every five seconds). Neither budget can be spent per key before the
signature verifies; the challenge is what makes each signed request single
use.

## Multi-node rules

Nothing about app keys lives in a node's memory. Challenges, keys, waiting
keys' last tries and sessions are Postgres rows, so a challenge issued by one
API process is spent once on any of them, and a revocation on one ends the
sessions every node checks. The switch and the local-password setting are read
from `server_settings` on each request. The rate-limit budgets follow the
rate limiter's backend (cluster-wide with Redis).

## The switch

`auth.app_key_sign_in` (default on) turns app key sign-in on or off
server-wide. It is read from `server_settings` on each request, so a change on
any node applies at once. While it is off, `getAppKeyCapability` reports
`disabled`, and the challenge, `introduce`, `sign-in`, `enroll` and
`PUT /auth/keys/person` answer `capability_disabled`. A person's own key list
and its deletes stay open, so keys can be reviewed and revoked; sessions keys
opened before are not ended by the switch. Backups classify it as an access setting: a restore keeps the
stricter of the live and the backed-up value.

## Backups

Both key tables and `auth_key_challenges` are left out of backups, as login
sessions and device sign-in requests are. They hold public keys, which are not
secret, but a key is a
credential: a restore that brought keys back would reopen sign-ins that were
revoked after the backup was taken. A restored server has no keys, so each
device enrolls again.

The rule that a public key is enrolled once holds for as long as its row
does. A restore empties both tables, revoked rows included, so a key that was
revoked before the restore can be enrolled again by a caller the server
allows to enroll.

## Cloned databases

A cloned database carries the server ID (see
[server-identity.md](server-identity.md)) and every key with it. The clone
accepts the same keys as the original, and the server ID alone cannot tell
the two apart. That is consistent: whoever holds a copy of the
database already holds the same accounts.
