# 2026-07-06 — Why the slow endpoints stayed slow after PR #292, and the fix plan

Commands assume the repository root is the cwd.

## Context

PR #292 (commit `5606beed`) shipped the home/Continue Watching/Latest latency work:
scan caps in the jellycompat resume path, the shared resolved-list cache, the
per-library Latest fast path, batched presign, and the plugin-installation cache.
The image built from it has been serving production for a full day, and the
deployment's request logs still show the same endpoint groups breaching 1s
(analysis window: 19h post-deploy, `log_min_duration_statement = 500ms` on the
Postgres side):

| Endpoint group | slow (≥1s) calls | p95 | worst |
|---|---|---|---|
| `/Shows/NextUp` | 301 | 17.1s | 44.8s |
| `/UserItems/Resume` + `Users/{userId}/Items/Resume` | 121 | 34.8s / 110.7s | 125.3s |
| `/Items/Latest` + `Users/{id}/Items/Latest` | 117 | 17.3s / 8.3s | 18.8s |
| `/Items` (incl. search-as-you-type) | 83 | 6.3s | 9.6s |
| native home sections + section items (`api/v1/home` routes) | 50 | 6.3s / 42.0s | 42.8s |

The deployed binary provably contains the PR #292 symbols, so the caps and caches
are live. They bounded *how many rows the loops touch* — they did not touch *what
each underlying query costs*. The Postgres slow-statement log for the same window
shows where the time actually goes:

| Statement | slow execs | total time | worst |
|---|---|---|---|
| `ListProgress(status="in_progress")` page query | 5,263 | 13,522s | 16.8s |
| Next-up `WITH completed_episodes …` CTE | 648 | 1,717s | 44.7s |
| `RemoveHistoryItems` history DELETE | 81 | 118s | 10.9s |
| Scanner `media_files` subtree lookup | 27 | 74s | 13.6s |
| `ListCompletedHistoryItems` (chunked rollup) | 65 | 64s | 1.6s |

## Root cause 1 — 4.32M stale "completed but resumable" progress rows + no index for the resume ordering

**Human-readable.** For accounts that bulk-imported their Plex watch history, every
imported "watched" row was stored as *finished AND parked at the very end of the
video*. The server's definition of "something you can resume" is "position greater
than zero" — deliberately, so a rewatch of an already-watched item re-enters
Continue Watching. Result: for a heavy importer, the server believes their **entire
watch history (232,979 of 233,016 rows for the worst profile) is resumable**. Every
Continue Watching page load walks that entire list, filters ~100% of it away in
memory, pages deeper, and repeats.

**Technical.** All current write paths enforce the invariant "completed ⇒
`position_seconds = 0`" (`internal/userstore/pgstore/progress.go`,
`internal/historyimport/repo.go:879`). But 4,318,693 of 4,579,210
`user_watch_progress` rows (94%, across 1,649 profiles) violate it with
`completed = TRUE AND position_seconds > 0` — legacy imports from before the
invariant. Only 189 of those are genuine mid-rewatch rows
(`position_seconds < duration_seconds`); the rest are parked at/past the end and
can never be a meaningful resume point.

The `in_progress` branch of `ListProgress`
(`internal/userstore/pgstore/progress.go:426`) filters
`position_seconds > 0 … ORDER BY updated_at DESC LIMIT … OFFSET …`. No index
serves that shape (the partial indexes cover `completed = true/false`, not
`position_seconds > 0`), so **every call walks all of the profile's rows via
`idx_user_watch_progress_profile` and top-N-sorts them** — measured 192ms warm /
multi-second cold per call for the worst profile (EXPLAIN ANALYZE: 232,962 rows
walked per call).

Every consumer loops this query per request:

- `internal/sections/fetcher.go` `collectContinueProgressItems`: up to 10 pages
  (`continueProgressMaxScanned = 1000` / page size 100) — serves the native
  Continue Watching section (the `api/v1/home` section-items route, 42s) **and**,
  since PR #292, the jellycompat Resume fast path (`loadResumeViaSections`) — the
  125s `Users/{userId}/Items/Resume` calls.
- `internal/jellycompat/handlers_items.go` `loadProgressPage`: up to
  `resumeScanMaxRows = 300` rows per request (the PR #292 cap — it bounds pages,
  not per-page cost).
