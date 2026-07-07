# Access Groups — Design Spec

**Date:** 2026-07-02
**Branch context:** ships with the OPA policy engine PR (epic #272 addendum)
**Status:** Approved — implemented alongside the policy engine

## Goal

Give admins the everyday permission surface the Rego editor is not: named
**groups** with permission toggles, users assigned to a group, and per-user
restrictions layered on top. The policy engine stays as invisible
infrastructure; the Rego editor becomes an advanced, off-by-default feature.

User-visible outcome:

- A new **Groups** admin page: create groups ("Users", "Kids", "Guests"),
  toggle what each group may do (library access, playback-quality ceiling,
  downloads, transcoded downloads, stream/transcode limits, assignable
  permissions, media requests).
- The user editor gains a **Group** picker. A user's own restrictions still
  apply on top of their group.
- The Policy page (Rego editor, baseline viewer, decision log) is hidden
  unless `policy.editor_enabled` is turned on.

## Model

- One group per user (`users.access_group_id`, nullable). No group = no group
  layer, i.e. exactly today's behavior. Existing users are untouched.
- **Composition is restriction-only: the strictest layer wins.** This is the
  same rule the resolver already uses for account ∩ profile, extended one
  layer up. Group grants are an upper bound; per-user settings can only
  tighten further:
  - Libraries: intersection (NULL = unrestricted at that layer).
  - Playback quality: `MinQuality` across layers.
  - Booleans (downloads, transcoded downloads, requests): AND.
  - Stream/transcode limits: strictest positive value (0 = unlimited at that
    layer).
  - Assignable permissions: user's granted set ∩ group's allowed set
    (NULL = all assignable).
- To give one user *more* than their group allows, move them to a more
  permissive group. This keeps every layer a narrowing and avoids tri-state
  "inherit vs override" semantics on existing user columns.
- Content-rating ceilings stay a **profile** (parental) concern and are not a
  group field in v1.

## Enforcement

Group facts merge **in Go, before policy inputs are built** — a shared
`access.ApplyGroupPolicy(user, group)` computes the effective account-layer
facts consumed by the viewer-scope resolver, the downloads/playback action
inputs, the session-limit provider, the permission gates, and the request
creation gate. The vendor Rego, its parity suites, and the decision-log
pipeline are untouched; decision inputs simply carry the merged values.

Revision semantics mirror the existing per-user rule: changing a group's
playback-quality ceiling bumps `access_policy_revision` for its members
(invalidating profile PIN tokens); library/download/limit changes do not.

## Surface

- `access_groups` table + `users.access_group_id` (ON DELETE SET NULL).
- Additive API: `/api/v1/admin/access-groups` CRUD (list includes member
  counts); `PUT /admin/users/{id}` and the user DTO gain `access_group_id`.
- `policy.editor_enabled` server setting (default false, hot-reloaded) drives
  the capability endpoint's `editor_available`; the engine itself always runs.
- Web: Groups admin page + group picker in the user editor; Policy nav entry
  shown only when the capability reports the editor available.

## Deferred

- Multiple groups per user, grant-above-group per-user overrides, per-group
  request quotas, group-scoped custom Rego.
