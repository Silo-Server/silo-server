# Admin settings UX

Admin settings are organized by admin intent ("I want subtitles to download
automatically"), not by subsystem, and they live in the admin sidebar beside
the tools they configure. There is no separate settings area: the sidebar is
one tree of sections and topics (`ADMIN_NAV_SECTIONS`,
`web/src/lib/adminNavigation.ts`), and every admin page, tool or settings,
appears in it exactly once.

- **Monitor**: Dashboard, Activity, Logs (Server Log, Client Diagnostics,
  Settings)
- **Manage**: Libraries (Libraries, Collections, Home Sections, Skip Markers,
  Subtitle Files, Settings), Requests (Queue, Settings), Users, Playback
  (Nodes, History, Settings), Downloads (Prepared Files, Settings)
- **Configure**: Server (General, Appearance, Storage & Database, Scheduled
  Tasks, Maintenance), Sign-in & Security (Sign-in, Security, Network Access,
  API Keys, Policy), Integrations (Plugins, Providers, Watch Providers, AI
  Services, Recommendations, Email, Notifications, Compatibility)

A topic that holds both tools and settings lists its own settings page last,
as plain "Settings"; the topic already says whose. A topic's heading only
opens and closes it (`SideNavTopic`); the topic holding the current page opens
by itself, and the rest stay as the admin left them (`useAdminNavTopics`).

Settings pages keep their own routes, `/admin/settings/:page`, and
`ADMIN_SETTINGS_NAV` (`web/src/lib/adminSettingsSearch.ts`) remains their
registry: id, label, description, and search index. The bare
`/admin/settings` opens the first page. Old `?tab=` URLs and retired page ids
from earlier layouts redirect to the page that absorbed them rather than
404ing (`LEGACY_ADMIN_SETTINGS_PAGE_ALIASES`). `⌘K`
(`AdminSectionCommandDialog`) is mounted in `AdminLayout`, and the search
field at the top of the desktop admin sidebar opens it; it lists the
sidebar's pages with each topic as its own group, so "Settings" always reads
under its topic, and it finds every indexed setting.

A setting lives on the page whose name covers it. Public signups and
self-service password reset only work with Silo passwords, so they sit with
password sign-in on Sign-in, as do the token lifetimes that the provider
re-check depends on. Logging holds every log setting, the server log and the
activity log retention alike. Trusted proxies sit beside the public URL on
General, as the setup wizard presents them. Subtitle sync is automatic audio
analysis of library media, like skip-marker detection, so it is on Libraries ›
Settings. The mail server has its own Email page, because invitations and
password resets need it as much as notifications do. Node routing (which
machine runs a remux or transcode and which serves the stream) is a section of
the Nodes page with its own save bar, because it only means anything once
nodes exist; its presets are the visible control and the five routes are the
Advanced tier.

What needs the admin (storage nobody configured, a restart a saved setting is
waiting on, mail that cannot send) is derived by `useSettingsAttention`
(`web/src/hooks/admin/useSettingsHealth.ts`) and listed once, as a "Needs
attention" notice on the Dashboard, each item linking to the page that fixes
it. The sidebar adds a dot only for what is broken (`warn`), on the page and on
its topic while that is closed; setup the admin has not done yet stays on the
Dashboard, so a server that deliberately runs without mail does not carry a
dot forever. A healthy item is never shown. Hovering or focusing a settings
page in the sidebar prefetches its code and values, so a click lands on a
filled form rather than a skeleton.

## User settings

Personal settings (`/settings`) keep their own sidebar, because the app
sidebar is for browsing. On desktop, opening settings swaps the app sidebar
for the settings sidebar (`SettingsSidebar`, `web/src/components/settings/`)
at the same width, so there is never a second navigation column. It holds a
back link to the last page the app showed outside settings
(`useSettingsReturnPath`), the current profile, search, and every page in its
groups. The root redirects to the first page on desktop; on a phone it is the
grouped list (`SettingsIndexList`). Crossing in or out is the one navigation
that changes what the column holds, so its contents slide in from the side the
reader is heading toward (`useSidebarSwap`), and the profile menu loads the
settings code when it opens.

## Visual system

One page is on screen at a time, and each thing on screen carries one signal.

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
a provider page (Providers, Watch Providers), not on the page that owns the
feature: Library & Metadata decides *whether* Silo looks for intro and
credits markers, while *which provider answers, in what order, and on what
terms* is a tile beside the subtitle and list providers, with a cross-link
each way. A tile only reads "Connected" when the provider could actually serve
a request — its configuration saved and the provider switched on — so an
installed plugin whose API key was never entered reads "Needs setup". Request
servers (Sonarr, Radarr) are the exception to the provider-page rule: they are
destinations that request routing chooses between, not interchangeable
providers, so the Requests page holds the servers, the routing rules that pick
one, and the request limits together. Routing starts on Standard, which needs
no setup (each request goes to the server for its type) and shows only a line
per media type; the rules appear under Advanced, which a second server of a
kind turns on. The Standard/Advanced switch, each routing rule and each media
type's Everything else carry their own validators and save as the admin goes;
the save pill covers only the general request settings. Staged edits raise
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
  essential controls per page above the fold. Placement on transcode nodes
  ("Generate … on", "Where to analyze audio") is Essential only while a
  transcode node exists; otherwise it is Advanced, and a disclosure opens by
  itself when a node-only choice has no node to run on, so its warning is
  never folded away. A setting is Essential only if a
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
  only reports that it is dirty; the settings layout mounts the guard once and
  blocks router navigation (the admin sidebar, browser back) with a
  confirmation. `useSettingsForm` keeps a `beforeunload` listener for tab
  close and reload, which the router never sees. Blocking is `useBlocker`,
  which is why `App.tsx` mounts a data router (`createBrowserRouter` +
  `RouterProvider`) — a page must never grow its own prompt, its own blocker,
  or its own draft store.
- `SettingsPageHeader` (`web/src/components/settings/`) — the one way a
  section names itself. Server health belongs in the Dashboard's attention
  notice, not repeated as a strip on every page.
- `SideNavSection`/`SideNavItem`/`SideNavTopic`/`SideNavSubItem`
  (`web/src/components/SideNav.tsx`) — the admin sidebar's building blocks.
  Pages never render their own nav; a new settings page is an entry in
  `ADMIN_SETTINGS_NAV`, its component in `AdminSettingsLayout`, and its place
  in `ADMIN_NAV_SECTIONS`.
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
