# Authentication API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. See [the native API contract](architecture/api-contract.md).

Commands assume the repository root is the cwd. Unprefixed paths are relative to the
server's `/api/v2` base URL.

The public auth operations that decode a JSON body before any credential is checked
(`login`, `setupServer`, `signup`, `refreshSession`, `startDeviceLogin`, `pollDeviceLogin`,
`completeOAuthLogin`, `acceptInvitation`, `createAppKeyChallenge`, `introduceAppKey` and
`signInWithAppKey`) follow the shared body rules: a body over the
1 MiB structured-body cap answers `413 payload_too_large`, and a non-blank body that is not
`application/json` (optionally with `charset=utf-8`), including one with no `Content-Type`,
answers `415 unsupported_media_type` before the operation runs. The app key routes set lower
caps: `createAppKeyChallenge` refuses a body over 1 KiB, and `introduceAppKey`,
`signInWithAppKey` and `enrollAppKeys` one over 16 KiB, with the same `413`.

For `setupServer` the media-type rule is what stops a cross-site browser form or `no-cors`
fetch from claiming the first administrator. A cross-origin JSON fetch requires a preflight,
which the main API does not grant. Same-origin browser and native JSON setup keep the
database-wide one-winner transaction. This does not add a setup secret or limit which
networks may complete setup.

## Login sessions

Login sessions belong to accounts and are separate from household profiles, registered
devices and playback sessions. All paths below use `/api/v2`.