- `internal/catalog/nextup_repo.go` `listResumableFirstEpisodes`: one 100-row call.

The 00:30 UTC log window shows the failure shape directly: ~17 sequential 1.1s
executions of this query (one paging loop) plus 16.5s cold executions saturating
the pool while other endpoints queue behind them.

**Fix (one-time direct DB repair — applied 2026-07-06, no migration shipped).**

Because this is a one-shot repair of legacy data on a single deployment, it was
applied directly against the production database instead of as a Goose
migration; the exact SQL and timings are recorded in the deployment's ops notes
(silo-base, `slow-endpoint-db-repair-2026-07-06.md`).

1. Data repair: `UPDATE user_watch_progress SET position_seconds = 0 WHERE
   completed = TRUE AND position_seconds > 0 AND position_seconds >=
   duration_seconds`, followed by `ANALYZE user_watch_progress`. Leaves the 189
   genuine mid-rewatch rows alone; does not touch `updated_at` or `synced_seq`
   (no sync flood, ordering preserved). These rows were already invisible in
   Continue Watching (dismissal/superseded/percent filtering), so no visible
   behavior changes — the data just stops lying to the query planner. Applied:
   4,318,504 rows in 2m05s.
2. Partial index `idx_uwp_profile_resume (user_id, profile_id, updated_at DESC)
   WHERE position_seconds > 0` (`CREATE INDEX CONCURRENTLY`) — turns every
   in-progress listing into an ordered index walk regardless of profile size
   (belt-and-braces against future bad data; after the repair the index is
   8.8 MB). Verified post-repair: the worst profile's in-progress page query
   went from 232,962 rows walked / 192ms warm to 37 rows / 1.0ms.

## Root cause 2 — Next Up anchors on the entire completed history

**Human-readable.** "Next Up" answers "what's the next episode of each show this
person is watching?". To find those shows it re-reads **every episode the person
has ever finished** — a quarter-million rows for bulk importers — on every call,
then for each fully-watched show walks all of its episodes looking for an
unwatched one that isn't there.

**Technical.** `buildListNextUpQuery` (`internal/catalog/nextup_repo.go`): the
`completed_episodes` CTE does `DISTINCT ON (e.series_id)` over **all** of the
profile's completed rows joined to `episodes` (233k rows for the worst profile),
then `eligible_series` runs a correlated anti-join against the (currently
non-selective, see RC1) `position_seconds > 0` set, then a per-series LATERAL
probes episodes in order — scanning *every* episode of a fully-watched series
before yielding nothing. 648 slow executions, 44.7s worst. This also drags down
the native home-sections aggregate (`api/v1/home` sections) via `maybeInjectNextUp` and the jellycompat
`/Shows/NextUp` route.

**Fix.** Bound the anchor for the global (non-`SeriesID`) query: a
`recent_completed` pre-CTE takes the most recent `nextUpAnchorMaxRows = 500`
completed rows via `idx_uwp_profile_completed` (an ordered index walk), and
`completed_episodes` derives series from that subset. A Next Up rail shows ~24
series; the 500 most recent completions cover every realistically surfaceable
series. Series-scoped calls (show-detail tile) keep the unbounded shape — they
are naturally bounded by one series. Prototyped on the live worst profile:
**44.7s → 517ms** (and the residual cost is the RC1 anti-join, which the repair
removes).

## Root cause 3 — series watch-state rollup materializes every episode of every series on the page

**Human-readable.** For any list of TV shows (per-library "Latest", library
browse, search results), the server computes each show's "N unwatched episodes"
badge by **loading every episode of every show on the page into memory** and then
asking the watch database about each episode in batches of 500. One 50-show page
of the Sports library expands to 32,467 episodes and ~65 sequential database
round-trips. PR #292 wired the cached Latest fast path through this same rollup
(for data parity), so even cache-hit responses pay it.

**Technical.** `enrichSeriesListUserData` (`internal/jellycompat/content_direct.go`)
→ `episodeRepo.ListBySeriesIDs` (all episodes) → `chunkedProgressByMediaItems` →
`ListProgressWithCompletedHistory` per 500 ids (each chunk hits
`user_watch_progress` + the `ListCompletedHistoryItems` GROUP BY). The same
per-episode fanout runs in `enrichDetailUserData` for every series row on detail
pages. It only ever produces four numbers per series (total/watched/in-progress/
played).

