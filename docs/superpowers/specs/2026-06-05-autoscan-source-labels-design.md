# Autoscan source labels — design

**Date:** 2026-06-05
**Status:** Approved, pending implementation plan
**Scope:** Admin UI labeling of autoscan scan sources, generic across plugins, plus an operator-editable per-source label.

Commands assume the repository root is the cwd.

## Problem

Autoscan sources are labeled in the admin UI (Sources tab and Activity tab) by their
`capability_id` plus `plugin #<installation_id>`. The `arr` scan-source plugin fans out
one source per connection (Sonarr, Sonarr4k, Radarr, Radarr4k) under a single generic
`arr` capability, so every row rendered an identical "arr (plugin #4)" label. A prior
change made the Sources/Activity labels lead with the bound connection name, which fixes
`arr`, but the fallback for any other plugin is still the cryptic `capability_id` +
`plugin #N`.

Two gaps remain:

1. **Other plugins look cryptic.** A connectionless plugin (e.g. cephfs) or any future
   plugin still falls back to the raw `capability_id`, ignoring the human-friendly
   `display_name` the plugin already declares in its manifest.
2. **No operator control.** An operator cannot name a source themselves. This is the only
   fully-general answer for plugin shapes the automatic chain cannot disambiguate (e.g. a
   single plugin installation creating several connectionless sources distinguished only
   by their `source_config`).

## Goals

- Generic, plugin-agnostic labels driven by metadata that already exists
  (manifest `display_name`, connection `name`), so future plugins look good with **no
  host code per plugin**.
- An optional operator-editable label per source that overrides the automatic chain.
- A single shared label-resolution helper, consumed by both the Sources and Activity
  panels, so the logic is not duplicated.

## Non-goals

- Plugin-declared label templates in the manifest/SDK (the "option C" from brainstorming).
  Deferred: it requires a cross-repo SDK/manifest contract change. The chain below leaves a
  clean seam for it.
- Backend-side label resolution returned on API payloads. The resolution stays in the
  frontend helper for now; promote it to the backend only if the mobile clients need the
  same labels.

## Resolution chain

A source's display label resolves through four rungs, most-specific first:

```
operator label  ->  connection name  ->  manifest display_name  ->  capability_id
```

- **operator label** — the optional per-source text the admin sets (this design's new field).
- **connection name** — the operator-named `autoscan_connections.name` bound to the source.
- **manifest display_name** — from the plugin manifest, surfaced by the
  `scan-source-plugins` endpoint (`AutoscanAvailableSource.display_name`).
- **capability_id** — last-resort raw identifier; never worse than today.

The chosen rung is the **title**. The remaining identity (plugin display name / capability +
`plugin #<installation_id>`) renders as a **subtitle**, so no information is lost when a
higher rung wins. Example: operator label `4K Movies` as title, `Radarr4k · plugin #4` as
subtitle.

### Per-plugin behavior

| Source shape | Title | Subtitle |
| --- | --- | --- |
| arr, connection bound, no operator label | `Radarr4k` (connection) | `arr · plugin #4` |
| arr, operator label set | `4K Movies` (operator) | `Radarr4k · plugin #4` |
| cephfs, no connection | `CephFS Watcher` (manifest display_name) | `plugin #5` |
| future plugin, nothing declared | `capability_id` | `plugin #N` |
| Activity event whose source was deleted (`source_id` null) | manifest display_name or `capability_id` from the event's stored fields | `plugin #N` |

## Components

### Shared helper — `web/src/lib/autoscanLabels.ts` (new)

Pure, React-free, unit-tested.

- `composeSourceLabel({ operatorLabel, connectionName, displayName, capabilityId, installationId })`
  `=> { name: string; detail: string }` — implements the four-rung chain and the
  subtitle composition.
- `pluginDisplayNameKey(installationId, capabilityId) => string` — stable map key.
- `buildPluginDisplayNames(available: AutoscanAvailableSource[]) => Map<string, string>` —
  builds the `installation_id + capability_id -> display_name` lookup both panels use.

Future seam: `composeSourceLabel` may later accept an optional `configLabel` rung (for the
deferred plugin-template option) without changing either panel's call shape.

### Migration — `migrations/174_autoscan_source_label.{up,down}.sql` (new)

```sql
-- up
ALTER TABLE public.autoscan_sources ADD COLUMN label text NOT NULL DEFAULT '';
-- down
ALTER TABLE public.autoscan_sources DROP COLUMN label;
```

`label` empty string = unset; the resolution chain falls through to the automatic rungs.
174 is a fresh, unused migration number (no collision with the reused-172 history).

### Backend (Go)

- `internal/autoscan/types.go` — add `Label string` to the `Source` struct and the source
  input type.
- `internal/autoscan/repository.go` — add `label` to `sourceColumns`, `scanSource`, and the
  create/update statements.
- `internal/api/handlers/autoscan.go` — accept `label` on source update; include it in the
  `autoscanStatusSource` and source responses. Trim whitespace and cap length at 120
  characters server-side.

### Frontend

- `web/src/api/types.ts` — add `label` to `AutoscanSource` and `AutoscanSourceInput`.
- `web/src/pages/admin/autoscan/SourcesPanel.tsx` — replace the inline `sourceIdentity`
  block with a call to `composeSourceLabel`. Add an optional "Label" text input to the
  source row, saved on blur via the existing `fullBody` PATCH path (mirrors the interval
  field's `handleIntervalBlur` auto-save). Build the display-name map from the existing
  `useAvailableScanSources()` query.
- `web/src/pages/admin/autoscan/ActivityPanel.tsx` — add `useAvailableScanSources()`, build
  the display-name map, map `source_id -> source` to obtain the operator label and
  connection, and route `pollSourceName` / `scanSourceName` through `composeSourceLabel`,
  returning `.name`. Preserve the existing `"Autoscan"` fallback for scans with no
  capability.

## Edit UX

Inline per-row, reusing the existing on-blur auto-save pattern (no modal, no new
interaction model). The label input is blank by default with placeholder text indicating
it is optional. Saving sends the full row state through the same source-update mutation
used for connection/interval/rewrites.

## Error handling and edge cases

- **Deleted source on an Activity event** — `autoscan_events.source_id` is
  `ON DELETE SET NULL`; with a null `source_id` there is no operator label or connection,
  so the chain resolves from the event row's own stored `capability_id` / `installation_id`
  (plus manifest display_name if the plugin is still installed).
- **Uninstalled plugin** — `display_name` does not resolve; the chain falls to
  `capability_id`.
- **Bound connection** — `autoscan_sources.connection_id` is `ON DELETE RESTRICT`, so a
  bound connection always resolves to a name.
- **Length / whitespace** — the operator label is trimmed and capped at 120 characters
  server-side; an all-whitespace label normalizes to empty (unset).

## Testing

- `web/src/lib/autoscanLabels.test.ts` — covers each rung of the chain: operator label
  wins, connection name wins, manifest display_name wins, capability fallback, and the
  subtitle composition.
- Extend the existing autoscan repository/handler Go tests to cover the `label` column
  round-trip (create/update/read) and the server-side trim + length cap.

## Scope summary

One migration pair, three Go files, four frontend files (one new). No SDK changes, no
multi-repo coordination. Operator-editable labels (this design) and the automatic chain
share the same helper, so the deferred plugin-template option layers on later with no
rework.
