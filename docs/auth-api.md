# Authentication API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` surface carries the same auth routes through the pre-1.0 bridge
> window, after which Silo answers the whole `/api/v1` namespace with `410 Gone` and the
> `client_upgrade_required` problem code. See
> [the native API contract](architecture/api-contract.md).

Commands assume the repository root is the cwd. Unprefixed paths are relative to the
server's `/api/v2` base URL.

## Account passwords

A password belongs to a login account, not to an individual household profile. Every profile on an
account therefore shares the same password. Self-service password changes are restricted to the
active primary profile. An admin account may also change its password before selecting a profile,
but selecting a secondary profile removes that authority. API keys and impersonation sessions can
never change an account password.

Local passwords are bcrypt hashes. Setup, invited signup, and password changes share the same
password policy. New passwords must contain at least 8 Unicode characters and be
no more than 72 UTF-8 bytes, the bcrypt input limit. The caller must prove knowledge of the current
password. Changing the password does not revoke existing login sessions; users can review and
revoke those separately through the account sessions API.

Accounts whose local password login is disabled keep their credential at the external provider and
cannot use this flow. An administrator can also make a password temporary or issue a reset link;
see [temporary passwords](#temporary-passwords) and [password reset links](#password-reset-links).

### `GET /auth/account/capability`

Requires an authenticated access-token session. When the request names an active profile, normal
profile and PIN verification applies. Clients should read this endpoint rather than infer support
from a server version.

```json
{
  "schema_version": 1,
  "change_password": true,
  "requires_current_password": true,
  "minimum_password_length": 8,
  "maximum_password_bytes": 72
}
```

`change_password` is true only when the caller is a permitted account owner and the account has
local password login enabled. The remaining fields describe the server contract even when it is
false.

### `POST /auth/account/password`

Requires the same authenticated profile authority as the capability endpoint. The request is
rate-limited separately from login attempts.

```json
{
  "current_password": "existing password",
  "new_password": "replacement password"
}
```

Success returns `204 No Content`.

| Status | Error | Meaning |
| --- | --- | --- |
| `400` | `bad_request` | The body is invalid or a required field is empty. |
| `400` | `invalid_current_password` | The current password did not match. |
| `400` | `weak_password` | The new password contains fewer than 8 characters. |
| `400` | `password_too_long` | The new password exceeds 72 UTF-8 bytes. |
| `403` | `password_change_forbidden` | The active profile is not the primary profile, or the caller is an API key or impersonation session. |
| `409` | `password_login_disabled` | The account does not use local password login. |

The Jellyfin-compatibility listener does not expose password mutation. Jellyfin-compatible clients
continue to authenticate with the account's current local password, while password management stays
on Silo's native API.

### V2 password management

`GET /api/v2/account/password/capability` exposes the same decision through the
common capability fields `revision`, `state`, and `allowed`, alongside the three
password-policy fields above. It supports ETag revalidation and uses
`Cache-Control: private, no-cache`. Selecting a profile requires its normal viewer
and PIN verification, even when the account is an administrator.

`POST /api/v2/account/password` accepts the same password body and returns 204.
It spends the dedicated `password_change` rate-limit budget used by v1; exhausted
requests return `429 rate_limited` with `Retry-After`. Invalid password values
return `422 validation_failed` naming the member. Disallowed account/profile
authority returns `403 permission_denied`, and disabled local password login
returns `409 conflict`. A capability read does not authorize the write: the
server checks the current account and profile again. This credential operation
does not require If-Match.

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

Anything else returns `403 password_change_required` (v1: `403` with error code
`password_change_required`). Refreshing is public and stays available. Such a
session may change the password without selecting a profile. The current
(temporary) password is still required, and reusing it as the new password returns
`422 validation_failed` at `body.new_password`.

After `POST /account/password` succeeds, refresh the tokens: the access token keeps
the restriction until it is replaced, and the refreshed pair no longer carries it.
The change revokes every other session of the account, since each was opened with
the temporary password.
An administrator impersonating the account is not restricted. Jellyfin and
Audiobookshelf compatible sign-ins refuse an account holding a temporary password,
because those clients cannot run the change; the account signs in to Silo first.

### Password reset links

An administrator can issue a single-use link that lets the account holder choose a
new password (see [administrator accounts](admin-users-api.md#passwords)). The link
opens `/reset-password/{token}` in the web client, which uses two public operations:

- `GET /password-resets/{token}` returns `username`, `server_name`, and `expires_at`.
- `POST /password-resets/{token}/complete` with `{ "password": "..." }` sets the new
  password and returns 200.

Every unusable link returns the same `404 not_found`, whether it is unknown, expired,
used, replaced, or outdated by another password change, or its account is disabled.
Invalid passwords return `422 validation_failed` at `body.password` and leave the link
unspent. Both operations spend the `password_reset` rate-limit budget, 20 requests per
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
`/reset-password/{token}` screen as an administrator's link and expires after an
hour. An account gets at most one requested link every five minutes. A requested
link replaces the account's earlier requested link, but never a live link an
administrator issued; while one exists, requests send nothing.

## Email addresses

Every v2 write that stores an account address (`setupServer`, `signup`,
administrator account create and update, and emailed invitations) runs the
same check (`internal/auth.ValidateEmail`): one bare mailbox, no display name
or comments, and a domain containing a dot with text on both sides. A bare
hostname such as `admin@siloserver` is refused with a `422 validation_failed`
problem at `body.email`. The web client applies the same check before sending.
The frozen `/api/v1` routes, and the v1-only per-profile notification address,
keep their previous `net/mail` acceptance.

## Login sessions on v2

`GET /api/v2/auth/sessions` lists the authenticated account's live login sessions, including
sessions on its other devices. Authentication is required; no active profile is needed.
Expired and revoked sessions are excluded before pagination.

The response uses the v2 collection envelope: `items` and `page`. Each item contains `id`,
`device_name`, `ip_address`, `created_at`, and `expires_at`. Timestamps use UTC with millisecond
precision. There is no `revoked_at` member because every returned session is active.

- `limit` defaults to 50 and accepts 1 through 200.
- Results are ordered by `created_at` descending, then `id` descending.
- Pass `page.next_cursor` unchanged as `cursor` to retrieve the next page. The cursor retains
  the full stored timestamp precision and is bound to the account and operation.
- `page.has_more` reports whether another page exists. The last page omits `next_cursor`.
- `offset` and out-of-range limits return `422 validation_failed`; an invalid or mismatched
  cursor returns `400 invalid_cursor`.

`DELETE /api/v2/auth/sessions/{id}` revokes a session owned by the caller's account and returns
`204 No Content`. A missing session or one owned by another account returns `404 not_found`.

The login-session step of the `database_maintenance` scheduled task deletes expired
login-session rows daily at 05:00 by default. Revoked sessions remain stored until their
expiry passes. The v1
session-list response shape and query remain unchanged, but expired rows disappear from that
listing once cleanup deletes them. Jellyfin-compatible clients continue to use the shared login
session validity checks; cleanup removes only sessions that have already expired.

### Access tokens after a role change

An access token carries the account's role, and admin checks trust it. When an
administrator changes the account's role, the login session stays valid, but every
request that presents an access token minted before the change is refused with
`401 token_refresh_required`. The v1 surface answers the same request with `401` and
error code `unauthorized`, which v1 clients already treat as "refresh and retry".

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
answer 401. It answers `503` with `Retry-After`: v2 `dependency_unavailable`, v1 error code
`service_unavailable`. `POST /auth/refresh` (`refreshSession`) answers the same way when it
cannot read the session or account. Whether the session is still valid is unknown, so a
client keeps its tokens, waits `Retry-After` seconds and retries; it signs out only on a
401. The proxy's header-authenticated `/stream/v3` routes answer `503 service_unavailable`
too. The Jellyfin surface keeps its session and answers `503` when its session check or
its stream and HLS authorization cannot read the session from the database, when a due
token refresh cannot reach the database, and when that refresh meets an unreachable
sign-in provider under the `fail_closed` outage policy.

Routes that check credentials outside the shared auth gate, refresh, the proxy media grant
and the Jellyfin session and stream checks may still treat a failed lookup as no session and
answer 401 or refuse the request. Examples are plugin content routes, theme-song grants,
Jellyfin admin API keys, Jellyfin image routes (which fall back to anonymous access), the
Jellyfin websocket, and the v2 socket-ticket check used by the events, playback-control,
watch-together and admin-logs sockets (a watch-together re-check that fails closes the
room). None of them is a token refresh, so a client that signs out only on a refresh 401
stays signed in and retries.

## Device sign-in

A TV opens a request, shows its code and QR link, and polls; a person approves
it where they are signed in. Rules for codes, links and lifetimes are in
[device-login.md](architecture/device-login.md).

| Operation | Credential | Notes |
| --- | --- | --- |
| `POST /auth/device/start` (`startDeviceLogin`) | none | `user_code` is eight digits (`4821-7730`); `verification_uri_complete` is `<base>/activate?code=48217730`, where `<base>` is `server.public_url` when configured; `expires_in` is 900. |
| `POST /auth/device/poll` (`pollDeviceLogin`) | device code | `opened: true` once an approver has looked a pending request up. A pending answer carries the current `expires_at`, which lookups can move later; the device moves its local deadline there. `approved` carries the token pair exactly once. |
| `POST /auth/device/cancel` (`cancelDeviceLogin`) | device code | Withdraws a live pending, or approved but uncollected, request and answers `canceled`; any other request answers its unchanged state (`denied`, `consumed`, `canceled`) or `expired`. Repeating converges. |
| `GET /auth/device?code=` or `?token=` (`getDeviceLogin`) | none | Adds `requested_at`, `server_name` and, when known, `server_id`. A lookup of a pending request marks it opened and holds its code for at least five more minutes, never past 30 minutes after start. |
| `POST /auth/device/approve`, `/approve-handoff`, `/deny` | the account's own login session | A canceled request is refused with 409. An API key or an impersonation session is refused with 403 `permission_denied` (v1: 403 `forbidden`), because an approval gives the device a login session. |
| `GET /auth/device/capability` | none | `cancel` and `opened_signal` report the two additions. |

`status` gains `canceled` on lookup and poll. Start, poll and cancel spend the
device rate-limit buckets; the lookup and all three decisions take a user code
and share the `device_lookup` bucket. The frozen v1 routes have no cancel
operation, report a canceled request as `denied`, keep the 10-minute
`?token=` start answer, and never mark a request opened.

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
| `POST /auth/login` (`login`) | none | Without `provider`, the name is routed: an account with local password sign-in signs in locally (and is refused with `local_login_disabled` while the server switch is off, never sent to the directory), any other name goes to the enabled LDAP plugin. |
| `GET /account/identities` (`listAccountIdentities`) | signed-in account | The caller's linked identities: provider, username, email, `linked_at`, `last_sign_in_at`, and `last_checked_at` (the provider's latest answer about the identity, from a sign-in or a re-check). |
| `POST /account/identities/link-credentials` (`linkAccountIdentityWithCredentials`) | the account's own login session | Links the directory (LDAP) identity. Body, all required and non-null: `installation_id` (the credentials provider from `listAuthProviders`), `password` (the account's local password, 1 to 1024 characters), `username` (directory username, 1 to 256) and `directory_password` (1 to 1024). Answers 201, `Location: /api/v2/account/identities`, and the linked identity as `listAccountIdentities` shows it. Refusals: 422 `validation_failed` at `body.password` (wrong local password) or `body.directory_password` (the directory refused the credentials); 409 `local_password_required`; 403 `not_permitted`, `account_disabled` (the directory account is disabled, locked or expired) or `password_expired`; 403 `permission_denied` (the Silo account is disabled, or an API key or impersonation session); 409 `identity_linked_elsewhere` or `already_linked`; 404 `not_found` (not an enabled credentials provider); 503 `provider_unavailable`; 429. Spends the `login` rate-limit budget. Linking turns local password sign-in off unless the account is break-glass. |
| `POST /account/identities/link-network` (`linkAccountIdentityWithNetwork`) | the account's own login session, through the network provider's overlay | Links the network identity of the requesting device. Body, required and non-null: `installation_id` (the network provider from `listAuthProviders`) and `password` (the account's local password). Answers 201 like `linkAccountIdentityWithCredentials`. Refusals: 403 `network_identity_required`, 422 `validation_failed` at `body.password`, 409 `local_password_required`, 403 `not_permitted`, 403 `permission_denied`, 409 `identity_linked_elsewhere` or `already_linked`, 404 `not_found`, 503 `provider_unavailable`, 429. Spends the `login` rate-limit budget. Unlike other linking, the account keeps its local password sign-in, which answers 403 `not_permitted` while the provider refuses the person (break-glass accounts excepted). |
| `DELETE /account/identities/{id}` (`deleteAccountIdentity`) | the account's own login session | Refused with 409 `last_sign_in_method` unless the account can still sign in with its local password or another identity. API keys and impersonation sessions get 403. |
| `GET /admin/users/{id}/identities` (`listAdminUserIdentities`) | acting admin | Adds `external_subject`, `issuer` and `last_check_status`: what the provider said at `last_checked_at` about the identity, from a sign-in or a re-check (`active`, `not_found`, `disabled`, `not_permitted`, `unsupported`, `unavailable`, or `none` before the first). |
| `POST /auth/refresh` (`refreshSession`) | the refresh token | A session opened through the provider is re-checked with it when due (see below). A refused account ends the session (401 `session_expired`). Under the `fail_closed` outage policy, an unreachable provider answers 503 `provider_unavailable`; the session stays valid and the client retries later. A database that cannot be read answers 503 `dependency_unavailable` with `Retry-After` (see [When the server cannot check a session](#when-the-server-cannot-check-a-session)). A provider answer the server received but could not apply, where a retry would not help, is 401 `invalid_token`: a refusal that could not be applied, a role sync that rolled back, or a replacement refresh token that could not be stored (the provider may accept only that token now). A refusal whose revocation rolled back stays on record and ends the session at the next check, and an unapplied active answer stays due. Any other database failure after an active answer is the retryable 503 above. |
| `POST /admin/users/{id}/identities` (`createAdminUserIdentity`) | acting admin | Links by `installation_id` and the exact `external_subject`. Turns local password sign-in off unless the account is break-glass or the installation is a network identity provider. |
| `DELETE /admin/users/{id}/identities/{identity_id}` (`deleteAdminUserIdentity`) | acting admin | May leave the account without a sign-in method; setting a password with `updateAdminUser` turns its local password sign-in back on. |
| `PUT /admin/users/{id}` (`updateAdminUser`) | acting admin | `break_glass` sets or clears the flag: admin accounts only, and only the server Owner may change it (403 `permission_denied`). The Owner has it by default (set at first-run setup and on every ownership move) and may clear it on its own account. A `password` also turns the account's local password sign-in back on, so an admin other than the Owner may not set its own while its local sign-in is off (403 `permission_denied`; the v1 route answers `owner_protected`). Demoting, disabling or clearing the last usable break-glass admin while local sign-in is off is 409 `break_glass_required`; so is deleting it (`deleteAdminUser`) or turning `auth.local_password_login` off without one (`updateAdminSettings`, `updateAdminSetting`). |
| `POST /admin/plugins/installations/{id}/auth-binding/test` (`testAdminPluginAuthBinding`) | acting admin | Sends staged `config` entries (blank secrets keep the stored value) to the plugin's `TestConnection`; answers `ok`, the plugin's `steps` and `callback_url`, the redirect URI to register at an OAuth provider (empty without a public URL). A plugin without a connection test, or a disabled installation, is 409. Not retried automatically. |
| `PUT /admin/plugins/installations/{id}/auth-binding` (`updateAdminPluginAuthBinding`) | acting admin | Applies without a restart (`X-Silo-Restart-Required: false`). `auto_provision` defaults to true. Enabling a second binding of the same kind is 409 `provider_already_enabled`; one network identity binding may be enabled beside the one OIDC or LDAP binding. |
| `GET /admin/plugins/installations` (`listAdminPluginInstallations`) | acting admin | Each OAuth auth binding carries `callback_url` (the v2 callback to register at the provider) and `post_logout_redirect_url` (`/login`, to register for provider logout), both on `server.public_url`; empty without a public URL and for a password provider. The other operations that answer an installation carry them too. |

Refusals use these problem types: `not_permitted` (403, the provider's
access rules refused the person), `account_required` (403, the provider
admitted the person but the server has no account for them and account
creation is off; v1 answers `not_permitted`), `local_login_disabled`
(403), `password_expired` (403), `email_in_use` (409),
`identity_linked_elsewhere` (409), `provider_already_enabled` (409),
`break_glass_required` (409), `last_sign_in_method` (409),
`provider_unavailable` (503), `network_identity_required` (403, a network
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
the linking operations), `provider_logout` and `select_account` (the starts
accept `prompt=select_account`). Directory linking needs no handshake, so
`getExternalSignInCapabilities` reports it. The flows are raw redirects; the rules behind them are in
[external-sign-in.md](architecture/external-sign-in.md#oauth-flows).

| Operation | Credential | Notes |
| --- | --- | --- |
| `POST /auth/oauth/{install_id}/init` (`initOAuthLogin`) | none | Web form start. `next` is a site-relative return path. On another origin than the public URL it answers 303 to `startOAuthLogin` there. |
| any of the three starts, `?prompt=select_account` | none | Passed to the plugin as the OIDC `prompt`, so the provider lets the person choose another provider account. The web "Switch account" path signs out of Silo, then starts with it. Any other value is 400. Linking flows always use `prompt=login`. |
| `GET /auth/oauth/{install_id}/start` (`startOAuthLogin`) | none | The same start as a link. Sets the browser-binding cookie, then 302 to the provider. On another origin it first answers 302 to itself on the public URL, with `bounce=1`. A `link_ticket` is 400: web linking starts with `startAccountIdentityLink`. Bad parameters are 400 text; a provider that cannot start the flow (plugin not loaded, `InitAuthorize` failed) or a missing public URL sends the browser to `/login?error=oauth_failed&reason=provider_unavailable` (`login_failed` for a server error), which does not redirect to the provider again. `initOAuthLogin` does the same once on the public origin. |
| `GET /auth/oauth/{install_id}/native/start` (`startNativeOAuthLogin`) | none | Apps open it on their saved server base in the system browser with `code_challenge` (S256), `code_challenge_method=S256` and `app_state`, plus `link_ticket` to link. The server records the origin the start arrived on as the flow's start origin. On another origin than the public URL that is not a local one (below) it answers 400 text; on an accepted one it keeps the start on the server and answers 302 to itself on the public URL with only `flow=<ID>` (random, single use, two minutes); an unknown, used, expired or foreign `flow`, or one presented on another origin than the public URL, is 400 text (the latter leaves the start for the public URL). Bad parameters are 400 text. With valid parameters, a provider that cannot start the flow sends the browser to the app redirect with `error=provider_unavailable` (`login_failed` for a server error), `state`, `server` and `iss`. An unknown, used, expired or foreign `link_ticket` sends it to the app redirect with `error=session_expired`, `state`, `server` and `iss`. |
| `GET /auth/oauth/{install_id}/callback` (`finishOAuthCallback`) | the binding cookie | The provider's redirect back, with `code` or `error`. A web flow's redirect to `/login/oauth-complete?code=` also sets the `silo_oauth_complete` cookie on the complete path of each API version (HttpOnly, SameSite=Lax, two minutes). |
| `POST /auth/oauth/complete` (`completeOAuthLogin`) | the completion code | Redeems the code for the token pair and `user`. A native code needs `code_verifier`; a web code refuses one and needs the `silo_oauth_complete` cookie of the browser the callback answered, which that browser sends by itself (400 `invalid_grant` otherwise; the code stays redeemable). The frozen v1 `POST /api/v1/auth/oauth/complete` needs the same cookie for a web code and answers 401 without it. The login session opens when the code is redeemed, so a code that is never redeemed leaves none. An unknown, expired or used code is 401 `invalid_token`, and a second redemption revokes the session the first opened. |
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
accounts only, never an admin, the Owner or a break-glass account),
`auth.provider_recheck_interval` (default `12h`, 5m to 30d: how old the
provider's last answer about an identity may be before a refresh asks again)
and `auth.provider_recheck_outage_policy` (`fail_open`, the default, keeps a
session whose provider cannot be reached; `fail_closed` refuses its refresh
until the provider answers).

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
`getExternalSignInCapabilities` reports `provider_recheck`. The frozen v1
refresh answers a `fail_closed` outage with its usual 401 `invalid_token`.

The frozen v1 routes share the login policy: v1 login answers the same codes,
v1 provider discovery still lists `local`, and the v1 auth binding write also
answers `X-Silo-Restart-Required: false` and 409 `provider_already_enabled`.
The v1 OAuth init sets the same binding cookie and, on another origin than the
public URL, answers 307 to itself there with `bounce=1`. It hands the provider
`<public URL>/api/v1/auth/oauth/<id>/callback` as `redirect_uri`, so a provider
must also register that v1 callback for sign-ins a v1 client starts. v1 takes no link
ticket and no `code_verifier`, so it cannot link or redeem a native code; its
completion also revokes the session of a reused code. The v1 admin user update
and delete keep the break-glass rule (409 `break_glass_required`), and a v1
admin password write also turns the account's local password sign-in back on,
with the same Owner-only rule for an admin's own account.
While local sign-in is off, sending or resending an invitation and accepting
one are refused with 403 `local_login_disabled` on both v1 and v2: an
invitation creates a local-password account, which could not sign in.

## Ordinary v2 authentication

The ordinary v2 auth surface provides login, refresh, logout, provider discovery,
initial setup, invited signup, device pairing, OAuth completion, and account
password management. Login and device-start submissions create fresh durable
state and must not be automatically replayed after an uncertain response.

Invited signup commits invite consumption and account creation together. With the
PostgreSQL profile provider, the optional default profile joins that transaction.
SQLite profile storage remains a separate-store boundary; this does not certify
an atomic cross-store operation or activate backend conversion.

The bundled web client uses the ordinary v2 routes. Its OAuth sign-in buttons
are links to `GET /api/v2/auth/oauth/{install_id}/start` (`startOAuthLogin`);
`POST /api/v2/auth/oauth/{install_id}/init` (`initOAuthLogin`) remains for
form posts. The provider returns to
`GET /api/v2/auth/oauth/{install_id}/callback`. Provider configuration must allow
that callback URI. The frozen v1 handshake remains available during the bridge;
both versions redeem the same one-time completion store through their completion
operation. See [OAuth sign-in flows](#oauth-sign-in-flows).

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
five-minute `HttpOnly` `SameSite=Lax` credential v1 issues, `Secure` on HTTPS,
scoped to the v2 plugin-content parent path `/api/v2/plugin-content` and never
broadened to `/`. The body is `{"expires_in": 300}`. A credential without a login
session, such as an API key, is refused with 403 `permission_denied`; an unknown
declared profile is 404 and a PIN-locked one without its token is 403
`profile_verification_required`. Repeating the request reissues an equivalent
cookie. The bundled web client launches pages under
`/api/v2/plugin-content/plugins/{installation_id}/`. The v2 launch does not expire
the separate legacy cookie; that cookie expires within five minutes. Compatibility
of the reissued cookie against a served auth-provider plugin is not yet proven
and is a follow-up.

Apple and Android adoption must be verified against each client's selected API
contract. Native clients must distinguish failed credential exchanges from an
expired bearer on an authenticated read, retain refresh concurrency protection,
and preserve the selected account and profile during device handoff. The native
migration inventory and client tests track adoption; server/web validation alone
does not establish native cutover or permit v1 retirement.


## V2 policy discovery

`GET /api/v2/policy/capability` reports `enabled`, `editor_available`,
`decision_types`, `generation`, `degraded`, optional `degraded_reason` and
`degraded_domains`, and `eval_timeouts`. An absent policy system returns `200`
with `enabled: false` and the supported decision types. This is discovery, so it
does not require policy-editor authorization.

The route requires authentication and preserves the demo restriction. A profile
is optional; when supplied it must pass viewer verification. The bundled web
policy query uses this endpoint. There are no Apple or Android callers to migrate.
The frozen v1 capability route retains its previous unavailable-system response.

### Public provider icons

Bootstrap generates provider icon URLs as
`/api/v2/plugin-content/plugins/{installation_id}/assets/...`. A capability
manifest may still supply the legacy `/api/v1/plugins/{installation_id}/assets/...`
form; V2 provider discovery projects that onto the versioned mount. Either form is
exposed only when the matching provider installation has a public GET route descriptor. Descriptor selection must use the proxy's exact/wildcard precedence;
prelogin images cannot depend on a launch cookie. Missing public-route proof,
unavailable content, malformed paths or private routes omit the icon without
failing provider discovery. Query strings and fragments are retained. While
the auth plugin's `icon_url_path` setting is unset or empty, the default its
global config schema declares is the button icon, so a fresh install shows
the plugin's own icon and clearing the setting returns to it. External
icons and the frozen v1 provider metadata remain unchanged. This projection does
not establish compatibility of an actual plugin's pages or assets.
