# Silo Complex 2.2 API Contract

This document freezes the additive `/api/v1` contract used by Silo Complex
2.2. Existing v1 endpoints, response bodies, and status codes remain
unchanged.

## Authentication and errors

All endpoints below require the existing bearer API credential and admin
authorization used by Complex admin calls:

```http
Authorization: Bearer sa_...
```

Successful responses use `Content-Type: application/json`. Timestamps are UTC
RFC 3339 strings. Errors use the existing JSON envelope:

```json
{"error":{"code":"machine_readable_code","message":"Safe human-readable message"}}
```

The stable error statuses are:

- `401 Unauthorized`: the bearer credential is missing, malformed, invalid,
  expired, or belongs to a disabled account.
- `403 Forbidden`: the authenticated account is not authorized as an acting
  administrator or lacks the capability's required scope.
- `404 Not Found`: the requested resource does not exist.
- `409 Conflict`: a request conflicts with the current session generation or
  an idempotency binding.
- `413 Content Too Large`: an admin playback-control request body exceeds 16
  KiB.
- `422 Unprocessable Entity`: the JSON request is syntactically valid but its
  values are invalid, including an oversized playback-control field.
- `500 Internal Server Error`: an unexpected server-side failure prevented a
  safe response.

No endpoint redirects authentication failures or returns secrets in an error
message.

The initial router stubs fail closed until the capability implementations land:
they advertise no capabilities, return no branding logo metadata, and return
only an incomplete session snapshot with `incomplete_reason:"not_implemented"`.
The endpoint sections below freeze the final contract that replaces those
temporary responses.

## `GET /api/v1/system/capabilities`

Returns `200 OK` with the exact capability identifiers used for feature
detection. Clients gate features on these identifiers, not on
`api_version` alone.

```json
{"api_version":"2.2","capabilities":["branding.v1","sessions.snapshot.v2","sessions.terminate.v1","users.identity.v1"]}
```

## `GET /api/v1/admin/branding`

Requires `branding.v1`. Returns `200 OK` with the configured server name and
preferred branding logo metadata:

```json
{"server_name":"Sullyflix","logo_url":"https://allowed-garage-host/...","logo_etag":"content-ref"}
```

`logo_url` is either an absolute HTTPS URL on the configured, allowlisted
public Garage/S3 host or `null`. `logo_etag` is the logo's stable content
reference or `null`; it is `null` whenever `logo_url` is `null`. Missing or
unsafe logo configuration is represented by these nullable fields and never
by a redirect or an arbitrary host.

## `GET /api/v1/admin/sessions/snapshot`

Requires `sessions.snapshot.v2`. Returns `200 OK` with one generation-stable
snapshot:

```json
{"snapshot_id":"uuid","generated_at":"RFC3339","complete":true,"sessions":[]}
```

`snapshot_id` is a UUID, `generated_at` is a UTC RFC 3339 timestamp, and
`sessions` is always a JSON array. Every session record contains stable
`session_id`, opaque `session_generation`, Silo `user_id`, `started_at`,
playback `state`, `is_transcoded`, and optional device/client labels.

When an authoritative snapshot cannot be produced, the endpoint still returns
`200 OK` with `complete:false`, a safe machine-readable `incomplete_reason`,
and the available `sessions` array. An incomplete snapshot must never be used
for termination decisions. This includes a snapshot that is otherwise complete
but cannot fit in the bounded termination registry; it returns
`incomplete_reason:"registry_capacity"` and never advertises `complete:true`.

## Admin playback control limits

Existing admin playback-control response statuses remain unchanged for
compatibility. Request bodies are limited to 16 KiB. Identifier fields are
bounded to 512 bytes for `session_id`, 128 bytes for `session_generation`,
`snapshot_id`, and `reason_code`, and 255 bytes for `idempotency_key`. Display
text is bounded to 1,024 Unicode code points for `reason`, 256 for `title`, and
4,096 for `message`. Bodies over the request limit return `413`; syntactically
valid requests with oversized fields return `422`.

## Compatibility guarantees

These routes are additive. In particular, the existing
`GET /api/v1/theme/branding`, `GET /api/v1/admin/sessions`, and admin playback
control routes remain registered with their existing bodies and status
semantics. Existing `/api/v1` fields are not renamed, removed, retyped, or
repurposed by this contract.
