# WeFlix theme — plan, findings and verification

A top-bar shell for this fork, selected by a new `weflix` theme. This file is the
repository copy of the plan; the working plan lives in the claude.ai project.

## What this adds

- A second theme, `weflix`, defined in new files and made the default in this
  fork. Upstream's `midnight-cinema` stays selectable and unchanged.
- A layout variant carried by the theme: WeFlix draws a top bar
  (`TopNav.tsx`), Midnight Cinema keeps the left rail.
- No backend, API, settings-contract, or playback change. The choice is
  device-local, so nothing is written to the settings API.

## Findings — how Silo theming actually works

1. Silo ships exactly one theme. `web/index.html` hardcodes a static
   `data-theme`, and `web/src/app.css` holds a single `[data-theme=...]` block
   that sets every semantic token.
2. Nothing selects a theme. There is no theme control for users or admins:
   `default_theme` appears nowhere under `web/src`, and the admin Appearance
   page stages only accent colour, card overlays, and title art.
3. `ui.theme` is deprecated. The web client no longer reads or writes it, and
   migration `20260926233851_retire_profile_themes` deleted the stored values.
4. Multi-theme was removed on purpose: the theme catalog API (2026-09-26) and
   the admin token/font/CSS overrides (2026-10-13) are both retired. Only
   `branding.accent_color` still reaches the client.
5. Themes are token-only and cannot affect layout. The shell is structural in
   `Layout.tsx` and `AppSidebar.tsx`, not themed — which is why the layout
   variant had to be added alongside the tokens.
6. Three contract tests pinned the single theme and now cover both:
   `highContrastCss.test.ts`, `highContrastContrast.test.ts`, and
   `themeBoot.test.tsx` (the last for the new pre-paint storage key).
7. App shell: `Layout.tsx` is the desktop rail, the `Sheet` mobile drawer, and
   the sticky mobile header; `AppSidebar.tsx` holds the nav and the avatar
   `DropdownMenu` the account menu was rearranged from.
8. Search is `GlobalSearch.tsx`, toggled by Ctrl/Cmd+K. It owns its own open
   state, so the bar's button dispatches an event rather than mounting a second
   instance.
9. Library links come from the profile's primary menu
   (`lib/uiCustomization.ts`, `PrimaryMenuItem`), resolved by a function now
   shared by the sidebar and the top bar.
10. There is no i18n layer — no framework and no locale files. UI strings are
    literal English, so reusing them means reusing the same literals.

## Decisions

Taken with Luther on 2026-10-29, superseding the earlier "default theme must not
change" rule:

1. **Option B.** WeFlix is a client-only theme: a second token block plus a
   `sidebar | topnav` layout variant, chosen from localStorage. No backend or
   settings-manifest change.
2. **WeFlix is the default in this fork.** Midnight Cinema stays selectable as
   the fallback. With no stored choice the app paints WeFlix.
3. **Discover is the existing Requests page** (`/requests`), shown under the
   same capability gate the sidebar already applies.
4. **The separate Home link is dropped**; the logo links home.
5. **The top-nav layout is tied to the WeFlix theme.** A theme owns both its
   tokens and its shell.
6. The control lives in **Settings → Interface**, built from the existing
   `ChoiceButtons` component.

## Files

New:

| File | Role |
|---|---|
| `web/src/themes/theme.ts` | Theme ids, default, storage key, parsing, DOM write |
| `web/src/themes/appThemeContext.ts` | Context and `useAppTheme` |
| `web/src/themes/AppThemeProvider.tsx` | Provider component |
| `web/src/themes/weflix.css` | WeFlix tokens, shell offset, scroll-row helper |
| `web/src/components/TopNav.tsx` | The top bar and account menu |
| `web/src/components/primaryMenuLinks.tsx` | Shared primary-menu resolution |
| `web/src/components/TopNav.test.tsx` | Top-bar tests |

Edited, kept small: `web/index.html` (static `data-theme`),
`web/src/main.tsx` (one import), `web/src/themeBoot.js` (pre-paint theme),
`web/src/App.tsx` (provider wrap), `web/src/components/Layout.tsx` (variant),
`web/src/components/AppSidebar.tsx` (uses the shared resolver),
`web/src/components/GlobalSearch.tsx` (open event),
`web/src/pages/settings/InterfaceSettings.tsx` (the control).

Test files updated to cover both themes: `highContrastCss.test.ts`,
`highContrastContrast.test.ts`, `themeBoot.test.tsx`, `Layout.test.tsx` (pinned
to the sidebar theme), `InterfaceSettings.test.tsx`.

