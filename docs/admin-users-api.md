# Administrator accounts and access groups

The v2 administrator routes require an authenticated acting administrator and
retain the demo write guard. Scoped API keys retain their existing account and
access-group grants; they cannot impersonate accounts. A household primary
profile is not a server administrator.

`GET /api/v2/admin/users/capabilities` reports account management, guarded
configuration, transactional default-profile creation, and access-group support.
It also reports the account projections below that depend on optional services:
`account_devices`, `watch_summary`, `account_downloads` (the downloads list, summary
and series monitors) and `request_usage`. A read whose flag is false answers 503.
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

## Policy defaults

An account's unset policy field takes its access group's value. Admin accounts
never belong to a group: their unset fields resolve to full access, the access
the Owner has, and an override on an admin account still restricts it. A
regular account with no group uses the built-in no-group values, which match
full access except that server-prepared downloads
(`download_transcode_allowed`) are off.

`GET /api/v2/admin/users/policy-defaults` returns both layers, `admin` and
`ungrouped`, in the shape of `effective_policy` without `permissions`, so
clients show where a default comes from without keeping their own copy. The
values come from the server build. Account capabilities advertise
`policy_defaults`. Existing `admin:users` keys may use this read.

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

Only the Owner manages administrator accounts. Other administrators manage
ordinary accounts and their own account, and receive `403 permission_denied` when
they:

- create an administrator, promote an account to administrator, or invite one;
- update, delete, or issue a password reset for another administrator or the
  Owner;
- create an API key for another administrator or the Owner, or change or revoke
  one of their keys.

The v1 account and API key routes answer the same refusals with
`403 owner_protected`, and the v1 invitation routes with `403 role_not_allowed`.
No account may change its own role, disable itself, or delete itself through these
routes, and that includes the Owner; those writes return the same 403. Nobody may impersonate the Owner. Administrators may still impersonate only
non-administrators, but the Owner may also impersonate other administrators.

`POST /api/v2/admin/users/{id}/transfer-ownership` makes another enabled
administrator the Owner and returns 204. The caller must be the Owner, signed in:
an API key or an impersonation session receives `403 permission_denied`, and so
does any caller that is not the Owner. A target that is not another enabled
administrator returns `422 validation_failed`. The previous Owner stays an
administrator. The operation has no v1 route and is not retryable; a replay is
refused because the caller is no longer the Owner. The capability endpoint reports
`ownership_transfer`. Moving ownership ends every session in which someone views the
server as the new Owner and the previous Owner's sessions viewing as other
administrators. It deletes the new Owner's API keys and reset link, since the previous
Owner could have created them; the new Owner creates new keys. It also revokes pending
administrator invitations, which the new Owner can resend.

Making an account an administrator deletes its API keys and its reset link. Any
administrator may create those for an ordinary account, so after a promotion they
would carry administrator authority that only the Owner grants.

These rules stop an administrator who deliberately tries to exceed its authority.
They check the caller's standing when the request arrives; they do not order
simultaneous requests around an ownership transfer, which a server sees rarely.

When the Owner account is lost or locked out, someone with shell access to a node
and the server's `DATABASE_URL` (no other server secret) recovers it with `silo owner set <username>`, for
example `docker compose exec silo silo owner set alice`. The command makes that
account the Owner, enables it, and grants it the administrator role if needed; a
role or status change signs the account out. The previous Owner stays an
administrator.

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
| `GET /users/{id}/profiles` | Complete profile collection with string IDs, names and `last_seen_at` |
| `GET /users/{id}/api-keys` | Bounded metadata-only key collection; no stored credential |
| `GET /users/{id}/ips` | Bounded IP history for one account, each address with its `location` |
| `GET /ips?ip=...` | Bounded account history for one valid IP address |
| `GET /users/{id}/settings/values` | Bounded setting-value collection and contract revision |
| `PUT /users/{id}/settings/values/{key}` | Validated setting value |
| `DELETE /users/{id}/settings/values/{key}` | 204 |
| `GET /users/{id}/devices` | Complete device collection for one account (`listAdminUserDevices`) |
| `GET /users/{id}/watch-summary` | Finalized play totals over recent days (`getAdminUserWatchSummary`) |
| `GET /users/{id}/downloads` | Paginated managed device downloads (`listAdminUserDownloads`) |
| `GET /users/{id}/downloads/summary` | Managed download totals (`getAdminUserDownloadSummary`) |
| `GET /users/{id}/download-subscriptions` | Paginated series monitors (`listAdminUserDownloadSubscriptions`) |
| `GET /request-users/{user_id}/usage` | Effective request policy and quota use (`getAdminRequestUserUsage`) |

