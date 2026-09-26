# Administrator accounts and access groups

The v2 administrator routes require an authenticated acting administrator and
retain the demo write guard. Scoped API keys retain their existing account and
access-group grants; they cannot impersonate accounts. A household primary
profile is not a server administrator.

`GET /api/v2/admin/users/capabilities` reports account management, guarded
configuration, transactional default-profile creation, and access-group support.
Unsupported services return a capability or dependency Problem Details response.
The existing paginated account list remains at `GET /api/v2/admin/users`.

## Account editor

`GET /api/v2/admin/users/{id}` returns the canonical account editor and an ETag.
The tag binds the acting account/profile, account revision, and inherited group
configuration revision. Conditional reads support `If-None-Match` and 304.

`PUT` and `DELETE` of that resource require `If-Match`: missing guards return 428;
stale guards return 412. The account revision check, configuration change, and
revocation of affected direct and impersonation login sessions commit in one
transaction. A failed write rolls back all three. Existing account writers also
advance the revision. After a successful update (204), fetch the canonical editor
again before editing further. Deletion returns 204.

Omitted update fields preserve their values. Nullable policy overrides accept
`null` to restore inheritance. Explicit empty library and permission arrays,
`false`, and zero concurrency limits retain their distinct meanings. Account
identity and ordinary boolean fields reject null.

`max_remote_stream_bitrate_kbps` and `max_local_stream_bitrate_kbps` are separate
nullable account overrides. `null` inherits the corresponding access-group
value; `0` explicitly allows unlimited bitrate. Both access-group fields
default to `0`. Values are nonnegative integers in kbps. A policy edit changes
new playback sessions, not streams already playing.

`POST /api/v2/admin/users` returns 201 with `{ "id": "..." }` and `Location`.
Default-profile creation, when requested, uses the existing transactional
provisioner. Unsupported transactional profile storage fails before account
creation. Username/email conflicts return 409.

Account listing accepts `GET /api/v2/admin/users?identity=...` for an exact,
case-insensitive match against either username or email. Surrounding whitespace
is trimmed; partial matches, wildcard expansion and mailbox-provider aliases
are not applied. Matching runs in the database before pagination and includes
disabled and administrator accounts. Omit the filter to retain the full listing.
The continuation cursor binds the filter and acting account; changing either
requires starting a new listing. Account capabilities advertise
`exact_identity_filter`. Existing `admin:users` keys may use this read.

An identity lookup does not reserve an identity or authorize a change. Conditional
account updates and database uniqueness remain authoritative at write time.

## Passwords

Account editor and list rows carry `password_login` and `password_change_required`.
`password_login` is false when an external authentication provider manages the
account's sign-in. Password actions do not apply to such an account, and clients
hide them. `password_change_required` is true while the account holds a temporary
password.