**Fix.** Compute the counts in one SQL aggregate. New optional interface
`userstore.SeriesEpisodeRollupStore`, implemented by `PostgresUserStore`
(the pgstore already references catalog tables — see
`buildProgressCatalogFilter`): a single `GROUP BY e.series_id` query over
`episodes` LEFT-JOINed to the profile's progress rows with the same
visibility/history semantics as `ListProgressWithCompletedHistory`
(hidden-items anti-join, completed-history fold). `enrichSeriesListUserData` and
`enrichDetailUserData` use it when the store implements it and keep the existing
chunked path as fallback (SQLite-backed user stores). Prototyped on the live
worst profile against the real Sports Latest page: **~17s → 123ms**.

This also fixes the slow `/Items?searchTerm=…` calls (Meilisearch itself is fast;
the search handler excludes movies/episodes, returns mostly series, and then paid
this same rollup) and the series portions of `/Items` browse.

## Root cause 4 (secondary) — two index-starved write/maintenance paths

- `RemoveHistoryItems` (`internal/userstore/pgstore/progress.go`): the watermark
  MAX and the DELETE filter `user_watch_history` by `(user_id, profile_id,
  media_item_id = ANY(...))`, but the only complete index is `(user_id,
  profile_id, watched_at DESC)` — per-user full history scans (10.9s worst; this
  is the `/UserPlayedItems/{itemId}` DELETE path, 19.4s worst end-to-end).
  Fix: plain btree `(user_id, profile_id, media_item_id)`.
- Scanner subtree lookups (`internal/scanner/file_repo.go`):
  `media_folder_id = $1 AND (file_path = $2 OR file_path LIKE $3 ESCAPE '\')` has
  no usable index for the path predicate (13.6s worst; holds pool connections that
  API requests then queue behind). The LIKE is prefix-anchored
  (`pathscope.PrefixLike`), so a btree on `(media_folder_id, file_path
  text_pattern_ops)` serves both arms and the `ORDER BY file_path`.

Both indexes were created directly on the production database
(`CREATE INDEX CONCURRENTLY`) alongside RC1's partial index — see the
deployment's ops notes.

## What is intentionally not changed

- `resumeScanMaxRows` / `continueProgressMaxScanned` / `maxSeriesUserDataRollups`
  caps stay — they remain correct guards; the fixes make each capped unit cheap.
- The `position_seconds > 0` rewatch semantics stay; the repair only removes rows
  that violate the documented write-path invariant.
- No `updated_at`/`synced_seq` changes in the repair — client sync state is
  untouched.
- `/api/v1/stream/{session_id}/subtitles/{track}` (subtitle *track* conversion)
  is out of scope per the task. The related font-attachment endpoint
  `/subtitles/{track}/fonts` *was* folded in as a follow-up — see deliverable 5 —
  because its per-attachment ffmpeg spawns shared the same slow-endpoint profile.

## Deliverables

1. `docs:` this plan.
2. One-time DB repair + three indexes, applied directly to the production
   database on 2026-07-06 (recorded in the deployment's ops notes; deliberately
   not shipped as a migration).
3. `perf(catalog):` bounded next-up anchor scan.
4. `perf(jellycompat,userstore):` SQL series watch-state rollup with chunked
   fallback.
5. `perf(playback):` single-pass ffmpeg font extraction for
   `/subtitles/{track}/fonts` (one media-file open instead of one per
   attachment), with the 32-attachment / 32 MiB caps preserved via a dump-dir
   watchdog.

## Verification

- `go build ./... && go vet ./...`; `go test -race` on `internal/catalog`,
  `internal/jellycompat`, `internal/userstore/...`, `internal/sections`.
- EXPLAIN ANALYZE numbers above were measured on the production database
  against the worst real profile, before and after the repair: in-progress page
  query 192ms/232,962 rows → 1.0ms/37 rows; deployed (unbounded) next-up shape
  17–44s → 1.1s; bounded next-up shape → 10ms; 50-series rollup ~17s → 119ms.
- Post-deploy of the code fixes: re-run the slow-request aggregation over
  `docker logs` and confirm the five endpoint groups drop out of the ≥1s report.