Every account projection answers 404 for an unknown account and 422 for a malformed
identifier.

Paginated reads accept `limit` (up to 200) and signed `cursor`; responses expose
`items` and `page`. Cursors bind the actor/profile, target, filters, and page size.
IP queries accept `days` from 1 to 365, defaulting to 30. Their cursor preserves a
fixed observation window and deterministic last-seen ties. Newer events do not
move earlier groups across that cursor. Addresses are returned without CIDR masks.
Each address carries `location`, `local` or `remote`, classified from the address
alone: private, loopback and link-local addresses are local, everything else is
remote. Request logs do not record the network-access route, so a request that
reached the server through a network-access provider from a private address reads
as `local` here even though playback treats that path as remote.

A profile's `last_seen_at` is the latest time any device registration reported the
profile, and null when none has. The device collection lists every device the
account's apps registered and every device that holds saved per-device settings,
most recently seen first; `last_seen_at` is the latest registration (null for a
device known only from saved settings), `last_updated` the latest of registration
and saved-setting writes, and `override_count` the saved per-device settings across
profiles. Each device lists its profiles with the same fields; `profile_name` is
empty when the profile no longer exists.

The watch summary accepts `days` from 1 to 365 (default 30) and an optional
`profile_id`. It totals the finalized attempts that ended at or after `since` (now
minus `days`): `plays`, `completed_plays`, `watched_seconds` and `last_played_at`
(null when none). Those are the attempts `listAdminPlaybackHistory` lists with the
same `user_id`, `profile_id` and `ended_after` set to `since`.

The request usage read reports the account's effective request policy as the
request service resolves it: `requests_enabled` (the server switch), `allowed`
(false when the account's switch, group or approval mode blocks it), `unlimited`,
`used`, `max_requests`, `window_days`, `window_start`, `remaining` and
`auto_approve`. An unlimited account is not counted, so `used` is 0.

### Account downloads

The downloads list and summary cover managed device rows only; ephemeral web
downloads have no device and are excluded. The list returns every status, newest
first, filtered by `profile_id` and `device_id` when given. Each row names the
catalog title and type of its `content_id` (the movie, or the series of an episode)
and carries `episode` with season, number and title when `episode_id` resolves to a
catalog episode, otherwise null. The summary counts non-revoked rows in `total`,
`completed`, `in_progress` (preparing, ready or downloading), `failed`, and `devices`,
sums their `file_size` in `total_bytes`, counts `revoked` rows separately, and counts
active series monitors. The monitor list reports each monitor's options with
`on_device` and `in_progress` counts for its own device and profile and
`removed_episodes`, the episodes the user deleted from the monitored series.

The server only records what the apps report. An app that never reports a status
leaves its rows at `ready`, and fetching a file does not change that status. Rows
for a device that was wiped or lost the app stay until the device is removed. These
reads never change a download or monitor; there is no administrator mutation.

Administrator settings use explicit target identity query parameters: `scope`,
`profile_id`, `client_family`, `device_id`, `library_id`, and `series_id` as required
by the setting scope. They never substitute the acting administrator's profile.
Both PostgreSQL and SQLite paginate by the full setting identity. Writes reuse
existing validation, normalization, mirror updates, and last-write semantics;
see [settings API](settings-api.md). Reset `nav.shortcuts` with PUT of its empty
document; DELETE is rejected. The web bulk-reset flow reports partial completion.

These administrator projections do not alter Jellyfin compatibility contracts.
There are no corresponding first-party Apple or Android API callers.
