# Shared List Cache (Home Rails) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Commands assume the repository root is the cwd.

**Goal:** Cut repeated database work and tail latency on the home screen by caching the *shared* part of every user-agnostic home rail once per access scope, refreshing it in the background before it expires (target TTL ~15 min), and layering the cheap per-user part (watched flags, play position, poster links) on top of the cached list on every request.

**Architecture:** Option A — an in-process, **process-global** resolved-list cache inserted at the native section fetch choke point (`internal/sections`). The section fetcher already returns a *shared, non-personalized* item list; the per-user overlay is applied later in the API handler. We cache at that boundary for the wider/fuller set of user-agnostic section types (including admin-curated collections). Redis is **not** the store — it is an optional invalidation signal only.

**Tech Stack:** Go, `golang.org/x/sync/singleflight` (already used in `internal/sections`), in-process map + mutex (mirrors the existing `editorialCandidateCache`), existing `catalog.AccessFilter`, targeted Go tests.

**Scope target:** Native Silo sections path (`internal/sections/fetcher.go` + `internal/api/handlers/sections.go`).

---

## Validated Direction

Confirmed directly against the code on `main`:

- **The shared base list and the per-user overlay are already separated.** `Fetcher.fetchSection` (`internal/sections/fetcher.go`) returns raw `[]*models.MediaItem` and, for the user-agnostic section types, takes only the access `filter` — no user/profile. The per-user overlay (watched state, play position, presigned poster URLs, overlay badges) is applied afterward in `SectionHandler.buildSectionsResponse` (`internal/api/handlers/sections.go`) on every request. This makes the fetcher output a clean, presign-free cache boundary.
- **There is precedent to copy.** `editorialCandidateCache` (`internal/sections/fetcher.go`) is already an in-process TTL cache guarded by a mutex + `singleflight.Group`, keyed by subject/library/access-filter (never by user). It caches *candidate ID lists*; this plan generalizes it to a *resolved item list* with a short TTL and refresh-ahead.
- **The fetch choke point is `Fetcher.FetchOne`.** Both `HandleHomeSections` (via `FetchAll` → `FetchOne`) and `HandleHomeSectionItems` route through it. Caching there covers the whole native home path in one place.
- **The cache must be process-global, not per-`Fetcher`.** The process constructs several independent `sections.NewFetcher(...)` instances (e.g. native API and recommendations). A cache stored on the `Fetcher` struct (as `editorialCandidateCache` is today) would be duplicated per instance and could never be shared across surfaces. A package-level cache lets any current or future consumer of `FetchOne` reuse the same warm entries.
- **`Collection` is not uniformly user-agnostic.** `fetchCollection` serves library collections (shared) but routes to `fetchUserCollection` when `cfg.UserCollectionID != ""` (profile-scoped). The cache must exclude the user-collection case or it would leak one profile's list to another.

---

## What is cached (the fuller user-agnostic set)

These rows are identical for everyone who can see the same libraries at the same content-rating cap, so they are cached by access scope:

- Recently Added (`SectionRecentlyAdded`)
- Recently Released / New Releases (`SectionRecentlyReleased`)
- Genre and custom-filter rows (`SectionGenre`, `SectionCustomFilter`)
- Trending on this server (`SectionTrendingOnServer`), Most Watched (`SectionMostWatched`)
- New to Library (`SectionNewToLibrary`)
- Critically Acclaimed (`SectionCriticallyAcclaimed`), Award Winners (`SectionAwardWinners`)
- Editorial Spotlight / featured (`SectionEditorialSpotlight`)
- Seasonal (`SectionSeasonalThemed`), Mood (`SectionMoodCollection`), Format Showcase (`SectionFormatShowcase`)
- Trending Discover (`SectionTrendingDiscover`)
- **Admin-curated lists (`SectionAdminCuratedList`)**
- **Library** collections only (`SectionCollection` where `cfg.UserCollectionID == ""`)

## What is NOT cached

- **Per-user rows (no shared base):** Continue Watching, Next Up, Next in Series, Recommended For You / Because You Watched / Similar Users Liked / Taste Match, Hidden Gems, Forgotten Favorites, Profile Activity Feed, and **user** collections (`SectionCollection` with a `UserCollectionID`). These bypass the cache entirely.
- **`SectionRandom`** is technically user-agnostic but intentionally randomized per request; caching would freeze it. Excluded to preserve behavior.

## The per-user overlay always runs fresh (correctness)

Even for a cached row, each request still computes, per person: watched flags (`isPlayed`/UserData), play position, and freshly presigned poster URLs — all in `buildSectionsResponse`. The cache only stores the *membership and ordering* of the row (presign-free `*models.MediaItem`). No request ever sees another user's watched state, and no cached entry carries a poster URL that could expire mid-cache.

---

## The access-scope cache key (security-critical)

If the key fails to capture an access boundary, the cache can serve items a user must not see. The key MUST include, and nothing that is per-user:

