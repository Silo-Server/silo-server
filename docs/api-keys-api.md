# API Keys API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. See [the native API contract](architecture/api-contract.md).

API keys are long-lived credentials for scripts and integrations. A key is a
string with an `sa_` prefix and is sent the same way as a JWT access token:

```
Authorization: Bearer sa_your_api_key_here
```

A key always acts as the user who owns it: the account's role and permissions
still apply, so an admin-only route needs a key owned by an admin account.

A key is either an **account key** or a **profile key**. An account key may send
`X-Profile-Id` naming any profile on its account and acts as that profile without
its PIN. Most profile operations require the header and answer `422` without it;
the few that accept its absence, such as `GET /api/v2/profiles` and
`GET /api/v2/user/libraries`, answer for the account as a whole. A profile key is bound to one profile when it is created: it always acts
as that profile, with that profile's rating, library and quality limits, and a
request naming another profile is `403 permission_denied`. A profile key holds no
admin role, even when its profile is the primary one on an admin account.
Deleting a profile deletes its keys. `GET /api/v2/profiles` lists only the bound
profile to a profile key.

Every key also shares its owner's [authenticated account allowance](auth-api.md#authenticated-account-allowance)
with that account's other keys, profiles, sessions and devices. On the native API,
the key's own tier, IP and global budgets still apply. Raising a key's tier does not
raise its owner's shared allowance.

## Scopes

By default a key is **unscoped** and can reach every route its owner can. A key
created with `scopes` is an allowlist credential instead: the auth middleware
admits it only to the routes those scopes name and answers `403 permission_denied`
everywhere else, including routes added after the key was issued. A scoped key
must send the route's plain path: a path that percent-encodes a `/`, a `.`, or
another character that needs no encoding, or that has a `.` or `..` segment, a
doubled slash, or a trailing slash, is refused with the same `403`, because the
router would not read it as the route the allowlist checked.

Scopes only narrow. They never grant, and they never bypass the owner's role
check. `admin:users` on a key owned by a non-admin account still cannot manage
users.

Scoped keys are also refused the writes that would let them trade the allowlist
for an unscoped **admin** session. The boundary is the admin role, not the
credential: provisioning and managing ordinary accounts is in scope.

| Attempted write on `/api/v2/admin/users` | Result |
|------------------------------------------|--------|
| `POST` with `role: "admin"` | `403 permission_denied` |
| `PUT` with `role: "admin"` | `403 permission_denied` |
| `PUT` with `password`, `email`, `role` or `break_glass` when the target account is currently an admin | `403 permission_denied` |
| `POST` with `password` and a non-admin `role` | allowed |
| `PUT` with `password` when the target account is not an admin | allowed |

Unscoped keys and JWT sessions are unaffected.

