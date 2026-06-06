# Calendar Presets & Personalized Default — Design

- **Date:** 2026-05-29
- **Status:** Approved (design); ready for implementation planning
- **Scope:** `silo-server` (Go backend + web admin UI). Client follow-ups in `silo-android` / `silo-apple` are additive and optional.
- **Note:** All paths are repository-relative. Commands assume the repository root is the cwd.

## Problem

The calendar works but is not practical. It defaults to *every* upcoming airing in the catalog, so it is noisy and undifferentiated — the user has to mentally filter out shows they don't care about. We want a Simkl-style preset switcher with a personalized default, powered by the server's real watch data rather than a manually curated list.

## Goals

- Default the calendar to a personalized **Following** view (shows the profile actually engages with).
- Offer a small set of presets, including a **server-wide** popularity view and an **external trending** view.
- Keep it cheap under load (hundreds of concurrent users): presets must **filter, not compute**.
- Make "what do I still need to watch this week" visible via a watched overlay.
- Preserve backward compatibility for existing clients.

## Non-Goals

- No cross-user caching of the windowed airing query yet (phased — add only if metrics show strain).
- No pulling in external content that isn't in the library (e.g. a followed show whose next season hasn't been added). The calendar stays catalog-scoped.
- No change to the week-view layout, timezone handling, or event ordering (recently tuned; left intact).
- No region/country or "For Kids" presets (considered and cut).

## Presets

The selector replaces today's `All / Favorites / Watchlist` toggles.