| Operation | Authorization and behavior |
| --- | --- |
| `GET /auth/sessions/capabilities` (`getLoginSessionCapabilities`) | Authenticated account. Reports `available`, `last_seen`, `admin_management` and `revoke_other_sessions`, with the standard capability state, permission answer and revision. `revoke_other_sessions` is false in [demo mode](#demo-mode) for a caller that is not an administrator, and for a [session limited to itself](#sessions-limited-to-themselves) (opened by device sign-in, or by another app the person allowed), which may end only itself. |
| `GET /auth/sessions` (`listSessions`) | Lists the caller's live, unrevoked login sessions, newest first, with cursor pagination. Account-level access does not require profile selection. |
| `DELETE /auth/sessions/{id}` (`deleteSession`) | Revokes a session owned by the caller. Unknown or another account's session returns 404. Ending another session of the same account is 403 `permission_denied` for a [session limited to itself](#sessions-limited-to-themselves) (opened by device sign-in, or by another app the person allowed), which may end only itself, and in [demo mode](#demo-mode) for a caller that is not an administrator. |
| `GET /admin/users/{user_id}/login-sessions` (`listAdminUserLoginSessions`) | Acting admin. Lists a selected account's live login sessions. The cursor is bound to the operation, administrator and target account. |
| `DELETE /admin/users/{user_id}/login-sessions/{session_id}` (`deleteAdminUserLoginSession`) | Acting admin. Revokes one session of the selected account; 204 on success, 404 for an unknown or mismatched session. |
| `DELETE /admin/users/{user_id}/login-sessions` (`deleteAdminUserLoginSessions`) | Acting admin. Revokes all live sessions of the selected account and returns `{ "revoked": N }`. Repeating an account-wide request returns zero when no live sessions remain. |

Session rows expose `id`, `device_name` (the client-reported identifier or User-Agent),
`ip_address` at sign-in, `created_at`, `expires_at`, `last_seen_at`, `current`, and
`device_key_id` (the app device key that opened the session, `null` for any other
sign-in).
`last_seen_at` is an actual authenticated native API request timestamp, recorded at
most once a minute per session across API nodes. It is null until activity has been
recorded, including for sessions created before the migration. Creation and expiry
are never substituted for activity. Tracking does not extend or reactivate sessions.

Both list responses include `current_session`: the caller's live login session even
when it falls outside the requested page, or null for API-key callers and an admin
inspecting another account. Account-wide admin revocation also ends existing sessions
opened through "View as user" by that account and withdraws its approved, uncollected
device sign-ins, including temporary remote-playback approvals that would mint a native
login session. The same transaction revokes the account's Audiobookshelf-compatible
sessions and deletes its stored Jellyfin-compatible sessions, as a password reset does;
when any of it fails, nothing is revoked and the request fails. After commit, every node
drops the Jellyfin-compatible sessions it holds in memory. Each compatibility lookup
also reads its stored row, so a missed eviction event cannot authorize a deleted session.
Extending a session or storing its refreshed tokens only updates an existing row and
ends the session when the row is gone. The response counts revoked
live login sessions, not withdrawn approvals or
compatibility sessions. Single-session revocation leaves other sessions and device approvals unchanged, except that it withdraws the device sign-ins that session [approved in advance](architecture/device-login.md#approval-in-advance) and no device has redeemed. Logging a session out withdraws them too.
Other accounts' own sessions remain usable. API keys are not login sessions and are not
revoked here.
Creating a "View as user" session locks the originating and viewed accounts in the
same order as revocation and rechecks the originating session before insertion. A
request authenticated before revocation cannot create a fresh session after it commits.

Admin mutations recheck the actor's current enabled admin standing and the target's
owner protection in the same transaction as revocation. Only the owner can revoke
another admin's sessions; nobody can revoke the owner's sessions except the owner.
A "View as user" session belongs to the viewed account, so any admin who may manage
that account can revoke it, even when the owner opened it. Revoking it leaves the
viewer's own session intact. Any admin can manage its own sessions. Revocation leaves
passwords, other accounts, profiles and registered device settings unchanged. The next
authenticated native API request rejects the revoked session, including access-token
and refresh requests.
Already issued stream grants are not withdrawn by these operations.

The Web Settings screen and admin user Sign-in tab use these operations. Native
Apple and Android management screens are outside the 1.0 acceptance scope; their
existing native authentication requests use the same session validity checks.
A Jellyfin-compatible sign-in, by password or Quick Connect, opens a login session that
these lists show under the client's name (its User-Agent, or the device name a Quick Connect
request sent). The Jellyfin-compatible session lives
only as long as that login session: revoking it, from the account's own list or by an admin,
or its expiry ends that one Jellyfin client on every node at its next request, including
streams and the socket, and leaves the account's other Jellyfin clients signed in.
Account-wide revocation ends them all.

## Account passwords

A password belongs to a login account, not to an individual household profile. Every profile on an
account therefore shares the same password. Self-service password changes are restricted to the
active primary profile. An admin account may also change its password before selecting a profile,
but selecting a secondary profile removes that authority. API keys and impersonation sessions can
never change an account password.

Local passwords are bcrypt hashes. Setup, invited signup, password changes, password resets and
passwords an administrator sets share one password policy, which an administrator sets under
Settings → Sign-in:

| Setting | Default | Rule |
| --- | --- | --- |
| `auth.password_min_length` | `8` | Fewest Unicode characters, from 8 to 72. |
| `auth.password_require_uppercase` | `false` | At least one capital letter (Unicode category `Lu`). |
| `auth.password_require_special` | `false` | At least one punctuation mark or symbol (Unicode categories `P` and `S`); letters, digits and spaces do not count. |

Every new password is also at most 72 UTF-8 bytes, the bcrypt input limit. The policy is read on
every check, so a change applies at once on every node. It applies only when a password is chosen;
passwords already stored keep working. The recovery command
`silo auth local-login enable -user <name> -temporary-password` sets a temporary password without
it, and the account then chooses a new one under the policy. A backup
restore never weakens the live policy.

A refused password returns `422 validation_failed` with one field error at the password member
(`body.password`, or `body.new_password` for a password change). Its `code` names a rule the
password breaks, and its `detail` is the English sentence for it, such as "Use at least 12
characters." When a password breaks several rules, the code names one of them, so a client should
not rely on which. A translated client shows `error.field.<code>` instead. That text is generic, so
`password_too_short` doesn't name the minimum; a client that wants to show it reads
[`getPasswordPolicy`](#get-authpassword-policy-getpasswordpolicy) again. Every operation that sets a password
except `changePassword` limits it to 8 to 72 characters in its schema, so a password outside that
range gets `out_of_range` there before the policy runs. `changePassword` accepts 1 to 1024
characters, so the policy codes cover every length in that range.

| Code | Rule broken |
| --- | --- |
| `password_too_short` | Fewer characters than `auth.password_min_length`. |
| `password_too_long` | More than 72 UTF-8 bytes. |
| `password_needs_uppercase` | No capital letter while `auth.password_require_uppercase` is on. |
| `password_needs_special` | No punctuation mark or symbol while `auth.password_require_special` is on. |

To change a password, the caller must prove knowledge of the current
password. By default, changing the password does not revoke existing login sessions; users can
review and revoke those separately through the account sessions API. The change can also
sign the account out everywhere else in the same step; see
[`POST /account/password`](#post-accountpassword-changepassword).

Accounts whose local password login is disabled keep their credential at the external provider and
cannot use this flow. An administrator can also make a password temporary or issue a reset link;
see [temporary passwords](#temporary-passwords) and [password reset links](#password-reset-links).

### `GET /auth/password-policy` (`getPasswordPolicy`)

Public, so the setup, signup and password-reset forms can check a password before sending it. The
server checks it again on every write.

```json
{
  "minimum_length": 8,
  "maximum_bytes": 72,
  "require_uppercase": false,
  "require_special": false
}
```

### `GET /account/password/capability` (`getAccountPasswordCapability`)

Requires an authenticated login session. `X-Profile-Id` is optional; a profile it
names gets its normal viewer and PIN verification, even when the account is an
administrator. Clients read this document rather than infer support from a server
version. It supports ETag revalidation and uses `Cache-Control: private, no-cache`.

```json
{
  "revision": "opaque-capability-revision",
  "state": "available",
  "allowed": true,
  "requires_current_password": true,
  "minimum_password_length": 8,
  "maximum_password_bytes": 72,
  "sign_out_other_sessions": true
}
```

`allowed` is true only when the caller may change the password: an access-token
session, not an API key or "View as user", acting as the account's primary profile,
or as an administrator with no profile selected. A session holding a temporary
password is exempt from the profile rule, since it cannot select a profile.
[Demo mode](#demo-mode) comes first: there `allowed` is false for every caller that is not an
administrator, a temporary-password session included. The policy fields describe the server contract even when
`allowed` is false. A server without a password service reports `state`
`not_configured`, with `allowed` and `sign_out_other_sessions` false.
`sign_out_other_sessions` says whether the change accepts that member; a server that
omits it rejects the member as unknown with `422`, so clients send it only when the
capability says so.

### `POST /account/password` (`changePassword`)

```json
{
  "current_password": "existing password",
  "new_password": "replacement password",
  "sign_out_other_sessions": false
}
```

Success returns `204 No Content`. A capability read does not authorize the write: the
server checks the current account and profile again. This credential operation does
not require `If-Match`. Repeating a request whose response was lost is safe; the
current password it names is then the new one, so the retry returns `422` at
`body.current_password` and changes nothing.

The optional `sign_out_other_sessions` boolean (default `false`) also signs the
account out everywhere except the calling session, in the transaction that replaces
the password: its other login sessions, "View as user" sessions it opened or that
view it, its Audiobookshelf-compatible sessions, its stored Jellyfin-compatible
sessions, and the device sign-ins it approved that the device has not collected yet.
This is what a password reset revokes, minus the caller's session. API keys survive.
After commit, every node drops the Jellyfin-compatible sessions it holds in memory,
as for account-wide admin revocation. `null` is not a value for it.

The operation spends the dedicated `password_change` rate-limit budget
(`ratelimit.auth.password_change.*`, 10 requests a minute with a burst of 5 per
client IP by default) rather than the generic authenticated limiter, so a caller
holding a session but not the password cannot brute-force it.

| Status | Problem type | Meaning |
| --- | --- | --- |
| `401` | `authentication_required` | No valid login session. |
| `403` | `permission_denied` | The active profile is not the primary profile, an administrator selected a secondary profile, or the caller is an API key or "View as user" session. |
| `409` | `conflict` | The account does not use local password login. |
| `422` | `validation_failed` | A member is missing, empty, `null` or unknown; the current password did not match (`body.current_password`); or the new password breaks the [password policy](#account-passwords) or is the same as a temporary password (`body.new_password`). |
| `429` | `rate_limited` | The `password_change` budget is spent; `Retry-After` says when to try again. |
| `503` | `dependency_unavailable` | The server has no password service, or password hashing is saturated; `Retry-After` says when to try again. |

The Jellyfin-compatibility listener does not expose password mutation. Jellyfin-compatible clients
continue to authenticate with the account's current local password, while password management stays
on Silo's native API.

### Temporary passwords

An administrator can make a password temporary when setting it (see
[administrator accounts](admin-users-api.md#passwords)). The account's `Account`
document then reports `password_change_required: true`: in the login, setup,
signup, device-pairing and invitation token pairs, and in `GET /account/me`.

Until the account chooses a new password, every session it opens may call only:

- `GET /account/me`
- `GET /account/password/capability`
- `POST /account/password`
- `POST /auth/logout`

Anything else returns `403 password_change_required`. Refreshing is public and stays available. Such a
session may change the password without selecting a profile. The current
(temporary) password is still required, and reusing it as the new password returns
`422 validation_failed` at `body.new_password`.

After `POST /account/password` succeeds, refresh the tokens: the access token keeps
the restriction until it is replaced, and the refreshed pair no longer carries it.
The change revokes every other session of the account, since each was opened with
the temporary password.
In [demo mode](#demo-mode) an account that is not an administrator cannot change its
password, a temporary one included, so such an account can only read `GET /account/me` and
sign out until an administrator sets it a regular password or turns demo mode off. Don't give
a demo's shared account a temporary password.
An administrator impersonating the account is not restricted. Jellyfin and
Audiobookshelf compatible sign-ins refuse an account holding a temporary password,
because those clients cannot run the change; the account signs in to Silo first.

### Password reset links

An administrator can issue a single-use link that lets the account holder choose a
new password (see [administrator accounts](admin-users-api.md#passwords)). The link
is `/reset-password#{token}`: the token is the URL fragment, which a browser never
sends to the server or in a `Referer` header, so reverse proxies in front of Silo do
not log it. The web client takes the token out of the address before it renders and
uses two public operations, which carry it in the request body:

- `POST /password-resets/lookup` (`lookupPasswordResetLink`) with `{ "token": "..." }`
  returns `username`, `server_name`, and `expires_at`.
- `POST /password-resets/complete` (`completePasswordResetLink`) with
  `{ "token": "...", "password": "..." }` sets the new password and returns 200.

The earlier path forms, `GET /password-resets/{token}` (`lookupPasswordReset`) and
`POST /password-resets/{token}/complete` (`completePasswordReset`), still answer the
same way but are deprecated, because a token in the path reaches proxy access logs.
They send the `Deprecation` and `Link` headers; no `Sunset` is declared. A link issued in the old `/reset-password/{token}` form still opens.

Every unusable link returns the same `404 not_found`, whether it is unknown, expired,
used, replaced, or outdated by a password or recovery email change, or its account
is disabled. Changing only the email's letter case preserves the link.
Invalid passwords return `422 validation_failed` at `body.password`, with a
[code that names the rule](#account-passwords), and leave the link
unspent. While password resets are not configured, every link operation returns `409`. Both operations spend the `password_reset` rate-limit budget, 20 requests per
minute per client IP by default.

Completing the reset spends the link, sets the password, clears a pending temporary
password, and revokes every login session of the account, including administrator
sessions impersonating it, in one transaction. The response then signs the caller
in, using the same shape as invitation acceptance: `status: "completed"`, `username`, and
`login_status`. With `signed_in`, `tokens` carries the new token pair. With
`sign_in_required`, the password is set but no session was opened; sign in normally.
Never replay a completion, since the link is already spent.

Links are stored only as SHA-256 digests, and the request log redacts the `{token}`
path segment.

### Sign-in links

A sign-in link is for a person who lost every device. An administrator makes one
for an account, and its page hands the Silo app an app code that enrolls new keys,
or lets the person set a password. The link is `<server address>/sign-in-link#<token>`,
single use for 24 hours, and the token travels in the fragment and the request body
only, as a reset link's does. The architecture is in
[app keys](architecture/app-keys.md#sign-in-links).

- `POST /admin/users/{user_id}/sign-in-links` (`createAdminUserSignInLink`), acting
  admin, body `{ "send_email": false }`. The link is built on the server's public
  URL, as a reset link is; without one the answer is 409
  `capability_not_configured`. An address changed since it was read is 409
  `conflict`. Answers 201
  `{ "url", "expires_at", "delivery_status" }`; `delivery_status` is
  `not_requested`, or with `send_email` `sent` or `failed_or_unknown` (the link is
  live either way and is still returned). A new link replaces the account's earlier
  links. A scoped API key can never make one (403 from the scope check: no scope
  names the operation). Otherwise who may make one follows the rule for setting
  that account's password as an administrator: any administrator for an ordinary
  account, only the Owner for another administrator or the Owner, and only the
  Owner for an account of their own whose password sign-in is off (403). A disabled account is 409 `conflict`. With `send_email`, an account with
  no email address is 409 `conflict` and a server without mail 409
  `capability_not_configured`, both before a link is made. A link
  is made only for an account allowed to sign in with a local password on this
  server, by password sign-in's server-and-account-state rule (the account view's
  `local_password_allowed`): a break-glass account always, any other while the
  switch is on and no network identity provider refuses it. It leaves out the
  account's own password sign-in flag, which the link's set-password turns on.
  Otherwise 409 `provider_sign_in_required` (the switch off) or 403
  `not_permitted` (a network provider's refusal); redeem and set-password check
  it again and leave the link or token unspent.
  Audited as `user.sign_in_link_created` with `delivery` and `expires_at`, never
  the token. A new link, a password set by any route, a change of its email address,
  signing the account out everywhere (or out of every other session), disabling it
  and promoting it each end its link, the code the link's redeem left and its set-password token. The account capability (`getAdminAccountCapabilities`) reports
  `sign_in_links` and `sign_in_link_email`.
- `POST /auth/sign-in-links/redeem` (`redeemSignInLink`), public, body
  `{ "token" }`. Answers `{ "code", "expires_in", "server_address", "link_allowed",
  "set_password_token" }`, every member optional. `code`, `expires_in`,
  `server_address` and `link_allowed` (always `false`) come together while
  `auth.app_key_sign_in` is on, the server is not in demo mode, and the account is
  not an administrator's when the link is redeemed. Otherwise the answer has only
  `set_password_token` and `no_code_reason`: `app_keys_off`, or `administrator`
  (a link gives an administrator the password route only and enrolls no key; the
  person sets a password, signs in on the site and uses `createAppCode` there).
  The role is the one at redeem: an administrator demoted since the link was made
  gets a code. `set_password_token` comes with every successful redeem, which
  happens only for an account that may sign in with a local password on this
  server (a break-glass account always, so also while the switch is off); it lives
  an hour, never past the link, and works once. The link is spent in the
  transaction that redeems it, which makes the app code when there is one. An
  unknown, used or expired link, and one whose account is disabled, is 404
  `not_found`; for an account that may not sign in with a local password (not
  break-glass, and the switch off or a network sign-in provider refusing it), 409
  `provider_sign_in_required` or 403 `not_permitted`, and the link stays unspent. Audited as `user.sign_in_link_redeemed`.
- `POST /auth/sign-in-links/set-password` (`setSignInLinkPassword`), public, body
  `{ "token": "<set_password_token>", "new_password" }`. Answers 204. The password
  passes the rules a reset's completion checks (422 at `body.new_password`, token
  unspent). It sets the password, turns the account's local password sign-in on,
  settles a temporary password and revokes every login session of the account as a
  reset does; app keys stay. It signs nobody in. 404 `not_found` for an unknown,
  used or expired token; 409 `provider_sign_in_required` or 403 `not_permitted`
  for an account that may not sign in with a local password on this server (not
  break-glass, and the switch off or a network sign-in provider refusing it), and
  the token stays unspent. Audited as `user.sign_in_link_password_set`.

Both public operations spend the `password_reset` rate-limit budget.

### Self-service password reset

When an administrator turns on `password_reset.self_service_enabled` (off by
default), an account holder can request a reset link from the sign-in page. Clients
read `GET /capabilities/password-reset`, a public server-wide capability document,
and offer the request only when `state` is `available`. The state is `disabled`
while the setting is off, and `not_configured` while the server lacks a configured
mail server or `server.public_url`.

`POST /password-resets` with `{ "login": "..." }` names the account by username or
email address, the same way sign-in does. Every accepted request returns `202` with
no body, whether or not an account matched. The server looks the account up and
sends the email in the background, so neither the answer nor its timing reveals
whether the account exists. The request fails only when the capability is off
(`409 capability_disabled`), not configured (`409 capability_not_configured`), or
rate limited (`429`, the `password_reset_request` budget: 5 requests per minute per
client IP by default).

The server emails a link only to an enabled account that signs in with a local
password and has a valid email address. The link opens the same
`/reset-password#{token}` screen as an administrator's link and expires after an
hour. An account gets at most one requested link every five minutes. A requested
link replaces the account's earlier requested link, but never a live link an
administrator issued; while one exists, requests send nothing.

## The account's creation time

Every answer that carries a token pair for one of the person's own sessions
also carries `account_created_at` beside `user`: password and network
sign-in, `completeOAuthLogin`, device sign-in polling, `refreshSession`,
signup and setup, an accepted invitation, a completed password reset, an
administrator's impersonation, and every sealed `signed_in` answer to one of
the person's own device keys. It is the account's `users.created_at`: RFC 3339
in UTC at the stored microsecond precision, with trailing zeros of the
fraction left out (so it is not the millisecond instant other fields use).
Nothing rewrites it after the account is made, and a full restore brings it
back exactly. Compare it as an instant, never as text. It is absent from every
answer to another app's key, one enrolled through a grant: its redeem, its
sign-ins and its refreshes. `getCurrentUser` and the administrator's views do
not carry it.

## Email addresses

Every write that stores an account address (`setupServer`, `signup`,
administrator account create and update, and emailed invitations) runs the
same check (`internal/auth.ValidateEmail`): one bare mailbox, no display name
or comments, and a domain containing a dot with text on both sides. A bare
hostname such as `admin@siloserver` is refused with a `422 validation_failed`
problem at `body.email`. The web client applies the same check before sending.

## Listing login sessions

`GET /api/v2/auth/sessions` lists the authenticated account's live login sessions, including
sessions on its other devices. Authentication is required; no active profile is needed.
Expired and revoked sessions are excluded before pagination.

The response uses the collection envelope, `items` and `page`, plus `current_session`. Each
item contains `id`, `device_name`, `ip_address`, `created_at`, `expires_at`, `last_seen_at`,
and `current`, plus `device_id` and `device_platform` when the client reported them.
Timestamps use UTC with millisecond
precision. There is no `revoked_at` member because every returned session is active.

A session records the device that opened it. Every sign-in that opens a login session (password
login, initial setup, signup, invitation acceptance, password-reset completion, OAuth completion,
network sign-in, and device-code collection) reads the request's
`X-Silo-Device-Id`, `X-Silo-Device-Name`, and `X-Silo-Device-Platform` headers, clamped like the
settings device headers: name to 120 characters, platform to 40. Surrounding whitespace
is trimmed from the id. A repeated `X-Silo-Device-Id` header, or an id made of anything
but letters, digits, `.`, `_`, `:` and `-`, is refused with `422 validation_failed`
before the sign-in runs, as on every operation. Each of these operations declares the
three headers, so an id longer than 128 characters is refused with `422
validation_failed` too (an `out_of_range` error at `header.X-Silo-Device-Id`, as on
the settings operations). Name and platform are declared without a limit and clamped,
so a long name never refuses a sign-in. `device_name` is the
reported name, else the `User-Agent`; a device-code sign-in falls back to the name the device
started the request with, then its `User-Agent`, then `This device`. A name or platform that is
not valid UTF-8 is dropped. These values are client-reported: they serve display and audit, and
nothing authorizes on them.

- `limit` defaults to 50 and accepts 1 through 200.
- Results are ordered by `created_at` descending, then `id` descending.
- Pass `page.next_cursor` unchanged as `cursor` to retrieve the next page. The cursor retains
  the full stored timestamp precision and is bound to the account and operation.
- `page.has_more` reports whether another page exists. The last page omits `next_cursor`.
- `offset` and out-of-range limits return `422 validation_failed`; an invalid or mismatched
  cursor returns `400 invalid_cursor`.

`DELETE /api/v2/auth/sessions/{id}` revokes a session owned by the caller's account and returns
`204 No Content`. A missing session or one owned by another account returns `404 not_found`.
In [demo mode](#demo-mode) a caller that is not an administrator may revoke only the session
making the request; another session of the same account is `403 permission_denied`. A missing
session or another account's is still `404 not_found`: ownership is checked first, in the
revocation's transaction.

The login-session step of the `database_maintenance` scheduled task deletes expired
login-session rows daily at 05:00 by default. Revoked sessions remain stored until their
expiry passes. Jellyfin-compatible clients check their login session on
every request too; cleanup removes only sessions that have already expired.

### Sessions limited to themselves

Two kinds of login session see and end only themselves among the account's sessions:

- A session opened by device sign-in. It may sit on a device the account holder doesn't
  control, such as a shared TV.
- A session opened by another app the person allowed: its device key was enrolled through a
  grant (`auth_device_keys.enrolled_via` is `grant`). That app is not one of the person's own,
  and its access ends when its key is revoked. A session one of the person's own device keys
  opened (`opened_via` `app_key`, key not enrolled through a grant) is ordinary.

The device key decides first: a session whose `device_key_id` names a grant-enrolled key is
limited, whatever else it records. Revoking such a key ends its sessions, and deleting a key
row deletes the sessions that name it, so a session naming a key that is not stored should not
exist; one that did would be limited.

For a session no grant-enrolled key opened, `auth_sessions.opened_via` records how
each session was opened and is the one record the server reads: `device_login` when a device
sign-in request (a code a signed-in person approved, Jellyfin Quick Connect, or a
remote-playback handoff) opened it, otherwise `password`, `provider`, `app_key` or
`impersonation`. `auth.LimitedToItself` decides; the two routes below read it, no other
request does.

Such a session is limited to itself when its `opened_via` is `device_login`, whatever its
platform. Two kinds of session have no `opened_via`:

- One a server of the previous release wrote. That release recorded only
  `auth_sessions.via_device_sign_in`, `true` for a device sign-in and `false` for any other,
  and the session is limited when the flag is `true`. The flag answers this one question. A
  session with `false` and no `opened_via` is ordinary for the session list and is still a
  session with no record of how it was opened: `false` does not mean a password sign-in, so
  it may do nothing that needs a recorded `opened_via` (see
  [app keys](architecture/app-keys.md#who-decides)).
- One older than both columns. It is limited when its recorded `device_platform` is a TV's:
  `tvOS`, `android-tv` or `roku`, in any case. That covers TVs signed in by code before
  either column; an older TV session opened with a password is limited too, which costs
  nothing because no TV app reads more of the list than `id` and `current` or ends another
  session.

Every other session is ordinary, a TV that signs in with a password and an app that signs in
with the person's own key included, as are a
session an administrator opened to act as the account and an API-key caller.

`via_device_sign_in` is still stored, because a server of the previous release names it in
every sign-in and must keep working against this database during a rolling deploy or after
going back a release. The one session insert writes it from `opened_via`, and a check
constraint (`auth_sessions_opened_via_matches_flag`) refuses a row in which the two disagree.
A session such a server inserts carries the flag and no `opened_via`, and is judged as the
first kind above. The column goes when no supported server writes it. The limit covers only the
account's own session list: a device signed in by code on an administrator's account still
holds that account's administrator powers, including ending any account's sessions, so a code
on a shared device should not be approved from an administrator's account.

For a limited caller, `listSessions` still answers `200` with every live session of the
account, because released Apple apps read the session ids to recognize one server saved under
two addresses. Its own item and `current_session` are complete. Every other item carries only
`id` and `current`: `device_name` and `ip_address` are empty strings, `device_id` and
`device_platform` are absent, `last_seen_at` is null, and `created_at` and `expires_at` repeat
the caller's own values, since they have no null form. The page is ordered by `id` and the
cursor holds only the last id, so neither tells which sessions are newer. A cursor minted for
one kind of caller is `400 invalid_cursor` for the other. `limit` and `page.has_more` behave as
for any caller.

`deleteSession` from a limited caller ends its own session as for any caller. Another session
of the account is `403 permission_denied`, with a detail saying a sign-in made with a device
code or by another app can end only its own session. A session of another account, or an unknown id, is `404
not_found` exactly as for an ordinary caller: the account check comes first, so the 403 says
nothing about other accounts' sessions. `POST /auth/logout` is unchanged.

What such a session still learns is how many live sessions the account has and their opaque
ids. A device sign-in session opened before the column from a device that isn't a TV has no
recorded mark and stays ordinary until it ends; an account holder ends shared devices'
sessions from their session list, or an administrator ends all of the account's sessions.

### Other devices' playback and web push

One rule decides which sessions are limited: the one above. Each surface applies it as its
shape allows. The login session list redacts the other entries. The surfaces below show or
act on one device's entries from another, and they apply only the half of the rule about
another app the person allowed (the session kind the authentication lookup puts on the
claims, the grant-enrolled key of `SessionOrigin`). A device signed in by code is not limited
on them. Every operation also states what such a session may do with it, and a refused one is
answered by the gate before its handler; see
[app keys](architecture/app-keys.md#what-a-granted-app-can-do). The gate also removes the
device such a session declares, so it records no device registry entry and no connection
record. What each surface gives such a session:

- `listHouseholdSessions` (`GET /profiles/household/sessions`) gives such a session only the
  playback sessions its own login session started, in full. Every other entry is left out,
  including a stream with no recorded login session (one started through the
  Jellyfin-compatible or Audiobookshelf-compatible surface, or before the record). The item
  shape is unchanged.
- The web push routes (`listNotificationWebPushSubscriptions`, `subscribeNotificationWebPush`,
  `unsubscribeNotificationWebPush`, `deleteNotificationWebPushSubscription`) are refused to it
  by the gate with 403 `permission_denied`, before anything is read or written: a profile's subscriptions
  belong to every browser it registered, and subscribing takes an endpoint's registration
  over.
- Reporting progress (`updatePlaybackProgress`), stopping (`stopPlayback`), replanning
  (`replanPlayback`), reporting a route event (`reportPlaybackRouteEvent`), minting a control
  ticket (`createPlaybackControlSocketTicket`) and fetching the stream's media (the original
  file, the HLS manifest and segments, subtitle sidecars and fonts) check the account and
  profile, and for such a session also that its own login session started the playback
  session: another login session's stream, or one with no recorded login
  session, is 403 `permission_denied`. Progress, stop and replan read the attempt anyway, and
  ask the rule only when the stream's login session is not the caller's, so a client acting
  on its own stream adds no query there. Minting a control ticket adds one read of the
  attempt for every caller. Replan refuses such a session as unavailable when that read
  fails; for every other caller a failed read falls through to the route's own answer. A
  replan for an attempt that does not exist answers 404 `session_not_found` and starts no
  transport. The media routes read the attempt only for such a session.
- The native push writes (`registerPushDevice`, `unregisterPushDevice`,
  `registerApplePushDevice`, `resetApplePushInstallation`) are refused to it by the gate with
  403 `permission_denied`, before anything is read or written. A registration with no ordered
  record yet (a legacy Android one, or an Apple installation after a reset) is bound only to
  the account and device id, and a reset needs no installation key, so the session could
  remove or take over another device's registration. Its own app could receive nothing through
  them: the Apple topic and the Android relay are the first-party apps'. The session-less
  retirements stay as they are; they need the installation key.
- Watch Together: a room's host is the account and profile, so such a session acting as the
  host's profile would steer the room of the person's own device. The gate refuses it
  creating a room and every host operation (`createWatchTogetherRoom`, `closeWatchTogetherRoom`,
  `startWatchTogetherRoomPlayback`, `stopWatchTogetherRoomPlayback`,
  `updateWatchTogetherRoomPolicy`, `selectWatchTogetherRoomItem`,
  `updateWatchTogetherRoomSelectionMode`, `stageWatchTogetherRoomItem`,
  `promoteWatchTogetherSuggestion`), and `fallbackWatchTogetherSource`, which any member may
  send and which switches what every member plays. `createWatchTogetherSocketTicket` refuses
  it for a room its account and profile host. As a member of another host's room it may
  connect: it reports its own state, suggests and votes, but the socket refuses its
  `transport_request` frames with the error frame any refused request gets, whatever the
  room's guest policy. Like any member, a `buffering` frame from it can still pause the room
  for everyone while it catches up, within the room's spacing and cooldown. A member is its
  account and profile, so connecting replaces a connection already held for them; such a
  session is instead refused (an error frame with code `forbidden` and the message "This
  profile's place in the room is still held by another connection; try again shortly", then
  the socket closes) while that member's seat is held, on this API server or another, as the
  room's stored runtime records. The room does not record which login session holds a seat,
  and a seat stays held for up to 30 seconds after a socket that dropped without closing and
  up to 45 seconds after its API server died, so the holder may be the app's own earlier
  connection; a client retries after a short wait. The person's own device connecting later still replaces the app. Deleting
  a suggestion (`deleteWatchTogetherSuggestion`) is limited to one its account and profile
  made, without the host's power to delete any; the suggester is recorded only as an account
  and profile, so that includes the person's own devices' suggestions on that profile.

The refusals carry the same detail as the key routes' refusal of a session that may not make
a change.

Two answers change for such a session beyond those surfaces. `getCurrentUser` answers
`email` as the empty string, the shape's "none set". `getDeviceLogin`, a public lookup that
attaches a session when one is sent, refuses it with 403 `permission_denied`: approving and
denying a device sign-in are refused to it, and with its session attached it would look up
the account's Quick Connect codes.

### Access tokens after a role change

An access token carries the account's role, and admin checks trust it. When an
administrator changes the account's role, the login session stays valid, but every
request that presents an access token minted before the change is refused with
`401 token_refresh_required`.

On `token_refresh_required` a client refreshes the session with its refresh token and
retries the request once with the new access token, which carries the new role. It
must not sign out: only a refused refresh ends the session (`401 session_expired`). If
the client caches the account's role, it reads `GET /account/me` again so role-gated
controls appear or disappear. Long-lived Apple notification display tokens are not
refused; their requests run with the account's current role.

### When the server cannot check a session

Requests that pass the shared auth gate, media requests included, look up their login
session (or API key) in the database. When that lookup fails, because the database is
unreachable or the query errors, the server has not judged the credential and does not
answer 401. It answers `503 dependency_unavailable` with `Retry-After`. `POST /auth/refresh` (`refreshSession`) answers the same way when it
cannot read the session or account. Whether the session is still valid is unknown, so a
client keeps its tokens, waits `Retry-After` seconds and retries; it signs out only on a
401. Viewer access answers the same `503`, with `Retry-After: 1`, when the policy that
resolves the viewer's access runs out of its evaluation time (`policy.eval_timeout_ms`):
the request is refused, but no decision was made, so the client retries. Any other failure
to resolve viewer access is still `500`. The proxy's header-authenticated `/stream/v3` routes answer `503 service_unavailable`
too. The Jellyfin surface keeps its session and answers `503` when its session check or
its stream and HLS authorization cannot read the session from the database, when a due
token refresh cannot reach the database, and when that refresh meets an unreachable
sign-in provider under the `fail_closed` outage policy. Its `/socket` answers a new
connection `503` the same way, and keeps an open socket open when a periodic re-check
cannot run.

Routes that check credentials outside the shared auth gate, refresh, the proxy media grant
and the Jellyfin session, stream and socket checks may still treat a failed lookup as no session and
answer 401 or refuse the request. Examples are plugin content routes, theme-song grants,
Jellyfin admin API keys, Jellyfin image routes (which fall back to anonymous access), and
the socket-ticket check used by the events, playback-control,
watch-together and admin-logs sockets (a watch-together re-check that fails closes the
room). None of them is a token refresh, so a client that signs out only on a refresh 401
stays signed in and retries.

## Sign-in rate limits

Operations that name a rate-limit budget (`login`, `setup`, `signup`, `invitation`,
`password_reset`, the `device_*` budgets and the others above) spend one counter per
budget wherever they run: the native API, and for `login` and the device budgets the
Jellyfin-compatible listener too. A budget counts per client address. That is the address
taken from `X-Forwarded-For` or `X-Real-IP` when the connection comes from a trusted proxy
(`clientip.trusted_proxies`; by default the RFC 1918 IPv4 ranges, `127.0.0.0/8` and
`::1`), otherwise the
connection's own address. A public IPv6 address counts by its /64, because one home line or
server usually holds a whole /64; this applies to every per-address budget, including the
per-address request budget of authenticated routes. Every other address counts on its own:
IPv4, unique local and link-local IPv6 (which only come from the local network, like
private IPv4 addresses), and the NAT64 addresses (`64:ff9b::/96` and the local-use `64:ff9b:1::/48`) that stand
for IPv4 clients.

When the client IP came from those headers, the request also spends a budget keyed on the
connection's own address: ten times the per-client one, shared by every client behind that
proxy. Naming a new client in the headers on each request therefore stops at that budget.
Direct connections and requests through a network access provider, whose plugin writes the
header itself, spend only the per-client budget. An exhausted budget answers 429 with
`Retry-After`.

### When the server is too busy to check a password

Every bcrypt hash and comparison, for account passwords and profile PINs alike, runs
under one limit per server process (`internal/auth/passwordhash`). At most half the
process's processors hash at once (`GOMAXPROCS`, which follows a container CPU limit;
at least one), and up to four requests per slot wait for a turn, for at most two
seconds. A request beyond that is refused before its password or PIN is checked, so on
a server with more than one processor a burst of sign-ins cannot take every core from
playback and the rest of the API.

The refusal is `503` with `Retry-After: 1`: native `dependency_unavailable`, Jellyfin
`ServiceUnavailable`, and a plain `503` on the
Audiobookshelf listener. It never answers as a wrong password, and every sign-in name
gets the same answer whether or not it has an account. Password sign-in, password
changes and resets, setup, signup, invitation acceptance, account creation and edits by
an administrator, credential linking, and setting a profile PIN all answer this way;
clients retry after the delay. A signup or invitation acceptance whose account was
already created when its sign-in was refused answers as before (`500`, or
`sign_in_required` from invitation acceptance), since repeating it cannot succeed. A
first OAuth sign-in that has to create the account fails with `login_failed`, and the
person starts it again.

A refused sign-in still spends an attempt of the name's budget, like any other failed
one ([Sign-in attempts per name](#sign-in-attempts-per-name)).

Profile PIN checks wait in a queue of their own that takes every other turn, so a burst
of sign-ins does not keep a signed-in household from switching profiles; see
[Profile PINs](#profile-pins). A Jellyfin or Audiobookshelf sign-in with `password#PIN`
still checks the password first and can be refused with it.

## Sign-in attempts per name

Every password sign-in also spends a budget kept for the name it signs in with, however
many addresses the attempts come from: native `login`, Jellyfin
`POST /Users/AuthenticateByName`, Audiobookshelf `/login` (beta), and
`linkAccountIdentityWithCredentials`, which spends the budget of the directory username
it checks. Names are trimmed and compared without regard to case, as sign-in looks up
accounts, so ` Alice` and `alice` share one budget. The database folds the case with the
`lower()` that `citext` compares by, so budgets follow its collation: with the C collation
`Älice` and `älice` are two accounts and two budgets, and two accounts never share one. If
that query fails, the attempt still counts, under a name folded by the server, but its
success clears and extends nothing; a username and the same account's
email address are two names. A Jellyfin name with a `#`, such as `alice#Kids`, spends the
budget of the part before the `#`, even when it signs in to an account whose own name
contains the `#`, so the budget does not depend on which accounts exist. Such an account's
native sign-ins spend the budget of its whole name, so it has two budgets. A name with no account has a budget like any other and is refused the same way, so
the budget does not reveal which names exist.

The limit is `ratelimit.SignInNamePolicy`: ten attempts per name. The first attempt
starts a 15-minute window, and the tenth locks the name for 15 minutes from that attempt.
While a name is locked, every attempt from an address the account is not known to sign
in from is refused before any password is checked, the right password included. A
successful sign-in while the name is not locked clears the count, and so does the right
temporary password on a Jellyfin or Audiobookshelf sign-in, which is refused only because
those apps cannot change it. A success clears and extends only a budget whose name belongs
to the account that signed in: one of its own names, or a name no account answers to,
such as its directory username. A directory identity of a separate account with the
same login name, or an `alice#Kids` account signing in through Jellyfin with the `alice`
budget, leaves that budget's counts alone and makes its address known only for its own
names, so it cannot clear guesses at the account the name finds.

An address becomes known for an account's username and email address when a password
sign-in to the account succeeds from it (including signup, invitation acceptance and a
completed password reset) or one of the account's login sessions refreshes from it (a
Jellyfin app's session refreshes its login session as the app keeps using it), and
stays known for 30 days after the last of those (`ratelimit.SignInTrustTTL`). A name
keeps the 32 addresses it used most recently (`ratelimit.SignInTrustMaxAddresses`), so
refreshes that each name a new address cannot grow the store without bound. While the
name is locked, each known address spends a budget of its own instead,
`ratelimit.SignInSourcePolicy` (ten attempts, then 15 minutes locked), which a success
from it clears. Addresses count as the per-address request budgets count them: an IPv4
address on its own and a public IPv6 client by its /64. A request through a network
access provider counts from the overlay peer the provider named, apart from the same
address on another provider installation or outside the overlay. A request with no
address that says where the person is never becomes known and never spends a known
address's budget: one a forwarder sent for a client the server could not identify (its
address is the forwarder's), one from loopback (a proxy, plugin or relay on the server's
host), and an overlay request without a peer. So someone who keeps a name
locked from elsewhere does not lock its people out where they usually sign in, and
guessing stays bounded: ten attempts per window from every unknown address together,
plus ten from each known one. Impersonation and remote-playback handoff sessions do not make an address known. Nor does an account's email when it
is another account's username, which only accounts from before the single login identity
space can have: that name signs in to the account with the username, whose name it stays. Every other failure counts,
including one caused by an unavailable directory or database, or by a server too busy to
check the password ([`503`](#when-the-server-is-too-busy-to-check-a-password)). A count that never reaches
ten expires 15 minutes after its first attempt. A Jellyfin or Audiobookshelf sign-in whose
password ends in a `#PIN` part counts twice, whether or not the name has a local account,
because it may check the password both with and without that part.
Attempts are counted before the password is checked, so concurrent guesses cannot overrun
the limit.

A locked name answers 429 with `Retry-After` giving the seconds left:

- Native: `rate_limited` problem, on `login` and `linkAccountIdentityWithCredentials`.
- Jellyfin: `{"Error":"TooManyRequests",...}`, as for its other 429s.
- Audiobookshelf: a plain-text 429.

The counts and known addresses live in Redis whenever the server has Redis configured,
so every node and every listener share them, independent of `ratelimit.backend` and of
whether request rate limiting is enabled. Only a server without Redis keeps them in
process memory, and forgets known addresses when it restarts, until the account's
devices sign in or refresh again. A Redis error on a count fails open and is logged; a
failed lookup of a known address counts it as unknown. A request whose own context was
canceled is refused.

Anyone who knows a name can keep it locked for unknown addresses by spending the budget
every 15 minutes. While that lasts, the account's people still sign in from known
addresses, existing sessions keep working and refreshing, completing a password reset or
an invitation signs in without spending the budget, and a signed-in device can approve a
new one through device sign-in.

## Authenticated account allowance

Authenticated routes also spend one `account:<user_id>` budget, shared across the
account's household profiles, login sessions, devices and API keys. Switching
credentials or forwarding headers does not create a new account allowance. The native
API and the Jellyfin-compatible listener share the counter. Distinct accounts
behind one proxy have separate account allowances; existing source and global
budgets still apply.

The account budget is four times the per-address budget set by
`ratelimit.ip.requests_per_second`, `ratelimit.ip.requests_per_minute` and
`ratelimit.ip.burst`: with the defaults (120 per second, 6000 per minute, burst 120)
an account gets 480 requests per second, 24000 per minute and burst 480. One login
often spans several networks, and each address is already held to one per-address
budget, so one busy address or integration can spend at most a quarter of its
account. The admin UI labels these settings **Per address and account**. IP, API-key and account checks run before the global
check, so a request rejected by a narrower budget does not consume global capacity.
All applicable budgets must allow the request. Successful API-key response headers
describe the key's tier budget; an account denial reports the exhausted account
budget using the existing 429 response and `Retry-After`.

Requests without an authenticated account do not spend an account allowance.
Pre-authentication display-token lookup and public sign-in/device operations retain
their source and global checks. With the memory backend, counters are per process;
Redis shares them across instances. When Redis is unavailable each process counts
its checks in its own memory, in the same second and minute windows Redis uses, so
the limits keep holding per process instead of switching off. A check that fails, or
gets no answer within a second, is counted locally, and that process then stops
waiting on Redis: its checks count locally at once, and every five seconds one
request is also counted in Redis in the background to find out whether Redis answers
again. The failure that stops the waiting and each failed background count are
logged. Checks that start before the first failure is noticed, up to a second after
Redis stops answering, each wait up to a second. A check that fails only because its
own request ended early does not count as Redis failing. Disabling rate limiting
disables the account allowance too.

## Session quota

The per-second and per-minute budgets stop bursts, not volume: a client that stays
just under them can still send hundreds of thousands of requests a day. Each login
session therefore also spends a token bucket, keyed `session:<session_id>`, that holds
`ratelimit.session.burst` requests (default 30000) and refills at
`ratelimit.session.requests_per_hour` (default 10000). The burst must refill from
empty within a week, so it can be at most 168 times the hourly rate. The admin UI
labels these **Per sign-in session**.

`ratelimit.session.mode` decides what happens when a bucket is empty:

- `off` counts nothing.
- `log` (the default) serves the request and logs `sign-in session over its hourly
  request quota` with the account, session and user agent, once per session every ten
  minutes.
- `enforce` refuses the request with the usual 429 and a `Retry-After` for the next
  token, and logs the same line.

The quota counts native API requests made with a login session's access token. It
does not count anything under `/api/v2/stream` or `/api/v2/playback`, so a player
keeps reporting progress, replanning and stopping after its session runs out; the
other media transfer routes (download files and their subtitles, direct downloads and
theme audio); `GET /api/v2/auth/provider-logout` and `POST /api/v2/auth/logout`, so a
session that spent its quota can still sign out; API-key requests, which have their own tiers;
admins; or requests made while impersonating. `ratelimit.QuotaExemptPathPattern`
defines the exempt routes and must match every route in `internal/api`'s native media
route list. The Jellyfin-compatible listener has its own sessions and is not counted.
Buckets share the per-key limiter's backend. Redis keeps one bucket per session for
every instance, timed by the Redis clock, and shares the window limiter's outage
pause: while Redis is unavailable each process counts in its own bucket. Settings
changes keep every bucket's balance; a bucket reads its size and rate on each
request, and is dropped once even the slowest refill the settings allow would have
filled it: a week after a drained bucket's last request, seconds after one a client
barely used.

The burst is sized so a first-party client can sync a whole library in one sitting
without running dry. The refill rate sits well below what an integration polling
around the clock sends, so sustained polling is what the quota catches.

`GET /api/v2/admin/stats/api-clients` lists the sessions with the most requests in a
window, leaving out what the quota leaves out, with each session's 429 count. The
hidden `summarize_api_usage` task summarizes the activity log into UTC hours every 15
minutes and keeps them for 30 days. `GET /api/v2/admin/stats/api-clients/{session_id}/endpoints`
lists the routes one session called in up to the last 24 hours. Both are admin-only;
`api_clients` in
`GET /api/v2/admin/dashboard/capabilities` advertises them.

## Household profiles

Every profile object the server returns carries `sync_id`: on
`GET /api/v2/profiles`, on the profile that `createProfile`, `updateProfile`
and the avatar operations return, and on the admin account-profile routes
under `/api/v2/admin/users/{id}/profiles`. It is the profile's shared ID: a
lower-case UUID that is the same for this profile on every server its person
uses, or `null` when the profile has none. An app that signs in to several
servers lines up one person's profiles across them by it. The ID is unique
within an account, not across the server.

`PUT /api/v2/profiles/{id}/avatar` (`uploadProfileAvatar`) takes the image in
the multipart part `avatar` and an optional text part `source_sha256`: a
SHA-256 that names the picture, as 64 lower-case hexadecimal characters.
Without the part, or with it empty, the server stores the SHA-256 of the
bytes it received. Any other value is 422 `validation_failed` at
`body.source_sha256`. The uploader chooses the value. To name one picture
alike on several servers, send the SHA-256 of the file as it was first
uploaded, not of a re-encoded copy. The hash is a label the server keeps
with the picture:
it never fetches, compares or deduplicates files by it, and choosing a preset
or deleting the avatar clears it. `GET /api/v2/profiles/set` reports it as the
profile's `picture`, `sha256:<hex>`. Only the PostgreSQL user store keeps the
label; the SQLite user store accepts `source_sha256` and drops it.

### The profile set

A profile set is the account's household as one versioned document that the
person's app keeps and sends to each server the person uses. The design (who
may send it, the version rule, what it changes and what it leaves alone) is in
[app-keys.md](architecture/app-keys.md#the-profile-set). Both operations need
the PostgreSQL user store: elsewhere they answer 501
`capability_unsupported`, and `getAppKeyCapability` reports `profile_set`
`false`.

| Operation | Caller | Notes |
| --- | --- | --- |
| `PUT /api/v2/profiles/set` (`putProfileSet`) | a signed `profile-set` envelope, or the account's own login session acting as its main profile | Makes the household match the set. 200 with the answer below. |
| `GET /api/v2/profiles/set` (`getProfileSet`) | the account's own login session | The set as this server holds it. Strong `ETag`; `If-None-Match` answers 304. |

**Request.** The body is one of two forms; anything else, both or neither, is
422 `validation_failed` at `body`.

- Signed: `{ "kid", "payload", "sig" }`, a `profile-set` envelope signed by the
  account's person key, on a challenge from `createAppKeyChallenge`. The
  payload carries `purpose`, `server_id`, `iat`, `exp`, `challenge`,
  `account_id` (the account on this server, as a string), `set`, and
  optionally `pairs` and `edits_seen`. It has no `origin` or `reply_key`; the
  answer is not sealed. A login session sent with it is not consulted.
- Session: `{ "set", "pairs"?, "edits_seen"? }` over the account's own login
  session, with `X-Profile-Id` naming the household's main profile and
  `X-Profile-Token` when that profile has a PIN.

`set` is `{ "version", "items" }`. `version` is a whole number from 0 to
2^53 - 1 (`7.0` is 7). `items` holds 1 to 64 items, the main profile first:

| Member | Type | Meaning |
| --- | --- | --- |
| `sync_id` | string | The profile's ID on every server: a lower-case UUID, unique in the set. |
| `name` | string | 1 to 64 characters after trimming. Two items with one name are refused with `name_conflict` (see below). |
| `picture` | string | `preset:<id>` (`avatar-1` to `avatar-8`, or `dicebear:<style>:<seed>`), `sha256:<64 lower-case hex>` naming an upload, or `""`. |
| `pin` | object | Exactly one of `{"set": "1234"}` (four digits), `{"keep": true}`, `{"clear": true}`. |
| `is_child`, `require_advisory_age`, `requests_blocked` | boolean | |
| `max_content_rating` | string | Up to 32 characters; `""` for none. |
| `max_advisory_age` | integer or null | 1 to 21; `null` for none. |
| `settings` | object | Settings-contract keys stored per profile, each to its value; `{}` for none. |
| `settings_reset` | array | Optional. Up to 256 keys whose profile value is removed. A key in both `settings` and `settings_reset` is 422. |

Every member in the table is required in every item, except
`settings_reset`: an item that leaves one out is 422 at that member, with code
`required`. A set is the whole truth about a profile, and a missing limit must
never be read as "no limit". `settings` is required too, and `{}` is how an
app says it has no setting to apply: a key in `settings` is set, a key in
`settings_reset` is reset, and a key in neither is left as the server has it.
An absent `settings` would mean the same as `{}`, and is refused so that an
app that forgot the member can't pass for one that chose to send nothing. This
stays so. A member added to the item later is optional and has a
stated default, so a set from an older app is still accepted by a newer
server.

Item members the server does not know are ignored. `pairs` is
`[{ "sync_id", "local_id" }]`, for the first use on an account that already has
profiles: each names an item and the existing profile it becomes. `edits_seen`
is the `edits_token` the app read from `getProfileSet` when it built the set,
at most 64 characters; absent means `""`.

A malformed member is 422 `validation_failed` at the member
(`body.set.items[2].name`, `body.pairs[0].local_id`, `body.edits_seen`). Both
forms are held to the same rules and answer the same `errors[]` for the same
set: the same `location`, `code` and `detail`. In the signed form the set
travels inside `payload`, and its members are still reported at the locations
the session form uses (`body.set…`), so an app has one mapping from a location
to a field. There `body.set` is not a path into the signed request, which has
no `set` member; it names the same member of the same set. Only a fault in the
envelope itself (its signature, challenge, `account_id` or times, or a payload
with no set at all) is reported as the other signed routes report it. A signed
set is validated after its signature verifies, so a 422 for it has already
spent the challenge: correct the set, get a new challenge, and sign again.

The server decides whether two names are the same: it trims them and compares
them without regard to case, so `Sam` and `sam` are one name here. An app does
not reproduce that comparison; it goes by the code and the location. Every
name clash is 422 `validation_failed` with code `name_conflict` at an item's
name, and is judged after the version (see Refusals), so a stale set is told it
is stale first:

- Two items of the set with one name: the entry is at the later item, with the
  detail "Two items of the set have this name; rename one." Every item counts,
  one left off for the limit or removed by the owner included, and so does an
  item whose profile keeps a name the owner gave it here.
- A new item named like a profile outside the set.
- An item whose name the owner already gave another of the set's profiles on
  this server (a profile keeps a name the owner set, whatever its item says);
  the item named is the one whose name comes from the set, the later one when
  both do.

An item 0 whose `sync_id` the owner removed on this server (see
`hidden_by_owner`) is 422 at `body.set.items[0].sync_id`.

**Answer.**

```json
{
  "version": 7,
  "profiles": [{ "sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e", "id": "12" }],
  "left_off": [],
  "awaiting_libraries": [],
  "awaiting_pictures": [],
  "awaiting_pins": [],
  "hidden_by_owner": ["6c9a2f2e-4f3b-4c55-9d43-1f0e2b7a8c11"],
  "overrides": [{ "sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e", "field": "max_content_rating", "value": "PG" }],
  "skipped_settings": [{ "sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e", "key": "playback.subtitle_mode" }]
}
```

- `profiles`: this server's ID for each profile it holds from the set, in set
  order.
- `left_off`: items not applied because of the household's profile limit, in
  set order.
- `awaiting_libraries`: child profiles with library limits on and no
  libraries, waiting for someone to pick them on this server.
- `awaiting_pictures`: items whose picture this server doesn't have, a
  `sha256:` picture not uploaded here or a preset it doesn't know. The profile
  keeps its old picture. The app uploads the picture with
  `uploadProfileAvatar` and `source_sha256`.
- `awaiting_pins`: profiles created from `keep` that still have no PIN.
- `skipped_settings`: known settings whose value this server refused; it kept
  its own. Unknown keys and `server_local` keys are not listed.
- `hidden_by_owner`: items the owner removed on this server. The set did not
  create or pair them.
- `overrides`: values the owner set on this server for the set's profiles,
  which the set left as they are (see [Edits made on this server](#edits-made-on-this-server)).
- `jellyfin_bare_name_sign_in`: whether a Jellyfin-compatible app that signs
  in with the bare account name (no `#profile`) gets a profile, as the
  account's profiles stand after the set. It is `true` when exactly one
  profile is named like the account (compared without case), or else exactly
  one profile has no PIN; otherwise the person must sign in as
  `account#Profile`. Sessions already open are not affected either way. The
  member is absent while the Jellyfin-compatible surface is off. It answers
  for the account's username. A person who types their email address instead
  is matched by the second rule only (exactly one profile with no PIN),
  because no profile is named like an email address.

The answer describes the account as it stands after the call, so a repeat of
the same set answers the same lists.

**Refusals.** They come in one order. A malformed set is 422 first: its
shape needs nothing from the server. Then a set that is older, other content
at the held version, or newer without the person's latest edits (`edits_seen`)
is `profile_set_stale`. Then the household must map the set (`pairs`, the
main profile). The held version again is then compared and answered as a
no-op; it is never refused for something only a write would do. Last, a set
that will write is refused for what it would do, in this order: two items of
the set with one name (`name_conflict`), item 0 taking a `sync_id` the owner
removed, then a name another profile here would share (`name_conflict`).

| Problem | When |
| --- | --- |
| 409 `profile_set_stale` | The version is older than the one the server holds; the same version with other content; the same version whose `set` PIN differs from the stored PIN, or whose `clear` finds a PIN (a PIN the owner set on this server is not compared); or a newer version while the server holds the person's own edits and `edits_seen` is not its current `edits_token`. Nothing is written. |
| 409 `capability_disabled` | The signed form while `auth.app_key_sign_in` is off. The session form works with app keys off. |
| 409 `conflict` | The household kept changing while the set was applied; or the account has profiles but no main profile, which a set can't apply to. Send the set again a few times, waiting longer each time, then stop and report it: the second case doesn't clear by itself. |
| 401 `signature_invalid`, `challenge_invalid` | The signed form, as on the other signed routes. A payload naming another account is `signature_invalid`. A replayed envelope is `challenge_invalid`. |
| 401 `authentication_required` | The session form without a login session. An API key carries no login session, so it gets this too. |
| 403 `password_change_required` | The session form from a login session that must change its temporary password first, as every other session route answers it. The session is not gone: change the password, refresh the session, and send the set again. |
| 401 `session_expired` | The account was deleted while the set was applied. |
| 403 `permission_denied` | The signed form for an administrator's account; a session opened by device sign-in, by another app's key, or by an administrator acting as the account, a remote-playback handoff, a session older than the record of how sessions open; either form from a non-administrator in demo mode. |
| 403 `main_profile_required` | The session form from a request that isn't acting as the main profile, or names no profile. |
| 403 `profile_verification_required` | The main profile has a PIN and the request did not prove it. |
| 429 `rate_limited` | The account's `profile_set` budget, 20 a minute with a burst of 10 by default (`ratelimit.auth.profile_set.*`): the session's account, or the account a signed set names once its signature has verified. Every device of a household shares an address, so the address is not what is counted; it has a budget ten times that, spent by every request before anything about it is trusted, which only abuse reaches. Both answer with `Retry-After`. A signed set that meets the account's limit has already spent its challenge: wait, get a new challenge, and sign a new envelope. The account's budget is spent by any login session of the account that sends a set, whichever profile it acts as, before the main profile is checked. Also, on a repeat of the held version, a profile whose PIN is locked after too many wrong PINs, with `Retry-After`, as `verifyProfilePIN` answers it. |

A repeat of the held version with a `set` PIN tells whether that PIN is the
stored one (200 or `profile_set_stale`), so each such comparison counts as a
PIN attempt on that profile, exactly as on `verifyProfilePIN`: a wrong PIN
counts, a right one clears the count, and the fifth wrong one locks the
profile for five minutes, during which the repeat answers 429 without
comparing.

After `profile_set_stale`, read `GET /profiles/set` before sending anything:
it says which version the server holds and its `edits_token`. Sending the held
version again without that read spends PIN attempts and can lock a profile.

A set that changes the main profile's PIN ends every `X-Profile-Token` minted
for the old PIN, the caller's included: the next household call needs
`verifyProfilePIN` with the new PIN.

**`GET /api/v2/profiles/set`.** The same sessions as the session form may read
it, with or without a profile header; it needs no PIN.

```json
{
  "version": 7,
  "items": [{
    "sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e", "id": "12", "position": 0,
    "name": "Sam", "picture": "preset:avatar-3", "has_pin": true, "is_child": false,
    "max_content_rating": "", "max_advisory_age": null, "require_advisory_age": false,
    "requests_blocked": false, "settings": { "playback.audio_language": "en" },
    "changed_here": [
      { "field": "name", "at": "2026-11-25T03:50:00.000Z", "by": "account" },
      { "field": "settings.playback.subtitle_language", "at": "2026-11-25T03:52:00.000Z", "by": "owner" }
    ]
  }],
  "deleted_here": [{ "sync_id": "6c9a2f2e-4f3b-4c55-9d43-1f0e2b7a8c11", "at": "2026-11-25T03:51:00.000Z", "by": "owner" }],
  "overrides": [{ "sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e", "field": "settings.playback.subtitle_language" }],
  "hidden_by_owner": ["6c9a2f2e-4f3b-4c55-9d43-1f0e2b7a8c11"],
  "local_only": [{ "id": "15", "name": "Guest", "has_pin": false }],
  "jellyfin_bare_name_sign_in": true,
  "edits_token": "e1.mFzDq0dX7eTsc3m6SfN0Lw1o2s0kHqgV"
}
```

`items` are the profiles holding a `sync_id`, in set order. `settings` is the
profile's own stored values for the keys a set carries; a key not set on the
profile is absent. `local_only` lists the profiles outside the set, in
creation order, with `has_pin`, so an app can work out
`jellyfin_bare_name_sign_in` for a set before it sends it.
`jellyfin_bare_name_sign_in` is as in the `PUT` answer, for the profiles as
they stand now. `changed_here`, `deleted_here`, `overrides`,
`hidden_by_owner` and `edits_token` are described below. An account that never
took a set answers `version` 0, no items, and every profile in `local_only`.

### Edits made on this server

A profile from the set can still be changed on each server through the
ordinary routes: the profile editor, settings, the avatar routes, the admin
Users page, a Jellyfin-compatible app's user configuration. The server records
each such change to a profile that has a `sync_id`, per field, with who made it
and when:

- `by: "account"` (a mark): the account's own sessions, its API keys and its
  Jellyfin-compatible sessions. The app reads these and decides whether to
  spread them to the person's other servers, asking first for a PIN, a limit
  or a deletion.
- `by: "owner"` (an override): an administrator on an admin route, or an
  administrator signed in as the account. A set leaves the field as it is and
  reports it in `overrides`. An override belongs to the profile, not to its
  `sync_id`: when a set gives the main profile a new `sync_id`, its overrides
  and marks move with it.

The later edit of a field wins, whoever makes it: an administrator's edit of a
field the person changed makes it an override, and the person's edit of an
overridden field on this server ends the override and makes it a mark again.
An override is not a lock.

What counts as an edit:

- A profile field (`name`, `picture`, `pin`, `is_child`, `max_content_rating`,
  `max_advisory_age`, `require_advisory_age`, `requests_blocked`) when the
  save changes its value. A save that sends every field marks only the fields
  that changed. `picture` changes with a new upload even when the avatar
  reference stays the same; `pin` changes whenever a PIN is set, the same
  digits included.
- A setting, `settings.<key>`, when a write or reset changes the profile's
  effective value: the value the settings routes answer for that profile
  (its own value, else what it inherits, an administrator's default
  included). Writing the value the profile already has, inherited or stored,
  and resetting a value equal to the one it would inherit, are not edits;
  choosing the contract default over a different administrator default is. Two keys
  the settings contract keeps as spellings of one preference
  (`playback.intro_skip_mode` and `playback.auto_skip_intro`) are recorded
  together: a change of either moves both to whoever made it.
  `server_local` keys and settings stored per device, library or series are
  not in the set and are never recorded. A Jellyfin-compatible app's
  configuration save writes only the values that differ from the effective
  ones it shows the app, resolved the same way, so echoing a page back
  records nothing.
- A profile's deletion, in `deleted_here`. An owner's deletion also hides the
  profile: a set does not create or pair it again on this server, and reports
  it in `hidden_by_owner`.

Nothing is recorded for the set's own writes, a backup restore, a profile
import, or a change to the main profile's name when the account's username
changes: a main profile from a set keeps its name then.

After a recorded edit commits, the server sends each of the account's
registered phones a background push, at most one a minute per account, so the
app can read `GET /profiles/set` without waiting to be opened. The push shows
nothing and does not name the profile that changed; see
[Profile set wake](notifications-push-api.md#profile-set-wake). A server with
push delivery off sends none, and the app learns of the edit when it next
reads the set.

In `GET /profiles/set`:

- `items[].changed_here`: the profile's recorded fields, `{ field, at, by }`,
  by field. Marks are listed until a set settles them, overrides until the
  person edits the field here.
- `field`, here and in `overrides`, is an open string: an item field name or
  `settings.<key>`. New item fields and new settings keys will appear; an app
  ignores, or carries through unchanged, a `field` it doesn't know.
- `deleted_here`: profiles of the set deleted here, `{ sync_id, at, by }`.
- `overrides`: `{ sync_id, field, value }` for each owner's field. `value` is
  what the profile holds now, encoded as a set item encodes it: `pin` is
  `"set"` or `"clear"`, never a PIN; `max_advisory_age` is a number or
  `null`; a setting the owner reset has no `value` member, because the
  profile has no value of its own (`null` is a value). A `picture` naming an
  upload this server kept no hash for has no `value` member in the `PUT`
  answer, which never reads an upload.
- `hidden_by_owner`: the `sync_id`s the owner deleted here.
- `deleted_here` and `hidden_by_owner` list every removal on record, including
  `sync_id`s the set has since dropped. The `PUT` answer's `hidden_by_owner`
  lists only that set's items. An app that doesn't find one of these
  `sync_id`s in its own set ignores it; the server is not out of step.
- `edits_token`: names the marks (`by: "account"` entries of `changed_here`
  and `deleted_here`); `""` when there are none. It changes when a mark is
  added (an edit that puts a field back as it was included), settled, or
  replaced by an owner's edit of the same field. Any other owner's edit is
  not a mark and leaves it as it is.

While `edits_token` is not empty, a newer set must carry it as `edits_seen`,
or it is `profile_set_stale` with nothing written: read the set, merge the
marks, and send again. An accepted newer set settles exactly the marks its
`edits_seen` named and they leave `changed_here` and `deleted_here`; a mark
recorded after the read stays. A repeat of the held version settles nothing.
An override stays until the person edits that field on this server, the
owner clears it, or the profile is deleted here. Two settings the settings contract keeps as one
preference under two keys are recorded together, so the person's edit of
either ends the override on both. An override belongs to its profile: a set that drops
the profile removes what the owner recorded for it, and an item under a new
`sync_id` is a new profile with none of the old one's overrides. A profile an
administrator deleted stays hidden on this server until an administrator
clears that.

An administrator sees these records and clears them from the admin routes in
[admin-users-api.md](admin-users-api.md#profiles-from-the-persons-app).
Clearing an override (a guarded `PATCH` that must name, in `If-Match`, the
`etag` the administrator was shown) puts the field back to the value of the
last accepted set at once, with three exceptions. A PIN the set sent as digits,
or kept (`{"keep": true}`), stays as the owner set it until a newer set. A
picture this server doesn't hold waits for its upload. A setting the last set
neither names nor resets has no value from the set: the clear writes nothing,
and the profile keeps the owner's value until a set names or resets the key.
The field leaves `overrides` and the next newer set applies to it. Clearing a
hidden profile takes it out of `hidden_by_owner` and creates nothing. A newer
set creates the profile only if it has an item with that `sync_id` and the
household has room for another profile; otherwise the item is in `left_off`,
or not in the set, and nothing comes back. A set that has the item can
still be refused as a whole (for example, its name is now another profile's on
the account), and then nothing is written. If it pairs the `sync_id` with a
profile on the account with no `sync_id`, that profile takes the `sync_id` and
keeps its history instead of a new profile being created, and needs no room in
the household, since the profile limit applies only to new profiles. After either
commits, the server wakes the
person's apps with a push where it can (at most one a minute per account, and
only with push on, a phone registered and the wake queued in time); otherwise
the app sees the change
the next time it syncs. A field that leaves `overrides` without the person's
own edit was cleared: send a newer set so the server holds the app's value.

## Profile PINs

Profile creation stores a requested PIN hash in the same
transaction as the profile and its preferences. The first committed profile
state already requires PIN proof. Creation rejects PINs longer than 72 bytes
before writing anything (422 `validation_failed`).
Omitting the PIN still creates an unlocked profile.

`POST /api/v2/profiles/{id}/verify-pin` (`verifyProfilePIN`) checks a household
profile's PIN. A match
issues the `X-Profile-Token` that unlocks the profile for the caller's login session; a
wrong PIN answers 200 with `valid: false`.

Each verification attempt affects the lockout counter. Clients must not
automatically replay a check after a timeout; the contract declares `verifyProfilePIN` as
`non_retryable` for this reason.

Wrong PINs are limited per profile, not per client address, because anyone guessing
already shares the account's sign-in. The limit is
`ratelimit.ProfilePINPolicy`: five attempts per profile. The first attempt starts a
five-minute window. The fifth attempt, if it is wrong, locks the profile for five
minutes from that attempt. While a profile is locked every check is refused, the
right PIN included, so the lockout cannot be bypassed by guessing on. A correct PIN
while not locked clears the count, and so does setting or removing the profile's PIN.
A count that never reaches five expires five minutes after its first attempt.
Attempts are counted before the PIN is compared, so concurrent guesses cannot overrun
the limit. The check then waits for a hashing turn in its own queue, ahead of
sign-ins ([when the server is too busy](#when-the-server-is-too-busy-to-check-a-password)).
When that queue is full or no turn comes within two seconds it answers that `503`, and
the attempt counts like any other; clients still must not replay a check automatically.

A locked check answers 429 `rate_limited` with `Retry-After` giving the seconds left.

A login session another app's key opened (an app enrolled through a grant; see
[app keys](architecture/app-keys.md)) has an allowance of its own. Its attempts count
per profile and app device key under `ratelimit.GrantedAppPINPolicy`: three attempts,
then one hour locked. The count follows the key, not the login session, so signing in
again with the same key does not reset it. Such a session never spends the household's
count, so its wrong guesses cannot lock the profile for the person's own devices, and
its correct PIN does not clear the household's count. It does read that count: while
the household's own attempts have locked the profile, it is refused with the same 429
and the household's `Retry-After`. A correct PIN while neither count is locked clears
the app's count. Setting or removing the profile's PIN clears only the household's
count; the app's lock runs out on its own.

Jellyfin sign-ins that use the `password#PIN` convention count against the same
per-profile budget. A locked profile fails there the way a wrong PIN does (401
`InvalidUsernameOrPassword`), and only the message names the lockout. PIN guesses
are counted only after the account password matched.

Audiobookshelf-compatible sign-ins (beta) use the same conventions:
`username#profile` names the profile and `password#PIN` carries its PIN. A bare
username names the profile called like the account when there is one, and otherwise
signs in to the primary profile only when that profile has no PIN. PIN
guesses count against the same per-profile budget, again only after the account
password matched, and a locked profile answers 401 like a wrong PIN, with a message
naming the lockout. Audiobookshelf access and refresh tokens carry the profile's PIN
revision. Once the profile has a PIN at another revision, because one was set or
changed, or once the profile is deleted, its tokens answer 401 and refresh is refused,
so the app signs in again. A token that names no profile works only while the account
has no profiles. The Socket.io connection checks the token the same way when it
authenticates.

The count lives in Redis whenever the server has Redis configured, so every node
shares it, independent of `ratelimit.backend` and of whether request rate limiting
is enabled. Only a server without Redis keeps it in process memory, which is correct
for a single node. A Redis error fails open and is logged;
a request whose own context was canceled is refused instead, so dropping the
connection does not buy an uncounted guess. Clients show the lockout from
`Retry-After`, for example "Too many incorrect PINs. Try again in 5 minutes.",
instead of "Incorrect PIN".

## Device sign-in

A TV opens a request, shows its code and QR link, and polls; a person approves
it where they are signed in. Rules for codes, links and lifetimes are in
[device-login.md](architecture/device-login.md).

| Operation | Credential | Notes |
| --- | --- | --- |
| `POST /auth/device/start` (`startDeviceLogin`) | none | `user_code` is eight digits (`4821-7730`); `verification_uri_complete` is `<base>/activate?code=48217730`, where `<base>` is `server.public_url` when configured; `expires_in` is 900. Optional `binding` (43 characters of unpadded base64url; anything else, including an empty string, is 422 at `body.binding`) starts a bound request; see [Session binding](architecture/device-login.md#session-binding). |
| `POST /auth/device/poll` (`pollDeviceLogin`) | device code; for a bound request also `binding_secret` | `opened: true` once an approver has looked a pending request up. A pending answer carries the current `expires_at`, which lookups can move later; the device moves its local deadline there. `approved` carries the token pair exactly once. A bound request polled without its `binding_secret`, or with another, is 403 `device_binding_mismatch` and is left unchanged; `binding_secret` is ignored for an unbound request and is at most 64 characters. A request whose approving account was disabled or deleted answers `denied`. A request approved in advance also answers 429 `rate_limited` with `Retry-After` to a wrong secret after 10 wrong secrets for its device code within 15 minutes (the right secret is never refused by that budget), and 503 `provider_unavailable` while the sign-in provider it was approved through is disabled. |
| `POST /auth/device/cancel` (`cancelDeviceLogin`) | device code | Withdraws a live pending, or approved but uncollected, request and answers `canceled`; any other request answers its unchanged state (`denied`, `consumed`, `canceled`) or `expired`. Repeating converges. A request approved in advance is left unchanged and answers 404. |
| `GET /auth/device?code=` or `?token=` (`getDeviceLogin`) | none; a login session's bearer token when sent | Adds `requested_at`, `server_name`, `binding` for a bound request and, when known, `server_id`. A lookup of a pending request marks it opened and holds its code for at least five more minutes, never past 30 minutes after start. A six-digit Jellyfin Quick Connect code is looked up only with the account's own login session: without a valid token it answers 401 and from an impersonation session 403, for every six-digit code. |
| `POST /auth/device/approve`, `/approve-handoff`, `/deny` | the account's own login session | A canceled request is refused with 409, and so is approving a request another account (for `approve-handoff`, another profile) already approved, or denying one another account approved (`conflict`). An API key, an impersonation session or a remote-playback handoff session is refused with 403 `permission_denied`, because an approval gives the device a login session. `approve-handoff` approves a remote-playback or Jellyfin Quick Connect request for the verified profile in `X-Profile-Id` (with `X-Profile-Token` for a PIN profile); `approve` refuses both with 409. |
| `POST /auth/device/preapprove` (`preapproveDeviceLogin`) | the account's own login session acting as the household's main profile (see "The household's main profile" under app keys) | Body `device_name` (1 to 128 characters), optional `device_platform` (at most 64), required `binding` (43 characters of unpadded base64url). Answers 201 with `id`, `device_code` and `expires_at`, 30 days on: an approved, single-use sign-in that only a poll with that `device_code` and the secret behind `binding` redeems. An API key, an impersonation session or a remote-playback handoff session is 403 `permission_denied`; a session whose provider identity is gone is 403 `not_permitted`; an account already holding 20 waiting is 409 `conflict`. Spends the `device_start` bucket. Audited as `user.device_sign_in_preapproved` with the request's `device_login_request_id`, `device_name` and `expires_at`, never its device code or binding. |
| `GET /auth/device/preapprovals` (`listDevicePreapprovals`) | the account's own login session, opened by a password, a provider or the person's own device key | 403 `permission_denied` for a session opened by device sign-in or by another app's key, one older than the record of how sessions open, an API key, an administrator acting as the account and a remote-playback session. The account's waiting sign-ins approved in advance, newest first: `items` of `id`, `device_name`, `device_platform`, `created_at`, `expires_at`. Never the device code or the binding. Not paged; the cap bounds it. |
| `DELETE /auth/device/preapprovals/{id}` (`revokeDevicePreapproval`) | the account's own login session, opened by a password, a provider or the person's own device key | Refused like the list. 204; the request moves to `denied` and the device's next poll answers `denied`. Revoking one already withdrawn is 204; a redeemed, expired, unknown or other account's id is 404. |
| `GET /admin/users/{user_id}/device-preapprovals`, `DELETE .../{id}` (`listAdminUserDevicePreapprovals`, `revokeAdminUserDevicePreapproval`) | administrator | The same list and revoke for any account; revoking follows the owner protection of revoking login sessions (403 `permission_denied`). |
| `GET /auth/device/capability` | none | `cancel` and `opened_signal` report the two additions; `jellyfin_quick_connect` reports Quick Connect approval; `session_binding` reports session binding; `preapprove` reports approval in advance ([device-login.md](architecture/device-login.md#approval-in-advance)). |

`status` gains `canceled` on lookup and poll. Start, poll and cancel spend the
device rate-limit buckets; the lookup and all three decisions take a user code
and share the `device_lookup` bucket. A lookup or decision with a six-digit
Quick Connect code also spends the account's Quick Connect code budget, 20
attempts per 15 minutes, and answers 429 `rate_limited` with `Retry-After` once
it is spent; see
[device-login.md](architecture/device-login.md#jellyfin-quick-connect).

### Remote-playback sessions

Collecting an approved `remote_playback` request answers as before: `tokens`
with an access and a refresh token, `profile_id`, `profile_token`,
`temporary: true` and `session_expires_at`. The session it opens is bound to
that profile and ends at `session_expires_at`, 24 hours after collection at
most. [device-login.md](architecture/device-login.md#remote-playback-handoff)
has the full rules; what a client sees:

- `POST /auth/refresh` keeps issuing both tokens until the session ends, but
  never moves the session past `session_expires_at`. The access and refresh
  tokens expire no later than that, and `expires_in` counts down to it when it
  is sooner than the usual access lifetime. After it, refresh answers 401.
- A request without `X-Profile-Id` acts as the bound profile. A request naming
  another profile answers 403 `permission_denied`. `GET /profiles` lists only
  the bound profile.
- The session has the account role `user` on every account, though `GET
  /account/me` still reports the account's own role. It reaches only
  the routes a TV needs to play and browse as that profile (listed in
  `internal/api/middleware/remote_playback.go`). Every other route, admin
  and account or household management included, answers 403
  `permission_denied`.
- It cannot approve or deny a device request, Quick Connect included.

## App key sign-in

App keys let an app sign in to an account with a key it holds instead of a
password. The rules (who decides, the origin rule, revocation, multi-node
behaviour) are in [app-keys.md](architecture/app-keys.md); this section is the
operation contract.

| Operation | Credential | Notes |
| --- | --- | --- |
| `GET /auth/keys/capability` (`getAppKeyCapability`) | none | `state` is `available` while `auth.app_key_sign_in` is on, `disabled` while it is off, and `not_configured` on a server started without a database for app keys. `protocol` is `silo-sync-v1` and `server_id` is the identity every signed payload names. `introduce`, `sign_in`, `enroll`, `app_codes` and `grants` are `true`; `profile_set` is `true` where profiles are kept in PostgreSQL ([The profile set](#the-profile-set)); `sign_in_links` is `true` while sign-in links are served. `signature_suites` is `["ES256"]` and `sealed_reply_suites` is `["P256-HKDF-SHA256-AES256GCM"]`. Conditional like every capability document (`ETag`, `If-None-Match`). |
| `POST /auth/keys/challenge` (`createAppKeyChallenge`) | none | Body `{}`. Answers `challenge` (base64url of 32 bytes), `expires_in` (120), `server_id` and `server_time` (Unix seconds). A challenge works for one signed request within two minutes, on any API process. |
| `POST /auth/keys/introduce` (`introduceAppKey`) | an `introduce` envelope signed by the account's person key, with `device_sig` | Enrolls the payload's `device_key` and opens a session. For an administrator's account the key is stored waiting and the sealed answer is `approval_required`. An account holds at most 10 waiting keys inside their 24 hours: one more is 409 `conflict`, after the challenge is spent. A waiting key past its 24 hours doesn't count, and a retention task later revokes it. Audited as `user.device_key_introduced` with the key's `device_key_id` and `device_key_status` (`active` or `pending`). |
| `POST /auth/keys/sign-in` (`signInWithAppKey`) | a `sign-in` envelope signed by an enrolled device key, without `device_sig` | Opens a session. A waiting key answers `approval_required`; see below. |
| `POST /auth/keys/enroll` (`enrollAppKeys`) | the account's own login session acting as the household's main profile (see below), and `{ "person_key", "message" }` where `message` is an `enroll` envelope signed with that person key, with `device_sig` | Registers the person key when the account has none, taking the session's provider binding; when `person_key` is already the account's person key it is reused as it is, binding unchanged. Enrolls the device key, which takes the session's provider binding, then opens a session bound to it. A different person key on the account, or a device key already enrolled on this server, is 409 `conflict`. The sealed plaintext carries `person_key_id`, as every `signed_in` answer to the person's own device does ([Sealed reply](#app-key-sign-in)); the client drops the session it called with. A malformed payload field is 422 at `body.message.payload`. Audited as `user.app_keys_enrolled` with the device key's `device_key_id`, the person key's `person_key_kid` when this call enrolled it, and `session_opened_via`, how the enrolling session was opened (`password`, `provider` or `app_key`). |
| `POST /auth/grants/redeem` (`redeemAppKeyGrant`) | a `grant-redeem` envelope signed by the app's own key (the grant's `app_key`), without `device_sig` | Enrolls another app's key with a grant the account's person key signed, and opens the app's session. The key is listed with `kind` `app`, `enrolled_by` `grant` and `allowed_by`. Refused for an administrator's account. See "Grants" below. Audited as `user.app_grant_redeemed` with the app key's `device_key_id`. |
| `POST /invitations/app-code` (`createInvitationAppCode`) | none; the invitation `token` in the body | Body `{ "token", "username", "email" }`. Starts a join through the Silo app and answers `{ "code", "expires_in", "server_address" }`; see "App codes" below. |
| `POST /auth/app-codes` (`createAppCode`) | the account's own login session acting as the household's main profile | Body `{}`. Answers `{ "code", "expires_in", "server_address" }` for "Open in Silo"; see "App codes" below. Audited as `user.app_code_issued` with `app_code_kind` `session` and `expires_at`, never the code. |
| `POST /auth/app-codes/redeem` (`redeemAppCode`) | `{ "person_key", "message" }` where `message` is a `redeem` envelope signed with that person key, with `device_sig`, the app code in its payload | Enrolls the device key and opens its session. The person key is enrolled when the account has none. For a session code, an account that already has a different person key keeps it, the device key is enrolled under the account's key, and nothing is revoked. A sign-in link's code instead replaces that key, which is revoked with the devices it introduced that still wait for approval. The sealed plaintext adds `username` and `person_key` beside `person_key_id`, which names the account's person key once the redeem commits; `person_key` is `enrolled` when `person_key` is now the account's person key, `kept` when the account kept a different one, and, for a sign-in link's code only, `replaced` when `person_key` took the place of a different one. A person who has lost every other device redeems a code, then calls `PUT /auth/keys/person` from the new session as the main profile. 404 `app_code_invalid` for an unknown, used or expired code. Audited on the account as `user.app_code_redeemed` with `app_code_kind` (`join`, `session` or `sign_in_link`), the device key's `device_key_id` and the person key's `person_key_kid` when this redeem enrolled it, never the code. |
| `GET /auth/keys/session` (`getAppKeySessionAccess`) | any login session | `{ "list", "manage", "approve", "sign_in_again" }`: what this session may do with the account's keys, and whether signing in again would help. Answers while the switch is off. |
| `GET /auth/keys` (`listAppKeys`) | the account's own login session, opened by password, provider or the person's own device key | `person_key` (`kid`, `enrolled_at`, `enrolled_ip_address`; absent when the account has none) and `devices[]` (`id`, `kid`, `kind` `device`\|`app`, `name`, `platform`, `status` `active`\|`pending`, `enrolled_by` `introduce`\|`session`\|`app-code`\|`grant`, `allowed_by`, `enrolled_at`, `enrolled_ip_address`, `last_used_at`), newest first. `enrolled_ip_address` is the client address of the request that enrolled the key, decided as a login session's `ip_address` is, except that the unspecified address (`0.0.0.0` or `::`) the resolver answers for a client it couldn't tell is stored as none; it is an empty string when unknown, which includes every key enrolled before the server recorded it, and for a caller who is not an administrator while the server runs in demo mode. A session opened by device sign-in or by another app's key is refused this list (and the pending list), so it never sees an address. Revoked and denied keys are left out, and so is a waiting key older than 24 hours, without anyone denying or revoking it. `last_used_at` is the last time the key opened a session: a key enrolled and not used since shows it equal to `enrolled_at`, and a key waiting for approval shows `null`. |
| `DELETE /auth/keys/devices/{id}` (`revokeAppDeviceKey`) | the account's own login session | 204. Revokes the key and ends every session it opened. A device may revoke its own key, which ends the session making the request. 404 for a key that isn't the account's. |
| `PUT /auth/keys/person` (`replacePersonKey`) | the account's own login session acting as the household's main profile | Body `{ "person_key" }`, unsigned. Answers `{ "person_key_id": "<kid>" }`. The old person key is revoked; devices already enrolled keep their keys, and a device the old key introduced that is still waiting for approval is denied with it. Sending the current key again changes nothing. Audited as `user.person_key_replaced`: `person_key_kid` before (the replaced key; no before side when the account had none) and after (the new key), `session_opened_via` as for enroll, and `denied_device_keys` when it denied waiting devices. Sending the current key again records nothing. |
| `GET /auth/keys/pending` (`listPendingAppKeys`) | as `GET /auth/keys` | `{ "items": [ ... ] }`, each a device object as in `GET /auth/keys` with `status` `pending`: the account's waiting keys inside their 24 hours, oldest first. |
| `POST /auth/keys/pending/{id}/approve` (`approvePendingAppKey`) | the account's own login session acting as the household's main profile | Body `{}`, 204. The key becomes active and the device's next `sign-in` opens a session. The account must still be an enabled administrator (403 `permission_denied`). Approving an active key changes nothing, except that the session that key itself opened is refused (403 `permission_denied`); an unknown, denied, revoked or expired key is 404 `not_found`. A session opened by one of the administrator's own device keys may approve. Audited as `user.device_key_approved`. |
| `POST /auth/keys/pending/{id}/deny` (`denyPendingAppKey`) | as `GET /auth/keys` | Body `{}`, 204. The key's next `sign-in` answers 403 `key_denied`, and neither list shows it. Denying a denied key changes nothing; an active, revoked or unknown key is 404 `not_found`. Audited as `user.device_key_denied`. |
| `POST /auth/sign-in-links/redeem` (`redeemSignInLink`) | none; the sign-in link's `token` in the body | Trades a sign-in link for an app code of its own kind and a set-password token; see [sign-in links](#sign-in-links). Redeeming that code at `redeemAppCode` enrolls the request's person key as the account's: `person_key` is `enrolled` when the account had none or held it already, `replaced` when it held another, which is revoked with the devices it introduced that still wait for approval. Devices already enrolled keep their keys. A new or replacing person key and its device start unbound to any provider; the account's own person key sent again keeps its provider binding, and its device takes it. For an administrator's account the link's redeem hands out no code, and a link's code is refused here with 404 `app_code_invalid`. The `user.app_code_redeemed` row also names the link (`sign_in_link_id`) and the account that made it (`sign_in_link_created_by`). |
| `GET /admin/users/{user_id}/keys` (`listAdminUserAppKeys`) | acting admin | What `GET /auth/keys` shows for that account, each object with `provider_bound`, `identity_gone` and `identity_id` (the provider account the key is bound to, as `listAdminUserIdentities` identifies it; `null` when the key is bound to none; historical when `identity_gone` is `true`, since that provider account is no longer linked or listed) added, and `waiting_until` (when a `pending` key stops waiting; `null` otherwise) on each device. `person_key` also carries `enrolled_by` (`session`\|`app-code`). A key whose provider account is no longer linked is listed with `identity_gone` `true`: it can't sign in again, and the person's own list leaves it out. Revoked and denied keys, and waiting keys past their 24 hours, are left out. An unknown account is 404. The account's device sign-ins approved in advance are `listAdminUserDevicePreapprovals`. |
| `DELETE /admin/users/{user_id}/keys/devices/{id}` (`revokeAdminUserDeviceKey`) | acting admin, under the Owner rules (below) | 204. Revokes the key and ends every session it opened. Revoking a revoked key changes nothing; a key of another account is 404. Audited as `user.device_key_revoked`. |
| `DELETE /admin/users/{user_id}/keys/person` (`revokeAdminUserPersonKey`) | acting admin, under the Owner rules | 204. Revokes the person key: no device can be introduced with it, and devices it introduced that are still waiting are denied. Devices already enrolled keep their keys and sessions; the person sets up a new person key from a signed-in device. An account with no person key, as straight after a first call, answers 204 and records nothing; an unknown account is 404. The request names no key and acts on the person key the account holds when it arrives, so it is not safe to replay blindly: a repeat after the person has set up a new key revokes that one. Read the list first. Audited as `user.person_key_revoked`, with the count of waiting devices it denied. |
| `POST /admin/users/{user_id}/keys/pending/{id}/deny` (`denyAdminUserPendingKey`) | acting admin, under the Owner rules | Body `{}`, 204. As `denyPendingAppKey`. No administrator operation approves a waiting key. Audited as `user.device_key_denied`. |

**Administrators.** The four `/admin/users/{user_id}/keys` operations answer
while `auth.app_key_sign_in` is off, so keys can be reviewed and removed then.
Any acting administrator lists any account. A change follows the Owner rules of
revoking login sessions: an administrator acts on an ordinary account and on
their own, only the Owner acts on another administrator or on the Owner (403
`permission_denied` otherwise). A scoped API key and an administrator acting as
an account are treated as those operations treat them: the account the
credential belongs to is the actor. The account capability
(`getAdminAccountCapabilities`) reports `app_keys` when these operations are
served. A login session opened by a device key carries that key's `id` as
`device_key_id` in `GET /auth/sessions` and the administrator's session list;
signing such a session out leaves the key, so the device signs in again.

**Signed messages.** An envelope is exactly `{ "kid", "payload", "sig" }`, plus
`device_sig` on `introduce` and `enroll`. `payload` is base64url (no padding) of
the UTF-8 JSON payload. `sig` is the 64-byte `r || s` ECDSA P-256 signature, with
SHA-256, over the ASCII bytes `silo-sync-v1.<purpose>.<payload>`, the payload
string exactly as sent; `device_sig` is the device key's signature over the same
bytes. Each route takes only its own purpose. Every payload carries `purpose`,
`server_id`, `iat` and `exp` (Unix seconds, read as doubles and accepted when
whole, 0 to 2^53 - 1), plus `challenge`, `origin` and `reply_key`; `introduce`
and `enroll` add `device_key`, `device_name` (up to 128 characters) and
`device_platform` (up to 64). Public keys are base64url of the 65-byte
uncompressed point, and `kid` is base64url of the first 16 bytes of its
SHA-256. `origin` must already be in its canonical form: lower-case `http` or
`https`, a lower-case ASCII host (an IPv6 host in brackets in its RFC 5952
form), the port only when it is not the scheme's default, nothing after. A
device stamps `iat` and `exp` from the challenge's `server_time`.

**Grants.** A `grant` envelope is signed by the account's person key away
from the server and never sent by its signer. Its payload has the common
`purpose`, `server_id`, `iat` and `exp` (at most 300 seconds apart), plus
`grant_id` (base64url of 16 random bytes), `account_id` (the account's `id`),
`origins` (a non-empty array of canonical origins), `app_key` (the app's public
key) and `app_name` (up to 128 characters); no challenge. A `grant-redeem`
payload has `challenge`, `origin` (one of the grant's `origins`), `reply_key`,
`grant` (the grant envelope as an object, `{ "kid", "payload", "sig" }`), and
optional `device_name` (defaults to the grant's `app_name`) and
`device_platform`. The server checks the outer signature against the grant's
`app_key`, then the grant's against the live person key of `account_id`. A
grant works once: its `grant_id` is recorded when the app's key is enrolled.
The answer is a sealed reply with the usual `signed_in` plaintext. The key
takes the provider binding of the person key that signed the grant. A session
the app opens can't enroll keys, replace the person key, approve a device
sign-in now or in advance, read the key list, or revoke any key but its own.
It is also refused (403 `permission_denied`) on creating a personal API key
(`createPersonalAPIKey`) and on the operations that link a sign-in provider
account. It is kept off the other devices' playback, web push and native push
registrations, and may not host or drive a Watch Together room
([Other devices' playback and web push](#other-devices-playback-and-web-push)). The
settings device registry (`/devices`) does not limit it yet.

| Refusal | Problem |
| --- | --- |
| The grant was already used; it names another server, or its `origins` leave out the redeem's `origin`; it doesn't verify by the account's live person key (unknown account, replaced or revoked person key); the account is an administrator's or disabled | 403 `app_grant_invalid` |
| The grant's own `exp` has passed | 403 `app_grant_expired` |
| The redeemer doesn't hold the grant's `app_key` (the outer signature doesn't verify by it, or the envelope's `kid` isn't that key's ID), or the `grant-redeem` envelope is outside its own `iat` and `exp` | 401 `signature_invalid` |
| The `app_key` is already enrolled on this server | 409 `conflict` |

A revoked app key is still enrolled: a public key is enrolled once on a server
and can't be enrolled again. An app the person removed and then allows again
makes a new key and asks for a new grant.

**Sealed reply.** The routes that open a session answer
`{ "epk", "nonce", "ciphertext" }`, the plaintext sealed to the payload's
`reply_key`:

1. `Z` is the X coordinate of ECDH between the device's `reply_key` private key
   and `epk` (65 bytes; refuse a point not on P-256).
2. The key is HKDF-SHA256 with input `Z || epk || reply_key` (the 65-byte
   points), salt the challenge's 32 raw bytes, info `silo-sync-v1.reply`, 32
   bytes.
3. The plaintext is AES-256-GCM of `ciphertext` (tag appended) with the 12-byte
   `nonce` and no additional data.

The plaintext is UTF-8 JSON: `{ "status": "signed_in", "device_id",
"account_id", "tokens" }`, where `tokens` is the token pair a password login
returns (`access_token`, `refresh_token`, `expires_in`, `user`) and
`account_id` equals `tokens.user.id`; or, for an administrator's device waiting
for approval, `{ "status": "approval_required", "device_id", "account_id" }`
with no tokens. `device_id` is the device key's `id`. A `signed_in` plaintext
also carries `local_route` (`spki_sha256`, and `url` when `server.local_url` is
set), the shape `getServerConnections` gives it, when the API listener has a
TLS identity; see
[The pinned route](architecture/app-keys.md#the-pinned-route) for the rules a
device follows with it. Refusals are not sealed.

A `signed_in` plaintext to one of the person's own device keys (from
`introduce`, `sign-in`, `enroll` and `redeemAppCode`) also carries
`person_key_id`, read in the transaction that opens the session:

- a string, the `kid` of the account's live person key;
- `null` when the account has none: none was enrolled, an administrator revoked
  it, or it is bound to a sign-in provider identity that is gone from the
  account (the key list leaves such a key out too);
- absent from a server that does not report it, and from the two answers that
  never carry it: `approval_required` (no session opened) and every answer to
  another app's key, one enrolled through a grant (`redeemAppKeyGrant` and that
  key's later `sign-in`).

It tells a device which person key this server holds for the account, so a
device that knows a newer person key, or gets `null`, learns that this server
holds an older key or none. Another app's key is not one of the person's
devices: a key ID that changes would tell that app the person replaced their
key, so its answers leave the member out.

An answer to another app's key carries the same account object as
`getCurrentUser` gives that session: `tokens.user.email` is the empty string
(the person's address is not that app's to read) and
`tokens.user.download_allowed` is false (downloads are refused to it), and
`tokens.user.permissions` lists only permissions one of whose operations is
open to it, which today leaves it empty; the
name, role and the rest are the account's. It carries no
`tokens.account_created_at`, and its refreshes carry none.

**Waiting keys.** Every key an administrator's `introduce` enrolls waits for
approval; keys from `enroll` start active. A waiting device repeats `sign-in`
with its own key and gets `approval_required` until it is approved. Each
attempt needs a fresh challenge and a newly signed envelope: resending the same
envelope after a 429 answers 401 `challenge_invalid`, because its challenge was
spent. Sooner than five seconds after its last try it is 429 `rate_limited`
with `Retry-After`; a key that has waited 24 hours, or was denied, is 403
`key_denied` and is left out of `GET /auth/keys`. A key whose provider binding
changed during the request also answers 429, with a one-second `Retry-After`:
wait, get a fresh challenge and sign a new envelope. That attempt goes through
every sign-in check again, so it can still be refused (for example 409
`provider_sign_in_required` or 503). The `Retry-After` on the challenge route's
own 429 means: wait that many seconds, then repeat the empty POST.

**Problems.** A refusal at or before the signature (a bad signature, a key this
server doesn't hold or holds revoked, the wrong purpose or `server_id`, outside
`iat` and `exp`, a `device_sig` that doesn't verify) is 401 `signature_invalid`.
An unknown, used or expired challenge is 401 `challenge_invalid`; a challenge is
spent once the signature verifies, even when a later check refuses. A signed
`origin` the server knows is wrong is 403 `origin_mismatch`. A malformed
envelope, or a payload field in the wrong form once the signature verifies, is
422 `validation_failed`; a body that isn't JSON is 400 `malformed_request`. A
public key already enrolled on this server, or a different person key already
on the account, is 409 `conflict`. While `auth.local_password_login` is off, a
key not enrolled through the sign-in provider is 403 `local_login_disabled`
except for a break-glass account. A key enrolled through the provider is 409
`provider_sign_in_required` when the provider can't re-check its identity and
last vouched longer ago than `auth.refresh_token_expiry`: the person signs in
through the provider once and the key works again. It is 403 `not_permitted`
when the provider refused the person, 503 `provider_unavailable` while its
provider is disabled or, under the fail-closed outage policy, unreachable, and
503 `dependency_unavailable` with `Retry-After` when the check could not be
made, as at refresh. Unlinking an identity revokes the keys bound to it and
ends the sessions they opened, at once. Uninstalling its plugin leaves those
keys dead instead: the key list leaves them out, their next use revokes them,
and a session one opened earlier lasts until the provider age limit, as any
provider session whose identity is gone. Either way such a key answers
`signature_invalid`. A `device_name` or
`device_platform` holding a control character is 422. A disabled
account is 403 `permission_denied`.

**Sessions that can't manage keys.** Every route below refuses with 403
`permission_denied`:

| Route | Password, provider, own device key | Device sign-in (a TV) | Another app's key (grant) | No record of how it opened | Admin acting as the account, API key, remote playback |
| --- | --- | --- | --- | --- | --- |
| `GET /auth/keys`, `GET /auth/keys/pending`, `POST /auth/keys/pending/{id}/deny` | allowed | refused | refused | refused | refused |
| `POST /auth/keys/enroll`, `PUT /auth/keys/person`, `POST /auth/app-codes`, `POST /auth/keys/pending/{id}/approve`, `POST /auth/device/preapprove` | main profile only | refused | refused | refused | refused |
| `DELETE /auth/keys/devices/{id}` | allowed | refused | its own key only | refused | refused |

**The household's main profile.** `enroll`, `PUT /auth/keys/person`,
`POST /auth/app-codes`, `pending/{id}/approve` and `POST /auth/device/preapprove` create lasting access
to the account every profile shares, so only the household's main profile may
call them. Approving a device sign-in that is waiting now keeps no profile
rule. `X-Profile-Id` must
name the account's primary profile, with `X-Profile-Token` when it has a PIN
(without it, 403 `profile_verification_required`); naming any other profile is
403 `main_profile_required`, on an administrator's account too, and an
`X-Profile-Id` that names no profile of the account is 404 `not_found`. A request without
`X-Profile-Id` is accepted only from an administrator account that has at most
one profile, with no PIN, maturity or library limit. An administrator's account
with several profiles, or a PIN, names its main profile and sends its token
like any other account. Otherwise the request is 403 `main_profile_required`, also
for a one-profile account that isn't an administrator. The other key routes have no profile rule.

The three 403 answers on these routes have three remedies, and an app tells
them apart by the problem type, never by the detail text:

| Problem | Meaning | What the app does |
| --- | --- | --- |
| `permission_denied` | This session may not do this, whatever profile it names: a TV's session, another app's, an API key, or one opened before the server recorded how sessions open. The session is checked before the profile, so such a session never gets one of the other two. On approve it is also the answer for an account that is not an administrator, which is checked last and can follow a correct profile. | Sign in again on this device. |
| `main_profile_required` | The request names a profile that isn't the main one, or names none where one is needed. | Switch to the main profile and send it again. |
| `profile_verification_required` | The main profile has a PIN the request didn't prove, or the profile changed while the request ran. | Verify the PIN (`verifyProfilePIN`) and send it again. |

`main_profile_required` is also what the session form of `PUT /profiles/set`
answers for a request that isn't acting as the main profile.

Each of these checks the named profile a second time once it holds the
account, and answers 403 `profile_verification_required` if the profile's PIN
changed in between, the profile is no longer the main one, or another write to
the profile is in progress. Nothing was changed, and the request can be made
again. For `enroll` that means a new challenge and a newly signed envelope:
the first challenge is spent once its signature verifies. The others are
sent again as they were. If the second try gets `profile_verification_required`
too, verify the profile's PIN again for a new `X-Profile-Token`; if it gets
`main_profile_required`, the profile is no longer the main one. A profile without
a PIN can get the first answer too, when a PIN was added or the profile was
being written at that moment.

**Codes and links.** Both issuing operations answer `link_allowed`. When it is
true (only an ordinary invitation's code: redeeming it creates an account with
no administrator power) a page may put the code in the join link. When it is
false, a client shows the server address and the code as text for the person
to type, and never builds a link, a scannable link or anything a device could
open from them: a link can be received by an app other than Silo, which would
redeem the code first. For an ordinary invitation that risk is accepted (the
intercepting app gets a new, empty account the invited person notices at
once); for an administrator's invitation or a session code it is not.

**App codes.** A server's web page hands the Silo app a one-time code, and
the app redeems it to enroll its keys (`docs/architecture/app-keys.md`, "App
codes"). A code is base64url of 32 random bytes, good once for ten minutes, and
never past its invitation's expiry. `server_address` is what the page puts in
the link that opens the app, `silo://join#v1.<base64url of server_address>.<code>`
or the same fragment after `https://siloserver.org/join`: `server.public_url`
when the request arrived on that address's origin, otherwise the address the
request arrived on.

- `POST /invitations/app-code` is the invite page's "Join with the Silo app". It
  checks the invitation as `acceptInvitationLink` does (404 `not_found` for an
  unknown, used, revoked or expired one) and reserves the account name; the
  invitation stays open, and a second code from it replaces the first. An
  emailed invitation's account takes its address as name and email; for one
  with no address `username` is required (422 at `body.username`) and `email`
  optional (422 at `body.email` when malformed). A username containing `@`
  that isn't the email the same request gives is 422 at `body.username`. A
  name or email already held, or reserved by another unspent code, is 409
  `conflict` with `errors[0]` `{ "code": "duplicate", "location":
  "body.username" }` or `"body.email"`, naming the field that clashed. A
  code that is expired, or whose invitation was accepted, revoked, resent or
  has expired, reserves nothing: it is deleted when another code asks for its
  name or email. An invitation that creates a profile the store can't create in the same
  transaction is 501 `capability_unsupported`, as accept answers. While local password
  sign-in is off it is 409 `provider_sign_in_required`: an invitation never
  creates an account whose only way in is its keys. Spends the `invitation`
  budget (20 a minute with a burst of 10 by default), which the page shares
  between the lookup, each code and each renewal.
- `POST /auth/app-codes` is "Open in Silo". Its code reaches an existing
  account, so a page shows it as text to type and never puts it in the join
  link or any other link: an app that receives a link before Silo does could
  redeem it, and the server can't tell a typed code from a lifted one. Its
  answer's `link_allowed` is always false. Refused (403 `permission_denied`)
  for an API key, an administrator acting as the account, a remote-playback
  session, a session opened by device sign-in or by another app's key, and a
  session older than the record of how sessions open. The keys take this
  session's provider identity, so it works with local password sign-in off
  from a provider sign-in. A code whose session ends before it is redeemed is
  `app_code_invalid`.
- `POST /auth/app-codes/redeem`, for a join code, creates the account with no
  password (`password_hash` NULL, local password sign-in off for the account),
  enrolls both keys and marks the invitation accepted in one transaction;
  409 `conflict` with `errors[0].code` `duplicate` if the name was taken since, 409 `provider_sign_in_required`
  if local password sign-in was turned off since, 404 `app_code_invalid` if the
  invitation was revoked, replaced or accepted since. For a session code it
  enrolls the device key on that account, enrolling `person_key` when the
  account has none. An account with a different person key keeps it: the
  device key is enrolled under the account's key and nothing is revoked
  (`person_key: kept`). To start fresh after losing every other device, the
  person redeems a code and then calls `PUT /auth/keys/person` from the new
  session as the main profile. Keys from a code start active,
  also on an administrator's account. Of two redemptions of one code exactly
  one succeeds.

Redeem's two 409 `conflict` answers have different remedies, so an app reads
`errors[0].code`, never the detail:

| `errors[0].code` | `errors[0].location` | Meaning | What the app does |
| --- | --- | --- | --- |
| `duplicate` | `body.message.payload` | The account name or email the join reserved was taken since the code was issued. | Send the person back to the server's page to choose another name and get a new code. |
| `key_enrolled` | `body.person_key` or `body.message.payload` (the device key) | This server already holds that public key. | Make that key again, get a new challenge and redeem the same code. |

What a refusal of `redeemAppCode` uses up. The challenge is spent once the
envelope's signature verifies, whatever is refused after that. The code is
spent only when the redeem succeeds: every refusal after the code is found
rolls it back, so it stays good until it expires.

| Answer | Challenge | Code |
| --- | --- | --- |
| 409 `capability_disabled`, 429 `rate_limited`, 413, 400 `malformed_request`, 422 at the envelope's shape or `person_key` | not spent | not spent |
| 401 `signature_invalid` because the signature doesn't verify by `person_key` | not spent | not spent |
| 401 `signature_invalid` after the signature verified (`kid` isn't `person_key`'s ID, wrong `server_id` or purpose, outside `iat` and `exp`), 422 at `body.message.payload`, 403 `origin_mismatch` | spent | not spent |
| 401 `challenge_invalid` | (already unusable) | not spent |
| 404 `app_code_invalid` | spent | unknown, used or expired already. A code whose session ended, or whose invitation was accepted, revoked, resent or expired, can't succeed; a join code like that is deleted once another code is issued for its name or email, and answers the same |
| 409 `conflict` (`duplicate` or `key_enrolled`), 409 `provider_sign_in_required`, 501 `capability_unsupported`, 403 `permission_denied` (the account is disabled), 503 | spent | not spent |
| 200 | spent | spent |

A session code is not checked against the main profile's PIN again at redeem:
it was issued to the verified main profile and ends with the session that
issued it. `server_address` is where the app connects, not proof of which
server answers; the signed `server_id` and the origin rule are.

The invitation lookup (`lookupInvitationLink`) reports `password_join` (a
password claim works here: local password sign-in is on) and `app_join` (a
Silo app claim works here: app key sign-in and local password sign-in are both
on, and `acceptance_available` is true), so the page offers only what works.

**Switch.** While `auth.app_key_sign_in` is off the challenge, `introduce`,
`sign-in`, `enroll`, `POST /auth/grants/redeem`, both app code issues, app
code redemption, `PUT /auth/keys/person`, `pending/{id}/approve` and the signed
form of `PUT /api/v2/profiles/set` answer 409 `capability_disabled`, and the
invitation lookup reports `app_join: false`. The capability,
`GET /auth/keys` and its deletes, `GET /auth/keys/pending` and
`pending/{id}/deny` stay open. A server in demo
mode (`demo.enabled`) behaves as if the switch were off, for administrators
too, and its capability reports `disabled`: every visitor shares one account
there, so no key is enrolled on it and none signs in. There
`DELETE /auth/keys/devices/{id}` and `pending/{id}/deny` are open to administrators only: a caller who is not one gets
the [demo refusal](#demo-mode). For the same reason
`POST /auth/device/preapprove` is refused in demo mode for a caller who is not
an administrator, and the device login capability reports `preapprove: false`.

**Rate limits.** `createAppKeyChallenge` spends the per-address
`key_challenge` budget and `introduce`, `sign-in`, `enroll`, grant redemption,
`createAppCode` and app code redemption the `key_sign_in` budget, each 60 a minute with a burst of 20 by default
(`ratelimit.auth.key_challenge.*`, `ratelimit.auth.key_sign_in.*`).

Server setting: `auth.app_key_sign_in` (default `true`). It is read on each
request, so a change applies on every node at once.

## External sign-in

OIDC and LDAP sign-in come from `auth_provider.v1` plugins. Account
resolution, the identity key, the local-password policy and the one-provider
rule are in [external-sign-in.md](architecture/external-sign-in.md).
`getExternalSignInCapabilities` (`GET /auth/external-sign-in/capabilities`,
public) reports which of these operations the server serves, including
`credentials_linking` (`linkAccountIdentityWithCredentials`) and
`network_sign_in` (`signInWithNetworkIdentity` and
`linkAccountIdentityWithNetwork`; see
[Network identity](architecture/external-sign-in.md#network-identity)).
`network_link_keeps_password` says linking a network identity keeps the
account's local password sign-in; clients use it to describe what connecting
does, because servers without it turn the password off. It describes the
linking rule whether or not `network_sign_in` is served.

| Operation | Credential | Notes |
| --- | --- | --- |
| `GET /auth/providers` (`listAuthProviders`) | none | Leaves `local` out while `auth.local_password_login` is off. `password_login` says whether any listed provider takes a password; apps and TVs hide the password form when it is false. An `oauth` provider carries `native_start_path`, the native start's path below the server base, while OAuth sign-in is served (a public URL is configured). Apps append `native_start_path` to their own saved server base URL and never open another origin; an `oauth` provider without it offers no native sign-in. A `network` provider is listed only to a request that came through its own overlay from a device it vouches for, with `network_sign_in_path` and `network_identity` (`display_name`, `username`: who owns the device, for a "Continue as" label); it is never `default`. Clients ignore modes they do not know. |
| `POST /auth/network/{id}/sign-in` (`signInWithNetworkIdentity`) | none; the request must come through the network provider's own overlay listener | Signs in the owner of the requesting device through network provider installation `{id}` (`mode: network`, such as the Tailscale plugin). Body: `{}` with `Content-Type: application/json`. Answers the same token pair as `login`. Refusals: 403 `network_identity_required` (the request did not come through that provider's overlay), 403 `not_permitted` (the provider refuses the device: unknown, tagged, or left out by its policy; or the account's OIDC or LDAP provider refuses the account), 403 `account_required`, 403 `permission_denied` (the account is disabled), 409 `email_in_use` or `identity_linked_elsewhere`, 404 `not_found` (not an enabled network provider), 503 `provider_unavailable`, 415 without a JSON body, 429. Spends the `login` rate-limit budget. |
| `POST /auth/login` (`login`) | none | Without `provider`, the name is routed: an account with local password sign-in signs in locally (and is refused with `local_login_disabled` while the server switch is off, never sent to the directory), any other name goes to the enabled LDAP plugin. A name that has used its attempt budget, or a client over the `login` rate-limit budget, is 429 `rate_limited` ([Sign-in attempts per name](#sign-in-attempts-per-name)). |
| `GET /account/identities` (`listAccountIdentities`) | signed-in account; `X-Profile-Id` optional | The caller's linked identities: provider, username, email, `linked_at`, `last_sign_in_at`, and `last_checked_at` (the provider's latest answer about the identity, from a sign-in or a re-check). Every profile on the account may read the list. `can_unlink` says whether `deleteAccountIdentity` would disconnect an identity now for the profile `X-Profile-Id` names, or for a request that names none: it is false for a profile that is not the household manager, and when the identity is the account's last way to sign in. Clients show the disconnect control only while it is true. `can_link` says whether the caller may link another identity: false for an API key or a "View as user" session. In [demo mode](#demo-mode) both are false for a caller that is not an administrator. A named profile passes the usual viewer gate, so a PIN-protected profile without `X-Profile-Token` is 403 `profile_verification_required`. |
| `POST /account/identities/link-credentials` (`linkAccountIdentityWithCredentials`) | the account's own login session | Links the directory (LDAP) identity. Body, all required and non-null: `installation_id` (the credentials provider from `listAuthProviders`), `password` (the account's local password, 1 to 1024 characters), `username` (directory username, 1 to 256) and `directory_password` (1 to 1024). Answers 201, `Location: /api/v2/account/identities`, and the linked identity as `listAccountIdentities` shows it. Refusals: 422 `validation_failed` at `body.password` (wrong local password) or `body.directory_password` (the directory refused the credentials); 409 `local_password_required`; 403 `not_permitted`, `account_disabled` (the directory account is disabled, locked or expired) or `password_expired`; 403 `permission_denied` (the Silo account is disabled, or an API key or impersonation session); 409 `identity_linked_elsewhere` or `already_linked`; 404 `not_found` (not an enabled credentials provider); 503 `provider_unavailable`; 429. Spends the `login` rate-limit budget. Linking turns local password sign-in off unless the account is break-glass. |
| `POST /account/identities/link-network` (`linkAccountIdentityWithNetwork`) | the account's own login session, through the network provider's overlay | Links the network identity of the requesting device. Body, required and non-null: `installation_id` (the network provider from `listAuthProviders`) and `password` (the account's local password). Answers 201 like `linkAccountIdentityWithCredentials`. Refusals: 403 `network_identity_required`, 422 `validation_failed` at `body.password`, 409 `local_password_required`, 403 `not_permitted`, 403 `permission_denied`, 409 `identity_linked_elsewhere` or `already_linked`, 404 `not_found`, 503 `provider_unavailable`, 429. Spends the `login` rate-limit budget. Unlike other linking, the account keeps its local password sign-in, which answers 403 `not_permitted` while the provider refuses the person (break-glass accounts excepted). |
| `DELETE /account/identities/{id}` (`deleteAccountIdentity`) | the account's own login session, acting as the household manager | How the account signs in belongs to the whole household, and the account cannot always link a disconnected identity again by itself (self-service linking needs the local password), so only the household manager may disconnect one: `X-Profile-Id` names the account's primary profile, with `X-Profile-Token` from `verifyProfilePIN` when that profile has a PIN. A named profile passes the usual viewer gate first: an unknown profile ID gets 404 `not_found`, and a PIN-protected profile without its token, primary or not, gets 403 `profile_verification_required`. A profile that passes the gate and is not the primary one gets 403 `permission_denied`, on an admin account too. A request without `X-Profile-Id` is accepted only from an admin account while no profile on the account has a PIN, a maturity limit, or library restrictions; otherwise it gets 403 `permission_denied`. Refused with 409 `last_sign_in_method` unless the account can still sign in with its local password or another identity. API keys and impersonation sessions get 403 `permission_denied`. |
| `GET /admin/users/{id}/identities` (`listAdminUserIdentities`) | acting admin | Adds `external_subject`, `issuer` and `last_check_status`: what the provider said at `last_checked_at` about the identity, from a sign-in or a re-check (`active`, `not_found`, `disabled`, `not_permitted`, `unsupported`, `unavailable`, or `none` before the first). |
| `POST /auth/refresh` (`refreshSession`) | the refresh token | A session opened through the provider is re-checked with it when due (see below). A refused account ends the session (401 `session_expired`). Under the `fail_closed` outage policy, an unreachable provider answers 503 `provider_unavailable`; the session stays valid and the client retries later. A database that cannot be read answers 503 `dependency_unavailable` with `Retry-After` (see [When the server cannot check a session](#when-the-server-cannot-check-a-session)). A provider answer the server received but could not apply, where a retry would not help, is 401 `invalid_token`: a refusal that could not be applied, a role sync that rolled back, or a replacement refresh token that could not be stored (the provider may accept only that token now). A refusal whose revocation rolled back stays on record and ends the session at the next check, and an unapplied active answer stays due. Any other database failure after an active answer is the retryable 503 above. |
| `POST /admin/users/{id}/identities` (`createAdminUserIdentity`) | acting admin | Links by `installation_id` and the exact `external_subject`. Turns local password sign-in off unless the account is break-glass or the installation is a network identity provider. |
| `DELETE /admin/users/{id}/identities/{identity_id}` (`deleteAdminUserIdentity`) | acting admin | May leave the account without a sign-in method; setting a password with `updateAdminUser` turns its local password sign-in back on. |
| `PUT /admin/users/{id}` (`updateAdminUser`) | acting admin | `break_glass` sets or clears the flag: admin accounts only, and only the server Owner may change it (403 `permission_denied`). The Owner has it by default (set at first-run setup and on every ownership move) and may clear it on its own account. A `password` also turns the account's local password sign-in back on, so an admin other than the Owner may not set its own while its local sign-in is off (403 `permission_denied`). Demoting, disabling or clearing the last usable break-glass admin while local sign-in is off is 409 `break_glass_required`; so is deleting it (`deleteAdminUser`) or turning `auth.local_password_login` off without one (`updateAdminSettings`, `updateAdminSetting`). |
| `POST /admin/plugins/installations/{id}/auth-binding/test` (`testAdminPluginAuthBinding`) | acting admin | Sends staged `config` entries (blank secrets keep the stored value only while the rest of that entry is unchanged and no secret in it is typed) to the plugin's `TestConnection`; answers `ok`, the plugin's `steps` and `callback_url`, the redirect URI to register at an OAuth provider. If the public URL is unset or the binding mode cannot be read, the response keeps the test results and leaves the URL empty. A plugin without a connection test, or a disabled installation, is 409. Not retried automatically. |
| `PUT /admin/plugins/installations/{id}/auth-binding` (`updateAdminPluginAuthBinding`) | acting admin | Applies without a restart (`X-Silo-Restart-Required: false`). `auto_provision` defaults to true. Enabling a second binding of the same kind is 409 `provider_already_enabled`; one network identity binding may be enabled beside the one OIDC or LDAP binding. |
| `GET /admin/plugins/installations` (`listAdminPluginInstallations`) | acting admin | Each OAuth auth binding carries `callback_url` (the callback to register at the provider) and `post_logout_redirect_url` (`/login`, to register for provider logout), both on `server.public_url`; empty without a public URL and for a password provider. The other operations that answer an installation carry them too. |

An acting administrator can read deployment ownership on the installed sign-in
capability and its binding. Their optional `env_managed` arrays list the binding
fields declared by the deployment, such as `enabled` and `callback_mode`. Binding
updates may change the other fields and must preserve owned values. An attempted
change answers 422 `validation_failed` with field code `managed_by_environment`.
See [Environment provisioning](admin-plugin-catalog-api.md#environment-provisioning)
for global config ownership and provisioning status. Public provider discovery
continues to advertise the native start path used by Apple and Android. Roku
uses the device sign-in flow. Jellyfin sign-in continues to use Silo's provider
registry.

Refusals use these problem types: `not_permitted` (403, the provider's
access rules refused the person), `account_required` (403, the provider
admitted the person but the server has no account for them and account
creation is off), `local_login_disabled`
(403), `password_expired` (403), `email_in_use` (409),
`identity_linked_elsewhere` (409), `provider_already_enabled` (409),
`break_glass_required` (409), `last_sign_in_method` (409),
`provider_unavailable` (503; also a sign-in the provider answered before a
re-check of the same account recorded a later answer, which succeeds when
repeated), `network_identity_required` (403, a network
identity sign-in or link that did not come through the provider's overlay)
and `invalid_grant` (400, a completion code
redeemed with a `code_verifier` that does not fit it, or a web code redeemed
without its browser's completion cookie). Linking adds
`local_password_required` (409, an account without local password sign-in
asked to confirm its password: link tickets and directory linking) and
`already_linked` (409, the account already has an identity at the provider:
directory linking, `completeAccountIdentityLink` and
`createAdminUserIdentity`); directory linking and
`completeAccountIdentityLink` also add `account_disabled` (403, the
provider's account is disabled, locked or expired). Link tickets, link
completion and directory linking answer each refusal they share with the
same problem type.

### OAuth sign-in flows

`getOAuthHandshakeCapabilities` (`GET /auth/oauth/capabilities`) reports
`available`, `native` (the native-app handoff, which needs a configured public
URL), `linking` (link tickets and
the linking operations), `provider_logout`, `stable_callback` (the fixed
provider redirect route), and `select_account` (the starts accept
`prompt=select_account`). Directory linking needs no handshake, so
`getExternalSignInCapabilities` reports it. The flows are raw redirects; the rules behind them are in
[external-sign-in.md](architecture/external-sign-in.md#oauth-flows).

The three starts share the per-address `oauth_start` rate-limit budget, 40 requests per
minute with a burst of 20. A start over it answers 429 `rate_limited` with `Retry-After`
instead of a redirect, before the plugin is asked. A form-post start that bounces to
`startOAuthLogin`, and a native start moved to the public URL, spend it twice. The
callbacks spend no budget.

| Operation | Credential | Notes |
| --- | --- | --- |
| `POST /auth/oauth/{install_id}/init` (`initOAuthLogin`) | none | Web form start. `next` is a site-relative return path. On another origin than the public URL it answers 303 to `startOAuthLogin` there. |
| any of the three starts, `?prompt=select_account` | none | Passed to the plugin as the OIDC `prompt`, so the provider lets the person choose another provider account. The web "Switch account" path signs out of Silo, then starts with it. Any other value is 400. Linking flows always use `prompt=login`. |
| `GET /auth/oauth/{install_id}/start` (`startOAuthLogin`) | none | The same start as a link. Sets the browser-binding cookie, then 302 to the provider. On another origin it first answers 302 to itself on the public URL, with `bounce=1`. A `link_ticket` is 400: web linking starts with `startAccountIdentityLink`. Bad parameters are 400 text; a provider that cannot start the flow (plugin not loaded, `InitAuthorize` failed) or a missing public URL sends the browser to `/login?error=oauth_failed&reason=provider_unavailable` (`login_failed` for a server error), which does not redirect to the provider again. `initOAuthLogin` does the same once on the public origin. |
| `GET /auth/oauth/{install_id}/native/start` (`startNativeOAuthLogin`) | none | Apps open it on their saved server base in the system browser with `code_challenge` (S256), `code_challenge_method=S256` and `app_state`, plus `link_ticket` to link. The server records the origin the start arrived on as the flow's start origin. On another origin than the public URL that is not a local one (below) it answers 400 text; on an accepted one it keeps the start on the server and answers 302 to itself on the public URL with only `flow=<ID>` (random, single use, two minutes); an unknown, used, expired or foreign `flow`, or one presented on another origin than the public URL, is 400 text (the latter leaves the start for the public URL). Bad parameters are 400 text. With valid parameters, a provider that cannot start the flow sends the browser to the app redirect with `error=provider_unavailable` (`login_failed` for a server error), `state`, `server` and `iss`. An unknown, used, expired or foreign `link_ticket` sends it to the app redirect with `error=session_expired`, `state`, `server` and `iss`. |
| `GET /auth/oauth/{install_id}/callback` (`finishOAuthCallback`) | the binding cookie | The provider's redirect back, with `code` or `error`. A web flow's redirect to `/login/oauth-complete?code=` also sets the `silo_oauth_complete` cookie on the complete path of each API version (HttpOnly, SameSite=Lax, two minutes). |
| `GET /auth/oauth/callback` (`finishStableOAuthCallback`) | the binding cookie | The same provider redirect for a binding using `callback_mode: stable`. Signed state names the installation and the original callback mode. |
| `POST /auth/oauth/complete` (`completeOAuthLogin`) | the completion code | Redeems the code for the token pair and `user`. A native code needs `code_verifier`; a web code refuses one and needs the `silo_oauth_complete` cookie of the browser the callback answered, which that browser sends by itself (400 `invalid_grant` otherwise; the code stays redeemable). The login session opens when the code is redeemed, so a code that is never redeemed leaves none. An unknown, expired or used code is 401 `invalid_token`, and a second redemption revokes the session the first opened. |
| `POST /account/identities/link-ticket` (`createAccountIdentityLinkTicket`) | the account's own login session | Body `installation_id` and the current `password`. Answers a single-use `ticket` valid for 5 minutes. A wrong password is 422 at `body.password`; an account without local password sign-in is 409 `local_password_required`; API keys and impersonation sessions are 403; an installation that is not an enabled OAuth provider (an LDAP one included) is 404. Spends the `password_change` rate-limit budget. |
| `POST /account/identities/link-start` (`startAccountIdentityLink`) | the ticket's account, own login session | Web linking. Body `link_ticket` and optional `next`. Consumes the ticket, sets the flow's browser-binding cookie on this response and answers `authorize_url`, which the same browser then opens; the provider is asked for a fresh sign-in (`prompt=login`). Must be called on the public URL's origin (409 otherwise). An unknown, used, expired or foreign ticket is 404; a provider that cannot start is 503 `provider_unavailable`. |
| `POST /account/identities/link-complete` (`completeAccountIdentityLink`) | the ticket's account, own login session | Native linking. Body `code` (from the app redirect with `link=1`) and `code_verifier`. Makes the link and answers 204. A verifier that does not fit is 400 `invalid_grant` and the code stays redeemable; an unknown, used or expired code, or another account, is 401 `invalid_token`; the link's own refusals are 409 `identity_linked_elsewhere`, 409 `already_linked`, 403 `not_permitted`, 403 `account_disabled` (the provider's account is disabled, locked or expired), 403 `password_expired`, 403 `permission_denied` (the Silo account is disabled) or 503 `provider_unavailable`. |
| `GET /auth/provider-logout` (`getProviderLogout`) | signed-in account | Call before `logout`. `end_session_url` is the provider sign-out to navigate to after the Silo logout, or empty. It is always empty for an API key or an impersonation session, so an administrator viewing as someone never carries that account's provider session or ID token. The server bounds the plugin lookup at 2 seconds and answers empty past it. |

Where a flow ends:

- Web sign-in: `/login/oauth-complete?code=<code>` on the public URL; the page
  redeems the code within 60 seconds. When it can't, it returns to
  `/login?error=oauth_failed` with `reason=session_expired` (the code was
  refused), `state_invalid` (no code) or `login_failed`, and the login page
  does not send the browser back to the provider on its own.
- Native sign-in: `org.siloserver.silo:/auth/callback?code=<code>&state=<app_state>&server=<server_id>&iss=<origin>`.
  The app checks `state`, that `iss` is its saved server base's origin, and
  that `server` is the `server_id` of the server it started with, then
  redeems the code with its `code_verifier` at its saved server base
  (below).
- Web linking: the flow's `next` path with `linked=1`. Linking opens no
  session.
- Native linking: the app redirect with `code`, `link=1`, `state`, `server`
  and `iss`. Nothing is linked yet: the app confirms with
  `completeAccountIdentityLink` within 60 seconds, signed in as the account
  that asked for the ticket.
- Failure: web sign-in goes to `/login?error=oauth_failed&reason=<reason>`,
  web linking to `next` with `error=oauth_link_failed&reason=<reason>`, and a
  native flow to the app redirect with `error=<reason>`, `state`, `server`
  and `iss`. Reasons: `not_permitted` (the provider's access rules refused
  the person), `account_required` (no account here and account creation is
  off), `email_in_use`, `identity_linked_elsewhere`,
  `account_disabled`, `provider_unavailable`, `state_invalid`,
  `session_expired`, `already_linked` (linking an account that already has an
  identity at this provider) and `login_failed` (anything else). A callback
  without the flow's binding cookie always goes to the web login page with
  `state_invalid`, whatever the flow. A callback that arrives after the flow
  expired (within an hour of it), in the browser that started it, goes to the
  flow's own failure location with `session_expired`.

`iss` on the app redirect: every app redirect of a native sign-in or
linking flow, success or failure, carries `iss`, the flow's start origin:
the origin the native start first arrived on, derived from the host a
trusted proxy names in a single `X-Forwarded-Host` (else the `Host` header)
and the scheme of the connection or the one a trusted proxy names
(`X-Forwarded-Proto`), serialized as `scheme://host[:port]` (scheme and host
in lower case, the scheme's default port left out, IPv6 in brackets, no
path, no trailing slash). A start that arrives on the public URL names the
public origin; a start on the app's saved LAN address names that address,
even though the browser finishes the flow on the public URL. A start on
another origin than the public URL is refused with a plain-text 400 unless
the origin is a connected network access provider's or a local address: an
IP literal in the loopback, private, link-local or `100.64.0.0/10` range,
or a single-label, `.local`, `.lan`, `.localdomain`, `.home.arpa` or
`.internal` host name.

- The app opens the native start on its saved server base: it appends
  `native_start_path` to the saved base URL, which keeps a reverse proxy's path prefix (for example
  `https://example.com/silo`), and never opens another origin.
- A redirect without `iss`, or with an `iss` other than the saved server
  base's origin serialized the same way, is discarded unredeemed ("This
  sign-in came back from a different server. Nothing was signed in.").
- The app redeems the code (`completeOAuthLogin` or
  `completeAccountIdentityLink`) only at its saved server base. It never
  moves, re-keys, duplicates or switches a saved server because of a
  sign-in.

This stops a relay by redirect: a hostile server saved in the app can
redirect the app's start to another server's native start with the app's
`code_challenge` and `app_state`. The browser then reaches that server on
one of its own origins, which its `iss` names, so the app neither accepts
the redirect for the hostile server nor posts the code and verifier there.
The move to the public origin carries only the random flow ID, so no page
on the way can change the recorded start origin. It does not stop a relay
by request: whoever sends the start chooses the `Host` header, so a hostile
server that sends the app's start to the real server itself can name its
own origin. The local-origin rule above limits that to a hostile server on
a local address or a network access provider origin.

Web logout with provider sign-out: call `getProviderLogout`, then `logout`,
then navigate to `end_session_url` when it is not empty. The provider returns
the browser to `/login` on the public URL, which the administrator registers
as a post-logout redirect at the provider. Whether the plugin offers an
end-session URL is the plugin's own setting (the OIDC plugin's
`provider_logout`). The web client bounds its wait for `getProviderLogout`
and logs out of Silo without the provider when it does not answer in time.

Server settings: `auth.local_password_login` (default `true`; turning it off
needs a break-glass admin; while it is off, `signup` and `acceptInvitation`
are refused with 403 `local_login_disabled` and `getSignupStatus` reports
signup off), `auth.email_auto_match` (default `false`; matches ordinary
accounts only, never an admin, the Owner or a break-glass account; in
[demo mode](#demo-mode) it matches no account on the server, not only the
shared demo account),
`auth.provider_recheck_interval` (default `12h`, 5m to 30d: how old the
provider's last answer about an identity may be before a refresh asks again),
`auth.provider_recheck_outage_policy` (`fail_open`, the default, keeps a
session whose provider cannot be reached; `fail_closed` refuses its refresh
until the provider answers) and `auth.app_key_sign_in` (default `true`; see
[App key sign-in](#app-key-sign-in)).

Provider re-check: a login session opened through the provider (an OAuth or
LDAP sign-in, a device sign-in approved from such a session, or a session of
an account when it is linked, break-glass accounts excepted) is re-checked at
refresh once the identity's last answer is older than the interval. The
provider removing, disabling or no longer admitting the person revokes every
login session of the account and deletes its API keys. A provider that
cannot re-check the identity (an OIDC provider that issued no refresh token)
stops the sliding: those sessions end `auth.refresh_token_expiry` after the
provider last vouched for them, a device sign-in approved from one keeping
its approver's limit. So do the sessions of an identity that was unlinked or
whose plugin was removed. Credentials that never refresh (API keys,
Audiobookshelf sessions, idle provider sessions) are covered by the
`recheck_external_identities` task, hourly by default: it re-checks each due
identity whose account still holds one, with the same outcomes, so a refused
account also loses its API keys and Audiobookshelf sessions. One node runs
each pass. When the provider cannot re-check an identity and the person has
not authenticated through it (an interactive sign-in, or a re-check that
answered active) for `auth.refresh_token_expiry`, a re-check from a refresh
or the task deletes the account's API keys and revokes its Audiobookshelf
sessions, as for a refusal; its login sessions keep their absolute-age
bound. People who sign in through the provider at least that often keep
their keys. If a revocation fails, nothing is revoked. A refusal is kept and
applied at the next check without asking the provider again; an
`UNSUPPORTED` answer is not stored, and the next refresh asks again.
`getExternalSignInCapabilities` reports `provider_recheck`.

While local sign-in is off, sending or resending an invitation and accepting
one are refused with 403 `local_login_disabled`: an invitation creates a
local-password account, which could not sign in.

## Ordinary authentication

The ordinary auth surface provides login, refresh, logout, provider discovery,
initial setup, invited signup, device pairing, OAuth completion, and account
password management. Login and device-start submissions create fresh durable
state and must not be automatically replayed after an uncertain response.

Invited signup commits invite consumption and account creation together. With the
PostgreSQL profile provider, the optional default profile joins that transaction.
The transaction rechecks public-signup and local-password policy while holding
the settings mutation lock. If disabling signup commits first, the in-flight
signup is refused without creating an account or spending the invite.
SQLite profile storage remains a separate-store boundary; this does not certify
an atomic cross-store operation or activate backend conversion.

The bundled web client uses these routes. Its OAuth sign-in buttons
are links to `GET /api/v2/auth/oauth/{install_id}/start` (`startOAuthLogin`);
`POST /api/v2/auth/oauth/{install_id}/init` (`initOAuthLogin`) remains for
form posts. The provider returns to
`GET /api/v2/auth/oauth/{install_id}/callback`. Provider configuration must allow
that callback URI. See [OAuth sign-in flows](#oauth-sign-in-flows).

The web transport never refreshes a stored session or automatically replays a
rejected login, setup, signup, OAuth completion, refresh, device-start, or
device-poll request. These operations carry their own credential or establish a
new login flow; refreshing an unrelated session cannot repair a refusal. A device
poll's scheduled continuation is a separate request governed by `poll_after`.
Authenticated account reads retain refresh recovery.

Omitting an optional login, setup, signup, or device-pairing member selects its
default. Explicit JSON `null` is rejected with `422 validation_failed` at that
member before the service performs any effect. Provider identifiers are accepted
exactly as discovery advertises them, including composite plugin identifiers.

`POST /api/v2/auth/plugin-launch` (`createPluginLaunch`) issues the plugin access
cookie for the current login session and optional validated profile: the same
five-minute `HttpOnly` `SameSite=Lax` credential, `Secure` on HTTPS,
scoped to the plugin-content parent path `/api/v2/plugin-content` and never
broadened to `/`. The body is `{"expires_in": 300}`. A credential without a login
session, such as an API key, is refused with 403 `permission_denied`; an unknown
declared profile is 404 and a PIN-locked one without its token is 403
`profile_verification_required`. Repeating the request reissues an equivalent
cookie. A page request through a cookie past half its lifetime gets a fresh one
while the login session could still refresh ([plugin content](architecture/plugin-content.md)).
The bundled web client launches pages under
`/api/v2/plugin-content/plugins/{installation_id}/`. Compatibility of the reissued
cookie against a served auth-provider plugin is not yet proven
and is a follow-up.

Native clients must distinguish failed credential exchanges from an expired
bearer on an authenticated read, retain refresh concurrency protection, and
preserve the selected account and profile during device handoff.


## Demo mode

With the `demo.enabled` server setting on, every visitor signs in to one shared account that
is not an administrator. Demo mode refuses that account any change one visitor could use to
lock out, sign out or take over the others. An operation marked demo-restricted
(`DemoRestricted` in `internal/apiv2`, `x-silo-demo-restricted` in the OpenAPI document)
answers a caller that is not an administrator `403 permission_denied` with the detail "This
action is not available in demo mode." Reads are never restricted, and administrators are
never restricted.

Every request reads `demo.enabled` (one setting read, no time-based cache), through one
`demomode.Setting` per process shared by every surface that enforces demo mode. Only the
stored value `true`, compared untrimmed, turns demo mode on. When a read fails, the error is
logged and the last value this process read is used: a server that is not a demo keeps
working, and a demo server keeps its demo refusal. Each API replica remembers its own value.
Only a process that has never read the setting can't tell whether it is a demo, and then it
fails closed without claiming to be one: a non-administrator's demo-restricted change is
`500 internal_error`, `deleteSession` on another session is `500 internal_error` (the caller's
own session still ends, and a session opened by device sign-in gets its usual `403`, because
that rule is judged first), and the capability answers below say no. An administrator's request
does not read the setting.

A visitor can:

- browse, play, and sign out (`logout`, or `deleteSession` on the session making the request);
- approve or deny a device sign-in (`approveDeviceLogin`, `approveDeviceHandoff`,
  `denyDeviceLogin`, and Jellyfin Quick Connect `POST /QuickConnect/Authorize`), which is how a
  visitor tries a TV app.

A visitor cannot:

- change the account password (`changePassword`), and so cannot sign the account out
  everywhere either;
- link or unlink an external sign-in (`createAccountIdentityLinkTicket`,
  `startAccountIdentityLink`, `completeAccountIdentityLink`,
  `linkAccountIdentityWithCredentials`, `linkAccountIdentityWithNetwork`,
  `deleteAccountIdentity`);
- end another session of the account (`deleteSession` on any other session);
- create or revoke personal API keys, or create, change or delete profiles;
- revoke an app device key or deny a device waiting for approval (`revokeAppDeviceKey`,
  `denyPendingAppKey`), since a key enrolled before demo mode was turned on still opens
  sessions on the account and revoking it ends them;
- approve a device sign-in in advance or withdraw one (`preapproveDeviceLogin`,
  `revokeDevicePreapproval`).

External sign-in enforces the same rule where every linking flow attaches its identity, in
account resolution: a linking flow that ends on an account that is not an administrator while
demo mode is on, including one started before demo mode was turned on, is refused with
`not_permitted` and attaches nothing. Email auto-match (`auth.email_auto_match`) does not apply
in demo mode, as when it is off, so a provider sign-in whose verified email equals the demo
account's goes on as an unmatched one: account creation refuses the taken email
(`409 email_in_use`) where the binding's `auto_provision` is on, and the sign-in is
`account_required` where it is off. Matching applies to no account in demo mode, not only the
shared demo account. Demo mode is read before resolution's transaction; a process that has
never read it fails the link and matches nothing.

The capability answers clients read before offering these actions say no to a visitor:
`getAccountPasswordCapability` `allowed`, `getLoginSessionCapabilities`
`revoke_other_sessions`, and `listAccountIdentities` `can_link` and `can_unlink`.

The Jellyfin-compatible listener serves no password change, identity linking, or revocation of
another session; its `POST /Sessions/Logout` ends only the calling session. On the
Audiobookshelf-compatible listener (beta), signing out normally revokes every token of the
account and profile and closes their playback sessions; in demo mode, for an account that is
not an administrator, it revokes only the caller's own credentials: the presented access token,
and the refresh token the request carries (the `x-refresh-token` header or the `refresh_token`
cookie) when it verifies as one of the same account and profile. It closes no playback session
(none records which token opened it). A refresh token the request does not carry stays live
until it expires. When the account's role can't be read, or the process has never read the
setting, sign-out takes the demo path. An expired access token revokes nothing.

`TestDemoModeCoversAccountOperations` in `internal/apiv2` fails when an operation tagged
`account`, `api-keys`, `app-keys`, `auth`, `device-login`, `notifications` or `profiles` that a
non-administrator can call is neither demo-restricted nor listed, with its reason, as allowed.

## Policy discovery

`GET /api/v2/policy/capability` reports `enabled`, `editor_available`,
`decision_types`, `generation`, `degraded`, optional `degraded_reason` and
`degraded_domains`, and `eval_timeouts`. An absent policy system returns `200`
with `enabled: false` and the supported decision types. This is discovery, so it
does not require policy-editor authorization.

The route requires authentication and preserves the demo restriction. A profile
is optional; when supplied it must pass viewer verification. The bundled web
policy query uses this endpoint.

### Public provider icons

Bootstrap generates provider icon URLs as
`/api/v2/plugin-content/plugins/{installation_id}/assets/...`. A capability
manifest may still supply the legacy `/api/v1/plugins/{installation_id}/assets/...`
form; provider discovery projects that onto the versioned mount. Either form is
exposed only when the matching provider installation has a public GET route descriptor. Descriptor selection must use the proxy's exact/wildcard precedence;
prelogin images cannot depend on a launch cookie. Missing public-route proof,
unavailable content, malformed paths or private routes omit the icon without
failing provider discovery. Query strings and fragments are retained. While
the auth plugin's `icon_url_path` setting is unset or empty, the default its
global config schema declares is the button icon, so a fresh install shows
the plugin's own icon and clearing the setting returns to it. External
icons remain unchanged. This projection does
not establish compatibility of an actual plugin's pages or assets.
