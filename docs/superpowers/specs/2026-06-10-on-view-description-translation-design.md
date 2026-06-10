# On-view description translation + per-profile metadata language — design

**Date:** 2026-06-10
**Status:** Approved direction (per-profile preference; auto with button fallback; any user behind an opt-in setting)
**Builds on:** `docs/superpowers/specs/2026-06-10-ai-translation-and-asr-design.md`

Commands assume the repository root is the cwd.

## Problem

Metadata translation currently triggers from the admin editor or the per-library
refresh fallback, always targeting the library's metadata language. A viewer whose
language differs from the library's (or whose library was never auto-translated) has
no way to get descriptions in their language, and there is no per-person language
preference at all — presentation language is library-scoped.

## Design

### Per-profile preferred metadata language

- New column `user_profiles.preferred_metadata_language text NOT NULL DEFAULT ''`
  (empty = inherit the library's metadata language). Threaded through the profile
  model/repo/API exactly like the existing `language` (UI language) field, plus a
  picker in the profile settings UI with an "Inherit library default" option.
- `access.Scope` gains `PreferredMetadataLanguage`, populated when the access
  resolver loads the profile, so every request already carries it.
- `catalog.AccessFilter` gains `ProfilePreferredLanguage`, and presentation language
  resolution becomes: explicit `PresentationLanguage` (request param) →
  **profile preference** → library `metadata_language`. Applied wherever the scope
  is available (native API and jellycompat), so all clients get profile-localized
  metadata server-side.

### "Not in your language" signal

`ItemDetail` gains `pending_translation_language` (omitted when empty): set when the
resolved presentation language differs from the item's default metadata language,
the base overview is non-empty, and no localization overview exists for that
language. It is pure data — clients combine it with the AI feature status to decide
what to render.

### On-view trigger

- New setting `metadata_ai.on_view`: `off` (default) | `button` | `auto`, surfaced
  on `GET /api/v1/metadata/ai/status` as `on_view`.
- New authenticated endpoint `POST /api/v1/items/{id}/translate-description`
  `{target_language}` — any profile, gated by `metadata_ai` readiness +
  `on_view != off`, item access enforced with the same `EnsureAccessible` check the
  watched-state endpoints use. The client echoes back the language the server
  reported in `pending_translation_language`.
- The endpoint goes through a new `Service.RequestOnView`, which adds one guard on
  top of the existing pipeline: if the latest job for (content, language) failed
  within a cooldown window, return it instead of enqueuing — a broken endpoint must
  not be retried on every page view. Existing guarantees do the rest: in-flight
  idempotency collapses concurrent viewers onto one job, skip-if-filled makes
  repeats free, and the shared semaphore caps endpoint load.

### Web UX

On the detail page, when `pending_translation_language` is set:

- `on_view=auto`: fire the request on view (once per item), pulse-animate the
  description while waiting, and poll the detail query until the flag clears (the
  job's first batch translates the item's own overview, so this lands in seconds
  even when a series job continues episodes in the background). Time out after ~45 s
  and show the original text — no error states in the viewer's face.
- `on_view=button`: a small "Translate" chip near the description triggers the same
  flow on click.

No job-status endpoint for viewers: completion is observed as the flag clearing on
refetch; failures surface only as the shimmer timing out (the cooldown stops
re-triggering).

## Non-goals

- Per-profile language in the Android/Apple UIs (server-side serving means they
  already *receive* localized metadata; surfacing the trigger there is a client
  follow-up using the same endpoint).
- Translating titles, or any admin-side behavior changes.
