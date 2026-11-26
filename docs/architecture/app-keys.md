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

Each key row also records `enrolled_ip_address`, the client address of the
request that enrolled it. It is the value the login session that request opens
records as `ip_address`: the address the client-address middleware resolved,
which reads forwarding headers only from `clientip.trusted_proxies` and
otherwise takes the connection's peer. Replacing the person key opens no
session and records the same resolved address. The one difference from the
session row: when the resolver could not tell the client and answered the
unspecified address (`0.0.0.0` or `::`), the key row stores none, where the
session row keeps that value. A key enrolled before the column existed, or
whose address was unknown, has none, and the key lists show an empty
string. The key lists that show it are refused to a
session opened by device sign-in or by another app's key, like the session
list's other sessions' addresses. Like `auth_sessions.ip_address` it is
personal data, and it travels in backups with the rest of the key row.

Every way a key or an approval in advance comes to exist writes a domain
audit row on the account in the transaction that makes it, so a rolled-back
or refused call leaves none: `user.app_keys_enrolled` (enroll from a
session), `user.person_key_replaced`, `user.device_key_introduced`,
`user.app_code_issued`, `user.app_code_redeemed` (both kinds of code),
`user.app_grant_redeemed` and `user.device_sign_in_preapproved`. A row names
keys by their IDs and an app code by its kind and expiry, never a code, a
hash or a secret; the enroll and replace rows also carry
`session_opened_via`, whether the session that made the change was opened by
a password, a provider or a device key. The fields are listed in
[admin-api.md](../admin-api.md#account-and-permission-audit-details).

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
  table). It cannot make a key match the wrong identity: once the restore has
  carried the sign-in links, it revokes every provider-bound key whose
  `(identity_id, user_id)` has no link (`appKeySettle` in
  `internal/backup/credentials_app_keys.go`), so a key whose identity is gone
  is already revoked when a later identity reuses its number.
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
(from the session that key itself opened it is refused, 403); an unknown,
denied, revoked or expired one is 404. `POST
/auth/keys/pending/{id}/deny` marks the key `denied`: its next `sign-in`
answers `key_denied`, and neither list shows it. Both decisions are recorded
as domain audit actions on the account (`user.device_key_approved`,
`user.device_key_denied`, with the key's ID).

An administrator's new device is approved in one of two ways. From a device
already signed in: a session opened by one of the administrator's own active
device keys may approve. Or on the new device itself, by signing in on the
server once there (a password or provider sign-in) and approving the waiting
key as the main profile. The second is allowed on purpose: the person proved
who they are to the server on that device, which is all an approval from
another device proves. A waiting key can't approve itself, because it opens
no session: its `sign-in` answers `approval_required` or 429 until someone
approves it, or `key_denied` once it is denied or 24 hours old. (The service
also refuses a session that names the key that opened it. That key is always
active, so this is not what keeps a waiting device from approving itself.)

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

1. The outer envelope's signature verifies by the grant's `app_key` and its
   `kid` is that key's ID, so only the app the person allowed can redeem it.
   A redeemer that doesn't hold that key is `signature_invalid` however it
   shows, a failed signature or another `kid`; `app_grant_invalid` is about
   the grant itself. The challenge is spent once this check passes.
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

## App codes

An app code is how a server's web page hands the Silo app what it needs to
enroll its keys: base64url of 32 random bytes, good once for ten minutes. Only
its SHA-256 is stored (`auth_app_codes`). The page opens the app with one link
built by one function, `appkeys.JoinLink` on the server and `buildJoinLink`
on the web:

```
silo://join#v1.<base64url of the server address>.<app code>
https://siloserver.org/join#v1.<base64url of the server address>.<app code>
```

Both carry the code in the fragment, so the link page's host never receives
it. The server address comes from the answer that issued the code
(`server_address`): `server.public_url`, in its canonical server address form,
when the request arrived on that address's canonical origin, so a path prefix a
reverse proxy adds survives; otherwise the request's own canonical origin. The
older `silo://invite?server=&token=` link, which hands released Android builds
the raw invitation token, is unchanged.

**A code travels in a link only when redeeming it creates a new account with
no administrator power.** Every other code, a session code or a join code for
an administrator's invitation, moves only by the person's own hands. A link
can be received by an app other than Silo (any app may claim a custom scheme),
and whatever receives it can redeem the code first with its own keys. For an
ordinary invitation that gets it a new, empty account: the invitation is spent,
so the invited person notices at once and the owner removes the account. That
risk is accepted. A session code reaches an existing account, an
administrator's included, and an administrator's join code would hand the
intercepting app a working administrator session (keys from a join code start
active, and the redeem opens a session at once).

The server decides, and says so in the answer that issues the code:
`link_allowed`. A page builds the join link only when it is true; otherwise it
shows the server address and the code as text to type, never a link, a
scannable link or anything a device could open. The server can't tell a typed
code from one lifted out of a link, so the protection is that no page makes
such a link. (The protocol defines no scannable form for an app code: its link
vectors carry a code only in the join link, which a camera would open as a
URL.)

`link_allowed` is decided at issue from the account input the redeem will
create (`joinAccountInput`, `linkAllowed` in `internal/invitations`), field by
field of what an invitation carries:

| Field | Can it confer administrator power? |
| --- | --- |
| role | Yes: only `user` allows a link; `admin`, or any other value, refuses. |
| permissions | The invitation sets none; the account takes its role's defaults (`auth.NewUserPermissions`). Anything beyond `marker_edit` refuses (`metadata_curation` edits what every account sees). |
| access group | No: a group narrows permissions and limits and never adds any (`access.ApplyGroupPolicy` intersects). |
| library list, first profile, name, email | No: media access and identity. |
| ownership, break-glass | Can't be carried: an invitation has no such field, and creating an account never sets them. |

An invitation can't be edited after a code is issued: resending makes a new
invitation (copying its role, group and libraries), and the old invitation's
codes stop working. `createAppCode` always answers `link_allowed: false`.

There are two kinds:

- **Join** (`POST /invitations/app-code`, public): the invite page's "Join
  with the Silo app". It checks the invitation exactly as accepting it does,
  checks the account name and email are free, and reserves the name. The
  invitation stays open and nothing else is created. A second code from one
  invitation replaces the first. The reservation is advisory: a partial unique
  index on the reserved name among unspent codes stops two joins choosing one
  name, and the account insert at redeem decides, so a name an administrator
  creates in between refuses the redeem with `conflict`. Only a live code
  reserves: issuing first deletes, under the settings lock, the unspent join
  codes for the same name or email that are expired or whose invitation can
  no longer be claimed (accepted, revoked, resent, superseded or expired), so
  a resent or revoked invitation doesn't hold its invitee's name for the rest
  of its code's ten minutes; such a code could never be redeemed. An emailed
  invitation's account takes its address as name and email, as accept gives
  it; for an invitation with no address the person chooses a username, and an
  email is optional. A username containing `@` must be the email the same
  request gives: otherwise someone could hold another person's address as a
  name and block that person's own invitation. A code is refused where accept
  would be (an invitation that creates a profile the store can't create in the
  same transaction), so the page is never handed a code no redeem can use.
- **Session** (`POST /auth/app-codes`): "Open in Silo" for a signed-in browser,
  shown as text to type, never as a link.
  It is a key-creating operation: only a session that may manage keys
  (`ActionManageKeys`), acting as the household's verified main profile,
  rechecked under the account lock. The code records the session and its
  provider binding. The keys it enrolls take that binding, and a code whose
  session ended before it is redeemed no longer works.

**Redeem** (`POST /auth/app-codes/redeem`, public, `key_sign_in` budget). The
body is `{ person_key, message }`, a `redeem` envelope signed with
`person_key`'s private half, with `device_sig`, on a challenge, the code in
its payload. The challenge is spent once the signature verifies. Then:

- A join runs in one transaction. Lock order: the settings lock, the
  invitation, the code, then the other invitations for the same address that
  the claim revokes, then the new account's rows. Accepting with a password
  and issuing a code take the settings lock first too, before any invitation
  or code row, so all three run one after the other and the rows each locks
  after it can't close a cycle. The join spends the code, claims the
  invitation through the same claim accept uses (re-checked: it may have been
  revoked, resent or expired since), creates the account with no password and
  the invitation's role, libraries, access group and first-profile choice,
  enrolls the person key and the device key, marks the invitation accepted and
  opens the device key's session. A code nobody redeems leaves the invitation
  open and no account.
- A session code locks the account, spends the code, and enrolls the device
  key. The request's person key is enrolled when the account has none (a
  provider-bound key whose identity is gone counts as none). An account with
  a different person key keeps it: the device key is enrolled under the
  account's key, nothing is revoked, devices waiting for approval stay
  waiting, and the request's person key, which only proved the device signed
  the envelope, is not stored. A redeem never replaces the person key; a
  person who has lost every other device redeems a code and then calls
  `PUT /auth/keys/person` from the new session as the main profile. The
  issuing session's row is read last; one that ended rolls the redeem back.

