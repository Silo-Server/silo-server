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

Feature-detection precedent: clients discover which metadata providers (including the
built-in NFO provider, #216) apply to a library type via
`GET /api/v1/libraries/provider-defaults` rather than version sniffing. New capabilities
should follow the same capability-endpoint pattern.