- Section identity: section type + section ID + a hash of the section `Config` (so two genre rows with different filters never collide).
- Requested `ItemLimit` (row size). Callers may request different sizes for the same section; the key must separate them (or the cache must store a superset and slice down).
- Access scope: sorted accessible library IDs, sorted disabled library IDs, max content rating, excluded media types, plus sort/order.

Model the string builder on the existing `editorialCandidateCacheKey`, extended with section ID + config hash + `ItemLimit`.

## Background refresh before expiry (the key behavior)

Each entry stores `builtAt`, a soft `refreshAfter` (e.g. `builtAt + 12min`), and a hard `expiresAt` (e.g. `builtAt + 15min`). A `getOrRefresh(ctx, key, loader)` helper implements:

- `now < refreshAfter` → return cached value, do nothing.
- `refreshAfter <= now < expiresAt` → return cached value **and** kick off one async rebuild via `singleflight` (only one rebuild per key). The fresh value swaps in when ready.
- `now >= expiresAt` → block on the build; `singleflight` collapses concurrent blockers into one build (stampede protection).

Net effect under steady traffic: entries are refreshed ahead of expiry, so live requests are served warm and never pay the cold rebuild. An optional low-frequency sweeper can keep rarely-hit hot scopes warm.

## Optional invalidation (freshness)

Newly scanned content otherwise appears up to ~15 min late in these rails — acceptable for "recently added"/"trending". Optionally subscribe to the existing Redis `EventScanComplete` / `EventMetadataUpdated` events (`internal/cache`) and drop affected scopes for near-instant freshness. This is a nice-to-have layered on top of the TTL, not a dependency.

---

## File Structure

- Add `internal/sections/resolvedlistcache.go`
  - Package-level cache: `map[string]resolvedListEntry` (value = presign-free `[]*models.MediaItem` + `TotalCount` + `builtAt`/`refreshAfter`/`expiresAt`), guarded by a `sync.RWMutex` and a package-level `singleflight.Group`.
  - `getOrRefresh(ctx, key, ttl, refreshLead, loader)` implementing serve / refresh-ahead / block-only-when-dead. Reuse the `f.now()` clock indirection for deterministic tests.
  - `resolvedListCacheKey(...)` builder (section type + section ID + config hash + `ItemLimit` + access scope), modeled on `editorialCandidateCacheKey`.
  - `isCacheableSectionType(resolved)` guard implementing the whitelist above, including the `cfg.UserCollectionID == ""` check for `SectionCollection` and the `SectionRandom` exclusion.
- Modify `internal/sections/fetcher.go`
  - Wrap the user-agnostic branch of `FetchOne` (and the `SectionEditorialSpotlight` branch) so that cacheable section types resolve through `getOrRefresh`; everything else calls the existing loader path unchanged.
  - Return defensive copies so callers cannot mutate cached slices.
- Add `internal/sections/resolvedlistcache_test.go`
  - Distinct access scopes never cross-serve (security).
  - `SectionCollection` with a `UserCollectionID` is never cached.
  - Refresh-ahead returns the current value without blocking; only a fully-expired entry blocks.
  - Stampede: concurrent cold requests collapse to a single loader call.
  - Overlay parity: a cached row produces the same per-user response as the uncached path (drive through `buildSectionsResponse` in `internal/api/handlers`).

No migration is planned. No plugin repo changes are planned. No client changes are planned.

---

## Deferred (explicitly out of scope here)

- **The cross-library recently-added path** in `directContentService.BrowseItems` is a separate choke point with its own per-user overlay, and is a separate follow-up.

---

## Tasks

- [ ] Add `resolvedlistcache.go` (cache struct, `getOrRefresh`, key builder, section-type whitelist).
- [ ] Wire `FetchOne` to route cacheable section types through the cache; leave per-user types untouched.
- [ ] Add the `SectionCollection` user-collection exclusion and `SectionRandom` exclusion.
- [ ] Add unit tests (scope isolation, user-collection exclusion, refresh-ahead non-blocking, stampede collapse).
- [ ] Add an overlay-parity test through `buildSectionsResponse`.
- [ ] (Optional) Subscribe to `EventScanComplete`/`EventMetadataUpdated` to invalidate affected scopes.
- [ ] `make lint`, `go test ./internal/sections/... ./internal/api/...`, `make verify-local-paths`.

## Risks / follow-ups

- **Security key completeness** is the number-one risk; when in doubt, add a field to the key and add a scope-isolation test.
- **Approximate totals:** hiding watched items post-cache keeps `TotalRecordCount` approximate — already an accepted tradeoff on the `isPlayed` path; unchanged here.
- **Staleness window** up to ~15 min for newly scanned content; mitigated by the optional scan-complete invalidation.
- **Memory:** ~200 items × small metadata × tens of scopes is negligible; cap entry count and evict LRU as a backstop.
