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

**Lock order.** Key rows are written only by application transactions, and
always in one order: the account row (`lockUser`), the plugin installation
(share) and the provider identity a key is bound to (`keySignInPolicy`), the
person key, the device keys, then the login sessions those keys opened. No
database trigger or cascade writes to a key row. A sign-in reads the key's
binding without a lock, locks the installation and identity, then locks the
key row and, when its binding changed meanwhile (a link committed in between),
answers 429 with a one-second `Retry-After`: the key is still good, and the
next attempt sees the new binding. `enroll` and `PUT /auth/keys/person` lock
the enrolling session's identity before the key rows and the session's own row
last. Every sweep that revokes an account's keys and sessions together (a
provider refusal, an email match, a promotion, an ownership move, unlinking an
identity) revokes the keys first, then the sessions. Deleting an account
deletes its device keys (which ends their sessions by cascade) and then its
person keys before it revokes the remaining sessions and deletes the account.
Opening a session by key holds the account and key locks when it inserts the
session, so a sign-in cannot create a live session after a concurrent
revocation has ended the key's sessions.

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

**When an identity is deleted.** A key records the identity it is bound to as
a plain number (`identity_id`, with no foreign key) and `provider_bound`.
Deleting an identity therefore never writes to a key row; that is what keeps
the cascade from uninstalling a plugin, which takes no account lock, out of
the key rows that application transactions write under it.

- **Unlinking** (`UnlinkOwn`, `AdminUnlink`, both through `deleteIdentity`,
  which runs under the account lock) locks the identity row, then revokes
  the keys bound to that identity and ends the sessions they opened, before
  the identity is deleted. The identity comes first because an uninstall's
  cascade takes the identity and then the sessions whose identity it clears.
- **Uninstalling a plugin** deletes its identities by cascade and does nothing
  to keys. A provider-bound key whose identity is gone is dead: the key list
  leaves it out, and its next `sign-in` or `introduce` revokes it, under the
  account lock, and answers `signature_invalid`, which tells the device to
  enroll again. A dead person key is also revoked when the account next
  enrolls or replaces its person key, so it never answers `conflict` for a
  key the list doesn't show. Sessions such a key opened earlier lose their identity through
  `auth_sessions.identity_id`'s `ON DELETE SET NULL`, as every provider
  session does when its identity disappears, and refresh ends them at the
  provider age limit (`checkSessionStanding`).

The number can only ever name that identity:

- Identity IDs come from a sequence and are not reused. Uninstalling a plugin
  and installing it again gives its people new identity rows with new IDs, so
  keys bound to the old ones stay dead.
- A restore is the one place the sequence can move backwards: a restored
  sequence can be behind the live one, and the restore moves it past the IDs
  it carries (`internal/backup/credentials.go`, the `setval` after carrying a
  table). It cannot make a key match the wrong identity, because both key
  tables are left out of backups, so a restored server has no keys at all. The
  backup code depends on this: if keys are ever carried through a restore, a
  carried key must be dropped unless its identity is carried with it.
- Every lookup of a key's identity compares the account as well as the ID, so
  even an equal number can only match an identity of the key's own account. A
  key whose number names another account's identity is treated as a key whose
  identity is gone.

**Local passwords off.** A key that is not provider-bound is a local
credential. While `auth.local_password_login` is off it signs in only for a
break-glass account, as a password would, and is refused with 403
`local_login_disabled` otherwise (`keySignInPolicy`). On such a server the
person signs in through the provider and enrolls from that session.