The hourly cleanup of expired codes never waits on any of these: it skips a
code row a request holds (`FOR UPDATE SKIP LOCKED`) and deletes it on a later
run.

Keys from a code start active, also on an administrator's account: the code
came from the account's own session, or the invitation an administrator made
(an invitation may grant the administrator role, as accept allows). A session
code is not checked against the main profile's PIN again at redeem: it was
issued to the verified main profile, and it ends with the session that issued
it.

`server_address` tells the app where to connect. It is not proof of which
server answers there; the redeem's signed `server_id` and the origin rule are,
as for every signed request.

**Two conflicts.** A redeem answers 409 `conflict` for two causes with
different remedies, told apart by `errors[0].code`, never by the detail:
`duplicate` (at `body.message.payload`) is a reserved name or email taken
since the code was issued, and the person gets a new code from the page with
another name; `key_enrolled` is a public key this server already holds, at
`body.person_key` for the person key and `body.message.payload` for the device
key, and the app makes that key again and redeems the same code on a new
challenge. Every refusal after the code is found returns inside the redeem's
transaction, so the code's spend rolls back and only the challenge is used up
(`docs/auth-api.md` has the table). Issuing a join code answers a taken name
with `duplicate` at `body.username` or `body.email`, whichever clashed. The sealed
plaintext is the usual `signed_in` answer plus `username`, `person_key_id`
(the account's person key once the redeem commits) and `person_key`
(`enrolled`, or `kept` when the account kept a different key). An unknown, spent or expired code, and one whose
invitation or session can no longer be used, is `app_code_invalid` (404). Of
two redemptions of one code, on any API processes, exactly one succeeds.

**Retention.** The hourly step that clears challenges deletes codes past their
expiry, spent or not. Codes are left out of backups.

### Accounts with no password

A join creates an account with no password: `password_hash` NULL and
`local_password_login_enabled` false. Its keys are its way in. One helper,
`models.User.HasLocalPassword`, answers whether an account has a usable local
password (a stored hash and local sign-in on for the account), and every
reader of that fact uses it: password sign-in, password change, reset links,
the administrator's account view. `createUser` stores NULL, never an empty
string, for a blank email or username, so two accounts with no email don't
collide on a unique constraint. A password sign-in to an account with no
stored password compares the placeholder hash, so the time does not tell a
stranger which accounts have one.

### Local passwords off

No join may create an account whose only credential is keys. While
`auth.local_password_login` is off, `POST /invitations/app-code` answers
`provider_sign_in_required` (409), and a redeem refuses a join code the same
way if the setting changed in between: the claim checks it under the settings
lock, as accept does. On such a server a person gets the app by signing in with
the provider on the server's site and using "Open in Silo"
(`POST /auth/app-codes`), so the keys enroll against that identity. Creating
and resending invitations stays refused while local passwords are off.

### Sessions that can't manage keys

`GET /auth/keys/session` answers what the caller's session may do:
`list` (`ActionReadKeys`), `manage` (`ActionManageKeys`), `approve` (manage,
and the request acts as the verified main profile) and `sign_in_again`. The
last is true only for a session refused for how it was opened (device sign-in,
or no record of how) whose account has another way into a browser: a local
password the server accepts for it, or a sign-in provider account. A person
with neither only ever gets a browser session by device sign-in, so "sign in
again" would be a dead end; the page says to manage devices in the Silo app
instead. It is false for an administrator acting as the account, an API key, a
remote-playback session and a grant-enrolled app's session.

## Who decides

The operations on keys check how the calling session was opened
(`auth_sessions.opened_via`, written at every session creation: `password`,
`provider`, `device_login`, `app_key` or `impersonation`). It is the only
record these rules read. The older `auth_sessions.via_device_sign_in` flag,
which a server of the previous release writes on its own, settles only
whether a session sees and ends just itself ([Sessions limited to
themselves](../auth-api.md#sessions-limited-to-themselves)): a session with
that flag and no `opened_via` is refused here like any session with no record
of how it was opened. The rule is one function, `auth.SessionAllows`, with one
action per kind of change:

- Every action needs the account's own login session
  (`Claims.IsOwnLoginSession`: no API key, no administrator acting as the
  account, no remote-playback handoff), and refuses a session opened by a key
  enrolled through a grant (another app): the app's access ends when its key
  is revoked, so it must not mint access that outlives that, nor read the
  person's devices.
- `ActionManageKeys` (`enroll`, `PUT /auth/keys/person`,
  `pending/{id}/approve`, `POST /auth/device/preapprove`,
  `DELETE /auth/device/preapprovals/{id}`) and `ActionReadKeys`
  (`GET /auth/keys`, `GET /auth/keys/pending`, `pending/{id}/deny`,
  `GET /auth/device/preapprovals`) also need a
  session opened by a password, a provider or the person's own device key. A
  session opened by device sign-in (a TV, a shared tablet, a browser approved
  by QR code) is refused: whoever holds it isn't necessarily the person, the
  device list is exactly what such a device is kept away from, and a TV must
  not refuse the administrator's new phone. A session with no record, older
  than the column, is refused the same way.
- `ActionApproveDeviceSignIn` (approving a device sign-in that is waiting now)
  refuses only the grant-enrolled app's session.
- `ActionReachOtherDevices` refuses only the grant-enrolled app's session too,
  and, unlike the actions above, passes every caller that is not the
  account's own login session, through `auth.SessionMayReachOtherDevices`.
  It keeps the app off the person's other devices: the household playback
  list leaves their streams out, the web push and native push writes refuse
  it, the playback progress, stop, replan and control-ticket routes refuse a
  stream another login session started, and Watch Together refuses it hosting
  or driving a room
  ([Other devices' playback and web push](../auth-api.md#other-devices-playback-and-web-push)).
  A device sign-in keeps what it has there; only the login session list
  limits it ([Sessions limited to
  themselves](../auth-api.md#sessions-limited-to-themselves)).
- `DELETE /auth/keys/devices/{id}` follows `ActionManageKeys`, except that a
  grant-enrolled app may revoke its own key. A device may revoke its own key,
  which ends the session asking.
- `GET /profiles/set` and the session form of `PUT /profiles/set` follow the
  same rule as `enroll` (see [The profile set](#the-profile-set)): a set can
  delete profiles and change their PINs and limits.

The key routes against each kind of session:

| Route | Password, provider, own device key | Device sign-in (a TV) | Grant-enrolled app | No `opened_via` | Admin acting as the account, API key, remote playback |
|---|---|---|---|---|---|
| `GET /auth/keys`, `GET /auth/keys/pending`, `pending/{id}/deny`, `GET /auth/device/preapprovals`, `DELETE /auth/device/preapprovals/{id}` | allowed | refused | refused | refused | refused |
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
  profile is 403 `main_profile_required`, on an administrator's account too.
- A request with no `X-Profile-Id` passes only from an administrator account
  that has at most one profile, with no PIN and no maturity or library limit:
  there the request can only mean that profile. This is not "administrators
  may leave the header out": an administrator's account with several
  profiles, or a PIN, names its main profile and sends its token like any
  other account. Anyone else, including a one-profile account that is not an
  administrator, must name the main profile; without it the request is 403
  `main_profile_required`. This is stricter than `changePassword`, which lets an
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
stored outside Postgres (`userdb.backend=sqlite`), where this transaction
can't read them.

What remains on the SQLite store is known and accepted. The handler's check
still runs there, so a secondary profile and an unproven PIN are refused as
everywhere. What is not caught is a change to the main profile (its PIN, or
which profile is the main one) that commits after the handler's check and
before the operation's own commit, which is the time the operation waits for
the account row plus its own short transaction. One request that was already
past the check goes through; the next one is checked against the new state.
The profile set and personal API keys are not offered on that store at all.

The key list, its deletes, the pending list and deny keep no profile rule:
any profile of the account may review and remove devices.

**Administrators.** An administrator reviews and removes another account's
keys on the Users page through `/admin/users/{user_id}/keys`: list, revoke a
device key, revoke the person key, deny a waiting key. These routes don't read
`opened_via` or the acting profile, and answer while the switch is off. Any
acting administrator lists any account. A change locks the acting
administrator, the target account and the request's audit actors in id order
(`lockAdminActorAndTarget`, shared with revoking login sessions and device
sign-ins approved in advance) and applies `CheckOwnerTarget`: an administrator
acts on an ordinary account and on their own, only the Owner on another
administrator or on the Owner. Then the key rows, then the sessions, the order
every key path takes, with one more account row in front; no key path locks two
accounts, so there is no cycle. No administrator route approves a waiting key:
approval is the person's, from one of their own sessions. The Owner may deny
another administrator's waiting key.

The administrator's list shows what the person's list hides: a provider-bound
key whose identity is gone from the account, marked `identity_gone`. It is
dead (revoked at its next use) but still occupies the person's device, so the
administrator sees why it no longer signs in. It shows how each key was
enrolled and, for a key a grant enrolled, the account whose person key allowed
it; nothing records which session or administrator vouched for a key beyond
that.

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

- **Revoking a device key** (the person's devices list, or an administrator on
  the Users page) ends its sessions at once, as above.
- **Revoking the person key** (an administrator on the Users page) stops it
  introducing devices and denies the devices it introduced that are still
  waiting, as replacing it does. Devices already enrolled keep their keys and
  sessions. The person sets up a new person key from a signed-in device.
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

## The profile set

A profile set is the account's household as one versioned document. The
person's app keeps it and sends it to each server the person uses, so the
same profiles, names, pictures, PINs, limits and settings exist on all of
them. `PUT /api/v2/profiles/set` makes the household match a set;
`GET /api/v2/profiles/set` reads it back as the server holds it. The
operation contract is in [auth-api.md](../auth-api.md#the-profile-set); the
service is `ProfileHandler.ApplyProfileSet` and the store's
`WithProfileSetTransaction`.

**What the set is on a server.** Each profile in the set carries `sync_id`
(`user_profiles.sync_id`), its ID on every server, and `sync_position`, its
place in the set. The account keeps the version and a content hash of the
last set it accepted (`users.profile_set_version`, `profile_set_sha256`,
`profile_set_accepted_at`). `user_profiles.sync_applied` records what the set
last wrote to each profile, including whether a profile created from `keep`
still waits for its PIN. A profile with no `sync_id` is outside the set.

**Who may send it.** Two forms:

- A `profile-set` message signed by the account's person key, verified on a
  challenge that it spends, so each signed set applies once. Its payload names
  `account_id`; a set addressed to another account is refused as a bad
  signature. The key must still be good for signing in: the provider and local
  password rules of a key sign-in run, and a provider-bound key whose identity
  is gone is revoked at this use. The signed form is refused for an
  administrator's account, so a person key alone never changes an admin's
  household, and while `auth.app_key_sign_in` is off. A key revoked while a
  verified set is being applied does not stop that set: the key is checked
  before the set's transaction, not inside it.
- The set from the account's own login session acting as the household's main
  profile, proved with its PIN token when it has a PIN (checked again inside
  the transaction against the profile's PIN revision). The session must pass
  the rule in [Who decides](#who-decides). Naming no profile never passes, on
  an admin account too. This form works with app keys off and for
  administrators' accounts.

`GET /profiles/set` takes the session rule without the PIN, so the person's
app reads the set in the background.

**The version rule.** Only a newer version changes anything. An older version
is `profile_set_stale`. The held version again never changes anything: with
other content it is stale; with the same content, each `set` PIN is compared
with the stored PIN and each `clear` with whether a PIN exists, and any
difference is stale. Otherwise it answers 200 and writes nothing. Because
that answer tells whether a `set` PIN matched, each such comparison on a
repeat spends the profile's PIN attempts as `verifyProfilePIN` does (a wrong
PIN counts, a right one clears the count), and a locked profile answers 429
without comparing; otherwise the main profile or the person key could read
another profile's four-digit PIN by repeating the held set. A newer version's
answer never depends on a PIN comparison, so it spends nothing. So a PIN
changes only with a higher version, and a retried or replayed old set cannot
undo a PIN changed since. These checks run on a first read and again under the
set's locks.

**Item members.** Every item carries every member the set defines today,
except `settings_reset`; an item that leaves one out is refused (422,
`required`). `settings` is required and `{}` means "leave every setting as it
is": the meaning an absent member would have, which is exactly why absence is
refused. An absent member is never read as false, empty or "no limit": a client
that forgot a field would silently drop a child's limit. The rule for later
versions follows from that. The members required today stay required. A
member added later is optional and has a stated default, so a set built by an
older app keeps being accepted by a newer server, and the default is chosen
so that leaving the member out can't loosen what the profile may see.

**One validation for both forms.** The session form's body is checked by the
request schema before its handler runs, and then by the set parser. A signed
set is opaque to the schema until its envelope verifies. The envelope's
verifier (`appkeys.VerifyProfileSet`) checks the signature, the challenge and
the envelope's own fields, requires only that a set is there, and hands the
`set`, `pairs` and `edits_seen` back as sent; it applies none of the
protocol's rules for what they hold, because a refusal there would name
`body.payload` and not the member. The handler then runs those three through
the same schema as the session body they would have been
(`validateSignedProfileSet`), and both forms go through the one parser
(`handlers.CheckProfileSet`) before anything is spent or locked. That shared
rule refuses everything the protocol's own content rule refuses
(`appkeys.Verify`, which the protocol's test vectors still run). A payload
with no set, or a null one, is a fault of the envelope. The two forms therefore cannot disagree on what a set may
hold, and they name a fault at the same location (`body.set.items[2].name`)
with the same code. A table test sends each malformed set both ways and
requires identical answers.

The content hash is SHA-256 over a canonical JSON document of the version and
each item as sent, members the server doesn't know included, keys sorted,
numbers read as doubles, and each `pin` reduced to its kind (`set`, `keep`,
`clear`). PIN digits stay out of it: a plain hash of a one-profile set would
give a four-digit PIN up faster than its bcrypt hash would. A `set` PIN is
compared with the stored bcrypt hash instead. `pairs` and `edits_seen` are not
content.

**Compared with what is stored now.** A newer set makes each profile equal its
item, except for settings marked `server_local`. Every field, PIN and setting
is compared with what the server stores at that moment, never with the last
set it accepted, and written only where it differs. An identical set writes
nothing: no row, no revision, no `pin_revision`, no access-policy revision.
`keep` never touches a PIN, including a longer PIN set on this server.

**Edits the sender hasn't seen.** `GET /profiles/set` answers `edits_token`,
which names the person's own edits on this server that no accepted set has
settled. A set sends it back as `edits_seen`. When the server holds such
edits, a newer set whose `edits_seen` is not the current token is stale and
writes nothing, so a set built before an edit can't silently undo it; with
none, `edits_seen` is not looked at. A repeat of the held version stays a
no-op whatever it says. The edits are described next.

**Edits made on this server.** A profile from the set can still be changed on
each server outside the set. The server records each such change in
`profile_set_edits`, one row per profile (by `sync_id`) and field, so the
person's app can merge it and the owner's choice on this server is not undone
by a set built elsewhere.

- *Marks and overrides.* The account's own sessions, API keys and
  Jellyfin-compatible sessions make marks (`by = 'account'`). An
  administrator on an admin route, or signed in as the account, makes
  overrides (`by = 'owner'`); `actor_user_id` is the administrator. A set
  leaves an overridden field as it is and reports it in `overrides`; an
  owner's deletion of a profile hides it, so a set neither creates nor pairs
  that `sync_id` here and reports it in `hidden_by_owner`. The later edit of a
  field replaces the row, whoever makes it, so an override is not a lock: the
  person's edit of an overridden field on this server makes it a mark, with
  `replaced_owner` set. An edit of either key of a pair the settings contract
  mirrors records both keys, so it ends the override on both. A profile
  delete clears its overrides, and a profile an administrator deleted stays
  hidden until the owner clears it (below). An override decides which value wins for a field of a profile
  both sides still hold; it does not lock the household. A set that drops the
  profile removes what the owner recorded for it, and an item under a new
  `sync_id` is a new profile with none of the old one's overrides, as the
  main profile can already delete a profile and create another through the
  profile routes.
- *Edits follow the profile.* An item that lands on an existing profile reads
  the edits recorded under that profile's `sync_id`, not the one the item
  names. Item 0 always lands on the main profile, so a set can give the main
  profile a new `sync_id`; its rows then move to the new ID in the set's
  transaction. Otherwise a new ID would shed the owner's overrides. The main
  profile can't take a `sync_id` the owner removed, which would end the
  hiding. Names are checked as the profiles will end up, a name the owner set
  included.
- *What an accepted set clears.* It deletes exactly the marks its token was
  computed from, matched by revision, so a mark written after that read is
  kept. A mark with `replaced_owner` is kept instead, stamped `settled_at`, as
  the record that the person ended an override; a settled row is history only
  and counts nowhere. Rows of a profile the set removes go with it. A repeat of
  the held version clears nothing.
- *The token.* `edits_token` is `"e1."` and 32 characters of base64url
  SHA-256 over each open mark's `sync_id`, field and revision, sorted. The
  revision comes from a sequence on every write of a row, so an edit that puts
  a field back still moves the token; a timestamp could repeat. Owner rows are
  left out: the person's app cannot settle them. So the token names the
  account's open marks and changes when a mark is added, settled, or replaced
  by an owner's edit of the same field; an owner's other edits leave it.
- *What `sync_applied` holds.* The set's value of every field it carries, an
  overridden field included, as if it had been written. A repeat of the held
  version does not compare a PIN the owner overrides, so a retry is not stale
  because of it.
- *The owner clears.* The admin routes clear an override, clear a hidden
  profile, and dismiss a settled row, each in the set's own transaction
  (`WithProfileSetTransaction`, naming every `sync_id` the account holds so it
  removes nothing), with the set's locks in the set's order and the edit row
  deleted last. Clearing an override applies the field's value from
  `sync_applied` with the set's per-field apply (`updateProfile` limited to
  that field, the four column-backed preferences included) under
  `WithoutRecording`, so the write is not an edit. A `set` PIN can't be
  applied, since the server holds no digits, and stays until a newer set
  compares and writes it; a `keep` PIN asks for nothing; a picture this
  server lacks waits for its upload; a setting the last set neither names
  nor resets is in no plan, so the clear writes nothing and the owner's
  value stays until a set names or resets the key. Clearing either half of a mirrored
  setting clears both rows, since a set writes the pair together. Clearing a
  hidden profile deletes the removal row only and creates nothing: the next
  accepted newer set creates the profile only if it has an item with that
  `sync_id` and the household has room (otherwise the item is left off); a
  set refused as a whole (a name clash, say) writes nothing; a set that
  pairs the `sync_id` with a profile outside the set gives it to that
  profile, which keeps its history; and a repeat of the held version
  creates nothing. Each clear then calls the wake hook
  (`profileedits.Waker`) itself, since its writes record nothing for the
  request's middleware to wake on, and answers what the hook reported: true
  only when that call queued a wake. With nothing recorded in the request,
  the hook skips its check that an edit committed; the clear calls it only
  after its own transaction has.
- *What an override clear must match.* The list gives each override a token:
  a SHA-256 over the account, the profile and the field and, for the field
  and a mirrored setting's other key, the set's value and whether it resets
  it, whether a clear applies it (all from the same rebuilt record the clear
  writes), and the edit row's revision, which moves on every recorded edit of
  the field. It reads only the profile row and the edit rows, which the
  clear's transaction has locked; the value in force is left out, since no
  confirmation shows it and an inherited value moves with writes outside
  those locks. The clear is a
  guarded `PATCH` whose `If-Match` must match the token recomputed in its
  transaction, after the profile rows are locked and before the name check,
  so a set accepted after the owner read the list refuses the clear (412)
  instead of the clear applying a value the owner never saw.

**Recording edits.** The store's shared write helpers record, in the write's
transaction: `updateProfile` for the set's item fields, compared before and
after under the row lock (a web save sends every field, so presence would
mark them all); the setting upsert, compare-and-set and delete helpers for a
profile-scope key a set carries, when the profile's effective value changes.
The value is resolved before and after the write, in its transaction, by
`settingsresolve` over the same candidate query the settings routes use and
the same administrator defaults reader the store is given
(`PostgresProvider.WithAdminDefaults`), with no policy constraint: the
profile's own row (locked first), else the account-scope row where the key
resolves through it, else the administrator's default, else the contract's.
An account-scope value written through the settings routes takes the
account's preference-settings lock, as the recording writers do, so it can't
change in between; an administrator default is read from the shared cached
reader, as the read routes read it, and a change landing between the two
reads at worst records or skips one edit.
A write or reset that leaves the effective value unchanged records nothing,
with the key
its contract mirror names (`settingscontract.MirrorKey`) recorded alongside,
since a set leaves the pair alone while either is the owner's; the profile
delete for the removal. The preference columns are recorded
through their settings keys only. The pool-level profile update runs in a
transaction of its own so the avatar writes and their record commit together.
Who acted comes from a holder the request gets before auth
(`profileedits.Middleware`), filled in by the native and Jellyfin-compatible
auth: the account itself, an administrator acting on another account (an
admin route; the role the admin gate checks), or an administrator signed in
as the account. Another account that is not an administrator records
nothing: no route lets one write another account's profiles, and either
label would be wrong. A write with no holder or no account
named (background work), and a context marked `WithoutRecording` (the set's
own apply, a restore set, a profile import), records nothing; restore and
import also write through raw SQL the recorder never sees. The username
rename skips a main profile with a `sync_id`, and the Jellyfin-compatible
configuration writer skips a value equal to the stored one. The SQLite user
store records nothing; it does not take sets either.

The edit rows are the last rows any transaction locks: after the account
locks, profile rows and the account row in the set, and after the profile row
in a profile save, avatar write or delete. The account's preference-settings
lock comes first: the profile, settings and Jellyfin-compatible configuration
transactions, a profile delete and the set take it always; the pool-level
profile update (the avatar routes) takes it always too, since it may also
lock the account row for an access field's revision; a pool-level setting
write takes it when it may record an edit, and otherwise writes one row on
its own and locks nothing else. An account deletion takes it before it locks
the account row. So a deletion runs wholly before or after a
recorded edit, never between the write and its record. The recording
statement also key-share locks the profile row it reads, so a delete of that
profile that took no such lock either waits for it or leaves it no profile.
The table has no foreign keys: one to `users` would take a key-share lock on
the account row on every edit. Deleting an account deletes its rows after the
account, in the same transaction, at both deletion sites.

After a request that recorded an edit, the middleware calls the server's
waker for each account that got one, on its own goroutine so the response
never waits for it. A caller that needs the answer calls the waker after its
write commits and gets it within five seconds. The waker queues a
`background_wake` push for each of the account's registered phones, at most 20
(`internal/notifications/push_profile_set.go`): a person can set a wake off
from their own session, and each phone costs an attempt row and a relay call.
The 20 are the phones a push was most recently accepted for, then the most
recently registered. APNs and FCM accept a push only for a real token of the
app, so registrations that never had a push accepted, such as ones with
made-up tokens, cannot take the place of a phone that has. That protects only
phones that have had a push accepted: a genuine phone that never has ranks by
registration time among the rest, so 20 more recently registered made-up
registrations rank ahead of it. With more than 20 live registrations, a new
phone that has not had a push accepted yet ranks behind 20 that have, and may
get no wake until a push to it is accepted. It wakes only for the request's
own edits that committed: the request remembers every edit revision it
recorded per account, and the waker checks that a row carrying one of them
exists. Revisions come from a sequence, so a write that rolled back sends
nothing, even when another request's edit of the account committed; that
request has its own wake. A later edit of the same field writes a new
revision over the row, so a request whose only edit was replaced that way
answers false and the later request's wake covers it. At most one wake goes
out a minute per account, on every API server, measured from when each wake
is admitted. The waker first reads the account's row in
`push_profile_set_wakes` without a lock, and inside the minute answers false
before reading any registration. Otherwise one statement decides and records
the wake: an upsert of that row that writes the new time, read from the clock
when it writes the row, after any wait for it, only when the last one is a
minute old. Two servers recording at once
queue one wake, no server keeps a timer, and removing a phone and registering
it again does not reset the limit, since the row is not tied to the account's
devices. The time is recorded in the transaction that queues the wake's
attempts, so a failure to queue them rolls it back. A send that fails after
that keeps the time, and the account's next edit inside the minute is not
pushed; the app learns of it when it opens. A second edit inside the minute
queues nothing; the phone learns of it from the next wake or when the app
opens. The table has no foreign key to `users`, which would lock the account
row on every wake and could deadlock with an account deletion. Notification
retention deletes rows a day old instead, a deleted account's included; until
then a deleted account's row is harmless, since account IDs are not reused.

The waker answers true when, and only when, this call queued a wake after the
edit committed: the wake's attempts were written and committed for at least
one phone. The answer is about the wake being queued, not delivered; it stays
true if the send then fails or runs out of time, so a caller may say a wake
was queued for the edit, not that a phone was reached. It answers false inside
the minute after another wake, while a wake for the account is still in
flight on this server, for an edit that rolled back, and with push off or no
phone to wake. A wake that left before the edit existed says nothing about
it. The waker never
fails the edit: errors are logged and the send runs in the background.

A wake is sent once and never retried. Its attempt row is inserted in flight,
as `retrying` with no `next_retry_at`, and the send records `delivered` or
`failed`. No claim query selects any of those, so no server's retry sweep,
including one of the previous release that would send it as an alert, ever
picks a wake up.

One wake of an account never overlaps the next. The phones are started in
the order above, four at a time, under one budget of 30 seconds, half the
one-minute interval, started when the wake is queued. Each send runs under
what is left of the budget: its relay requests, its database writes, and its
wait for the relay credential's lock all give up when the budget ends. That
lock is shared by every push the server sends; only a wake's wait for it is
bounded, and every other push waits for it as long as it is held, as before. One
send can take up to seven relay requests, more than the budget allows, so
there is no deadline per phone. A slow or dead registration holds one of the
four slots, and the account's other phones go on in the other three. Four
keeps one wake to a quarter of the 16 workers the push dispatcher already
sends through the same relay with.

Once every send has returned, one statement records every phone still in
flight as `failed`, on a fresh context limited to five seconds: a send that
panicked or stopped before it recorded an outcome as `wake_send_unfinished`,
a phone the budget didn't reach or let finish as `wake_out_of_time`. A
later wake may reach such a phone; nothing retries it, and the app reads the
set when it opens. The relay requests stop when the 30-second budget ends,
and that last write follows on its own five-second context, so the account's
in-flight mark, released after that write, a panic included, is held at most
35 seconds after the wake is queued. If that write fails or times out, the
server logs it and the rows stay in flight: no claim query selects them, so
nothing sends them, until the notification retention task marks them failed
once they are ten minutes old. By default that task runs daily; an
administrator can change or disable its schedule, which leaves such rows in
flight longer. A wake is queued up to five
seconds after its time is admitted, so it has ended within 40 seconds of
that, before the account's limit lets another start; a test holds the budget
plus the final write under the interval less those five seconds.

On each server, a wake of an account also does not start while the previous
one is still in flight there. That guard is a mark in the process, taken
only once the edit is known to have committed; a row in the database would
outlive a process that stopped mid-wake and hold the account's wakes until
retention. A committed edit that meets the mark answers false and queues
nothing. If the wake holding it then queues nothing (every phone went away
before its insert, or the insert failed), neither edit is woken; the app
reads the set when it opens and at the next wake. Across servers, the
budget and the account's limit row keep wakes apart. The worst case for one
account is one wake at a time on a server, each ending within 35 seconds of
being queued. Only a process that stops mid-wake, or a last write that fails,
leaves a row in flight. The push queue gauge leaves wake rows out,
because no worker ever claims one, and the notification retention task (by
default daily; the schedule can be changed) marks a row that has been in
flight for more than ten minutes as failed.
Retention deletes wake rows after seven days.

**Mapping.** The first item is always the main profile, which the set never
creates, deletes or swaps; a set naming the main profile's `sync_id` anywhere
else is refused. Other items match a profile by `sync_id`, then by `pairs`
(for the first use, naming a profile with no `sync_id`), and are otherwise
new. A profile with no `sync_id` that `pairs` doesn't name is left exactly as
it is and listed in `local_only`.

**Over the limit.** New items are created in the set's order while the
household is under its profile limit; the rest are reported in `left_off`.
Profiles outside the set count toward the limit, and nothing is deleted to make
room.

**Names.** Names are unique in the set, compared trimmed and without case as
everywhere else. The check runs on the household as the set leaves it, so a
rename chain applies; an item named like a profile outside the set refuses the
whole set (`name_conflict`). There is no unique index on names: the set
transaction holds every profile row and the account's preference lock, which
every other profile writer takes first.

**What a new profile starts with.** The item's fields and settings, no picture
unless the item names a preset this server knows, and no PIN unless the item
sends `set`. A profile created from `keep` is reported in `awaiting_pins`
until it gets a PIN. A child profile starts with library limits on and no
libraries: which libraries a profile sees is chosen per server, and is
reported in `awaiting_libraries` until someone picks them.

**Pictures.** `preset:<id>` sets a preset this server knows; an unknown preset
keeps the current picture and reports the item in `awaiting_pictures`.
`sha256:<hex>` is matched against the label of the profile's own upload only,
never against bytes and never across profiles. A hash the profile doesn't
carry keeps the current picture until the app uploads it with that
`source_sha256`. An upload from before the label was kept gets the hash of
the server's stored copy (a re-encoded file) the first time it is needed. A
label can be stale if a node from an older release replaced an upload during
a rolling upgrade; it is not repaired.

**Settings that stay on one server.** Keys the settings contract marks
`server_local` (they name a library, section, collection or uploaded image)
are ignored on write and left out of `GET`. Unknown keys and keys not stored
per profile are ignored. A known key whose value this server refuses is
skipped, the server's value kept, and listed in `skipped_settings`. `null` is a
value; `settings_reset` removes the profile's own value so it inherits again.
The four preferences the profile object still serves from columns
(`auto_skip_intro`, `auto_skip_credits`, `auto_skip_recap`,
`auto_play_next_preview`) are written with their settings.

**What removal deletes.** A profile holding a `sync_id` the set no longer has
is deleted as `deleteProfile` deletes one: its rows in every profile table,
its collections, its Jellyfin-compatible sessions, in the set's transaction,
then the same clean-ups after the commit (uploaded avatar, collection
artwork, devices, notification state, and the purges other services run).
Only profiles with a `sync_id` are ever removed, never the main profile. The
app confirms deletions with the person before it sends such a set.

**Locks.** The set's transaction takes the account's preference-settings and
section-override locks, the audit actors, the collections of the profiles it
removes in ID order, every profile row of the account in ID order, and the
account row last. That is a profile delete's order extended, and a profile
save locks its profile row before the account row, so neither deadlocks with
a set. Deleting the account locks the account row before its profiles, the
opposite order: a set and an account deletion running together can deadlock,
and PostgreSQL aborts one of them. An aborted set starts over; a set whose
account is gone answers as any request for a deleted account
(`session_expired`). The lock order stays, because it is the one profile
saves need.

**SQLite.** The SQLite user store keeps profiles in a per-account file while
other profile data lives in PostgreSQL, so a set can't be one transaction
there. `getAppKeyCapability` reports `profile_set` `false` and both routes
answer `capability_unsupported`.

## Rate limits

`createAppKeyChallenge` spends the per-address `key_challenge` budget;
`introduce`, `sign-in`, `enroll`, grant redemption, app code redemption and
`createAppCode` spend `key_sign_in`. Both default to 60 a minute with a burst
of 20, enough for a few waiting devices behind one address (each asks every
five seconds). `createInvitationAppCode` spends the invitation routes'
`invitation` budget, 20 a minute with a burst of 10: the invite page shares it
between the lookup, each code and each renewal. Neither budget can be spent
per key before the signature verifies; the challenge is what makes each signed
request single use.

Both of those are counted per client address, and stay so: a sign-in is
untrusted until its signature verifies, so there is no account to count it
against. What that means in practice:

- A device signs in with its key when its refresh token has run out, not on
  every start. One sign-in costs one challenge and one signed request.
- From one address, 20 devices can sign in at once and then one a second.
  A device over the budget gets 429 with `Retry-After` and tries again.
- The signed routes share one budget: an `introduce`, a `sign-in`, an
  `enroll` and a grant redemption from one address all draw on the same 60 a
  minute.
- Behind a reverse proxy that passes the client address on, each client has
  its own budget, and the proxy's own address has one ten times as large that
  all of them share. Behind a proxy that doesn't pass it on, every person's
  devices count as one client and share one 60 a minute, as they already do
  for password sign-in.
- An owner whose household or proxy needs more raises
  `ratelimit.auth.key_challenge.*` and `ratelimit.auth.key_sign_in.*`
  (requests per minute and burst).

`PUT /profiles/set` is limited per account, not per address: `profile_set`,
20 a minute with a burst of 10, is spent by the session's account, or by the
account a signed set names once its signature has verified
(`ratelimit.Middleware.AllowEndpointAccount`, called from the handler). A
household's devices share one address, and so does every account behind a
reverse proxy that doesn't pass the client address on; counting the address
would give all of them one person's allowance. The address still has a budget,
ten times the account's, spent in the gate before anything about the request
is trusted (`KeyEndpointByAccount`). An unverified signed request can only
count there. A forwarding proxy's own address gets that same tenfold budget,
not ten times it again. The signed form also spends a challenge, and has
spent it by the time the account's limit refuses the set.

## Multi-node rules

Nothing about app keys lives in a node's memory. Challenges, used grants, app codes, keys,
waiting keys' last tries and sessions are Postgres rows, so a challenge issued by one
API process is spent once on any of them, and a revocation on one ends the
sessions every node checks. The switch and the local-password setting are read
from `server_settings` on each request. The rate-limit budgets follow the
rate limiter's backend (cluster-wide with Redis).
The profile set wake's one-a-minute limit holds across API processes
through one upsert of the account's row in `push_profile_set_wakes` (see
[The profile set](#the-profile-set)).

## The switch

`auth.app_key_sign_in` (default on) turns app key sign-in on or off
server-wide. It is read from `server_settings` on each request, so a change on
any node applies at once. While it is off, `getAppKeyCapability` reports
`disabled`, and the challenge, `introduce`, `sign-in`, `enroll`, grant
redemption, both app code issues and app code redemption,
`PUT /auth/keys/person`, `pending/{id}/approve` and the signed
form of `PUT /profiles/set` answer
`capability_disabled`; the invitation lookup reports `app_join: false`. A person's own key list and its deletes, the pending
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
inherits it. A `demo.enabled` that can't be read fails those routes; it is
never taken as "not a demo". Approving a device sign-in in advance is not behind the switch;
it carries the ordinary demo restriction (refused for a caller who is not an
administrator) and the device login capability reports `preapprove: false`.
The key lists stay open in demo mode, so its visitors can see and revoke
keys enrolled before it was turned on, but `GET /auth/keys` and
`GET /auth/keys/pending` answer `enrolled_ip_address` as the empty string to a
caller who is not an administrator: those keys' addresses are not every
visitor's to see. The administrator's list is unchanged.

## Backups

Both key tables are in the backup and come back on a full restore, so an
account with no password can still sign in on its devices once the server is
restored. The rules are in [backups.md](backups.md#credentials); in short:

- The live server's row wins wherever it has one, so on the server that took
  the backup a key revoked since stays revoked and a key enrolled since keeps
  working. The backup's other rows come back.
- On a server that never held the keys, the backup's come back as the backup
  has them: a key revoked after the backup works again, and so does a person
  key replaced after it, as a password changed after the backup does. After
  such a restore, people check their devices list and revoke what they no
  longer use. The Silo apps also compare the server's key list with their own
  record at their next sign-in, but that is what the apps do, not something
  the server guarantees.
- Nothing is deleted except a restored row whose public key a live row of
  another account now holds, so a public key stays enrolled once, revoked rows
  included. The restore revokes, during the restore, a provider-bound key
  whose sign-in link is not in the restored data, a key enrolled since the
  backup on an account the backup gives more authority, and a waiting device
  key only the backup has.
- A signed request names the server ID, so the keys work only where the
  restored server answers with the backup's: on the server it came from, or
  on an install with no accounts, which takes the backup's ID
  ([backups.md](backups.md#server-identity-on-a-full-restore)). A full
  restore onto a server that has an account keeps that server's ID, and the
  backup's keys come back revoked.
- A separate restore (`--into`) keeps no keys, and an account a restore set
  creates has none: its devices sign in another way and enroll.

What doesn't restore, and what a person sees:

- Login sessions: every app signs in again. One with a working key does that
  by itself.
- Challenges (`auth_key_challenges`): a sign-in in progress is refused once,
  and the app asks for a new challenge.
- Used grant IDs (`auth_grant_uses`): left out. A grant lives five minutes, so
  a restore never revives one that was used.
- Waiting keys only the backup has: the device enrolls again and waits for an
  administrator's approval again.
- Device sign-ins, including ones approved in advance: the TV shows a new
  code to approve.

The local network TLS key comes from `SECRET_KEY`, not the database. A
restore under another `SECRET_KEY` gives the server a new TLS key, and every
device reports that the server's key changed.

A restore of selected accounts does not carry the profile set: restored
profiles have no `sync_id`, so they are outside the set and the app pairs them
again. A full database restore carries everything, the edits made on the
server included (`profile_set_edits`): marks, overrides and hidden profiles
come back with the profiles they name. A restore set and a profile import
carry no edits and record none.

## Cloned databases

A cloned database carries the server ID (see
[server-identity.md](server-identity.md)) and every key with it. The clone
accepts the same keys as the original, and the server ID alone cannot tell
the two apart. That is consistent: whoever holds a copy of the
database already holds the same accounts.