Create and update accept `require_password_change` to make the password in the same
request temporary. At its next sign-in the account must choose a new password before
its session can do anything else (see
[temporary passwords](auth-api.md#temporary-passwords)). The flag is only valid
alongside `password`; sending it alone returns `422 validation_failed` at
`body.require_password_change`. An account without local password sign-in cannot
hold a temporary password; updating one with the flag returns `409 conflict`. A
password sent without the flag is not temporary and clears a pending change. Setting a password still revokes the account's login
sessions.

`POST /api/v2/admin/users/{id}/password-reset` issues a password reset link, so an
administrator can help a locked-out account without handling its password. The body
is `{ "delivery": "email" }` or `{ "delivery": "link" }`, and the response is 201:

| Member | Meaning |
|--------|---------|
| `delivery` | The requested delivery. |
| `delivery_status` | `sent` or `failed_or_unknown` for email; `not_requested` for a link. |
| `reset_url` | For `link` only: the link itself, disclosed once. |
| `expires_at` | When the link stops working, 24 hours after issue. |

`email` sends the link to the account's address and never returns it. When the mail
server does not confirm delivery, the link is still live: send again or create a link
instead. `link` returns the URL for the administrator to share with the account
holder. The URL is a bearer credential for the account's password until it expires.

An account holds at most one live link: issuing a new one replaces the old. The link
works once. Any other password change also retires it. The public side of the flow
is described in [password reset links](auth-api.md#password-reset-links).

| Condition | Result |
|-----------|--------|
| No such account | `404 not_found` |
| External provider manages sign-in, account disabled, or no email address for `email` | `409 conflict` |
| Email not configured for `email` | `409 capability_not_configured` |
| No server public URL (`server.public_url`) | `409 capability_not_configured` |

Account capabilities advertise `password_reset_link` (a public URL is configured)
and `password_reset_email` (email is also configured). The operation is not
retryable: each call replaces the previous link. The request log records each issue
against the acting administrator and target account. Scoped `admin:users` keys do
not reach this route, and the handler refuses a scoped key for an administrator
account in any case.

`POST /api/v2/admin/users/{id}/impersonate` returns the shared token-pair contract.
It creates a login session and has no replay identity. Clients must not retry an
uncertain response automatically. The web client checks its captured authority
before installing returned credentials.

## Server Owner

The account created by first-run setup is the server Owner. On a server set up
before the Owner existed, the earliest-created enabled administrator became the
Owner; a server with no enabled administrator at that point has none. There is at
most one Owner, and `is_owner` in the account projection marks it.

Only the Owner may act on the Owner's account. Other administrators receive
`403 permission_denied` when they update, delete, or issue a password reset for
it, when they create an API key for it, or when they change or revoke one of its
keys. The v1 account and API key routes answer the same refusals with
`403 owner_protected`. The Owner may not demote, disable, or
delete itself; those writes return the same 403. Ownership cannot be
transferred yet. Nobody may impersonate the Owner. Administrators may still
impersonate only non-administrators, but the Owner may also impersonate other
administrators.

## Access groups

`/api/v2/admin/access-groups` supports bounded list and create operations; the
`/{id}` resource supports canonical GET, guarded PUT, and guarded DELETE. Create
returns 201 and Location. Update returns the new canonical representation and
ETag; delete returns 204. Missing/stale guards return 428/412.

Group configuration changes advance a monotonic revision, including default-group
changes. Canonical editor responses exclude changing membership counts; list
responses include them. Group changes and account policy propagation share the
same transaction and lock order. Group and account writes have no automatic
response replay; after an uncertain result, reload before starting another intent.

## Account projections

| Route below `/api/v2/admin` | Response |
| --- | --- |
| `GET /users/{id}/profiles` | Complete profile collection with string IDs and names |
| `GET /users/{id}/api-keys` | Bounded metadata-only key collection; no stored credential |
| `GET /users/{id}/ips` | Bounded IP history for one account |
| `GET /ips?ip=...` | Bounded account history for one valid IP address |
| `GET /users/{id}/settings/values` | Bounded setting-value collection and contract revision |
| `PUT /users/{id}/settings/values/{key}` | Validated setting value |
| `DELETE /users/{id}/settings/values/{key}` | 204 |

Paginated reads accept `limit` (up to 200) and signed `cursor`; responses expose
`items` and `page`. Cursors bind the actor/profile, target, filters, and page size.
IP queries accept `days` from 1 to 365, defaulting to 30. Their cursor preserves a
fixed observation window and deterministic last-seen ties. Newer events do not
move earlier groups across that cursor. Addresses are returned without CIDR masks.

Administrator settings use explicit target identity query parameters: `scope`,
`profile_id`, `client_family`, `device_id`, `library_id`, and `series_id` as required
by the setting scope. They never substitute the acting administrator's profile.
Both PostgreSQL and SQLite paginate by the full setting identity. Writes reuse
existing validation, normalization, mirror updates, and last-write semantics;
see [settings API](settings-api.md). Reset `nav.shortcuts` with PUT of its empty
document; DELETE is rejected. The web bulk-reset flow reports partial completion.

These administrator projections do not alter Jellyfin compatibility contracts.
There are no corresponding first-party Apple or Android API callers.
