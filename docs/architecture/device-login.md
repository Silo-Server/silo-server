# Device sign-in

A device without a keyboard (a TV) signs in by showing a code that a person
approves somewhere they are already signed in: the web page `/activate`, or a
Silo phone app. The device never handles a password. The state machine lives in
`internal/auth/device_login.go`; the operations are in
`internal/apiv2/device_login.go`, over the handler methods in
`internal/api/handlers/auth_device.go`.
The operation contract is in [auth-api.md](../auth-api.md#device-sign-in).
Jellyfin apps reach the same requests through Quick Connect; see
[Jellyfin Quick Connect](#jellyfin-quick-connect). A device can tie its
requests to a secret it keeps; see [Session binding](#session-binding).

## Codes

- **User code:** eight digits. The wire form is `4821-7730`; clients display
  it as `4821 7730` (4+4, space-separated) and people type it on phone
  keypads or read it aloud. Lookups ignore spaces and dashes. It is stored
  only as a hash.
- **Unique per server, not globally.** Every deployment issues its own codes,
  and two servers can show the same digits at the same time. A code means
  nothing without the server that issued it; no client may treat a bare code
  as globally meaningful. `user_code_hash` is unique across every stored
  request, so a start that draws a stored code draws again (up to five times).
- **Device code:** 32 random bytes the device polls with. Never shown.
- **Browser code:** 32 random bytes. No current link carries it; lookups by
  `token` keep working for links issued before user codes moved into the
  link.
- **Match code:** two English words. Kept on the wire for older clients and the
  LAN setup protocol's consistency check. People compare the user code
  instead; no client displays the match words.

## Verification link

`verification_uri` is `<base>/activate` and `verification_uri_complete` is
`<base>/activate?code=<8 digits>`, which keeps the TV's QR code small. `<base>`
is `server.public_url` when it is configured and a valid origin, because a
phone off the TV's network can't open a LAN address; otherwise it is the origin
the device used, from `clientip.RequestOrigin`: `X-Forwarded-Proto` and
`X-Forwarded-Host` count only when a trusted proxy sent them. There is no
shared short URL: each server's link is its own.

## Lifetimes

- A request lives 15 minutes from start.
- The first approver lookup of a pending request records `opened_at`; the
  device's poll then reports `opened: true`, and the TV stops replacing its code.
- Every lookup of a pending request extends its expiry to at least 5
  minutes from now, never past 30 minutes after it started, so repeated
  lookups can't keep a code alive.
- A pending poll answer carries the request's current `expires_at`. The
  device moves its local deadline to that value, so a TV following the start
  answer's `expires_in` doesn't give up on a code someone is approving.
- A request [approved in advance](#approval-in-advance) lives 30 days and is
  never extended.
- Requests more than a day past their expiry are deleted by the
  `cleanup_device_logins` step of the `database_maintenance` task
  (`DeviceLoginRetention`).

## States

`pending` → `approved` → `consumed` is the sign-in path. `denied` is the
approver refusing. `canceled` is the device withdrawing its own request with
its device code (`cancelDeviceLogin`), from `pending` or from `approved` before
the tokens were collected, so an abandoned code can't be approved later. A
cancel after expiry changes nothing and answers `expired`. `expired` is
derived from `expires_at`, not stored.

Decisions on a canceled request are refused. Password resets withdraw
approvals that were not yet collected.

## Who decides

Approve, approve-handoff and deny need a login session of the account. An
API key, an impersonation session or a
[remote-playback handoff](#remote-playback-handoff) session is refused with 403
(`permission_denied`) before the code is looked at. An
approval gives the device a login session of the account: from an API key
that would turn a credential no refresh re-checks into a refreshing session,
from an impersonation session it would give the device a session with no
trace of the impersonator, and from a handoff it would turn a day on one
profile into an ordinary sign-in. Deny follows the same rule, so only the
people who can approve decide. A device approved from a session opened through an
external sign-in provider inherits that session's provider chain
([external-sign-in.md](external-sign-in.md#provider-re-check)).

The first approval holds. Approving a request another account (or, for
approve-handoff, another profile) already approved is refused with 409
(`conflict`), and so is another account's deny: anyone
holding the code could otherwise cancel a sign-in someone else approved. Deny
from the approving account, from any of its profiles, withdraws its approval
before the device collects it. An approval whose approving account or profile
was deleted can no longer be collected, and any account may deny it.

Approval locks the account and checks that the submitting login session is
still active before updating the device request. Collection and credential
revocation also lock the account before the device request. A request that
authenticated before its session was revoked cannot approve afterwards.
Provider refusal withdraws approvals made through that identity, including
those from a break-glass account; its approvals made through local login stay
independent of the provider.

## Session binding

A phone setting up a TV over the LAN can sign it in to several accounts and
servers in one session. The person compares the TV's code once, for the first
request; the phone approves the later requests without a comparison. Without
more, an attacker on the LAN path could swap one of those later requests for
its own, and the phone would approve it with the person's account. Session
binding lets the phone tell the TV's requests from anyone else's:

1. For one setup session the TV draws a 32-byte random secret and keeps it. It
   sends it only over HTTPS to the server.
2. Every v2 `startDeviceLogin` in that session, on every server, sends
   `binding`: base64url without padding of SHA-256 of the secret, 43
   characters of `[A-Za-z0-9_-]`. Anything else, including an empty string,
   is 422 `validation_failed` at `body.binding`. Without `binding` the request
   is unbound, as before.
3. `getDeviceLogin` returns the request's `binding` (absent when unbound). The
   phone trusts the binding of the request whose code the person compared. It
   approves a later request with the same binding without a comparison, asks
   for a comparison when a later request has none, and refuses one with
   another binding.
4. Every `pollDeviceLogin` of a bound request must send `binding_secret`, the
   secret as unpadded base64url. The server compares base64url(SHA-256(secret))
   with the stored binding in constant time. A poll without it, or with
   another secret, is 403 `device_binding_mismatch` whatever the request's
   state, and changes nothing: the request is neither consumed nor marked, and
   the TV holding the secret can still collect it. `binding_secret` is ignored
   for an unbound request. A `binding_secret` longer than 64 characters is
   422 `validation_failed` at `body.binding_secret` before it is hashed.

The binding is public, since anyone holding the code can read it, but it is
only a hash. An attacker that copies it into its own request gets a request
the phone approves without asking, yet it cannot poll that request to tokens
without the secret. The worst it can do is make one sign-in fail.

The binding is stored as `binding_hash` on `device_login_requests` and never
changes after start. Approve, approve-handoff, deny and cancel ignore it.
`GET /api/v2/auth/device/capability` reports `session_binding: true`; a TV
sends `binding` and `binding_secret` only to a server that reports it. The
phone needs no flag: it acts on what the lookup returns.

A TV uses one secret for every server in a pairing session, so each of those
servers learns it from the TV's polls. A hostile server among the person's
chosen targets, working with an attacker on the network path, could therefore
start a bound request on another chosen server that the phone approves
without a comparison. The project accepts that residual risk; see the client
design for the alternatives.

**Rolling deploys.** A node from an older release ignores `binding` on start
and does not check `binding_secret` on poll. It cannot collect a bound request
all the same: the schema refuses to mark a bound request consumed unless
`binding_checked` is set, and only this release sets it, after checking the
secret. An older node's collection of a bound request fails as a server error
and rolls back the session it inserted, so the request stays approved for the
TV to collect through an updated node. Rolling the migration back refuses
every live bound request before it drops the columns.

## Approval in advance

A phone that joins a new server can get a TV into it while the TV is off. The
phone asks the server for a sign-in that is already approved, tied to one of
the TV's [session binding](#session-binding) fingerprints, and passes the
device code to the TV through its own channel. The TV redeems it later with
the ordinary `pollDeviceLogin`, sending the device code and the secret behind
the fingerprint. Nothing on the poll changes for the TV.

- **Creating it.** `POST /api/v2/auth/device/preapprove` takes `device_name`,
  `device_platform` and `binding`, and answers 201 with `id`, `device_code` and
  `expires_at`. Only the account's own login session may call it: an API key,
  a session an administrator opened as the account, or a remote-playback
  handoff is 403 `permission_denied`, and a temporary-password session is
  stopped by the middleware as for every other route. A session opened by
  device sign-in, or by an app key that another app was granted, is refused
  with 403 `permission_denied` too (`auth.SessionAllows`, see
  [app-keys.md](app-keys.md#who-decides)): the sign-in it creates outlives the
  session that made it. The account and the
  approving session are locked and checked as for an ordinary approval, and the
  row is inserted `approved`, with `preapproved_at` set, `ip_address` NULL and
  the session's provider chain. The row keeps `client_purpose = 'device_login'`,
  so the poll path and older nodes treat it as any approved bound request.
- **Reachable only by its device code.** The device code is 32 random bytes
  (256 bits). The browser and user codes stored beside it are random 32-byte
  values that are never returned, and `getDeviceLogin`, approve and deny refuse
  a pre-approved row even when one of those codes matches, so no eight-digit
  or six-digit code finds it. `cancelDeviceLogin` takes no secret, so it leaves
  a pre-approved row as it is and answers 404. Only `pollDeviceLogin` with the
  device code reaches it, and only with the secret does it give tokens.
- **Lifetime.** 30 days from approval, single use. The poll that collects it
  moves it to `consumed` in the same transaction that opens the session, as for
  any request. Lookups never extend it (the opened hold applies only to pending
  requests), and retention deletes it a day after it expires, as any request.
- **Cap.** An account holds at most 20 waiting (approved, unexpired,
  uncollected) pre-approved sign-ins. Another is 409 `conflict`. The count runs
  under the account lock, so concurrent calls cannot overrun it.
- **What ends it.** Revoking it from the list (below) moves it to `denied`.
  The account-wide events that withdraw an ordinary uncollected approval
  withdraw it too: signing out everywhere, an administrator disabling the
  account or setting its password, a password reset, a password change that
  signs out other sessions, a sign-in provider refusing the identity, and the
  sign-in policy changes that revoke sign-ins. A poll for an account disabled
  or deleted since answers `denied`; this holds for every device sign-in, not
  only pre-approved ones, since over 30 days it is a normal case.
- **The approving session.** The row records the login session that approved
  it (`preapproved_session_id`), and a sign-in approved in advance stands only
  while that session is live: not revoked, not expired. The poll that would
  collect it reads the session's state (a plain read, under the account and
  request locks the poll already holds) and answers a request whose approver
  has ended as `denied`, as it answers a withdrawn one; the lists and the cap
  leave such a request out. However the session ended (the session list, an
  administrator, logout, a revoked app key that opened it, an account-wide
  revocation, a node from an older release, or simply expiring), a session
  someone else was using therefore cannot leave a sign-in behind for later,
  and cannot hide one by logging itself out. Sign-ins the account approved
  from its other sessions stay, and so does a device that already redeemed:
  its session is its own and is ended from the session list. Ending a session
  writes no request row; the request stays approved in its row, nothing can
  collect it, and retention deletes it a day after it expires. A collect that
  read the approver as live a moment before it ended still succeeds, as an
  approval given a moment before a logout does.
  An approving session that simply expires now stops its sign-ins at once;
  before, they lasted until they expired.
- **Provider identity at redemption.** When the approving session came from a
  provider identity, the poll locks and checks that identity as a provider
  sign-in does, before the device row: a refused identity, an unlinked one, or
  an identity whose installation is gone refuses the poll as `denied` and
  withdraws the row; a disabled installation answers 503
  `provider_unavailable` and leaves the row for a later poll. The device's
  session carries the identity and the approving session's `provider_since`,
  as every device sign-in does. Redeeming is not a provider sign-in, so it
  never restarts the limit that ends a chain the provider can no longer vouch
  for ([external-sign-in.md](external-sign-in.md#provider-re-check)): where
  the provider cannot re-check, a sign-in redeemed late yields a session
  that ends `auth.refresh_token_expiry` after the phone's provider sign-in,
  which can be soon. A session whose identity was already gone cannot
  pre-approve (403 `not_permitted`).
- **Address.** The device's session records the address of the polling
  device, not the phone's.
- **List and revoke.** `GET /api/v2/auth/device/preapprovals` and
  `DELETE /api/v2/auth/device/preapprovals/{id}` serve the account's own login
  session, opened by a password, a provider or the person's own device key
  (`auth.SessionAllows`, `ActionReadKeys` for the list and `ActionManageKeys`
  to withdraw): a shared TV's session or another app's may neither read nor
  withdraw the waiting approvals; `GET /api/v2/admin/users/{user_id}/device-preapprovals` and
  `DELETE .../{id}` serve administrators, under the owner protection that
  revoking login sessions has. Items carry `id`, `device_name`,
  `device_platform`, `created_at` and `expires_at`, never the device code or
  the fingerprint. The list is bounded by the cap and not paged. Revoking a
  row already withdrawn succeeds; any other id is 404.
- **Wrong secrets.** Besides the per-address `device_poll` budget, each
  pre-approved request has its own budget of wrong binding secrets, keyed by
  its device code hash: 10 attempts, then 15 minutes locked
  (`ratelimit.DevicePreapprovalPolicy`), counted in Redis when the deployment
  has it. Only a wrong secret spends an attempt. While locked, a wrong secret
  is 429 `rate_limited` with `Retry-After`. The right secret is never
  refused by the budget and clears the count, and wrong secrets never change
  the row, so someone who learns a device code can neither keep the TV out
  nor destroy its sign-in.
- **Rolling deploys and rollback.** A node from the release before this one
  does not know the marker: it would let a bare device code cancel a
  pre-approved request and would collect one without the provider check
  above. Clients create pre-approved sign-ins only after
  `GET /api/v2/auth/device/capability` reports `preapprove`, which an older
  node never does, and an upgrade should finish before the feature is relied
  on. Rolling the migration back deletes the waiting pre-approved requests.
- **Deploying the change that dropped the withdrawal trigger.** While a node
  still on the build before
  `20261126204848_drop_session_preapproval_withdrawal_trigger` runs beside
  this one, that node honors a sign-in approved in advance whose approving
  session ended in that window, because it collects any approved request and
  the trigger that withdrew it is gone. Rolling that migration back withdraws
  every request still approved in advance whose approving session has ended,
  so the earlier build finds none to collect. The earlier build also ends
  sessions with request rows before session rows, through the trigger, so it
  can deadlock with this build's paths that end sessions (PostgreSQL aborts
  one, which can be repeated); and its ownership transfer does not take the
  ownership lock (see app-keys.md, "Lock order").
- **Promotion and the other account events.** These withdraw every uncollected
  approval of the account on purpose, in advance or at the moment, through
  one function (`withdrawDeviceSignInApprovalsInTransaction`), after the
  account's sessions and with the request rows locked in id order:
  - signing out everywhere or out of every other session, which a password
    reset, a password change, an administrator setting the password,
    disabling or deleting the account, and the sign-in policy changes that
    revoke sign-ins all do (`revokeSignInsInTransaction`);
  - an administrator signing the account out everywhere (`RevokeAsAdmin`);
  - promotion to administrator or the Owner, and a move of ownership
    (`revokeCredentialsIssuedToNonAdmin`; see "Promotion" in
    [app-keys.md](app-keys.md#promotion)).

  A sign-in provider refusing an identity withdraws the approvals made
  through that identity, and the person denies or revokes one at a time.
  Ending a single session writes no request row. Collecting takes the account
  row before the request row, as each of these does, so a collect after one
  commits is refused, and one before it succeeds for the account as it was.

`GET /api/v2/auth/device/capability` reports `preapprove: true`.

## Remote-playback handoff

A phone sends playback to a TV that is not signed in to the account, such as a
friend's or a hotel's, with a `remote_playback` request (`temporary` must be
true). The phone approves it with `approve-handoff`, which binds it to the
phone's verified profile. Collecting it gives the TV a temporary login session
of that one profile. The poll answer is unchanged: `tokens` (access and
refresh), `profile_id`, `profile_token`, `temporary: true` and
`session_expires_at`. The Apple and Android TV apps require the refresh token.
The web approval page (`/activate`) never approves a handoff: it shows the
request as playback sent from a Silo app, offers only deny, and follows a
pending one until the app decides it.

The session row (`auth_sessions`) records what the session is, and every node
enforces it from the row rather than from the tokens, so a token issued before
the session was bound is held to the same rules:

- `purpose` is `remote_playback` (`account` for every other sign-in).
- `bound_profile_id` is the approved profile.
- `absolute_expires_at` is when the session ends: 24 hours after collection,
  or `auth.refresh_token_expiry` when that is shorter. `session_expires_at`
  reports it.

**Lifetime.** A refresh issues a new access and refresh token as for any
session but never moves `expires_at` past `absolute_expires_at`; a database
trigger holds the same limit for every writer. Both tokens expire no later
than the session's end, and `expires_in` counts down to it when that is
sooner than the usual access lifetime. Once it passes, refresh answers 401, and the
TV apps end the handoff.

**One profile.** A request that names no profile acts as the bound profile, so
the viewer gate resolves its limits and, for a PIN profile, checks the
`profile_token`. A request naming another profile is refused with 403.
`GET /profiles` lists only the bound profile. Tickets for the events and
playback-control sockets carry the bound profile, and the socket refuses a
ticket for any other. The events socket delivers only the bound profile's
events and account-wide ones, and offers no history-import channel.

**No account powers.** The session acts with the account role `user` whatever
the account's role is, and is not the account's own login session. `GET
/account/me` still describes the account itself. It reaches
only the v2 routes in `remotePlaybackRoutes`
(`internal/api/middleware/remote_playback.go`): the session itself (`GET
/account/me`, logout), playback and its control socket and media, next-up
and browsing reads for the bound profile, its track choices, subtitles and
their sync and timing, the settings reads the player makes and the events
socket. Every other route answers 403
(`permission_denied`), so a route added later stays closed to handoffs
until it is listed, and so is a path whose encoding the router would read
differently. That shuts out:

- admin routes, including on an admin account;
- account management: password, API keys, login sessions, linked sign-ins,
  provider logout, plugin launch;
- household management, even when the bound profile is the primary one;
- device approvals of every kind, Quick Connect included;
- settings writes, downloads and download links, Apple push registration,
  Watch Together and plugin pages.

The checks that do not run behind `RequireAuth` refuse a handoff too: Apple
notification display tokens and direct-download links an older release minted
for one, a Watch Together socket ticket minted before the session was bound,
plugin access, and approvals (the approving session must be
`purpose = 'account'` while the account is locked).

Stream tokens already in media URLs are not tied to the login session, as for
any sign-in: once a handoff ends it cannot start or replan playback, and the
API's media routes refuse its bearer, but a proxy or transcode-node URL minted
for a playback it already started stays usable until that token expires.

**Sessions issued before this rule.** The migration binds each handoff whose
device request is still stored and ends it 24 hours after it was created; one
already older than that ends at once. Device requests are deleted a day after
they expire, so a handoff collected before then cannot be told apart from an
ordinary sign-in and keeps the lifetime and powers it had. The account owner
can find it in the session list and revoke it.

**Rolling deploys.** While nodes from an older release still run, a trigger on
`device_login_requests` binds the session such a node creates when it collects
a handoff, in the same transaction, and the `expires_at` trigger holds the
limit when such a node refreshes one. An older node does not check the
session's purpose on requests. It serves a handoff's tokens with the old powers
when their role matches the account's. On an admin account it refuses this
release's tokens, which say `user`, as tokens to refresh, so until every node
runs this release that handoff's requests fail on older nodes. Rolling the
migration back ends every handoff session rather than leaving it readable as an
ordinary sign-in.

## Rate limits and guessing

Rate limits are per server and keyed on client IP. The lookup and all three
decisions (approve, approve-handoff, deny) take a user code, so they spend one
shared per-IP budget, `device_lookup` (60 per minute, burst 20 by default), in place of the generic authenticated limiter. A
guesser therefore gets about 60 x 15 = 900 guesses over a code's 15-minute
life, at most about 1,800 over the 30-minute cap, against 10^8 codes; a hit
only lets them approve a stranger's TV into their own account. Six-digit Quick
Connect codes have their own rules; see
[Jellyfin Quick Connect](#jellyfin-quick-connect). The budget is
loose enough for a household behind one NAT: an approval is one lookup, one
decision and at most 20 watch lookups a minute. Start, poll and cancel use the
`device_start` and `device_poll` budgets. The limits apply only while rate
limiting is enabled.

## Multi-deployment rules

Every Silo server is independent; there is no Silo-run service between them.

- A code means nothing without the server that issued it (see Codes).
- The link on the TV and in the QR is always the issuing server's own; there
  is no fixed short URL.
- An app that takes a typed code uses the active server, or asks which saved
  server when there are several. It never looks a code up on every saved
  server.
- The approval card names the server by display name (`server_name`) and host
  next to the device and when it was requested (`requested_at`), so a
  collision or a wrong-server lookup is visible before anyone approves.
- Links into the phone apps carry `server_id` so the app can find the saved
  server by identity rather than by URL. The web approval page (`/activate`)
  offers "Open in the Silo app" on phones while signed out, linking to
  `silo://device?server=<server_id>&url=<origin>&code=<code>`: `server` is
  `getDeviceLogin`'s `server_id` (left out when absent), `url` the page's
  origin, for adding the server when it isn't saved, and `code` the user code
  without its separator. The app matches `server` against its saved servers
  and falls back to the exact `url`; it approves with its own session and
  never switches its active server. The link is only ever a tapped button.

## Jellyfin Quick Connect

A Jellyfin app on the Jellyfin-compatible API signs in with Quick Connect
([jellycompat-api.md](../jellycompat-api.md#quick-connect)). Its request is a
device sign-in request with `client_purpose = 'jellyfin_quick_connect'`, so it
keeps every rule above (lifetimes, opened hold, states, who decides, clusters)
except these:

- **Code:** six digits without a leading zero, the form Jellyfin apps show,
  ungrouped. It is derived one-way from the request's secret
  (`auth.QuickConnectCode`) instead of being drawn beside it, because Jellyfin
  apps redraw the code from every `Connect` poll while the server stores codes
  only as hashes. A clash with a live code draws a fresh secret; an expired
  Quick Connect request gives its code back at once instead of holding it until
  retention deletes the row.
- **Secret:** the device code, in Jellyfin's form: 32 random bytes as 64
  uppercase hex digits.
- **Profile:** a Jellyfin session belongs to one profile, so the approval names
  it. Approvers use `approve-handoff`, whose profile is the request's verified
  `X-Profile-Id` (a PIN profile needs its `X-Profile-Token`), or a signed-in
  Jellyfin app's `/QuickConnect/Authorize`, which approves for that session's
  own profile. `approve` refuses a Quick Connect request with 409.
- **Collection:** only `/Users/AuthenticateWithQuickConnect` collects it, into a
  compat session for the approved profile; the native poll answers 404 for its
  secret, because a native session could pick any profile. The compat session
  is written in the transaction that consumes the request, under a token
  derived one-way from the secret: a failed write leaves the request approved,
  and asking again with the secret while the request lives returns the same
  session without minting another login session. An account that
  must replace a temporary password is refused, as in Jellyfin password
  sign-in, and the approval is withdrawn so the app stops polling.
- **Not temporary:** the session lasts like any sign-in.
- **No device cancel:** Jellyfin has no route for an app to withdraw its
  request; an abandoned code expires. Approvers can still decline it.

Six digits are 100 times fewer than eight, so a Quick Connect code is found
only for a signed-in account:

- `getDeviceLogin` stays public; it reads a bearer token when one is sent
  (`AttachSession`) and uses it only when it belongs to an active login
  session. Without one, a six-digit lookup answers 401, and from an
  impersonation session 403, whether or not the request exists; a client
  holding an expired token refreshes it instead of reading the code as
  unknown.
- Every lookup or decision with a six-digit code by an account spends one
  attempt of that account's budget, `ratelimit.QuickConnectCodePolicy`: 20
  attempts, then 15 minutes locked, answered 429 `rate_limited` with
  `Retry-After`. Approving one device takes two. Every attempt counts, a hit
  included, and nothing resets the count, so an account cannot reset it by
  looking up a request it opened itself. The budget lives in Redis when the
  server has Redis, shared by every node and by the native routes and
  Jellyfin's `Authorize`, and in process memory otherwise. Like the profile PIN
  limit, it fails open while Redis cannot answer, leaving the per-address
  budget.
- The per-address `device_lookup` budget still applies on top.

An account therefore gets at most about 40 guesses at a code over its
30-minute cap, against 9 x 10^5 codes. Eight-digit codes are not counted
against the account. The approval page neither watches a Quick Connect request
nor reloads it after a decision, since each lookup would spend the budget.

Starting a request needs no account, so the per-address `device_start` budget
is what bounds how many live requests one address holds. Many addresses can
still fill a noticeable share of the six-digit space for as long as their
requests live; a mistyped code is then more likely to land on someone else's
request. The approval card names the device, its app and the address it came
from so the approver can tell.

## Clusters

All state lives in `device_login_requests` in Postgres. A request started on
one node can be looked up, approved, canceled and collected on any other;
`TestDeviceLoginAcrossNodesDB` covers it with two services on separate
connection pools.
