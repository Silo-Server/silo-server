# Section-fetch & jellycompat performance plan

Status: in progress. Commands assume the repository root is the cwd.

> **Log provenance / staleness check.** The production logs analyzed here were
> captured against commit `b1b3a9b4` (2026-07-01). Current `origin/main` is
> `3d82aa59` (2026-07-02), 15 commits ahead. Only two of those commits touch the
> hot files, and both are unrelated (`3d8a63de` metadata-language upsert
> precedence; `d6c4dce1` playback-session conflict error) — verified they do not
> change `ListBySeries`, `loadProgressPage`, the next-up query, the browse
> fast-path, or the detail-enrich path. So the diagnoses below still apply to
> current `main`; nothing since the logs has fixed these.

## Revised implementation scope (2026-07-03, after code verification)

After reading the current code, several original fixes were re-scoped or
deferred. This section is authoritative; the "Proposed fixes" section further
down is the original analysis and is kept for context.

**Commit 1 (safe subset):**
- **#2 Resume scan cap — IMPLEMENTED.** `loadProgressPage` now bounds its scan to
  `resumeScanMaxRows = 300` so `EnableTotalRecordCount=true` can't drive the
  O(history) scan (`internal/jellycompat/handlers_items.go`). Builds clean.
  Caveat: the specific logged 35.9s Resume call actually went through the capped
  `FetchOne` fast path (the deferred #1 cold recompute), so this removes a latent
  risk rather than that exact spike.
- **#9 drop redundant enrich — RE-SCOPED (do not remove wholesale).**
  `enrichDetailUserData` (`content_direct.go:612-626`) is redundant only for
  **movie/episode leaf** items (all `batchListItemDetails` callers re-apply
  batched `progress[...]` which overrides it). For **series** it builds the
  episode-**rollup** `UserData` (Played/UnplayedItemCount) that no progress row
  covers — removing it would strip series watch-state from `/Items`/`/Items/Latest`
  (which can return Series). Correct fix: skip enrich for leaf types only, keep it
  for series (or batch the leaf-progress lookup so output is identical).
- **#4 concurrency — RE-SCOPED (pool-constrained).** `pgxpool` MaxConns defaults
  to **20** (`internal/config/db_loader.go:162`); live usage already ~25 across the
  DB. Raising `fetchAllMaxConcurrency` 4→8 doubles per-request pool pressure, so
  ~2–3 concurrent home requests would saturate the pool and could regress under
  load. Only bump conservatively (e.g. 4→6) or after a load test; not a blind
  constant change.

**Deferred to their own PRs (heavier than "light"):**
- **#1 continue-watching latency.** There is no existing result cache to "warm";
  the Resume/CW path recomputes via `FetchOne` every call. The hourly `:12` spike
  lines up with the `collection sync scheduler` / hourly session-cleanup jobs, so
  the root cause may be periodic DB contention, not a cache miss. Needs its own
  investigation (contention vs. adding a short-TTL result cache with progress-write
  invalidation) — carries user-visible staleness risk.
- **#3 `/Shows/{id}/Episodes`.** A `LIMIT` on `ListBySeries` truncates a series;
  the real fix is client-visible pagination requiring Android/Apple coordination.
- **#5 dedup + SQL-side filtering.** The dedup needs cross-section memoization that
  `FetchAll` does not currently have; the SQL-filter half is a `userstore`
  interface refactor. Both are structural, not light.

**Dropped:**
- **#6 content_id NextUp rewrite.** ~28ms off an already ~91ms warm query,
  invisible once cached, highest correctness risk (`::int`/lexicographic trap).
  Not worth it.

**Commit 2 (separate) — IMPLEMENTED:**
- **#8 browse fast-path.** When a cross-library `recently_added` browse also has
  an `isPlayed` filter, the over-fetch loop no longer advances `filters.Offset`
  into `BrowsePage`'s whole-catalog GROUP BY on the 2nd chunk. Instead it pulls
  the entire over-fetch budget (`maxScannedRows`) in a single
  `BrowseRecentlyAddedAcrossLibraries` merged index walk
  (`internal/jellycompat/content_direct.go`), so `/Items/Latest` stays on the
  ~1ms/library fast path. Expected ~0.8–1.6s → ~50–150ms for heavy watchers with
  2+ libraries.

## Problem (what operators/users saw)

Production `silo` logs (24h window) show recurring multi-second stalls on the
home screen and continue-watching rails, on both native clients and Jellyfin
compat clients.

- **Continue Watching is the worst offender.** The native `slow section fetch`
  warning fired 132× in 24h (126 of them `type=continue_watching`), with the
  aggregate home fetch (`slow aggregate section fetch`) firing 17× at
  `section_count=32`. Distribution of `slow section fetch`: p50 ≈ 1.1s,
  p90 ≈ 3.8s, worst 35.8s.
- **A very regular hourly spike.** `section_id=compat-resume` reliably jumps to
  ~3.5s clustered at **:12–:14 past every hour**, then returns to fast. This is
  the fingerprint of an ~1h cache TTL expiring followed by a cold recompute that
  a live request pays for.
- **Jellycompat resume/next-up/episodes are slow under field expansion.**
  Excluding long-lived websocket/stream/transcode connections (expected to be
  long), the slowest query-backed endpoints were:
  - the Resume endpoints (`UserItems/Resume` + the per-user
    `Users/{id}/Items/Resume` form) — 275 calls >500ms, worst **35.9s** (a VidHub
    client requesting `Limit=20` with total-record-count).
  - `Shows/NextUp` — 399 >500ms, 139 >1000ms.
  - `Shows/{id}/Episodes` — 58 >500ms, worst **10.1s** (long series, all slow
    calls requested `MediaSources`/`MediaStreams` expansion).
  - the native home-sections aggregate (`api/v1/home` sections) — worst 5.4s.
  - `/api/v1/recommendations/taste-seed/items` — 55 >1000ms, worst 5.6s.

## What is NOT the problem

The base SQL is fast. `EXPLAIN ANALYZE` of the continue-watching base fetch for
the single heaviest user on the box (`user_id=627`, 11,459 in-progress rows)
returns in **~1ms** on the existing partial indexes
(`idx_uwp_profile_in_progress`, `idx_uwp_profile_completed`). The multi-second
cost is application-side: over-scanning, per-section serialization, cold-cache
recompute, and one avoidable large join in the NextUp query.

## Evidence: the NextUp query cost breakdown

`EXPLAIN (ANALYZE, BUFFERS)` of the real `buildListNextUpQuery`
(`internal/catalog/nextup_repo.go:112`) for `user_id=627` executed in **91ms
warm**, and the cost is dominated by one thing:

```text
completed_episodes CTE:
  Nested Loop (actual 60.8ms, 56,884 buffers)
    -> Index Scan idx_user_watch_progress_profile  (14,512 rows, 847 buffers)
    -> Index Scan episodes_pkey  (loops=14,512, 56,037 buffers)   <-- dominant
LATERAL next-episode lookup: fast (~9k buffers total across 137 series)
Execution Time: 91.163 ms  (warm; cold/disk-bound is the 3.5s production case)
```

The `completed_episodes` CTE does
`JOIN episodes e ON e.content_id = uwp.media_item_id` and probes
`episodes_pkey` **once per completed-progress row (14,512×)** purely to read
`series_id`, `season_number`, and `episode_number`. That single join is ~60ms of
91ms warm and is the largest driver of the cold-cache blowup.

## The content_id insight (validated)

`content_id` is a **deterministic, structured natural key**, not a random DB id
(`internal/contentid/contentid.go`, migration
`migrations/sql/20260612130000_deterministic_content_id.sql`). An episode's id
embeds its series anchor, season, and episode:

```text
episode-tvdb-296762-1-5   ->  series = series-tvdb-296762, season = 1, episode = 5
```

This is a **documented, frozen, load-bearing invariant** (`contentid.go:29-32`:
"the watch-history query relies on this to resolve a show without an episodes
table lookup. Never break it."). It is exposed as `SeriesIDFromContentID`
(`contentid.go:256`).

Measured coverage on this DB:
- 2,110,532 of 2,111,778 episode rows (**99.94%**) are provider-anchored and
  derivable; the derived series id matches `series_id` for **100%** of them.
- Only 1,246 rows / 134 series are legacy/`local-` ids with no embedded anchor.
- Of in-progress `user_watch_progress` rows, 86k are `episode-` (derivable) and
  44k are movies/books/other (not episodes at all).

Crucially, **there is already a proven in-repo SQL pattern** for exactly this,
used by the watch-history source to avoid the episodes join
(`internal/catalog/history_source.go:218-282`):
- `seriesFromAnchoredEpisodeExpr` — `'series-' || split_part(id,'-',2) || '-' || split_part(id,'-',3)`.
- `anchoredEpisodePredicate` — requires 5 non-empty `-` components before
  treating an id as anchored.
- Null-poisons the episodes join key for anchored ids
  (`CASE WHEN <anchored> THEN NULL ELSE media_item_id END`) so the planner skips
  the `episodes_pkey` probe, and only legacy/local ids `LEFT JOIN episodes`.

Season/episode numbers are also parseable from the id (last two `-` segments),
so the CTE can obtain **all three** values it needs (`series_id`,
`season_number`, `episode_number`) without touching `episodes` for the 99.94%
anchored majority — while COALESCE-ing to the existing join for the legacy tail.

This makes the NextUp optimization **low-risk and well-precedented**, not novel.

## Proposed fixes (ranked by expected impact)

The ranking below was re-ordered after an adversarial review. The production
pain is cold-cache spikes hitting live requests, so **caching is the highest-
impact lever**, not the query rewrite. The content_id rewrite is a real warm/cold
I/O trim but is demoted and gated on a correctness fix (see #6).

### Tier 1 — highest impact, addresses the actual production symptom

1. **Warm / lengthen the continue-watching cache** so the hourly `:12` cold
   recompute never lands on a live request. Options: background refresh before
   TTL expiry, or a longer TTL with async invalidation on progress write. The
   evidence (base query ~1–91ms, prod 3.5s clustered at `:12–:14` hourly) says
   this single fix most likely kills the headline spike on its own.

2. **Cap the Resume full-history scan.** When `EnableTotalRecordCount=true`,
   `loadProgressPage` (`handlers_items.go:2446-2483`) keeps paging the user's
   entire in-progress history to compute a total — the documented 35.9s path.
   Stop scanning to the end: cap scan depth (e.g. reuse
   `continueProgressMaxScanned`) and return an approximate/clamped total, or omit
   the exact total for oversized histories.

3. **Bound `/Shows/{id}/Episodes`.** `ListBySeries`
   (`internal/catalog/episode_repo.go:684`) returns the whole series with no
   `LIMIT`; paginate it (the worst 10.1s case). Also fix the `episodeRepo == nil`
   fallback that fans out one `ListEpisodes` per season — a genuine N+1
   (`internal/jellycompat/handlers_items.go:2673`).
   - NOTE (review correction): do **not** bother adding a `LIMIT` to
     `listResumableFirstEpisodes` — its input already comes from
     `ListProgress(..., 100, 0)` (`nextup_repo.go:290`), so `ANY($3)` is already
     ≤100 ids. That would be a cosmetic no-op.

### Tier 2 — cheap serialization/dedup wins

4. **Raise/tune `fetchAllMaxConcurrency`** (currently `4`,
   `internal/sections/fetcher.go:51`). With 32 home sections this serializes into
   ~8 waves; total ≈ `ceil(N/4) × slowest-section`. Likely a bigger aggregate-
   latency lever than the NextUp micro-optimization and cheaper. **Measure
   `pgxpool` max conns first** — raising concurrency while cold queries are slow
   amplifies pool pressure.

5. **Avoid double NextUp work in combined mode.** `fetchContinueWatchingSection`
   calls `FetchNextUpItems` inline when `next_up_mode="combined"`
   (`fetcher.go:436`) while the `next_up` section computes it again — compute once
   and share. Also **push dismissal/access filtering into SQL** for
   continue-watching so it stops scanning up to 1000 rows across 10 sequential
   `ListProgress` round-trips to fill a 20-item section (`fetcher.go:498-522`).

### Tier 3 — the NextUp content_id rewrite (warm/cold I/O trim, NOT the spike fix)

6. **Derive series/season/episode from `content_id` in the NextUp CTE instead of
   joining `episodes`.** Confirmed effect: the `completed_episodes` CTE for user
   627 drops from **67.5ms / 56,893 buffers** to **38.8ms / 13,362 buffers** —
   the 56k `episodes_pkey` probes vanish. But this does NOT touch the LATERAL
   (episodes + media_files), which is the legitimately irreducible part and
   becomes the new dominant term, and it does NOT address the cold-cache spike
   that Tier 1 #1 fixes. Keep it, but as an optimization, not the headline.

   **Mandatory correctness requirements (do not skip):**
   - **`::int` casts are required.** `split_part` returns TEXT. The LATERAL
     compares against integer columns `(e2.season_number, e2.episode_number)`, and
     the `DISTINCT ON` orders by `season_number DESC, episode_number DESC`. Mixed
     `(text,text) > (int,int)` raises `operator does not exist: text > integer`;
     forcing both sides to text makes `('1','2') > ('1','10')` return TRUE
     (lexicographic), so NextUp would surface the wrong episode. Cast every derived
     season/episode value to `int`.
   - **This is NOT fully precedented.** `history_source.go:275-282` derives only
     the series-id *string* (never used numerically). The numeric season/episode
     derivation is new and is exactly where the lexicographic landmine lives —
     copying `history_source` verbatim gives the series expr but not the casts.
   - **Fallback for the legacy tail is mandatory.** COALESCE to the episodes join
     for the 0.06% legacy/local (`local-`/Sonyflake) ids using the same
     `anchoredEpisodePredicate` (5 non-empty `-` components) as `history_source.go`.
   - Data validated safe for anchored ids: across all 2,110,532 anchored episode
     rows, derived season/episode match the table columns with **0 mismatches**
     (including season-0/specials); all segments numeric.

### Tier 2 — high-frequency jellycompat browse (`/Items/Latest`, `/Items`)

These are very high traffic — 10,837 `/Items/Latest` + 6,030 `/Items` calls in
24h — so even sub-second slowness is a large aggregate load. 333 `/Items/Latest`
calls ran >800ms (p50 1.1s, max 1.6s); every slow one requested full `Fields`
(`MediaSources`+`MediaStreams`) with `isPlayed=false&groupItems=true`.

Root cause is **not** the detail expansion. It is `isPlayed=false` (a per-profile
overlay that can't be pushed into SQL) forcing `BrowseItems` into an over-fetch
loop that advances `filters.Offset` (`internal/jellycompat/content_direct.go:477`).
The cross-library recently-added **fast path is gated on `Offset == 0`**
(`content_direct.go:430`); as soon as a heavy watcher needs a 2nd chunk it falls
through to the generic `BrowsePage` (`content_direct.go:435`), whose multi-library
plan is a whole-catalog `GROUP BY` HashAggregate + top-N heapsort over ~147k
movies (`internal/catalog/browse.go:544-548`).

`EXPLAIN (ANALYZE, BUFFERS)` of the 2nd-chunk shape (`LIMIT 150 OFFSET 150`,
`type=movie`, full projection): **755ms per call** (HashAggregate over 147,212
rows, top-N heapsort). The offset-0 fast path
(`BrowseRecentlyAddedAcrossLibraries`, index walk on
`idx_item_libraries_folder_seen_content`) is ~1ms/library. So one fall-through =
0.8–1.6s; two (very heavy watchers) reach the top of the range. Only affects
users with 2+ libraries and lots of movie watch history — consistent with "some
calls slow."

8. **Keep the `isPlayed` over-fetch loop on the fast path.** Track a separate
   chunk offset and re-call `BrowseRecentlyAddedAcrossLibraries` for each chunk
   (growing top-N bound) instead of advancing `filters.Offset` into `BrowsePage`
   — or give the multi-library recently-added GROUP BY an offset-capable
   index-ordered plan. Expected: **~0.8–1.6s → ~50–150ms (≈8–10×)**.

9. **Drop the redundant per-item `enrichDetailUserData`** in
   `GetItemDetailsByIDs` (`content_direct.go:693-702` → `:612-614`): it runs a
   single-item `GetProgress` + `ListCompletedHistoryItems` **per item (~100
   sequential point queries for 50 items)**, but the handler already fetched
   progress in one batched call (`resolveUserStateForContentIDs`,
   `handlers_items.go:961`) and `userDataDTO` overrides it (`mapping.go:551-559`),
   so the per-item work is thrown away. Remove or batch it. Expected: shave
   ~50–150ms of pure waste.

### Explicitly out of scope

- **`/api/v1/recommendations/taste-seed/items` (4.4–5.5s, every call).** Confirmed
  it only fires on the explicit `/taste-seed` onboarding page
  (`web/src/pages/TasteSeed.tsx` via `useTasteSeedItems`); the Home screen renders
  only `TasteSeedBanner` (dismissed-state check, no items query). 56 calls / 4
  users in 24h. Rare, opt-in, not on the homescreen — deliberately deferred.

### Ops (not code)

7. **Enable `pg_stat_statements`** — confirmed OFF (`shared_preload_libraries`
   empty). Enabling it makes future regressions measurable, but it **requires a
   Postgres restart** (not a live toggle) — schedule accordingly.

## Risk / follow-ups

- The content_id rewrite's `::int` cast + lexicographic-ordering trap is the
  single biggest correctness gap; gate it on an explicit test (episode 2 vs 10
  within a season, and the `season DESC, episode DESC` tiebreak) before merge.
- After the rewrite, the LATERAL (episodes + media_files) becomes the dominant
  term; measure its cold cost separately — it may warrant its own index review.
- Raising `fetchAllMaxConcurrency` trades DB pool pressure for latency; validate
  against `pgxpool` max conns under real concurrency, not just latency.
- Cache warming changes staleness semantics for continue-watching; confirm
  progress writes still invalidate promptly enough that a just-watched item moves.
- `/Shows/{id}/Episodes` pagination is client-visible (jellycompat); verify
  Android/Apple clients tolerate a bounded page + `startItemId` continuation.

## Verification plan

- `EXPLAIN (ANALYZE, BUFFERS)` before/after on the NextUp rewrite for `user_id=627`;
  assert the `episodes_pkey` loop count drops from ~14.5k to ~0 for anchored users.
- Replay the worst production queries (Resume `Limit=20` + total-count; long-series
  Episodes) and confirm sub-second.
- Watch `slow section fetch` / `slow aggregate section fetch` counts in logs after
  deploy; the hourly `:12` compat-resume spike should disappear.
- Dedicated ordering test for the content_id derivation: a series with episodes
  2 and 10 in one season, asserting NextUp picks episode 2 → 3 (not 10 → 11) and
  the `DISTINCT ON ... DESC` tiebreak picks the highest-numbered completed episode.
- `make lint`, `go build ./...`, targeted unit tests for the content_id SQL
  derivation (mirror existing `history_source` tests, plus the `::int` cast path).