**Jellyfin-compatible apps.** An account that signs in only with app keys has
no password, so a Jellyfin-compatible app signs it in with Quick Connect: the
app shows a code, and a key session approves it on
`POST /api/v2/auth/device/approve-handoff` for one of the account's profiles
(see [jellycompat-api.md](../jellycompat-api.md#quick-connect)). The user that
app gets reports `HasPassword: false`, and a password sign-in for the account
fails with the same answer as an unknown name.

**Administrators.** Every key an administrator's `introduce` enrolls is stored
`pending`, and the sealed answer is `approval_required` with no tokens, so a
copied person key alone can't open an admin session. Keys from `enroll` start
active: the enrolling session already proves the admin. A waiting device asks
again with `sign-in` on a fresh challenge, no sooner than five seconds after
its last try (`last_attempt_at`; sooner is 429 with `Retry-After`), and a key that
has waited 24 hours, or was denied, answers `key_denied`.

**Approving a waiting device.** `GET /auth/keys/pending` lists the account's
waiting keys inside their 24 hours, oldest first. `POST
/auth/keys/pending/{id}/approve` makes one active: in one transaction it
locks the account, then the key, then the approving session, and re-checks
under those locks that the account is still an enabled administrator, the key
is still waiting, unrevoked, inside its 24 hours and on this account, and the
approving session is still live. The device's next `sign-in` opens a session
and runs the provider checks then. Approving an active key changes nothing
(except from the session that key itself opened, which is refused: a device
does not approve itself); an unknown, denied, revoked or expired one is 404. `POST
/auth/keys/pending/{id}/deny` marks the key `denied`: its next `sign-in`
answers `key_denied`, and neither list shows it. Both decisions are recorded
as domain audit actions on the account (`user.device_key_approved`,
`user.device_key_denied`, with the key's ID). A session opened by one of the
administrator's own active device keys may approve, which is how a new device
is approved from a device already signed in; a session may never decide on the
key that opened it.

**A cap on waiting keys.** Whoever holds an administrator's person key can call
`introduce` again and again, and each call leaves a waiting row. An account
holds at most 10 waiting keys inside their 24 hours (`auth.MaxPendingKeys`); an
`introduce` that would add an eleventh is refused with 409 `conflict`, the
answer the cap on device sign-ins approved in advance gives. The count runs
under the account lock in the transaction that inserts the key; the challenge
is already spent by then, as for every refusal after the signature. The
retention task that deletes expired challenges (`cleanup_app_key_challenges`,
hourly) also revokes waiting keys past their 24 hours. It sets `revoked_at` and
never deletes the row, so the public key stays enrolled once, and it takes
each account's lock in turn. A swept key's `sign-in` still verifies its
signature and answers `key_denied`, as it did before the sweep.

## Grants

A grant lets another app, one that doesn't hold the person key, enroll its own
key on one account. The person allows the app on a device that holds the
person key, which signs a `grant` envelope away from the server: the app's
public key (`app_key`), the account (`account_id`), the addresses the app may
use (`origins`), the app's name, a random 16-byte `grant_id`, and `exp` at
most 300 seconds after `iat`. The app sends it inside a `grant-redeem` signed
with its own key to `POST /auth/grants/redeem` (`AppKeySignIn.RedeemGrant`).

The checks, in order:

1. The outer envelope's signature verifies by the grant's `app_key`, so only
   the app the person allowed can redeem it; a `kid` that isn't that key's ID
   is `app_grant_invalid`. The challenge is spent once this signature verifies.
2. The grant verifies by the live person key of the account it names, and both
   envelopes name this server. A person key that was replaced or revoked, or
   is provider-bound to an identity no longer on the account, signs no grant.
   The grant's own `exp` past is `app_grant_expired`, so the app can tell a wrong
   clock where it was signed from a bad grant; the `grant-redeem` envelope's
   own times follow the general rule (`signature_invalid`).
3. The redeem's `origin` is one of the grant's `origins`, and the origin rule
   applies to it as on the other signed routes.
4. In one transaction, under the usual lock order: the account is enabled and
   not an administrator's, the person key's provider binding still holds, the
   person key is still live, and the grant's ID is recorded for the account
   in `auth_grant_uses`. A second redemption of one grant, on any API process,
   finds the row and is refused `app_grant_invalid`. The record is per account
   because the signer chooses the ID: another account's grant with the same
   ID uses up only its own. Then the key is enrolled and its session opened.
   A person key whose identity is gone from the account is revoked here, as
   at its next use anywhere, and the grant refused.

A refusal for an account with no person key takes about as long as one for a
grant that fails its signature (a decoy check), so the time does not say
which accounts have a person key.

The enrolled key has `kind = 'app'`, `enrolled_via = 'grant'`, the signing
person key as `person_key_id`, and that person key's provider binding
(`identity_id`, `provider_bound`), so the provider rule applies to the app as
to the person's own devices. Its name is the redeem's `device_name`, or else
the grant's `app_name`. The key list shows it with `allowed_by`, the account's
name. From then on the app signs in with `sign-in` like any device.

**Administrators.** A grant for an administrator's account is refused. An
admin's own devices wait for approval when they introduce themselves, and a
grant has no approval step; another app has no business holding an admin
session.

**What the app's session may not do.** The app's access must end when its key
is revoked, so its session may not mint access that outlives that (see "Who
decides"): it can't enroll keys, replace the person key, approve a device
sign-in now or in advance, read the account's key list, or revoke any key but
its own. It also can't create a personal API key or link a sign-in provider
account, the two older routes that hand an account a lasting credential
(`SessionMayIssueCredential`); other sessions keep what those routes' own
rules give them. The person, or the server's owner, revokes the app from the key list
like any device.

**Retention.** A used grant's ID is kept until an hour past the grant's `exp`
(the margin covers a database clock ahead of an API process's), then the
hourly retention step that clears challenges deletes it. By then every process
refuses the grant by its `exp`.

## Who decides

The operations on keys check how the calling session was opened
(`auth_sessions.opened_via`, written at every session creation: `password`,
`provider`, `device_login`, `app_key` or `impersonation`). The rule is one
function, `auth.SessionAllows`, with one action per kind of change:

- Every action needs the account's own login session
  (`Claims.IsOwnLoginSession`: no API key, no administrator acting as the
  account, no remote-playback handoff), and refuses a session opened by a key
  enrolled through a grant (another app): the app's access ends when its key
  is revoked, so it must not mint access that outlives that, nor read the
  person's devices.
- `ActionManageKeys` (`enroll`, `PUT /auth/keys/person`,
  `pending/{id}/approve`, `POST /auth/device/preapprove`) and `ActionReadKeys`
  (`GET /auth/keys`, `GET /auth/keys/pending`, `pending/{id}/deny`) also need a
  session opened by a password, a provider or the person's own device key. A
  session opened by device sign-in (a TV, a shared tablet, a browser approved
  by QR code) is refused: whoever holds it isn't necessarily the person, the
  device list is exactly what such a device is kept away from, and a TV must
  not refuse the administrator's new phone. A session with no record, older
  than the column, is refused the same way.
- `ActionApproveDeviceSignIn` (approving a device sign-in that is waiting now)
  refuses only the grant-enrolled app's session.
- `DELETE /auth/keys/devices/{id}` follows `ActionManageKeys`, except that a
  grant-enrolled app may revoke its own key. A device may revoke its own key,
  which ends the session asking.

The key routes against each kind of session:

| Route | Password, provider, own device key | Device sign-in (a TV) | Grant-enrolled app | No `opened_via` | Admin acting as the account, API key, remote playback |
|---|---|---|---|---|---|
| `GET /auth/keys`, `GET /auth/keys/pending`, `pending/{id}/deny` | allowed | refused | refused | refused | refused |
| `enroll`, `PUT /auth/keys/person`, `pending/{id}/approve`, `POST /auth/device/preapprove` | main profile only | refused | refused | refused | refused |
| `DELETE /auth/keys/devices/{id}` | allowed | refused | its own key only | refused | refused |

**The household's main profile.** Every profile of a household shares the
account and its login session, so a change that creates lasting access to the
account is limited to the household's main profile, the one that manages the
household: `enroll`, `PUT /auth/keys/person`, `pending/{id}/approve` and
approving a device sign-in in advance (`POST /auth/device/preapprove`, a
sign-in that lasts 30 days). Approving a device sign-in that is waiting now
keeps no profile rule. The
check is one function, `handlers.AuthHandler.ActsAsVerifiedMainProfile`,
called through `requireVerifiedMainProfile` in the v2 handler of each, which is
registered with `mainProfileOnly` (profile-scoped, `X-Profile-Id` optional). It
is the household manager rule that disconnecting a sign-in and creating a
personal API key apply (`canManageHouseholdAs`):

- `X-Profile-Id` naming the primary profile passes; one with a PIN needs
  `X-Profile-Token` (403 `profile_verification_required` from the viewer gate,
  or from the handler if the PIN changed since the token). Naming any other
  profile is 403 `permission_denied`, on an administrator's account too.
- A request with no `X-Profile-Id` passes only from an administrator account
  that has at most one profile, with no PIN and no maturity or library limit:
  there the request can only mean that profile. This is not "administrators
  may leave the header out": an administrator's account with several
  profiles, or a PIN, names its main profile and sends its token like any
  other account. Anyone else, including a one-profile account that is not an
  administrator, must name the main profile; without it the request is 403
  `permission_denied`. This is stricter than `changePassword`, which lets an
  administrator leave the header out even when the main profile has a PIN;
  approving a waiting device is only ever done by an administrator, so that
  rule would let any profile on the device skip the PIN.

The handler checks the profile before the operation's transaction starts, and
that transaction may wait for the account's row. So the handler passes the
profile it checked and its PIN revision (`auth.ActingProfile`) to the
operation, and each of the four checks it again right after it locks the
account (`recheckActingProfile`): the row must still exist, still be the
primary profile, and still be at that PIN revision, or the operation ends with
`ErrActingProfileChanged` (403 `profile_verification_required`) and changes
nothing. The check holds a share lock on the profile row until the transaction
ends, so a PIN change waits for it.

The check never waits for the profile row (`FOR SHARE NOWAIT`); a row another
write holds counts as changed. A profile write can hold its row and then touch
the account row (a foreign key insert, the access policy revision), which this
transaction has locked, so waiting could deadlock. Creating a personal API key
waits instead, and can, because it takes only a key-share lock on the account.

Two cases have nothing to check again: a request that named no profile (the
administrator with one unprotected profile), and a server whose profiles are
stored outside Postgres, where this transaction can't read them.

The key list, its deletes, the pending list and deny keep no profile rule:
any profile of the account may review and remove devices.

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
  key, devices already enrolled keep theirs. A device the old key introduced
  that is still waiting for approval is refused with it (`denied`): replacing
  the key is how a person cuts off a copy of it, and a waiting device may be
  the copy's work. That device is introduced again with the new key.
- **A provider refusing the person** (`applyAnswer`), **an email match taking
  over the account** (`account_resolution.go`), **a promotion to admin or a
  move of ownership** (`revokeCredentialsIssuedToNonAdmin`) revoke every key of
  the account and end their sessions, in the same transaction, as they delete
  the account's API keys. A promoted account's keys never had an admin's
  approval.
- **A password reset, "sign out everywhere" and a password change** end
  sessions as before and keep keys: the device signs in again with its key. The
  devices list is where a key is revoked.
- **Unlinking an identity** revokes the keys bound to it and ends their
  sessions. **Uninstalling a plugin** leaves them dead, revoked at their next
  use (above).
- **A provider that can't re-check** bounds key sign-in by age (above) without
  revoking anything.

## Rate limits

`createAppKeyChallenge` spends the per-address `key_challenge` budget;
`introduce`, `sign-in`, `enroll` and grant redemption spend `key_sign_in`. Both default to 60 a
minute with a burst of 20, enough for a few waiting devices behind one address
(each asks every five seconds). Neither budget can be spent per key before the
signature verifies; the challenge is what makes each signed request single
use.

## Multi-node rules

Nothing about app keys lives in a node's memory. Challenges, used grants, keys,
waiting keys' last tries and sessions are Postgres rows, so a challenge issued by one
API process is spent once on any of them, and a revocation on one ends the
sessions every node checks. The switch and the local-password setting are read
from `server_settings` on each request. The rate-limit budgets follow the
rate limiter's backend (cluster-wide with Redis).

## The switch

`auth.app_key_sign_in` (default on) turns app key sign-in on or off
server-wide. It is read from `server_settings` on each request, so a change on
any node applies at once. While it is off, `getAppKeyCapability` reports
`disabled`, and the challenge, `introduce`, `sign-in`, `enroll`, grant
redemption, `PUT /auth/keys/person` and `pending/{id}/approve` answer
`capability_disabled`. A person's own key list and its deletes, the pending
list and `deny` stay open, so keys can be reviewed, revoked and refused; sessions keys
opened before are not ended by the switch. Backups classify it as an access setting: a restore keeps the
stricter of the live and the backed-up value.

**Demo mode.** On a demo server (`demo.enabled`) every visitor signs in to
one shared account. A key a visitor enrolled would be a lasting credential of
theirs on an account that isn't theirs, able to replace the person key or
approve devices after they left. So in demo mode app key sign-in is off for
everyone, administrators included, whatever the setting says
(`appKeySwitch`): the capability reports `disabled` and the same routes
answer `capability_disabled`, so a demo account never holds a key and one
enrolled before demo mode was turned on does not sign in. It is one
condition where the switch is read, so every route the switch closes
inherits it. Approving a device sign-in in advance is not behind the switch;
it carries the ordinary demo restriction (refused for a caller who is not an
administrator) and the device login capability reports `preapprove: false`.

## Backups

Both key tables, `auth_key_challenges` and `auth_grant_uses` are left out of
backups, as login sessions and device sign-in requests are. A grant lives five
minutes, so a restore never revives one that was used. They hold public keys, which are not
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