## Verification

Every number below is from a command that ran, not an expectation.

| Gate | Result |
|---|---|
| Typecheck (`tsc -b`) | exit 0 |
| Lint (`eslint .`) | 0 errors, 83 warnings — the same count as the pre-change baseline, none from these files |
| Production build (`pnpm run build`) | succeeded; Vite built in 1m 4s |
| Full web suite | 6825 tests: 6796 passed, 23 skipped, 6 failed |
| Baseline suite (all changes stashed) | 6808 tests: 6802 passed, 6 failed — the same cases plus a shuffle test that flakes between runs |
| `tailwindTokenSyntax`, `Catalog` in isolation | 30/30 pass; both failures in the full run were 5s/15s timeouts under load, with zero rule offenders found by grep |
| Guard tests | `highContrastCss` 3/3, `highContrastContrast` 6/6 (3 checks × both themes), `themeBoot` 14/14 |
| Related suites | `Layout` 18/18, `AppSidebar` 12/12, `InterfaceSettings` 6/6, `TopNav` 10/10 |
| Route walk, real UI | 13/13 bar links and account-menu items reached the expected route |
| In-browser measurement | WeFlix: 1 Primary nav, no sidebar, at 1440/1024/820/390. Midnight Cinema: 0 Primary nav, sidebar visible at 1440 |
| Search icon | opened a dialog: true |

Pre-existing failures, present with and without these changes, in code this
change does not touch: three `GlobalSearch` request-row cases, one
`WatchPlaybackChrome.shuffle` case, one `OnboardingGate` case. Not fixed here.

The route walk, run against the real UI with a browser:

```
Home  /                              ->  /
For You  /recommendations            ->  /recommendations
Calendar  /calendar                  ->  /calendar
Discover  /requests                  ->  /requests
Favorites  /catalog?source=favorites ->  /catalog?source=favorites
Watchlist  /catalog?source=watchlist ->  /catalog?source=watchlist
Collections (menu)                   ->  /collections
History (menu)                       ->  /catalog?source=history
Notifications (menu)                 ->  /notifications
Calendar (menu)                      ->  /calendar
Settings (menu)                      ->  /settings/playback
Watch party (menu)                   ->  /rooms
Switch Profile (menu)                ->  /profiles
Logout (menu)                        ->  /login
```

## Screenshots

In `docs/weflix/screenshots/`, captured from the real UI at 1440×900, 1024×800,
820×1100 and 390×844:

| File | Shows |
|---|---|
| `weflix-1440.png` | Top bar with the logo, For You, Calendar, divider, Discover, icons, avatar; full-width content |
| `weflix-1440-account-menu.png` | The two-column account menu with its vertical divider |
| `weflix-1440-search.png` | The search dialog opened from the bar's icon |
| `weflix-1024.png`, `weflix-820.png` | The same bar at desktop and tablet widths |
| `weflix-390.png` | Phone: logo and icons in the bar, links as a scrollable chip row |
| `midnight-cinema-1440.png`, `midnight-cinema-390.png` | The upstream theme, unchanged |

## Questions for Luther

- **Admin entry.** It is gated by the same `useIsActingAdmin` rule the sidebar
  uses, which requires the acting profile to be the primary one. The throwaway
  profile used for these captures was not primary, so Admin did not appear.
  Confirm it shows for your own primary profile.
- **Discover timing.** Discover appears once the requests capability resolves,
  the same gate the sidebar applies to Requests, so it is absent for the first
  moments of a cold load. Say if you want it always visible; that is a behaviour
  change beyond this task.
- **Accent.** `--primary` is set to WeFlix red (`#e50914`), so every primary CTA
  turns red, not only the wordmark. Artboard 1 shows red only in the wordmark —
  tell me if you want it dialled back to a neutral primary.
- **Radius.** The global `--radius` stays at upstream's `0.75rem` so no existing
  component changes shape; the artboard's ~24px radius is applied to the account
  menu panel alone.
- **Library links.** The artboard's Movies / TV Shows / Anime entries are library
  items in the profile's primary menu. The capture profile had none configured,
  so the bar showed the built-ins (For You, Calendar). Add libraries under
  Settings → Interface to see them.

## Not done

- **No NAS changes.** `DEPLOY.md` is a document only; nothing was deployed.
- Screenshots come from viewport sizes in a headless browser, not a real phone
  or tablet.
- The admin area and the settings sidebar were not re-checked under WeFlix
  beyond reachability; settings deliberately keeps its own sidebar.
