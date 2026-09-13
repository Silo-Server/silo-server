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
cannot use this flow.

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

The `cleanup_auth_sessions` scheduled task deletes expired login-session rows at startup and
once every 24 hours by default. Revoked sessions remain stored until their expiry passes. The v1
session-list response shape and query remain unchanged, but expired rows disappear from that
listing once cleanup deletes them. Jellyfin-compatible clients continue to use the shared login
session validity checks; cleanup removes only sessions that have already expired.

## Ordinary v2 authentication

The ordinary v2 auth surface provides login, refresh, logout, provider discovery,
initial setup, invited signup, device pairing, OAuth completion, and account
password management. Login and device-start submissions create fresh durable
state and must not be automatically replayed after an uncertain response.

Invited signup commits invite consumption and account creation together. With the
PostgreSQL profile provider, the optional default profile joins that transaction.
SQLite profile storage remains a separate-store boundary; this does not certify
an atomic cross-store operation or activate backend conversion.

The bundled web client uses the ordinary v2 routes. Browser OAuth initiation uses
`POST /api/v2/auth/oauth/{install_id}/init` and the provider returns to
`GET /api/v2/auth/oauth/{install_id}/callback`. Provider configuration must allow
that callback URI. The frozen v1 handshake remains available during the bridge;
both versions redeem the same one-time completion store through their completion
operation.

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
failing provider discovery. Query strings and fragments are retained. External
icons and the frozen v1 provider metadata remain unchanged. This projection does
not establish compatibility of an actual plugin's pages or assets.
