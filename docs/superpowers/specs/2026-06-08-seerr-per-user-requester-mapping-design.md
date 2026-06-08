# Seerr per-user requester mapping (opt-in attribution + per-user permissions)

Status: design • 2026-06-08
Scope: multi-repo — `silo-plugin-sdk` (descriptor field), `silo-server` (requester
identity resolution), `silo-plugin-requests-seerr` (the mapping + Seerr user API).

## Context

The seerr request-router plugin submits `POST /api/v1/request` to Overseerr/
Jellyseerr authenticated with the connection's **admin API key** and **no
`userId`**, so every Silo request appears in Seerr as a request from the single
API-key owner (admin) and is auto-approved. There is no per-user attribution and
no way to route requests through Seerr's per-user approval/quotas. The host
already passes `requester_user_id` (an int) in the `request_router` Fulfill
descriptor, but no email/username — and the requests host has no user-identity
resolution at all, so the plugin cannot match a Silo requester to a Seerr user
today.

## Goal

A per-connection setup choice:
- **`admin` (default):** today's behavior — all requests under the API-key admin.
- **`mapped`:** attribute each request to the Seerr user matching the Silo
  requester's **email**, auto-creating that Seerr user (with operator-chosen
  default permissions) when none exists. Whether mapped requests still
  auto-approve is governed by the granted permissions.

## Non-goals

- No manual Silo↔Seerr mapping table — matching is automatic by email.
- No change to the arr plugin or to `admin` mode.
- No Seerr → Silo user sync; we only resolve/create the user needed for a request.

## Decisions (locked)

- Match key: **email** (case-insensitive).
- Default permissions for created users: **explicit per-permission toggles** in the
  connection form.
- Fallback when a requester can't be mapped/created (no email, lookup/create
  error): **fall back to the admin user** (omit `userId`) so the request still
  goes through; logged.
- Identity transport: **push** into the Fulfill descriptor (keeps the seerr plugin
  a pure HTTP plugin — no runtime-broker dependency).

## Components

### 1. SDK contract (`silo-plugin-sdk`, branch `feat/request-router-capability`)

`request_router.proto` `RequestDescriptor` gains two fields (next free numbers
after `requester_profile_id = 7`):

```proto
  string requester_email = 8;
  string requester_username = 9;
```

Regenerate with `buf generate` (protoc not needed). New getters
`GetRequesterEmail()` / `GetRequesterUsername()`.

### 2. Host (`silo-server`)

- `internal/plugins/user_identity_lookup.go`: add `Email string` to `UserIdentity`
  and select it (`SELECT username, email FROM users WHERE id = $1`). Existing
  callers (the plugin-proxy header path) are unaffected — they read `Username`.
- `internal/requests`: introduce a narrow resolver interface
  ```go
  type RequesterIdentityResolver interface {
      ResolveRequester(ctx context.Context, userID int) (email, username string, err error)
  }
  ```
  injected into the requests `Service` (nil-safe: when nil, descriptors carry no
  email/username, i.e. plugins behave as `admin`). Wire it at
  `cmd/silo/main.go` (the `mediarequests.NewService(...)` call) from a small
  adapter over `plugins.PgUserIdentityLookup` (or a direct `users` query).
- The service resolves the requester **once per request** and the Fulfill
  `RequestDescriptor` (built by `routerDescriptor` in `internal/requests/provider.go`)
  carries `requester_email` / `requester_username`. Mechanism (resolved values
  carried on the in-flight request vs. threaded into the descriptor builder /
  provider call) is finalized in the plan. A resolver error degrades to empty
  strings (never blocks the request).
- Privacy: email is populated for every request_router Fulfill (arr ignores it —
  in-process only); it leaves the box only when the seerr plugin forwards it to
  Overseerr in `mapped` mode.

### 3. Seerr plugin (`silo-plugin-requests-seerr`)

**Config (`internal/router/config.go`):** parse from the connection config blob:
- `requester_mode` string — `"admin"` (default / empty) or `"mapped"`.
- `perm_request`, `perm_request_4k`, `perm_auto_approve`, `perm_auto_approve_4k`,
  `perm_manage_requests` booleans (only meaningful in `mapped` mode).

**Seerr user API (`internal/seerr`):**
- `FindUserByEmail(ctx, c, email) (*User, error)` — GET `/api/v1/user?take=N&skip=…`
  paginated; case-insensitive email match; returns nil when absent.
