# Device sign-in

A device without a keyboard (a TV) signs in by showing a code that a person
approves somewhere they are already signed in: the web page `/activate`, or a
Silo phone app. The device never handles a password. The state machine lives in
`internal/auth/device_login.go`; the operations are in
`internal/apiv2/device_login.go` and, frozen, in `internal/api/handlers/auth_device.go`.
The operation contract is in [auth-api.md](../auth-api.md#device-sign-in).

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
API key or an impersonation session is refused with 403 (v2
`permission_denied`, v1 `forbidden`) before the code is looked at. An
approval gives the device a login session of the account: from an API key
that would turn a credential no refresh re-checks into a refreshing session,
and from an impersonation session it would give the device a session with no
trace of the impersonator. Deny follows the same rule, so only the people who
can approve decide. A device approved from a session opened through an
external sign-in provider inherits that session's provider chain
([external-sign-in.md](external-sign-in.md#provider-re-check)).

Approval locks the account and checks that the submitting login session is
still active before updating the device request. Collection and credential
revocation also lock the account before the device request. A request that
authenticated before its session was revoked cannot approve afterwards.
Provider refusal withdraws approvals made through that identity, including
those from a break-glass account; its approvals made through local login stay
independent of the provider.

## Rate limits and guessing

Rate limits are per server and keyed on client IP. The lookup and all three
decisions (approve, approve-handoff, deny) take a user code, so on both v1
and v2 they spend one shared per-IP budget, `device_lookup` (60 per minute,
burst 20 by default), in place of the generic authenticated limiter. A
guesser therefore gets about 60 x 15 = 900 guesses over a code's 15-minute
life, at most about 1,800 over the 30-minute cap, against 10^8 codes; a hit
only lets them approve a stranger's TV into their own account. The budget is
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

## Clusters

All state lives in `device_login_requests` in Postgres. A request started on
one node can be looked up, approved, canceled and collected on any other;
`TestDeviceLoginAcrossNodesDB` covers it with two services on separate
connection pools.
