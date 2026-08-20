# API Keys API

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

| Attempted write on `/api/v1/admin/users` | Result |
|------------------------------------------|--------|
| `POST` with `role: "admin"` | `403 insufficient_scope` |
| `PUT` with `role: "admin"` | `403 insufficient_scope` |
| `PUT` with `password` or `role` when the target account is currently an admin | `403 insufficient_scope` |
| `POST` with `password` and a non-admin `role` | allowed |
| `PUT` with `password` when the target account is not an admin | allowed |

Unscoped keys and JWT sessions are unaffected.

Discover the scopes a server understands with the capability endpoint below
rather than sniffing the server version.

---

## Self-service endpoints

The management endpoints below (create, list, delete) require a **JWT access
token**; authenticating them with an API key returns `403`, because a key may
not mint or enumerate keys. The capability endpoint is the exception: it is a
static catalog, so any authenticated caller may read it.

### List the available scopes

```
GET /api/v1/api-keys/scopes
```

Feature detection for API key scopes. Requires authentication; needs no
particular role. Note that a *scoped* key is refused here like anywhere else
outside its allowlist.

```json
{
  "scopes": [
    {
      "name": "admin:users",
      "description": "Manage user accounts: create, list, read, update, and delete users and read their profiles. Cannot create or modify admin accounts."
    },
    {
      "name": "admin:access-groups:read",
      "description": "Read access groups and their policies."
    }
  ]
}
```

A server that predates scopes has no such route and answers `404`; treat that
as "no scope support" and create unscoped keys.

### Create a key

```
POST /api/v1/api-keys
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `label` | string | yes | Human-readable name for the key. |
| `scopes` | string[] | no | Scope names from the capability endpoint. Omitted, `null`, or `[]` creates an unscoped key. Duplicates are removed and the list is sorted; an unknown scope is a `400`. |

Returns `201` with the key record. **The full `key` value is returned on every
read of the owner's own keys, but treat the create response as the moment to
store it.**

```json
{
  "id": 12,
  "user_id": 7,
  "label": "ci",
  "key": "sa_1f0c…",
  "rate_tier": "standard",
  "scopes": ["admin:users"],
  "created_at": "2026-08-19T12:00:00Z",
  "last_used_at": null
}
```

`scopes` is always an array; `[]` means unscoped.

### List your keys

```
GET /api/v1/api-keys
```

Returns an array of the same objects, newest first.

### Delete a key

```
DELETE /api/v1/api-keys/{id}
```

Returns `204`. Deleting a key you do not own returns `404`.

---

## Admin endpoints

These require an admin account.

### List every key

```
GET /api/v1/admin/api-keys
```

Same fields as above plus `username` for the owning account.

### List one user's keys

```
GET /api/v1/admin/users/{userId}/api-keys
```

### Create a key for a user

```
POST /api/v1/admin/api-keys
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `label` | string | yes | Human-readable name for the key. |
| `user_id` | integer | no | Owning account; defaults to the calling admin. |
| `scopes` | string[] | no | Same validation as the self-service endpoint. |

### Change a key's rate tier

```
PUT /api/v1/admin/api-keys/{id}/tier
```

Body: `{"tier": "standard"}` or `{"tier": "elevated"}`. Any other value is a
`400`.

### Delete any key

```
DELETE /api/v1/admin/api-keys/{id}
```

Returns `204`.
