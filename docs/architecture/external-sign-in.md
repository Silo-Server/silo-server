# External sign-in

People can sign in with an account that lives outside Silo: an OIDC identity
provider or an LDAP directory. An `auth_provider.v1` plugin talks to the
provider and returns typed facts about the person; the host owns Silo accounts,
identities, roles, sessions and the local-password policy. The host side lives
in `internal/auth` (`account_resolution.go`, `identity_*.go`,
`local_login_policy.go`, `provider_registry.go`, `provider_recheck.go`,
`oauth_*.go`); the
operations are in `internal/apiv2/external_sign_in.go` and
`internal/apiv2/oauth_handshakes.go`. The operation contract is in
[auth-api.md](../auth-api.md#external-sign-in). The plugin contract is the
SDK's `docs/auth-provider.md`.

## Identity key

- An identity is keyed by (plugin installation, `external_subject`). The
  subject is exactly what the plugin returns: OIDC `iss|sub` (Entra:
  `tid|oid`), LDAP the directory's unique id attribute. The host never
  normalizes it and never matches on a username or email.
- An account holds at most one identity per installation; an identity belongs
  to one account.
- Upgrading a plugin keeps its installation id, so links survive. Uninstalling
  removes the installation and its identity rows; the accounts stay.

## Plugin answers

- `Authenticate` (password) and `ExchangeCode` (OAuth) return
  `AuthenticateResponse`. A `denial` other than unspecified is a refusal,
  checked before the subject: `NOT_PERMITTED` → `not_permitted`,
  `ACCOUNT_DISABLED` → the account-disabled refusal, `PASSWORD_EXPIRED` →
  `password_expired`, `PROVIDER_UNAVAILABLE` → `provider_unavailable`, and
  `INVALID_CREDENTIALS` or an unknown value → invalid credentials.
  `denial_detail` goes to the log only.
- A response without a subject is invalid credentials.
- The host reads only the typed fields. The free-form `claims` Struct is the
  plugin's own; in particular `email_verified`, which gates email auto-match,
  never comes from it. A plugin built before SDK v0.22.0 therefore provides
  the subject, display name and email only, with the email unverified.
- A gRPC `Unauthenticated` is invalid credentials; any other plugin failure,
  including a plugin that cannot start, is `provider_unavailable`. A disabled
  installation accepts nobody.

## Account resolution

One order, shared by OIDC and LDAP, in one database transaction under an
advisory lock on (installation, subject), so concurrent first sign-ins on any
node create one account:

1. **Linked identity.** Sign in its account. A disabled account is refused.
2. **Linking flow** (a signed-in account linking the provider): link to that
   account. An identity linked to another account is refused with
   `identity_linked_elsewhere`.
3. **Email auto-match.** Only when `auth.email_auto_match` is on (off by
   default) and the provider said `email_verified` is true; unset counts as
   false. Link to the account holding that email if it is an ordinary account
   (role `user`, not the Owner, not break-glass) with no identity at this
   installation. An email the provider asserts never hands it an admin: when
   an admin, the Owner or a break-glass account holds the email, creation
   below refuses with `email_in_use`, and an administrator links the account
   explicitly. Silo does not verify account emails (registration, invitations
   and profile edits store what was typed), so the account matched is whichever
   one registered or set that address first, and its holder may not be the
   person signing in. The link therefore ends every credential the provider
   did not vouch for, in the same transaction: login and Audiobookshelf
   sessions, device sign-in approvals and API keys. Unlike a linking flow or
   an administrator link, the old login sessions are revoked, not moved under
   the identity.
4. **Account creation**, when the binding's `auto_provision` is on (default
   on). The plugin has already applied its group rules.
   - Refused with `email_in_use` when any account holds the provider's email
     (as email or username): no duplicate account is created.
   - Username: the provider's username (only the part before an `@`, so a
     Kanidm SPN or an Entra UPN gives the bare name), else the email's local
     part, else the subject (after the last `|`), sanitized, with `_2`, `_3`…
     on collision.
   - Email: the provider's; when it sends none, the reserved placeholder
     `<username>@plugin-<installation>.invalid`.
   - Role `admin` only when the plugin's managed role is ADMIN; otherwise
     `user`, in the default access group.
   - One default profile named after the display name (profile stores that
     cannot join the transaction create the account without one).
   - Local password sign-in off, with an unusable random password.
   - The identity row. Any failure rolls the whole creation back.
5. **Otherwise** `account_required`: the provider admitted the person (its
   group rules passed), but no account is linked or matched and account
   creation is off, so an administrator has to add one. It is a kind of
   `not_permitted`: the frozen v1 login and the Jellyfin login still answer
   `not_permitted`, while the v2 login (problem type `account_required`, 403),
   the web login page and the app redirect (`reason=` / `error=`
   `account_required`) tell it apart. `not_permitted` stays the answer for the
   provider's own refusals (its group rules, `NOT_PERMITTED`,
   `access_denied`).

