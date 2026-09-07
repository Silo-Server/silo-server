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
| Playback v3 subtitle artifacts reporting the source codec as `format` and the video resume origin as `timing_origin_seconds` | Subtitle reliability, before v1 lock, [contract](playback-protocol-v3.md#8-track-identity-and-the-subtitle-ordinal-space) | Artifact metadata now describes actual served bytes and absolute source cue timestamps (origin zero). Apple clock mapping is coordinated; Android and web already map sidecar source time separately from the video timeline. Native embedded selection is separately negotiated with `embedded_subtitles_v1`, so clients cannot mistake an embedded decision for a missing sidecar. |
| String `GET`/`PUT`/`DELETE /api/v1/settings…`, the unknown-key extension bag, preference fields on profile/library/series DTOs | Cross-platform settings contract, [design](settings-contract.md) | Replaced wholesale by the typed settings contract. Deferring past lock would mean carrying the Deprecation/Sunset surface *and* the untyped key bag — which lets any client invent a production setting the server stores unvalidated — through the deprecation window, which is the exact surface the contract exists to close. |
| The ten string-registry admin user-settings routes: `GET /api/v1/admin/users/{id}/settings`, `GET /api/v1/admin/users/{id}/settings/{key}`, `PUT /api/v1/admin/users/{id}/settings/{key}`, `DELETE /api/v1/admin/users/{id}/settings/{key}`, `GET /api/v1/admin/users/{id}/device-settings`, `GET /api/v1/admin/users/{id}/device-settings/{key}`, `DELETE /api/v1/admin/users/{id}/device-settings/{key}`, `PUT /api/v1/admin/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}`, `DELETE /api/v1/admin/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}`, `DELETE /api/v1/admin/users/{id}/profiles/{profile_id}/devices/{device_id}/settings` | Cross-platform settings contract, [design](settings-contract.md) | The admin projection of the removal above: these routes read and wrote the string registry the contract replaces. Their canonical successors are `GET /api/v1/admin/users/{id}/settings/values` (every stored value across all scopes) and `PUT`/`DELETE /api/v1/admin/users/{id}/settings/values/{key}` at an explicit scope, sharing the session routes' validation. Keeping the string routes past lock would preserve an admin-only write path into the untyped bag after the user-facing one closed. |
| The legacy (pre-v3) request and response bodies of `POST /api/v1/playback/start`. The route stays; a body that does not declare `protocol_version: 3` now gets `426 client_upgrade_required` instead of a legacy plan | Playback protocol v3, [spec](playback-protocol-v3.md) | v3 is a platform-neutral contract that moves route selection, quality laddering and track choice server-side. The legacy body carried the opposite model — the client posted a decision it had already made. Running both means every planner change has to be made twice, in two shapes that disagree about who decides, and the legacy shape is the one the client-specific bugs live in. A deprecation window would extend that duplication across the whole window for a protocol no shipping client will still speak by lock. |
| `POST /api/v1/playback/transcode/start` | Playback protocol v3, [spec](playback-protocol-v3.md) | Client-posted transcode recipes. Superseded by the `quality_change` replan operation: the client sends a label from `playback_plan.available_qualities` and the server picks the encode. Keeping the endpoint keeps a second, unvalidated way to start a transcode that bypasses plan identity entirely. |
| `PATCH /api/v1/playback/{session_id}/audio` | Playback protocol v3, [spec](playback-protocol-v3.md) | Superseded by the `track_change` replan operation, which changes the audio track *and* returns the resulting plan. The PATCH mutated the session without re-planning, so a track change that invalidated the route left the client playing a plan the server no longer agreed with. |
| `409 protocol_disabled` on `POST /api/v1/playback/route-events`, and the `"enabled": false` shape of `GET /api/v1/playback/capability` | Playback protocol v3, [spec](playback-protocol-v3.md) | Both described a server with v3 switched off. With v3 the only playback protocol that state cannot exist — "disabled" would mean "no playback at all". The `enabled` field itself is kept and is constant `true`, so clients that feature-detect against it keep working; only the negative shape and the status code go. |
| The draft-v3 platform-specific wire vocabulary: `ClientPlaybackContextV3.features`, `.platform`, and `.engines`; `PlanV3.engine`; `output_route_generation` in start, replan, output-context, and route-event bodies; Android device/build fields (`brand`, `device`, `product`, `soc_*`, `build_*`, `security_patch`, `sdk_int`, `abis`); and the `media3_only` / `detailed_decode_capabilities` feature tokens | Platform-neutral playback protocol v3, [spec](playback-protocol-v3.md) | These names exposed one client's implementation as the cross-platform contract. Before v1 lock they are replaced by neutral delivery classes, evidence tiers, `device.platform` / `device.os_version` / bounded `platform_details`, opaque `output_context_id`, and top-level feature advertisement. Carrying both drafts through lock would force every client to translate Media3-specific aliases indefinitely and leave two conflicting sources of capability truth. |
| The `playback.local_transcode_fallback` server setting | Playback node-routing policy, 2026-08-29 | One boolean described only the worker-to-API execution fallback edge and could not express egress policy or different treatment for remux and video encode. It is migrated to the independent remux/video execution settings before removal, though `worker_only` is deliberately stricter than the boolean it replaces: it also covers container-only remuxes, which now route to a worker instead of running in the API process. Prepared downloads retain their separate behavior under `download.local_transcode_fallback`. |
| Accepting `access_group_id` alongside the admin role on `POST /api/v1/admin/users`, `PUT /api/v1/admin/users/{id}`, and `POST /api/v1/admin/invitations` — all three now reject the combination with `422` (`ErrAdminGrouped`, "Admin accounts cannot belong to an access group") | Admin-ungrouped constraint, 2026-08-22 | Admins are never grouped: the household access-group ceiling has no meaning for an account that already has server-wide admin rights, and silently accepting a group on an admin account left a stored value that read as a policy nobody enforced. Rejecting the combination at write time is cheaper to carry than a deprecation window for a field whose only valid value on an admin account was already `null`. |
| The `watch_provider_activity` object on `GET /api/v1/admin/stats`, with its `trakt_connected_profiles`, `trakt_enabled_profiles`, `trakt_export_enabled`, and `trakt_scrobble_enabled` fields | Watch-provider dashboard widget, 2026-08-28 | Every field was hardcoded to Trakt while the watch-provider subsystem is pluggable: Simkl, MDBList, and any plugin provider sync through the same tables and were invisible in it. The replacement is the additive `watch_providers` array, one entry per registered provider, advertised by `watch_providers` on `GET /admin/dashboard/capabilities`. The object had exactly one consumer — the admin web dashboard, which ships with the server — so a deprecation window would only preserve a shape that can never describe a second provider. |

Feature-detection precedent: clients discover which metadata providers (including the
built-in NFO provider, #216) apply to a library type via
`GET /api/v1/libraries/provider-defaults` rather than version sniffing. New capabilities
should follow the same capability-endpoint pattern.
