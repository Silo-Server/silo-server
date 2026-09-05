# Authentication API

Commands and paths in this document assume the repository root or the server's `/api/v1` base URL.

## Account passwords

A password belongs to a login account, not to an individual household profile. Every profile on an
account therefore shares the same password. Self-service password changes are restricted to the
active primary profile. An admin account may also change its password before selecting a profile,
but selecting a secondary profile removes that authority. API keys and impersonation sessions can
never change an account password.

Local passwords are bcrypt hashes. New passwords must contain at least 8 Unicode characters and be
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
