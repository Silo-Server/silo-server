# Admin settings UX

Admin settings are organized by admin intent ("I want subtitles to download
automatically"), not by subsystem. `/admin/settings` is the **Overview**:
server health across the top and one live card per settings group. Twelve
standalone pages hang off it: General, Storage & Database, Appearance,
Security & Access, Library & Metadata, Playback, Downloads, Subtitles &
Metadata, Watch Providers, AI Services, Notifications, and Compatibility. The
global admin sidebar has one Settings destination; the Overview owns the
settings information architecture. Old `?tab=` URLs and retired page ids from
earlier layouts (including `integrations`, now split into Subtitles & Metadata,
Watch Providers, and AI Services) redirect to the page that absorbed them
rather than 404ing.
`⌘K` (`AdminSectionCommandDialog`) is mounted in `AdminLayout` so search works
from every admin page, not just the Dashboard.

## Visual system

One page is on screen at a time, and each thing on screen carries one signal.
The admin settings detail view deliberately mirrors the user settings page
(`SettingsLayout`): one `surface-panel-lg` shell with a `SideNavItem` rail on
the left and the page content on the right, so the two settings surfaces read
as the same product. The Overview stays the category directory: it explains
each group's scope, and every category has its own `/admin/settings/:page`
route. The rail (`SettingsPageRail`, rendered by the settings shell) lists
every settings page with the open one marked; an All settings link above the
shell leads back to the directory. The rail is desktop-only — on smaller
screens the Overview is the directory, exactly as on the user side. The
Overview shows a health tile only for a tile in `warn` or `off`. The
**Setup & health** section explains that it holds recommendations and
configuration problems; an empty checklist reads "No action needed" and
names the conditions that will appear there. Below it is one card per
settings group. Each card explains the group's scope and names the sections
inside it. Live state stays in the health area instead of reducing a
multi-provider group to one misleading summary.

A category page opens with `SettingsPageHeader`: the title, and page actions
if it has any. No breadcrumb, no lede, no status strip. Below it, settings
are hairline-ruled rows inside `FieldGroup`s — thin wrappers over the shared
`SettingsGroup` panel the user settings pages use — one panel per group,
never per field. The Advanced tier stays inline as one disclosure row per
group. A row can carry its `server_settings` key as a mono caption under the
label (`SettingField`'s `settingKey`) so an admin can match the UI to the
API and environment overrides, and a violet dot marks unsaved edits on the
row and on the group heading (`dirty`, driven from `form.isDirty`). A
description under a field label is the exception, not the rule: one short
sentence, and only when the label alone is ambiguous. Units live beside the
control (`SettingField`'s `unit`), not in the label. When every field in a
group needs a restart, the group says so once (`FieldGroup restartAll`) and
the fields inside drop their chips. Provider credentials are `ProviderTile`s
that expand in place to Test before saving; their border is neutral in every
state and the state is a dot plus a word in the header. Provider setup lives on
a provider page (Subtitles & Metadata, Watch Providers), not on the page that
owns the feature: Library & Metadata decides *whether* Silo looks for intro and
credits markers, while *which provider answers, in what order, and on what
terms* is a tile beside the subtitle and metadata providers, with a cross-link
each way. A tile only reads "Connected" when the provider could actually serve
a request — its configuration saved and the provider switched on — so an
installed plugin whose API key was never entered reads "Needs setup". Staged edits raise
one floating save pill (`SaveBar`) and arm the shell's unsaved-changes prompt;
the restart prompt is a single `RestartBanner` (`web/src/components/admin/`)
rendered by the admin shell (`AdminLayout`), never per page. A restart is owed
by the server, not by the page that asked for it, so the banner sits in the
flow at the top of the content column on *every* admin page — dashboard,
users, tasks, settings — and follows the admin around until they restart or
dismiss it.

## Three tiers, and how to pick one for a new setting

Every admin setting is one of:

- **Essential** — shown by default, no disclosure needed. Target at most ~8
  essential controls per page above the fold. A setting is Essential only if a
  household admin on a single-node install would plausibly need it without
  being told to look for it (on/off toggles for a whole feature, the handful
  of values that make the feature usable at all).
- **Advanced** — correct but not essential; collapsed by default behind one
  `AdvancedSection` disclosure per page (or per `FieldGroup` on a dense page).
  Open state persists in `localStorage` and auto-expands when a dirty or
  invalid field lives inside it. Tuning knobs, alternate backends,
  and anything whose default is good enough that most admins never touch it
  belong here.
- **Hidden** — no UI at all, on any page. The setting is still a normal
  `server_settings` row: readable and writable through the admin settings API
  and environment configuration exactly as before this reorganization. Use
  Hidden for legacy key families kept for compatibility, settings that only
  make sense with expert knowledge of the codebase, or values better derived
  automatically (e.g. from node pool capacity) than hand-set.

The tier is a UI-only decision. It must never change a key's validation,
default resolution, or API visibility — moving a setting to Hidden is
reversible by adding UI back, not by a data migration.

## Shared primitives

Reuse these instead of adding a bespoke variant per page:

- `SettingField` / `FieldGroup` / `SaveBar` (`web/src/pages/admin-settings/`)
  and `useSettingsForm` (`web/src/hooks/`) — the one save model. Every page
  batches edits and commits them through one `SaveBar` with Discard; provider
  credentials are the only exception, and only because they need
  Test-before-commit, which is `ProviderTile` rather than a bespoke card per
  provider.
- `UnsavedChangesGuard` (`web/src/components/`) plus `useReportUnsavedChanges`
  (`web/src/hooks/useUnsavedChanges.ts`) — the one unsaved-edits prompt. A form
  only reports that it is dirty; the settings shell mounts the guard once and
  blocks router navigation (rail, back link, admin sidebar, browser back) with
  a confirmation. `useSettingsForm` keeps a `beforeunload` listener for tab
  close and reload, which the router never sees. Blocking is `useBlocker`,
  which is why `App.tsx` mounts a data router (`createBrowserRouter` +
  `RouterProvider`) — a page must never grow its own prompt, its own blocker,
  or its own draft store.
- `SettingsPageHeader` (`web/src/components/settings/`) — the one way a
  section names itself. Live state belongs on the Overview, not repeated as a
  strip on every page.
- `SettingsPageRail` (`web/src/components/settings/`) — the one sibling nav,
  rendered once by the settings shell from `ADMIN_SETTINGS_NAV` using the
  shared `SideNavSection`/`SideNavItem` primitives. Pages never render their
  own nav or add entries directly to the rail.
- `SettingsGroup` (`web/src/components/settings/`) — the one settings panel,
  shared with the user settings pages; admin pages reach it through the
  `FieldGroup` wrapper, which layers on the restart-all line, the
  unsaved-edits dot, and the restart context.
- `AdvancedSection` — the one collapsible-disclosure primitive for the
  Advanced tier. Do not add another `<details>`, another bespoke collapsible
  component, or a per-page expand/collapse toggle.
- `SecretField` — the one credential control: an always-editable password
  input whose masked placeholder stands in for the saved value. Typing stages
  a replacement; emptying the input keeps the saved secret, so no ordinary save
  erases one by accident. Clearing is always a deliberate act, and every
  surface has exactly one way to do it: either a page-level action (Disconnect,
  Clear credentials) or, where the page has none, the field's own opt-in
  `onClear`/`cleared` affordance, which stages the empty write for the save bar
  and can be taken back with "Keep saved value" or Discard.
- `LimitField` — the one "Unlimited" checkbox pattern, replacing "0 = unlimited"
  hint text conventions.
- A restart badge on `SettingField` itself, sourced from
  `config.RestartRequired` (`internal/config/restart_keys.go`), not hand-copied
  into hint text or inferred by a page-local heuristic. `RestartRequired` is
  the single source of truth for which keys need a process restart to take
  effect; a new field's badge must read that function (directly, or via a
  manifest/meta endpoint built on top of it) rather than duplicating its
  judgment.

## Deferred out of this reorganization

These were identified during the review but deliberately left for later work,
not folded into this pass:

- Key renames (e.g. un-namespaced `allow_4k_transcode`,
  `enable_transcode_throttle`, `transcode_throttle_seconds` moving under
  `playback.*`).
- Introducing `server.public_url` as one canonical public URL that
  `jellyfin_compat.public_url` and friends would derive from.
- Deleting the legacy `s3.operational_*` rows that a past migration copied and
  never removed.

Each still applies via its existing key and behavior; only the UI
reorganization and tiering in this document shipped now.
