# Silo v1 Scope

**Status: NOT LOCKED — proposal window open.**

Propose capabilities with the **v1 capability proposal** issue template; triage happens on the
[Silo v1 project](https://github.com/orgs/Silo-Server/projects/5).

When the scope locks, this file becomes the source of truth and will contain:

1. **Locked capabilities** — a table of capability epics (issue links) with one-line scope statements.
2. **API policy** — additive-only within `/api/v1` (no field renames/removals, no type changes,
   no status-code repurposing; removals only via the Deprecation/Sunset header flow; capability
   endpoints for feature detection). Contract tooling: #135.
3. **Amendment rules** — after lock, this file changes only via PR with code-owner review.
   An amendment PR is the exception process: it must say what changes, why it cannot wait
   for v1.1, and what it displaces.

Until lock: treat any capability not tracked as `Proposed`/`Locked` on the project as out of scope
for feature PRs (see the scope gate in `CLAUDE.md`).

## Breaking removals taken before lock

The additive-only rule in item 2 binds at lock. Before then a removal is in scope, and there is no
amendment to write because the amendment process in item 3 does not exist yet. `CLAUDE.md` states
the rule without that qualifier, which reads as a contradiction — it is not, but a removal taken
now has to be recorded here so a reader after lock can tell a deliberate decision from a violation.

Each entry names what goes, why waiting is worse, and the design that decided it. **Every removal
listed here must have shipped before the scope locks.** One still outstanding at lock loses its
justification and falls back to the Deprecation/Sunset flow like anything else.

| Removed | Release | Rationale |
|---|---|---|
| String `GET`/`PUT`/`DELETE /api/v1/settings…`, the unknown-key extension bag, preference fields on profile/library/series DTOs | Cross-platform settings contract, [design](../superpowers/specs/2026-07-10-cross-platform-user-settings-contract-design.md) | Replaced wholesale by the typed settings contract. Deferring past lock would mean carrying the Deprecation/Sunset surface *and* the untyped key bag — which lets any client invent a production setting the server stores unvalidated — through the deprecation window, which is the exact surface the contract exists to close. |
| The ten string-registry admin user-settings routes: `GET /api/v1/admin/users/{id}/settings`, `GET /api/v1/admin/users/{id}/settings/{key}`, `PUT /api/v1/admin/users/{id}/settings/{key}`, `DELETE /api/v1/admin/users/{id}/settings/{key}`, `GET /api/v1/admin/users/{id}/device-settings`, `GET /api/v1/admin/users/{id}/device-settings/{key}`, `DELETE /api/v1/admin/users/{id}/device-settings/{key}`, `PUT /api/v1/admin/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}`, `DELETE /api/v1/admin/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}`, `DELETE /api/v1/admin/users/{id}/profiles/{profile_id}/devices/{device_id}/settings` | Cross-platform settings contract, [design](../superpowers/specs/2026-07-10-cross-platform-user-settings-contract-design.md) | The admin projection of the removal above: these routes read and wrote the string registry the contract replaces. Their canonical successors are `GET /api/v1/admin/users/{id}/settings/values` (every stored value across all scopes) and `PUT`/`DELETE /api/v1/admin/users/{id}/settings/values/{key}` at an explicit scope, sharing the session routes' validation. Keeping the string routes past lock would preserve an admin-only write path into the untyped bag after the user-facing one closed. |

Feature-detection precedent: clients discover which metadata providers (including the
built-in NFO provider, #216) apply to a library type via
`GET /api/v1/libraries/provider-defaults` rather than version sniffing. New capabilities
should follow the same capability-endpoint pattern.
