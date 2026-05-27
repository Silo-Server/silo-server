# PageBack Component Design

## Goal

Replace the inconsistent collection of inline back affordances across user-facing pages with a single shared `PageBack` component, placed in the top-left of every non-root page at a stable pixel offset that does not drift with title length, hero content, or page layout.

Today, back navigation is implemented eight different ways across the app (`DetailBreadcrumb` chevron inside the hero, outline `<Button>` at the page top, ghost `<Link>` with "Back to X" text, `surface-panel-subtle` pill in the page header, plain text link with `←`, etc.), at three different positions (overlaid on hero info column, top-left of page-shell, top-right of page header). A user reported the affordance is hard to find precisely because it moves around as titles and hero content shift. This spec consolidates all of them.

## Behavior

### The component

`<PageBack />` renders a small circular chevron pill in the top-left of its containing element. It is visually identical to the existing `DetailBreadcrumb` chevron (`glass-subtle` background, `ChevronLeft size-5`, muted-foreground with hover) so users already familiar with the season/episode back arrow see no change in that affordance — only a more consistent location.

- Takes no `to` or `onClick` props. A single, fixed behavior is the point.
- Calls `navigate(-1)` on click.
- `aria-label` defaults to "Go back" and is overridable via an optional `label` prop for screen reader context.
- Uses `position: absolute; top: 1rem; left: 1rem; z-index: 20` with `sm:top-1.5rem sm:left-1.5rem`. Pages place it inside a `position: relative` ancestor — typically the existing hero `<section>` (which is already `relative isolate overflow-hidden`) or a wrapping `<div className="relative">` at the top of the page-shell.

### Placement strategy

Two cases:

1. **Hero pages** (ItemDetail variants, PersonDetail). `<PageBack />` is a direct child of the hero `<section>`. The hero is already `relative isolate overflow-hidden`, so the chevron overlays the backdrop in the top-left. No layout change to the hero itself.
2. **Non-hero pages** (Settings, Request detail, Request browse, Collection editors, Smart collection wizard, Recommendations section, Profile customize home). `<PageBack />` is rendered at the top of the page-shell inside a `<div className="relative">` wrapper, replacing the page's current bespoke back link or button.

In both cases the chevron lands in roughly the same screen pixel range — top-left, just inside the page-shell padding. The title and surrounding content shift around the chevron instead of the other way around.

### Behavior on history-less loads

