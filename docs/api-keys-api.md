# API Keys API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` key routes are summarized in
> [Bridge note](#bridge-note) and are retired after the pre-1.0 bridge window. See
> [the native API contract](architecture/api-contract.md).

API keys are long-lived credentials for scripts and integrations. A key is a
string with an `sa_` prefix and is sent the same way as a JWT access token:

```
Authorization: Bearer sa_your_api_key_here
```

A key always acts as the user who owns it: the account's role and permissions
still apply, so an admin-only route needs a key owned by an admin account.

## Scopes

By default a key is **unscoped** and can reach every route its owner can. A key
created with `scopes` is an allowlist credential instead: the auth middleware
admits it only to the routes those scopes name and answers `403` everywhere
else, including routes added after the key was issued.

Scopes only narrow. They never grant, and they never bypass the owner's role
check. `admin:users` on a key owned by a non-admin account still cannot manage
users.

Scoped keys are also refused the writes that would let them trade the allowlist
for an unscoped **admin** session. The boundary is the admin role, not the
credential: provisioning and managing ordinary accounts is in scope.

| Attempted write on `/api/v2/admin/users` | Result |
|------------------------------------------|--------|
| `POST` with `role: "admin"` | `403 insufficient_scope` |
| `PUT` with `role: "admin"` | `403 insufficient_scope` |
| `PUT` with `password` or `role` when the target account is currently an admin | `403 insufficient_scope` |
| `POST` with `password` and a non-admin `role` | allowed |
| `PUT` with `password` when the target account is not an admin | allowed |

Unscoped keys and JWT sessions are unaffected.

Discover the scopes a server understands with the capability endpoint below
rather than sniffing the server version.

## Admin key management

Admin key management lives under `/api/v2/admin/api-keys`:

| Method | Path | Result |
|---|---|---|
| GET | `/admin/api-keys/capabilities` | Availability, supported scopes and tiers, and editor support |
| GET | `/admin/api-keys` | Bounded metadata collection with opaque cursor continuation |
| GET | `/admin/api-keys/{id}` | Canonical metadata and a strong `ETag` |
| POST | `/admin/api-keys` | `201` creation response containing the full key and canonical `Location` |
| PUT | `/admin/api-keys/{id}/tier` | Conditional tier update returning canonical metadata and `ETag` |
| DELETE | `/admin/api-keys/{id}` | Conditional deletion returning `204` without a body |

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

Personal key management operates on the login account, without requiring a household
profile.

| Method | Path | Result |
|---|---|---|
| GET | `/api/v2/api-keys/scopes` | Availability and supported scopes |
| GET | `/api/v2/api-keys` | Metadata collection with opaque cursor continuation |
| POST | `/api/v2/api-keys` | `201` response containing the credential once |
| DELETE | `/api/v2/api-keys/{id}` | Owner-only revocation returning `204` |

Listing, creation, and revocation require JWT authentication; API-key credentials
receive `403`. Creation also requires a server admin account: a regular account's
login session receives `403` and no key is created. Body validation runs first, so
an invalid body still answers `422`. Any account can still list and revoke keys it
already owns. Scope discovery retains its availability to unscoped API keys.
Creation and revocation retain the demo restriction; listing and scope discovery
do not. An unavailable store reports
`available: false` in scope discovery and `503` for management operations.

Create with `{"label":"Script","scopes":[]}`. Omitting scopes creates an unscoped
key. Explicit nulls and unknown fields are rejected. The account comes from the
login session; a caller cannot choose another owner. Store the returned credential
securely: creation is non-retryable after an uncertain response. List responses
contain metadata only, with string IDs and optional usage timestamps.

The list accepts `limit` (1–200, default 50) and `cursor`, ordered by creation time
and ID descending. Cursors bind to the account and page size, independently of the
selected profile. Revocation checks ownership in the database delete. Missing keys
and keys belonging to another account both return `404`; retrying revocation has
no additional effect. This self-service revocation does not require `If-Match`.

The current web, Apple, and Android clients have no personal key-management
consumer to migrate. Jellyfin credential endpoints keep their separate protocol.

## Restricted discovery scopes

The scope catalog includes two additional native v2 read permissions:

| Scope | Allowed reads |
|---|---|
| `libraries:read` | `/api/v2/user/libraries` and its `/capabilities` |
| `admin:sessions:summary:read` | `/api/v2/admin/sessions/summary` and `/api/v2/admin/sessions/capabilities` |

Library discovery retains the owner's account/profile visibility. Session
summaries require an administrator owner and can cover all accounts or a supplied
account filter. Neither scope grants diagnostic session details, playback control,
media playback, account editing, filesystem paths or API-key management.

These additions do not change existing unscoped credentials or add client-specific
permission rules. Existing native clients need no migration; scope catalogs and
capability documents remain additive.

## Bridge note

The frozen alpha surface serves the same features at `/api/v1/api-keys`,
`/api/v1/api-keys/scopes`, `/api/v1/admin/api-keys`,
`/api/v1/admin/users/{userId}/api-keys`, and `/api/v1/admin/api-keys/{id}/tier`. It
uses integer IDs, spells the tier field `tier` rather than `rate_tier`, has no
cursor pagination, and does not use `ETag`/`If-Match` preconditions. Those routes are
frozen: no feature work lands on them, and Silo 1.0 answers the whole `/api/v1`
namespace with `410 Gone` and the `client_upgrade_required` problem code. Build
against `/api/v2`. `POST /api/v1/api-keys` keeps its frozen behavior and still lets
any login account create a key; only the v2 route requires an admin account.
