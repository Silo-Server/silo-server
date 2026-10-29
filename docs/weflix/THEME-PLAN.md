# WeFlix theme — findings and plan

Status: **blocked at Step 1.** Silo has no selectable theme system, so Step 2's
"register a new theme, selectable wherever themes are chosen today" has nowhere
to register into. What exists and the cheapest ways forward are below.

The plan file named in the task, `claude/SILO-WEFLIX-THEME.md`, was not present
in this checkout. This document was written from the task description instead.

## Findings

1. Silo ships exactly one theme. `web/index.html` hardcodes
   `<html data-theme="midnight-cinema">` and `web/src/app.css` holds a single
   `[data-theme="midnight-cinema"]` block that sets every semantic token.
2. Nothing anywhere selects a theme. There is no theme control for users or
   admins: `default_theme` appears nowhere under `web/src`, and the admin
   Appearance page stages only accent colour, card overlays, and title art.
3. `ui.theme` is deprecated: the web client no longer reads or writes it, and
   migration `20260926233851_retire_profile_themes` deleted the stored values.
4. Multi-theme support was removed on purpose. The theme catalog API (ratified
   2026-09-26) and the admin token/font/CSS overrides (ratified 2026-10-13) are
   both retired; only `branding.accent_color` still reaches the client.
5. Themes are token-only and cannot affect layout. The shell is structural in
   `web/src/components/Layout.tsx` and `AppSidebar.tsx`, not themed.
6. Three tests pin the single theme and would fail on a second theme block:
   `web/src/highContrastCss.test.ts` (`expect(themes).toHaveLength(1)`),
   `web/src/highContrastContrast.test.ts` (`THEMES = ["midnight-cinema"]`), and
   `internal/branding/render_test.go`.
7. App shell: `Layout.tsx` is the desktop rail plus the `Sheet` mobile drawer
   and the sticky mobile header; `AppSidebar.tsx` holds the nav and the avatar
   `DropdownMenu` that already carries the account menu items.
8. Search is `GlobalSearch.tsx`, opened by Ctrl/Cmd+K. Routes already exist for
   Home `/`, Requests `/requests`, Favorites `/favorites`, Watchlist
   `/watchlist`, Collections `/collections`, Calendar `/calendar`.
9. Library links are generated from the user-customizable primary menu
   (`web/src/lib/uiCustomization.ts`, `PrimaryMenuItem`) rendered by
   `AppSidebar`, so a top bar can reuse that list unchanged.
10. There is no i18n layer — no framework and no locale files. UI strings are
    literal English, so "reuse existing i18n strings" means reusing the same
    literals.

## Why this blocks Step 2

Step 2 asks for a theme that is selectable where themes are chosen today. No
such place exists, and every route to creating one is closed by the task's own
rules:

- Reusing `ui.theme` needs a new enum member in the shared settings contract
  (`contracts/settings/v1/manifest.json`), which Go validates. That is an API
  and contract change, excluded by "no backend/API changes".
- `ui.admin_theme_vars` and `ui.admin_custom_css` are no longer read by the web
  client at all, so they cannot carry a theme either.
- Hardcoding WeFlix as the only look would change the default theme, which the
  task forbids.
- A new client-side switcher needs a persistence home. Any server-side home is
  a backend change; a localStorage-only home is new product surface in a client
  that deliberately deleted theming, and it contradicts the three tests above.

## Cheapest options

| Option | Diff size | Keeps default theme intact | Notes |
|---|---|---|---|
| A. Fork-only rebrand | Small | No | WeFlix becomes the fork's only theme: edit the `data-theme` value, its token block, and the shell. Simplest by far. |
| B. Client-only switch | Medium | Yes | Add a second token block plus a `sidebar \| topnav` layout variant, switched from localStorage with a control added to Interface settings. No backend change. Requires updating the three single-theme guard tests. |
| C. Full multi-theme | Large | Yes | Re-enable `ui.theme` with a `weflix` value. Needs contract and backend changes, and reopens a ratified removal. |

Option B is the only one that satisfies both "WeFlix is selectable" and "the
default theme is unchanged" without touching the backend.

## Questions for Luther

- Which option (A, B, or C)? The task's premise that a theme picker exists is
  wrong, so this needs a decision before any code.
- Discover = Requests (`/requests`)? Artboard 1 shows "Discover" in the link
  row; Requests is the only matching route.
- Dropping the separate Home link in favour of the logo is fine as drawn, but it
  removes the labelled Home affordance. Confirm.
- Where should the top-nav layout live: only under the WeFlix theme, or as an
  independent user preference that any theme can use?