When a user lands on a non-root page via direct URL (deep link, refresh, new tab) and there is no prior history entry within the SPA, `navigate(-1)` does nothing useful. The chevron is still rendered (we cannot detect this state reliably with React Router v6's history API). This matches browser mouse-back behavior and is acceptable because (a) the failure mode is silent — clicking the button does nothing visible — and (b) users who reach a page via deep link can navigate via the main app shell. We do not gate rendering on history length.

### What `DetailBreadcrumb` becomes

`DetailBreadcrumb` keeps its textual breadcrumb path on Season and Episode pages — the path (e.g., "Severance › Season 1") communicates hierarchy and is independent of back navigation. The leading `ChevronLeft` is removed from `DetailBreadcrumb`; PageBack now owns that affordance. The breadcrumb segments themselves remain individually clickable links to their hierarchy targets.

## Architecture

`PageBack` is a presentational client component with one dependency: React Router's `useNavigate`. No new state, no new context, no new routes. The component lives in `web/src/components/PageBack.tsx` alongside other shared UI primitives.

Each consuming page imports `PageBack` and renders it inside an already-`relative` container. No central placement registry, no per-route configuration — each page is responsible for opting in. This keeps the change localized and reviewable per page.

## Pages affected

### Hero pages — add `<PageBack />` (new affordance)

- `web/src/pages/ItemDetail/MovieContent.tsx`
- `web/src/pages/ItemDetail/SeriesContent.tsx`
- `web/src/pages/PersonDetail.tsx`

### Hero pages — add `<PageBack />`, simplify `DetailBreadcrumb`

- `web/src/pages/ItemDetail/SeasonContent.tsx`
- `web/src/pages/ItemDetail/EpisodeContent.tsx`

In both, the inline `DetailBreadcrumb` retains the textual hierarchy path inside the hero info column; PageBack overlays the hero at top-left.

### Non-hero pages — replace existing back affordance

- `web/src/pages/SettingsLayout.tsx` — remove the `surface-panel-subtle` `ArrowLeft` pill from the page header.
- `web/src/pages/RequestDetail.tsx` — remove both the outline `<Button>` at the top and the duplicate `<Button>` near the bottom of the page.
- `web/src/pages/RequestBrowse.tsx` — remove the "Back to Requests" text link.
- `web/src/pages/CollectionEditor.tsx` — remove the ghost "Back to Collections" `<Link>` (currently rendered in three branches).
- `web/src/pages/ImportedCollectionEditor.tsx` — same.
- `web/src/pages/SmartCollectionWizard.tsx` — remove the ghost "Back to Collections" link at the top (the inline "Back to Filters" / floating wizard back buttons stay; they are step controls, not page navigation).
- `web/src/pages/RecommendationsSection.tsx` — remove the "Recommendations" `<Link>` with `ArrowLeft` near the section header.

### Non-hero pages — add `<PageBack />` (new affordance)

- `web/src/pages/ProfileCustomizeHome.tsx` — currently has no page-level back affordance (the existing `onBackToGallery` is an intra-page state callback, not a page-level navigation control). Add `<PageBack />` at the top.

### Component changes

- `web/src/components/PageBack.tsx` (new) — described above.
- `web/src/pages/ItemDetail/components/DetailBreadcrumb.tsx` (modified) — drop the leading `ChevronLeft` button. Component becomes a pure path renderer. Update the component's tests if they cover the chevron rendering.

### Pages explicitly not changed

- Top-level user-facing pages (no back affordance is appropriate, they are reached as navigation roots): Home, Catalog, LibraryBrowse, LibraryRecommended, LibraryCollections, LibraryPage, Calendar, Collections, Recommendations (list view), Requests (list view), Profiles.
- Auth and onboarding flows (state-machine controlled, not history controlled): Login, Signup, OAuthComplete, ActivateDevice, SetupWizard, TasteSeed.
- Playback chrome (`web/src/playback/WatchPlaybackChrome.tsx`) — already has its own chevron in the player overlay; the watch UX is intentionally separate from the page-shell.
- Watch Together flows (`WatchTogetherJoin`, `WatchTogetherRoomPage`) — the in-flyout "Back to results" is a contextual subnav inside a flyout, not page-level navigation.
- Admin pages — the original feedback was user-facing only; admin can adopt `PageBack` in a follow-up pass if desired.

## Edge cases

- **No history entry on initial load.** Clicking `PageBack` calls `navigate(-1)`, which is a no-op. Acceptable; matches browser mouse-back behavior. Documented but not gated.
- **Backdrop image is mostly white in the top-left.** The `glass-subtle` background provides enough contrast against bright backdrops thanks to its translucent dark fill; this is the same treatment `DetailBreadcrumb` already uses on Season/Episode pages, so behavior is unchanged.
- **Mobile viewport.** Component uses `top-4 left-4` on small screens and `sm:top-6 sm:left-6` on `sm:` and up. The chevron is `size-5` (20px) inside `p-1.5` padding — a 32px tap target, which is below the 44px Apple HIG recommendation but matches the existing `DetailBreadcrumb` chevron. If mobile reach is a concern, bump padding to `p-2` (40px target). Default to `p-1.5` for visual parity with current code.
- **RTL layout.** The component pins to `left`. If the app later supports RTL, this becomes `start`-relative; out of scope for this change.
- **Keyboard focus.** Standard `<button>` element, focusable by default, gets the existing global focus ring from Tailwind base styles. No custom focus handling.

## Out of scope

- Replacing back affordances on admin pages.
- Persisting or reconstructing app-internal history when users land via deep link (would require a custom history wrapper).
- Adding a keyboard shortcut (e.g., `Esc` or `Backspace`) for back navigation. Worth considering in a follow-up but not part of this change.
- Replacing the in-player back chevron in `WatchPlaybackChrome`.
- Touching Watch Together's contextual subnav.

## Verification

Commands assume the repository root is the cwd.

- `cd web && pnpm run lint`
- `cd web && pnpm run format:check`
- Frontend component test for `PageBack`: renders a button with `aria-label`, calls `navigate(-1)` on click, applies the documented Tailwind class string (snapshot or class assertion).
- Update existing `DetailBreadcrumb` test (if present) to confirm the leading chevron is no longer rendered and the path segments still render and link correctly.
- Smoke test in the dev frontend: navigate from Home → a movie detail page → confirm the chevron is in the top-left and clicking it returns to Home. Repeat for Series, Season, Episode, Person, Request detail, Request browse, Collection editor, Smart collection wizard, Settings, and Recommendations Section. Confirm the chevron stays in the same screen position across all of them.
- Visual check on a Season detail page: confirm the textual breadcrumb path ("Series Title › Season N") still renders inside the hero info column and segment links still navigate to the series page.