| Preset | Shows | Source |
|---|---|---|
| **Following** *(default)* | Upcoming airings for series the profile has watched, favorited, or watchlisted | per-profile: `user_favorites` ∪ `user_watchlist` ∪ watched-series rollup |
| **Popular here** | Airings for what is most-watched server-wide | cached `recommendation_cache` (`RecTypePopular`, global key) |
| **Trending** | Airings for externally-trending shows that exist in the library | `trending_discover_snapshots` |
| **Everything** | All catalog airings (today's behavior) | no restriction |

Per-profile vs server-wide is split cleanly across two presets: **Following** is personal (this profile), **Popular here** is the server-wide aggregate.

## Core principle: filter, don't compute

Every preset resolves to **the windowed airing candidate list ∩ a small id-set**. No preset triggers a fresh aggregation at request time.

1. **Step 1 — windowed candidate fetch** (unchanged from today): the 3-branch UNION in `internal/catalog/calendar_repo.go` over movies/episodes/season-premieres, scoped only by library access + content rating, across the ±2-day-padded window. Its result depends only on the access signature, not on viewer identity, so it remains cacheable across users later.
2. **Step 2 — intersect with one id-set**, chosen by preset:
   - **Following** → resolve the profile's followed id-set once per request, then filter `itemIDExpr = ANY($set)`.
   - **Popular / Trending** → read the already-cached global id-set and intersect.
   - **Everything** → no restriction.
3. **Step 3 — group by day, sort by viewer-local wall-clock time, respond** (unchanged).

The only heavy SQL is Step 1, which already runs today for every calendar load. Presets add a bounded id-set intersection, never a new aggregation.

### Unify the filter mechanism

All personal/preset filters resolve to "an id-set, then `itemIDExpr = ANY($set)`". This replaces the special-cased favorites/watchlist `EXISTS` subqueries in `appendPersonalFilterClause` with a single code path (favorites and watchlist become id-sets resolved from their tables — they are subsets of Following). One mechanism is easier to reason about and removes duplicated clause-building.

`itemIDExpr` continues to be `mi.content_id` for movies and `e.series_id` / `s.series_id` for episodes / season premieres, so a watched/favorited **movie** matches by its own content_id while a followed **series** matches by series id.

### Following id-set

Resolved per request as the DISTINCT union of:

- `SELECT media_item_id FROM user_favorites WHERE user_id = $u AND profile_id = $p`
- `SELECT media_item_id FROM user_watchlist WHERE user_id = $u AND profile_id = $p`
- `SELECT DISTINCT COALESCE(e.series_id, wp.media_item_id) FROM user_watch_progress wp LEFT JOIN episodes e ON e.content_id = wp.media_item_id WHERE wp.user_id = $u AND wp.profile_id = $p`

"Engaged" = the presence of a `user_watch_progress` row (you've started it); no completion threshold required for Following. The rollup mirrors the established pattern in `GetPopularItems` (`internal/recommendations/repo.go`) — reuse that rollup expression rather than duplicate it.

The candidate list (Step 1) is small (one week of airings), so intersecting against a bounded array is cheap. Caveat: a pathologically large followed-set (a profile that has watched thousands of distinct series) makes the array large; if that ever shows up as hot, switch the Following preset to correlated `EXISTS` against the user's tables. Not building that now.

### Popular / Trending id-sets

- **Popular**: `GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "")` → `[]ScoredItem`; use `MediaItemID` values (already series-level via the `GetPopularItems` rollup). Refreshed by the recommendations worker on a TTL; readers never aggregate live.
- **Trending**: `TrendingSnapshotRepository.Get(ctx, source, window)` → `TrendingSnapshot.ContentIDs` (pre-resolved to catalog content_ids, viewer-agnostic). The calendar reads the canonical default snapshot (`tmdb` / `week`). Unioning multiple sources/windows is possible but out of scope.

**Access is preserved automatically:** Popular/Trending id-sets are viewer-agnostic and may include items a given profile cannot access. Because we intersect them with the access-scoped Step-1 candidates (library scoping + `MaxContentRating` already applied there), inaccessible items drop out naturally. No extra access check needed on the id-sets.

**Cold/empty cache:** if the Popular cache or Trending snapshot is empty (fresh server, worker hasn't run, provider unconfigured), the id-set is empty and the preset returns no rows. This must render the empty state, not an error.

## Watched overlay

After Step 1, decorate the windowed events with per-profile watched status:

- One bounded lookup: `SELECT media_item_id FROM user_watch_progress WHERE user_id = $u AND profile_id = $p AND completed = true AND media_item_id = ANY($episodeContentIds)` over the window's episode/movie content_ids.
- Watched items render dimmed + ✓. This is a per-profile overlay applied after Step 1 (like the Following filter), so it does not affect the shareability of the Step-1 candidate set.
- Premiere / finale / new-season badges extend the existing `buildBadges` logic in `internal/api/handlers/calendar.go`.

## UI / UX

- **Selector** (`web/src/pages/Calendar.tsx`): segmented pills on desktop, collapsing to a dropdown on narrow viewports — the same responsive swap the week navigator already uses. The library picker stays beside it. Both presentations bind to one selected-preset state.
- **Empty state**: when **Following** has no upcoming airings (new or sparse profile), show a nudge with quick switches to Popular / Trending / Everything instead of a blank week.
- **Persistence**: remember the profile's last-chosen preset in `localStorage` (keyed by profile id) so the calendar reopens where the user left it. Server-side profile preference is a possible future enhancement.
- **Card cues** (`web/src/components/calendar/CalendarEventCard.tsx`, `web/src/lib/upcomingEventPresentation.ts`): badges + watched overlay as above.

## API contract

- Extend `/calendar`'s `filter` parameter to accept `following | popular | trending | everything`.
- **Keep `all | favorites | watchlist` valid** so existing `silo-android` / `silo-apple` clients keep working unchanged. `all` and `everything` are equivalent. This is purely additive.
- Add an additive `watched: bool` field (and any new badge string) to each event item. Older clients ignore unknown fields.
- Default when `filter` is omitted: today it is `all`. The **web client** will request `following` explicitly; the server default stays `all` to avoid changing behavior for existing API consumers. (Default-view personalization is a client choice, not a server-contract change.)
- **Profile requirement:** the `/calendar` route already runs behind `RequireProfile`, so a profile is always present. `following` (and legacy `favorites` / `watchlist`) plus the watched overlay are per-profile and resolve against it; `popular` / `trending` / `everything` / `all` are profile-agnostic and ignore it.

### Multi-repo coordination (per CLAUDE.md)

- `silo-android` / `silo-apple`: may adopt the new presets and the watched cue as independent follow-ups. Nothing here forces a lockstep change because old filter values and response shape remain valid.

## Implementation surface

Backend:
- `internal/catalog/calendar_repo.go` — add an id-set restriction to `CalendarFilter`; generalize `appendPersonalFilterClause` into a single `ANY($set)` path; keep library/rating clauses intact.
- A followed-id-set resolver, `ListFollowedSeriesIDs(ctx, userID, profileID)`, placed in the package that owns the favorites/watchlist/episode join; reuse the `GetPopularItems` rollup expression.
- `internal/api/handlers/calendar.go` — validate the expanded `filter` set; orchestrate preset → id-set resolution (Following resolver, Popular cache read, Trending snapshot read); apply the watched overlay; extend `buildBadges`.

Frontend:
- `web/src/pages/Calendar.tsx` — responsive selector + preset persistence + empty state.
- `web/src/hooks/queries/calendar.ts` — new `filter` values; `watched` field on the event type.
- `web/src/components/calendar/CalendarEventCard.tsx` — watched overlay + badge rendering.
- `web/src/lib/upcomingEventPresentation.ts` — badge/treatment helpers.

## Edge cases

- Cold/empty Popular cache or Trending snapshot → empty preset → empty state, never an error.
- Profile with no follows → Following empty → empty state with preset nudges.
- Large followed-set → bounded array today; correlated-`EXISTS` fallback noted for later.
- Movie vs episode/season id matching (`content_id` vs `series_id`) handled by the existing `itemIDExpr` selection per branch.
- Specials (`season_number = 0`) remain excluded.
- Access control (`MaxContentRating`, library scoping, disabled libraries) still enforced for every preset via Step 1; viewer-agnostic id-sets cannot leak inaccessible items because of the intersection.
- Backward-compat filter values (`all | favorites | watchlist`) still resolve through the unified id-set path with identical results.

## Testing strategy

- **Repo** (`internal/catalog/calendar_repo_test.go`): each preset's id-set filter (following / popular / trending / everything), empty id-set → zero rows, access + rating enforcement preserved across presets, movie-by-content_id vs series-by-series_id matching, legacy favorites/watchlist parity.
- **Handler** (`internal/api/handlers/calendar_test.go`): filter validation accepts new + legacy values; Popular/Trending cold cache degrades to empty gracefully; `watched` field populated correctly; watched lookup bounded to the window.
- **Frontend**: selector responsive swap (pills ↔ dropdown), preset persistence across reloads, watched overlay + badge rendering, Following empty state.

## Future work

- Cross-user cache of the Step-1 windowed candidate set keyed by (window, access-signature), if load metrics justify it.
- External enrichment of followed shows whose next season is not yet in the library.
- Server-side persisted preset preference per profile.
- Optional union of multiple Trending sources/windows.
