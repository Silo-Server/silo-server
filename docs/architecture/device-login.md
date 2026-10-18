# Device sign-in

A device without a keyboard (a TV) signs in by showing a code that a person
approves somewhere they are already signed in: the web page `/activate`, or a
Silo phone app. The device never handles a password. The state machine lives in
`internal/auth/device_login.go`; the operations are in
`internal/apiv2/device_login.go` and, frozen, in `internal/api/handlers/auth_device.go`.
The operation contract is in [auth-api.md](../auth-api.md#device-sign-in).
Jellyfin apps reach the same requests through Quick Connect; see
[Jellyfin Quick Connect](#jellyfin-quick-connect).

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
- **Browser code:** 32 random bytes. Only the frozen v1 start link still
  carries it; lookups by `token` keep working for those links and for v2
  links issued before user codes moved into the link.
- **Match code:** two English words. Kept on the wire for older clients and the
  LAN setup protocol's consistency check. People compare the user code
  instead. TV apps released before user codes show only the match words, so
  until both TV apps display the user code, the `/activate` card also prints
  the words on a secondary "Older TV apps show ..." line. Remove that line
  once the TV apps that show user codes have shipped.
- **The TV side of the same fallback.** Phone apps released before user
  codes show only the match words when they confirm a LAN pairing, so the
  TV's LAN pairing confirmation (Apple `TVPairingReceiverView`, Android
  `TvPairingPanel`) also prints the words on a secondary line, "Older phones
  show <WORDS> instead.", where `<WORDS>` is the request's `match_code`.
  The TV's code and QR sign-in screens show no match words. Remove that line
  once the phone apps that show user codes have shipped.

## Verification link

`verification_uri` is `<base>/activate` and `verification_uri_complete` is
`<base>/activate?code=<8 digits>`, which keeps the TV's QR code small. `<base>`
is `server.public_url` when it is configured and a valid origin, because a
phone off the TV's network can't open a LAN address; otherwise it is the origin
the device used. There is no shared short URL: each server's link is its own.
The frozen v1 start keeps its own link; see Frozen v1 surface.

## Lifetimes

- A request lives 15 minutes from start (10 minutes when started through the
  frozen v1 route).
- The first v2 approver lookup of a pending request records `opened_at`; the
  device's poll then reports `opened: true`, and the TV stops replacing its code.
- Every v2 lookup of a pending request extends its expiry to at least 5
  minutes from now, never past 30 minutes after it started, so repeated
  lookups can't keep a code alive.
- A pending poll answer carries the request's current `expires_at`. The
  device moves its local deadline to that value, so a TV following the start
  answer's `expires_in` doesn't give up on a code someone is approving.
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
(v2 `permission_denied`, v1 `forbidden`) before the code is looked at. An
approval gives the device a login session of the account: from an API key
that would turn a credential no refresh re-checks into a refreshing session,
from an impersonation session it would give the device a session with no
trace of the impersonator, and from a handoff it would turn a day on one
profile into an ordinary sign-in. Deny follows the same rule, so only the
people who can approve decide. A device approved from a session opened through an
external sign-in provider inherits that session's provider chain
([external-sign-in.md](external-sign-in.md#provider-re-check)).

Approval locks the account and checks that the submitting login session is
still active before updating the device request. Collection and credential
revocation also lock the account before the device request. A request that
authenticated before its session was revoked cannot approve afterwards.
Provider refusal withdraws approvals made through that identity, including
those from a break-glass account; its approvals made through local login stay
independent of the provider.

## Remote-playback handoff

A phone sends playback to a TV that is not signed in to the account, such as a
friend's or a hotel's, with a `remote_playback` request (`temporary` must be
true). The phone approves it with `approve-handoff`, which binds it to the
phone's verified profile. Collecting it gives the TV a temporary login session
of that one profile. The poll answer is unchanged: `tokens` (access and
refresh), `profile_id`, `profile_token`, `temporary: true` and
`session_expires_at`. The Apple and Android TV apps require the refresh token.

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
sooner than the usual access lifetime. Once it passes, refresh answers 401 on
v1 and v2, and the TV apps end the handoff.

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
(v2 `permission_denied`), so a route added later stays closed to handoffs
until it is listed, and so is a path whose encoding the router would read
differently. That shuts out:

- admin routes, including on an admin account;
- account management: password, API keys, login sessions, linked sign-ins,
  provider logout, plugin launch;
- household management, even when the bound profile is the primary one;
- device approvals of every kind, Quick Connect included;
- settings writes, downloads and download links, Apple push registration,
  Watch Together and plugin pages;
- every frozen `/api/v1` route except the public refresh.

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
decisions (approve, approve-handoff, deny) take a user code, so on both v1
and v2 they spend one shared per-IP budget, `device_lookup` (60 per minute,
burst 20 by default), in place of the generic authenticated limiter. A
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

## Frozen v1 surface

`/api/v1` has no cancel route and keeps its values:

- Start answers `expires_in` 600 for a 10-minute request, and
  `verification_uri_complete` is `<origin>/activate?token=<browser code>`.
  `<origin>` is the address the device used, not `server.public_url`, because
  the frozen v1 answer is defined by the request host. The user code is
  eight digits, which the v1 `XXXX-XXXX` format admits.
- Lookups never mark a request opened or move its expiry; only v2 lookups do.
- A canceled request reads as `denied` in v1 lookups and polls, and a
  decision on one answers v1's `denied` conflict code.
- Responses never carry `opened`, `expires_at` on polls, `requested_at`,
  `server_id` or `server_name`.
- Approve, approve-handoff and deny spend the `device_lookup` budget, as on v2.
- Approve, approve-handoff and deny refuse API keys and impersonation
  sessions (see Who decides). v1 applies the rule too because it closes a
  way to mint a refreshable login session from an API key, and it adds no
  new v1 value: the refusal is v1's existing 403 with its existing
  `forbidden` error code.

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
  unknown. The frozen v1 routes never pass an account, so a six-digit code is
  unknown to them.
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
