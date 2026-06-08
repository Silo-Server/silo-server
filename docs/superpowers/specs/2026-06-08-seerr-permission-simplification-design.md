# Simplify Seerr mapped-user permissions (1080p-always + 4K + auto-approve)

Status: design • 2026-06-08
Scope: `silo-plugin-requests-seerr` only (config + permission computation +
manifest). No SDK/host change. Refines the just-shipped per-user mapping.

## Context

The mapped-mode Seerr connection currently exposes five default-permission
toggles (`perm_request`, `perm_request_4k`, `perm_auto_approve`,
`perm_auto_approve_4k`, `perm_manage_requests`) which the plugin ORs into the
created user's Overseerr permission bitfield. This is more granular than needed
and lets an operator misconfigure (e.g. deny basic requesting). The desired model
mirrors the arr plugin: everyone can request 1080p; 4K eligibility follows the
requester's max playback quality (host-decided); two simple operator toggles.

## Goal

Reduce the mapped-user permission config to **two toggles** and make 4K
eligibility per-user (driven by the requester's max quality, exactly as the arr
plugin's 4K is), with a blanket override toggle.

## Decisions (locked)

- Everyone can always request 1080p (`REQUEST` always granted; no toggle).
- Remove `manage_requests` entirely.
- Per-user 4K: grant `REQUEST_4K` when the Fulfill request contains a 4K quality
  (the host only sends a 2160p quality when the requester's max quality is
  4K/Any — same `allowedQualities` gate the arr plugin rides). No per-user lookup.
- A blanket **`request_4k_all`** toggle (default off): also grant `REQUEST_4K` to
  every mapped user regardless of their max quality. Permission-only — the host
  still decides whether a 4K request is actually sent (it won't send 2160p for a
  1080p-capped user unless global force-dual is on).
- A single **`auto_approve`** toggle (default on): grant `AUTO_APPROVE` (and
  `AUTO_APPROVE_4K` when the user is 4K-eligible) so mapped requests auto-approve;
  off → they land in Seerr's per-user pending queue.

## Permission bitfield (computed at user-create time)

```
bits = REQUEST                                   // always
if requestHas4K || request_4k_all: bits |= REQUEST_4K
if auto_approve:
    bits |= AUTO_APPROVE
    if requestHas4K || request_4k_all: bits |= AUTO_APPROVE_4K
```
where `requestHas4K = any(q.GetIs4K() for q in FulfillRequest.Qualities)`.

(Overseerr bits, verified: REQUEST=32, REQUEST_4K=1024, AUTO_APPROVE=128,
AUTO_APPROVE_4K=32768.)

## Components (all in `silo-plugin-requests-seerr`)

- **`internal/router/config.go` `Connection`:** drop `PermRequest`,
  `PermRequest4K`, `PermAutoApprove`, `PermAutoApprove4K`, `PermManageRequests`;
  add `Request4KAll bool` (`request_4k_all`) and `AutoApprove bool`
  (`auto_approve`). Keep `Mapped`, `RequireMappedUser`, `Supports4K`.
- **`internal/seerr/types.go`:** keep the exported `PermRequest`/`PermRequest4K`/
  `PermAutoApprove`/`PermAutoApprove4K` consts; remove `PermManageRequests` and
  the 5-bool `PermissionBits` helper (no longer fits — the new computation needs
  `requestHas4K`, which lives in `package router`).
- **`internal/router/server.go`:** add `userPermissions(conn Connection,
  requestHas4K bool) int` implementing the bitfield above. In `Fulfill`, compute
  `requestHas4K` from `req.GetQualities()` and pass it into
  `resolveRequesterUserID`, which uses it for `CreateUser`'s permissions. Existing
  user attribution (find-by-email, `userId` on the body, admin fallback,
  `require_mapped_user`) is unchanged.
- **`manifest.json` admin_form:** remove the five `perm_*` fields; add
  `request_4k_all` (SWITCH, default false, "Allow all users to request 4K") and
  `auto_approve` (SWITCH, default true, "Auto-approve requests"), both
  `show_when: requester_mode == "mapped"`. Update `json_schema.properties`
  accordingly (every admin_form key must be listed, or `LoadWithChecksum` rejects
  the manifest).

## Non-goals

- No retroactive permission updates for an already-created Seerr user whose max
  quality later changes (permissions are set at create time, as today).
- No SDK/host change — 4K eligibility is read off the qualities the host already
  sends.

## Testing

- `config_test.go`: parses `request_4k_all` + `auto_approve`; old `perm_*` keys
  gone.
- `server_test.go`:
  - mapped, 1080p-only request, `auto_approve` on → created user permissions =
    `REQUEST|AUTO_APPROVE` (160), no `REQUEST_4K`/`AUTO_APPROVE_4K`.
  - mapped, request includes a 4K quality, `auto_approve` on → permissions add
    `REQUEST_4K|AUTO_APPROVE_4K`.
  - mapped, 1080p-only request, `request_4k_all` on → permissions include
    `REQUEST_4K` even without a 4K quality.
  - `auto_approve` off → no `AUTO_APPROVE*` bits.
  - admin mode + the existing find/create/fallback/`require_mapped_user` tests
    still pass.
- `go test ./...` (incl. `TestEmbeddedManifestLoads` / `TestAdminFormLayout`).

## Rollout

Rebuild the seerr plugin binary, reinstall via `cmd/plugininstall` (installation
id 6), `docker compose restart silo`. No host redeploy, no SDK change, no
migration. (A live mapped connection configured with the old `perm_*` keys, if
any, simply loses them — re-toggle the two new switches.)