Linking by any path turns the account's local password sign-in off unless it
is a break-glass account or the identity is a network identity. The OIDC or
LDAP provider then answers for the account, so a password must not outlive
the provider's removal of the person. A network identity keeps the password
and gates it instead ([Network identity](#network-identity)).

## Role sync

At every sign-in and every provider re-check the managed role is
applied: ADMIN promotes, USER demotes, unspecified leaves the role alone. A
change revokes the account's login sessions, like an administrator role
change. The Owner, break-glass accounts that can use their local password,
and the last enabled admin are never demoted; the skip is logged.

## Local-password policy

- `auth.local_password_login` (default on) is the server-wide switch. Off,
  the local provider refuses every account except break-glass admins with
  `local_login_disabled`, after checking the password. The check is in
  `LocalProvider.Authenticate`, so it covers v1, v2, Jellyfin and
  Audiobookshelf compatibility.
- The server Owner is break-glass by default: first-run setup and every
  ownership move (transfer or `silo owner set`) set the flag on the new Owner,
  and a migration set it on the existing one. The previous Owner keeps its own
  flag. The Owner may clear it on its own account, subject to the
  last-usable-account rule below. An Owner whose local password sign-in was
  already off stays off until a password is set.
- `users.break_glass` is admin-only (`users_break_glass_admin`) and only the
  server Owner may set or clear it (403 `permission_denied`), so an admin cannot
  make its own role immune to provider demotion. A role change away from admin
  clears it. For the same reason only the Owner may set the password of its
  own account through account administration while that account's local
  password sign-in is off (403 `permission_denied`; the v1 route answers
  `owner_protected`): the write would turn local sign-in back on, and local
  sign-ins skip the provider's role sync and re-check.
- At least one usable break-glass account (enabled admin with local password
  sign-in) must exist while the switch is off. Turning the switch off, and
  clearing, demoting, disabling, removing password sign-in from or deleting the
  last such account, are refused with `break_glass_required`. Both sides take
  the server-settings advisory lock, so they cannot pass each other's check.
- Recovery from a shell with the server's `DATABASE_URL`:
  `silo auth local-login enable` turns the switch back on;
  `silo auth local-login enable -user <name or email>` turns one account's
  local password sign-in back on (linking turns it off, the Owner's too, so a
  provider outage or removing its plugin would otherwise lock the account
  out), and `-temporary-password` also gives it a random temporary password,
  printed once, that must be changed at the next sign-in, revoking its
  sign-ins.
- An administrator setting an account's password (v1 and v2 admin user
  update) also turns its local password sign-in back on.
- While the switch is off, invited signup and invitation acceptance, which
  create local-password accounts, are refused with `local_login_disabled`
  before the invite is spent, and signup is reported off.
- Every node reads the switch from the database at each decision, so a change
  applies at once everywhere.
- Discovery (`listAuthProviders`) leaves the local provider out while the
  switch is off and reports `password_login`: whether any listed provider takes
  a password.

## Password routing

A password sign-in without a provider (apps, TVs, Jellyfin and Audiobookshelf
clients) is routed by account:

- a name that matches an account with local password sign-in signs in with
  the local provider only, whatever the server-wide switch says (while it is
  off, the local provider refuses a non-break-glass account with
  `local_login_disabled`), so a local password is never sent to a directory;
- any other name goes to the enabled credentials plugin (LDAP), which may link
  or create the account;
- without a credentials plugin, local.

With an OIDC provider, a password sign-in without a provider only reaches the
local provider. A password sign-in that names an OAuth provider fails as wrong
credentials before the plugin is asked, so a password never reaches an OAuth
plugin.

A local password sign-in rechecks the account, password hash and local sign-in
policy while holding the account row lock in the transaction that creates its
login session. Linking or revoking the account cannot leave an in-flight local
sign-in with a new session after its credentials were retired.

Jellyfin's `password#PIN` convention (a profile PIN after the last `#`)
applies to local accounts only: a name routed to the directory gets one
attempt with the password as typed, so a PIN is never sent to the directory
and a failed Jellyfin sign-in counts once toward its lockout. The PIN part
counts toward the profile's PIN lockout shared with `verifyProfilePIN` (see
[Profile PINs](../auth-api.md#profile-pins)). A Jellyfin
sign-in refused by the sign-in policy (`local_login_disabled`,
`not_permitted`, `email_in_use`, `identity_linked_elsewhere`, an expired
directory password) answers 401 `InvalidUsernameOrPassword` with the reason
as its message; a provider that cannot answer is 503 `ServiceUnavailable`.

Audiobookshelf compatibility (beta) signs in local accounts only: its sessions
refresh without the provider re-check below, so directory users are not
routed to their provider there.

## One provider, no restart

- At most one primary auth binding (OIDC, LDAP) is enabled at a time, plus at
  most one network identity binding ([Network identity](#network-identity));
  enabling a second of the same kind is refused with
  `provider_already_enabled` (checked under an advisory lock), and so is a
  second binding from the same installation, because identities and account
  rechecks are keyed by installation. A binding's kind is read from its
  capability's stored `auth_modes`, so it cannot drift from what the plugin
  declares. The built-in local provider always exists.
- The provider registry is rebuilt from the bindings on this node after a
  plugin lifecycle change or a binding write, and on every other node when the
  admin channel announces `plugins_changed` or `auth_providers_changed`. A
  failed rebuild keeps the previous set and is retried after 10 seconds, and
  every node also rebuilds once a minute, so a node that missed an
  announcement (or has no event bus) stops offering a binding an
  administrator turned off within that minute. Announcements that arrive
  while a rebuild is pending collapse into one. Auth binding writes answer
  `X-Silo-Restart-Required: false`.

## Sign-in settings page

The admin Sign-in page shows one sign-in plugin at a time as numbered setup
steps, built from the plugin's manifest so the host needs no per-plugin code
(`web/src/pages/admin-settings/signInSetup.ts`):

- **Steps.** Each global config entry's `admin_form` sections become steps in
  manifest order, titled by the section. Fields outside every section form
  one step titled by the entry. A section marked `collapsible` goes under
  Advanced instead. "Create accounts on first sign-in" (the binding's
  `auto_provision`) closes the last plugin step, so a plugin lists its
  access rules last.
- **Host steps.** OAuth plugins get a first step with the callback and
  post-logout URLs to register. Plugins whose capability sets
  `metadata.connection_test` get a test step after their own. A
  `display_name` entry with a single `value` field becomes the last step,
  Login button, because the server reads it for the login page.
- **Test look-up user.** Capability metadata
  `connection_test_username_field: "<config key>.<field key>"` names a field
  the connection test reads as the person to look up. The page asks for it
  in the test step, lays it over the staged entry for the test only, and
  never saves it.
- **Saving.** Config edits and `auto_provision` save from the page's save
  bar, provider writes before server settings, each through the operation
  it would use on its own. Turning a binding on or off applies at once.
  Turn on stays disabled while a required field (or a required entry
  without required fields) has nothing saved, or while the plugin has
  unsaved edits.

## OAuth flows

The plugin runs the protocol with the provider (PKCE and nonce towards the
IdP, token validation). The host owns the flow: its state, the browser that
runs it, and the handoff to the client. Flow rows live in Postgres
(`oauth_sessions`, `oauth_completions`, `oauth_link_tickets`,
`oauth_pending_links`, `oauth_native_starts`), so any node can serve any
step.

- **Public origin.** The provider always returns the browser to the callback
  on the configured public URL (`server.public_url`). Without one, a v2 web
  start sends the browser to `/login` with `reason=provider_unavailable`, a
  v2 native start with a valid `code_challenge` and `app_state` goes to the
  app redirect with `error=provider_unavailable`, and only the frozen v1
  init answers 409. A start that arrives on another origin (scheme, host and
  port as `clientip.RequestScheme` and `clientip.RequestHost` show it: a
  trusted proxy's `X-Forwarded-Host`, else `Host`) is first redirected to
  the same start on the public origin. A web start is marked `bounce=1` so a
  proxy that hides the origin cannot loop; the v2 form post goes to the GET
  start (303) and the frozen v1 init repeats itself there (307). A native
  start is parked on the server and moves with only a flow ID (see Native
  apps). Refusals that need no browser state (bad installation id, plugin not
  loaded) answer before any redirect.
- **Browser binding.** Every start sets a cookie holding 256 random bits,
  named after the flow's state so parallel flows do not collide, HttpOnly,
  SameSite=Lax (the provider's redirect back is a top-level GET), Secure on an
  https public URL, scoped to the callback path, living as long as the flow
  (10 minutes) plus an hour's grace. The flow stores only its SHA-256. The callback compares in
  constant time; a missing or wrong cookie consumes the flow and answers
  `state_invalid` on the web login page, never the flow's own destination.
  This stops a flow an attacker started from finishing in a victim's browser.
  The callback clears the cookie. It does not cover the web completion code,
  which the callback hands to the browser in a URL; see Completion codes.
- **State.** HMAC-signed with the installation id and expiry, single use
  (consumed at the callback, even when refused).
- **Expired flows.** The flow row and its binding cookie outlive the state
  by an hour (`OAuthExpiredFlowGrace`). A callback with an authentic but
  expired state, in the browser that started the flow, ends at the flow's
  own failure location with `session_expired`: the app redirect for a native
  flow, the `next` path for a web linking flow, the web login otherwise. A
  bad signature, another installation or a missing binding cookie still ends
  on the web login with `state_invalid`.
- **Return path.** `next` is kept only when it is a same-origin path: it
  starts with one `/`, and neither it nor its percent-decoded forms start with
  `//`, contain a backslash or a control character, or parse with a scheme,
  host or user info. Anything else is `/`.
- **Switch account.** A sign-in start may carry `prompt=select_account`,
  which the host passes to the plugin's `InitAuthorize` as the OIDC `prompt`
  so the provider lets the person choose another provider account. The web
  "Not you? Switch account" path signs out of Silo only and then starts with
  it. Any other `prompt` value is refused with 400; linking flows always ask
  for `prompt=login`.
- **Registration URLs.** The administrator registers the callback,
  `<public URL>/api/v2/auth/oauth/<installation id>/callback`, and, for
  provider logout, the post-logout redirect `<public URL>/login` at the
  provider. The admin installation view (`listAdminPluginInstallations` and
  the other operations that answer an installation) shows both on each OAuth
  auth binding (`callback_url`,
  `post_logout_redirect_url`), and the connection test repeats the callback.
  Both are empty while no public URL is configured and for a password
  provider.
- **Provider errors.** An `error` on the callback maps to a reason:
  `access_denied` and the interaction errors to `not_permitted`,
  `temporarily_unavailable` and `server_error` to `provider_unavailable`,
  anything else to `login_failed`.

### Completion codes

A successful sign-in callback resolves the account and stores a completion
code: 256 random bits, only its hash stored, redeemable for 60 seconds. The
row holds what the login session needs (the account, the identity the sign-in
came through, the browser's device name and address) and no tokens. The
session opens only when the code is redeemed: redemption locks the account
before the completion row, rechecks the account and enabled installation,
then locks the linked identity. It creates the session in the same
transaction, marks the code used with that session, and answers the session's
token pair. A code that expires
unredeemed, or whose redemptions are all refused (wrong verifier, another
browser, an account or installation disabled since the callback), therefore
leaves no session behind. The row is kept for 10 more minutes so that a second redemption is
recognized as reuse, which revokes the session the first redemption opened. A
code whose redemption deadline passed is refused without revoking. Linking
flows open no session: a web link is made at the callback, and a native link
when the app confirms it (Native apps).

A code proves its client before reuse or expiry is looked at, so a code that
reaches the wrong client cannot even trigger the reuse revocation. A native
code needs the app's `code_verifier` (Native apps). A web code is bound to the
browser the callback answered: the callback sets the completion cookie
`silo_oauth_complete` (256 random bits; HttpOnly, SameSite=Lax, Secure on an
https public URL, two minutes) on the complete path of each API version, the
code stores its SHA-256, and a redemption without the matching cookie is
refused with `invalid_grant` and leaves the code redeemable. Without it, an
attacker could finish a sign-in in their own browser and send the
`/login/oauth-complete?code=` link to a victim, whose browser would then be
signed in as the attacker (login CSRF). A later web sign-in finishing in the
same browser replaces the cookie, so only the latest code there redeems.
Codes stored before the binding existed have none and are refused.
The database maintenance task (`cleanup_oauth_flows`) deletes expired flows,
codes past that window, and expired link tickets, pending links and parked
native starts.

### Native apps

Apps (iOS, iPadOS, macOS, Android) never register anything at the IdP: the IdP
only sees the server's https callback.

1. The app makes a random `app_state` and a PKCE verifier. It takes
   `native_start_path` from `listAuthProviders`, appends it to its saved
   server base URL, adds `code_challenge` (S256 only) and `app_state`, and
   opens the result in the system browser (ASWebAuthenticationSession,
   Custom Tabs). It never opens another origin. An `oauth` provider without
   `native_start_path` offers no native sign-in.
2. The server records the flow's start origin: the origin the start arrived
   on, derived like the public-origin check (the host a trusted proxy names
   in a single `X-Forwarded-Host`, else the `Host` header, and the scheme of
   the connection or the one a trusted proxy names in `X-Forwarded-Proto`)
   and serialized as `scheme://host[:port]` (lower case, default port
   omitted, IPv6 in brackets, no path).
   - On the public origin, the server stores the flow (challenge,
     `app_state`, start origin), sets the binding cookie and redirects to the
     provider.
   - Any other origin must be one a server outside the local network cannot
     hold: the origin of a connected network access provider, an IP literal
     in the loopback, private, link-local or `100.64.0.0/10` range, or a
     single-label, `.local`, `.lan`, `.localdomain`, `.home.arpa` or
     `.internal` host name. Any other origin is a plain-text 400 (see the
     relay below); such an app has to save the public URL instead.
   - On an accepted other origin (a LAN address, say), the binding cookie
     would land where the callback cannot read it. The server keeps the
     start in
     `oauth_native_starts` (the hash of a random 256-bit ID, the challenge,
     `app_state`, prompt, the account of a consumed link ticket and the start
     origin; two minutes, single use) and redirects the browser to the native
     start on the public origin with only `flow=<ID>`. That start deletes the
     row and opens the flow from it. Nothing that names the start origin, the
     challenge or `app_state` travels in the browser, so no page on the way
     can change the recorded start origin. An unknown, used, expired or
     foreign ID is a plain-text 400. So is an ID presented on any origin but
     the public one, where the flow's binding cookie could not reach the
     callback; that refusal leaves the start in place for the public origin.
3. The callback finishes the sign-in and redirects to the fixed app URI
   `org.siloserver.silo:/auth/callback` (RFC 8252 private-use scheme; the
   client never supplies a redirect) with `code`, `state`, `server` (the
   server's `server_id`) and `iss`, or `error` on failure. `iss` is the
   flow's start origin. Every app redirect of a sign-in or linking flow
   carries it, success or failure, including a start that fails before the
   provider.
4. The app checks `state`, requires `iss` to equal its saved server base's
   origin, serialized the same way (otherwise it discards the code), checks
   `server`, then redeems the code with its `code_verifier` at its saved
   server base. A native code without the right verifier is refused with
   `invalid_grant` and stays redeemable, and the verifier is checked before
   reuse is detected, so a redirect intercepted without it cannot revoke the
   app's session; a web code refuses a verifier. Only the one-time code
   travels in the app redirect, never a token.
5. The app never moves, re-keys, duplicates or switches its saved server
   because of a sign-in: a server saved on its LAN address signs in and stays
   on that address.

The start origin narrows a relay; it does not close it everywhere. A
hostile server saved in the app can pass the app's start, with its
`code_challenge` and `app_state`, to the real server's native start. The
real server's callback would hand the app a valid code, `state` and
`server` (which the hostile server can echo), and an app that redeemed at
its saved address would post the code and verifier to the hostile server.

- A relay by redirect fails: the browser reaches the real server on one of
  the real server's own origins, which the real server records and names in
  `iss`; that is not the hostile origin the app saved, so the app discards
  the redirect.
- A relay by request is the residual. The `Host` header (or a trusted
  proxy's `X-Forwarded-Host`) is chosen by whoever sends the start, so the
  hostile server can send the start to any listener of the real server
  itself with its own origin as `Host` and hand the browser the real
  server's redirect to the public origin. The real server then names the
  hostile origin in `iss`. The server therefore records a start origin
  other than the public URL only when a server outside the local network
  cannot hold it (step 2). What remains open is a hostile server on such a
  local address, or a network access provider origin, that the person
  saved in the app.

Checking `Sec-Fetch-*` or the referrer at the start would not help, because
the redirect chain is a normal top-level navigation.

A reverse proxy in front of a native start must pass the host the app used,
either as `Host` or, as a trusted proxy, in `X-Forwarded-Host`, and, when it
terminates TLS, be a trusted proxy that names the scheme
(`X-Forwarded-Proto`). Otherwise the start origin differs from the app's
saved address and the app refuses the redirect, or the start is refused
because its origin is not a local one. The server logs the recorded start
origin of a parked start at debug level and a refused origin at info
level.

### Linking

A signed-in account links the provider after re-entering its local password:
`createAccountIdentityLinkTicket` issues a single-use ticket (256 bits, only
its hash stored, 5 minutes, bound to the installation and the account).
Linking must be finished by the account that asked, never by whoever opens a
URL, or an attacker could request a ticket for their own account and get a
victim to sign in at the provider (the Vaultwarden CVE-2026-47158 class):

- **Web.** The web client hands the ticket to `startAccountIdentityLink`, an
  authenticated call on the public origin. It consumes the ticket (another
  account's ticket is refused and burned), stores the flow for that account
  and sets the binding cookie on that response, then answers the provider's
  authorize URL for the same browser to open. Only the browser that holds
  the cookie can finish the flow; the GET starts refuse `link_ticket`.
- **Native.** The system browser that runs an app's flow holds no bearer
  token, so the app passes the ticket as `link_ticket` to the native start.
  The callback links nothing: it parks the provider's answer
  (`oauth_pending_links`: the code's hash, the account, the app's S256
  challenge, the answer encrypted like completion tokens) and sends the
  browser to the app redirect with `code`, `link=1` and `iss`. The app
  checks `iss` like a sign-in's and confirms with
  `completeAccountIdentityLink` at its saved server base within 60
  seconds, presenting the code, its `code_verifier` and its own bearer token;
  the link is made only when both fit and the bearer's account is the
  ticket's. A wrong verifier leaves the code redeemable; another account's
  attempt removes it. A start off the public origin consumes the ticket
  before it moves, and the parked start keeps the ticket's account. An
  unknown, used, expired or foreign ticket sends the browser to the app
  redirect with `error=session_expired`.
- Linking flows ask the provider for a fresh sign-in (`prompt=login`), so an
  existing provider session in the browser is not silently reused.

The identity is linked through the resolution order above (an identity held
by another account is `identity_linked_elsewhere`, an account already linked
at this installation is `already_linked`). A linking flow opens no session:
the web returns to `next` with `linked=1`. Accounts without local password
sign-in cannot get a ticket.

Only OAuth installations take part: a credentials (LDAP) installation gets no
ticket and no flow (404).

### Directory linking

A credentials (LDAP) installation has no browser flow, so an account links
its directory identity in one call, `linkAccountIdentityWithCredentials`
(`POST /account/identities/link-credentials`, from a login session only).
The body carries the account's local password and the directory username and
password. The host checks the local password first, so a wrong one never
reaches the directory, then calls the plugin's `Authenticate` and links the
identity it answers for through the resolution order above, as a linking
flow: the same `identity_linked_elsewhere` and `already_linked` refusals, the
provider's managed role, local password sign-in turned off unless the account
is break-glass, and the `identity_linked` audit event. The plugin's denials
keep their sign-in meaning, except that a disabled directory account is
`account_disabled`, distinct from a disabled Silo account. The call spends
the login rate-limit budget. `getExternalSignInCapabilities` reports it as
`credentials_linking`. An administrator can also link any identity by its
subject (`createAdminUserIdentity`), and email auto-match links on sign-in.

## Network identity

A network access provider plugin ([network-access.md](network-access.md))
already knows who owns the device behind every request it proxies: Tailscale
answers that with WhoIs. When the plugin also declares an `auth_provider.v1`
capability with the `network` auth mode, people on its overlay sign in with
no password and no browser, which is what makes a TV sign in with one button.
The host side is `internal/auth/network_sign_in.go`; the plugin contract is
the SDK's `NetworkIdentityAuth`.

- **Where the peer comes from.** The plugin stamps `X-Silo-Ingress-Peer`
  (the overlay peer's IP) next to its ingress token. `netaccess.Middleware`
  strips the header from every request and records the peer on the access
  path only when the token is valid, so a LAN or public client cannot name
  one. The path also records the installation whose token it was.
- **Who the peer is.** The host never decides that itself. It asks the
  plugin (`AuthenticatePeer`) about the peer of the request in hand, and only
  when the request came through that installation's own listener; any other
  request is `network_identity_required`. The plugin answers like
  `Authenticate`: a stable subject for the person (Tailscale: control host
  and user ID), names, and `managed_role` when the overlay's policy sets the
  Silo role, or a denial (an unknown or tagged device, one its policy leaves
  out).
- **Sign-in** (`signInWithNetworkIdentity`,
  `POST /auth/network/{id}/sign-in`) runs the answer through the account
  resolution order above with the binding's `auto_provision`, then opens a
  login session vouched for by the identity under the same account, identity
  and installation locks as an OAuth completion
  (`Service.OpenIdentitySession`). The body is an empty JSON object: the
  media-type rule then refuses a cross-site form post, so a page someone
  visits cannot sign their device in.
- **Discovery.** `listAuthProviders` lists a network provider only to a
  request from its own overlay whose peer the plugin vouches for, with
  `network_sign_in_path` and the owner's name for a "Continue as" label.
  Answers about a peer (identity or refusal) are cached for 30 seconds per
  provider instance and peer; a binding or plugin configuration change builds
  a new instance, so a changed rule applies at once. Discovery waits at most
  2 seconds for the plugin, and concurrent lookups for one peer share one
  call; a plugin that cannot answer in time is left out and not cached.
  Sign-in
  and linking always ask again. A network provider is never the default
  provider, never counts toward `password_login`, and never receives a
  password: login and Jellyfin routing skip it, a login that names it fails
  as wrong credentials, and the frozen v1 provider list leaves it out.
- **Linking** (`linkAccountIdentityWithNetwork`,
  `POST /account/identities/link-network`) works like directory linking: the
  account re-enters its local password, then the peer's identity is linked as
  a linking flow. The owner of an existing account uses it, because a first
  network sign-in would otherwise meet `email_in_use`. Unlike directory
  linking, the account keeps its local password sign-in and its open local
  sessions stay local: the password still works where the overlay does not
  reach (a public URL, Jellyfin-compatible apps). An account that network
  sign-in created has no usable password until an administrator sets one.
- **The overlay still gates the password.** While an enabled network
  provider's latest answer about the account's identity is a refusal
  (`networkRefused`, `pending_refusal` included), every password surface
  refuses the account with `not_permitted`, after checking the password;
  break-glass accounts are exempt. The refusal itself ends every session and
  API key of the account, as for any provider that answers for it. The block
  lifts only when the provider vouches again, at a network sign-in or an
  active re-check: an unavailable or unsupported answer keeps a refusal on
  record as the identity's status (`recordIdentityCheck`);
  the scheduled pass re-checks the network identity of an account with local
  password sign-in on even when it holds no credential to bound, so a person
  removed from the overlay is found and one added back is let in. It is not
  the account's disabled flag, which stays the administrator's. To let a
  person keep only their password, unlink the network identity; turning the
  network provider off also lifts every block.
- **Re-check** is the ordinary provider re-check: the plugin answers
  `CheckAccount` from its own view of the overlay (Tailscale: the person
  still has an untagged device in the node's peer list, and still holds a
  required grant). A person removed from the overlay cannot reach its
  listener at all; a server that also has a public URL ends their sessions at
  the next re-check.
- **The primary provider comes first.** One network binding may be on beside
  the one OIDC or LDAP binding, and an account can hold an identity at each.
  When it does, the primary provider is the account's authority
  (`primaryAuthorityOf`). The network identity's `managed_role` is ignored at
  sign-in, linking and re-check. A refusal from the network provider ends only
  the sessions opened through the network identity (as for a break-glass
  account), and a network provider that cannot re-check removes nothing,
  keeping the account's API keys and the sessions the primary provider vouches
  for. While the primary provider's latest answer refuses the account, network
  sign-in is `not_permitted`, so the overlay cannot undo the primary
  provider's deprovisioning. A refresh of a network session re-checks only
  the network identity, so the scheduled pass re-checks the primary identity
  of an account with live network sessions. Otherwise the two providers would each apply their
  own role and sign the account out at every change.
- **No email matching.** The host drops `email_verified` from a network
  provider's answer, so a network identity never links to an account by email
  auto-match: whoever uses a device is its owner, which proves nothing about
  an email address.
- **Trust.** Anyone who controls a device signed in to the overlay as a
  person can sign in to Silo as that person, with that person's role, within
  what the overlay's policy allows them, and can link that person's identity
  to their own account. Everyone using a shared TV signs in as whoever signed
  that TV in to the overlay; profiles separate viewers. The same holds for
  anyone whose traffic reaches the node through a person's device: a reverse
  proxy, Tailscale Serve, or a router that masquerades its LAN into the
  overlay makes everyone behind it that device's owner. The Tailscale plugin
  names no peer for a request carrying forwarding headers, but a plain TCP
  forwarder adds none, so relays must be tagged devices, which never sign in.
  Subjects are scoped to the control plane, not the tailnet: moving the Silo
  node to another tailnet gives everyone new user IDs, so re-checks of the
  old identities answer not found.

`getExternalSignInCapabilities` reports both operations as
`network_sign_in`.

### Provider logout

The Silo logout never waits long for the provider: the server bounds the
plugin lookup at 2 seconds (answering empty past it), and the web client
bounds its wait for the answer before it sends `logout`. `getProviderLogout`
answers empty for an API key or an impersonation session, so an
administrator viewing as someone neither ends that person's provider session
nor carries their ID token; the web logout skips the provider while
impersonating. Otherwise it asks the
plugin (`AuthProviderChecks.EndSessionUrl`) for the end-session URL of the
account's identity at the enabled OAuth provider, with `<public URL>/login` as
the post-logout redirect. The plugin's own setting is the only switch: the
OIDC plugin answers empty unless its operator turned `provider_logout` on.
The web client calls it before `logout` and navigates there afterwards. Only an absolute http(s) URL is passed on; a plugin that
predates the RPC (Unimplemented), a failure or no identity answers empty.
The request carries the identity's stored `refresh_state`, so an OIDC plugin
that keeps the ID token there can add `id_token_hint`.

## Provider re-check

Refresh slides a login session's expiry, so without a re-check a person the
provider removed would keep Silo access for as long as a device keeps
refreshing. The host therefore asks the provider again.

- **Which sessions.** `auth_sessions.identity_id` names the identity a
  session came from: an OAuth or LDAP sign-in (including compatibility
  password sign-ins routed to LDAP), a device sign-in approved
  from such a session (`device_login_requests.approved_identity_id`), and the
  open sessions of an account when an identity is linked to it, unless the
  account is break-glass. Local and impersonation sessions carry none.
  Unlinking the identity clears it (`ON DELETE SET NULL`).
- **Absolute age.** `auth_sessions.provider_since` is when the provider last
  vouched for the chain a session belongs to: the provider sign-in that opened
  it, the approving session's value for a device sign-in
  (`device_login_requests.approved_provider_since`), or the session's own
  creation when a link attached it. When the provider cannot re-check the
  identity, and when the identity is gone (self or administrator unlink,
  plugin removal), the session stops sliding and ends
  `auth.refresh_token_expiry` (default 30 days) after `provider_since`.
  Chaining device sign-ins therefore cannot extend access past the
  approver's limit. A remote-playback handoff approval records the
  approver's chain the same way. Only a login session may approve a device: an API key
  would turn a credential no refresh re-checks into a refreshing session,
  and an impersonation session would hand the device a session with no trace
  of the impersonator, so both are refused (v2 403 `permission_denied`, v1
  403 `forbidden`). See [device-login.md](device-login.md#who-decides).
- **When.** At refresh, when the identity's last answer is older than
  `auth.provider_recheck_interval` (default 12 h), or the last attempt could
  not reach the provider at least 2 minutes ago. Refreshes in between use the
  stored answer (the outage policy below), so an outage does not make every
  refresh wait on the provider. A sign-in through the provider counts as an
  answer (`active`), so the first re-check comes one interval after it. A
  node runs at most 8 re-checks at once, each holding a database connection
  while it waits; a refresh that finds no free slot counts the provider as
  unavailable without storing it.
- **Scheduled pass.** Some credentials act as the account without a refresh
  to trigger the re-check: API keys, Audiobookshelf sessions, and login
  sessions opened through the identity that sit idle. The task
  `recheck_external_identities` (hourly by default) asks the provider about
  every identity whose last answer is due by the same rule, whose account
  still holds one of those credentials and is not break-glass, and whose
  installation is an enabled sign-in provider. It applies the same answers
  as a refresh. Every node runs the task manager, so a Postgres advisory lock
  lets one node run the pass while the others skip it, and each check still
  takes the identity's advisory lock and re-reads whether it is due, so a
  pass never asks twice what a refresh on another node just asked. The pass
  waits for a free re-check slot instead of counting the provider as
  unavailable. A pass with nothing due records no run. For an identity the
  provider cannot re-check (`UNSUPPORTED`), the pass applies the same bound
  as a refresh (below): it revokes the API keys and Audiobookshelf sessions
  once the person has not authenticated through the provider for the
  absolute age. It skips an identity whose last answer was `UNSUPPORTED`
  when its account has local password sign-in turned on, since that bound
  does not apply to it. Nobody can be asked about an identity of an
  installation that is no longer an enabled sign-in provider, so for those
  the pass only applies that bound, without asking or storing an answer:
  once the person has not authenticated through the provider for the
  absolute age, the account's API keys and Audiobookshelf sessions end.
  Uninstalling the plugin deletes its identities (`ON DELETE CASCADE`), so
  after an uninstall nothing bounds the API keys and Audiobookshelf
  sessions of its accounts.
- **How.** The host loads the plugin first, bounded by 10 seconds and
  outside any lock, since loading may start the plugin process. It then
  calls `AuthProviderChecks.CheckAccount` with the subject
  and the stored `refresh_state`, bounded by 10 seconds, detached from the
  request so a client hanging up cannot lose a refresh token the provider
  already rotated. One acquired connection holds the identity's session
  advisory lock (the key sign-in locks), so at most one check per
  (installation, subject) is in flight across nodes; a refresh queued behind
  it waits up to 15 seconds. If that check finishes in time, the queued
  refresh uses its answer; otherwise it counts the provider as unavailable
  (the outage policy below). A nonempty replacement `refresh_state` commits
  on the held connection before waiting for account or identity locks,
  because the provider may already have spent the stored token. Before
  applying the answer, its transaction locks the account and then the
  identity and checks that the
  identity still exists. An unlink that completed during the provider call
  makes its answer obsolete. For an answer that revokes credentials (a
  refusal, or `UNSUPPORTED` past the bound), the answer and a
  cleared state are written in the same savepoint as the revocation: if the
  revocation fails, they roll back with it, nothing is revoked and the
  refresh fails. A refusal whose revocation rolled back, or whose account
  or identity lock failed, is kept as the identity's `pending_refusal`, and
  the next check (refresh or scheduled)
  applies it without asking the provider again, since a plugin may already
  have spent or dropped the refused token (the OIDC plugin answers a refused
  token with a state that keeps only the ID token and `sub`, which is
  committed before the answer transaction), and could then only answer
  `UNSUPPORTED`.
  A sign-in through the provider, or any recorded answer, clears it. For
  any other answer, the answer is written before the savepoint too, so a
  failed role sync cannot lose it. An `ACTIVE` answer whose role sync fails
  clears `last_checked_at` to keep the check due, including after a short
  outage retry. The next refresh asks again and applies the role.
- **Lost answers.** The rotated state can still be lost after the plugin
  spent the stored token at the provider: the call fails in transport, the
  node or the plugin dies, or saving its replacement state fails. Before each
  call the host commits `check_outcome_unknown` on the identity in its own
  statement on that same connection, before opening the answer transaction.
  Saving replacement state clears it. An answer transaction also clears it
  when the plugin presented its current state: `ACTIVE`, `NOT_FOUND`,
  `DISABLED` or `NOT_PERMITTED`. A sign-in that stores a new `refresh_state`
  clears it too. An `UNAVAILABLE` or
  `UNSUPPORTED` answer without a state, or a check that never reached the
  plugin, leaves it set. While it is set, the next `NOT_FOUND`, `DISABLED`
  or `NOT_PERMITTED` of a call that presented a stored `refresh_state` may
  be the provider refusing a token the lost call already rotated, so it is
  applied as `UNSUPPORTED`. When that answer omits replacement state, the
  uncertainty remains for later checks of the same stored token. The OIDC
  plugin drops a refused token from its
  state, so that account then answers `UNSUPPORTED` until its next sign-in.
  A call without a stored state (LDAP checks the directory without one)
  presented no token a lost call could have rotated, so its refusal revokes
  at once.
- **Answers.** `last_checked_at` and `last_check_status` record every one;
  administrators see both on the account's identities.
  `last_authenticated_at` records when the provider last vouched for the
  person: an interactive sign-in (or the linking sign-in that created the
  identity) or an `ACTIVE` answer. An identity an administrator linked
  without a sign-in counts from `linked_at` until then. Identities linked
  before the column existed count from the upgrade, so the bound below
  cannot revoke credentials at the first scheduled pass after it.
  - `ACTIVE`: the provider's details and managed role are applied (role sync
    above). A role change revokes the account's sessions, the refreshing one
    included. An empty (or blank) issuer, username, email or display name
    means the provider did not say: the stored value stays, so a provider
    that answers with the subject only never erases what a sign-in stored.
    The picture URL is not stored.
  - `NOT_FOUND`, `DISABLED`, `NOT_PERMITTED`: every login session of the
    account is revoked (the account-wide revocation, which also drops
    Audiobookshelf sessions, withdraws device approvals and clears cached
    compatibility sessions on every node), its API keys are deleted (they act
    as the account with no session to re-check), and the refresh is refused.
    The account is not disabled; a later sign-in the provider accepts works.
    For a break-glass account only the sessions opened through the identity
    end: its local sessions and API keys stay, since a break-glass account
    keeps local credentials independent of the provider (a link does not
    attach them and the scheduled pass skips it). Audit event
    `recheck_revoked`.
  - `UNSUPPORTED`, or a plugin that answers `Unimplemented` or has no
    `CheckAccount`: stored; the identity's sessions stop sliding (absolute
    age above). API keys and Audiobookshelf sessions have no such bound, so
    when `last_authenticated_at` is older than `auth.refresh_token_expiry`
    (default 30 days) the account's API keys are deleted and its
    Audiobookshelf sessions revoked, as for a refusal, while its login
    sessions (and its Jellyfin compatibility sessions) keep their
    absolute-age bound. Audit event `recheck_credentials_revoked` (with the
    counts), written only when something was revoked. People who sign in
    through the provider at least that often keep their keys. The bound
    skips an account with local password sign-in turned on (for example
    after an administrator set its password): that
    person signs in without the provider, and password sign-ins through
    Jellyfin and Audiobookshelf go to the local provider, which never
    stamps `last_authenticated_at`.
  - This node has no provider for the installation: nothing is stored, since
    the answer is the node's, not the provider's. The bindings every node
    shares decide this refresh: an installation that is no longer an enabled
    provider stops the sliding and applies the `UNSUPPORTED` bound on API
    keys and Audiobookshelf sessions; one that is enabled but not loaded
    here (a node that has not rebuilt its providers yet) counts as
    unavailable. A
    plugin this node cannot load (its process fails to start) is the node's
    failure too: nothing is stored, the refresh counts the provider as
    unavailable, and the scheduled pass counts the check as `failed`.
  - `UNAVAILABLE`, `UNSPECIFIED`, an unknown value, any other error, a
    timeout, or a lock wait that runs out: with
    `auth.provider_recheck_outage_policy` `fail_open` (default) the session
    slides as usual and the next refresh asks again; with `fail_closed` the
    refresh is refused without revoking the session (v2 503
    `provider_unavailable`, v1 401 `invalid_token`; a Jellyfin compatibility
    session whose refresh fails is dropped).
  - The response's `external_subject` and `denial` are ignored.
- **refresh_state at rest.** Stored per identity in
  `plugin_auth_identities.refresh_state`, AES-GCM encrypted with the server
  secret and bound to the row (`plugin_auth_identities:refresh_state:<id>`).
  An unset `refresh_state` keeps the stored value, an empty Struct clears it.
  A value that no longer decrypts is logged and sent as none; an OIDC plugin
  then answers `UNSUPPORTED`.

## Audit

Link, unlink, account creation, role change and skipped demotion are written
to the operational log with `component=auth` and an `audit_event` attribute
(`identity_linked`, `identity_unlinked`, `identity_link_started`,
`account_created`, `role_changed`, `role_change_skipped`, `recheck_revoked`,
`recheck_credentials_revoked`),
with the installation, account and, for administrator actions, the acting
account. A role change from a re-check carries `method=recheck`.