- `CreateUser(ctx, c, email string, permissions int) (*User, error)` — POST
  `/api/v1/user` `{ "email": …, "permissions": <bitfield> }`.
- `User` type: `{ ID int; Email string; Permissions int }`.
- `permissionBits(cfg)` builds the Overseerr bitfield from the five toggles. The
  exact bit values come from Overseerr `server/lib/permissions.ts` (e.g. REQUEST,
  REQUEST_4K, AUTO_APPROVE, AUTO_APPROVE_4K, MANAGE_REQUESTS); these are pinned as
  named constants in the plugin and verified against a live `/api/v1/user`
  round-trip during implementation.

**Fulfill (`internal/router/server.go`):**
- Resolve the Seerr user id for the request **once** (before the per-connection/
  per-quality loop), only when `mode == "mapped"` and `descriptor.requester_email`
  is non-empty:
  1. `FindUserByEmail`; if found → use `user.ID`.
  2. else `CreateUser(email, permissionBits(cfg))` → use the new id.
  3. on any error, or `admin` mode, or empty email → `userId = 0` (admin
     fallback), logged once.
  Resolution uses the FIRST connection's client (all connections of one seerr
  installation share the same Seerr instance); cache the resolved id for the
  request.
- `seerr.CreateRequestBody` gains `UserID int \`json:"userId,omitempty"\`` — set
  only when a mapped id was resolved (`omitempty` preserves today's admin body).
- The dedup/recovery (`409` / empty-2xx → `FindExistingRequest`) and `CheckStatus`
  paths are unchanged.

**Manifest `admin_form` (`manifest.json`):** add
- `requester_mode` SELECT with options "Admin user" (`admin`) / "Map to Silo users"
  (`mapped`), default `admin`.
- the five permission SWITCHes, each `show_when: requester_mode == "mapped"`.
- (`supports_4k` switch stays.)

## Data flow (mapped mode)

```
Silo user requests → host approves → Fulfill(descriptor{requester_email, …}, conns)
  └─ seerr plugin: mode==mapped, email present
       ├─ FindUserByEmail(email)  ──found──▶ userId = user.ID
       │                          └─none──▶ CreateUser(email, permBits) → userId
       └─ POST /api/v1/request { mediaType, mediaId, is4k, userId }
            └─ attributed to that user; auto-approves iff the user has AUTO_APPROVE
```

## Error handling

- Resolver/lookup/create failure or empty email → `userId` omitted (admin
  fallback); the request is never blocked by attribution. Log at WARN once per
  request with the email (or user id) and the cause.
- Seerr user-list pagination caps at a sane bound; if the email isn't found within
  the scan, treat as "not found" → create.

## Testing

- **SDK:** proto compiles; `GetRequesterEmail`/`GetRequesterUsername` exist.
- **Host:** `PgUserIdentityLookup` returns email; the requests service populates
  `RequesterEmail`/`RequesterUsername` on the descriptor from an injected fake
  resolver; a nil resolver yields empty identity (admin behavior); resolver error
  degrades to empty (request still fulfilled).
- **Seerr plugin** (httptest Seerr server, mirroring existing `api_test.go`):
  - `admin` mode → no `/api/v1/user` calls, body has no `userId` (current behavior).
  - `mapped` + existing user by email → `userId` set, no create call.
  - `mapped` + no match → `CreateUser` called with the expected permission bitfield,
    `userId` set to the created id.
  - `mapped` + empty email / list error / create error → admin fallback (no
    `userId`), request still submitted.
  - `permissionBits` assembles the correct bitfield from each toggle combination.
- Gates: SDK `buf generate` + `go build`; host `go test ./internal/requests/ ./internal/plugins/`,
  `go vet`; seerr plugin `go test ./...`.

## Rollout

No migration. Coordinated rebuild/redeploy (same loop as Spec B):
1. SDK: edit proto, regenerate, `go build`.
2. Host: implement, re-vendor SDK, rebuild `web/dist` (no FE change here, but the
   image bundles it) + image, redeploy.
3. seerr plugin: implement + manifest, rebuild binary, reinstall via
   `cmd/plugininstall` against installation id 6, `docker compose up -d silo`.

Manual verification: set a seerr connection to `mapped` with Auto-Approve on,
request as a non-admin Silo user whose email matches a Seerr user → the Seerr
request shows that user as requester and is approved; request as a user with no
Seerr account → a Seerr local user is created with the configured permissions and
the request is attributed to them; with Auto-Approve off → the request sits in
Seerr's pending queue under that user.
