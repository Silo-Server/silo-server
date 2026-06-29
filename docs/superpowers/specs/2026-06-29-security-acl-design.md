# Security ACL Design

Date: 2026-06-29

## Goal

Replace Silo's current binary `user` / `admin` authorization model with a full access-control-list policy engine that can support tiered security, customizable groups, media access rules, playback restrictions, and explainable access decisions.

The first implementation must preserve current behavior while introducing the engine and migration path. Later phases can expose richer group and rule management in the admin UI.

## Current State

Silo currently stores a `role` string and a `permissions` string array on each user. Most privileged routes check whether the authenticated claim role equals `admin`. A small feature-permission layer exists for `marker_edit` and `metadata_curation`; admins implicitly receive those permissions, while normal users need them assigned.

The user model already includes several policy-oriented fields:

- assigned library IDs
- maximum playback quality
- access policy revision
- maximum streams
- maximum transcodes
- maximum profiles
- direct download permission
- assigned permissions

Those fields should be treated as existing policy inputs during migration rather than discarded.

## Design Principles

The backend policy engine should be more flexible than the initial UI. The UI should begin with understandable presets and group editors, while the engine stores and evaluates ACL rules.

Authorization must become action-based rather than role-string-based. API code should ask whether an actor can perform a specific action against a specific resource, not whether the actor is an admin.

Access decisions must be explainable. Admins need to know which rule allowed or denied a request, especially once users can belong to multiple groups.

The first production rollout must be behavior-preserving. Existing admins remain admins. Existing users keep their library restrictions, playback limits, download settings, and current feature permissions.

## Core Model

An actor is a user, plus the groups they belong to, plus any direct user-level overrides.

A policy rule has:

- subject: user, group, built-in role, or everyone
- action: what is being attempted
- resource: what the action targets
- effect: allow or deny
- conditions: optional constraints that refine when the rule applies
- priority: deterministic ordering when multiple rules match at the same specificity
- metadata: name, description, created_by, updated_at, and audit fields

Groups are named collections of ACL rules. Built-in roles are seeded as default groups, not special cases spread across the codebase.

## Built-In Groups

Initial seeded groups should include:

- Owner: full control over server, security, users, settings, libraries, tasks, plugins, media access, playback, downloads, and diagnostics.
- Admin: broad operational control, but owner-only actions remain reserved for the owner group.
- Library Manager: manage libraries, scans, media organization, and library-scoped tasks.
- Metadata Curator: edit metadata, posters, markers, and provider matches for allowed libraries.
- Viewer: play allowed media under normal playback policy.
- Restricted Viewer: play allowed media with tighter default playback and download limits.

These groups are seed data. Admins can later create custom groups and can eventually edit most seeded group memberships, while owner-only safety rules remain protected.

## Actions

The initial action set should cover existing behavior and obvious near-term needs:

- `server.view`
- `server.configure`
- `security.manage`
- `users.view`
- `users.manage`
- `users.impersonate`
- `libraries.view`
- `libraries.manage`
- `tasks.view`
- `tasks.run`
- `logs.view`
- `plugins.view`
- `plugins.manage`
- `nodes.view`
- `nodes.manage`
- `metadata.curate`
- `markers.edit`
- `playback.play`
- `playback.transcode`
- `downloads.direct`
- `downloads.transcode`
- `profiles.manage`
- `requests.create`
- `requests.approve`

Existing `marker_edit` maps to `markers.edit`. Existing `metadata_curation` maps to `metadata.curate`.

## Resources

The first resource model should include:

- server
- security settings
- users
- groups
- libraries
- media item
- media type: movie, series, episode, audiobook, ebook, music
- tasks
- logs
- plugins
- remote nodes
- profiles
- requests

Resource matching should support hierarchy. For example, a rule on a library applies to items in that library. A rule on `media_type:movie` applies to movie items across allowed libraries unless narrowed by another resource scope.

## Conditions

Conditions should be represented as structured JSON with typed evaluation helpers, not as free-form expressions. The initial condition set should support:

- allowed library IDs
- media types
- profile context, including primary profile behavior
- maximum playback quality
- maximum concurrent streams
- maximum concurrent transcodes
- direct downloads allowed
- transcoded downloads allowed
- content rating limits

Advanced conditions such as network location, time windows, device class, or remote access can be added later without changing the core rule model.

## Precedence

Policy evaluation must be deterministic and documented:

1. Disabled users are denied.
2. Owner allow rules bypass ordinary denies for operational actions, except audit logging still occurs.
3. Direct user deny rules win over direct user allow rules.
4. Direct user allow rules win over group deny rules.
5. Group deny rules win over group allow rules.
6. Group allow rules grant access.
7. Legacy default allow applies only to existing normal playback paths during migration.
8. Everything else is denied by default.

This gives direct user overrides clear meaning while keeping group denies useful.

## Restrictions and Limits

ACL rules answer whether an action is allowed. Playback and account limits also need an effective policy result.

Effective restrictions should merge as follows:

- library access: union of allowed libraries
- allowed media types: union
- allowed capabilities: resolved by ACL action evaluation
- maximum quality: highest allowed quality across matching allow rules, unless a direct user deny or condition excludes the request
- maximum streams: highest matching limit
- maximum transcodes: highest matching limit
- downloads: allowed only when the relevant download action is allowed
- rating limits: most restrictive matching limit unless a more specific direct user allow grants an exception

The evaluator should return both the boolean decision and the effective limits used for playback/download decisions.

## Explain Access

The policy package should expose an explain mode that returns:

- final decision: allow or deny
- requested actor, action, and resource
- matched rules in evaluation order
- winning rule
- effective limits used
- reason code

The admin UI should eventually expose this as an "Explain access" tool on user, group, library, and item screens.

## Data Model

Initial tables:

- `acl_groups`: custom and seeded groups
- `acl_group_members`: user-to-group memberships
- `acl_rules`: allow/deny rules for users, groups, built-in roles, or everyone
- `acl_policy_revisions`: global policy revision tracking for cache/session invalidation
- `acl_rule_audit`: optional append-only rule mutation log if the existing activity log is not enough

Existing user columns should remain during the migration:

- `role`
- `permissions`
- `library_ids`
- playback/download/profile limit columns
- `access_policy_revision`

The resolver should initially read both the new ACL tables and legacy fields. Once every caller uses the policy service, legacy fields can be converted to seeded rules or compatibility projections.

## API Shape

Backend code should move toward a central authorization service:

```go
type Authorizer interface {
    Authorize(ctx context.Context, request AccessRequest) (AccessDecision, error)
    Explain(ctx context.Context, request AccessRequest) (AccessExplanation, error)
    EffectivePolicy(ctx context.Context, userID int, scope PolicyScope) (EffectivePolicy, error)
}
```

Middleware should use action-specific gates:

```go
RequireAction("users.manage")
RequireActionForItem("metadata.curate")
RequireActionForLibrary("libraries.manage")
```

The existing `RequireAdmin` middleware should remain temporarily as a compatibility wrapper around owner/admin-equivalent ACL actions, then be retired route by route.

## Session and Cache Invalidation

JWTs currently carry role information. ACL decisions should not permanently trust stale role or permission claims.

The policy system should use the existing access policy revision concept. When group membership, direct user overrides, or security-sensitive rules change, affected sessions should be invalidated or forced to refresh. Short-lived per-request or short-TTL policy caches are acceptable if keyed by user ID and policy revision.

## UI Direction

The first UI should avoid a raw rule-builder as the default experience.

Initial admin screens should expose:

- users and assigned groups
- built-in groups
- custom groups
- group capabilities
- library/media scopes
- playback and download limits
- direct user overrides
- explain access

An advanced ACL editor can come later once the safe group-based workflow is proven.

## Migration Plan

Phase 1: Foundation with no behavior change.

- Add action/resource constants.
- Add policy engine interfaces and tests.
- Seed ACL groups and compatibility rules.
- Keep existing `role` and `permissions` behavior intact.

Phase 2: Route conversion.

- Convert admin-only middleware and handlers to action-specific checks.
- Keep `RequireAdmin` as a wrapper for unconverted routes.
- Add coverage for representative admin, media, marker, metadata, download, and playback paths.

Phase 3: Media and playback policy.

- Route library access, playback, transcoding, and download decisions through the policy service.
- Preserve current user limits during migration.
- Add explain output for denied playback/download decisions.

Phase 4: Group UI.

- Add admin UI for group assignment and group-scoped capabilities.
- Add direct user overrides.
- Add explain access UI.

Phase 5: Advanced ACL.

- Add advanced rule editing.
- Add richer conditions only after the core model is stable.
- Consider migration away from legacy role/permission columns once compatibility is no longer needed.

## Testing Strategy

Unit tests should cover:

- allow/deny precedence
- group membership merging
- direct user overrides
- owner/admin compatibility
- legacy `marker_edit` and `metadata_curation` mapping
- library-scoped access
- playback/download limit merging
- disabled user denial
- explain output stability

Integration tests should cover:

- admin route authorization
- metadata curation by library-scoped curator
- marker editing by assigned user
- playback allowed/denied by library and media type
- direct download allowed/denied
- policy revision invalidation after group changes

Frontend tests should cover:

- user/group form state
- capability toggles
- direct overrides
- explain access display
- preservation of existing admin/user behavior

## Rollout Safety

The ACL engine must be introduced behind compatibility behavior first. A deployment should not suddenly lock out admins or change media access.

At least one owner-capable account must exist after migration. The first migration should promote the oldest enabled admin account to Owner and assign every other enabled admin to Admin. If no admin exists, the existing first-user/bootstrap behavior must still create an owner-capable account.

Every denied privileged request should return a stable error code and be logged with enough context to diagnose the decision through explain access.

## Open Decisions

The initial design chooses full ACL internally, group-oriented UI externally, and incremental migration. The advanced rule-editor UI can be finalized during implementation planning because it does not change the core model.

## Foundation Implementation Notes

The first implementation pass adds the ACL data model, constants, evaluator, compatibility mapping, repository, and authorizer facade without converting existing route middleware. Current behavior remains controlled by the legacy role and permission paths until route-specific ACL checks are introduced in later PRs.