Each scope has an `audience`. An `admin` scope names operations that need an
administrator account. A `user` scope acts as a profile and needs no role; the
user scopes are listed under [User scopes](#user-scopes). A profile key, and any
key on an account that is not an admin, carries user scopes only and at least one.

Discover the scopes a server understands with the capability endpoint below
rather than sniffing the server version.

## Admin key management

Admin key management lives under `/api/v2/admin/api-keys`, plus one account's key list under
`/api/v2/admin/users`:

| Method | Path | Result |
|---|---|---|
| GET | `/admin/api-keys/capabilities` | Availability, supported scopes and tiers, and editor support |
| GET | `/admin/api-keys` | Bounded metadata collection with opaque cursor continuation |
| GET | `/admin/api-keys/{id}` | Canonical metadata and a strong `ETag` |
| POST | `/admin/api-keys` | `201` creation response containing the full key and canonical `Location` |
| PUT | `/admin/api-keys/{id}/tier` | Conditional tier update returning canonical metadata and `ETag` |
| DELETE | `/admin/api-keys/{id}` | Conditional deletion returning `204` without a body |
| GET | `/admin/users/{id}/api-keys` | One account's keys, as a bounded metadata collection with cursor continuation |

These operations require acting-admin authority. Mutations retain the demo
restriction; reads do not. Scoped API keys
cannot access credential management; unscoped keys retain the owning account's
access. Personal key management uses the separate account-scoped operations below.

IDs use JSON strings. Canonical metadata excludes the full key, usage timestamps,
and the owner's display name. The collection adds usage and owner display fields.
Only the creation response contains the full credential. Save it then; creation
must not be retried automatically after an uncertain response.

The list accepts `limit` (1–200, default 50) and `cursor`. It orders by creation
time descending, then ID descending. The cursor is bound to the acting account,
profile, and page size. Continue using the returned cursor; a changed scope or
invalid cursor requires a fresh first page. The list has no exact total.

The tier update body is `{"rate_tier":"standard"}` or
`{"rate_tier":"elevated"}`. Read the canonical resource before editing and send its captured tag in
`If-Match`. A missing precondition returns `428`; a stale tag returns `412` with
the current tag. `If-Match: *` explicitly permits changing the current resource.
Canonical reads support conditional requests, including `304` for an unchanged
`If-None-Match` tag. Authentication usage and no-op tier edits do not invalidate
configuration tags. Successful deletion returns no validator.


## Personal key management

Personal key management operates on the login account. Listing, revocation, and scope
discovery do not require a household profile; creation acts through one (see below).

| Method | Path | Result |
|---|---|---|
| GET | `/api/v2/api-keys/scopes` | Availability and supported scopes |
| GET | `/api/v2/api-keys` | Metadata collection with opaque cursor continuation |
| POST | `/api/v2/api-keys` | `201` response containing the credential once |
| DELETE | `/api/v2/api-keys/{id}` | Owner-only revocation returning `204` |

Listing, creation, and revocation require JWT authentication; API-key credentials
receive `403`. While the server setting `auth.user_api_keys` is off (the default),
creation also requires a server admin account: a regular account's login session
receives `403` and no key is created. Body validation runs first, so an invalid
body still answers `422`. [User keys](#user-keys) covers the setting turned on.

A key skips every profile's PIN, so creation also requires the household manager:
the admin acts through the account's primary profile (`X-Profile-Id`), with
`X-Profile-Token` from `verifyProfilePIN` when that profile has a PIN. Another
profile on the admin's account receives `403 permission_denied`; a primary profile
without its token receives `403 profile_verification_required`. A request without
`X-Profile-Id` may create a key only while no profile on the account has a PIN,
a maturity limit, or library restrictions; otherwise it receives
`403 permission_denied`. Any account can still list and revoke keys it
already owns: the primary profile every key on the account, any other profile
only the keys bound to it. A key outside that view answers `404` to revocation.
Listing and revocation without `X-Profile-Id` cover the whole account while no
profile on it has a PIN, a maturity limit, or library restrictions, and are
otherwise `422`, as on the catalog reads; both rules are for login sessions, since an API key gets `403` here first. Scope discovery retains its
availability to unscoped API keys.
Creation and revocation retain the demo restriction; listing and scope discovery
do not. An unavailable store reports
`available: false` in scope discovery and `503` for management operations.

Create with `{"label":"Script","scopes":[]}`. Omitting scopes creates an unscoped
key. Explicit nulls and unknown fields are rejected. The account comes from the
login session; a caller cannot choose another owner. Store the returned credential
securely: creation is non-retryable after an uncertain response. List responses
contain metadata only, with string IDs and optional usage timestamps.

The list accepts `limit` (1–200, default 50) and `cursor`, ordered by creation time
and ID descending. Cursors bind to the account, the page size, and the view of the
keys the acting profile gets (every key, or one profile's own); a change of view
needs a fresh first page. Revocation checks ownership in the database delete. Missing keys
and keys belonging to another account both return `404`; retrying revocation has
no additional effect. This self-service revocation does not require `If-Match`.

The current web, Apple, and Android clients have no personal key-management
consumer to migrate. Jellyfin credential endpoints keep their separate protocol.

### User keys

The admin setting `auth.user_api_keys` (default `false`) lets accounts that are
not admins, and profiles other than the primary one, create keys for themselves.
`GET /api/v2/api-keys/scopes` reports it as `user_keys_enabled`, with the
per-account limit as `max_user_keys`. Turning the setting off stops new keys;
existing ones keep working until they are revoked. A server whose profile store
is not the Postgres one (`userdb.backend: sqlite`) reports `user_keys_enabled`
false and refuses these keys whatever the setting says, because a key's profile
binding and its PIN check read profiles from Postgres.

Create one with `{"label":"Sync","scopes":["watch:read"],"profile_id":"<id>"}`.
`profile_id` binds the key to that profile; omit it for an account key.

| Acting profile (`X-Profile-Id`) | May create |
|---|---|
| The account's primary profile | An account key, or a profile key for any profile on the account |
| Any other profile | A profile key for itself only |
| None | Nothing on a regular account |

The acting profile sends its own `X-Profile-Token` when it has a PIN; without it
the request is `403 profile_verification_required`. Only the account's own login
session creates these keys: a view-as session and a remote-playback handoff
receive `403`.

Every user key carries at least one scope, all of audience `user`; anything else
is `422` naming `body.scopes`. A `profile_id` that is not a profile of the
account is `422` naming `body.profile_id`. An account that already holds
`max_user_keys` keys receives `409 conflict`. An admin's own unscoped or
admin-scoped account key is unaffected by the setting and the limit, and an
admin binds a key to a profile under the same rules as everyone else.

User keys share the `standard` rate tier; only an admin changes a key's tier.
The Jellyfin and plugin surfaces refuse them, as they refuse every scoped key.

## User scopes

| Scope | Admits (under `/api/v2`) |
|---|---|
| `catalog:read` | `/user/libraries`, home and library layouts and rows, `/catalog` reads including `POST /catalog/query` and `/catalog/letters`, people, seasons and episodes |
| `watch:read` | `/history`, `/progress`, progress snapshots, `/favorites`, `/ratings`, `/watchlist`, `/not-interested`, and `/profile/export` |
| `watch:write` | `POST` and `DELETE /watched/{id}`, `POST /sync/progress`, `POST /history/remove`, `DELETE /progress/{id}/position`, writes to favorites, ratings, hidden titles and watchlist entries for titles in the library. `POST /profile/import` stays outside it, because an import also overwrites the profile's settings and notification preferences |
| `notifications` | `GET /notifications`, `/notifications/sync`, `/notifications/unread-count`, `/notifications/capabilities`, and marking read with `POST /notifications/{id}/read` and `/notifications/read-all`. No preferences, webhooks, email, Discord or push registrations |
| `requests` | Request discovery and search, and reading, creating, following, cancelling and removing requests as the profile's own session can: the account's requests, or only the profile's own when it is blocked from requests. Also adding and removing watchlist titles that are not in the library (`PUT` and `DELETE /watchlist/titles/{media_type}/{tmdb_id}`), which can create or withdraw a request |
| `libraries:read` | See [Restricted discovery scopes](#restricted-discovery-scopes) |

`catalog:read`, `watch:read` and `notifications` also open the realtime events socket
(`GET /events/capabilities`, `POST /events/ws-ticket`), each for its own
channel; see [User keys](realtime-api.md#user-keys) in the realtime API.

Every user scope except `libraries:read`, which predates them and keeps its
original reach, also admits `GET /account/me` and `GET /profiles`, so a script
can find the profile IDs an account key may name. No user scope admits playback,
a stream, a download, a setting, a credential, or household management, and
`GET /watch/{id}` stays outside them because it is the playback plan.

## Restricted discovery scopes

The scope catalog includes two additional native read permissions:

| Scope | Allowed reads |
|---|---|
| `libraries:read` | `/api/v2/user/libraries` and its `/capabilities` |
| `admin:sessions:summary:read` | `/api/v2/admin/sessions/summary` and `/api/v2/admin/sessions/capabilities` |

Library discovery retains the owner's account/profile visibility. Session
summaries require an administrator owner and can cover all accounts or a supplied
account filter. Neither scope grants diagnostic session details, playback control,
media playback, account editing, filesystem paths or API-key management.

These additions do not change existing unscoped credentials or add client-specific
permission rules. Scope catalogs and capability documents remain additive.

## Monitoring and session control scopes

Two scopes serve an outside monitoring tool, such as a dashboard that watches
playback and keeps its own history. Every admin operation they admit requires a
key owned by an administrator account; on any other account it answers
`403 permission_denied`. `/events/capabilities` is not an admin operation and
answers any owner.

`admin:monitor:read` admits these reads, all `GET`:

| Area | Paths under `/api/v2` |
|---|---|
| Live sessions | `/admin/sessions`, `/admin/sessions/summary`, `/admin/sessions/capabilities`, `/admin/sessions/command-capabilities`, `/events/capabilities` |
| History and stats | `/admin/playback-history`, `/admin/playback-history/capabilities`, `/admin/playback-history/{session_id}`, `/admin/stats/timeseries`, `/admin/stats/playback-activity`, `/admin/stats/top-activity`, `/admin/stats/downloads`, `/admin/insights/capabilities`, `/admin/insights/activity`, `/admin/insights/people`, `/admin/insights/devices`, `/admin/insights/bandwidth`, `/admin/insights/titles`, `/admin/insights/titles/{content_id}`, `/admin/insights/library` |
| Accounts | `/admin/users`, `/admin/users/capabilities`, `/admin/users/{id}`, and `/admin/users/{id}/` `profiles`, `devices`, `ips`, `downloads`, `downloads/summary`, `watch-summary` |
| Devices and addresses | `/admin/devices`, `/admin/devices/capabilities`, `/admin/ips` |
| Downloads | `/admin/downloads/entries`, `/admin/downloads/devices`, `/admin/downloads/storage/capabilities`, `/admin/downloads/preparations`, `/admin/downloads/preparations/capabilities` |
| Hosts and nodes | `/admin/system/resources`, `/admin/system/resources/capabilities`, `/admin/nodes/summary`, `/admin/nodes/capabilities`, `/admin/server/status` |
| Catalog | `/admin/catalog/items`, `/admin/catalog/capabilities` |
| Media requests | `/admin/requests`, `/admin/requests/counts`, `/admin/requests/capabilities`, `/admin/requests/{id}/events`, `/admin/requests/{id}/poster` |

The scope never returns filesystem paths, node addresses, storage keys, setting
values or credentials. Operations that carry them stay outside it: item files, download
storage other than its capabilities, settings, logs, API keys, audit, per-device setting values, node
sessions, and the request integrations, settings and routes. The one setting value
it returns is `requests_enabled` on `/admin/requests/counts`, the server-wide
requests switch, so a monitor can tell a disabled feature from an empty queue. Five monitored
reads withhold details from scoped keys:

- `/admin/system/resources` omits each disk's `path`.
- `/admin/catalog/items` refuses `include_file_paths` with `403` unless the key
  also holds `admin:catalog:paths`.
- `/admin/downloads/preparations` omits each job's `error`.
- `/admin/requests` omits `last_error` on each request and its targets, and
  `/admin/requests/{id}/events` omits each event's `message`. Both are free
  text that can name download servers, their addresses and routing rules.
  `/admin/requests` also omits each target's `instance_name` and `route_name`,
  the names the administrator gave a download server and a routing rule;
  `integration_kind` still says what kind of server took the target.
- Nodes are read through `/admin/nodes/summary`, which has no addresses, paths,
  overrides or capability documents. `/admin/nodes` stays outside the scope.

The stats and insights reads ignore `refresh` for scoped keys and answer from the shared
cached aggregate, so a polling tool cannot force the aggregate queries.

Unscoped keys and login sessions keep the full documents.

The scope also admits one write, `POST /api/v2/events/ws-ticket`, which opens
the realtime events socket for the `sessions`, `catalog` and `downloads` channels for up to
four hours. The key's revision and scope are rechecked every 15 seconds; see
[API-key handshake](realtime-api.md#api-key-handshake).

`admin:sessions:control` admits `POST /api/v2/admin/sessions/{session_id}/`
`pause`, `resume`, `stop`, `message` and `terminate`, plus
`GET /api/v2/admin/sessions/command-capabilities`. It grants no reads beyond
that; combine it with `admin:monitor:read` to find the sessions to act on.

## Catalog paths scope

`admin:catalog:paths` admits `GET /api/v2/admin/catalog/items` and
`/admin/catalog/capabilities`, and lets the listing answer `include_file_paths`
with each file's path on the server, for a tool that mirrors the library and
matches its files. It requires a key owned by an administrator account, like
the monitoring scopes. It grants nothing else: combine it with
`admin:monitor:read` for the realtime socket that keeps a mirror current.

